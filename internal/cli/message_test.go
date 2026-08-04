package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/ui"
)

// envWriting returns an Env whose streams are buffers, for asserting on the
// exact bytes a message writer produces.
func envWriting() (*Env, *bytes.Buffer) {
	var stderr bytes.Buffer
	return &Env{Argv0: "tw", Stdout: &bytes.Buffer{}, Stderr: &stderr}, &stderr
}

// TestAMessageOnOneLineIsUnchanged pins the case that is nearly all of them: a
// message with nothing to continue comes out exactly as it went in, prefix and
// newline and nothing else.
func TestAMessageOnOneLineIsUnchanged(t *testing.T) {
	env, stderr := envWriting()

	env.progressf("creating branch %s off origin/%s", "feature/eng-1", "main")
	env.warnf("origin unreachable — forking from local %s", "main")
	env.errorf("refusing to remove %q", "eng-1")

	want := "creating branch feature/eng-1 off origin/main\n" +
		"warning: origin unreachable — forking from local main\n" +
		"error: refusing to remove \"eng-1\"\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// TestAMessageContinuesUnderItself is the rule the whole file exists for: a
// message's later lines are indented, so two lines read as one message rather
// than as two.
//
// A prefixed message indents to the width of its prefix, putting every line's
// text in one column. Progress has no prefix to align under — its text starts at
// the margin — so it takes a plain hanging indent instead, which is the one
// place the two rules differ and the reason the indent is not simply derived.
func TestAMessageContinuesUnderItself(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(env *Env)
		want  int
	}{
		{"progress", func(env *Env) { env.progressf("nothing written\nremove --dry-run to save it") }, len(progressIndent)},
		{"warning", func(env *Env) { env.warnf("post_create did not finish\nsee the log") }, len(warningPrefix)},
		{"error", func(env *Env) { env.errorf("refusing to remove it\nre-run with --force") }, len(errorPrefix)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, stderr := envWriting()
			tc.write(env)

			lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
			if len(lines) != 2 {
				t.Fatalf("got %d lines, want 2: %q", len(lines), stderr.String())
			}
			if got := len(lines[1]) - len(strings.TrimLeft(lines[1], " ")); got != tc.want {
				t.Errorf("continuation indented %d columns, want %d\n%q", got, tc.want, stderr.String())
			}
			if strings.Contains(lines[1], "warning:") || strings.Contains(lines[1], "error:") {
				t.Errorf("the prefix repeats on the continuation: %q", lines[1])
			}
		})
	}
}

// TestAPrefixedContinuationSitsUnderTheTextAbove is the half of the rule that
// makes a prefixed message read as one block: its later lines start in the same
// column its first line's text does, not one short of it and not one past.
func TestAPrefixedContinuationSitsUnderTheTextAbove(t *testing.T) {
	env, stderr := envWriting()
	env.warnf("post_create did not finish\nsee the log")
	env.errorf("refusing to remove it\nre-run with --force")

	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	for _, pair := range [][2]int{{0, 1}, {2, 3}} {
		first, second := lines[pair[0]], lines[pair[1]]
		text := strings.Index(first, strings.Fields(strings.TrimLeft(first, " "))[1])
		if got := len(second) - len(strings.TrimLeft(second, " ")); got != text {
			t.Errorf("continuation at column %d, want %d to sit under %q", got, text, first)
		}
	}
}

// TestWriteErrorMatchesTheErrorsCommandsPrint keeps main's path and the
// package's own agreeing. Which of the two reported a failure is treewright's
// business, not the reader's, so the two must be indistinguishable.
func TestWriteErrorMatchesTheErrorsCommandsPrint(t *testing.T) {
	env, stderr := envWriting()
	env.errorf("no worktree %q\nhave:%s", "nope", asLines([]string{"alpha", "beta"}))

	var direct bytes.Buffer
	WriteError(&direct, errors.New("no worktree \"nope\"\nhave:"+asLines([]string{"alpha", "beta"})))

	if got, want := direct.String(), stderr.String(); got != want {
		t.Errorf("WriteError = %q, errorf = %q", got, want)
	}
}

// TestAListIsSetOutOneEntryPerLine covers asLines: the entries land under the
// line that introduces them, each stepped in from it, and an empty list adds
// nothing at all so a caller need not count first.
func TestAListIsSetOutOneEntryPerLine(t *testing.T) {
	env, stderr := envWriting()
	env.progressf("wrote to %s:%s", "/repo/.claude", asLines([]string{"SKILL.md", "hooks/hooks.json"}))

	want := "wrote to /repo/.claude:\n" +
		"    SKILL.md\n" +
		"    hooks/hooks.json\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}

	// Each entry is indented past the prose that introduces it, in a warning as
	// in progress — the step is relative to the message, not an absolute column.
	env2, stderr2 := envWriting()
	env2.warnf("these are missing:%s", asLines([]string{"a"}))
	lines := strings.Split(strings.TrimRight(stderr2.String(), "\n"), "\n")
	prose := strings.Index(lines[0], "these")
	if got := len(lines[1]) - len(strings.TrimLeft(lines[1], " ")); got <= prose {
		t.Errorf("list entry at column %d, want it stepped in past the prose at %d", got, prose)
	}

	if got := asLines(nil); got != "" {
		t.Errorf("asLines(nil) = %q, want nothing rather than a bare colon with silence under it", got)
	}
}

// TestFieldsLineUpUnderOneColumn covers asFields, which is what the register is
// built on: each fact named and stacked, values in one column, so a reader after
// the log path can go straight to it instead of reading the sentence it used to
// be the end of.
func TestFieldsLineUpUnderOneColumn(t *testing.T) {
	env, stderr := envWriting()
	env.warnf("post_create failed in eng-9%s", asFields(
		field("failed step", "npm ci"),
		field("log", ".git/treewright/post-create-eng-9.log"),
	))

	want := "warning: post_create failed in eng-9\n" +
		"         failed step  npm ci\n" +
		"         log          .git/treewright/post-create-eng-9.log\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// TestAFieldValueCanSpanLines covers the case a list-valued field is: its later
// entries hold the value column rather than sliding back under the label.
func TestAFieldValueCanSpanLines(t *testing.T) {
	env, stderr := envWriting()
	env.progressf("wrote the plugin%s", asFields(
		field("directory", "/repo/.claude"),
		field("files", "SKILL.md\nhooks/hooks.json"),
	))

	want := "wrote the plugin\n" +
		"  directory  /repo/.claude\n" +
		"  files      SKILL.md\n" +
		"             hooks/hooks.json\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// TestColorMarksTheSeverityAndTheCopyablePart covers the third thing a message
// leans on. It is decoration: with color off the text is unchanged, which is
// what lets a redirected stderr and a NO_COLOR terminal carry the same message.
func TestColorMarksTheSeverityAndTheCopyablePart(t *testing.T) {
	// A buffer is not a terminal, so this is the color-off path — the one every
	// other test in this package reads, and the one a pipe gets.
	env, stderr := envWriting()
	env.warnf("could not switch to session proj%s",
		asFields(field("attach with", env.copyable("tw attach proj"))))
	if strings.Contains(stderr.String(), "\x1b") {
		t.Errorf("escape sequences reached a non-terminal writer: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "attach with  tw attach proj") {
		t.Errorf("stderr = %q, want the command present whether or not it is colored", stderr.String())
	}

	// os.Stdout under `go test` is not a terminal either, so this is still the
	// off path — what it pins is that the prefix survives it intact.
	if got := colorPrefix(os.Stdout, warningPrefix, ui.Yellow); got != warningPrefix {
		t.Errorf("colorPrefix = %q, want %q unchanged with color off", got, warningPrefix)
	}
}

// TestPaintedPrefixKeepsItsSpaceOutsideTheColor drives the on branch, which no
// writer a test can make ever reaches — every buffer reads as a pipe, so
// paintPrefix takes the decision as an argument. The space has to end up
// outside the escape codes: a colored run ending in a space is a colored
// space, and the one place it shows is a terminal that draws backgrounds.
func TestPaintedPrefixKeepsItsSpaceOutsideTheColor(t *testing.T) {
	got := paintPrefix(warningPrefix, ui.Yellow, true)
	if want := ui.Yellow.Apply("warning:", true) + " "; got != want {
		t.Errorf("paintPrefix on = %q, want %q — the word colored, the space bare", got, want)
	}
	if got := paintPrefix(warningPrefix, ui.Yellow, false); got != warningPrefix {
		t.Errorf("paintPrefix off = %q, want the prefix untouched", got)
	}
}

// TestAnOptionalClauseTakesALineOrNothing covers under, which carries the caveat
// a message has only sometimes.
func TestAnOptionalClauseTakesALineOrNothing(t *testing.T) {
	if got := under(""); got != "" {
		t.Errorf("under(%q) = %q, want nothing", "", got)
	}
	env, stderr := envWriting()
	env.progressf("closed its tmux window (%s)%s", "eng-1", under("it is the last in session proj"))
	if got, want := stderr.String(), "closed its tmux window (eng-1)\n  it is the last in session proj\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// TestACountReadsAsASentence covers the helper that replaced "1 file(s)", the
// shape a message takes when it covers both cases at once and reads as neither.
func TestACountReadsAsASentence(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0 uncommitted files"},
		{1, "1 uncommitted file"},
		{2, "2 uncommitted files"},
	} {
		if got := count(tc.n, "uncommitted file", "uncommitted files"); got != tc.want {
			t.Errorf("count(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestEveryLineOfAMessageKeepsTheVoice extends the one-voice rule to the lines a
// message continues onto. TestMessagesShareOneVoice checks what a handful of
// commands print; this checks the writers themselves, so a continuation cannot
// arrive capitalized or full-stopped whichever command wrote it.
func TestEveryLineOfAMessageKeepsTheVoice(t *testing.T) {
	env, stderr := envWriting()
	env.warnf("a finding\nwhat to do about it\nwhy it matters")
	env.errorf("another finding:%s", asLines([]string{"one", "two"}))

	for line := range strings.SplitSeq(strings.TrimRight(stderr.String(), "\n"), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, "warning: "), "error: ")
		if trimmed == "" {
			continue
		}
		if first := trimmed[0]; first >= 'A' && first <= 'Z' {
			t.Errorf("line starts with a capital, unlike the rest: %q", line)
		}
		if strings.HasSuffix(line, ".") {
			t.Errorf("line ends in a period, unlike the rest: %q", line)
		}
	}
}
