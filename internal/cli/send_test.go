package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `send` types at the agent already running in a worktree's window, which is
// the case --prompt cannot reach: the command carrying a prompt runs only in a
// window that was created, so a resume that finds one open drops it.
//
// The window is real in every test here, because what is being covered is the
// interaction with a tty: what arrives, in what order, and what is refused
// before anything arrives at all.

// receives configures a window whose command writes whatever is typed at it to
// a file, so a test can assert on what the agent would have received.
func receives(t *testing.T) (config, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "received")
	return "command = \"cat > " + marker + "\"\n", marker
}

// TestSendTypesOneLineAtTheAgent is the whole of what the command does, and the
// two tmux details that used to be prose: the words arrive as words rather than
// as key names, and Enter submits them.
func TestSendTypesOneLineAtTheAgent(t *testing.T) {
	requireTmux(t)
	config, marker := receives(t)
	f := newFixture(t, config)
	f.mustRun("new", "eng-1")

	// Deliberately a message whose words are also tmux key names, which is what
	// a send without -l would mangle into keystrokes.
	const message = "Enter the review comments and Escape the shell quoting"
	r := f.exec("send", "eng-1", message)
	if r.err != nil {
		t.Fatalf("send: %v\n%s", r.err, r.both())
	}
	waitForContent(t, marker, message+"\n", "the message")

	// Nothing on stdout: there is no answer here, only something done.
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing", r.stdout)
	}
	if !strings.Contains(r.stderr, "sent to ENG-1") {
		t.Errorf("stderr = %q, want the window it reached named", r.stderr)
	}
}

// TestSendShowsThePaneBeforeItTypes puts "look before you type" in the
// transcript rather than in a rule someone can skip. An agent sitting on a
// question with options takes the next keystrokes as the answer to it, and the
// capture is what makes that visible to whoever is sending.
func TestSendShowsThePaneBeforeItTypes(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = \"sh -c 'echo overwrite the config file? y/n; sleep 300'\"\n")
	f.mustRun("new", "eng-1")
	waitForPane(t, windowIDNamed(t, "proj", "ENG-1"), "overwrite the config file?")

	r := f.exec("send", "eng-1", "y")
	if r.err != nil {
		t.Fatalf("send: %v\n%s", r.err, r.both())
	}
	shown := strings.Index(r.stderr, "overwrite the config file?")
	sent := strings.Index(r.stderr, "sent to")
	if shown < 0 {
		t.Fatalf("stderr = %q, want what the window was showing", r.stderr)
	}
	if sent < 0 || shown > sent {
		t.Errorf("stderr = %q, want the pane reported before the message was sent", r.stderr)
	}
}

// TestSendDryRunReadsWithoutTyping covers looking as an action of its own: the
// pane comes back and nothing is typed, which is also how a window is read
// when there is nothing to say to it.
func TestSendDryRunReadsWithoutTyping(t *testing.T) {
	requireTmux(t)
	config, marker := receives(t)
	f := newFixture(t, config)
	f.mustRun("new", "eng-1")

	r := f.exec("send", "-n", "eng-1", "you never see this")
	if r.err != nil {
		t.Fatalf("send --dry-run: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "nothing was sent") {
		t.Errorf("stderr = %q, want it said that nothing was typed", r.stderr)
	}

	// The message is optional under --dry-run, the action being the look.
	if r := f.exec("send", "--dry-run", "eng-1"); r.err != nil {
		t.Errorf("send --dry-run with no message: %v\n%s", r.err, r.both())
	}
	if body := readIfPresent(t, marker); body != "" {
		t.Errorf("the window received %q from a dry run", body)
	}
}

// TestSendRefusesALineBreak: Enter is what submits, so the second line and
// everything after it would post as further turns. treewright cannot tell that
// from an intention, and by the time anyone sees it, it has happened.
func TestSendRefusesALineBreak(t *testing.T) {
	requireTmux(t)
	config, marker := receives(t)
	f := newFixture(t, config)
	f.mustRun("new", "eng-1")

	r := f.exec("send", "eng-1", "first line\nsecond line")
	if r.err == nil {
		t.Fatalf("send with a line break succeeded\n%s", r.both())
	}
	// The way through is the shape --prompt-file already has: a file, and a
	// line naming it.
	if !strings.Contains(r.err.Error(), "--prompt-file") {
		t.Errorf("error = %q, want the way through named", r.err)
	}
	if body := readIfPresent(t, marker); body != "" {
		t.Errorf("the window received %q from a refused send", body)
	}
}

// TestSendRefusesTheCallersOwnWindow. An agent typing at itself is a real
// footgun and a hard one to notice from the inside: the message arrives in this
// very session, ahead of whatever was being answered, and reads afterwards as
// an instruction from somewhere else.
func TestSendRefusesTheCallersOwnWindow(t *testing.T) {
	requireTmux(t)
	config, marker := receives(t)
	f := newFixture(t, config)
	f.mustRun("new", "eng-1")

	insideSession(t, "proj")
	t.Setenv("TMUX_PANE", paneIn(t, "proj", "ENG-1"))

	r := f.exec("send", "eng-1", "do the thing")
	if r.err == nil {
		t.Fatalf("send to the caller's own window succeeded\n%s", r.both())
	}
	if !strings.Contains(r.err.Error(), "running in") {
		t.Errorf("error = %q, want it to say the window is the caller's own", r.err)
	}
	if body := readIfPresent(t, marker); body != "" {
		t.Errorf("the window received %q from a refused send", body)
	}
}

// TestSendNeedsAWindowToTypeInto: a worktree with no window has no agent in it,
// and the error names the command that opens one rather than leaving the reader
// to work out that resume is the way.
func TestSendNeedsAWindowToTypeInto(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")
	f.Worktree("eng-1")

	r := f.exec("send", "eng-1", "carry on")
	if r.err == nil {
		t.Fatalf("send to a worktree with no window succeeded\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "treewright resume eng-1") {
		t.Errorf("error = %q, want the way to open one named", msg)
	}
}

// TestSendRefusesAWindowHeldOpenAfterItsCommandDied is the state the capture
// buys twice over. The held-open wrapper is the one place a treewright window
// outlives its agent: the command failed, its output is being kept on screen,
// and what is reading the keyboard is a shell blocked on `read`. A message
// there reaches nobody — and the Enter after it satisfies that read and closes
// the window, erasing the one thing that explains why there is no agent.
func TestSendRefusesAWindowHeldOpenAfterItsCommandDied(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'echo no such model >&2; exit 12'\n")
	f.mustRun("new", "eng-1")
	waitForPane(t, windowIDNamed(t, "proj", "ENG-1"), heldOpenNotice)

	r := f.exec("send", "eng-1", "carry on")
	if r.err == nil {
		t.Fatalf("send to a held-open window succeeded\n%s", r.both())
	}
	if !strings.Contains(r.err.Error(), "no agent in it") {
		t.Errorf("error = %q, want the dead agent named", r.err)
	}
	// And the window is still there with the failure still on it, which is what
	// the refusal is protecting.
	if got := panesOn(t, f.DirFor("eng-1")); got != 1 {
		t.Errorf("%d panes in the worktree, want the held window still open", got)
	}
	pane, _ := tmuxctl(t, "capture-pane", "-p", "-t", windowIDNamed(t, "proj", "ENG-1"))
	if !strings.Contains(pane, "no such model") {
		t.Errorf("pane = %q, want the failure still readable", pane)
	}
}

// TestSendResolvesASlugTheWayResumeDoes: an unambiguous prefix is enough and
// the expansion is reported, since the caller may be about to type into
// somebody else's session.
func TestSendResolvesASlugTheWayResumeDoes(t *testing.T) {
	requireTmux(t)
	config, marker := receives(t)
	f := newFixture(t, config)
	f.mustRun("new", "eng-1646-app-landing-page")

	r := f.exec("send", "eng-1646", "carry on")
	if r.err != nil {
		t.Fatalf("send: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "eng-1646 matches worktree eng-1646-app-landing-page") {
		t.Errorf("stderr = %q, want the expansion reported", r.stderr)
	}
	waitForContent(t, marker, "carry on", "the message")
}

// readIfPresent is what a refused send is checked against: the window's command
// creates the file as it starts, so "nothing arrived" is an empty file rather
// than a missing one — and a missing one is an empty answer too.
func readIfPresent(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(body)
}
