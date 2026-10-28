package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jay-snyder/treewright/internal/gittest"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// The name every agent treewright launches answers to: what it is, where it is
// read, and the one collision that is refused rather than lived with. See
// names.go.

// waitForStart waits for the agent stub's log to hold one start exactly as
// given, since each window's command runs on its own time and a substring
// would let "--name proj" pass for "--name proj-alpha".
func waitForStart(t *testing.T, log, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(startsOf(t, log), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("no agent was started as %q — the starts were %q", want, startsOf(t, log))
}

// TestEveryAgentIsNamedAfterItsRepository is the scheme: the repository's
// config name and the slug for a worktree, the same with the scratch name for a
// scratch window, and the config name alone for the base window. The log is
// what the agent was actually handed, and ls --json reads the same names back
// off the windows.
func TestEveryAgentIsNamedAfterItsRepository(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	f.mustRun("new", "alpha")
	f.mustRun("scratch", "ask")
	f.mustRun("base")

	for _, want := range []string{"start:--name proj-alpha", "start:--name proj-ask", "start:--name proj"} {
		waitForStart(t, log, want)
	}
	rows := windowRows(t, f)
	for name, want := range map[string]string{"alpha": "proj-alpha", "ask": "proj-ask", baseName: "proj"} {
		if got := rows[name].AgentName; got != want {
			t.Errorf("%s's agent_name = %q, want %q", name, got, want)
		}
	}
}

// TestAnAgentLaunchedWithoutANameIsListedWithout: a template with no {name} is
// a repository whose agent is given none, so there is nothing to report — and
// an empty agent_name is what tells a reader to reach it with send.
func TestAnAgentLaunchedWithoutANameIsListedWithout(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "alpha")

	row := windowRows(t, f)["alpha"]
	if row.WindowID == "" {
		t.Fatal("no window open on alpha, so this proves nothing")
	}
	if row.AgentName != "" {
		t.Errorf("agent_name = %q, want none for an agent launched with no name", row.AgentName)
	}
}

// TestResumingNamesTheAgentHoweverItStartsOne: resume_command, --fresh and
// restore each start an agent, and each gives it the name `new` would have —
// which is also how an agent started before names were given gets one.
func TestResumingNamesTheAgentHoweverItStartsOne(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	f.Worktree("alpha")
	f.Worktree("beta")
	f.Worktree("gamma")

	f.mustRun("resume", "alpha")
	waitForStart(t, log, "start:--continue --name proj-alpha")

	f.mustRun("resume", "beta", "--fresh")
	waitForStart(t, log, "start:--name proj-beta")

	f.mustRun("restore", "-d")
	waitForStart(t, log, "start:--continue --name proj-gamma")
	// The base window has no recorded conversation yet, so it resumes by
	// directory like a worktree — under the repository's own name.
	waitForStart(t, log, "start:--continue --name proj")
}

// TestAResumedConversationKeepsTheWindowsName: resuming a recorded
// conversation by id names it too, and the record and the name are two things
// — the base window's conversation is recorded as "base" and its agent is the
// repository's.
func TestAResumedConversationKeepsTheWindowsName(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	writeRecord(t, f, "base", "base-conversation")

	f.mustRun("resume", "base")
	waitForStart(t, log, "start:--resume base-conversation --name proj")
}

// TestANameAndAPromptCannotFillEachOther: both are text somebody chose, and
// neither may be read as the other's placeholder. A brief quoting this
// repository's own documentation mentions {name}, and a slug may hold braces.
func TestANameAndAPromptCannotFillEachOther(t *testing.T) {
	got, err := fillTemplate("agent --name {name} {prompt}", "command", "see {name} in the docs", "proj-{prompt}")
	if err != nil {
		t.Fatal(err)
	}
	if want := `agent --name 'proj-{prompt}' 'see {name} in the docs'`; got != want {
		t.Errorf("filled = %q, want %q", got, want)
	}
	// And with no prompt, the placeholder and its space go as before, while the
	// name still arrives.
	if got, _ := fillTemplate("agent --name {name} {prompt}", "command", "", "proj-alpha"); got != "agent --name 'proj-alpha'" {
		t.Errorf("filled without a prompt = %q, want the name and no trailing placeholder", got)
	}
}

// ---- two repositories, one name -------------------------------------------

// collidingRepo registers a second repository under a name that is this one's
// plus a hyphen, which is the only shape two repositories' agent names can
// collide in.
func collidingRepo(t *testing.T, f *fixture, body string) *gittest.Repo {
	t.Helper()
	repo := gittest.New(t)
	addConfig(t, f.registry, "proj-v2", "main_dir = '"+repo.MainDir+"'\nbase_branch = 'main'\n"+body)
	return repo
}

// TestTwoRepositoriesNeverGiveOutOneAgentName: proj's worktree v2 would be
// proj-v2, the name of the other repository's base window, and proj's v2-fix
// would be proj-v2-fix, the name of its worktree fix. A message to either name
// would reach one agent of two with nothing to say which, so the name is
// refused when it would be given out — before anything is created.
func TestTwoRepositoriesNeverGiveOutOneAgentName(t *testing.T) {
	f := newFixture(t, "")
	other := collidingRepo(t, f, "")
	other.Worktree("fix")

	for _, tc := range []struct {
		args  []string
		whose string
	}{
		{[]string{"new", "v2"}, "proj-v2's base window"},
		{[]string{"new", "v2-fix"}, "proj-v2's worktree fix"},
		{[]string{"scratch", "v2-fix"}, "proj-v2's worktree fix"},
		{[]string{"move", "v2"}, "proj-v2's base window"},
	} {
		r := f.exec(tc.args...)
		if r.err == nil {
			t.Errorf("%v was allowed, want the name refused", tc.args)
			continue
		}
		if msg := flat(r.err.Error()); !strings.Contains(msg, "already the name of the agent in "+tc.whose) {
			t.Errorf("%v: error = %q, want it to name %s", tc.args, msg, tc.whose)
		}
	}
	if f.Exists("v2") || f.Exists("v2-fix") {
		t.Error("a worktree was created for a name that was refused")
	}

	// A name nobody else answers to is given out as ever.
	if r := f.exec("new", "v3"); r.err != nil {
		t.Errorf("new v3: %v — a name nobody answers to was refused\n%s", r.err, r.both())
	}
}

// TestARepositoryThatNamesNoAgentsTakesNoNames: the collision is between two
// agents, so a repository whose templates give its agents no names cannot be
// on either side of one.
func TestARepositoryThatNamesNoAgentsTakesNoNames(t *testing.T) {
	f := newFixture(t, "")
	collidingRepo(t, f, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")

	if r := f.exec("new", "v2"); r.err != nil {
		t.Errorf("new v2: %v — refused for a name the other repository never gives out\n%s", r.err, r.both())
	}
}

// ---- reaching an agent that has a name -------------------------------------

// TestAnUndeliveredPromptNamesTheAgentToMessage: a prompt aimed at a window
// already open never reaches its agent, and the warning says how to — by the
// agent's own messaging and its name, where its module has one.
func TestAnUndeliveredPromptNamesTheAgentToMessage(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	f.mustRun("new", "alpha")
	waitForStart(t, log, "start:--name proj-alpha")

	r := f.exec("resume", "alpha", "--prompt", "address the review")
	if r.err != nil {
		t.Fatalf("resume: %v\n%s", r.err, r.both())
	}
	msg := flat(r.stderr)
	for _, want := range []string{"prompt not delivered", "with SendMessage", "to proj-alpha"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr = %q, want it to say %q", msg, want)
		}
	}
	if strings.Contains(msg, "send --repo") {
		t.Errorf("stderr = %q, want the agent's own route rather than send", msg)
	}
}

// TestAnUndeliveredPromptNamesSendWithoutAModule: the default command names
// the agent, but with no agent key there is no module to say what it messages
// with — so the warning names send, which reaches it all the same.
func TestAnUndeliveredPromptNamesSendWithoutAModule(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")
	log := agentStub(t, false)
	f.mustRun("new", "alpha")
	waitForStart(t, log, "start:--name proj-alpha")

	r := f.exec("resume", "alpha", "--prompt", "address the review")
	if msg := flat(r.stderr); !strings.Contains(msg, "prompt not delivered") || !strings.Contains(msg, "treewright send --repo proj alpha") {
		t.Errorf("stderr = %q, want send named as the way to the agent", msg)
	}
}

// TestReusingAnOpenScratchSessionTypesNothingAtANamedAgent: --reuse used to
// type the prompt at an open window, which is the one route that can land as
// the answer to whatever the agent's screen is asking. An agent with a name and
// messaging of its own is reached that way instead, so nothing is typed, the
// prompt is reported undelivered with the name to send it to, and the window is
// still brought forward.
func TestReusingAnOpenScratchSessionTypesNothingAtANamedAgent(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	f.mustRun("scratch", "ask")
	waitForStart(t, log, "start:--name proj-ask")
	id := windowIDNamed(t, "proj", "ask")

	r := f.exec("scratch", "--reuse", "ask", "--prompt", "look again at the retry loop")
	if r.err != nil {
		t.Fatalf("scratch --reuse: %v\n%s", r.err, r.both())
	}
	msg := flat(r.stderr)
	for _, want := range []string{"prompt not delivered", "scratch window ask is already open", "with SendMessage", "to proj-ask"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr = %q, want it to say %q", msg, want)
		}
	}
	// What send would have printed, had it run: the pane, and what it typed.
	if strings.Contains(msg, "sent to") {
		t.Errorf("stderr = %q, want nothing reported as sent", msg)
	}
	time.Sleep(100 * time.Millisecond)
	if pane, _ := tmuxctl(t, "capture-pane", "-p", "-t", id); strings.Contains(pane, "retry loop") {
		t.Errorf("the prompt was typed at the window anyway — it shows:\n%s", pane)
	}
	if starts := startsOf(t, log); len(starts) != 1 {
		t.Errorf("starts = %q, want the one agent and no second", starts)
	}
}

// TestTheGuardNamesTheRouteTheAgentHas: the refusal's one-line handoff goes to
// the agent standing in the refused worktree, by its own messaging where it has
// a name to be reached at — and the brief route is unchanged either way.
func TestTheGuardNamesTheRouteTheAgentHas(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	f.Worktree("alpha")
	f.mustRun("new", "beta")
	waitForStart(t, log, "start:--name proj-beta")

	r := f.guard(f.DirFor("alpha"), "Edit", map[string]any{"file_path": f.DirFor("beta") + "/x.go"})
	if !errors.Is(r.err, ErrRefused) {
		t.Fatalf("err = %v, want the edit refused\n%s", r.err, r.both())
	}
	msg := flat(r.stderr)
	for _, want := range []string{
		"treewright resume beta --prompt-file /tmp/beta-brief.md",
		`SendMessage to proj-beta: "read /tmp/beta-brief.md in full"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal = %q, want it to say %q", msg, want)
		}
	}
	if strings.Contains(msg, "treewright send") {
		t.Errorf("refusal = %q, want the agent's own route rather than send", msg)
	}
}

// TestAHeldOpenWindowShedsItsAgentName: a window held open on a dead agent's
// output has nobody at the name it was stamped with, so the wrapper takes the
// name off with the state — and the reader is pointed at send, which knows a
// held-open window when it sees one, rather than at an address nobody answers.
func TestAHeldOpenWindowShedsItsAgentName(t *testing.T) {
	requireTmux(t)
	// The command takes a name and lives a moment before it fails, so the stamp
	// treewright writes as the window opens is there to be seen first.
	f := newFixture(t, "command = ': {name}; sleep 1; exit 3'\n")
	f.mustRun("new", "boom")

	id := heldWindowID(t, "boom")
	if got := agentNameOn(t, id); got != "proj-boom" {
		t.Fatalf("agent name on the new window = %q, want proj-boom", got)
	}
	waitForPane(t, id, "press Enter")
	if got := agentNameOn(t, id); got != "" {
		t.Errorf("agent name on the held window = %q, want it shed with the agent", got)
	}
}

// agentNameOn reads the agent name recorded on a window.
func agentNameOn(t *testing.T, id string) string {
	t.Helper()
	out, err := tmuxctl(t, "display-message", "-p", "-t", id, "#{"+tmux.AgentNameOption+"}")
	if err != nil {
		t.Fatalf("read the agent name: %v\n%s", err, out)
	}
	return out
}
