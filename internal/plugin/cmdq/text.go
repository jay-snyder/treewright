package cmdq

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Hidden characters, and what is shown in their place.
//
// Everything this plugin prints that somebody else wrote — the command, the
// reason, who asked, the directory, a run's output — could carry a character
// that rewrites the screen around it rather than appearing on it: an escape
// sequence that moves the cursor and repaints the command above it, a carriage
// return that writes a harmless line over a harmful one, a direction override
// that shows a string backwards. So none of them is ever written to the
// terminal. Each is shown as a stand-in instead: the C0 controls as their
// Control Pictures (ESC as ␛, tab as ␉), DEL as ␡, and the rest as �, which
// the list highlights. Newlines stay newlines, since a command of several
// lines is ordinary.
//
// "The rest" is the C1 controls, which some terminals read as escape
// sequences of their own; bytes that are not UTF-8, which would otherwise
// reach the terminal raw; and the zero-width and direction-formatting
// characters — U+200B–200F, U+202A–202E, U+2060–2064, U+2066–2069 and U+FEFF —
// which show nothing while changing what the characters around them look like.
const replacement = '\uFFFD'

// standIn is what r is shown as, and whether it needs one.
func standIn(r rune, size int) (rune, bool) {
	switch {
	case r == '\n':
		return r, false
	case r < 0x20:
		return 0x2400 + r, true
	case r == 0x7f:
		return '\u2421', true
	case r >= 0x80 && r <= 0x9f,
		r >= 0x200b && r <= 0x200f,
		r >= 0x202a && r <= 0x202e,
		r >= 0x2060 && r <= 0x2064,
		r >= 0x2066 && r <= 0x2069,
		r == 0xfeff:
		return replacement, true
	case r == utf8.RuneError && size <= 1:
		return replacement, true
	}
	return r, false
}

// Visible is s with every hidden character replaced by its stand-in.
func Visible(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		shown, _ := standIn(r, size)
		b.WriteRune(shown)
		i += size
	}
	return b.String()
}

// OneLine is s with every control character — a newline among them — turned
// into a space and the ends trimmed: what `add` makes of the reason and of who
// is asking, which the list gives a line each. The other hidden characters
// stay, to be shown as stand-ins like anywhere else.
func OneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s))
}

// isStandIn reports a rune Visible puts in place of a hidden character. A
// Control Picture typed as itself is marked too: it cannot be told from a
// stand-in on screen, and is as odd a thing to find in a command.
func isStandIn(r rune) bool {
	return (r >= 0x2400 && r <= 0x2421) || r == replacement
}

// markStandIns wraps each stand-in in already-visible text with mark, which
// the list uses to put it in reverse video. Called on visible text only, line
// by line, after wrapping: the escapes it adds are not characters, and must
// not be counted as columns.
func markStandIns(s string, mark func(string) string) string {
	if !strings.ContainsFunc(s, isStandIn) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if isStandIn(r) {
			b.WriteString(mark(string(r)))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// What a run leaves behind for `status`.
const (
	// KeepBytes is how much of a run's output is kept as it goes: enough for
	// the last screenful of anything, and a bound on a command that prints
	// for an hour.
	KeepBytes = 64 << 10
	// KeepLines is how many lines of that `status` shows.
	KeepLines = 20
)

// escapeSequences matches what a terminal reads as an instruction rather than
// as text: CSI sequences, the string-carrying ones (OSC, DCS and the rest)
// up to their terminator or the end of the text, and two-character escapes.
var escapeSequences = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|(?s:\x1b[\]PX^_].*?(?:\x07|\x1b\\|$))|\x1b[ -/]*[0-~]`)

// Tail is the last KeepLines lines of a run's output, as a person reading it
// back would want them: escape sequences gone, each line as a carriage return
// left it — the text after the last one, which is what the terminal showed —
// and the blank lines a command ends on dropped. truncated says the output
// began before what was kept, so its first line is a fragment and is dropped
// too.
//
// What is left can still hold hidden characters — a backspace, a stray
// control byte — and is shown through Visible like everything else.
func Tail(kept []byte, truncated bool) string {
	text := strings.ToValidUTF8(string(kept), string(replacement))
	text = escapeSequences.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		parts := strings.Split(line, "\r")
		shown := ""
		for _, p := range parts {
			if p != "" {
				shown = p
			}
		}
		lines[i] = shown
	}
	if truncated && len(lines) > 0 {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines[max(0, len(lines)-KeepLines):], "\n") + "\n"
}

// Ago is how long ago t was, as the list says it in passing: just now, then
// minutes, hours and days, each to the whole unit.
func Ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	default:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	}
}

// Describe is an entry's state in a phrase: how it left the list, or that it
// is waiting and since when, with its last failure if it had one.
func Describe(e Entry, now time.Time) string {
	when := ""
	if !e.Done.IsZero() {
		when = ", " + e.Done.Local().Format("2006-01-02 15:04")
	}
	switch e.Outcome {
	case Waiting:
	case Ran:
		return "ran, exit 0" + when
	case Dismissed:
		return "dismissed by the person" + when
	case Withdrawn:
		return "withdrawn" + when
	default:
		return Visible(string(e.Outcome)) + when
	}
	if !e.Failed.IsZero() {
		return "waiting — " + LastFailure(e, now)
	}
	return "waiting — queued " + Ago(e.Added, now)
}

// LastFailure says how an entry's last run went, for one whose run failed.
func LastFailure(e Entry, now time.Time) string {
	return "last run failed: exit " + strconv.Itoa(e.Status) + ", " + Ago(e.Failed, now)
}
