package main

import (
	"testing"

	"github.com/jay-snyder/treewright/internal/cli"
)

// TestEveryWaitOutcomeHasACodeOfItsOwn: an agent's next step is built on
// which of these it got, so no two may share a number, and none may be one
// that already means something else — 0 the command ran, 1 an error, 2 being
// invoked wrong.
func TestEveryWaitOutcomeHasACodeOfItsOwn(t *testing.T) {
	want := map[cli.WaitOutcome]int{cli.WaitDismissed: 3, cli.WaitTimedOut: 4, cli.WaitRunFailed: 5}
	seen := map[int]bool{}
	for outcome, code := range want {
		got := waitExit(outcome)
		if got != code {
			t.Errorf("outcome %d exits %d, want %d", outcome, got, code)
		}
		if got <= 2 || seen[got] {
			t.Errorf("outcome %d exits %d, which is taken", outcome, got)
		}
		seen[got] = true
	}
}
