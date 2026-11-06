package cmdq

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// entry is a waiting entry for the layout and view tests, which never touch
// the store.
func entry(id, command string) Entry {
	return Entry{ID: id, Command: command, From: "storefront", Why: "a reason", Dir: "/src/storefront", Added: time.Now()}
}

// plain lays entries out with color off and no directory gone.
func plain(entries []Entry, cols int) layout {
	return lay(entries, make([]bool, len(entries)), cols, time.Now(), false)
}

// owned is the lines entry n was laid out as.
func owned(l layout, n int) []string {
	return l.lines[l.starts[n] : l.ends[n]+1]
}

func TestAnEntryIsLaidOutAsWhoWhyWhatAndWhere(t *testing.T) {
	e := entry("1", "sudo installer -pkg ./toolchain.pkg -target /\nsudo xcodebuild -license accept")
	e.From, e.Why, e.Dir = "storefront-eng-2318", "install the toolchain the build now pins", "/src/storefront-eng-2318"
	got := owned(plain([]Entry{e}, 80), 0)
	want := []string{
		"storefront-eng-2318 · queued just now",
		"install the toolchain the build now pins",
		"$ sudo installer -pkg ./toolchain.pkg -target /",
		"> sudo xcodebuild -license accept",
		"in /src/storefront-eng-2318",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("laid out as\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestACommandWrapsHardAndAReasonAtSpaces: a command's characters all stay
// where they are, since a space a wrap swallowed would be a different command,
// while a reason may break between words.
func TestACommandWrapsHardAndAReasonAtSpaces(t *testing.T) {
	e := entry("1", "echo aaaa bbbb cccc dddd")
	e.Why = "one two three four"
	lines := owned(plain([]Entry{e}, 14+marginWidth), 0) // 14 columns of text
	if lines[1] != "one two three " || lines[2] != "four" {
		t.Errorf("reason wrapped as %q", lines[1:3])
	}
	if lines[3] != "$ echo aaaa bb" || lines[4] != "  bb cccc dddd" {
		t.Errorf("command wrapped as %q", lines[3:5])
	}
	joined := strings.TrimPrefix(lines[3], "$ ") + strings.TrimPrefix(lines[4], "  ")
	if joined != e.Command {
		t.Errorf("the wrapped command reads %q, not %q", joined, e.Command)
	}
}

// TestNothingHiddenReachesTheScreen covers every field the list draws, since
// an escape in a reason can repaint the command under it as well as one in
// the command can.
func TestNothingHiddenReachesTheScreen(t *testing.T) {
	e := entry("1", "echo hi\x1b[2K\rrm -rf ~")
	e.From, e.Why, e.Dir = "a\x1b]0;x\x07", "b\x1b[1A\u202e", "/tmp/\x1b[31m"
	e.Failed, e.Status = time.Now(), 1
	for _, color := range []bool{false, true} {
		l := lay([]Entry{e}, []bool{false}, 80, time.Now(), color)
		for _, line := range l.lines {
			if stripped := stripOurEscapes(line); strings.ContainsAny(stripped, "\x1b\r\u202e\x07") {
				t.Errorf("color %v: a hidden character reached the screen: %q", color, line)
			}
		}
	}
	l := lay([]Entry{e}, []bool{false}, 80, time.Now(), true)
	if !strings.Contains(strings.Join(l.lines, "\n"), "\x1b[7m␛\x1b[0m") {
		t.Errorf("a stand-in is not in reverse video:\n%q", l.lines)
	}
}

// stripOurEscapes takes away the color codes the list writes itself, which are
// the only escapes allowed on a line.
func stripOurEscapes(s string) string {
	for _, code := range []string{"\x1b[0m", "\x1b[1m", "\x1b[2m", "\x1b[7m", "\x1b[31m", "\x1b[32m", "\x1b[33m", "\x1b[36m"} {
		s = strings.ReplaceAll(s, code, "")
	}
	return s
}

func TestAnEntrysStateIsOnItsSecondLine(t *testing.T) {
	failed := entry("1", "false")
	failed.Failed, failed.Status = time.Now(), 3
	gone := entry("2", "true")
	ran := entry("3", "true")
	ran.Outcome, ran.Done = Ran, time.Now()
	l := lay([]Entry{failed, gone, ran}, []bool{false, true, false}, 80, time.Now(), false)
	for n, want := range []string{"last run failed: exit 3", "its directory is gone", "ran, exit 0"} {
		if got := owned(l, n)[1]; !strings.HasPrefix(got, want) {
			t.Errorf("entry %d's second line = %q, want %q", n, got, want)
		}
	}
}

// newView is a view of entries on a screen rows by cols, as the list first
// draws it.
func newView(entries []Entry, rows, cols int) *view {
	v := &view{moved: byPick}
	v.refresh(entries, make([]bool, len(entries)), rows, cols, time.Now(), false)
	return v
}

// again is the queue read again on the refresh timer, with nothing pressed.
func (v *view) again(entries []Entry) {
	v.moved = byRefresh
	v.next(entries)
}

// next is the list drawn again after a key, as the screen's loop does it:
// placed by whatever the key said moved it.
func (v *view) next(entries []Entry) {
	v.refresh(entries, make([]bool, len(entries)), v.rows, v.cols, time.Now(), false)
}

// TestThePickIsAnEntryNotARow: a command queued while the list is open arrives
// at the top, and the person's pick stays on the entry they chose rather than
// on the row, where a different command now sits.
func TestThePickIsAnEntryNotARow(t *testing.T) {
	a, b := entry("a", "echo a"), entry("b", "echo b")
	v := newView([]Entry{a, b}, 40, 80)
	v.press(keyDown)
	if v.shown[v.sel].ID != "b" {
		t.Fatalf("picked %s, want b", v.shown[v.sel].ID)
	}
	v.again([]Entry{entry("new", "sudo something-else"), a, b})
	if got := v.shown[v.sel].ID; got != "b" {
		t.Errorf("after a refresh the pick is %s, want it still on b", got)
	}
	if v.press(keyEnter) != runIt || v.shown[v.sel].Command != "echo b" {
		t.Errorf("Enter would not run the entry that was picked")
	}
}

// TestAPickThatLeftElsewhereIsNotReplaced: when another popup ran or dismissed
// the picked entry, Enter waits for a fresh pick rather than act on whatever
// took its place, and the screen says so.
func TestAPickThatLeftElsewhereIsNotReplaced(t *testing.T) {
	a, b, c := entry("a", "echo a"), entry("b", "echo b"), entry("c", "echo c")
	v := newView([]Entry{a, b, c}, 40, 80)
	v.press(keyDown)
	v.again([]Entry{a, c})
	for _, k := range []key{keyEnter, "d"} {
		if got := v.press(k); got != nothing {
			t.Errorf("%s on a lost pick = %v, want nothing", k, got)
		}
		if v.note != noteLost || !strings.Contains(v.draw(false), noteLost) {
			t.Errorf("%s on a lost pick did not say so: %q", k, v.draw(false))
		}
	}
	v.press(keyUp)
	if v.lost || v.press(keyEnter) != runIt || v.shown[v.sel].ID != "a" {
		t.Errorf("a fresh pick did not take the pick back")
	}
}

// TestAnEntryThatLeftHereIsNoSurprise: running or dismissing the picked entry
// from this list takes it away too, and the one that moves into its place is
// picked without a fuss, as it was in the script.
func TestAnEntryThatLeftHereIsNoSurprise(t *testing.T) {
	a, b := entry("a", "echo a"), entry("b", "echo b")
	v := newView([]Entry{a, b}, 40, 80)
	v.leftHere = true
	v.again([]Entry{b})
	if v.lost || v.press(keyEnter) != runIt {
		t.Errorf("the next entry was not picked after one left from here")
	}
}

// TestAnEntryPartlyOnScreenIsShownWholeBeforeItRuns: nothing runs that the
// person could not see whole. The first Enter scrolls it into view; the
// second runs it.
func TestAnEntryPartlyOnScreenIsShownWholeBeforeItRuns(t *testing.T) {
	// Each entry is five lines and a gap; with a body of eight lines, the
	// second entry is cut off at the foot of the screen.
	v := newView([]Entry{entry("a", "echo a"), entry("b", "echo b")}, 8+chromeLines, 80)
	v.sel = 1
	if v.onScreen() {
		t.Fatalf("the second entry is all on screen; the test needs it cut off")
	}
	if got := v.press(keyEnter); got != nothing {
		t.Fatalf("Enter on an entry partly off screen = %v, want nothing yet", got)
	}
	if v.note != noteScrolled {
		t.Errorf("note = %q", v.note)
	}
	v.next(v.shown)
	if !v.onScreen() {
		t.Fatalf("the entry was not scrolled into view: top %d", v.top)
	}
	if got := v.press(keyEnter); got != runIt {
		t.Errorf("Enter on the whole entry = %v, want it run", got)
	}
}

func TestAnEntryTallerThanTheScreenRunsAsItIs(t *testing.T) {
	v := newView([]Entry{entry("a", strings.Repeat("echo line\n", 20))}, 10, 80)
	if got := v.press(keyEnter); got != runIt {
		t.Errorf("Enter = %v, want an entry no view can show whole run as it is", got)
	}
}

// TestOnlyEnterRuns presses every key the list knows and a few it does not,
// in both lists: only Enter, in the waiting list, ever asks for a run.
func TestOnlyEnterRuns(t *testing.T) {
	keys := []key{
		keyNone, keyUp, keyDown, keyPgUp, keyPgDn, keyHome, keyEnd, keyEsc, keyQuit, keyEOF,
		"k", "j", " ", "d", "u", "h", "q", "r", "y", "x", "1", "\x1a",
	}
	for _, k := range keys {
		v := newView([]Entry{entry("a", "echo a")}, 40, 80)
		if got := v.press(k); got == runIt {
			t.Errorf("%q ran a command", k)
		}
	}
	v := newView([]Entry{entry("a", "echo a")}, 40, 80)
	if got := v.press(keyEnter); got != runIt {
		t.Errorf("Enter = %v, want runIt", got)
	}
	v.press("h")
	v.again([]Entry{entry("a", "echo a")})
	if got := v.press(keyEnter); got == runIt {
		t.Errorf("Enter in the history ran a command")
	}
}

func TestEnterOnAnEntryWhoseDirectoryIsGoneSaysSoInPlace(t *testing.T) {
	v := &view{moved: byPick}
	v.refresh([]Entry{entry("a", "echo a")}, []bool{true}, 40, 80, time.Now(), false)
	if got := v.press(keyEnter); got != nothing {
		t.Errorf("Enter = %v, want nothing", got)
	}
	if !strings.Contains(v.draw(false), noteGone) {
		t.Errorf("the screen did not say why:\n%s", v.draw(false))
	}
	if got := v.press("d"); got != dismissIt {
		t.Errorf("d = %v, want it dismissed", got)
	}
}

// TestAScrollPicksWhatIsOnScreen: PgDn moves the view, and the pick follows to
// the first entry that starts in it.
func TestAScrollPicksWhatIsOnScreen(t *testing.T) {
	var entries []Entry
	for _, id := range []string{"a", "b", "c", "d"} {
		entries = append(entries, entry(id, "echo "+id))
	}
	v := newView(entries, 7+chromeLines, 80)
	v.press(keyPgDn)
	v.place()
	if v.top == 0 || v.lay.starts[v.sel] < v.top {
		t.Errorf("after PgDn top %d, picked %s starting at %d", v.top, v.shown[v.sel].ID, v.lay.starts[v.sel])
	}
}

func TestTheTitleSaysWhichListAndHowMany(t *testing.T) {
	v := newView([]Entry{entry("a", "echo a"), entry("b", "echo b")}, 40, 80)
	if !strings.Contains(v.draw(false), "waiting to run · 2") {
		t.Errorf("title missing:\n%s", v.draw(false))
	}
	v.press("h")
	v.again(nil)
	if screen := v.draw(false); !strings.Contains(screen, "history · 0") || !strings.Contains(screen, noteNoHistory) {
		t.Errorf("history title missing:\n%s", screen)
	}
	narrow := newView(nil, 40, 30)
	if screen := narrow.draw(false); strings.Contains(screen, "q close") {
		t.Errorf("hints that do not fit were drawn anyway:\n%s", screen)
	}
}

// ---- the screen, driven by scripted keys --------------------------------

// liveScreen is a Screen over a store of the test's own, with keys the test
// types and a run that records what it was asked to run instead of running it.
type liveScreen struct {
	Screen

	typing *io.PipeWriter
	out    *syncBuffer
	ran    chan string
	closed chan error
}

// startScreen shows the list, with every run answered by result after during
// has happened, as if while the command ran.
func startScreen(t *testing.T, s Store, result Result, during func(ls *liveScreen)) *liveScreen {
	t.Helper()
	keys, typing := io.Pipe()
	ls := &liveScreen{typing: typing, out: &syncBuffer{}, ran: make(chan string, 8), closed: make(chan error, 1)}
	ls.Screen = Screen{
		Store: s,
		Keys:  NewKeyboard(keys),
		Out:   ls.out,
		Size:  func() (int, int) { return 40, 100 },
		Every: time.Hour, // nothing redraws behind the test's back
		run: func(r Run) (Result, error) {
			ls.ran <- r.Command
			if during != nil {
				during(ls)
			}
			return result, nil
		},
	}
	t.Cleanup(func() { _ = typing.Close() })
	go func() { ls.closed <- ls.Show() }()
	return ls
}

var ranFine = Result{Status: 0, Output: "done\n"}

func (ls *liveScreen) press(t *testing.T, keys string) {
	t.Helper()
	if _, err := ls.typing.Write([]byte(keys)); err != nil {
		t.Fatal(err)
	}
}

func (ls *liveScreen) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case err := <-ls.closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the list never closed")
	}
}

// TestTheCommandRunIsTheOneShown: the file is rewritten after the list drew
// it, and what runs is still the text the person read.
func TestTheCommandRunIsTheOneShown(t *testing.T) {
	s := newStore(t)
	id := queue(t, s, "echo shown")
	ls := startScreen(t, s, ranFine, nil)
	waitFor(t, ls.out, "$ echo shown")

	path := filepath.Join(s.Dir, pendingDir, id, commandFile)
	if err := os.WriteFile(path, []byte("echo swapped"), 0o600); err != nil {
		t.Fatal(err)
	}
	ls.press(t, "\r")
	if got := <-ls.ran; got != "echo shown" {
		t.Errorf("ran %q, want the command that was on screen", got)
	}
	waitFor(t, ls.out, "any key: back to the list")
	ls.press(t, "q")
	ls.waitClosed(t)

	e, err := s.Find(id)
	if err != nil || e.Outcome != Ran || s.Output(e) != "done\n" {
		t.Errorf("after a run that exited 0 the entry is %+v (%v), output %q", e, err, s.Output(e))
	}
}

// TestKeysTypedDuringARunAreDrained: a q typed while the command ran was the
// command's, and does not close the list at the prompt that follows.
func TestKeysTypedDuringARunAreDrained(t *testing.T) {
	s := newStore(t)
	queue(t, s, "echo one")
	ls := startScreen(t, s, ranFine, func(ls *liveScreen) {
		_, _ = ls.typing.Write([]byte("q"))
		time.Sleep(20 * time.Millisecond)
	})
	waitFor(t, ls.out, "$ echo one")
	ls.press(t, "\r")
	<-ls.ran
	waitFor(t, ls.out, "any key: back to the list")
	select {
	case <-ls.closed:
		t.Fatal("a key typed during the run closed the list")
	case <-time.After(150 * time.Millisecond):
	}
	ls.press(t, "x")
	waitFor(t, ls.out, "nothing waiting")
	ls.press(t, "q")
	ls.waitClosed(t)
}

func TestDismissingAndPuttingBack(t *testing.T) {
	s := newStore(t)
	id := queue(t, s, "echo one")
	ls := startScreen(t, s, ranFine, nil)
	waitFor(t, ls.out, "$ echo one")
	ls.press(t, "d")
	waitFor(t, ls.out, "nothing waiting")
	if e, _ := s.Find(id); e.Outcome != Dismissed {
		t.Fatalf("d left the entry as %q", e.Outcome)
	}
	ls.press(t, "h")
	waitFor(t, ls.out, "history · 1")
	ls.press(t, "u")
	waitFor(t, ls.out, "nothing in the history yet")
	if e, _ := s.Find(id); e.InHistory() {
		t.Fatalf("u left the entry in the history")
	}
	ls.press(t, "q") // back from the history
	waitFor(t, ls.out, "waiting to run · 1")
	ls.press(t, "\x1a") // Ctrl+Z, which must not stop anything
	ls.press(t, "q")
	ls.waitClosed(t)
	select {
	case got := <-ls.ran:
		t.Errorf("%q ran without Enter", got)
	default:
	}
}

func TestAFailedRunStaysOnTheList(t *testing.T) {
	s := newStore(t)
	id := queue(t, s, "false")
	ls := startScreen(t, s, Result{Status: 3, Output: "nope\n"}, nil)
	waitFor(t, ls.out, "$ false")
	ls.press(t, "\r")
	waitFor(t, ls.out, "exit 3 · it stays on the list")
	waitFor(t, ls.out, "any key: back to the list")
	ls.press(t, "x")
	waitFor(t, ls.out, "last run failed: exit 3")
	ls.press(t, "q")
	ls.waitClosed(t)
	e, err := s.Find(id)
	if err != nil || e.InHistory() || e.Status != 3 || s.Output(e) != "nope\n" {
		t.Errorf("after a failed run the entry is %+v (%v), output %q", e, err, s.Output(e))
	}
}

func TestTheRunIsShownUnderWhoAskedAndTheCommand(t *testing.T) {
	s := newStore(t)
	queue(t, s, "echo one\necho two")
	ls := startScreen(t, s, ranFine, nil)
	waitFor(t, ls.out, "$ echo one")
	ls.press(t, "\r")
	<-ls.ran
	waitFor(t, ls.out, "exit 0 · done, moved to the history")
	waitFor(t, ls.out, "any key: back to the list")
	for _, want := range []string{"storefront-eng-2318 asked for this · in ", "$ echo one\r\n> echo two\r\n"} {
		if !strings.Contains(ls.out.String(), want) {
			t.Errorf("the run's header is missing %q:\n%q", want, ls.out.String())
		}
	}
	ls.press(t, "q")
	ls.waitClosed(t)
}
