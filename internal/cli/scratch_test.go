package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/tmux"
)

// A scratch window is a second agent session standing in the base checkout, and
// every test here drives a real server, because what is being covered is which
// window a command reaches when two stand in one directory — something only the
// pane listing can get right or wrong.
//
// Every fixture runs a command that stays up, since a window whose command has
// exited is a window that has closed, and the assertions are about windows that
// exist.

const lingering = "command = 'sleep 300'\n"

// ---- identity ----------------------------------------------------------------

// TestBaseFindsItsOwnWindowBesideAScratchOne is the assertion the whole feature
// rests on: however many scratch windows stand in the main checkout, `base`
// means the one base window, and switches to it rather than opening another.
func TestBaseFindsItsOwnWindowBesideAScratchOne(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")
	f.mustRun("scratch", "ask")
	f.mustRun("scratch", "review")

	r := f.exec("base")
	if r.err != nil {
		t.Fatalf("base: %v\n%s", r.err, r.both())
	}
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"main", "ask", "review"}) {
		t.Errorf("windows = %v, want the base window and the two scratch windows, and nothing opened beside them", got)
	}
	if got := activeWindowIn(t, "proj"); got != "main" {
		t.Errorf("active window = %q, want base to have selected the base window", got)
	}
}

// TestAScratchWindowDoesNotAnswerForTheBaseCheckout covers the orders in which a
// scratch window could have been mistaken for the base window: opened before it,
// so it is the older window on the directory — and with no base window open at
// all, which is the empty slot a scratch window claiming its directory would
// have filled. Each of base, close base and send base must behave as though the
// scratch window were not there.
func TestAScratchWindowDoesNotAnswerForTheBaseCheckout(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("scratch", "ask")

	if r := f.exec("close", "base"); r.err == nil {
		t.Errorf("close base closed something with only a scratch window open\n%s", r.both())
	}
	if r := f.exec("send", "base", "hello"); r.err == nil {
		t.Errorf("send base reached something with only a scratch window open\n%s", r.both())
	}
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"ask"}) {
		t.Fatalf("windows = %v, want the scratch window alone and untouched", got)
	}

	// The base window does not exist, so base opens one rather than switching to
	// the older window standing where it would.
	f.mustRun("base")
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"ask", "main"}) {
		t.Errorf("windows = %v, want base to have opened a window of its own", got)
	}

	// And closing the base window closes that one, the scratch window being
	// older and in the same directory.
	f.mustRun("close", "base")
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"ask"}) {
		t.Errorf("windows = %v, want close base to have closed the base window and only it", got)
	}
}

// TestScratchAlwaysOpensANewWindow is the difference from `base`: a request for
// a scratch window is never answered by a window that happens to stand in the
// same directory.
func TestScratchAlwaysOpensANewWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")

	r := f.exec("scratch", "ask")
	if r.err != nil {
		t.Fatalf("scratch: %v\n%s", r.err, r.both())
	}
	f.mustRun("scratch", "review")
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"main", "ask", "review"}) {
		t.Errorf("windows = %v, want a new window for each scratch name", got)
	}
	// Stamped as what it is and where it stands, and as no worktree.
	if got := windowStamp(t, "ask", "@treewright_scratch"); got != "ask" {
		t.Errorf("@treewright_scratch = %q, want the name", got)
	}
	if got := windowStamp(t, "ask", "@treewright_worktree"); got != f.MainDir {
		t.Errorf("@treewright_worktree = %q, want the main checkout it stands on", got)
	}
	if got := windowStamp(t, "ask", "@treewright_slug"); got != "" {
		t.Errorf("@treewright_slug = %q, want none — a scratch window is not a worktree", got)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing — the answer is a window", r.stdout)
	}
}

// ---- names ---------------------------------------------------------------------

// TestAScratchNameCannotCollideWithAWorktreeSlug: send, close and resume take a
// slug and a scratch name alike, so a name that meant both would leave them
// unable to say which they had reached. Refused when the name is given out.
func TestAScratchNameCannotCollideWithAWorktreeSlug(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("new", "eng-1")
	before := windowsIn(t, "proj")

	r := f.exec("scratch", "eng-1")
	if r.err == nil {
		t.Fatalf("scratch took a worktree's slug\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "already a worktree") ||
		!strings.Contains(msg, "treewright resume --repo proj eng-1") {
		t.Errorf("error = %q, want the worktree named and the way to its window", msg)
	}
	if got := windowsIn(t, "proj"); !slices.Equal(got, before) {
		t.Errorf("windows = %v, want nothing opened by a refused scratch (had %v)", got, before)
	}
}

// TestNewRefusesTheNameOfAnOpenScratchWindow is the same rule from the other
// side, refused before any branch or directory exists.
func TestNewRefusesTheNameOfAnOpenScratchWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("scratch", "ask")

	r := f.exec("new", "ask")
	if r.err == nil {
		t.Fatalf("new took an open scratch window's name\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "scratch window") ||
		!strings.Contains(msg, "treewright close --repo proj ask") {
		t.Errorf("error = %q, want the scratch window named and the way to close it", msg)
	}
	if f.Exists("ask") {
		t.Error("the worktree was created anyway")
	}
	if f.Git(f.MainDir, "branch", "--list", f.BranchFor("ask")) != "" {
		t.Error("the branch was created anyway")
	}

	// Once the scratch window is gone, so is the collision: nothing records one.
	f.mustRun("close", "ask")
	if r := f.exec("new", "ask"); r.err != nil {
		t.Errorf("new after the scratch window closed: %v\n%s", r.err, r.both())
	}
}

// TestScratchRefusesANameAlreadyOpen: a second window under one name would make
// the name mean two windows, and the error names the way to the first.
func TestScratchRefusesANameAlreadyOpen(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("scratch", "ask")

	r := f.exec("scratch", "ask")
	if r.err == nil {
		t.Fatalf("scratch opened a second window called ask\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "already open") ||
		!strings.Contains(msg, "treewright resume --repo proj ask") {
		t.Errorf("error = %q, want the open window named and the way to it", msg)
	}
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"ask"}) {
		t.Errorf("windows = %v, want the one scratch window", got)
	}
}

// TestScratchRefusesTheBaseCheckoutsNames: they win every lookup, so a scratch
// window under one of them could be opened and never reached.
func TestScratchRefusesTheBaseCheckoutsNames(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	for _, name := range []string{"base", "main"} {
		r := f.exec("scratch", name)
		if r.err == nil {
			t.Errorf("scratch %s was accepted\n%s", name, r.both())
			continue
		}
		if !strings.Contains(r.err.Error(), "names the base checkout") {
			t.Errorf("scratch %s: error = %q, want the base checkout named", name, r.err)
		}
	}
	if got := windowsIn(t, "proj"); len(got) != 0 {
		t.Errorf("windows = %v, want nothing opened", got)
	}
}

// TestScratchNeedsAName covers the usage refusals: a name is how everything
// else reaches the window, and it follows a slug's rules so that what can be
// typed here is what can be typed at `new`.
func TestScratchNeedsAName(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("scratch")
	if !errors.Is(r.err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage for a scratch with no name", r.err)
	}
	if !strings.Contains(r.stderr, "a name is required") {
		t.Errorf("stderr = %q, want the missing name named", r.stderr)
	}

	r = f.exec("scratch", "a/b")
	if !errors.Is(r.err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage for a name no slug could have", r.err)
	}
	if !strings.Contains(r.stderr, "follows a slug's rules") {
		t.Errorf("stderr = %q, want it to say why a slug's rules apply", r.stderr)
	}
}

// TestScratchHandsItsAgentAPrompt: the command is `command`, filled the way `new`
// fills it, so the kickoff prompt arrives shell-quoted as one argument.
func TestScratchHandsItsAgentAPrompt(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "prompt")
	f := newFixture(t, "command = \"printf %s {prompt} > "+marker+"\"\n")

	const prompt = "what does the retry logic do when the token's expired?"
	if r := f.exec("scratch", "ask", "--prompt", prompt); r.err != nil {
		t.Fatalf("scratch: %v\n%s", r.err, r.both())
	}
	waitForContent(t, marker, prompt, "the scratch window's command")
}

// TestScratchWithoutTmuxRunsHere: with no tmux there is no window to open, and
// the answer is the one `base` gives rather than a third — the command runs in
// this terminal, in the main checkout.
func TestScratchWithoutTmuxRunsHere(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "where")
	f := newFixture(t, "command = 'pwd > "+marker+"'\n")
	t.Chdir(f.Root)
	hideTmux(t)

	if r := f.exec("scratch", "--repo", "proj", "ask"); r.err != nil {
		t.Fatalf("scratch without tmux: %v\n%s", r.err, r.both())
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the command never ran: %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != f.MainDir {
		t.Errorf("the command ran in %q, want the main checkout %q", got, f.MainDir)
	}
}

// ---- reaching one ----------------------------------------------------------------

// TestSendReachesAScratchWindowByName, and only it: the base window stands in
// the same directory, and a lookup by directory would have typed there.
func TestSendReachesAScratchWindowByName(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")
	config, marker := receives(t)
	f.setConfig("main_dir = '" + f.MainDir + "'\n" + config)
	f.mustRun("scratch", "ask")

	const message = "which of the retry paths is live?"
	r := f.exec("send", "ask", message)
	if r.err != nil {
		t.Fatalf("send: %v\n%s", r.err, r.both())
	}
	waitForContent(t, marker, message+"\n", "the scratch window's agent")
	if msg := flat(r.stderr); !strings.Contains(msg, "sent to ask") || !strings.Contains(msg, "scratch window ask") {
		t.Errorf("stderr = %q, want the scratch window it reached named as one", msg)
	}

	// The base window's tty would have echoed anything typed at it.
	if pane, _ := tmuxctl(t, "capture-pane", "-p", "-t", windowIDNamed(t, "proj", "main")); strings.Contains(pane, message) {
		t.Errorf("the base window shows %q — the message was typed there too", pane)
	}
}

// TestCloseFindsAScratchWindowByName, and after it has gone finds nothing: a
// scratch window has no worktree whose record could outlive it, so the name is
// an ordinary miss, and the message says no scratch window is open by it.
func TestCloseFindsAScratchWindowByName(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")
	f.mustRun("scratch", "ask")

	r := f.exec("close", "ask")
	if r.err != nil {
		t.Fatalf("close: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "closing tmux window ask") {
		t.Errorf("stderr = %q, want the scratch window named", r.stderr)
	}
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"main"}) {
		t.Errorf("windows = %v, want the scratch window gone and the base window left", got)
	}

	r = f.exec("close", "ask")
	if r.err == nil {
		t.Fatalf("a second close found something\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "no scratch window by that name is open") {
		t.Errorf("error = %q, want it to say no scratch window answers to the name", msg)
	}
}

// TestTheWindowsListedByACloseMissIncludeScratchOnes: they claim no directory,
// so a list built from the directory map alone would leave out exactly the
// windows a reader of that error may have meant.
func TestTheWindowsListedByACloseMissIncludeScratchOnes(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")
	f.mustRun("scratch", "ask")

	r := f.exec("close", "nope")
	if r.err == nil {
		t.Fatalf("close nope found something\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "ask") || !strings.Contains(msg, "main") {
		t.Errorf("error = %q, want every open window listed, the scratch one included", msg)
	}
}

// TestResumeSwitchesToAnOpenScratchWindow: resuming a window that is open is
// going to it, whether its row is a worktree's or a scratch window's.
func TestResumeSwitchesToAnOpenScratchWindow(t *testing.T) {
	requireTmux(t)
	// A placeholder in both templates, since resume refuses a prompt that either
	// could not take before it looks for a window at all.
	f := newFixture(t, "command = 'sleep 300 {prompt}'\nresume_command = 'sleep 300 {prompt}'\n")
	f.mustRun("scratch", "ask")
	f.mustRun("base")

	r := f.exec("resume", "ask")
	if r.err != nil {
		t.Fatalf("resume: %v\n%s", r.err, r.both())
	}
	if got := activeWindowIn(t, "proj"); got != "ask" {
		t.Errorf("active window = %q, want the scratch window selected", got)
	}
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"ask", "main"}) {
		t.Errorf("windows = %v, want nothing opened", got)
	}

	// A prompt cannot reach an agent already running, which is said as it is
	// for any window resume finds open.
	if r := f.exec("resume", "ask", "--prompt", "and another thing"); !strings.Contains(r.stderr, "prompt not delivered") {
		t.Errorf("stderr = %q, want the undelivered prompt warned about", r.stderr)
	}
}

// TestResumeOfANameNothingAnswersToNamesScratch: a closed scratch window keeps
// nothing, so there is nothing to resume, and the message says that rather than
// sending its reader to look for a session that is not recorded anywhere.
func TestResumeOfANameNothingAnswersToNamesScratch(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("new", "eng-1")

	r := f.exec("resume", "ask")
	if r.err == nil {
		t.Fatalf("resume of a name nothing answers to succeeded\n%s", r.both())
	}
	msg := flat(r.err.Error())
	for _, want := range []string{`no worktree "ask"`, "nothing to resume", "eng-1", "treewright scratch --repo proj ask"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to say %q", msg, want)
		}
	}

	// An ambiguous prefix is still resolveSlug's to report, candidates and all.
	f.mustRun("new", "eng-2")
	if r := f.exec("resume", "eng"); r.err == nil || strings.Contains(r.err.Error(), "scratch") {
		t.Errorf("resume of an ambiguous prefix = %v, want the candidates rather than the scratch hint", r.err)
	}
}

// ---- listing -------------------------------------------------------------------

// TestLsListsScratchWindowsUnderTheBaseRow: where they stand, with their own
// status, and without repeating the base row's numbers. The JSON flags them the
// way it flags the base checkout, so a consumer deciding where work goes still
// reads row 0 and is never handed one by mistake.
func TestLsListsScratchWindowsUnderTheBaseRow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")
	f.mustRun("new", "eng-1")
	f.mustRun("scratch", "review")
	f.mustRun("scratch", "ask")

	var slugs []string
	for line := range strings.SplitSeq(strings.TrimSpace(f.mustRun("ls")), "\n") {
		// The asterisk marking the row you stand in, which is the base row here.
		fields := strings.Fields(strings.TrimPrefix(line, "*"))
		slugs = append(slugs, fields[0])
		if fields[0] == "ask" && (fields[1] != "scratch" || fields[2] != "-" || fields[3] != "ask") {
			t.Errorf("ask's row = %q, want status scratch, no divergence, and its own window", line)
		}
	}
	if want := []string{"SLUG", "main", "ask", "review", "eng-1"}; !slices.Equal(slugs, want) {
		t.Errorf("rows = %v, want %v — the scratch windows under the base row, by name", slugs, want)
	}

	var rows []struct {
		Slug     string `json:"slug"`
		Base     bool   `json:"base"`
		Scratch  bool   `json:"scratch"`
		Dir      string `json:"dir"`
		Branch   string `json:"branch"`
		Status   string `json:"status"`
		Ahead    *int   `json:"ahead"`
		Window   string `json:"window"`
		WindowID string `json:"window_id"`
	}
	r := f.exec("ls", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, r.stdout)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want base, two scratch windows and eng-1: %s", len(rows), r.stdout)
	}
	if base := rows[0]; !base.Base || base.Scratch || base.Window != "main" {
		t.Errorf("row 0 = %+v, want the base checkout, with the base window rather than a scratch one", base)
	}
	ask := rows[1]
	if !ask.Scratch || ask.Base || ask.Slug != "ask" || ask.Status != "scratch" ||
		ask.Dir != f.MainDir || ask.Branch != "" || ask.Ahead != nil {
		t.Errorf("row 1 = %+v, want the ask scratch window on the main checkout with no branch or divergence", ask)
	}
	if ask.WindowID != windowIDNamed(t, "proj", "ask") {
		t.Errorf("row 1 window_id = %q, want the scratch window's own", ask.WindowID)
	}
	if rows[3].Scratch || rows[3].Slug != "eng-1" {
		t.Errorf("row 3 = %+v, want the worktree, unflagged", rows[3])
	}
}

// TestLsShowsAScratchWindowWithNoWorktrees: "no worktrees" is still true, and it
// is no longer the whole answer — there is an agent running, and what it is
// doing is the thing the table is for.
func TestLsShowsAScratchWindowWithNoWorktrees(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("scratch", "ask")

	r := f.exec("ls")
	if !strings.Contains(r.stdout, "ask") || !strings.Contains(r.stdout, "scratch") {
		t.Errorf("stdout = %q, want the scratch window listed", r.stdout)
	}
}

// TestCompletionOffersOpenScratchWindows: the name is the only way to type one,
// so the commands that reach it complete it — and rm, which cannot, does not.
func TestCompletionOffersOpenScratchWindows(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("new", "eng-1")
	f.mustRun("scratch", "review")
	f.mustRun("scratch", "ask")

	if got := f.mustRun("__complete", "targets"); got != "base\nask\nreview\neng-1\n" {
		t.Errorf("targets = %q, want base, the scratch windows by name, then the worktrees", got)
	}
	if got := f.mustRun("__complete", "slugs"); got != "eng-1\n" {
		t.Errorf("slugs = %q, want the worktrees alone", got)
	}
}

// ---- what does not change ------------------------------------------------------

// TestRestoreLeavesScratchWindowsAlone: restore opens the windows the disk says a
// session should have, and a scratch window is not on disk. One that is open is
// neither reopened nor mistaken for the base window, which restore still opens.
func TestRestoreLeavesScratchWindowsAlone(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering+"resume_command = 'sleep 300'\n")
	f.Worktree("eng-1")
	f.mustRun("scratch", "ask")

	if r := f.exec("restore", "-d"); r.err != nil {
		t.Fatalf("restore: %v\n%s", r.err, r.both())
	}
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"ask", "main", "eng-1"}) {
		t.Errorf("windows = %v, want the base window and eng-1 opened beside the scratch window", got)
	}
	if got := activeWindowIn(t, "proj"); got != "main" {
		t.Errorf("active window = %q, want the base window selected, not the scratch one", got)
	}
}

// ---- signal and fresh-base -------------------------------------------------------

// TestAnAgentSignalsTheWindowItIsRunningIn: the pane is asked before the
// directory. A scratch agent stands in the base checkout exactly where the base
// window's does, and asked by directory its "waiting" landed on the base window.
func TestAnAgentSignalsTheWindowItIsRunningIn(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")
	f.mustRun("scratch", "ask")
	base, ask := windowIDNamed(t, "proj", "main"), windowIDNamed(t, "proj", "ask")

	t.Setenv("TMUX", "/dev/null,0,0")
	t.Setenv("TMUX_PANE", paneIn(t, "proj", "ask"))
	f.mustRun("signal", "waiting")

	if got := stateOn(t, ask); got != "waiting" {
		t.Errorf("state on the scratch window = %q, want waiting", got)
	}
	if got := rawNameOn(t, ask); got != "!ask" {
		t.Errorf("scratch window's name = %q, want the waiting marker on it", got)
	}
	if got := stateOn(t, base); got != "" {
		t.Errorf("state on the base window = %q, want nothing — the other agent's state is not its own", got)
	}
	if got := rawNameOn(t, base); got != "main" {
		t.Errorf("base window's name = %q, want it unmarked", got)
	}
}

// TestTheBaseWindowsAgentSignalsItsOwnWindowFromAWorktree is the same fix with
// no scratch window in it, which is why it is a fix and not a feature: the base
// window's shell walked into a worktree, an agent started there, and asked by
// directory its state landed on the worktree's own window.
func TestTheBaseWindowsAgentSignalsItsOwnWindowFromAWorktree(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	f.mustRun("base")
	f.mustRun("new", "eng-1")

	t.Setenv("TMUX", "/dev/null,0,0")
	t.Setenv("TMUX_PANE", paneIn(t, "proj", "main"))
	t.Chdir(f.DirFor("eng-1"))
	f.mustRun("signal", "working")

	if got := stateOn(t, windowIDNamed(t, "proj", "main")); got != "working" {
		t.Errorf("state on the base window = %q, want working", got)
	}
	if got := stateOn(t, windowIDNamed(t, "proj", "eng-1")); got != "" {
		t.Errorf("state on eng-1's window = %q, want nothing — its agent said nothing", got)
	}
}

// TestAWindowTreewrightDidNotOpenStillSignalsByDirectory keeps the fallback what
// it was: from a window treewright did not open, the checkout decides, so a
// window opened by hand on a worktree gets the state — and keeps its own name.
func TestAWindowTreewrightDidNotOpenStillSignalsByDirectory(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, lingering)
	wt := f.Worktree("eng-1")
	startSession(t, "mine", "hand-made", wt.Dir)

	t.Setenv("TMUX", "/dev/null,0,0")
	t.Setenv("TMUX_PANE", paneIn(t, "mine", "hand-made"))
	t.Chdir(wt.Dir)
	f.mustRun("signal", "waiting")

	id := windowIDNamed(t, "mine", "hand-made")
	if got, _ := tmuxctl(t, "display-message", "-p", "-t", id, "#{"+tmux.AgentStateOption+"}"); got != "waiting" {
		t.Errorf("state on the hand-made window = %q, want waiting", got)
	}
	if got, _ := tmuxctl(t, "display-message", "-p", "-t", id, "#{window_name}"); got != "hand-made" {
		t.Errorf("hand-made window's name = %q, want it left as the user named it", got)
	}
}

// TestFreshBaseYieldsToAWorkingAgent: a session starting in a scratch window
// stands in the checkout the base window's agent is working in, and a
// fast-forward there moves files under that agent. So it is left, and said —
// how far behind, and why — since that is the answer to whether what this
// agent reads is current.
func TestFreshBaseYieldsToAWorkingAgent(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "features = ['fresh-base']\n"+lingering)
	f.mustRun("base")
	f.mustRun("scratch", "ask")
	base := windowIDNamed(t, "proj", "main")
	setState(t, base, stateWorking)
	aheadOrigin(t, f)
	before := headOf(t, f, f.MainDir)

	t.Setenv("TMUX", "/dev/null,0,0")
	t.Setenv("TMUX_PANE", paneIn(t, "proj", "ask"))
	r := f.exec("session-start")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	for _, want := range []string{"1 commit behind origin/main", "was left there", "window main is working"} {
		if !strings.Contains(flat(r.stdout), want) {
			t.Errorf("stdout = %q, want it to say %q", r.stdout, want)
		}
	}
	if got := headOf(t, f, f.MainDir); got != before {
		t.Errorf("HEAD moved to %s under a working agent", got)
	}

	// The caller's own window reporting working is the session now ending, not
	// an agent this could land under.
	setState(t, base, stateDone)
	setState(t, windowIDNamed(t, "proj", "ask"), stateWorking)
	if out := f.mustRun("session-start"); !strings.Contains(out, "fast-forwarded") {
		t.Errorf("session-start = %q, want the fast-forward once only the caller was working", out)
	}
}

// setState stamps an agent state on a window the way `signal` would, for a test
// whose subject is what reads it.
func setState(t *testing.T, id, state string) {
	t.Helper()
	if out, err := tmuxctl(t, "set-window-option", "-t", id, tmux.AgentStateOption, state); err != nil {
		t.Fatalf("set the agent state on %s: %v\n%s", id, err, out)
	}
}
