// This is an external test package (git_test, not git) because it imports the
// gittest helper, which itself imports git — an internal test package importing
// it would be an import cycle. Everything under test here is exported anyway.
package git_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/gittest"
)

// ---- discovery -------------------------------------------------------------

func TestManagedIsEmptyWithNoWorktrees(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}

	// A newline-split of empty command output yields one empty element rather
	// than none, which would make len() lie and every "is there anything here?"
	// guard pass. Callers must be able to trust this count.
	managed, err := repo.Managed()
	if err != nil {
		t.Fatalf("Managed: %v", err)
	}
	if len(managed) != 0 {
		t.Errorf("want no managed worktrees, got %d: %+v", len(managed), managed)
	}
}

func TestManagedFindsSiblingsOnly(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}
	f.Worktree("alpha")
	f.Worktree("beta")

	// A worktree that is not a "<repo>-<slug>" sibling belongs to somebody else
	// and is not treewright's to list or remove.
	outside := filepath.Join(f.Root, "unrelated")
	f.Git(f.MainDir, "worktree", "add", "--quiet", outside, "-b", "someone-else", "origin/main")

	managed, err := repo.Managed()
	if err != nil {
		t.Fatalf("Managed: %v", err)
	}
	var slugs []string
	for _, wt := range managed {
		slugs = append(slugs, wt.Slug)
	}
	if got, want := strings.Join(slugs, ","), "alpha,beta"; got != want {
		t.Errorf("slugs = %q, want %q", got, want)
	}
	for _, wt := range managed {
		if wt.Branch != f.BranchFor(wt.Slug) {
			t.Errorf("%s: branch = %q, want %q", wt.Slug, wt.Branch, f.BranchFor(wt.Slug))
		}
	}
}

// TestManagedWorksThroughASymlinkedPath is a regression test. git reports fully
// resolved paths, so building the sibling prefix from an unresolved caller path
// matched nothing: `new` created a worktree that `ls`, `prune`, `resume`, and
// completion then could not see.
func TestManagedWorksThroughASymlinkedPath(t *testing.T) {
	f := gittest.New(t)
	f.Worktree("viasym")

	viaLink := f.Symlink()
	if viaLink == f.MainDir {
		t.Fatal("symlinked path is identical to the real one; test proves nothing")
	}

	managed, err := (git.Repo{Dir: viaLink}).Managed()
	if err != nil {
		t.Fatalf("Managed: %v", err)
	}
	if len(managed) != 1 || managed[0].Slug != "viasym" {
		t.Errorf("through symlink: got %+v, want one worktree with slug viasym", managed)
	}
}

func TestMainDirIsReportedFromInsideAWorktree(t *testing.T) {
	f := gittest.New(t)
	wt := f.Worktree("alpha")

	// Config discovery depends on this: standing anywhere in the repo must
	// identify the same main checkout.
	got, err := (git.Repo{Dir: wt.Dir}).MainDir()
	if err != nil {
		t.Fatalf("MainDir: %v", err)
	}
	if got != f.MainDir {
		t.Errorf("MainDir from worktree = %q, want %q", got, f.MainDir)
	}
}

// ---- state -----------------------------------------------------------------

func TestInspectStatuses(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}

	atbase := f.Worktree("atbase") // left alone, so its tip is still origin/main

	localonly := f.Worktree("localonly")
	f.Commit(localonly.Dir, "local change")

	pushed := f.Worktree("pushed") // on origin but unmerged: an open pull request
	f.Commit(pushed.Dir, "pushed change")
	f.Push(pushed.Dir, pushed.Branch)

	dirty := f.Worktree("dirty")
	f.Write(dirty.Dir, "a.txt", "seed\nuncommitted\n")

	tests := []struct {
		name       string
		wt         git.Worktree
		want       git.Status
		wantDirty  int
		wantUnpush int
		wantAhead  int
	}{
		{"tip at base is merged", atbase, git.StatusMerged, 0, 0, 0},
		{"local-only commit is unpushed", localonly, git.StatusUnpushed, 0, 1, 1},
		{"pushed but unmerged is active", pushed, git.StatusActive, 0, 0, 1},
		{"uncommitted changes outrank all", dirty, git.StatusDirty, 1, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := repo.Inspect(tc.wt, "main")
			if got.Status != tc.want {
				t.Errorf("Status = %q, want %q", got.Status, tc.want)
			}
			if got.DirtyFiles != tc.wantDirty {
				t.Errorf("DirtyFiles = %d, want %d", got.DirtyFiles, tc.wantDirty)
			}
			if got.Unpushed != tc.wantUnpush {
				t.Errorf("Unpushed = %d, want %d", got.Unpushed, tc.wantUnpush)
			}
			if !got.Compared {
				t.Fatal("Compared = false, want an ahead/behind comparison")
			}
			if got.Ahead != tc.wantAhead || got.Behind != 0 {
				t.Errorf("ahead/behind = %d/%d, want %d/0", got.Ahead, got.Behind, tc.wantAhead)
			}
		})
	}
}

// TestSquashMergeReadsAsMerged covers the case treewright exists to get right.
// Where a project squash-merges its pull requests, a landed branch's own commits
// never appear on origin, and a naive "are these commits upstream?" check calls
// that unpushed work and refuses to clean it up.
func TestSquashMergeReadsAsMerged(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}
	wt := f.Worktree("squashed")

	f.Write(wt.Dir, "a.txt", "seed\nchange 1\n")
	f.Git(wt.Dir, "commit", "--quiet", "-am", "work 1")
	f.Write(wt.Dir, "a.txt", "seed\nchange 1\nchange 2\n")
	f.Git(wt.Dir, "commit", "--quiet", "-am", "work 2")
	f.Push(wt.Dir, wt.Branch)

	if repo.IsMerged(wt.Branch, "main") {
		t.Fatal("branch reads as merged before the squash merge happened")
	}

	f.SquashMerge(wt.Branch, "squashed work (#1)")

	if !repo.IsMerged(wt.Branch, "main") {
		t.Error("squash-merged branch does not read as merged")
	}
	// Its commits are genuinely absent from origin now, which is why the
	// unpushed count alone cannot decide whether removal is safe.
	if got := repo.Unpushed(wt.Branch); got != 2 {
		t.Errorf("Unpushed = %d, want 2 (commits exist only locally after a squash merge)", got)
	}
	if got := repo.Inspect(wt, "main").Status; got != git.StatusMerged {
		t.Errorf("Status = %q, want %q", got, git.StatusMerged)
	}
}

// TestIsMergedWritesOneObjectEver pins the squash check's synthetic commit to a
// fixed identity and date. Without that its hash varied per invocation, so every
// `ls` left behind another dangling object.
func TestIsMergedWritesOneObjectEver(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}
	wt := f.Worktree("unmerged")
	f.Commit(wt.Dir, "work")

	// First call takes the squash path and writes the synthetic commit.
	repo.IsMerged(wt.Branch, "main")
	after := f.LooseObjects()

	// Further calls must reuse that identical object.
	for range 5 {
		repo.IsMerged(wt.Branch, "main")
	}
	if got := f.LooseObjects(); got != after {
		t.Errorf("loose objects grew from %d to %d across repeated calls", after, got)
	}
}

func TestIsMergedAfterNormalMerge(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}
	wt := f.Worktree("normal")
	f.Commit(wt.Dir, "change")

	f.Git(f.MainDir, "merge", "--no-ff", "--quiet", "-m", "merge", wt.Branch)
	f.Git(f.MainDir, "push", "--quiet", "origin", "main")

	if !repo.IsMerged(wt.Branch, "main") {
		t.Error("normally merged branch does not read as merged")
	}
}

func TestQueriesOnMissingRefsAreSafe(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}

	if got := repo.Unpushed("no-such-branch"); got != 0 {
		t.Errorf("Unpushed(missing) = %d, want 0", got)
	}
	if repo.IsMerged("no-such-branch", "main") {
		t.Error("IsMerged(missing) = true, want false")
	}
	if repo.IsMerged("", "main") {
		t.Error("IsMerged(detached worktree) = true, want false")
	}
	if repo.BranchExists("no-such-branch") {
		t.Error("BranchExists(missing) = true, want false")
	}
	if got := git.DirtyFiles(filepath.Join(f.Root, "does-not-exist")); got != 0 {
		t.Errorf("DirtyFiles(missing dir) = %d, want 0", got)
	}
	// An unknown base cannot be compared against, and that must read as unknown
	// rather than as zero divergence.
	if _, _, ok := repo.AheadBehind("main", "no-such-base"); ok {
		t.Error("AheadBehind against a missing base reported a valid comparison")
	}
}

func TestAheadBehindCountsBothSides(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}
	wt := f.Worktree("diverged")

	// One commit on the branch, two on main: ahead 1, behind 2.
	f.Commit(wt.Dir, "branch work")
	f.Write(f.MainDir, "b.txt", "one\n")
	f.Git(f.MainDir, "add", ".")
	f.Git(f.MainDir, "commit", "--quiet", "-m", "main 1")
	f.Write(f.MainDir, "c.txt", "two\n")
	f.Git(f.MainDir, "add", ".")
	f.Git(f.MainDir, "commit", "--quiet", "-m", "main 2")
	f.Git(f.MainDir, "push", "--quiet", "origin", "main")

	ahead, behind, ok := repo.AheadBehind(wt.Branch, "main")
	if !ok {
		t.Fatal("comparison failed")
	}
	if ahead != 1 || behind != 2 {
		t.Errorf("ahead/behind = %d/%d, want 1/2", ahead, behind)
	}
}

func TestWorktreesReportsBranchWithoutRefPrefix(t *testing.T) {
	f := gittest.New(t)
	f.Worktree("named")

	list, err := (git.Repo{Dir: f.MainDir}).Worktrees()
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	for _, wt := range list {
		if strings.HasPrefix(wt.Branch, "refs/") {
			t.Errorf("branch %q still carries its ref prefix", wt.Branch)
		}
	}
}

// ---- fetching --------------------------------------------------------------

// flakyGit puts a shim named "git" first on PATH that fails its first `fails`
// fetches and then hands every call to the real git. It returns the path of the
// file counting the fetches, which is the whole of what these tests assert on:
// how many times treewright asked.
//
// The real git is resolved before PATH is changed, so the shim delegates to the
// binary the rest of the fixture is already using rather than to itself.
func flakyGit(t *testing.T, fails int) (attempts string) {
	t.Helper()
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find the real git: %v", err)
	}
	dir := t.TempDir()
	attempts = filepath.Join(dir, "attempts")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = fetch ]; then
	n=$(cat %[1]q 2>/dev/null || echo 0)
	n=$((n + 1))
	printf '%%s' "$n" > %[1]q
	if [ "$n" -le %[2]d ]; then
		echo "fatal: unable to access 'origin': the blip" >&2
		exit 128
	fi
fi
exec %[3]q "$@"
`, attempts, fails, gitBinary)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the git shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return attempts
}

// fetchesSoFar reads the counter flakyGit keeps. A missing file is none, which
// is what a test asserting that nothing was retried needs to be able to see.
func fetchesSoFar(t *testing.T, attempts string) int {
	t.Helper()
	raw, err := os.ReadFile(attempts)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read the fetch counter: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("fetch counter holds %q: %v", raw, err)
	}
	return n
}

// TestAFetchThatFailsOnceIsAskedAgain is the incident this covers: a fetch
// failed, twelve run by hand straight afterwards all succeeded, and the branch
// had already been forked from a base nobody had checked. The blip is only
// worth anything if the second attempt is what the caller gets back.
func TestAFetchThatFailsOnceIsAskedAgain(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}
	attempts := flakyGit(t, 1)

	if err := repo.FetchRetrying("origin", "main"); err != nil {
		t.Fatalf("FetchRetrying: %v", err)
	}
	if got := fetchesSoFar(t, attempts); got != 2 {
		t.Errorf("git fetch ran %d times, want 2 — the first failure was not retried", got)
	}
}

// TestARetryIsOneRetry keeps the recovery from becoming a loop. A failure that
// is not a blip has to reach the offline fallback quickly, and the error the
// caller writes its warning from is the second attempt's, not a summary of
// both.
func TestARetryIsOneRetry(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}
	attempts := flakyGit(t, 99)

	err := repo.FetchRetrying("origin", "main")
	if err == nil {
		t.Fatal("want an error from a fetch that never succeeds, got none")
	}
	if got := fetchesSoFar(t, attempts); got != 2 {
		t.Errorf("git fetch ran %d times, want exactly 2", got)
	}
	if said := git.Said(err); !strings.Contains(said, "the blip") {
		t.Errorf("the error does not carry what git said: %q", said)
	}
}

// TestFetchItselfAsksOnce holds the line between the two entry points. The
// teardown callers in rm and prune stay on Fetch because a stale origin/<base>
// only makes IsMerged say no — the safe direction — and paying a backoff there
// would slow every teardown to reach an answer nothing acts on.
func TestFetchItselfAsksOnce(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}
	attempts := flakyGit(t, 1)

	if err := repo.Fetch("origin", "main"); err == nil {
		t.Fatal("want the shim's failure back from Fetch, got none")
	}
	if got := fetchesSoFar(t, attempts); got != 1 {
		t.Errorf("git fetch ran %d times, want 1 — Fetch does not retry", got)
	}
}

// ---- introspection, as setup and doctor use it -----------------------------

// TestDefaultBranchReadsOriginHEAD covers the value setup writes as base_branch.
// The clone below is what records origin/HEAD; a repo assembled by init and
// remote add — which is what the fixture is — has no such ref, so both the
// preferred route and the fallback are exercised.
func TestDefaultBranchReadsOriginHEAD(t *testing.T) {
	f := gittest.New(t)

	if got := (git.Repo{Dir: f.MainDir}).DefaultBranch(); got != "main" {
		t.Errorf("DefaultBranch with no origin/HEAD = %q, want main from the checked-out branch", got)
	}

	clone := filepath.Join(f.Root, "cloned")
	f.Git(f.Root, "clone", "--quiet", f.Origin, clone)
	if got := (git.Repo{Dir: clone}).DefaultBranch(); got != "main" {
		t.Errorf("DefaultBranch in a clone = %q, want main", got)
	}

	// origin/HEAD is recorded once, at clone time, and survives the branch it
	// names being renamed or never pushed. Trusting it blindly would write a
	// base_branch into a generated config that nothing can fork from.
	f.Git(clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	if got := (git.Repo{Dir: clone}).DefaultBranch(); got != "main" {
		t.Errorf("DefaultBranch with a stale origin/HEAD = %q, want the checked-out main", got)
	}
}

func TestUserEmailAndRemote(t *testing.T) {
	f := gittest.New(t)
	repo := git.Repo{Dir: f.MainDir}

	if got := repo.UserEmail(); got != "test@example.com" {
		t.Errorf("UserEmail = %q, want the repo-local identity", got)
	}
	if !repo.HasRemote("origin") {
		t.Error("HasRemote(origin) = false, want true")
	}
	if repo.HasRemote("upstream") {
		t.Error("HasRemote(upstream) = true, want false for a remote that is not configured")
	}
}

// TestRemoteBranchNamespacesCountsWhatOriginUses is what lets setup guess a
// team's branch prefixes. It counts against origin rather than the local branches,
// so a fresh clone that has none of its own still sees the convention.
func TestRemoteBranchNamespacesCountsWhatOriginUses(t *testing.T) {
	f := gittest.New(t)
	for _, branch := range []string{"feature/a", "feature/b", "bug/c", "release-2.0"} {
		f.Git(f.MainDir, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
	}
	f.Git(f.MainDir, "fetch", "--quiet", "origin")

	got := (git.Repo{Dir: f.MainDir}).RemoteBranchNamespaces("origin")
	if got["feature/"] != 2 || got["bug/"] != 1 {
		t.Errorf("namespaces = %v, want feature/ twice and bug/ once", got)
	}
	// main and release-2.0 have no namespace at all, and a dash is not a delimiter:
	// "eng-142-white-screen" is a ticket key, not a namespace called "eng-".
	for _, absent := range []string{"main/", "release-2.0/", "release-", "eng-"} {
		if _, ok := got[absent]; ok {
			t.Errorf("namespaces = %v, want no %q", got, absent)
		}
	}
}

// TestIgnoredFilesCollapsesIgnoredDirectories is why setup can propose carry
// files at all: without the collapse, a repo with node_modules reports thousands
// of paths and nothing useful can be filtered out of them.
func TestIgnoredFilesCollapsesIgnoredDirectories(t *testing.T) {
	f := gittest.New(t)
	f.Write(f.MainDir, ".gitignore", ".env\nnode_modules/\n")
	f.Git(f.MainDir, "add", ".gitignore")
	f.Git(f.MainDir, "commit", "--quiet", "-m", "ignore rules")

	f.Write(f.MainDir, ".env", "SECRET=1\n")
	f.Write(f.MainDir, "node_modules/pkg/index.js", "module.exports = 1\n")
	f.Write(f.MainDir, "node_modules/pkg/.env", "junk\n")

	got := (git.Repo{Dir: f.MainDir}).IgnoredFiles()
	var sawEnv, sawDir bool
	for _, rel := range got {
		switch rel {
		case ".env":
			sawEnv = true
		case "node_modules/":
			sawDir = true
		}
		if strings.HasPrefix(rel, "node_modules/") && rel != "node_modules/" {
			t.Errorf("listed %q inside a wholly ignored directory", rel)
		}
	}
	if !sawEnv {
		t.Errorf("IgnoredFiles = %v, want .env listed individually", got)
	}
	if !sawDir {
		t.Errorf("IgnoredFiles = %v, want node_modules collapsed to one entry", got)
	}
}
