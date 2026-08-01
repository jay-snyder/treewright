package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/jay-snyder/treewright/internal/ui"
)

// How a message is shaped on the way out.
//
// Every message treewright prints is one message, however much it has to say.
// The ones that had the most to say used to say it as one line — a finding, an
// em dash, what to do about it, a second em dash, why it mattered — and a
// terminal wrapped that wherever it happened to run out of room, mid-path and
// mid-command, so the thing the reader needed most was the thing hardest to
// pick out.
//
// Three things fix that, and the first is not enough on its own. Breaking the
// line was the first, and it left the same prose sitting on three lines instead
// of one: the register this package prints in is the register the docs are
// written in, and a trailing appositive that reads well in a paragraph is a
// thing to parse in a terminal, where nobody is reading — they are looking for
// one fact. So a message now front-loads its subject, says one thing per line,
// and puts the rest in labelled fields (asFields) or a list (asLines). Second,
// the writers below indent every line after the first, which is what holds a
// block together as one message; done here rather than at each call site,
// because thirty call sites spelling their own indent is thirty chances to
// spell it differently — one hand-typed run of seven spaces in rm was the whole
// of the old mechanism. Third, color marks the two spans worth finding without
// reading: the severity, and the part meant to be typed.
//
// Color is decoration only. It is off down a pipe, off under NO_COLOR and off
// on a dumb terminal, so every message has to carry its whole meaning in the
// text — what color buys is the eye landing in the right place first, never a
// fact only the colored copy has.

// The prefixes, following git's convention, and the indent that goes under
// each. Progress carries no prefix at all, so its continuations take a plain
// two-column hanging indent instead — with no prefix to align under, an
// unindented second line would read as a second message rather than as the rest
// of this one.
const (
	warningPrefix  = "warning: "
	errorPrefix    = "error: "
	progressIndent = "  "
)

// listIndent sets a list in from the prose that introduces it, so a run of
// paths under "wrote to <dir>:" reads as the list it is rather than as more
// sentence. Relative to the message's own indent, not absolute: a list under a
// warning steps in from the warning's text, exactly as one under progress steps
// in from progress's.
const listIndent = "  "

// continuationFor is the indent a message's later lines take under prefix: the
// prefix's own width, so the text of every line starts in the same column.
func continuationFor(prefix string) string { return strings.Repeat(" ", len(prefix)) }

// colorPrefix paints a prefix for w, keeping the trailing space outside the
// escape codes — a colored run ending in a space is a colored space, and the
// one place it shows is a terminal that draws backgrounds.
func colorPrefix(w io.Writer, prefix string, c ui.Color) string {
	if !ui.ColorEnabled(w) {
		return prefix
	}
	word, rest, spaced := strings.Cut(prefix, " ")
	if spaced {
		return c.Apply(word, true) + " " + rest
	}
	return c.Apply(word, true)
}

// copyable marks the span of a message the reader is meant to type or paste —
// the command, the path, the config line. It is the one thing in a message
// people are looking for rather than reading, and in a three-line finding it
// was previously indistinguishable from the prose either side of it.
//
// Cyan rather than bold, to stay in the palette the tables already use, and
// applied against the stream the message goes to, so a redirected stderr gets
// none of it.
func (e *Env) copyable(s string) string {
	return ui.Cyan.Apply(s, ui.ColorEnabled(e.Stderr))
}

// writeMessage writes a message under prefix, indenting every line after the
// first to indent. A message with no newlines in it comes out exactly as it
// went in, which is nearly all of them.
func writeMessage(w io.Writer, prefix, indent, message string) {
	for i, line := range strings.Split(message, "\n") {
		if i == 0 {
			fmt.Fprintf(w, "%s%s\n", prefix, line)
			continue
		}
		fmt.Fprintf(w, "%s%s\n", indent, line)
	}
}

// WriteError prints an error the way every other message is printed. main owns
// the exit code and so is the one place a returned error becomes text, which
// makes this the one piece of the message contract that has to leave the
// package: without it an error split across lines would arrive with its later
// lines flat against the margin, under a first line that is indented past them.
func WriteError(w io.Writer, err error) {
	writeMessage(w, colorPrefix(w, errorPrefix, ui.Red), continuationFor(errorPrefix), err.Error())
}

// progressf reports what treewright is doing. Unprefixed, on stderr.
func (e *Env) progressf(format string, args ...any) {
	writeMessage(e.Stderr, "", progressIndent, fmt.Sprintf(format, args...))
}

// warnf reports something the user should know but that does not stop the
// command — a stale config entry, a branch that could not be deleted.
func (e *Env) warnf(format string, args ...any) {
	writeMessage(e.Stderr, colorPrefix(e.Stderr, warningPrefix, ui.Yellow),
		continuationFor(warningPrefix), fmt.Sprintf(format, args...))
}

// errorf reports a failure the command has handled itself, returning ErrSilent
// rather than an error for main to print. Going through the same writer is what
// makes those messages indistinguishable from the ones main prints, which they
// should be: which of the two paths reported a failure is treewright's business
// and not the reader's.
func (e *Env) errorf(format string, args ...any) {
	writeMessage(e.Stderr, colorPrefix(e.Stderr, errorPrefix, ui.Red),
		continuationFor(errorPrefix), fmt.Sprintf(format, args...))
}

// asLines sets values out one per line, for the tail of a message whose list
// would otherwise run off the side of the terminal — the files a plugin wrote,
// the worktrees a prefix matched, the prefixes a repository uses.
//
// It returns a leading newline and no trailing one, so it appends to the line
// that introduces the list: progressf("wrote to %s:%s", dir, asLines(written)).
// Nothing here indents to a column, because the message writer has not applied
// its own indent yet; what these carry is the one step in from that.
//
// An empty list renders as nothing at all rather than as a bare colon with
// silence under it, which lets a caller hand this a slice it has not counted.
func asLines(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return "\n" + listIndent + strings.Join(values, "\n"+listIndent)
}

// fieldGap separates a field's label from its value. Two spaces, as the tables
// use between columns, so a message's fields and a table's columns read as the
// same thing — which they are.
const fieldGap = "  "

// asFields sets out label/value pairs one per line, labels padded to one width
// so the values line up in a column.
//
// This is what the register is built on. A message with a what, a where and a
// how used to run them together with em dashes between — "post_create in eng-9
// stopped at 'npm ci' — see .git/treewright/post-create-eng-9.log" — and a
// reader after the log path had to read the whole sentence to find it. Named
// and stacked, each fact is where the eye can go straight to it, and the labels
// are the message saying what its own parts are.
//
// A value may itself span lines; its later lines are padded to the label's
// width, so a field holding a list keeps the value column. Like asLines this
// returns a leading newline and no trailing one, and does not indent to a
// column — the message writer has not applied its own indent yet.
func asFields(pairs ...[2]string) string {
	width := 0
	for _, p := range pairs {
		width = max(width, utf8.RuneCountInString(p[0]))
	}

	var b strings.Builder
	valueColumn := strings.Repeat(" ", width+len(fieldGap))
	for _, p := range pairs {
		label := p[0] + strings.Repeat(" ", width-utf8.RuneCountInString(p[0])) + fieldGap
		for i, line := range strings.Split(p[1], "\n") {
			if i == 0 {
				fmt.Fprintf(&b, "\n%s%s", label, line)
				continue
			}
			fmt.Fprintf(&b, "\n%s%s", valueColumn, line)
		}
	}
	return b.String()
}

// field is asFields' pair, spelled as a function so a call site reads as a list
// of labelled things rather than as a slice of two-element arrays.
func field(label, value string) [2]string { return [2]string{label, value} }

// under puts an optional clause on a line of its own beneath the one before it,
// and contributes nothing when there is no clause. It is for the caveat a
// message carries only sometimes — the window that is the last in its session —
// which read as a parenthetical wedged into the middle of the sentence it
// qualified, in the one case where it changes the answer.
func under(line string) string {
	if line == "" {
		return ""
	}
	return "\n" + line
}

// count writes a number and the thing it counts, in the form the number calls
// for. "1 uncommitted file(s)" is the shape a message takes when it has to
// cover both cases at once, and it reads as neither.
func count(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}
