package cli

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The tests here drive a real tmux server, because the behavior they cover exists
// only in the interaction with one: which window a teardown leaves behind. Nothing
// else in the suite runs under tmux — every other test unsets $TMUX so that no
// windows are opened — and that is precisely why a teardown could orphan the
// window named after the stream without any test noticing.
//
// The server is private to the test: its own socket, its own session, killed
// afterwards, so a developer's own tmux session is never touched.

// tmuxServer starts a private tmux server and points the tmux package at it.
//
// $TMUX carries the socket path, which is how a tmux client finds its server, so
// setting it both makes tmux.Inside() true and directs every call in the package
// under test at this server rather than the user's.
func tmuxServer(t *testing.T, startDir string) (label string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	label = "treemux-test-" + t.Name()

	ctl := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", label}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	// A stale server from an interrupted run would silently host the windows this
	// test then asserts about.
	_, _ = ctl("kill-server")

	// Registered before the server exists so an early failure still tears down,
	// and reading socket at cleanup time rather than now. kill-server does not
	// unlink the socket file, so the test would otherwise leave one behind in the
	// developer's tmp directory on every run.
	var socket string
	t.Cleanup(func() {
		_, _ = ctl("kill-server")
		if socket != "" {
			_ = os.Remove(socket)
		}
	})

	if out, err := ctl("new-session", "-d", "-s", "base", "-c", startDir); err != nil {
		t.Skipf("cannot start a tmux server here: %v\n%s", err, out)
	}
	socket, err := ctl("display-message", "-p", "#{socket_path}")
	if err != nil {
		t.Skipf("cannot read the socket path: %v", err)
	}
	// The pid and session fields are not read by the operations under test; the
	// socket path is what matters.
	t.Setenv("TMUX", socket+",0,0")
	return label
}

// openWindowOn creates a window sitting in dir, running something long-lived so
// the window stays open until the code under test closes it.
func openWindowOn(t *testing.T, label, name, dir string) {
	t.Helper()
	out, err := exec.Command("tmux", "-L", label, "new-window", "-d", "-n", name, "-c", dir, "sleep 300").CombinedOutput()
	if err != nil {
		t.Fatalf("open a window on %s: %v\n%s", dir, err, out)
	}
}

// windowsOn reports how many panes are sitting in dir.
func windowsOn(t *testing.T, label, dir string) int {
	t.Helper()
	out, err := exec.Command("tmux", "-L", label, "list-panes", "-a", "-F", "#{pane_current_path}").CombinedOutput()
	if err != nil {
		// No panes at all is a valid answer: killing the last window ends the
		// server, which reports an error rather than an empty list.
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
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

	label := tmuxServer(t, f.MainDir)
	openWindowOn(t, label, "WINSOME", wt.Dir)
	if got := windowsOn(t, label, wt.Dir); got != 1 {
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

	if got := windowsOn(t, label, wt.Dir); got != 0 {
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

	label := tmuxServer(t, f.MainDir)
	openWindowOn(t, label, "DOOMED", doomed.Dir)
	openWindowOn(t, label, "KEEPER", keeper.Dir)

	if err := os.Chdir(f.MainDir); err != nil {
		t.Fatal(err)
	}
	if r := f.exec("rm", "--yes", "doomed"); r.err != nil {
		t.Fatalf("rm: %v\n%s", r.err, r.both())
	}

	if got := windowsOn(t, label, doomed.Dir); got != 0 {
		t.Errorf("%d panes still in the removed worktree, want none", got)
	}
	if got := windowsOn(t, label, keeper.Dir); got != 1 {
		t.Errorf("%d panes in the untouched worktree, want 1 — an unrelated window was closed", got)
	}
	// The base window the command was run from must survive too.
	if got := windowsOn(t, label, f.MainDir); got != 1 {
		t.Errorf("%d panes in the main checkout, want 1 — the caller's own window was closed", got)
	}
}

// TestRmWithNoWindowOpenSaysNothing covers the ordinary case of tearing down a
// worktree nobody has a window on: there is nothing to close and nothing to report.
func TestRmWithNoWindowOpenSaysNothing(t *testing.T) {
	f := newFixture(t, "")
	f.Worktree("quiet")

	label := tmuxServer(t, f.MainDir)
	_ = label

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
