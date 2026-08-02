package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
)

// Moving uncommitted work out of the base checkout and into a worktree of its
// own.
//
// The situation is ordinary: someone started typing in the main checkout and
// then realized the change wants a branch. What makes it dangerous is that the
// work is uncommitted, so until it is somewhere else the base checkout is the
// only copy of it — and the sequence that moves it has a verification gate in
// the middle whose whole purpose is to be passed before anything is thrown away.
//
// That sequence was six commands in the claude skill's guide, in a strict order,
// with the order carrying the safety. An agent improvising anywhere in it loses
// work, and the improvisation to expect is `git clean -fd` for the last step:
// it looks like "delete the untracked files" and it is "delete every untracked
// file", including the ones this move never touched and any ignored file that
// happens to sit inside an untracked directory. So the files to delete are
// recorded before anything else happens, and deleted by name.
//
// `git stash` is not the shortcut it looks like, and this is the note for
// whoever proposes it next: one stash stack is shared by every worktree of a
// repository. A `pop` in the wrong checkout is a keystroke away, and the work is
// then in neither place anybody expected — a failure mode with no error message
// in it. The patch file is worse to type and cannot be popped anywhere by
// accident.

func cmdMove(env *Env, args []string) error {
	var keep bool
	var prompt, promptFile string
	positional, err := parseArgs("move", args,
		map[string]*bool{"--keep": &keep}, promptValues(&prompt, &promptFile), 2)
	if err != nil {
		return err
	}
	slug, override := at(positional, 0), at(positional, 1)
	if slug == "" {
		return usageErrorf("move", "a slug is required")
	}

	cfg, err := resolveConfig("")
	if err != nil {
		return err
	}
	prefix, slug := splitPrefix(env, cfg, slug)
	if err := validateSlug(cfg, slug); err != nil {
		return err
	}
	prompt, err = resolvePrompt("move", prompt, promptFile)
	if err != nil {
		return err
	}
	command, err := fillPrompt(cfg.Command, "command", prompt)
	if err != nil {
		return err
	}

	base := cfg.MainDir
	if git.DirtyFiles(base) == 0 {
		return fmt.Errorf("nothing to move — %s has no uncommitted work in it%s",
			base, asFields(field("start a worktree instead with", env.copyable(env.Argv0+" new "+slug))))
	}

	patch, added, err := takePatch(env, cfg, slug)
	if err != nil {
		return err
	}

	dir, branch, err := createWorktree(env, cfg, prefix, slug)
	if err != nil {
		return leftInPlace(err, patch)
	}
	if err := git.ApplyPatch(dir, patch); err != nil {
		return leftInPlace(fmt.Errorf("the work did not apply in %s: %w", slug, err), patch,
			field("the worktree it stopped in", dir))
	}
	// Verified before the base checkout is touched, and verified with
	// `diff HEAD` rather than `diff`: --3way applies through the index, so the
	// work arrives staged and a plain `diff` has nothing to show — which reads
	// exactly like a patch that never applied.
	stat, err := git.DiffStat(dir)
	if err != nil || strings.TrimSpace(stat) == "" {
		return leftInPlace(fmt.Errorf("the patch applied but %s shows no changes against HEAD", slug), patch,
			field("the worktree it stopped in", dir))
	}
	env.progressf("the work is in %s now%s", slug, asFields(
		field("changes", summaryOf(stat)),
		field("state", "staged, as a three-way apply leaves it"),
	))

	kept := keep
	switch {
	case keep:
		env.progressf("--keep, so %s still holds its copy\nnothing was taken out of it", base)
	default:
		if err := clearBase(env, base, added); err != nil {
			env.warnf("the work reached %s, but %s still holds a copy: %v\nclear it by hand once you have looked at the worktree", slug, base, err)
			kept = true
		}
	}
	// The patch is what a failure leaves behind to recover from, so it goes only
	// when there is nothing left to recover: the work has landed and the
	// checkout it came out of has been dealt with.
	if !kept {
		_ = os.Remove(patch)
	}

	// The worktree's path, as `new`'s is, so `cd "$(treewright move eng-1)"`
	// works — printed here rather than the moment the worktree existed, because
	// what this command answers is where the work went, and until the patch had
	// landed there was no honest answer.
	fmt.Fprintln(env.Stdout, dir)

	// Last, so the agent's first sight of the worktree is the work already in
	// it. A window opened before the patch landed would put an agent in an empty
	// checkout being asked to carry on with something not there yet.
	openWorktreeWindow(env, cfg, worktreeWindow{
		Slug: slug, Branch: branch, Dir: dir,
		Name: cfg.WindowName(slug, override), Command: command, Prompt: prompt,
	})
	return nil
}

// takePatch writes what the base checkout holds against HEAD to a file, and
// reports the paths that patch would create.
//
// The order inside it is the safety. The untracked files are listed first,
// because that list is what makes the eventual cleanup exact and because
// everything after it changes what the listing would say. They are then marked
// intent-to-add so they reach the diff at all, the patch is written, and the
// index is put straight back — by path, so a file the user had staged
// themselves is untouched.
//
// Putting it back here rather than at the end is what makes every failure below
// this point honest: from the moment this returns, the base checkout is byte for
// byte what it was, and it stays that way until the work has demonstrably landed
// somewhere else.
func takePatch(env *Env, cfg *config.Config, slug string) (patch string, added []string, err error) {
	base := cfg.MainDir
	untracked, err := git.UntrackedFiles(base)
	if err != nil {
		return "", nil, err
	}
	if err := git.IntentToAdd(base, untracked); err != nil {
		return "", nil, err
	}
	// Read from the diff rather than from the untracked listing, so a file the
	// user had already staged as new is carried and cleaned up like any other.
	added, err = git.AddedPaths(base)
	if err != nil {
		_ = git.Unstage(base, untracked)
		return "", nil, err
	}

	patch = movePatchPath(cfg, slug)
	if err := os.MkdirAll(filepath.Dir(patch), 0o755); err != nil {
		_ = git.Unstage(base, untracked)
		return "", nil, err
	}
	writeErr := git.WritePatch(base, patch)
	if err := git.Unstage(base, untracked); err != nil {
		// Worth saying rather than failing on: what is left is intent-to-add
		// entries in an index whose files are all still there, which `git reset`
		// undoes — and the patch, which is the thing being protected, is written.
		env.warnf("could not put %s's index back: %v\nrun %s there", base, err, env.copyable("git reset"))
	}
	if writeErr != nil {
		_ = os.Remove(patch)
		return "", nil, writeErr
	}

	info, err := os.Stat(patch)
	if err != nil || info.Size() == 0 {
		_ = os.Remove(patch)
		return "", nil, fmt.Errorf("nothing to move — %s holds no change git can carry", base)
	}
	return patch, added, nil
}

// clearBase takes the moved work out of the checkout it came from: the files the
// patch created, then everything it changed.
//
// The added files go by name, from the list recorded before any of this started.
// `git clean -fd` is what an improvising hand reaches for here and it is the one
// thing this must not do — it deletes every untracked file rather than these,
// and an untracked directory it removes takes the ignored files inside it too.
//
// Empty directories are left where a deleted file was the last thing in them.
// Removing them means removing directories, which is exactly the reach that
// makes `clean -fd` dangerous, and an empty directory costs a reader nothing.
func clearBase(env *Env, base string, added []string) error {
	// The index first, since a path the user had staged as new is in it and
	// would otherwise survive as an entry pointing at a file about to go.
	if err := git.Unstage(base, added); err != nil {
		return err
	}
	for _, rel := range added {
		if err := os.Remove(filepath.Join(base, rel)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := git.RestoreTracked(base); err != nil {
		return err
	}
	env.progressf("%s is back to HEAD%s", base, under(clearedNote(added)))
	return nil
}

// clearedNote says how many files were deleted rather than restored, when there
// were any. They are the half of the cleanup git could not have done on its own,
// so they are the half worth naming.
func clearedNote(added []string) string {
	if len(added) == 0 {
		return ""
	}
	return count(len(added), "file the move created is gone", "files the move created are gone")
}

// movePatchPath names the patch, beside post_create's log inside the .git
// directory treewright already writes to — so this adds no new place to look for
// what treewright left behind.
//
// Inside the worktree would put it in the diff somebody reviews, and /tmp is
// swept by the machine on its own schedule, which is the wrong property for the
// only copy of somebody's uncommitted work.
func movePatchPath(cfg *config.Config, slug string) string {
	return filepath.Join(cfg.MainDir, ".git", "treewright",
		"move-"+strings.ReplaceAll(slug, "/", "-")+".patch")
}

// leftInPlace reports a failure that stopped before the base checkout was
// touched, and names the patch.
//
// Both halves are the message: what did not happen is as much the news as what
// did, since the reader's first question about a failed move is whether their
// work is still where they left it. The patch stays on disk deliberately — it
// is a second copy of that work and the way back into a worktree by hand.
func leftInPlace(err error, patch string, extra ...[2]string) error {
	return fmt.Errorf("%w\nnothing was taken out of the base checkout%s", err,
		asFields(append([][2]string{field("the work is also in", patch)}, extra...)...))
}

// summaryOf is the last line of a diffstat — "2 files changed, 30 insertions(+)"
// — which is the line a reader wants when the question is whether the same work
// arrived rather than which parts of it did.
func summaryOf(stat string) string {
	lines := strings.Split(strings.TrimSpace(stat), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
