// Package gittest builds throwaway git repositories for tests: a bare "origin"
// plus a main checkout pushed to it, in a temp directory that the testing
// package removes afterwards.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jay-snyder/treemux/internal/git"
)

// BranchPrefix is the prefix Worktree gives the branches it creates. Tests that
// configure treemux must use the same value.
const BranchPrefix = "x/"

// Repo is a scratch repository.
type Repo struct {
	t       *testing.T
	Root    string // holds origin.git and the checkout
	Origin  string // bare remote
	MainDir string // main checkout, on branch "main"
}

// New creates the repository with one commit on main, pushed to origin.
//
// Root is resolved through any symlinks so that tests can compare paths against
// what git reports without normalizing every assertion. The case where a caller
// reaches a repo through a symlink is covered by its own tests, rather than by
// making every fixture juggle two spellings of the same path.
func New(t *testing.T) *Repo {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	r := &Repo{
		t:       t,
		Root:    root,
		Origin:  filepath.Join(root, "origin.git"),
		MainDir: filepath.Join(root, "repo"),
	}

	// The bare repo's default branch is set explicitly so that its HEAD names the
	// branch that actually gets pushed. Left to init.defaultBranch it may say
	// "master", which a clone then records as origin/HEAD — a ref pointing at a
	// branch that does not exist, and a state worth reproducing only deliberately.
	r.Git(root, "init", "--quiet", "--bare", "-b", "main", r.Origin)
	r.Git(root, "init", "--quiet", "-b", "main", r.MainDir)
	// Identity is set locally, and signing forced off, so a developer's global
	// git config cannot make these tests fail or hang.
	r.Git(r.MainDir, "config", "user.email", "test@example.com")
	r.Git(r.MainDir, "config", "user.name", "Test")
	r.Git(r.MainDir, "config", "commit.gpgsign", "false")
	r.Git(r.MainDir, "remote", "add", "origin", r.Origin)

	r.Write(r.MainDir, "a.txt", "seed\n")
	// Mirrors reality: the files treemux carries into a new worktree are
	// gitignored, so carrying them must not make the worktree read as dirty.
	r.Write(r.MainDir, ".gitignore", ".env\n")
	r.Git(r.MainDir, "add", ".")
	r.Git(r.MainDir, "commit", "--quiet", "-m", "init")
	r.Git(r.MainDir, "push", "--quiet", "-u", "origin", "main")
	return r
}

// Git runs a git command that must succeed, returning its combined output.
func (r *Repo) Git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Write creates or replaces a file.
func (r *Repo) Write(dir, name, content string) {
	r.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

// DirFor returns the worktree directory a slug maps to.
func (r *Repo) DirFor(slug string) string { return r.MainDir + "-" + slug }

// BranchFor returns the branch a slug maps to.
func (r *Repo) BranchFor(slug string) string { return BranchPrefix + slug }

// Exists reports whether a slug's worktree directory is present.
func (r *Repo) Exists(slug string) bool {
	_, err := os.Stat(r.DirFor(slug))
	return err == nil
}

// Worktree creates a worktree at repo-<slug> on a new branch off origin/main.
func (r *Repo) Worktree(slug string) git.Worktree {
	r.t.Helper()
	dir, branch := r.DirFor(slug), r.BranchFor(slug)
	r.Git(r.MainDir, "worktree", "add", "--quiet", dir, "-b", branch, "origin/main")
	return git.Worktree{Dir: dir, Branch: branch, Slug: slug}
}

// Commit replaces a.txt in a worktree and commits the change.
func (r *Repo) Commit(dir, content string) {
	r.t.Helper()
	r.Write(dir, "a.txt", "seed\n"+content+"\n")
	r.Git(dir, "commit", "--quiet", "-am", "work: "+content)
}

// Push publishes a branch from a worktree.
func (r *Repo) Push(dir, branch string) {
	r.t.Helper()
	r.Git(dir, "push", "--quiet", "origin", branch)
}

// SquashMerge does to a branch what a forge does when it squash-merges a pull
// request: collapse it into a single new commit on main, push that, and delete
// the remote branch. The branch's own commits never reach origin.
func (r *Repo) SquashMerge(branch, message string) {
	r.t.Helper()
	r.Git(r.MainDir, "merge", "--squash", branch)
	r.Git(r.MainDir, "commit", "--quiet", "-m", message)
	r.Git(r.MainDir, "push", "--quiet", "origin", "main")
	r.Git(r.MainDir, "push", "--quiet", "origin", "--delete", branch)
	r.Git(r.MainDir, "fetch", "--quiet", "--prune", "origin")
}

// LooseObjects counts loose objects in the object database, for asserting that
// an operation did or did not write to it.
func (r *Repo) LooseObjects() int {
	r.t.Helper()
	for _, line := range strings.Split(r.Git(r.MainDir, "count-objects", "-v"), "\n") {
		if v, ok := strings.CutPrefix(line, "count:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				r.t.Fatalf("parse count-objects output %q: %v", line, err)
			}
			return n
		}
	}
	r.t.Fatal("count-objects printed no count")
	return 0
}

// Symlink returns the path to the main checkout as reached through a symlink,
// for testing that a symlinked path behaves the same as a resolved one.
//
// The link is created inside Root and points at Root, so "Root/link/repo"
// resolves to "Root/repo". Keeping it inside Root means the temp-directory
// cleanup removes it; RemoveAll unlinks symlinks rather than following them, so
// pointing at the enclosing directory is safe.
func (r *Repo) Symlink() string {
	r.t.Helper()
	link := filepath.Join(r.Root, "link")
	if err := os.Symlink(r.Root, link); err != nil {
		r.t.Fatalf("symlink: %v", err)
	}
	return filepath.Join(link, filepath.Base(r.MainDir))
}
