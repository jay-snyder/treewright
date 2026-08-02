// Package testenv answers one question for a test that cannot run: is that a
// fact about a developer's machine, or a hole in CI?
//
// Tests here drive real binaries — git, tmux, and each shell the shims target —
// and have always skipped when one was missing. That is right on a laptop, where
// installing every tool the suite touches is a courtesy rather than a
// requirement. It is wrong in CI, which installs all of them on purpose: a skip
// there takes the whole tmux integration, or the fish shim's only syntax check,
// out of the run while the run stays green — which is indistinguishable from the
// tests having passed. An install step that is dropped, renamed, or fails without
// failing the job costs nothing at the moment it breaks and everything later.
//
// So one guard reports both ways. Locally it skips and says what to install.
// Under CI it fails, naming the tool, because there the tool was promised.
package testenv

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// RequireTool reports that a test needs a binary it cannot find.
func RequireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		Unavailablef(t, "%s is not installed", name)
	}
}

// Unavailablef reports that this environment cannot run the test — a tool absent,
// a server that would not start — and decides whether that is a skip or a
// failure. Callers say what is wrong; where it is wrong is this package's business.
//
// CI is the variable every CI system sets, rather than GITHUB_ACTIONS, so that a
// suite run anywhere the tools were installed on purpose is held to the same rule.
func Unavailablef(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") == "" {
		t.Skipf(format, args...)
		return
	}
	t.Fatalf("CI installs what the tests need, so this is a failure rather than a skip: "+format, args...)
}

// PrivateTmuxServer points every tmux call this test makes at a server of its
// own — its own -L label in its own socket directory, both exported through the
// environment tmux and treewright already read — and takes both away
// afterwards. It returns the label, for a test that drives tmux by hand. The
// server itself is not started here: no test pays for one unless it runs
// something that opens a window.
//
// It lives in this package because three packages need the same isolation and
// each had grown its own copy — and because the socket directory is the one
// place the suite legitimately steps outside t.TempDir, which wants exactly one
// exemption with its explanation. The directory goes under /tmp because a unix
// socket path is limited to little over a hundred characters, and a macOS temp
// directory path approaches that by itself.
//
// Removing the directory is what cleans up, rather than asking tmux where its
// socket is: kill-server does not unlink the socket, and a server whose last
// window has already closed is not there to answer the question.
func PrivateTmuxServer(t *testing.T) (label string) {
	t.Helper()
	//nolint:usetesting // t.TempDir gives a path too long for a unix socket on macOS
	dir, err := os.MkdirTemp("/tmp", "tmx")
	if err != nil {
		t.Fatalf("make a tmux socket directory: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", dir)

	// Sanitized because a subtest's name contains "/", which tmux would read as
	// a path — and "." and ":", which its target syntax reads as separators.
	label = strings.Map(func(r rune) rune {
		switch r {
		case '/', '.', ':':
			return '-'
		}
		return r
	}, t.Name())
	t.Setenv("TREEWRIGHT_TMUX_LABEL", label)

	// Registered after both Setenvs, so it runs before either is undone and the
	// kill still finds the server.
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", label, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	return label
}
