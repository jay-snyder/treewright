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
