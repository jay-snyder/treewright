package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `move` is the one command here that can destroy uncommitted work, so what is
// tested is not only that the work arrives but that it is never gone from both
// places at once: the base checkout keeps its copy until the patch has landed
// and been checked, and every failure before that leaves it exactly as it was.

// dirtyBase leaves the main checkout holding one of each kind of uncommitted
// change, and returns the paths worth asserting about.
//
// The ignored files are the point of two of them. `.env` is what carry_files
// copies into every worktree, and `scratch/.env` sits inside an untracked
// directory — which is what `git clean -fd` takes with the directory, and the
// reason the files to delete are recorded rather than swept.
func dirtyBase(t *testing.T, f *fixture) {
	t.Helper()
	f.Write(f.MainDir, "a.txt", "seed\nmodified in the base checkout\n")
	f.Write(f.MainDir, "staged.txt", "staged before the move\n")
	f.Git(f.MainDir, "add", "staged.txt")
	f.Write(f.MainDir, "untracked.txt", "never tracked\n")
	f.Write(f.MainDir, "scratch/notes.md", "notes on the way\n")
	f.Write(f.MainDir, "scratch/.env", "SECRET=1\n")
	f.Write(f.MainDir, ".env", "TOKEN=2\n")
}

func read(t *testing.T, parts ...string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		return ""
	}
	return string(body)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestMoveCarriesEveryKindOfUncommittedChange is the whole command in one pass:
// what git tracks and what it does not, staged and unstaged, all of it arriving
// in the new worktree and none of it left in the checkout it came from.
func TestMoveCarriesEveryKindOfUncommittedChange(t *testing.T) {
	f := newFixture(t, "")
	dirtyBase(t, f)

	r := f.exec("move", "eng-1")
	if r.err != nil {
		t.Fatalf("move: %v\n%s", r.err, r.both())
	}

	// stdout is the worktree's path and nothing else, as `new`'s is.
	wt := f.DirFor("eng-1")
	if got, want := r.stdout, wt+"\n"; got != want {
		t.Errorf("stdout = %q, want exactly %q", got, want)
	}

	for _, tc := range []struct{ path, want string }{
		{"a.txt", "seed\nmodified in the base checkout\n"},
		{"staged.txt", "staged before the move\n"},
		{"untracked.txt", "never tracked\n"},
		{"scratch/notes.md", "notes on the way\n"},
	} {
		if got := read(t, wt, tc.path); got != tc.want {
			t.Errorf("%s in the worktree = %q, want %q", tc.path, got, tc.want)
		}
	}

	// And the base checkout is back at HEAD: the modification undone, the files
	// it created gone.
	if got, want := read(t, f.MainDir, "a.txt"), "seed\n"; got != want {
		t.Errorf("a.txt in the base checkout = %q, want %q", got, want)
	}
	for _, rel := range []string{"staged.txt", "untracked.txt", "scratch/notes.md"} {
		if exists(filepath.Join(f.MainDir, rel)) {
			t.Errorf("%s is still in the base checkout after being moved", rel)
		}
	}
	if status := f.Git(f.MainDir, "status", "--porcelain"); status != "" {
		t.Errorf("base checkout status = %q, want it clean", status)
	}

	// The work arrives staged, a three-way apply going through the index — which
	// is why the check that it arrived has to be `diff HEAD`.
	if staged := f.Git(wt, "diff", "--cached", "--name-only"); staged == "" {
		t.Errorf("nothing is staged in the worktree, want the applied work")
	}
	if unstaged := f.Git(wt, "diff", "--name-only"); unstaged != "" {
		t.Errorf("worktree has unstaged changes %q, want the work staged", unstaged)
	}

	// Nothing left behind: the patch existed only until the work had landed.
	if patches, _ := filepath.Glob(filepath.Join(f.MainDir, ".git", "treewright", "move-*.patch")); len(patches) != 0 {
		t.Errorf("patch files left behind after a move that worked: %v", patches)
	}
}

// TestMoveLeavesTheFilesItDidNotTake is the guard against the command an
// improvising hand reaches for. `git clean -fd` deletes every untracked file
// rather than these, and an untracked directory it removes takes the ignored
// files inside it too — so the .env in scratch/ is the one that would go
// silently.
func TestMoveLeavesTheFilesItDidNotTake(t *testing.T) {
	f := newFixture(t, "")
	dirtyBase(t, f)

	if r := f.exec("move", "eng-1"); r.err != nil {
		t.Fatalf("move: %v\n%s", r.err, r.both())
	}

	for _, rel := range []string{".env", "scratch/.env"} {
		if got := read(t, f.MainDir, rel); got == "" {
			t.Errorf("%s was deleted from the base checkout — it is ignored, not moved", rel)
		}
	}
	// The ignored files are also not in the worktree by way of the patch: what
	// puts a .env there is carry_files, which is a separate decision.
	if exists(filepath.Join(f.DirFor("eng-1"), "scratch", ".env")) {
		t.Error("an ignored file was carried by the patch")
	}
}

// TestMoveRefusesACleanCheckout: there is nothing to move, and the error names
// the command that was meant instead.
func TestMoveRefusesACleanCheckout(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("move", "eng-1")
	if r.err == nil {
		t.Fatalf("move from a clean checkout succeeded\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "treewright new eng-1") {
		t.Errorf("error = %q, want the command that was meant named", msg)
	}
	if f.Exists("eng-1") {
		t.Error("a worktree was created for a move with nothing to move")
	}
}

// TestMoveLeavesTheBaseAloneWhenTheWorkDoesNotApply is the case the ordering
// exists for. The patch is written against the checkout's HEAD and applied over
// a worktree forked from origin, and the two can disagree — so the work is
// verified in the worktree before a byte of the checkout is touched.
func TestMoveLeavesTheBaseAloneWhenTheWorkDoesNotApply(t *testing.T) {
	f := newFixture(t, "")

	// origin/main moves on and the base checkout does not follow, so the patch's
	// context is a commit the new worktree does not have — and both sides have
	// changed the same line, which a three-way merge cannot settle.
	f.Commit(f.MainDir, "landed on origin")
	f.Push(f.MainDir, "main")
	f.Git(f.MainDir, "reset", "--hard", "--quiet", "HEAD~1")
	f.Write(f.MainDir, "a.txt", "seed\nstarted in the base checkout\n")
	f.Write(f.MainDir, "untracked.txt", "never tracked\n")

	r := f.exec("move", "eng-1")
	if r.err == nil {
		t.Fatalf("move succeeded with a patch that cannot apply\n%s", r.both())
	}
	msg := flat(r.err.Error())
	if !strings.Contains(msg, "nothing was taken out of the base checkout") {
		t.Errorf("error = %q, want it to say the checkout is untouched", msg)
	}
	if !strings.Contains(msg, "move-eng-1.patch") {
		t.Errorf("error = %q, want the patch named as the way back", msg)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want no path for work that did not arrive", r.stdout)
	}

	// Every byte of it still there, which is the promise.
	if got, want := read(t, f.MainDir, "a.txt"), "seed\nstarted in the base checkout\n"; got != want {
		t.Errorf("a.txt in the base checkout = %q, want it untouched at %q", got, want)
	}
	if got := read(t, f.MainDir, "untracked.txt"); got != "never tracked\n" {
		t.Errorf("untracked.txt = %q, want it untouched", got)
	}
	// Including its index: the intent-to-add entries treewright wrote to build
	// the patch are taken back off, so nothing is staged that was not before.
	if staged := f.Git(f.MainDir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("base checkout has %q staged, want the index as it was found", staged)
	}
	// And the patch survives, being a second copy of work that exists in one
	// place.
	patch := filepath.Join(f.MainDir, ".git", "treewright", "move-eng-1.patch")
	if !exists(patch) {
		t.Error("the patch was deleted after a failure, taking the way back with it")
	}
}

// TestMoveKeepLeavesBothCopies covers the flag: the work reaches the worktree
// and the checkout it came from is not cleared, for whoever wants to compare or
// carry on in place.
func TestMoveKeepLeavesBothCopies(t *testing.T) {
	f := newFixture(t, "")
	dirtyBase(t, f)

	r := f.exec("move", "--keep", "eng-1")
	if r.err != nil {
		t.Fatalf("move --keep: %v\n%s", r.err, r.both())
	}

	if got := read(t, f.DirFor("eng-1"), "untracked.txt"); got != "never tracked\n" {
		t.Errorf("untracked.txt in the worktree = %q, want the work to have arrived anyway", got)
	}
	if got, want := read(t, f.MainDir, "a.txt"), "seed\nmodified in the base checkout\n"; got != want {
		t.Errorf("a.txt in the base checkout = %q, want %q — --keep leaves it", got, want)
	}
	if got := read(t, f.MainDir, "untracked.txt"); got != "never tracked\n" {
		t.Errorf("untracked.txt = %q, want --keep to leave it", got)
	}
	// The index is still the caller's own, staged file and all.
	if staged := f.Git(f.MainDir, "diff", "--cached", "--name-only"); staged != "staged.txt" {
		t.Errorf("staged in the base checkout = %q, want the caller's own staging kept", staged)
	}
}

// TestMoveDoesNotUnstageWhatTheCallerStaged is the reason the index is put back
// by path rather than with a bare `git reset`. A pathless reset would unstage
// everything the caller had staged themselves, which is theirs and none of a
// move's business — and it would do it on the failure path too, where the
// promise is that nothing was touched.
func TestMoveDoesNotUnstageWhatTheCallerStaged(t *testing.T) {
	f := newFixture(t, "")
	f.Write(f.MainDir, "a.txt", "seed\nstaged by hand\n")
	f.Git(f.MainDir, "add", "a.txt")
	f.Write(f.MainDir, "untracked.txt", "never tracked\n")

	// --keep, so the checkout is left exactly as the patch found it.
	if r := f.exec("move", "--keep", "eng-1"); r.err != nil {
		t.Fatalf("move --keep: %v\n%s", r.err, r.both())
	}

	if staged := f.Git(f.MainDir, "diff", "--cached", "--name-only"); staged != "a.txt" {
		t.Errorf("staged = %q, want a.txt still staged as the caller left it", staged)
	}
	if untracked := f.Git(f.MainDir, "ls-files", "--others", "--exclude-standard"); untracked != "untracked.txt" {
		t.Errorf("untracked = %q, want untracked.txt still untracked", untracked)
	}
}

// TestMoveHandsTheAgentItsInstructions: the worktree is made the way `new`
// makes it, prompt included, so the agent that arrives is told what the work it
// has just been handed is for.
func TestMoveHandsTheAgentItsInstructions(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "prompt")
	f := newFixture(t, "command = \"printf %s {prompt} > "+marker+"\"\n")
	dirtyBase(t, f)

	f.mustRun("move", "eng-1", "--prompt", "carry on with the rounding fix")
	waitForContent(t, marker, "carry on with the rounding fix", "the window's command")
}
