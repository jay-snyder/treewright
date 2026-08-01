package cli

import (
	"os"
	"slices"
	"strings"

	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// `signal` is the agent-state protocol's one verb: an agent's own hooks run
// `treewright signal waiting` and the window belonging to the worktree they are
// standing in is stamped with the state, shown in the AGENT column of `ls` and,
// for waiting, as a marker on the window's name. treewright never sets a state
// itself — agents write, treewright displays — and nothing clears one on focus:
// switching to a waiting window is arrival, not help, and the agent's own next
// transition is the truth.
//
// The state is a tmux window option rather than a file for the reason given on
// the option's declaration in internal/tmux: the agent is the window's own
// command, so the agent dying is the option evaporating, and a crashed agent
// cannot leave a stale claim behind.

// The closed vocabulary, chosen from the human's chair — each state answers
// "what does this window want from me?". Closed like the branch-prefix list is:
// every consumer needs to know what a state means, and a vocabulary nobody
// shares is a column nobody can read.
const (
	stateWorking = "working" // busy; leave it alone
	stateWaiting = "waiting" // blocked on you — a permission prompt, a question
	stateDone    = "done"    // turn finished; there is a result to look at
	stateClear   = "clear"   // no agent state: the agent left while its window lives on
)

var signalStates = []string{stateWorking, stateWaiting, stateDone, stateClear}

// cmdSignal records an agent's state on its worktree's window.
//
// Being invoked wrong is the only loud failure. Everything else — no tmux, no
// registered repo, no window on this worktree — exits 0 in silence, because the
// hooks that run this fire in every session the agent has, in plain terminals
// and repositories treewright has never heard of, and each of those is out of
// scope rather than broken. A hook that warns about out-of-scope turns every
// such session into a nagging one, which is how integrations get ripped out.
// This is the one command deliberately outside the "a background failure needs
// somewhere to be reported" rule: a no-op signal is not a failure.
func cmdSignal(_ *Env, args []string) error {
	positional, err := parseArgs("signal", args, nil, nil, 1)
	if err != nil {
		return err
	}
	state := at(positional, 0)
	if state == "" {
		return usageErrorf("signal", "a state is required (one of: %s)", strings.Join(signalStates, ", "))
	}
	if !slices.Contains(signalStates, state) {
		return usageErrorf("signal", "unknown state %q (one of: %s)", state, strings.Join(signalStates, ", "))
	}

	w, ok := signalTarget()
	if !ok {
		return nil
	}

	value := state
	if state == stateClear {
		// Cleared is unset, not written as "" — see SetAgentState.
		value = ""
	}
	// Best-effort from here down, in keeping with the silence above: the window
	// was there a moment ago, and one that has closed since needs no state.
	_ = tmux.SetAgentState(w.ID, value)
	if w.Stamped() {
		// Only a window treewright opened gets its name decorated. One the user
		// opened by hand on a worktree's directory still carries the state option —
		// harmless, and their status line may read it — but its name is theirs.
		_ = tmux.SetWaitingMarker(w, state == stateWaiting)
	}
	return nil
}

// signalTarget finds the window owned by the checkout the caller is standing
// in, reporting ok=false for every way the caller can be out of scope. A bool
// rather than an error, because no branch here is one: each is a session that
// is none of treewright's business, and cmdSignal answers all of them with the
// same silence.
//
// The checkout is named by git's own spelling of its root — the same fully
// resolved path a window's worktree stamp holds — so the lookup lands however
// the hook's working directory was spelled, and from a subdirectory of the
// worktree just the same.
func signalTarget() (tmux.Window, bool) {
	if !tmux.Available() {
		return tmux.Window{}, false
	}
	cfg, err := resolveConfig("")
	if err != nil {
		return tmux.Window{}, false
	}
	wd, err := os.Getwd()
	if err != nil {
		return tmux.Window{}, false
	}
	top, err := (git.Repo{Dir: wd}).TopLevel()
	if err != nil {
		return tmux.Window{}, false
	}
	w, ok := tmux.Windows(sessionFor(cfg))[top]
	return w, ok
}
