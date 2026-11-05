package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jay-snyder/treewright/internal/plugin/cmdq"
)

// queueStore is the queue the fixture's commands write to, which newFixture
// keeps under a temporary XDG_STATE_HOME.
func queueStore() cmdq.Store { return cmdq.Store{Dir: cmdq.Dir()} }

// queued adds a command through the CLI and returns the ID it printed.
func (f *fixture) queued(args ...string) string {
	f.t.Helper()
	r := f.exec(append([]string{"cmdq", "add"}, args...)...)
	if r.err != nil {
		f.t.Fatalf("cmdq add: %v\n%s", r.err, r.both())
	}
	return strings.TrimSpace(r.stdout)
}

// pendingCommands is what is waiting, as the commands that will run.
func pendingCommands(t *testing.T) []string {
	t.Helper()
	pending, err := queueStore().Pending()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range pending {
		out = append(out, e.Command)
	}
	return out
}

// quickWaits has wait look every few milliseconds, for the test's length.
func quickWaits(t *testing.T) {
	t.Helper()
	every := waitEvery
	waitEvery = 10 * time.Millisecond
	t.Cleanup(func() { waitEvery = every })
}

// TestCmdqAddPrintsTheIDAndNothingElse is the output contract: the ID is the
// answer, which the agent hands to wait, and everything said to the agent
// about it goes to stderr.
func TestCmdqAddPrintsTheIDAndNothingElse(t *testing.T) {
	f := newFixture(t, "")
	r := f.exec("cmdq", "add", "--why", "install the toolchain", "--", "sudo", "installer", "-pkg", "./toolchain.pkg")
	if r.err != nil {
		t.Fatalf("add: %v\n%s", r.err, r.both())
	}
	id := strings.TrimSpace(r.stdout)
	if !cmdq.ValidID(id) || r.stdout != id+"\n" {
		t.Fatalf("stdout = %q, want an ID and nothing else", r.stdout)
	}
	for _, want := range []string{"queued for the person to run", "treewright cmdq wait " + id} {
		if !strings.Contains(flat(r.stderr), want) {
			t.Errorf("stderr = %q, want %q", r.stderr, want)
		}
	}
	e, err := queueStore().Find(id)
	if err != nil {
		t.Fatal(err)
	}
	// The words after -- are the command as a shell would pass them on, flags
	// that look like treewright's included.
	if e.Command != "sudo installer -pkg ./toolchain.pkg" || e.Why != "install the toolchain" {
		t.Errorf("queued %+v", e)
	}
	if e.Dir != f.MainDir && e.Dir != mustEval(t, f.MainDir) {
		t.Errorf("dir = %q, want where add ran, %s", e.Dir, f.MainDir)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestCmdqAddReadsAQuotedHeredoc is how an agent is told to pass a command:
// on stdin, so its own shell expands nothing on the way in.
func TestCmdqAddReadsAQuotedHeredoc(t *testing.T) {
	f := newFixture(t, "")
	command := "sudo tee /etc/hosts.d/$NAME <<'X'\n127.0.0.1 storefront.test\nX\n"
	r := f.execWithStdin(command, "cmdq", "add", "--why", "map the test host")
	if r.err != nil {
		t.Fatalf("add: %v\n%s", r.err, r.both())
	}
	if got := pendingCommands(t); len(got) != 1 || got[0] != strings.TrimSuffix(command, "\n") {
		t.Errorf("queued %q, want the heredoc as written", got)
	}
}

func TestCmdqAddRefusesWhatCannotBeRunAsShown(t *testing.T) {
	f := newFixture(t, "")
	for _, c := range []struct {
		name  string
		stdin string
		args  []string
		usage bool
		want  string
	}{
		{"no command at all", "", []string{"--why", "x"}, true, "the command is empty"},
		{"a blank command", "", []string{"--why", "x", "--", "  "}, true, "the command is empty"},
		{"a NUL byte", "echo a\x00b", []string{"--why", "x"}, false, "NUL byte"},
		{"no reason", "", []string{"--", "true"}, true, "--why is required"},
		{"a blank reason", "", []string{"--why", " \n\t", "--", "true"}, true, "--why is required"},
		{"a directory that is not there", "", []string{"--why", "x", "--cwd", "/nonexistent/storefront", "--", "true"}, false, "no such directory"},
		{"a command not after --", "", []string{"--why", "x", "true"}, true, "after --"},
		{"an empty --from", "", []string{"--why", "x", "--from", " ", "--", "true"}, true, "--from is empty"},
		{"a wait flag", "", []string{"--why", "x", "--timeout", "3", "--", "true"}, true, "--timeout is for wait"},
	} {
		r := f.execWithStdin(c.stdin, append([]string{"cmdq", "add"}, c.args...)...)
		if r.err == nil {
			t.Errorf("%s: queued anyway", c.name)
			continue
		}
		if got := errors.Is(r.err, ErrUsage); got != c.usage {
			t.Errorf("%s: usage error %v, want %v", c.name, got, c.usage)
		}
		if said := r.err.Error() + r.stderr; !strings.Contains(said, c.want) {
			t.Errorf("%s: said %q, want %q", c.name, said, c.want)
		}
	}
	if got := pendingCommands(t); len(got) != 0 {
		t.Errorf("refused commands were queued: %q", got)
	}
}

// TestCmdqAddTakesTheReasonAndTheAskerAsOneLine: each gets a line of the list,
// and a newline or an escape in either would draw over the entry around it.
func TestCmdqAddTakesTheReasonAndTheAskerAsOneLine(t *testing.T) {
	f := newFixture(t, "")
	id := f.queued("--why", "two\nlines\x1b[2J", "--from", "checkout-api\n", "--", "true")
	e, err := queueStore().Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Why != "two lines [2J" || e.From != "checkout-api" {
		t.Errorf("why = %q, from = %q", e.Why, e.From)
	}
}

// TestCmdqFromDefaultsToTheAskingAgentsName: the name treewright gave the agent
// in the window add was run from, which is what tells the base window's agent
// from a scratch window's — the folder name both stand in cannot.
func TestCmdqFromDefaultsToTheAskingAgentsName(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = ': {name}; sleep 300'\nresume_command = ': {name}; sleep 300'\n")
	f.mustRun("scratch", "audit")
	standIn(t, "proj", "audit")
	id := f.queued("--why", "x", "--", "true")
	if e, _ := queueStore().Find(id); e.From != "proj-audit" {
		t.Errorf("from = %q, want the scratch agent's name, proj-audit", e.From)
	}
}

func TestCmdqFromFallsBackToTheRepositoryOutsideANamedWindow(t *testing.T) {
	f := newFixture(t, "")
	id := f.queued("--why", "x", "--", "true")
	if e, _ := queueStore().Find(id); e.From != "proj" {
		t.Errorf("from = %q, want the config's name", e.From)
	}
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	id = f.queued("--why", "x", "--", "true")
	if e, _ := queueStore().Find(id); e.From != filepath.Base(elsewhere) {
		t.Errorf("from = %q, want the directory's name outside any repository", e.From)
	}
}

// TestCmdqMessagesNameTheKeyThatIsBound: the key the person has, read from the
// server, rather than a terminal emulator's mapping that no plugin can see.
func TestCmdqMessagesNameTheKeyThatIsBound(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	standIn(t, "proj", "eng-1")

	r := f.exec("cmdq", "add", "--why", "x", "--", "true")
	if !strings.Contains(flat(r.stderr), "opens the list treewright cmdq") {
		t.Errorf("with no key bound, stderr = %q, want the command named", r.stderr)
	}
	f.mustRun("tmux-init", "--apply", "--cmdq-key", "Q")
	r = f.exec("cmdq", "add", "--why", "x", "--", "true")
	if !strings.Contains(flat(r.stderr), "opens the list C-b Q") {
		t.Errorf("with Q bound, stderr = %q, want the key named", r.stderr)
	}
	f.setConfig("main_dir = '" + f.MainDir + "'\ncommand = 'sleep 300'\nfeatures = ['cmdq']\n")
	if out := f.exec("session-start").stdout; !strings.Contains(out, "C-b Q") {
		t.Errorf("session-start did not name the bound key:\n%s", out)
	}
}

// TestCmdqAddRefusesWhatTheGuardWould: a command queued to run in another
// agent's worktree would get a person to make the change the guard refuses
// the agent itself.
func TestCmdqAddRefusesWhatTheGuardWould(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	other := f.DirFor("eng-1")

	for _, c := range []struct {
		name    string
		args    []string
		refused bool
	}{
		{"a change in another worktree", []string{"--cwd", other, "--", "sudo make install"}, true},
		{"a cd into another worktree", []string{"--", "cd " + other + " && sudo make install"}, true},
		{"a read of another worktree", []string{"--cwd", other, "--", "git status"}, false},
		{"a change in the caller's own checkout", []string{"--", "sudo make install"}, false},
	} {
		r := f.exec(append([]string{"cmdq", "add", "--why", "x"}, c.args...)...)
		if refused := r.err != nil; refused != c.refused {
			t.Errorf("%s: refused %v, want %v\n%s", c.name, refused, c.refused, r.both())
			continue
		}
		if c.refused && !strings.Contains(r.stderr, "eng-1 is another agent's worktree") {
			t.Errorf("%s: refusal = %q, want the guard's", c.name, r.stderr)
		}
	}
	if got := pendingCommands(t); len(got) != 2 {
		t.Errorf("queued %q, want only the two allowed", got)
	}
}

// TestCmdqWaitSaysHowItWent covers every way a wait ends, by the outcome main
// turns into an exit code.
func TestCmdqWaitSaysHowItWent(t *testing.T) {
	quickWaits(t)
	f := newFixture(t, "")
	store := queueStore()

	ran := f.queued("--why", "x", "--", "true")
	if err := store.KeepOutput(ran, "all done\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.Retire(ran, cmdq.Ran, time.Now()); err != nil {
		t.Fatal(err)
	}
	r := f.exec("cmdq", "wait", ran)
	if r.err != nil || !strings.HasPrefix(r.stdout, "ran, exit 0") || !strings.Contains(r.stdout, "  | all done") {
		t.Errorf("wait on one that ran: %v\n%s", r.err, r.both())
	}

	dismissed := f.queued("--why", "x", "--", "true")
	if err := store.Retire(dismissed, cmdq.Dismissed, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, f.exec("cmdq", "wait", dismissed), WaitDismissed, "dismissed by the person")

	withdrawn := f.queued("--why", "x", "--", "true")
	f.mustRun("cmdq", "drop", withdrawn)
	assertOutcome(t, f.exec("cmdq", "wait", withdrawn), WaitDismissed, "withdrawn")

	// A failure from before the wait began is not this wait's answer.
	waiting := f.queued("--why", "x", "--", "false")
	if err := store.RecordFailure(waiting, 1, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, f.exec("cmdq", "wait", waiting, "--timeout", "1"), WaitTimedOut, "waiting — last run failed")

	// One after it is.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = store.RecordFailure(waiting, 3, time.Now())
	}()
	assertOutcome(t, f.exec("cmdq", "wait", waiting, "--timeout", "10"), WaitRunFailed, "last run failed: exit 3")
}

func assertOutcome(t *testing.T, r result, want WaitOutcome, said string) {
	t.Helper()
	outcome, ok := errors.AsType[*OutcomeError](r.err)
	if !ok || outcome.Outcome != want {
		t.Errorf("wait returned %v, want outcome %d\n%s", r.err, want, r.both())
	}
	if !strings.Contains(r.stdout, said) {
		t.Errorf("wait printed %q, want %q on stdout", r.stdout, said)
	}
}

func TestCmdqWaitIsInvokedWithAnIDAndATimeInSeconds(t *testing.T) {
	f := newFixture(t, "")
	id := f.queued("--why", "x", "--", "true")
	for _, args := range [][]string{
		{"cmdq", "wait"},
		{"cmdq", "wait", id, "--timeout", "0"},
		{"cmdq", "wait", id, "--timeout", "soon"},
		{"cmdq", "wait", id, "--why", "x"},
	} {
		if r := f.exec(args...); !errors.Is(r.err, ErrUsage) {
			t.Errorf("%q = %v, want a usage error", args, r.err)
		}
	}
	r := f.exec("cmdq", "wait", "20200101-000000-0000")
	if r.err == nil || errors.Is(r.err, ErrUsage) || !strings.Contains(r.err.Error(), "no entry") {
		t.Errorf("wait on an unknown ID = %v, want a plain error naming it", r.err)
	}
	if _, isOutcome := errors.AsType[*OutcomeError](r.err); isOutcome {
		t.Errorf("an unknown ID was reported as an outcome, which would exit as one")
	}
}

// TestCmdqStatusShowsWhatTheLastRunLeftAsItIs: the last lines, each through
// the same stand-ins the list uses, so the output of a run that printed an
// escape cannot reach the terminal of whoever reads it back.
func TestCmdqStatusShowsWhatTheLastRunLeftAsItIs(t *testing.T) {
	f := newFixture(t, "")
	id := f.queued("--why", "x", "--", "false")
	store := queueStore()
	if err := store.KeepOutput(id, "Password:\nsorry, try again\x1b[2K\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFailure(id, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	out := f.mustRun("cmdq", "status", id)
	want := "waiting — last run failed: exit 1, just now\n  | Password:\n  | sorry, try again␛[2K\n"
	if out != want {
		t.Errorf("status = %q, want %q", out, want)
	}
}

func TestCmdqDropWithdrawsOnlyWhatIsWaiting(t *testing.T) {
	f := newFixture(t, "")
	id := f.queued("--why", "x", "--", "true")
	r := f.exec("cmdq", "drop", id)
	if r.err != nil || r.stdout != "" || !strings.Contains(r.stderr, "withdrew "+id) {
		t.Errorf("drop: %v, stdout %q, stderr %q", r.err, r.stdout, r.stderr)
	}
	r = f.exec("cmdq", "drop", id)
	if r.err == nil || !strings.Contains(flat(r.err.Error()), "has already left the list it was withdrawn") {
		t.Errorf("a second drop = %v", r.err)
	}
	if r := f.exec("cmdq", "drop", "../../etc"); r.err == nil || !strings.Contains(r.err.Error(), "no entry") {
		t.Errorf("drop of a path = %v", r.err)
	}
}

func TestCmdqListAndHistoryPrintTheEntries(t *testing.T) {
	f := newFixture(t, "")
	if r := f.exec("cmdq", "list"); r.stdout != "" || !strings.Contains(r.stderr, "nothing waiting") {
		t.Errorf("an empty list printed %q, said %q", r.stdout, r.stderr)
	}
	id := f.queued("--why", "log in", "--from", "storefront", "--", "gh auth login\necho \x1b]0;x\x07done")
	out := f.mustRun("cmdq", "list")
	for _, want := range []string{
		id + "  storefront  (waiting — queued just now)",
		"  why: log in",
		"  $ gh auth login",
		"  > echo ␛]0;x␇done",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list is missing %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("list wrote a hidden character to the terminal: %q", out)
	}
	if r := f.exec("cmdq", "history"); r.stdout != "" || !strings.Contains(r.stderr, "no history yet") {
		t.Errorf("an empty history printed %q, said %q", r.stdout, r.stderr)
	}
	f.mustRun("cmdq", "drop", id)
	if out := f.mustRun("cmdq", "history"); !strings.Contains(out, id+"  storefront  (withdrawn, ") {
		t.Errorf("history = %q", out)
	}
}

// TestTheCmdqListNeedsATerminal: without one there is nobody to press Enter,
// and the message says where the list can be had instead.
func TestTheCmdqListNeedsATerminal(t *testing.T) {
	f := newFixture(t, "")
	r := f.exec("cmdq")
	if r.err == nil || !strings.Contains(flat(r.err.Error()), "the list needs a terminal open it with treewright cmdq or print it treewright cmdq list") {
		t.Errorf("cmdq with no terminal = %v", r.err)
	}
	if r := f.exec("cmdq", "--why", "x"); !errors.Is(r.err, ErrUsage) {
		t.Errorf("the list with --why = %v, want a usage error", r.err)
	}
	if r := f.exec("cmdq", "status", "--", "true"); !errors.Is(r.err, ErrUsage) {
		t.Errorf("a command given to status = %v, want a usage error", r.err)
	}
	if r := f.exec("cmdq", "nonesuch"); !errors.Is(r.err, ErrUsage) {
		t.Errorf("an unknown sub-command = %v, want a usage error", r.err)
	}
}

// TestSessionStartTellsAnAgentAboutTheQueue only where the repository asked:
// what it says is written for an agent — when to queue, how, and to wait in
// the background straight after.
func TestSessionStartTellsAnAgentAboutTheQueue(t *testing.T) {
	f := newFixture(t, "")
	if out := f.exec("session-start").stdout; strings.Contains(out, "cmdq") {
		t.Errorf("session-start spoke of the queue in a repository that did not ask:\n%s", out)
	}
	f.setConfig("main_dir = '" + f.MainDir + "'\nfeatures = ['cmdq']\n")
	out := f.exec("session-start").stdout
	for _, want := range []string{"sudo", "treewright cmdq add --why", "<<'EOF'", "treewright cmdq wait <ID>", "in the\nbackground", `"treewright cmdq" at a prompt`} {
		if !strings.Contains(out, want) {
			t.Errorf("session-start is missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorReportsTheQueueOnceItIsUsed(t *testing.T) {
	f := newFixture(t, "")
	for _, fi := range findings(t, f) {
		if strings.HasPrefix(fi.detail, "cmdq:") {
			t.Fatalf("doctor reported on a queue nobody used: %s", fi.detail)
		}
	}
	gone := filepath.Join(t.TempDir(), "storefront-eng-2318")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	f.queued("--why", "x", "--", "true")
	f.queued("--why", "x", "--cwd", gone, "--", "true")
	if got := has(t, findings(t, f), "cmdq: queue 2 commands waiting"); got != "ok" {
		t.Errorf("queue finding = %q", got)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if got := has(t, findings(t, f), "cmdq: directories 1 command queued in a directory that has gone"); got != "warn" {
		t.Errorf("gone-directory finding = %q, want a warning", got)
	}
}

func TestCompletionOffersCmdqsSubcommands(t *testing.T) {
	f := newFixture(t, "")
	if got := f.mustRun("__complete", "cmdq"); got != "add\nwait\nstatus\ndrop\nlist\nhistory\n" {
		t.Errorf("completion = %q", got)
	}
}

func TestTheCmdqPopupTakesMostOfTheTerminal(t *testing.T) {
	hideTmux(t)
	if w, h := cmdqPopupSize(""); w <= 80 || h <= 24 {
		t.Errorf("with no client to measure, the popup is %dx%d, want a large one", w, h)
	}
}
