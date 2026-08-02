package cli

import (
	"slices"
	"strings"
	"testing"
)

// `close` exists so that nothing driving treewright has to type raw tmux. Every
// test here drives a real server, because what the command is for is the
// interaction with one — and the case it is mostly for is a window whose
// worktree has already been deleted, which no amount of git state can stand in
// for.

// TestCloseClosesTheWorktreesWindow is the ordinary call, and the two things it
// must not do: touch the worktree, or reach a window that is not the one named.
func TestCloseClosesTheWorktreesWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")

	r := f.exec("close", "eng-1")
	if r.err != nil {
		t.Fatalf("close: %v\n%s", r.err, r.both())
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing — there is no answer here", r.stdout)
	}
	if !strings.Contains(r.stderr, "closing tmux window ENG-1") {
		t.Errorf("stderr = %q, want the window it closed named", r.stderr)
	}

	if got := windowsIn(t, "proj"); slices.Contains(got, "ENG-1") {
		t.Errorf("windows = %v, want ENG-1 gone", got)
	} else if !slices.Contains(got, "ENG-2") {
		t.Errorf("windows = %v, want the other worktree's window left alone", got)
	}
	// The worktree, its branch and anything in it are none of close's business.
	if !f.Exists("eng-1") {
		t.Error("close removed the worktree, which is rm's job and not this one's")
	}
	if f.Git(f.MainDir, "branch", "--list", f.BranchFor("eng-1")) == "" {
		t.Error("close deleted the branch")
	}
}

// TestCloseWorksAfterTheWorktreeIsGone is the case the command is mostly for.
// `rm` will not close a window without being asked, so with nobody to prompt it
// names this command — and by then the directory the window is sitting in has
// been deleted. The window is found by the path treewright recorded on it, and
// that record outlives the path.
func TestCloseWorksAfterTheWorktreeIsGone(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	wt := f.DirFor("eng-1")

	// rm without --yes and with nobody to prompt leaves the window standing.
	f.Git(f.MainDir, "worktree", "remove", "--force", wt)
	if f.Exists("eng-1") {
		t.Fatal("the worktree is still there, so this proves nothing")
	}
	if got := windowsIn(t, "proj"); !slices.Contains(got, "ENG-1") {
		t.Fatalf("windows = %v, want the stranded window still open", got)
	}

	// Counted by window name rather than by the panes standing in the directory,
	// and deliberately so: whether a pane still reports a directory that has been
	// deleted is a fact about the operating system rather than about treewright.
	// Linux stops reporting it and macOS keeps reporting it, so an assertion
	// either way would be testing the kernel. What makes the window reachable
	// here is @treewright_worktree, and the mechanism itself is covered where it
	// can be covered without an operating system in the way — parsePanes' "a
	// window opened on a worktree answers for it after its pane moves".
	if r := f.exec("close", "eng-1"); r.err != nil {
		t.Fatalf("close after rm: %v\n%s", r.err, r.both())
	}
	if got := windowsIn(t, "proj"); slices.Contains(got, "ENG-1") {
		t.Errorf("windows = %v, want the stranded window closed", got)
	}
}

// TestCloseSaysWhatItWillTakeWithIt, before it takes it. Closing a session's
// last window ends the session and moves or detaches whoever was attached, so
// the report has to arrive first — afterwards there may be nobody to read it.
func TestCloseSaysTheSessionEndsWithIt(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")

	r := f.exec("close", "eng-1")
	if r.err != nil {
		t.Fatalf("close: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "last in session proj, which ends with it") {
		t.Errorf("stderr = %q, want the session it ends named", r.stderr)
	}
	// And the caveat is not offered when it does not apply.
	f2 := newFixture(t, "command = 'sleep 300'\n")
	f2.mustRun("new", "eng-1")
	f2.mustRun("new", "eng-2")
	if r := f2.exec("close", "eng-1"); strings.Contains(r.stderr, "ends with it") {
		t.Errorf("stderr = %q, want no session caveat for a window with company", r.stderr)
	}
}

// TestCloseNamesTheCallersOwnWindow. Closing it is a real thing to want — the
// agent guide asks for exactly that as the last step of a teardown — so it is
// allowed, and what is said is that nothing after it runs.
func TestCloseNamesTheCallersOwnWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")

	insideSession(t, "proj")
	t.Setenv("TMUX_PANE", paneIn(t, "proj", "ENG-1"))

	r := f.exec("close", "eng-1")
	if r.err != nil {
		t.Fatalf("close: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "nothing after this runs") {
		t.Errorf("stderr = %q, want the caller told this is the last thing", r.stderr)
	}
	if got := windowsIn(t, "proj"); slices.Contains(got, "ENG-1") {
		t.Errorf("windows = %v, want the caller's own window closed as asked", got)
	}
}

// TestCloseWithNoWindowOpenSaysWhatIsOpen. Nothing was closed, so the error has
// to leave the reader able to find what they meant rather than guessing again.
func TestCloseWithNoWindowOpenSaysWhatIsOpen(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.Worktree("quiet")
	f.mustRun("new", "eng-1")

	r := f.exec("close", "quiet")
	if r.err == nil {
		t.Fatalf("close with no window open succeeded\n%s", r.both())
	}
	msg := flat(r.err.Error())
	if !strings.Contains(msg, "no window is open on quiet") {
		t.Errorf("error = %q, want the worktree named", msg)
	}
	if !strings.Contains(msg, "ENG-1") {
		t.Errorf("error = %q, want the windows that are open listed", msg)
	}
	// The window that does exist is untouched by a call that found nothing.
	if got := windowsIn(t, "proj"); !slices.Contains(got, "ENG-1") {
		t.Errorf("windows = %v, want the unrelated window left alone", got)
	}
}

// TestCloseResolvesASlugPrefix, while there is still a worktree to match
// against — and reports the expansion, since the caller is about to close
// something.
func TestCloseResolvesASlugPrefix(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1646-app-landing-page")

	r := f.exec("close", "eng-1646")
	if r.err != nil {
		t.Fatalf("close: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "eng-1646 matches worktree eng-1646-app-landing-page") {
		t.Errorf("stderr = %q, want the expansion reported", r.stderr)
	}
	if got := windowsIn(t, "proj"); len(got) != 0 {
		t.Errorf("windows = %v, want the matched window closed", got)
	}
}

// TestCloseReachesTheBaseWindow: it answers to its own names as it does in the
// resume menu, and closing it is a legitimate thing to want even though it
// usually ends the repository's session.
func TestCloseReachesTheBaseWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "proj", "MAIN", f.MainDir)

	if r := f.exec("close", "base"); r.err != nil {
		t.Fatalf("close base: %v\n%s", r.err, r.both())
	}
	if got := panesOn(t, f.MainDir); got != 0 {
		t.Errorf("%d panes still in the main checkout, want the base window closed", got)
	}
}

// TestCloseWarnsWhenTheAgentIsWorking. The agent is the window's command, so
// closing the window stops it — and that is the one thing about a window
// treewright knows and the caller may not, the state coming from the agent's own
// hooks rather than from anything visible in the window's name.
//
// It warns rather than refuses: the caller asked for this, and a refusal would
// need a --force, which is a flag people learn to pass by reflex. What the
// warning buys is the loss being on the record at the moment it happens.
func TestCloseWarnsWhenTheAgentIsWorking(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	t.Chdir(f.DirFor("eng-1"))
	f.mustRun("signal", "working")

	r := f.exec("close", "eng-1")
	if r.err != nil {
		t.Fatalf("close: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "warning: the agent in ENG-1 says it is working") {
		t.Errorf("stderr = %q, want the working agent warned about", r.stderr)
	}
	// Warned, and then closed: this is not a refusal.
	if got := windowsIn(t, "proj"); slices.Contains(got, "ENG-1") {
		t.Errorf("windows = %v, want the window closed as asked", got)
	}
	// And the warning arrives before the window does, since afterwards there may
	// be no session left to say it in.
	warned, closing := strings.Index(r.stderr, "says it is working"), strings.Index(r.stderr, "closing tmux window")
	if warned < 0 || closing < 0 || warned > closing {
		t.Errorf("stderr = %q, want the warning before the close is announced", r.stderr)
	}
}

// TestCloseIsQuietAboutTheStatesATeardownExpects. waiting is an agent blocked on
// a person and done is one with nothing in flight — both are what an ordinary
// cleanup closes, and a warning that fires on the ordinary case is one that
// stops being read.
func TestCloseIsQuietAboutTheStatesATeardownExpects(t *testing.T) {
	requireTmux(t)
	for _, state := range []string{"waiting", "done", "clear"} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t, "command = 'sleep 300'\n")
			f.mustRun("new", "eng-1")
			t.Chdir(f.DirFor("eng-1"))
			f.mustRun("signal", state)

			r := f.exec("close", "eng-1")
			if r.err != nil {
				t.Fatalf("close: %v\n%s", r.err, r.both())
			}
			if strings.Contains(r.stderr, "warning:") {
				t.Errorf("stderr = %q, want no warning for a %s agent", r.stderr, state)
			}
		})
	}
}

// TestRmYesWarnsAboutAWorkingAgent. --yes answered "close the window", which is
// not the same as "tell me nothing about what was in it": the reason a window is
// never closed unasked is that something may still be running in it, and here
// treewright knows that something was.
func TestRmYesWarnsAboutAWorkingAgent(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	t.Chdir(f.DirFor("eng-1"))
	f.mustRun("signal", "working")
	t.Chdir(f.MainDir)

	r := f.exec("rm", "--yes", "eng-1")
	if r.err != nil {
		t.Fatalf("rm --yes: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "says it is working") {
		t.Errorf("stderr = %q, want the working agent warned about", r.stderr)
	}
	if !strings.Contains(r.stderr, "closed its tmux window ENG-1") {
		t.Errorf("stderr = %q, want the window still closed", r.stderr)
	}
}
