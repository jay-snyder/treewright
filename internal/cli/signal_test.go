package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jay-snyder/treewright/internal/tmux"
)

// The signal protocol's contract has two halves, and the tests here mirror
// them: in scope, the state lands on the worktree's window and shows wherever
// windows do; out of scope, silence. See the "Agent state" section of
// docs/design-notes.md for why each half is the way it is.

// stateOn reads the agent state off a window the way a status line would —
// through a format, which renders an option nobody set as the empty string
// rather than calling it invalid.
func stateOn(t *testing.T, id string) string {
	t.Helper()
	out, err := tmuxctl(t, "display-message", "-p", "-t", id, "#{"+tmux.AgentStateOption+"}")
	if err != nil {
		t.Fatalf("read the agent state on %s: %v\n%s", id, err, out)
	}
	return out
}

// rawNameOn reads a window's name as tmux spells it, marker included — the one
// reader that must not have the marker stripped, the marker being what it is
// asserting about.
func rawNameOn(t *testing.T, id string) string {
	t.Helper()
	out, err := tmuxctl(t, "display-message", "-p", "-t", id, "#{window_name}")
	if err != nil {
		t.Fatalf("read the name of %s: %v\n%s", id, err, out)
	}
	return out
}

func TestSignalStampsTheWorktreesWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "alpha")
	id := windowIDNamed(t, "proj", "ALPHA")
	t.Chdir(f.DirFor("alpha"))

	r := f.exec("signal", "waiting")
	if r.err != nil {
		t.Fatalf("signal waiting: %v\n%s", r.err, r.both())
	}
	// Silent in scope too: the answer is the stamp on the window, and the hooks
	// running this have no ear for narration.
	if r.both() != "" {
		t.Errorf("output = %q, want nothing on either stream", r.both())
	}
	if got := stateOn(t, id); got != "waiting" {
		t.Errorf("agent state = %q, want %q", got, "waiting")
	}
	// waiting is the one state that must reach a status line, so the name
	// carries it...
	if got := rawNameOn(t, id); got != "!ALPHA" {
		t.Errorf("window name = %q, want the waiting marker on it", got)
	}
	// ...but the marker is display, not identity: everything reading through
	// the package sees the name underneath.
	if got := tmux.Windows("proj")[f.DirFor("alpha")]; got.Name != "ALPHA" || got.State != "waiting" {
		t.Errorf("window through the package = %+v, want the clean name with the state", got)
	}

	// Any other state takes the marker off.
	f.mustRun("signal", "working")
	if got := stateOn(t, id); got != "working" {
		t.Errorf("agent state = %q, want %q", got, "working")
	}
	if got := rawNameOn(t, id); got != "ALPHA" {
		t.Errorf("window name = %q, want the marker gone", got)
	}

	// clear unsets rather than writing a fifth word for the column to show.
	f.mustRun("signal", "clear")
	if got := stateOn(t, id); got != "" {
		t.Errorf("agent state = %q, want it unset", got)
	}

	// A hook is not always standing at the worktree's root, and the checkout is
	// resolved through git, so a subdirectory signals the same window.
	sub := filepath.Join(f.DirFor("alpha"), "deep", "inside")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	t.Chdir(sub)
	f.mustRun("signal", "done")
	if got := stateOn(t, id); got != "done" {
		t.Errorf("agent state after signaling from a subdirectory = %q, want %q", got, "done")
	}
}

// TestSignalReachesTheBaseWindow pins that the base checkout is in scope: an
// agent runs there too — it is where the next piece of work gets asked for —
// and its window carries a state like any worktree's.
func TestSignalReachesTheBaseWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("base")

	f.mustRun("signal", "working")
	if got := stateOn(t, windowIDNamed(t, "proj", "MAIN")); got != "working" {
		t.Errorf("agent state on the base window = %q, want %q", got, "working")
	}
}

// TestSignalIsSilentOutOfScope pins the half of the contract that keeps the
// hooks installable at all: they fire in every session the agent has, so every
// way of being none of treewright's business exits 0 with nothing said.
func TestSignalIsSilentOutOfScope(t *testing.T) {
	t.Run("outside any repository", func(t *testing.T) {
		f := newFixture(t, "")
		t.Chdir(t.TempDir())
		r := f.exec("signal", "waiting")
		if r.err != nil {
			t.Errorf("err = %v, want nil — a hook must not fail its agent", r.err)
		}
		if r.both() != "" {
			t.Errorf("output = %q, want silence", r.both())
		}
	})

	t.Run("in a checkout with no window", func(t *testing.T) {
		f := newFixture(t, "")
		r := f.exec("signal", "waiting") // standing in the main checkout, nothing open
		if r.err != nil {
			t.Errorf("err = %v, want nil", r.err)
		}
		if r.both() != "" {
			t.Errorf("output = %q, want silence", r.both())
		}
	})
}

// TestSignalRejectsAStateOutsideTheVocabulary is the one loud case: being
// invoked wrong is a mistake in the hook's own configuration, and the message
// names the words that would have worked.
func TestSignalRejectsAStateOutsideTheVocabulary(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("signal", "napping")
	if !errors.Is(r.err, ErrUsage) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
	if !strings.Contains(r.stderr, "working, waiting, done, clear") {
		t.Errorf("stderr = %q, want the vocabulary listed", r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing", r.stdout)
	}

	if r := f.exec("signal"); !errors.Is(r.err, ErrUsage) {
		t.Errorf("err = %v, want a usage error for the missing state", r.err)
	}
}

func TestAgentStateShowsInLsAndJSON(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "alpha")
	f.mustRun("new", "beta")

	// Until something signals, the column does not exist: for a repo whose
	// command is an editor, the feature is invisible.
	if out := f.mustRun("ls"); strings.Contains(out, "AGENT") {
		t.Errorf("ls = %q, want no AGENT column before anything has signaled", out)
	}

	t.Chdir(f.DirFor("alpha"))
	f.mustRun("signal", "waiting")

	out := f.mustRun("ls")
	if !strings.Contains(out, "AGENT") || !strings.Contains(out, "waiting") {
		t.Errorf("ls = %q, want the AGENT column carrying the state", out)
	}

	r := f.exec("ls", "--json")
	if r.err != nil {
		t.Fatalf("ls --json: %v\n%s", r.err, r.both())
	}
	states := agentStatesBySlug(t, r.stdout)
	if got := states["alpha"]; got != "waiting" {
		t.Errorf("alpha agent_state = %q, want %q", got, "waiting")
	}
	// The field is always there, empty where nothing has signaled — a
	// consumer's schema should not depend on who happens to be signaling today.
	if got, ok := states["beta"]; !ok || got != "" {
		t.Errorf("beta agent_state = %q (present %v), want the empty string", got, ok)
	}

	// And once the last state is cleared, the column goes with it.
	f.mustRun("signal", "clear")
	if out := f.mustRun("ls"); strings.Contains(out, "AGENT") {
		t.Errorf("ls = %q, want the AGENT column gone once nothing is signaling", out)
	}
}

// agentStatesBySlug reads slug → agent_state out of `ls --json`.
func agentStatesBySlug(t *testing.T, jsonOut string) map[string]string {
	t.Helper()
	var rows []struct {
		Slug       string `json:"slug"`
		AgentState string `json:"agent_state"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, jsonOut)
	}
	states := make(map[string]string, len(rows))
	for _, row := range rows {
		states[row.Slug] = row.AgentState
	}
	return states
}

// TestAHeldOpenWindowShedsItsAgentState covers the one window that outlives its
// agent. A failing command's window is deliberately kept so its output stays
// readable, and left alone it would keep the dead agent's last claim: a
// "working" in the ls table for as long as the window sat unread, a waiting
// marker flagging a window whose agent is gone. The wrapper's tail takes both
// off, and it runs before the "press Enter" report — so once that is on
// screen, the cleanup has already happened.
func TestAHeldOpenWindowShedsItsAgentState(t *testing.T) {
	requireTmux(t)
	// The command stands in for an agent that signaled and then died: it stamps
	// its own window and marks its own name, then fails. Targets are the pane's
	// own, through $TMUX_PANE, exactly as the wrapper's cleanup aims its calls.
	f := newFixture(t,
		"command = 'tmux set-window-option -t $TMUX_PANE "+tmux.AgentStateOption+" working"+
			" && tmux rename-window -t $TMUX_PANE !BOOM && exit 3'\n")
	f.mustRun("new", "boom")

	id := heldWindowID(t, "BOOM")
	waitForPane(t, id, "press Enter")
	if got := stateOn(t, id); got != "" {
		t.Errorf("agent state on the held window = %q, want it shed with the agent", got)
	}
	if got := rawNameOn(t, id); got != "BOOM" {
		t.Errorf("held window's name = %q, want the waiting marker shed too", got)
	}
}

// heldWindowID finds a window by name while its name is mid-flight: the command
// above marks it, the wrapper unmarks it, and windowIDNamed would fatal on
// whichever spelling it happened to miss.
func heldWindowID(t *testing.T, name string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := tmuxctl(t, "list-windows", "-t", "=proj", "-F", "#{window_name}\t#{window_id}")
		if err == nil {
			for line := range strings.SplitSeq(out, "\n") {
				if n, id, ok := strings.Cut(line, "\t"); ok && strings.TrimPrefix(n, tmux.WaitingMarker) == name {
					return id
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no window named %s (marked or not) ever appeared in session proj", name)
	return ""
}

// waitForPane polls a pane's contents for a line a background command is
// expected to print, the way waitForFile waits for a file.
func waitForPane(t *testing.T, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var pane string
	for time.Now().Before(deadline) {
		pane, _ = tmuxctl(t, "capture-pane", "-p", "-t", id)
		if strings.Contains(pane, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pane %s never showed %q — it shows:\n%s", id, want, pane)
}
