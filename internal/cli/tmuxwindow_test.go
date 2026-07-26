package cli

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jay-snyder/treewright/internal/testenv"
)

// The tests here drive a real tmux server, because the behavior they cover exists
// only in the interaction with one: which window a teardown closes, and which it
// leaves alone.
//
// The server is the one every fixture already owns — its own socket, killed
// afterwards — so a developer's own tmux is never touched. The helpers are shared
// with tmuxsession_test.go, which covers where windows are put in the first place.

// tmuxctl runs a tmux command against the test's private server.
func tmuxctl(t *testing.T, args ...string) (string, error) {
	t.Helper()
	label := os.Getenv("TREEWRIGHT_TMUX_LABEL")
	if label == "" {
		t.Fatal("no private tmux server — newFixture is what sets TREEWRIGHT_TMUX_LABEL")
	}
	out, err := exec.Command("tmux", append([]string{"-L", label}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// startSession creates a session holding one window that sits in dir and stays
// open, standing in for a session treewright itself would have created.
//
// -f /dev/null because a tmux server reads ~/.tmux.conf as it starts, and this is
// usually the call that starts the test's server. Without it the developer's own
// bindings and options land in the table these tests inspect — so a developer who
// had loaded treewright's own tmux integration into their config would watch the
// suite fail on their machine and nowhere else. The socket is already private;
// the configuration has to be too.
func startSession(t *testing.T, session, window, dir string) {
	t.Helper()
	requireTmux(t)
	if out, err := tmuxctl(t, "-f", "/dev/null", "new-session", "-d", "-s", session, "-n", window, "-c", dir, "sleep 300"); err != nil {
		testenv.Unavailable(t, "cannot start a tmux server here: %v\n%s", err, out)
	}
}

// startShellSession creates a session whose window runs a shell rather than a
// command that just sits there, so a test can walk it from one directory to
// another the way a person does.
func startShellSession(t *testing.T, session, window, dir string) {
	t.Helper()
	requireTmux(t)
	if out, err := tmuxctl(t, "-f", "/dev/null", "new-session", "-d", "-s", session, "-n", window, "-c", dir, "/bin/sh"); err != nil {
		testenv.Unavailable(t, "cannot start a tmux server here: %v\n%s", err, out)
	}
}

// walkInto moves a window's shell into dir, which is what `treewright cd` does to
// the base window — and the everyday way two windows come to stand in one
// worktree.
//
// The wait is for the shell rather than for tmux: the pane reports its new
// directory once the cd has actually run.
func walkInto(t *testing.T, session, window, dir string) {
	t.Helper()
	id := windowIDNamed(t, session, window)
	if out, err := tmuxctl(t, "send-keys", "-t", id, "cd "+dir, "Enter"); err != nil {
		t.Fatalf("send cd to %s: %v\n%s", window, err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := tmuxctl(t, "display-message", "-p", "-t", id, "#{pane_current_path}"); err == nil && out == dir {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("window %s never moved into %s", window, dir)
}

// windowStamp reads back one of the user options treewright records on a window.
//
// Through a format rather than through show-options, which does not consider an
// option nobody set to be an option at all: it answers "invalid option" and
// fails, where a format renders it as the empty string. The format is also how
// treewright reads the stamp back itself, and how a user's status line would, so
// this asks the question the way it is really asked.
func windowStamp(t *testing.T, session, window, option string) string {
	t.Helper()
	out, err := tmuxctl(t, "display-message", "-p", "-t", windowIDNamed(t, session, window), "#{"+option+"}")
	if err != nil {
		t.Fatalf("read %s on window %s: %v\n%s", option, window, err, out)
	}
	return out
}

// twoWindowsInOneWorktree builds the collision the lookup has to survive: the
// base window, opened first and standing in the worktree after a
// `treewright cd`, and the worktree's own window, opened by treewright afterwards.
//
// The visitor is deliberately both the older window and the one arranged first,
// so neither age nor position can pick the right answer — only what treewright
// recorded on the window it opened. It returns the worktree they share.
func twoWindowsInOneWorktree(t *testing.T, f *fixture) string {
	t.Helper()
	startShellSession(t, "proj", "MAIN", f.MainDir)
	if r := f.exec("new", "eng-1"); r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	wt := f.DirFor("eng-1")
	walkInto(t, "proj", "MAIN", wt)
	swapWindows(t, "proj", "MAIN", "ENG-1")
	return wt
}

// openWindowOn adds a window sitting in dir to a session that already exists.
func openWindowOn(t *testing.T, session, window, dir string) {
	t.Helper()
	if out, err := tmuxctl(t, "new-window", "-d", "-t", "="+session+":", "-n", window, "-c", dir, "sleep 300"); err != nil {
		t.Fatalf("open a window on %s: %v\n%s", dir, err, out)
	}
}

// windowIDNamed finds a window by name in a session, for the commands that take
// a window rather than a position.
func windowIDNamed(t *testing.T, session, name string) string {
	t.Helper()
	out, err := tmuxctl(t, "list-windows", "-t", "="+session, "-F", "#{window_name}\t#{window_id}")
	if err != nil {
		t.Fatalf("list the windows of %s: %v\n%s", session, err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if found, id, ok := strings.Cut(line, "\t"); ok && found == name {
			return id
		}
	}
	t.Fatalf("no window named %s in session %s, only:\n%s", name, session, out)
	return ""
}

// swapWindows exchanges two windows' positions, standing in for a user
// rearranging their status line.
//
// By id rather than by index, so the swap says which windows it means: the point
// of the tests using it is that a window's position must not decide anything.
func swapWindows(t *testing.T, session, a, b string) {
	t.Helper()
	if out, err := tmuxctl(t, "swap-window",
		"-s", windowIDNamed(t, session, a), "-t", windowIDNamed(t, session, b)); err != nil {
		t.Fatalf("swap %s with %s: %v\n%s", a, b, err, out)
	}
}

// panesOn reports how many panes are sitting in dir, across every session.
func panesOn(t *testing.T, dir string) int {
	t.Helper()
	out, err := tmuxctl(t, "list-panes", "-a", "-F", "#{pane_current_path}")
	if err != nil {
		// No panes at all is a valid answer: killing the last window ends the
		// server, which reports an error rather than an empty list.
		return 0
	}
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == dir {
			count++
		}
	}
	return count
}

// TestRmClosesTheWindowOnTheRemovedWorktree is the regression test for a teardown
// run from somewhere else, which is how it is normally run: `treewright rm eng-1` from
// the base window used to close nothing, because it looked at the caller's own pane
// rather than at the worktree's, and left a window sitting in a deleted directory.
func TestRmClosesTheWindowOnTheRemovedWorktree(t *testing.T) {
	f := newFixture(t, "")
	wt := f.Worktree("winsome")

	startSession(t, "proj", "MAIN", f.MainDir)
	openWindowOn(t, "proj", "WINSOME", wt.Dir)
	if got := panesOn(t, wt.Dir); got != 1 {
		t.Fatalf("%d panes in the worktree before removal, want 1", got)
	}

	// Deliberately run from the main checkout, not from inside the worktree.
	t.Chdir(f.MainDir)
	r := f.exec("rm", "--yes", "winsome")
	if r.err != nil {
		t.Fatalf("rm: %v\n%s", r.err, r.both())
	}

	if got := panesOn(t, wt.Dir); got != 0 {
		t.Errorf("%d panes still sitting in the deleted %s, want none", got, wt.Dir)
	}
	// And it says so, since a window closing is not something to do silently.
	if !strings.Contains(r.stderr, "closed its tmux window (WINSOME)") {
		t.Errorf("stderr = %q, want the closed window named", r.stderr)
	}
}

// TestRmLeavesOtherWindowsAlone guards the obvious way to overshoot: closing the
// caller's window, or any window that is not the removed worktree's.
func TestRmLeavesOtherWindowsAlone(t *testing.T) {
	f := newFixture(t, "")
	doomed := f.Worktree("doomed")
	keeper := f.Worktree("keeper")

	startSession(t, "proj", "MAIN", f.MainDir)
	openWindowOn(t, "proj", "DOOMED", doomed.Dir)
	openWindowOn(t, "proj", "KEEPER", keeper.Dir)

	t.Chdir(f.MainDir)
	if r := f.exec("rm", "--yes", "doomed"); r.err != nil {
		t.Fatalf("rm: %v\n%s", r.err, r.both())
	}

	if got := panesOn(t, doomed.Dir); got != 0 {
		t.Errorf("%d panes still in the removed worktree, want none", got)
	}
	if got := panesOn(t, keeper.Dir); got != 1 {
		t.Errorf("%d panes in the untouched worktree, want 1 — an unrelated window was closed", got)
	}
	// The base window the command was run from must survive too.
	if got := panesOn(t, f.MainDir); got != 1 {
		t.Errorf("%d panes in the main checkout, want 1 — the caller's own window was closed", got)
	}
}

// TestRmClosesTheWorktreesWindowNotAVisitor is the dangerous half of the lookup
// bug. Two windows stand in the worktree — the worktree's own, and a base window
// whose shell followed a `treewright cd` into it — and the one to close is the
// worktree's. Choosing by position, as this used to, picked whichever was further
// left, so a rearranged status line had `rm --yes` closing the base window: the
// window that keeps the session alive, while the genuinely stranded one stayed
// open on a directory that no longer exists.
func TestRmClosesTheWorktreesWindowNotAVisitor(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	twoWindowsInOneWorktree(t, f)

	t.Chdir(f.MainDir)
	r := f.exec("rm", "--yes", "eng-1")
	if r.err != nil {
		t.Fatalf("rm: %v\n%s", r.err, r.both())
	}

	if !strings.Contains(r.stderr, "closed its tmux window (ENG-1)") {
		t.Errorf("stderr = %q, want the worktree's own window closed", r.stderr)
	}
	got := windowsIn(t, "proj")
	if slices.Contains(got, "ENG-1") {
		t.Errorf("windows in session proj = %v, want ENG-1 gone with its worktree", got)
	}
	if !slices.Contains(got, "MAIN") {
		t.Errorf("windows in session proj = %v, want the visiting window left alone", got)
	}
}

// TestRmWithNoWindowOpenSaysNothing covers the ordinary case of tearing down a
// worktree nobody has a window on: there is nothing to close and nothing to report.
func TestRmWithNoWindowOpenSaysNothing(t *testing.T) {
	f := newFixture(t, "")
	f.Worktree("quiet")

	startSession(t, "proj", "MAIN", f.MainDir)

	t.Chdir(f.MainDir)
	r := f.exec("rm", "--yes", "quiet")
	if r.err != nil {
		t.Fatalf("rm: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "tmux window") {
		t.Errorf("stderr = %q, want no mention of a window that was never open", r.stderr)
	}
}

// TestRmClosesAWindowInAnotherSession covers the window that a session per
// repository does not account for: one opened by hand, or before this repo had a
// session of its own. Its directory is being deleted either way, so leaving it
// behind would strand it exactly as the in-session case would.
//
// It is also the case where closing the window ends its session, which is worth
// saying before an attached client is moved somewhere it did not ask to be.
func TestRmClosesAWindowInAnotherSession(t *testing.T) {
	f := newFixture(t, "")
	wt := f.Worktree("astray")

	startSession(t, "someone-elses-session", "ASTRAY", wt.Dir)

	t.Chdir(f.MainDir)
	r := f.exec("rm", "--yes", "astray")
	if r.err != nil {
		t.Fatalf("rm: %v\n%s", r.err, r.both())
	}

	if got := panesOn(t, wt.Dir); got != 0 {
		t.Errorf("%d panes still sitting in the deleted %s, want none", got, wt.Dir)
	}
	if !strings.Contains(r.stderr, "the last in session someone-elses-session") {
		t.Errorf("stderr = %q, want the ending session named", r.stderr)
	}
}
