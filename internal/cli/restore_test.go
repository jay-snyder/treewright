package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/config"
)

// These tests cover what a repository's session holds after a restore, and what
// treewright does about the client while it fills it: a window per worktree, the
// base window current, nothing moved, and — where a window could not be opened —
// a report left on screen instead of an attach.
//
// Every restore here either passes -d or runs from inside a session, because
// those are the two paths that cannot reach a real `tmux attach-session`. One
// that did would hand the developer's own terminal to a test's tmux server, with
// nothing left to type a detach into. noTerminalHere is the third way, and the
// one test whose subject is that decision uses it.

// killSession takes a session away the way a restart does, leaving the checkouts
// on disk.
func killSession(t *testing.T) {
	t.Helper()
	// Session "proj" because every fixture registers exactly one config, under
	// that name — as windowStamp takes it, and for the same reason: a parameter
	// for it would be a choice no caller has.
	if out, err := tmuxctl(t, "kill-session", "-t", "=proj"); err != nil {
		t.Fatalf("kill session proj: %v\n%s", err, out)
	}
}

// noTerminalHere makes restore's terminal check answer no, which is what a
// startup file or a script gets.
func noTerminalHere(t *testing.T) {
	t.Helper()
	prev := onATerminal
	onATerminal = func() bool { return false }
	t.Cleanup(func() { onATerminal = prev })
}

// refuseWindow puts a tmux shim first on PATH that fails to open the window
// called name and forwards every other call to the real tmux.
//
// A window that cannot be opened is otherwise hard to arrange and easy to
// arrange wrongly: tmux answers a `-c` directory that does not exist by opening
// the window somewhere else and exiting 0, so deleting a worktree behind git's
// back proves nothing. What the failure has to be is tmux refusing, which is
// what this is.
func refuseWindow(t *testing.T, name string) {
	t.Helper()
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("tmux is not on PATH to begin with: %v", err)
	}
	dir := t.TempDir()
	shim := "#!/bin/sh\ncase \" $* \" in\n" +
		"  *\" new-window \"*\" -n " + name + " \"*) echo 'shim: no window for you' >&2; exit 1 ;;\n" +
		"esac\nexec " + tmuxPath + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write the tmux shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRestoreOpensTheBaseWindowAndOneWindowPerWorktree is the command's whole
// point: after a restart the checkouts are still there and the session is not,
// so one command puts back every window the session should have.
func TestRestoreOpensTheBaseWindowAndOneWindowPerWorktree(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")
	killSession(t)

	r := f.exec("restore", "-d")
	if r.err != nil {
		t.Fatalf("restore: %v\n%s", r.err, r.both())
	}

	// The base window first, then the worktrees in the order `ls` prints them.
	// Restore gives you a tidied session rather than a photocopy of the one you
	// lost, and this is what tidied means.
	if got, want := windowsIn(t, "proj"), []string{"main", "eng-1", "eng-2"}; !slices.Equal(got, want) {
		t.Errorf("windows in session proj = %v, want %v", got, want)
	}
	// And each one is standing in its own checkout, not wherever treewright ran.
	for _, dir := range []string{f.MainDir, f.DirFor("eng-1"), f.DirFor("eng-2")} {
		if got := panesOn(t, dir); got != 1 {
			t.Errorf("%d panes in %s, want 1", got, dir)
		}
	}
	// There is no answer to print — only a session that is back.
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing", r.stdout)
	}
	// Detached, nobody is about to look at the session, so what is in it and the
	// way in are worth saying.
	if !strings.Contains(flat(r.stderr), "3 windows open in tmux session proj") {
		t.Errorf("stderr = %q, want the count of what is open", r.stderr)
	}
	if !strings.Contains(flat(r.stderr), "attach with treewright attach proj") {
		t.Errorf("stderr = %q, want the way in", r.stderr)
	}
}

// TestRestoreLeavesTheBaseWindowCurrent covers what attaching then lands on.
// tmux makes each new window current as it is created, so without a select of
// its own a restored session opens on whichever worktree sorted last.
func TestRestoreLeavesTheBaseWindowCurrent(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")
	killSession(t)

	f.mustRun("restore", "-d")

	if got := activeWindowIn(t, "proj"); got != "main" {
		t.Errorf("session proj opens on %q, want the base window", got)
	}
}

// TestRestoreOpensWhatIsMissingAndLeavesTheRestAlone is idempotence, which is
// what makes `tw restore` a reasonable thing to type in a session that is
// already up: it means "open whatever is missing here".
//
// `new` opens a window per worktree and no base window, so the first restore
// has exactly one window to add — and the second has none.
func TestRestoreOpensWhatIsMissingAndLeavesTheRestAlone(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")
	before := map[string]string{
		"eng-1": windowIDNamed(t, "proj", "eng-1"),
		"eng-2": windowIDNamed(t, "proj", "eng-2"),
	}

	f.mustRun("restore", "-d")
	f.mustRun("restore", "-d")

	if got, want := windowsIn(t, "proj"), []string{"eng-1", "eng-2", "main"}; !slices.Equal(got, want) {
		t.Errorf("windows in session proj = %v, want %v — nothing duplicated, the base window added", got, want)
	}
	for name, id := range before {
		if got := windowIDNamed(t, "proj", name); got != id {
			t.Errorf("window %s is now %s, was %s — an open window must be left exactly as it is", name, got, id)
		}
	}
}

// TestRestoreRunsResumeCommandInEveryWindow is what gives a restored agent its
// conversation back: the window runs resume_command, the same "carry on where I
// left off" `resume` gives one worktree at a time.
//
// The marker is relative, so each window leaves it in its own checkout — which
// is what proves the command ran there rather than that treewright said it
// would.
func TestRestoreRunsResumeCommandInEveryWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'touch resumed && sleep 300'\n")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")
	killSession(t)

	f.mustRun("restore", "-d")

	for _, dir := range []string{f.MainDir, f.DirFor("eng-1"), f.DirFor("eng-2")} {
		waitForFile(t, filepath.Join(dir, "resumed"), "resume_command in "+dir)
	}
}

// TestRestoreFreshRunsCommandInstead covers --fresh, which is the same request
// resume's --fresh makes: a new agent session in every window, however much
// there was to continue.
func TestRestoreFreshRunsCommandInstead(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	killSession(t)
	// Rewritten after the worktree exists, so the marker can only come from the
	// restore: `new` runs command too.
	f.setConfig("main_dir = '" + f.MainDir + "'\n" +
		"command = 'touch fresh && sleep 300'\nresume_command = 'touch resumed && sleep 300'\n")

	f.mustRun("restore", "--fresh", "-d")

	waitForFile(t, filepath.Join(f.DirFor("eng-1"), "fresh"), "command under --fresh")
	if _, err := os.Stat(filepath.Join(f.DirFor("eng-1"), "resumed")); err == nil {
		t.Error("resume_command ran under --fresh")
	}
}

// TestRestoreDoesNotDragTheClientThroughEveryWindow is why restore needed a way
// into openWindow that focuses nothing.
//
// Focusing each window in turn is a switch-client per window, which for an
// attached client is the session flickering past under their hands and landing
// wherever the loop ended. There is no client in a headless test, and that is
// what makes the attempt visible: a switch-client with nobody to move fails, and
// focusWindow reports it. No such warning is the proof that nothing was moved.
func TestRestoreDoesNotDragTheClientThroughEveryWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")
	killSession(t)

	// A session of the user's own, with treewright being typed into it.
	startSession(t, "other", "DECOY", f.MainDir)
	insideSession(t, "other")

	r := f.exec("restore", "-d")
	if r.err != nil {
		t.Fatalf("restore: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "could not switch to session") {
		t.Errorf("stderr = %q, want no client move attempted for any window", r.stderr)
	}
	if got := activeWindowIn(t, "other"); got != "DECOY" {
		t.Errorf("session other now shows %q, want the window it was showing", got)
	}
}

// TestRestoreReportsAWindowItCouldNotOpenAndStaysOut covers the one time restore
// has anything to say: attaching would paint over the report, so a restore that
// did not finish stays out of the session and hands the reader the way in.
//
// The worktree behind the failure still gets its window. One window failing is
// not the rest of them failing, and the ones behind it are exactly what the
// reader would otherwise be opening by hand.
func TestRestoreReportsAWindowItCouldNotOpenAndStaysOut(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")
	killSession(t)
	refuseWindow(t, "eng-1")

	r := f.exec("restore")
	if !errors.Is(r.err, ErrSilent) {
		t.Fatalf("err = %v, want ErrSilent — the report is written here, not by main", r.err)
	}
	for _, want := range []string{
		"1 window could not be opened",
		"this terminal was not attached",
		"missing eng-1",
		"attach anyway with treewright attach proj",
	} {
		if !strings.Contains(flat(r.stderr), want) {
			t.Errorf("stderr = %q, want %q in the report", r.stderr, want)
		}
	}
	if got := windowsIn(t, "proj"); !slices.Contains(got, "eng-2") {
		t.Errorf("windows in session proj = %v, want the worktree behind the failure opened too", got)
	}
}

// TestRestoreSkipsTheAttachWithoutATerminal is the scripted caller who forgot
// -d. Every bit of the work is done correctly, and tmux cannot take a pipe, so
// the attach is skipped and said rather than attempted and failed.
func TestRestoreSkipsTheAttachWithoutATerminal(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	killSession(t)
	noTerminalHere(t)

	r := f.exec("restore")
	if r.err != nil {
		t.Fatalf("restore: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "no terminal here") {
		t.Errorf("stderr = %q, want the skipped attach said out loud", r.stderr)
	}
	if !strings.Contains(flat(r.stderr), "attach with treewright attach proj") {
		t.Errorf("stderr = %q, want the way in", r.stderr)
	}
	// And the work was done anyway, which is the whole reason this is not an
	// error.
	if got, want := windowsIn(t, "proj"), []string{"main", "eng-1"}; !slices.Equal(got, want) {
		t.Errorf("windows in session proj = %v, want %v", got, want)
	}
}

// TestRestoreAttachesOnACleanRun covers the default, in the one form a headless
// test can reach: inside tmux the attach is a switch-client, which needs no
// terminal.
//
// Moving a client needs a client, which a headless test has none of, so the move
// fails — and that failure is the proof it was attempted. A clean restore also
// prints no summary of its own: the session is about to be on screen, and the
// report would say "opened two windows" to somebody about to look at two
// windows.
func TestRestoreAttachesOnACleanRun(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	killSession(t)

	startSession(t, "other", "DECOY", f.MainDir)
	insideSession(t, "other")

	r := f.exec("restore")
	if r.err == nil || !strings.Contains(r.err.Error(), "no tmux client followed") {
		t.Fatalf("err = %v, want the failure to come from trying to move a client\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "windows open in tmux session") {
		t.Errorf("stderr = %q, want no summary on a clean restore", r.stderr)
	}
}

// TestRestoreSaysWhenYouAreAlreadyInTheSession is the same decision reaching the
// case where nothing visible would happen. Attaching a second client to the
// session this one is in is the nesting tmux warns about, so it is refused —
// and something has to be said, or the command reads as having silently failed.
func TestRestoreSaysWhenYouAreAlreadyInTheSession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")
	insideSession(t, "proj")

	r := f.exec("restore")
	if r.err != nil {
		t.Fatalf("restore: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "already attached to proj") {
		t.Errorf("stderr = %q, want it to say the client is already there", r.stderr)
	}
}

// TestRestoreSaysWhenEveryWindowIsInAnotherSession covers the state where there
// is nothing for restore to attach to: every window it wanted is already open
// somewhere else, so this repository never gets a session of its own.
//
// A window in another session is used rather than duplicated — that rule is
// older than restore — and here it is also left exactly where it is, which is
// the half restore says differently. What would otherwise happen is tmux's own
// complaint about a session target that is not there.
func TestRestoreSaysWhenEveryWindowIsInAnotherSession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	// A window of somebody's own, standing in the main checkout, in a session that
	// is not this repository's. No worktrees, so it is the only window restore
	// wants.
	startSession(t, "other", "hand-rolled", f.MainDir)

	r := f.exec("restore", "-d")
	if r.err != nil {
		t.Fatalf("restore: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(flat(r.stderr), "window hand-rolled is in session other, not proj leaving it where it is") {
		t.Errorf("stderr = %q, want the window reported where it is and left there", r.stderr)
	}
	if !strings.Contains(r.stderr, "no tmux session proj is running") {
		t.Errorf("stderr = %q, want it to say there is nothing to attach to", r.stderr)
	}
	if got := windowsIn(t, "other"); !slices.Equal(got, []string{"hand-rolled"}) {
		t.Errorf("windows in session other = %v, want the one that was already there", got)
	}
}

// TestRestoreWarnsAboutASetupThatFailed keeps the warning `resume` and `cd`
// carry: a worktree whose post_create stopped half way is one whose build is
// about to fail for reasons that have nothing to do with the work.
//
// It is a warning and not a failure, so it must not hold the attach back — which
// is also why it is the one thing about a restore the reader may never see, the
// session painting over it a moment later. See the note at the end of restore's
// section in docs/design-notes.md.
func TestRestoreWarnsAboutASetupThatFailed(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n"+
		"post_create = ['exit 1']\n")
	f.mustRun("new", "eng-1")
	_, failed := postCreatePaths(&config.Config{MainDir: f.MainDir}, "eng-1")
	waitForFile(t, failed, "post_create")
	killSession(t)

	r := f.exec("restore", "-d")
	if r.err != nil {
		t.Fatalf("restore: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(flat(r.stderr), "post_create failed in eng-1") {
		t.Errorf("stderr = %q, want the half-finished setup reported", r.stderr)
	}
}
