// Package git wraps the git commands treewright needs.
//
// Everything here shells out to the git binary rather than using a git library:
// treewright's questions ("is this branch squash-merged?") are most precisely
// answered by the same plumbing commands a human would run, and staying close to
// those commands keeps the behavior auditable.
package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Repo is a git repository identified by its main checkout directory.
type Repo struct {
	Dir string
}

// runIn executes git in dir and returns its trimmed stdout.
func runIn(dir string, args ...string) (string, error) {
	return runEnvIn(dir, nil, args...)
}

// runEnvIn is runIn with extra environment variables. The returned error wraps
// the exec error so callers can inspect it with errors.As, while the message
// carries git's stderr — without it a failure reads only as "exit status 128".
func runEnvIn(dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("git %s: %s (%w)", strings.Join(args, " "), msg, err)
		}
		return out, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func (r Repo) run(args ...string) (string, error) { return runIn(r.Dir, args...) }

// runOK reports whether a git command succeeded, discarding its output. For the
// "does this ref exist?" checks, where failure is an answer rather than a fault.
func (r Repo) runOK(args ...string) bool {
	_, err := r.run(args...)
	return err == nil
}

// Name is the main checkout's directory name, e.g. "myrepo" for ~/code/myrepo.
// Managed worktrees are its siblings, named "<Name>-<slug>".
func (r Repo) Name() string { return filepath.Base(r.Dir) }

// ---- discovery -------------------------------------------------------------

// Worktree is one checkout attached to the repository.
type Worktree struct {
	Dir    string // absolute path on disk, as git reports it
	Branch string // branch name without refs/heads/; empty when HEAD is detached
	Slug   string // treewright slug; set only by Managed
}

// Worktrees lists every worktree attached to the repo, main checkout first
// (git guarantees that ordering).
//
// The porcelain format is one blank-line-separated record per worktree:
//
//	worktree /path/to/checkout
//	HEAD 1a2b3c...
//	branch refs/heads/main
//
// A detached HEAD emits "detached" in place of the branch line, which simply
// leaves Branch empty — the state every query here already treats as "no branch
// to reason about". Parsing this once yields both the path and the branch for
// every worktree, which is why nothing runs `git branch --show-current` per
// directory.
func (r Repo) Worktrees() ([]Worktree, error) {
	out, err := r.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var (
		list []Worktree
		cur  *Worktree
	)
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			list = append(list, Worktree{Dir: strings.TrimPrefix(line, "worktree ")})
			cur = &list[len(list)-1]
		case cur == nil:
			continue // field before any "worktree" line: malformed, ignore
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		}
	}
	return list, nil
}

// TopLevel returns the root of the checkout Dir is inside, as git reports it —
// the worktree's own root, where MainDir answers with the repository's main
// checkout from any of its worktrees. It is how `signal` names the checkout the
// calling hook is standing in, and git's fully resolved spelling is what makes
// the answer comparable to a window's worktree stamp.
func (r Repo) TopLevel() (string, error) {
	return r.run("rev-parse", "--show-toplevel")
}

// MainDir returns the repo's main checkout path as git sees it. Callers use this
// to identify which repo they are standing in: git reports the same main path
// from inside any of the repo's worktrees.
func (r Repo) MainDir() (string, error) {
	list, err := r.Worktrees()
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", fmt.Errorf("no worktrees reported for %s", r.Dir)
	}
	return list[0].Dir, nil
}

// Managed lists only the worktrees treewright created: the siblings of the main
// checkout named "<repo>-<slug>". Anything else attached to the repo — the main
// checkout itself, or a worktree made by hand elsewhere — is left alone.
//
// The prefix is built from git's own spelling of the main checkout rather than
// from r.Dir, so a caller that reached this repo through a symlinked path still
// matches: git always reports fully resolved paths, and comparing those against
// an unresolved prefix would match nothing at all.
//
// A slug is allowed to contain a path separator so that worktrees created before
// `new` rejected such slugs remain visible and removable.
func (r Repo) Managed() ([]Worktree, error) {
	list, err := r.Worktrees()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	prefix := list[0].Dir + "-"
	managed := make([]Worktree, 0, len(list))
	for _, wt := range list {
		if !strings.HasPrefix(wt.Dir, prefix) {
			continue
		}
		wt.Slug = strings.TrimPrefix(wt.Dir, prefix)
		managed = append(managed, wt)
	}
	// Sorted by slug so that listings, the resume menu's numbering, and
	// completion candidates are stable and diffable. git's own order reflects
	// creation order, which changes as worktrees come and go.
	sort.Slice(managed, func(i, j int) bool { return managed[i].Slug < managed[j].Slug })
	return managed, nil
}

// CurrentBranch reports the branch checked out in dir ("" when detached).
func CurrentBranch(dir string) (string, error) {
	return runIn(dir, "branch", "--show-current")
}

// ---- introspection, for setup and doctor -----------------------------------

// DefaultBranch reports the branch a clone of this repo would check out, by
// reading the symbolic ref origin/HEAD that git records at clone time.
//
// Falls back to whatever the main checkout currently has out, then to "main".
// The point is to guess a base branch well enough that a generated config is
// usually right, not to be authoritative: the value lands in a file the user can
// edit, and every command reports which branch it forked from.
//
// origin/HEAD is only trusted when the branch it names still resolves. It is a
// symbolic ref recorded once, at clone time, from whatever the remote's own HEAD
// then said — so it can outlive a renamed default branch, or name a branch that
// was never pushed at all. Returning such a name would put a base_branch in a
// generated config that nothing can fork from.
func (r Repo) DefaultBranch() string {
	if out, err := r.run("symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if branch := strings.TrimPrefix(out, "refs/remotes/origin/"); branch != out && branch != "" {
			if r.RefExists("refs/remotes/origin/" + branch) {
				return branch
			}
		}
	}
	if branch, err := CurrentBranch(r.Dir); err == nil && branch != "" {
		return branch
	}
	return "main"
}

// RemoteBranchNamespaces counts the leading namespace of every branch on a
// remote: "feature/eng-1" and "feature/eng-2" make "feature/" worth 2. Branches
// with no namespace contribute nothing.
//
// Read from refs/remotes/<remote>, which a clone fills in for every branch the
// remote has, so this describes what the team does rather than what this checkout
// happens to have fetched by hand.
//
// Only "/" delimits a namespace. A dashed convention ("feature-eng-1") is
// indistinguishable from an ordinary ticket key ("eng-142-white-screen") without
// already knowing the team's scheme, and reading "eng-" as a namespace would be
// wrong far more often than right.
func (r Repo) RemoteBranchNamespaces(remote string) map[string]int {
	// strip=3 drops "refs/remotes/<remote>/", leaving the branch name as the remote
	// spells it — including the slashes, which are the whole point here.
	out, err := r.run("for-each-ref", "--format=%(refname:strip=3)", "refs/remotes/"+remote)
	if err != nil || out == "" {
		return nil
	}
	counts := make(map[string]int)
	for name := range strings.SplitSeq(out, "\n") {
		// "HEAD" is the remote's symbolic ref rather than a branch, and lands here
		// with no namespace, so it drops out with the unnamespaced branches.
		if ns, _, found := strings.Cut(name, "/"); found && ns != "" {
			counts[ns+"/"]++
		}
	}
	return counts
}

// UserEmail reports the git identity in force for this repo, respecting any
// repo-local override of the global setting. Empty when none is configured.
func (r Repo) UserEmail() string {
	out, err := r.run("config", "user.email")
	if err != nil {
		return ""
	}
	return out
}

// HasRemote reports whether a remote of this name is configured.
func (r Repo) HasRemote(name string) bool {
	return r.runOK("remote", "get-url", name)
}

// IgnoredFiles lists paths git is ignoring, relative to the main checkout.
//
// --directory collapses a wholly ignored directory into a single entry ending in
// "/", which is what keeps node_modules from contributing thousands of paths
// while still listing an ignored file that sits in an otherwise tracked
// directory, such as apps/api/.env.
func (r Repo) IgnoredFiles() []string {
	out, err := r.run("ls-files", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// Ignored reports whether the repository's ignore rules cover a path, given
// relative to the main checkout. A directory answers for itself, so
// ".claude/skills/treewright" is a question this can be asked.
//
// --no-index asks the rules alone. Without it git answers "no" for anything in
// the index however the rules read, which is the right answer to check-ignore's
// usual question — why is this file not being ignored — and the wrong one here,
// where a committed path and an unmentioned one need telling apart. Tracked
// asks that half separately.
func (r Repo) Ignored(rel string) bool {
	return r.runOK("check-ignore", "--quiet", "--no-index", "--", rel)
}

// Tracked reports whether git has anything under a path in the index. A
// directory is a legitimate argument for the same reason it is to Ignored: the
// question is about a tree of files, and one tracked file in it means the tree
// is in the repository's history.
func (r Repo) Tracked(rel string) bool {
	out, err := r.run("ls-files", "--", rel)
	return err == nil && out != ""
}

// ---- state queries ---------------------------------------------------------

// DirtyFiles counts uncommitted changes in a worktree, staged or not, including
// untracked files. Returns 0 when the directory is clean or missing.
func DirtyFiles(dir string) int {
	out, err := runIn(dir, "status", "--porcelain")
	if err != nil || out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

// BranchExists reports whether a local branch of this name exists.
func (r Repo) BranchExists(branch string) bool {
	if branch == "" {
		return false
	}
	return r.runOK("show-ref", "--verify", "--quiet", "refs/heads/"+branch)
}

// RefExists reports whether a ref resolves, e.g. "origin/main".
func (r Repo) RefExists(ref string) bool {
	return r.runOK("rev-parse", "--verify", "--quiet", ref)
}

// Unpushed counts commits on branch that are reachable from no origin ref — work
// that exists nowhere but this local branch. 0 means every commit is either
// pushed or already merged somewhere upstream.
func (r Repo) Unpushed(branch string) int {
	if !r.BranchExists(branch) {
		return 0
	}
	out, err := r.run("rev-list", "--count", branch, "--not", "--remotes=origin")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0
	}
	return n
}

// squashCheckEnv fixes every input to the synthetic commit below except its tree
// and parent, so its hash is fully determined by what is being tested. Repeated
// runs therefore write the identical object rather than a new one each time, and
// the result does not depend on the user's configured git identity.
var squashCheckEnv = []string{
	"GIT_AUTHOR_NAME=treewright",
	"GIT_AUTHOR_EMAIL=treewright@invalid",
	"GIT_AUTHOR_DATE=@0 +0000",
	"GIT_COMMITTER_NAME=treewright",
	"GIT_COMMITTER_EMAIL=treewright@invalid",
	"GIT_COMMITTER_DATE=@0 +0000",
}

// IsMerged reports whether branch's work has landed in origin/<base>, by either
// route a pull request can take:
//
//   - Normal or rebase merge: every commit on the branch is reachable from
//     origin/<base>, so the branch has no commits outside it.
//   - Squash merge: the branch's individual commits never land upstream at all;
//     they are collapsed into one new commit. To recognize that, synthesize a
//     single commit holding the branch's entire tree on top of its merge-base —
//     exactly the patch a squash merge produces — and ask `git cherry` whether
//     an equivalent patch is already upstream. A "-" prefix means yes.
//
// Stricter than "has no unpushed commits": a branch that is pushed but whose
// pull request is still open is not merged.
//
// The squash path writes one dangling commit object to the object database.
// Nothing ever points a ref at it and git's normal gc reaps it, but this is the
// reason treewright needs write access to .git even for read-only-looking commands.
func (r Repo) IsMerged(branch, base string) bool {
	if branch == "" {
		return false
	}
	upstream := "origin/" + base
	if !r.RefExists(upstream) {
		return false
	}

	if out, err := r.run("rev-list", "--count", branch, "--not", upstream); err == nil && out == "0" {
		return true
	}

	mergeBase, err := r.run("merge-base", upstream, branch)
	if err != nil || mergeBase == "" {
		return false
	}
	tree, err := r.run("rev-parse", branch+"^{tree}")
	if err != nil {
		return false
	}
	synth, err := runEnvIn(r.Dir, squashCheckEnv, "commit-tree", tree, "-p", mergeBase, "-m", "squash-check")
	if err != nil {
		return false
	}
	out, err := r.run("cherry", upstream, synth)
	if err != nil {
		return false
	}
	return strings.HasPrefix(out, "-")
}

// AheadBehind counts commits branch is ahead of and behind origin/<base>. ok is
// false when the comparison cannot be made, which callers render as "?" rather
// than as 0/0 — an unknown is not a zero.
func (r Repo) AheadBehind(branch, base string) (ahead, behind int, ok bool) {
	if branch == "" {
		return 0, 0, false
	}
	// For A...B, --left-right --count prints "<left>\t<right>", left being the
	// count reachable from A alone. With A=origin/<base> that is how far the
	// branch is behind, and right is how far it is ahead.
	out, err := r.run("rev-list", "--left-right", "--count", "origin/"+base+"..."+branch)
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, false
	}
	behind, err1 := strconv.Atoi(fields[0])
	ahead, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return ahead, behind, true
}

// Status summarizes how safe a worktree is to throw away.
type Status string

// The statuses `ls` reports, in the precedence it applies them: dirty outranks
// everything because it is the most easily lost, then merged, then unpushed, and a
// branch that is pushed but not merged is active.
const (
	StatusDirty    Status = "dirty"    // uncommitted changes present
	StatusMerged   Status = "merged"   // landed in origin/<base>; safe to remove
	StatusUnpushed Status = "unpushed" // commits exist only here
	StatusActive   Status = "active"   // pushed, not yet merged (open pull request)

	// StatusBase is the main checkout, which is not a worktree and is never
	// thrown away. It gets a status of its own because the others answer "how
	// safe is this to remove", and every one of their answers is wrong here: a
	// base checkout sitting level with origin has no commits outside it, which
	// reads as merged — the green that means "safe to delete" — about the one
	// directory in the repository that must never go.
	StatusBase Status = "base"
)

// Info is everything the ls table and the removal guards need about one
// worktree, gathered in a single pass.
type Info struct {
	Worktree

	Status     Status
	DirtyFiles int
	Unpushed   int
	Ahead      int
	Behind     int
	Compared   bool // false when ahead/behind could not be computed
}

// Inspect gathers a worktree's state relative to origin/<base>.
//
// Uncommitted work outranks everything because it is the most easily lost, then
// merged, then unpushed; a pushed-but-unmerged branch is "active".
func (r Repo) Inspect(wt Worktree, base string) Info {
	info := Info{Worktree: wt}
	info.DirtyFiles = DirtyFiles(wt.Dir)
	info.Unpushed = r.Unpushed(wt.Branch)
	info.Ahead, info.Behind, info.Compared = r.AheadBehind(wt.Branch, base)

	switch {
	case info.DirtyFiles > 0:
		info.Status = StatusDirty
	case r.IsMerged(wt.Branch, base):
		info.Status = StatusMerged
	case info.Unpushed > 0:
		info.Status = StatusUnpushed
	default:
		info.Status = StatusActive
	}
	return info
}

// BaseCheckout describes the main checkout as a row alongside the worktrees.
//
// Two of the three columns mean the same thing here as anywhere: uncommitted
// files are work left lying in the window you investigate from, and the
// divergence is measured against origin/<base> exactly as it is for a worktree —
// which for the checkout parked on that branch is the "you need to pull"
// indicator, and the one number that says whether what you are reading is stale.
//
// Only the status is different, and StatusBase says why.
//
// The branch is read fresh rather than remembered, because the base checkout is
// the one place the user switches branches by hand, from inside the window.
func (r Repo) BaseCheckout(base string) Info {
	branch, err := CurrentBranch(r.Dir)
	if err != nil {
		branch = "" // detached, or unreadable: the same "no branch" either way
	}
	info := Info{Worktree: Worktree{Dir: r.Dir, Branch: branch}, Status: StatusBase}
	info.DirtyFiles = DirtyFiles(r.Dir)
	info.Unpushed = r.Unpushed(branch)
	info.Ahead, info.Behind, info.Compared = r.AheadBehind(branch, base)
	return info
}

// ---- mutations -------------------------------------------------------------

// Fetch updates one ref from a remote. An error usually means the network is
// unavailable, which callers treat as "work offline" rather than as fatal.
func (r Repo) Fetch(remote, ref string) error {
	_, err := r.run("fetch", "--quiet", remote, ref)
	return err
}

// FetchPrune drops remote-tracking refs whose upstream branch is gone.
func (r Repo) FetchPrune(remote string) error {
	_, err := r.run("fetch", "--prune", "--quiet", remote)
	return err
}

// AddWorktree checks an existing branch out into a new worktree directory.
func (r Repo) AddWorktree(dir, branch string) error {
	_, err := r.run("worktree", "add", dir, branch)
	return err
}

// AddWorktreeNewBranch creates branch at startPoint and checks it out into dir.
func (r Repo) AddWorktreeNewBranch(dir, branch, startPoint string) error {
	_, err := r.run("worktree", "add", dir, "-b", branch, startPoint)
	return err
}

// RemoveWorktree deletes a worktree directory and detaches it from the repo.
//
// --force is needed for the ordinary case, not to override treewright's own safety
// checks: every worktree carries untracked build output that git refuses to
// delete without it. Whether the work is safe to lose is decided before this is
// ever called.
func (r Repo) RemoveWorktree(dir string) error {
	_, err := r.run("worktree", "remove", "--force", dir)
	return err
}

// DeleteBranch removes a local branch.
//
// -D rather than -d because a squash-merged branch does not look merged to git:
// its commits are not reachable from the base branch, so -d refuses. Callers
// have already established that the work landed.
func (r Repo) DeleteBranch(branch string) error {
	_, err := r.run("branch", "-D", branch)
	return err
}

// ---- moving uncommitted work between checkouts ------------------------------

// The operations `move` is built out of. They take a directory rather than
// hanging off Repo because the whole point is that two checkouts are involved:
// the work leaves one and arrives in the other, and neither is "the repository".
//
// Everything here is a plain git command with nothing clever in it, which is
// deliberate — this is the one path in treewright that can destroy uncommitted
// work, and what makes it auditable is that each step is a command a person
// could have run and can check afterwards.

// UntrackedFiles lists the files git neither tracks nor ignores in a checkout,
// relative to its root.
//
// --exclude-standard is what keeps the ignored files out, and they are kept out
// on purpose: a .env is carried into a new worktree by carry_files, and a move
// that swept it up would take it out of the checkout every other worktree is
// carried from.
//
// -z because these paths are handed straight back to git and to os.Remove, and
// a filename with a newline in it would otherwise arrive as two.
func UntrackedFiles(dir string) ([]string, error) {
	out, err := runIn(dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	return nulSeparated(out), nil
}

// IntentToAdd records paths in the index as files meant to be added, which is
// what puts an untracked file into `git diff HEAD` — without it a patch of the
// working tree carries changes to tracked files and nothing else.
//
// An empty list is a no-op rather than a bare `git add -N`, which would mean
// every untracked file in the checkout.
func IntentToAdd(dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := runIn(dir, append([]string{"add", "-N", "--"}, paths...)...)
	return err
}

// Unstage puts the index entries for paths back to what HEAD says, which for a
// path HEAD does not have means removing the entry — undoing IntentToAdd exactly.
//
// Named paths rather than a bare `git reset`, and that is the whole reason this
// exists as its own function. A pathless reset would also unstage whatever the
// user had staged themselves, which is theirs and none of a move's business; by
// path, only the entries treewright wrote are touched.
//
// An empty list is a no-op for the same reason IntentToAdd's is.
func Unstage(dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := runIn(dir, append([]string{"reset", "--quiet", "--"}, paths...)...)
	return err
}

// AddedPaths lists the paths a diff against HEAD would create — the untracked
// files just marked intent-to-add, and any the user had staged as new.
//
// It is what a move deletes from the checkout it took the work out of, and it
// is read from the diff rather than from the untracked listing so that a file
// the user had already `git add`ed is not left behind as a second copy.
func AddedPaths(dir string) ([]string, error) {
	out, err := runIn(dir, "diff", "HEAD", "--name-only", "--diff-filter=A", "-z")
	if err != nil {
		return nil, err
	}
	return nulSeparated(out), nil
}

// nulSeparated splits git's -z output, dropping the empty field its trailing
// separator leaves behind.
func nulSeparated(out string) []string {
	var paths []string
	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// WritePatch writes a checkout's whole diff against HEAD to path — staged and
// unstaged alike, which is what "everything that is not committed" means.
//
// It streams into the file rather than going through runIn, for two reasons
// that both matter here: a working tree's diff can be large, and runIn trims
// its output, which would take the final newline off a patch that has to end
// with one.
//
// --binary so that a changed image or a compiled fixture crosses too. Without
// it git writes "Binary files differ" and the patch is one git apply refuses.
func WritePatch(dir, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "diff", "HEAD", "--binary")
	cmd.Dir = dir
	cmd.Stdout = f
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = f.Close()
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("git diff HEAD: %s (%w)", msg, err)
		}
		return fmt.Errorf("git diff HEAD: %w", err)
	}
	return f.Close()
}

// ApplyPatch applies a patch in a checkout.
//
// --3way is what makes it land rather than merely apply: it falls back to a
// three-way merge using the blobs the patch names, so a patch written against
// one commit still applies over another. It goes through the index, so the work
// arrives staged — which is why a caller checking that it worked has to ask
// `git diff HEAD` rather than `git diff`.
func ApplyPatch(dir, path string) error {
	_, err := runIn(dir, "apply", "--3way", path)
	return err
}

// DiffStat summarizes what a checkout holds that HEAD does not, staged
// included. Empty means nothing does.
func DiffStat(dir string) (string, error) {
	return runIn(dir, "diff", "HEAD", "--stat")
}

// RestoreTracked puts every tracked file in a checkout back to HEAD, in the
// index and in the working tree both.
//
// From HEAD rather than from the index, which is the difference between
// clearing a checkout and merely discarding the changes nobody had staged: work
// that was staged is in the patch that has already landed elsewhere, so leaving
// it here would be leaving a second copy.
func RestoreTracked(dir string) error {
	_, err := runIn(dir, "checkout", "HEAD", "--", ".")
	return err
}
