package cli

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/tmux"
)

// The rule close enforces, decided by the window the command is running in:
// from a worktree or scratch window only that window, the base window from none
// of treewright's windows, another window from the base window only once its
// agent has yielded, and anything from outside treewright's windows. Each test
// stands in a real window of the private server, the way an agent does.

// standIn puts the caller in a window of the test's private server, the way an
// agent running there is: $TMUX naming that server's socket, which the guard
// compares a raw tmux line against, and $TMUX_PANE one of the window's panes,
// which is what every command asks to find the window it is running in.
func standIn(t *testing.T, session, window string) {
	t.Helper()
	sockets, err := tmuxctl(t, "list-sessions", "-F", "#{socket_path}")
	if err != nil {
		t.Fatalf("find the private server's socket: %v\n%s", err, sockets)
	}
	t.Setenv("TMUX", strings.Split(sockets, "\n")[0]+",0,0")
	t.Setenv("TMUX_PANE", paneIn(t, session, window))
}

// yieldedOn reads a window's yield the way a status line would.
func yieldedOn(t *testing.T, id string) bool {
	t.Helper()
	out, err := tmuxctl(t, "display-message", "-p", "-t", id, "#{"+tmux.YieldedOption+"}")
	if err != nil {
		t.Fatalf("read the yield on %s: %v\n%s", id, err, out)
	}
	return out != ""
}

// everyKind is a repository with a window of every kind treewright opens: the
// base window, named main after its branch, the eng-1 and eng-2 worktrees'
// windows, and a scratch window called ask.
func everyKind(t *testing.T) *fixture {
	t.Helper()
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")
	f.mustRun("scratch", "ask")
	return f
}

// TestAScratchWindowCannotCloseTheBaseWindow is the incident: a scratch agent in
// the main checkout, setting out to close its own window, worked out from where
// it stood that its window was the base window and ran `close base`. It is
// refused by either of the names it answers to, and the refusal names the bare
// close, which needs no name to get right.
func TestAScratchWindowCannotCloseTheBaseWindow(t *testing.T) {
	f := everyKind(t)
	standIn(t, "proj", "ask")

	for _, name := range []string{"base", "main"} {
		r := f.exec("close", "--repo", "proj", name)
		if r.err == nil {
			t.Fatalf("close %s from the scratch window succeeded\n%s", name, r.both())
		}
		msg := flat(r.err.Error())
		for _, want := range []string{
			"base is the main checkout's window, and this command is running in ask's",
			"only that window can be closed",
			"close this one " + canonicalName + " close",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("close %s: error = %q, want it to say %q", name, msg, want)
			}
		}
	}
	if got := windowsIn(t, "proj"); !slices.Contains(got, "main") || !slices.Contains(got, "ask") {
		t.Errorf("windows = %v, want the base window and the caller's both left open", got)
	}
}

// TestAWorktreeWindowCannotCloseAnotherWindow: not another worktree's, and not
// a scratch window, whatever it was told the window was called.
func TestAWorktreeWindowCannotCloseAnotherWindow(t *testing.T) {
	f := everyKind(t)
	writeRecord(t, f, "ask", "ask-conversation")
	standIn(t, "proj", "eng-1")

	for _, name := range []string{"eng-2", "ask"} {
		r := f.exec("close", name)
		if r.err == nil {
			t.Fatalf("close %s from eng-1's window succeeded\n%s", name, r.both())
		}
		if msg := flat(r.err.Error()); !strings.Contains(msg, name+" is not the window this command is running in, which is eng-1") {
			t.Errorf("close %s: error = %q, want the two windows named", name, msg)
		}
	}
	if got := windowsIn(t, "proj"); len(got) != 4 {
		t.Errorf("windows = %v, want all four left open", got)
	}
	// A scratch session's record is that session, and goes nowhere either.
	if !exists(filepath.Join(f.MainDir, ".git", "treewright", "sessions", "ask")) {
		t.Error("a refused close ended the scratch session's record")
	}
}

// TestAWorktreeWindowClosesItself, by its slug as before and with nothing named
// at all — the form that cannot get the name wrong.
func TestAWorktreeWindowClosesItself(t *testing.T) {
	f := everyKind(t)

	standIn(t, "proj", "eng-1")
	if r := f.exec("close", "eng-1"); r.err != nil {
		t.Fatalf("close eng-1 from its own window: %v\n%s", r.err, r.both())
	}
	standIn(t, "proj", "eng-2")
	r := f.exec("close")
	if r.err != nil {
		t.Fatalf("a bare close from eng-2's window: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "closing tmux window eng-2") {
		t.Errorf("stderr = %q, want the window it closed named", r.stderr)
	}
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"main", "ask"}) {
		t.Errorf("windows = %v, want only the two worktree windows gone", got)
	}
	if !f.Exists("eng-1") || !f.Exists("eng-2") {
		t.Error("close removed a worktree, which is rm's job")
	}
}

// TestABareCloseEndsTheScratchSessionItRunsIn, record and all, as close by name
// does — and with --repo, which the agent guide says to pass always, naming the
// window's own repository.
func TestABareCloseEndsTheScratchSessionItRunsIn(t *testing.T) {
	f := everyKind(t)
	writeRecord(t, f, "ask", "ask-conversation")
	standIn(t, "proj", "ask")

	if r := f.exec("close", "--repo", "proj"); r.err != nil {
		t.Fatalf("a bare close from the scratch window: %v\n%s", r.err, r.both())
	}
	if got := windowsIn(t, "proj"); slices.Contains(got, "ask") {
		t.Errorf("windows = %v, want the scratch window closed", got)
	}
	if exists(filepath.Join(f.MainDir, ".git", "treewright", "sessions", "ask")) {
		t.Error("the scratch session's record survived its close")
	}
}

// TestABareCloseNeedsAWindowOfItsOwn: outside treewright's windows there is
// nothing it could mean, and it is the usage error it always was; and --repo
// naming another repository is a request this window cannot answer.
func TestABareCloseNeedsAWindowOfItsOwn(t *testing.T) {
	f := everyKind(t)

	if r := f.exec("close"); !errors.Is(r.err, ErrUsage) {
		t.Errorf("a bare close outside treewright's windows: err = %v, want a usage error", r.err)
	}

	addConfig(t, f.registry, "other", "main_dir = '"+t.TempDir()+"'\nbase_branch = 'main'\n")
	standIn(t, "proj", "eng-1")
	if r := f.exec("close", "--repo", "other"); r.err == nil {
		t.Errorf("a bare close naming another repository closed something\n%s", r.both())
	}
	if got := windowsIn(t, "proj"); !slices.Contains(got, "eng-1") {
		t.Errorf("windows = %v, want eng-1 left open", got)
	}
}

// TestTheBaseWindowCannotCloseItself, by name or bare. A person closes it from
// a shell outside treewright's windows, or by quitting its agent, and that is
// what the refusal says.
func TestTheBaseWindowCannotCloseItself(t *testing.T) {
	f := everyKind(t)
	standIn(t, "proj", "main")

	for _, args := range [][]string{{"close", "base"}, {"close"}} {
		r := f.exec(args...)
		if r.err == nil {
			t.Fatalf("%v from the base window succeeded\n%s", args, r.both())
		}
		msg := flat(r.err.Error())
		if !strings.Contains(msg, "the base window is never closed from inside treewright's windows") ||
			!strings.Contains(msg, "close it from a shell outside treewright's windows") {
			t.Errorf("%v: error = %q, want the rule and the way a person closes it", args, msg)
		}
	}
	if got := windowsIn(t, "proj"); !slices.Contains(got, "main") {
		t.Errorf("windows = %v, want the base window left open", got)
	}
}

// TestTheBaseWindowClosesAnotherWindowOnlyOnceItsAgentHasYielded: closing a
// window stops its agent, and only that agent knows whether that loses
// anything. So the refusal says how to ask it, and once it has yielded the
// close goes through, worktree and scratch window alike.
func TestTheBaseWindowClosesAnotherWindowOnlyOnceItsAgentHasYielded(t *testing.T) {
	f := everyKind(t)

	for _, name := range []string{"eng-1", "ask"} {
		standIn(t, "proj", "main")
		r := f.exec("close", "--repo", "proj", name)
		if r.err == nil {
			t.Fatalf("close %s from the base window with no yield succeeded\n%s", name, r.both())
		}
		msg := flat(r.err.Error())
		for _, want := range []string{
			name + "'s agent has not yielded its window",
			canonicalName + " send --repo proj " + name,
			"run treewright yield",
			"leaves its window to a person",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("close %s: error = %q, want it to say %q", name, msg, want)
			}
		}
		if got := windowsIn(t, "proj"); !slices.Contains(got, name) {
			t.Fatalf("windows = %v, want %s left open by the refusal", got, name)
		}

		standIn(t, "proj", name)
		f.mustRun("yield")
		standIn(t, "proj", "main")
		if r := f.exec("close", "--repo", "proj", name); r.err != nil {
			t.Fatalf("close %s from the base window once it yielded: %v\n%s", name, r.err, r.both())
		}
		if got := windowsIn(t, "proj"); slices.Contains(got, name) {
			t.Errorf("windows = %v, want %s closed once its agent yielded", got, name)
		}
	}
}

// TestTheBaseWindowAsksANamedAgentThroughItsOwnMessaging: the question goes the
// way any message does — the agent's own messaging to the name on its window
// where it has one, and send only where it does not.
func TestTheBaseWindowAsksANamedAgentThroughItsOwnMessaging(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	f.mustRun("base")
	f.mustRun("new", "alpha")
	waitForStart(t, log, "start:--name proj-alpha")
	standIn(t, "proj", "main")

	r := f.exec("close", "--repo", "proj", "alpha")
	if r.err == nil {
		t.Fatalf("close alpha with no yield succeeded\n%s", r.both())
	}
	msg := flat(r.err.Error())
	if want := `SendMessage to proj-alpha: "your window is about to be closed`; !strings.Contains(msg, want) {
		t.Errorf("error = %q, want it to say %q", msg, want)
	}
	if strings.Contains(msg, "treewright send") {
		t.Errorf("error = %q, want the agent's own route rather than send", msg)
	}
}

// TestYieldMarksOnlyTheCallersOwnWindow: found from the pane, so no agent can
// yield a window but its own — and ls --json reports it, which is how an agent
// that asked through send reads the answer.
func TestYieldMarksOnlyTheCallersOwnWindow(t *testing.T) {
	f := everyKind(t)
	standIn(t, "proj", "eng-1")

	r := f.exec("yield")
	if r.err != nil {
		t.Fatalf("yield: %v\n%s", r.err, r.both())
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing", r.stdout)
	}
	for name, want := range map[string]bool{"eng-1": true, "eng-2": false, "ask": false, "main": false} {
		if got := yieldedOn(t, windowIDNamed(t, "proj", name)); got != want {
			t.Errorf("yield on %s = %v, want %v", name, got, want)
		}
	}

	var rows []struct {
		Slug          string `json:"slug"`
		WindowYielded bool   `json:"window_yielded"`
	}
	if err := json.Unmarshal([]byte(f.mustRun("ls", "--json")), &rows); err != nil {
		t.Fatalf("ls --json: %v", err)
	}
	for _, row := range rows {
		if row.WindowYielded != (row.Slug == "eng-1") {
			t.Errorf("ls --json: %s window_yielded = %v", row.Slug, row.WindowYielded)
		}
	}
}

// TestYieldNeedsAWindowItCouldBeClosedFrom: run where it means nothing, it says
// so and fails — it is run on purpose rather than by a hook, so signal's
// silence is not owed. The base window has nothing to yield, never being closed
// from inside treewright's windows.
func TestYieldNeedsAWindowItCouldBeClosedFrom(t *testing.T) {
	f := everyKind(t)
	if r := f.exec("yield"); r.err == nil {
		t.Error("yield outside treewright's windows succeeded")
	}
	standIn(t, "proj", "main")
	if r := f.exec("yield"); r.err == nil {
		t.Error("yield in the base window succeeded")
	}
	if yieldedOn(t, windowIDNamed(t, "proj", "main")) {
		t.Error("the base window was marked")
	}
}

// TestSignalWorkingTakesTheYieldBack: a yield answers for the work the agent
// had when it gave it, and a new prompt is new work — the question asking for
// the yield included, so a mark is always the answer to the latest one. done
// and waiting are the turn pausing, which changes nothing about it; clear is
// the agent leaving.
func TestSignalWorkingTakesTheYieldBack(t *testing.T) {
	f := everyKind(t)
	id := windowIDNamed(t, "proj", "eng-1")
	standIn(t, "proj", "eng-1")

	for _, step := range []struct {
		signal string
		want   bool
	}{{"done", true}, {"waiting", true}, {"working", false}, {"clear", false}} {
		f.mustRun("yield")
		f.mustRun("signal", step.signal)
		if got := yieldedOn(t, id); got != step.want {
			t.Errorf("after yield then signal %s, yielded = %v, want %v", step.signal, got, step.want)
		}
	}
}

// TestRmYesFromTheBaseWindowRefusesBeforeRemovingAnything when the window it
// would close has not yielded: --yes asked for the window to be closed, and a
// refusal that came after the removal would leave the half that cannot be
// undone done. Once the agent has yielded, the teardown goes through whole.
func TestRmYesFromTheBaseWindowRefusesBeforeRemovingAnything(t *testing.T) {
	f := everyKind(t)
	standIn(t, "proj", "main")

	r := f.exec("rm", "--yes", "--repo", "proj", "eng-1")
	if r.err == nil {
		t.Fatalf("rm --yes with no yield succeeded\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "nothing was removed") || !strings.Contains(msg, "run treewright yield") {
		t.Errorf("error = %q, want it to say nothing was removed and how to ask", msg)
	}
	if !f.Exists("eng-1") || f.Git(f.MainDir, "branch", "--list", f.BranchFor("eng-1")) == "" {
		t.Fatal("a refused rm --yes removed the worktree or its branch")
	}

	standIn(t, "proj", "eng-1")
	f.mustRun("yield")
	standIn(t, "proj", "main")
	if r := f.exec("rm", "--yes", "--repo", "proj", "eng-1"); r.err != nil {
		t.Fatalf("rm --yes once eng-1 yielded: %v\n%s", r.err, r.both())
	}
	if f.Exists("eng-1") {
		t.Error("eng-1 is still there")
	}
	if got := windowsIn(t, "proj"); slices.Contains(got, "eng-1") {
		t.Errorf("windows = %v, want eng-1's window closed", got)
	}
}

// TestRmYesFromAnotherWorktreesWindowRefusesBeforeRemovingAnything: the window
// is not the caller's to close, and --yes asked for exactly that.
func TestRmYesFromAnotherWorktreesWindowRefusesBeforeRemovingAnything(t *testing.T) {
	f := everyKind(t)
	standIn(t, "proj", "eng-1")

	r := f.exec("rm", "--yes", "eng-2")
	if r.err == nil {
		t.Fatalf("rm --yes eng-2 from eng-1's window succeeded\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "nothing was removed") {
		t.Errorf("error = %q, want it to say nothing was removed", msg)
	}
	if !f.Exists("eng-2") {
		t.Error("a refused rm --yes removed the worktree")
	}
	if got := windowsIn(t, "proj"); !slices.Contains(got, "eng-2") {
		t.Errorf("windows = %v, want eng-2's window left open", got)
	}
}

// TestRmFromTheBaseWindowLeavesAnUnyieldedWindowWithTheWayToAsk: with nobody at
// a terminal, rm without --yes used to name the close to run. From the base
// window that close would be refused, so the question comes first.
func TestRmFromTheBaseWindowLeavesAnUnyieldedWindowWithTheWayToAsk(t *testing.T) {
	f := everyKind(t)
	noTTY(t)
	standIn(t, "proj", "main")

	r := f.exec("rm", "--repo", "proj", "eng-1")
	if r.err != nil {
		t.Fatalf("rm: %v\n%s", r.err, r.both())
	}
	msg := flat(r.stderr)
	for _, want := range []string{
		"it stays open, because its agent has not yielded it",
		"run treewright yield",
		canonicalName + " close --repo proj eng-1",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr = %q, want it to say %q", msg, want)
		}
	}
	if got := windowsIn(t, "proj"); !slices.Contains(got, "eng-1") {
		t.Errorf("windows = %v, want eng-1's window left open", got)
	}
}

// TestACallerOutsideTreewrightsWindowsClosesAnything: a plain shell, or a
// window treewright did not open, is where a person closes what the rule keeps
// agents from — the base window included, and with no yield asked for.
func TestACallerOutsideTreewrightsWindowsClosesAnything(t *testing.T) {
	f := everyKind(t)
	startSession(t, "mine", "hand-made", f.MainDir)

	if r := f.exec("close", "eng-1"); r.err != nil {
		t.Fatalf("close eng-1 from a plain shell: %v\n%s", r.err, r.both())
	}
	standIn(t, "mine", "hand-made")
	for _, name := range []string{"eng-2", "ask", "base"} {
		if r := f.exec("close", "--repo", "proj", name); r.err != nil {
			t.Fatalf("close %s from a window treewright did not open: %v\n%s", name, r.err, r.both())
		}
	}
	if got := windowsIn(t, "proj"); len(got) != 0 {
		t.Errorf("windows = %v, want every one closed", got)
	}
}
