package cmdq

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/jay-snyder/treewright/internal/ui"
)

// The list: the queue drawn as a screen of its own, which the person moves
// through and runs commands from.
//
// It is not internal/ui's picker, which numbers single-line rows and must
// never be handed a newline; an entry here is several lines. It is split into
// two halves so that the half that decides things can be tested without a
// terminal. The layout and the view — which lines belong to which entry, where
// the view sits, which entry is picked after a refresh, what a key does — are
// plain functions of the entries, the size and the key. Screen is the other
// half: it reads the queue, draws, reads keys and runs what Enter asks for.

// key is one key the list understands, as the keyboard decoded it. A printable
// key is itself; the rest are named.
type key string

const (
	keyNone  key = "" // a key the list ignores, or no key: a refresh is due
	keyUp    key = "up"
	keyDown  key = "down"
	keyPgUp  key = "pgup"
	keyPgDn  key = "pgdn"
	keyHome  key = "home"
	keyEnd   key = "end"
	keyEnter key = "enter"
	keyEsc   key = "esc"
	keyQuit  key = "quit" // Ctrl+C, which closes the list from anywhere
	keyEOF   key = "eof"
)

// escapeWait is how long a lone ESC waits for the rest of a sequence. Long
// enough for a terminal over ssh to deliver the rest of what one key sends,
// short enough that Escape on its own does not feel slow.
const escapeWait = 30 * time.Millisecond

// key reads the next key from what is already pending, waiting only for the
// rest of an escape sequence. With nothing pending it returns keyNone.
func (k *Keyboard) key() key {
	b, ok := k.next(0)
	if !ok {
		if k.eof {
			return keyEOF
		}
		return keyNone
	}
	switch b {
	case '\r', '\n':
		return keyEnter
	case 0x03:
		return keyQuit
	case 0x1b:
		return k.escape()
	}
	if b < 0x20 || b == 0x7f {
		// Ctrl+Z among them: in raw mode it is a byte rather than a stop
		// signal, and a stop would leave the popup with no program in it.
		return keyNone
	}
	if b >= 0x80 {
		// The rest of a character no key is bound to.
		for len(k.pending) > 0 && k.pending[0]&0xc0 == 0x80 {
			k.pending = k.pending[1:]
		}
		return keyNone
	}
	return key(string(rune(b)))
}

// escape reads the rest of a key that began with ESC, a byte at a time so a
// key pressed straight after it is left for the next read.
func (k *Keyboard) escape() key {
	b, ok := k.next(escapeWait)
	if !ok {
		return keyEsc
	}
	if b != '[' && b != 'O' {
		return keyNone // Alt and a key: nothing here is bound to one
	}
	seq := string(rune(b))
	for len(seq) < 6 {
		c, ok := k.next(escapeWait)
		if !ok {
			break
		}
		seq += string(rune(c))
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '~' {
			break
		}
	}
	switch seq {
	case "[A", "OA":
		return keyUp
	case "[B", "OB":
		return keyDown
	case "[5~":
		return keyPgUp
	case "[6~":
		return keyPgDn
	case "[H", "OH", "[1~":
		return keyHome
	case "[F", "OF", "[4~":
		return keyEnd
	}
	return keyNone
}

// ---- layout ------------------------------------------------------------------

// layout is the entries laid out as the lines of the list, and which lines
// each entry owns. A line owned by no entry — the gap between two — is owned
// by -1.
type layout struct {
	lines  []string
	owner  []int
	starts []int
	ends   []int
}

func (l *layout) add(line string, owner int) {
	l.lines = append(l.lines, line)
	l.owner = append(l.owner, owner)
}

// marginWidth is what the list keeps beside an entry's lines: the bar that
// marks the pick and a space on its left, and two columns on its right.
const marginWidth = 4

// lay lays the entries out for a screen cols wide. gone says which entries'
// directories have gone, for the ones still waiting.
//
// Who asked and when on the first line, cut to fit; how its last run went or
// how it left the list on the next; the reason, wrapped at spaces; the
// command, wrapped hard so every character of it stays where it is, with $ in
// front of its first line and > in front of the lines after; and where it
// runs. Everything that came from somebody else goes through Visible first,
// and is wrapped as the stand-ins it became.
func lay(entries []Entry, gone []bool, cols int, now time.Time, color bool) layout {
	width := max(8, cols-marginWidth)
	var l layout
	for n, e := range entries {
		if n > 0 {
			l.add("", -1)
		}
		l.starts = append(l.starts, len(l.lines))

		note := " · queued " + Ago(e.Added, now)
		from := cutRunes(Visible(e.From), width-utf8.RuneCountInString(note))
		l.add(paint(from, ui.Bold, color)+ui.Dim.Apply(note, color), n)
		switch {
		case e.InHistory():
			c := ui.Dim
			if e.Outcome == Ran {
				c = ui.Green
			}
			l.add(paint(cutRunes(Describe(e, now), width), c, color), n)
		case gone[n]:
			l.add(paint(cutRunes("its directory is gone — d dismisses it", width), ui.Yellow, color), n)
		case !e.Failed.IsZero():
			l.add(paint(cutRunes(LastFailure(e, now), width), ui.Red, color), n)
		}

		for _, line := range wrapWords(Visible(e.Why), width) {
			l.add(paint(line, ui.Plain, color), n)
		}
		for i, line := range strings.Split(Visible(e.Command), "\n") {
			mark := "> "
			if i == 0 {
				mark = "$ "
			}
			for j, part := range wrapRunes(line, width-2) {
				lead := ui.Dim.Apply(mark, color)
				if j > 0 {
					lead = "  "
				}
				l.add(lead+paint(part, ui.Plain, color), n)
			}
		}
		for _, line := range wrapRunes("in "+Visible(e.Dir), width) {
			l.add(paint(line, ui.Dim, color), n)
		}
		l.ends = append(l.ends, len(l.lines)-1)
	}
	return l
}

// paint colors a line of visible text, putting each stand-in in reverse video
// rather than in the line's color — and leaving it as it is with color off,
// where the stand-in's own shape is what shows it.
func paint(s string, c ui.Color, color bool) string {
	if !color {
		return s
	}
	var b strings.Builder
	run := strings.Builder{}
	flush := func() {
		b.WriteString(c.Apply(run.String(), true))
		run.Reset()
	}
	for _, r := range s {
		if isStandIn(r) {
			flush()
			b.WriteString(ui.Reverse.Apply(string(r), true))
			continue
		}
		run.WriteRune(r)
	}
	flush()
	return b.String()
}

// wrapWords wraps s at width, breaking after the last space that fits where
// there is one. The spaces stay at the end of their line, so the lines put
// back together are s exactly.
func wrapWords(s string, width int) []string {
	r := []rune(s)
	var lines []string
	for len(r) > width {
		cut := width
		if i := lastSpace(r[:width]); i > 0 {
			cut = i + 1
		}
		lines = append(lines, string(r[:cut]))
		r = r[cut:]
	}
	return append(lines, string(r))
}

func lastSpace(r []rune) int {
	for i, c := range slices.Backward(r) {
		if c == ' ' {
			return i
		}
	}
	return -1
}

// wrapRunes wraps s at width hard: every character stays where it is, which
// is what a command needs, since a space a wrap swallowed is a different
// command.
func wrapRunes(s string, width int) []string {
	width = max(1, width)
	r := []rune(s)
	var lines []string
	for len(r) > width {
		lines = append(lines, string(r[:width]))
		r = r[width:]
	}
	return append(lines, string(r))
}

// cutRunes shortens s to at most n runes.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if n < 0 {
		n = 0
	}
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---- the view ----------------------------------------------------------------

// mode is which of the two lists is showing.
type mode int

const (
	waitingList mode = iota
	historyList
)

// moved is what last moved the view, which decides how it is placed: a pick
// shows the whole of the picked entry, a scroll moves the pick to what is on
// screen, and a refresh holds the view still unless the pick slipped off it.
type moved int

const (
	byPick moved = iota
	byScroll
	byRefresh
)

// action is what a key asks the screen to do with an entry. The view decides;
// the screen does, since doing means the store and the terminal.
type action int

const (
	nothing action = iota
	runIt
	dismissIt
	restoreIt
	closeList
)

// view is where the list stands: what is shown, which entry is picked, and
// which line is at the top.
type view struct {
	mode  mode
	shown []Entry
	gone  []bool
	lay   layout
	sel   int
	top   int
	rows  int
	cols  int
	moved moved

	// lost is a pick whose entry left the list somewhere else — another popup
	// ran or dismissed it — which Enter will not act on until the person
	// picks again: the entry that slid into its place is one they never
	// chose. leftHere is the same departure caused here, by Enter, d or u,
	// which is no surprise and needs no fresh pick.
	lost     bool
	leftHere bool

	// note is a line the last key earned, said in place at the foot of the
	// list until the next key.
	note string
}

// The lines of the screen that are not entries: the title, the gap under
// it, and the line at the foot.
const chromeLines = 3

func (v *view) body() int { return max(1, v.rows-chromeLines) }

// refresh lays out a fresh read of the queue, keeping the pick on the entry it
// was on rather than on the row.
func (v *view) refresh(entries []Entry, gone []bool, rows, cols int, now time.Time, color bool) {
	picked, wasAt := "", 0
	if v.sel < len(v.shown) && v.sel < len(v.lay.starts) {
		picked, wasAt = v.shown[v.sel].ID, v.lay.starts[v.sel]
	}
	v.shown, v.gone, v.rows, v.cols = entries, gone, rows, cols
	v.lay = lay(entries, gone, cols, now, color)
	if picked != "" {
		found := false
		for j, e := range entries {
			if e.ID != picked {
				continue
			}
			v.sel, found = j, true
			// A view scrolled down moves with the pick, so the entry holds
			// still on screen. One at the top stays there, to show what was
			// queued above it.
			if v.top > 0 {
				v.top += v.lay.starts[j] - wasAt
			}
			break
		}
		if !found && !v.leftHere {
			v.lost = true
		}
	}
	v.leftHere = false
	v.place()
}

// place puts the view where what moved it says it goes.
func (v *view) place() {
	count, body := len(v.shown), v.body()
	if count == 0 {
		v.sel, v.top = 0, 0
		return
	}
	v.sel = min(max(v.sel, 0), count-1)
	v.top = max(0, min(v.top, len(v.lay.lines)-body))
	start := v.lay.starts[v.sel]
	switch v.moved {
	case byPick:
		v.show()
	case byRefresh:
		if start < v.top || start >= v.top+body {
			v.show()
		}
	case byScroll:
		if start >= v.top && start < v.top+body {
			return
		}
		// The first entry that starts on screen, or one taller than the
		// screen that fills it.
		for i := range count {
			if v.lay.starts[i] >= v.top && v.lay.starts[i] < v.top+body {
				v.sel = i
				return
			}
		}
		for i := range count {
			if v.lay.ends[i] >= v.top {
				v.sel = i
				return
			}
		}
	}
}

// show brings the whole of the picked entry on screen, or as much of it from
// its start as fits.
func (v *view) show() {
	start, end, body := v.lay.starts[v.sel], v.lay.ends[v.sel], v.body()
	if start < v.top {
		v.top = start
	} else if end >= v.top+body {
		v.top = min(end-body+1, start)
	}
}

// onScreen reports whether the picked entry is wholly on screen, or as much of
// it as could be: one taller than the list is as on screen as it gets.
func (v *view) onScreen() bool {
	start, end, body := v.lay.starts[v.sel], v.lay.ends[v.sel], v.body()
	if end-start >= body {
		return true
	}
	return start >= v.top && end < v.top+body
}

// The notes a key can earn. Spelled once, because the tests read them back.
const (
	noteLost      = "the entry you picked has left the list — pick again with ↑↓"
	noteScrolled  = "it is all on screen now — Enter runs it"
	noteGone      = "not run — its directory is gone, d dismisses it"
	noteNothing   = "nothing waiting"
	noteNoHistory = "nothing in the history yet"
)

// press decides what a key does, and returns what the screen is to do about
// it. Only Enter on a picked, whole, runnable entry returns runIt: that is
// the one way anything here runs.
func (v *view) press(k key) action {
	v.note = ""
	count := len(v.shown)
	pick := func(sel int) {
		v.sel, v.moved, v.lost = sel, byPick, false
	}
	switch k {
	case keyUp, "k":
		pick(max(v.sel-1, 0))
	case keyDown, "j":
		pick(min(v.sel+1, max(count-1, 0)))
	case keyHome:
		pick(0)
	case keyEnd:
		pick(max(count-1, 0))
	case keyPgDn, " ":
		v.top, v.moved, v.lost = v.top+v.body(), byScroll, false
	case keyPgUp:
		v.top, v.moved, v.lost = v.top-v.body(), byScroll, false
	case keyEnter:
		if v.mode != waitingList || count == 0 {
			return nothing
		}
		if v.lost {
			v.note = noteLost
			return nothing
		}
		if v.gone[v.sel] {
			v.note = noteGone
			return nothing
		}
		// Only what the person could see runs: an entry partly scrolled away
		// comes into view first, and the next Enter runs it.
		if !v.onScreen() {
			v.moved, v.note = byPick, noteScrolled
			return nothing
		}
		return runIt
	case "d":
		if v.mode != waitingList || count == 0 {
			return nothing
		}
		if v.lost {
			v.note = noteLost
			return nothing
		}
		return dismissIt
	case "u":
		if v.mode != historyList || count == 0 {
			return nothing
		}
		if v.lost {
			v.note = noteLost
			return nothing
		}
		return restoreIt
	case "h":
		v.toggle()
	case "q", keyEsc:
		if v.mode == historyList {
			v.toggle()
			return nothing
		}
		return closeList
	case keyQuit, keyEOF:
		return closeList
	case keyNone:
	}
	return nothing
}

func (v *view) toggle() {
	if v.mode == waitingList {
		v.mode = historyList
	} else {
		v.mode = waitingList
	}
	v.sel, v.top, v.moved, v.lost, v.shown = 0, 0, byPick, false, nil
}

// draw is the whole screen, from the top-left corner: written over what was
// there rather than after clearing it, so a redraw every few seconds does not
// flicker.
func (v *view) draw(color bool) string {
	title, keys := "waiting to run", "↑↓ pick  Enter run  d dismiss  h history  q close"
	if v.mode == historyList {
		title, keys = "history", "↑↓ pick  u back to waiting  h back"
	}
	title += fmt.Sprintf(" · %d", len(v.shown))
	// Whole hints drop off the end until the line fits.
	room := v.cols - utf8.RuneCountInString(title) - 6
	for utf8.RuneCountInString(keys) > room && strings.Contains(keys, "  ") {
		keys = keys[:strings.LastIndex(keys, "  ")]
	}
	if utf8.RuneCountInString(keys) > room {
		keys = ""
	}

	var b strings.Builder
	const eol = "\x1b[K\r\n"
	b.WriteString("\x1b[H  " + ui.Bold.Apply(title, color) + "   " + ui.Dim.Apply(keys, color) + eol + eol)
	if len(v.shown) == 0 {
		empty := noteNothing
		if v.mode == historyList {
			empty = noteNoHistory
		}
		b.WriteString("  " + empty + eol)
	}
	for row := 0; row < v.body() && len(v.shown) > 0; row++ {
		i := v.top + row
		if i >= len(v.lay.lines) {
			break
		}
		lead := "  "
		if v.lay.owner[i] == v.sel {
			lead = ui.Cyan.Apply("▌", color) + " "
		}
		b.WriteString(lead + v.lay.lines[i] + eol)
	}
	b.WriteString("\x1b[J")

	foot := ""
	if v.top > 0 {
		foot += "↑ more above  "
	}
	if v.top+v.body() < len(v.lay.lines) {
		foot += "↓ more below (PgDn)"
	}
	if v.lost {
		foot = noteLost
	}
	if v.note != "" {
		foot = v.note
	}
	if foot != "" {
		fmt.Fprintf(&b, "\x1b[%d;1H  %s\x1b[K", v.rows, ui.Dim.Apply(cutRunes(foot, v.cols-2), color))
	}
	return b.String()
}

// ---- the screen --------------------------------------------------------------

// Screen is the list on a terminal.
type Screen struct {
	Store Store
	Keys  *Keyboard
	Out   io.Writer

	// Term is the terminal the list is drawn on, whose size it follows and
	// hands on to each command it runs. Resized says that size changed.
	Term    *os.File
	Resized <-chan os.Signal

	// Size stands in for asking Term, in the tests, which have none.
	Size func() (rows, cols int)

	Color bool
	Every time.Duration // how often the queue is read again
	Now   func() time.Time

	// run runs an entry, and is Run.Do outside the tests.
	run func(Run) (Result, error)
}

// The terminal's own controls the screen uses, named for reading.
const (
	altScreenOn  = "\x1b[?1049h"
	altScreenOff = "\x1b[?1049l"
	cursorOff    = "\x1b[?25l"
	cursorOn     = "\x1b[?25h"
	clearScreen  = "\x1b[2J\x1b[H"
)

// drainQuiet is how long the keyboard must be still before the keys typed
// during a run count as drained.
const drainQuiet = 50 * time.Millisecond

// Show runs the list until the person closes it. The terminal is the
// caller's to put in raw mode, and to put back.
func (s Screen) Show() error {
	if s.run == nil {
		s.run = Run.Do
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Every <= 0 {
		s.Every = 5 * time.Second
	}
	if s.Size == nil {
		s.Size = s.termSize
	}
	fmt.Fprint(s.Out, altScreenOn+cursorOff+clearScreen)
	defer fmt.Fprint(s.Out, cursorOn+altScreenOff)

	ticker := time.NewTicker(s.Every)
	defer ticker.Stop()
	v := view{mode: waitingList, moved: byPick}
	for {
		entries, err := s.read(v.mode)
		if err != nil {
			return err
		}
		rows, cols := s.Size()
		v.refresh(entries, s.gone(v.mode, entries), rows, cols, s.Now(), s.Color)
		fmt.Fprint(s.Out, v.draw(s.Color))
		v.moved = byRefresh

		switch v.press(s.nextKey(ticker.C)) {
		case nothing:
		case closeList:
			return nil
		case runIt:
			if !s.runEntry(v.shown[v.sel]) {
				return nil
			}
			v.leftHere, v.moved = true, byPick
		case dismissIt:
			_ = s.Store.Retire(v.shown[v.sel].ID, Dismissed, s.Now())
			v.leftHere, v.moved = true, byPick
		case restoreIt:
			_ = s.Store.Restore(v.shown[v.sel].ID)
			v.leftHere, v.moved = true, byPick
		}
	}
}

func (s Screen) read(m mode) ([]Entry, error) {
	if m == historyList {
		return s.Store.History()
	}
	return s.Store.Pending()
}

// gone says, for each waiting entry, whether its directory has gone — a
// worktree removed since the command was queued in it. Asked at every
// refresh, so the mark comes and goes with the directory.
func (s Screen) gone(m mode, entries []Entry) []bool {
	gone := make([]bool, len(entries))
	if m == historyList {
		return gone
	}
	for i, e := range entries {
		info, err := os.Stat(e.Dir)
		gone[i] = err != nil || !info.IsDir()
	}
	return gone
}

// nextKey waits for a key, or for the next refresh or a resize, which return
// keyNone so the loop draws again.
func (s Screen) nextKey(tick <-chan time.Time) key {
	if len(s.Keys.pending) == 0 && !s.Keys.eof {
		select {
		case chunk, open := <-s.Keys.chunks:
			if !open {
				s.Keys.eof = true
				return keyEOF
			}
			s.Keys.pending = append(s.Keys.pending, chunk...)
		case <-tick:
			return keyNone
		case <-s.Resized:
			return keyNone
		}
	}
	return s.Keys.key()
}

// runEntry runs one entry, from the screen it was picked on: the command as
// drawn, not as it is on disk now. It reports false when the person closed
// the list from the prompt that follows.
func (s Screen) runEntry(e Entry) bool {
	fmt.Fprint(s.Out, altScreenOff+clearScreen+cursorOn)
	fmt.Fprint(s.Out, s.header(e))

	result, err := s.run(Run{
		Command: e.Command,
		Dir:     e.Dir,
		Keys:    s.Keys,
		Screen:  s.Out,
		Size:    s.winsize,
		Resized: s.Resized,
	})

	rule := ui.Dim.Apply(strings.Repeat("─", 40), s.Color)
	fmt.Fprint(s.Out, "\r\n"+rule+"\r\n")
	switch {
	case err != nil:
		fmt.Fprintf(s.Out, "%s · it stays on the list\r\n", ui.Red.Apply("not run: "+Visible(err.Error()), s.Color))
	case result.Status == 0:
		// The output goes with the entry before it moves, so a wait that sees
		// the move finds the lines already there.
		_ = s.Store.KeepOutput(e.ID, result.Output)
		said := "done, moved to the history"
		if errors.Is(s.Store.Retire(e.ID, Ran, s.Now()), ErrNoEntry) {
			said = "it had already left the list"
		}
		fmt.Fprintf(s.Out, "%s · %s\r\n", ui.Green.Apply("exit 0", s.Color), said)
	default:
		_ = s.Store.KeepOutput(e.ID, result.Output)
		_ = s.Store.RecordFailure(e.ID, result.Status, s.Now())
		fmt.Fprintf(s.Out, "%s · it stays on the list\r\n", ui.Red.Apply(fmt.Sprintf("exit %d", result.Status), s.Color))
	}
	// Drained before the prompt is drawn, so that a key pressed in answer to
	// the prompt is never mistaken for one typed at the command.
	s.Keys.Drain(drainQuiet)
	fmt.Fprint(s.Out, ui.Dim.Apply("any key: back to the list   q: close", s.Color))
	k := s.Keys.waitKey()
	fmt.Fprint(s.Out, altScreenOn+cursorOff+clearScreen)
	return k != "q" && k != keyQuit && k != keyEOF
}

// termSize is the terminal's size, or a conventional one when it cannot be
// read.
func (s Screen) termSize() (rows, cols int) {
	if s.Term != nil {
		if cols, rows, err := term.GetSize(int(s.Term.Fd())); err == nil && rows > 0 && cols > 0 {
			return rows, cols
		}
	}
	return 24, 80
}

// winsize is the terminal's size as a command's terminal is given one.
func (s Screen) winsize() (*unix.Winsize, bool) {
	if s.Term == nil {
		return nil, false
	}
	ws, err := unix.IoctlGetWinsize(int(s.Term.Fd()), unix.TIOCGWINSZ)
	return ws, err == nil
}

// waitKey waits as long as it takes for one key.
func (k *Keyboard) waitKey() key {
	for len(k.pending) == 0 {
		if !k.fill(time.Hour) && k.eof {
			return keyEOF
		}
	}
	return k.key()
}

// header is what a run is shown under: who asked, where it runs, and the
// command, through Visible like everything else somebody else wrote.
func (s Screen) header(e Entry) string {
	var b strings.Builder
	b.WriteString(paint(Visible(e.From), ui.Bold, s.Color) + ui.Dim.Apply(" asked for this · in ", s.Color) + paint(Visible(e.Dir), ui.Dim, s.Color) + "\r\n")
	for i, line := range strings.Split(Visible(e.Command), "\n") {
		mark := "> "
		if i == 0 {
			mark = "$ "
		}
		b.WriteString(ui.Dim.Apply(mark, s.Color) + paint(line, ui.Plain, s.Color) + "\r\n")
	}
	b.WriteString(ui.Dim.Apply(strings.Repeat("─", 40), s.Color) + "\r\n")
	return b.String()
}
