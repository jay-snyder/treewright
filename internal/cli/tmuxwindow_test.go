package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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
	label := os.Getenv("TREEMUX_TMUX_LABEL")
	if label == "" {
		t.Fatal("no private tmux server — newFixture is what sets TREEMUX_TMUX_LABEL")
	}
	out, err := exec.Command("tmux", append([]string{"-L", label}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// startSession creates a session holding one window that sits in dir and stays
// open, standing in for a session treemux itself would have created.
func startSession(t *testing.T, session, window, dir string) {
	t.Helper()
	requireTmux(t)
	if out, err := tmuxctl(t, "new-session", "-d", "-s", session, "-n", window, "-c", dir, "sleep 300"); err != nil {
		t.Skipf("cannot start a tmux server here: %v\n%s", err, out)
	}
}

// openWindowOn adds a window sitting in dir to a session that already exists.
func openWindowOn(t *testing.T, session, window, dir string) {
	t.Helper()
	if out, err := tmuxctl(t, "new-window", "-d", "-t", "="+session+":", "-n", window, "-c", dir, "sleep 300"); err != nil {
		t.Fatalf("open a window on %s: %v\n%s", dir, err, out)
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
// run from somewhere else, which is how it is normally run: `treemux rm eng-1` from
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
	if err := os.Chdir(f.MainDir); err != nil {
		t.Fatal(err)
	}
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

	if err := os.Chdir(f.MainDir); err != nil {
		t.Fatal(err)
	}
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

// TestRmWithNoWindowOpenSaysNothing covers the ordinary case of tearing down a
// worktree nobody has a window on: there is nothing to close and nothing to report.
func TestRmWithNoWindowOpenSaysNothing(t *testing.T) {
	f := newFixture(t, "")
	f.Worktree("quiet")

	startSession(t, "proj", "MAIN", f.MainDir)

	if err := os.Chdir(f.MainDir); err != nil {
		t.Fatal(err)
	}
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

	if err := os.Chdir(f.MainDir); err != nil {
		t.Fatal(err)
	}
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
