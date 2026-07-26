package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jay-snyder/treemux/internal/config"
	"github.com/jay-snyder/treemux/internal/git"
	"github.com/jay-snyder/treemux/internal/tmux"
	"github.com/jay-snyder/treemux/internal/ui"
)

// ---- new -------------------------------------------------------------------

func cmdNew(env *Env, args []string) error {
	positional, err := parseArgs("new", args, nil, nil, 2)
	if err != nil {
		return err
	}
	slug, override := at(positional, 0), at(positional, 1)
	if slug == "" {
		return usageErrorf("new", "a slug is required")
	}

	cfg, err := resolveConfig("")
	if err != nil {
		return err
	}
	slug = stripPrefix(env, cfg, slug)
	if err := validateSlug(slug); err != nil {
		return err
	}

	repo := repoFor(cfg)
	dir := cfg.DirFor(slug)
	branch := cfg.BranchFor(slug)

	// Checked before anything is reported, because git's own refusal arrives
	// several steps in — after "reusing existing branch" has already been printed
	// — and says "already exists" about a path rather than naming the command
	// that opens what is already there.
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("worktree for %s already exists at %s — open it with \"treemux resume %s\"",
			slug, dir, slug)
	}

	switch {
	case repo.BranchExists(branch):
		env.progressf("reusing existing branch %s", branch)
		if err := repo.AddWorktree(dir, branch); err != nil {
			return err
		}
	default:
		// Refresh the base branch so the fork point is the latest commit, not
		// whatever this checkout last happened to fetch.
		if err := repo.Fetch("origin", cfg.BaseBranch); err == nil {
			env.progressf("creating branch %s off origin/%s", branch, cfg.BaseBranch)
			if err := repo.AddWorktreeNewBranch(dir, branch, "origin/"+cfg.BaseBranch); err != nil {
				return err
			}
		} else if repo.BranchExists(cfg.BaseBranch) {
			// Offline: a local base branch is a stale but usable fork point.
			env.warnf("origin unreachable — forking from local %s, which may be behind", cfg.BaseBranch)
			if err := repo.AddWorktreeNewBranch(dir, branch, cfg.BaseBranch); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("no %s ref available; refusing to guess a base", cfg.BaseBranch)
		}
	}

	carryFiles(env, cfg, dir)
	if err := startPostCreate(env, cfg, dir, slug); err != nil {
		env.warnf("post_create did not start: %v", err)
	}

	// The worktree path is this command's answer, so it goes to stdout and
	// nowhere else: `cd "$(treemux new foo)"` is meant to work.
	fmt.Fprintln(env.Stdout, dir)

	// Reported rather than returned: the worktree exists, the branch exists, and
	// the path is already on stdout, so `cd "$(treemux new eng-1)"` must not fail
	// because tmux could not be made to open a window. resume and base do return
	// it, a window being the whole of what they were asked for.
	if err := openWindow(env, cfg, tmux.Spec{
		Dir:     dir,
		Name:    cfg.WindowName(slug, override),
		Command: cfg.Command,
		Slug:    slug,
		Branch:  branch,
	}); err != nil {
		env.warnf("%v", err)
	}
	return nil
}

// validateSlug rejects slugs that would not round-trip through a directory name
// or a branch name.
//
// A slug containing "/" turns "<repo>-a/b" into a nested path: `new` silently
// creates the intermediate "<repo>-a" directory and `rm` later removes only the
// leaf, leaving it behind empty.
//
// The rest of the rules are git's, from check-ref-format. They are restated here
// rather than delegated to git so that the answer is one sentence naming the
// slug, instead of git's several lines about ref syntax arriving from three steps
// deeper — by which point treemux has already announced what it was about to do.
func validateSlug(slug string) error {
	if slug == "" {
		return usageErrorf("new", "the slug is empty once the branch prefix is removed")
	}
	if strings.Contains(slug, "/") {
		return usageErrorf("new", "slug %q cannot contain %q — it would nest the worktree inside a stray directory", slug, "/")
	}
	if strings.ContainsAny(slug, " \t\n") {
		return usageErrorf("new", "slug %q cannot contain whitespace — it becomes both a directory and a branch name", slug)
	}
	// git rejects these outright in a ref name; ~^: and ? * [ are its wildcard
	// and revision syntax, and control characters are simply forbidden.
	for _, r := range slug {
		if strings.ContainsRune("~^:?*[\\", r) || r < 0x20 || r == 0x7f {
			return usageErrorf("new", "slug %q cannot contain %q — git does not allow it in a branch name", slug, r)
		}
	}
	// The positional rules: a leading dash reads as a flag, and the others are
	// spellings git reserves.
	switch {
	case strings.HasPrefix(slug, "-"):
		return usageErrorf("new", "slug %q cannot start with %q — it would read as a flag", slug, "-")
	case strings.HasPrefix(slug, "."), strings.HasSuffix(slug, "."):
		return usageErrorf("new", "slug %q cannot start or end with %q — git does not allow it in a branch name", slug, ".")
	case strings.Contains(slug, ".."), strings.Contains(slug, "@{"), strings.HasSuffix(slug, ".lock"):
		return usageErrorf("new", "slug %q is not usable as a branch name — git reserves %q, %q and a %q suffix", slug, "..", "@{", ".lock")
	}
	return nil
}

// carryFiles copies the gitignored files a fresh checkout lacks but the app
// needs — .env files, local credentials, editor settings.
func carryFiles(env *Env, cfg *config.Config, dir string) {
	for _, rel := range cfg.CarryFiles {
		src := filepath.Join(cfg.MainDir, rel)
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			// A carry_files entry that does not exist is almost always a stale
			// config, and the worktree will fail confusingly later.
			env.warnf("carry_files: %s not found in %s", rel, cfg.MainDir)
			continue
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			env.warnf("carry_files: %v", err)
			continue
		}
		if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
			env.warnf("carry_files: %s: %v", rel, err)
		}
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// startPostCreate launches the configured setup command without waiting, so the
// window opens immediately.
//
// Its output goes to a log file under .git rather than to the terminal or to
// /dev/null: inside the worktree it would show up as an untracked file and make
// the tree read as dirty, and discarded entirely there would be no way to find
// out why an install failed.
func startPostCreate(env *Env, cfg *config.Config, dir, slug string) error {
	if strings.TrimSpace(cfg.PostCreate) == "" {
		return nil
	}
	logDir := filepath.Join(cfg.MainDir, ".git", "treemux")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, "post-create-"+strings.ReplaceAll(slug, "/", "-")+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	// The child gets its own duplicated descriptor, so closing this one does not
	// cut off its output after treemux exits.
	defer logFile.Close()

	cmd := exec.Command("sh", "-c", cfg.PostCreate)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	// Deliberately not waited on: the process outlives treemux.
	env.progressf("running post_create in the background — log: %s", logPath)
	return nil
}

// ---- rm --------------------------------------------------------------------

func cmdRm(env *Env, args []string) error {
	var force, yes bool
	positional, err := parseArgs("rm", args, map[string]*bool{
		"-f": &force, "--force": &force,
		"-y": &yes, "--yes": &yes,
	}, nil, 1)
	if err != nil {
		return err
	}
	slug := at(positional, 0)
	if slug == "" {
		return usageErrorf("rm", "a slug is required")
	}

	cfg, err := resolveConfig("")
	if err != nil {
		return err
	}
	slug = stripPrefix(env, cfg, slug)

	repo := repoFor(cfg)
	managed, err := repo.Managed()
	if err != nil {
		return err
	}
	// Resolved against the worktrees that exist, rather than deriving a path from
	// the slug and letting git object: a typo would otherwise surface as "is not a
	// working tree" about a path the user never typed.
	target, err := resolveSlug(env, repo, managed, slug)
	if err != nil {
		return err
	}
	slug, dir, branch := target.Slug, target.Dir, target.Branch

	if !force {
		// Refresh origin/<base> before judging. A just-squash-merged branch has
		// no commits on origin at all, so deciding against a stale ref would
		// report the landed work as unsaved and refuse.
		_ = repo.Fetch("origin", cfg.BaseBranch)

		var reasons []string
		if n := git.DirtyFiles(dir); n > 0 {
			reasons = append(reasons, fmt.Sprintf("%d uncommitted file(s)", n))
		}
		// Unpushed commits are only lost work if they are not already merged: a
		// squash merge leaves them unreachable from origin even though their
		// content landed.
		if n := repo.Unpushed(branch); n > 0 && !repo.IsMerged(branch, cfg.BaseBranch) {
			reasons = append(reasons, fmt.Sprintf("%d commit(s) not on origin", n))
		}
		if len(reasons) > 0 {
			fmt.Fprintf(env.Stderr, "error: refusing to remove %q — %s would be lost\n",
				slug, strings.Join(reasons, " and "))
			fmt.Fprintf(env.Stderr, "       re-run with --force to remove it anyway\n")
			return ErrSilent
		}
	}

	// Note the caller's location before deleting anything, so we can tell whether
	// their shell is about to be standing in a directory that is gone.
	wd, wdErr := os.Getwd()
	// And which window is open on the worktree, asked while the worktree still
	// exists. This is usually not the caller's own window: `treemux rm eng-1` is
	// run from the base window, and the one left pointing at a deleted directory
	// is the window named after the worktree.
	staleWindow := tmux.Windows(sessionFor(cfg))[dir]

	if err := repo.RemoveWorktree(dir); err != nil {
		return err
	}
	// Best-effort from here: the worktree is already gone, so a failure to delete
	// the branch or prune refs is worth reporting but not fatal.
	//
	// A worktree on a detached HEAD has no branch to delete, which is not an error;
	// git reports one only for worktrees created outside treemux.
	if branch != "" {
		if err := repo.DeleteBranch(branch); err != nil {
			env.warnf("could not delete branch %s: %v", branch, err)
		}
	}
	_ = repo.FetchPrune("origin")
	if branch == "" {
		env.progressf("removed worktree %s, which was on no branch", dir)
	} else {
		env.progressf("removed worktree %s and branch %s", dir, branch)
	}
	fmt.Fprintln(env.Stdout, dir)

	// Two separate leftovers, and the caller usually has only one of them: a shell
	// standing in the deleted directory, and a window open on it.
	if wdErr == nil && insideDir(wd, dir) {
		escapeDeletedDir(env, cfg.MainDir, dir)
	}
	offerWindowClose(env, staleWindow, yes)
	return nil
}

// escapeDeletedDir asks the calling shell to leave a directory that no longer
// exists. Without the shell integration treemux can only say so.
func escapeDeletedDir(env *Env, mainDir, goneDir string) {
	emitEval(env, "cd "+shellQuote(mainDir))
	if env.EvalFile == "" {
		env.progressf("your shell is still in the deleted %s — run: cd %s", goneDir, mainDir)
	}
}

// offerWindowClose offers to close a tmux window whose worktree has been removed,
// since it now points at a directory that does not exist. assumeYes closes
// without asking, and a zero window means there was none open.
//
// The window is identified from the worktree's path rather than from the caller's
// own pane, because a teardown is normally run from somewhere else: closing "the
// window I am in" left the window named after the worktree behind, still sitting in
// the deleted directory.
//
// No client is needed to close a window, so this runs outside tmux too: a worktree
// torn down from a plain shell should not leave a window behind in the session
// waiting to be attached to.
func offerWindowClose(env *Env, window tmux.Window, assumeYes bool) {
	if window.ID == "" {
		return
	}
	// Closing a session's last window ends the session, which moves an attached
	// client elsewhere or detaches it. Normally the base window keeps the session
	// alive, so this only comes up once that has been closed by hand.
	last := ""
	if tmux.LastInSession(window.ID) {
		last = fmt.Sprintf(", the last in session %s, which ends with it", window.Session)
	}

	if assumeYes {
		_ = tmux.KillWindow(window.ID)
		env.progressf("closed its tmux window (%s%s)", window.Name, last)
		return
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		// Nobody to ask — treemux was run by a script or an agent. Say what to
		// run rather than closing a window unasked, since something may still be
		// running in it.
		env.progressf("its tmux window (%s%s) now points at a deleted directory; close it with: tmux kill-window -t %s",
			window.Name, last, window.ID)
		return
	}
	defer tty.Close()

	fmt.Fprintf(tty, "close its tmux window (%s%s)? [y/N] ", window.Name, last)
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
		_ = tmux.KillWindow(window.ID)
	}
}

// ---- ls --------------------------------------------------------------------

func cmdLs(env *Env, args []string) error {
	var asJSON bool
	positional, err := parseArgs("ls", args, map[string]*bool{"--json": &asJSON}, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(at(positional, 0))
	if err != nil {
		return err
	}

	repo := repoFor(cfg)
	managed, err := repo.Managed()
	if err != nil {
		return err
	}
	session := sessionFor(cfg)
	windows := tmux.Windows(session)

	// Nothing at all when there are no worktrees, in either mode. The base
	// checkout heads the listing as it heads the menu, but a table holding only
	// the row that is always there says nothing a repository with no worktrees
	// needs to hear, and a JSON consumer counting what it can work on should not
	// have to subtract the one row it can never remove.
	//
	// This is the one state where the listing and the menu differ, and they
	// differ because they are for different things. The menu is a way through, so
	// it must offer the base checkout precisely when there is nothing else to
	// offer. A listing is an answer, and "no worktrees" is the answer.
	infos := make([]git.Info, 0, len(managed)+1)
	if len(managed) > 0 {
		infos = append(infos, repo.BaseCheckout(cfg.BaseBranch))
	}
	for _, wt := range managed {
		infos = append(infos, repo.Inspect(wt, cfg.BaseBranch))
	}

	if asJSON {
		// An empty array, not a message: a caller parsing this needs valid JSON
		// whether or not there is anything to report.
		return writeJSON(env, worktreesJSON(infos, windows))
	}
	if len(infos) == 0 {
		env.progressf("no worktrees for %s", repo.Name())
		return nil
	}
	worktreeTable(infos, windows, session).Render(env.Stdout, ui.ColorEnabled(env.Stdout))
	return nil
}

// ---- prune -----------------------------------------------------------------

func cmdPrune(env *Env, args []string) error {
	var yes bool
	positional, err := parseArgs("prune", args, map[string]*bool{"-y": &yes, "--yes": &yes}, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(at(positional, 0))
	if err != nil {
		return err
	}

	repo := repoFor(cfg)
	// Judge against a current origin/<base> for the same reason rm does: a branch
	// that landed moments ago should be reapable now, not next time.
	_ = repo.Fetch("origin", cfg.BaseBranch)

	managed, err := repo.Managed()
	if err != nil {
		return err
	}
	var targets []git.Worktree
	for _, wt := range managed {
		if git.DirtyFiles(wt.Dir) != 0 {
			continue
		}
		if repo.IsMerged(wt.Branch, cfg.BaseBranch) {
			targets = append(targets, wt)
		}
	}

	if len(targets) == 0 {
		env.progressf("nothing to prune: no merged, clean worktrees")
		return nil
	}

	// The worktree paths go to stdout in both modes, so the output has the same
	// shape whether the caller is previewing or committing; the narration on
	// stderr is what says which of the two happened.
	if !yes {
		env.progressf("%d merged worktree(s) can be removed — re-run with --yes to delete them", len(targets))
		for _, wt := range targets {
			fmt.Fprintln(env.Stdout, wt.Dir)
		}
		return nil
	}

	// Where the caller is standing matters here for the same reason it does in
	// rm: prune can delete the very directory their shell sits in. Open windows
	// are looked up for the same reason too, and while the worktrees still exist.
	wd, wdErr := os.Getwd()
	windows := tmux.Windows(sessionFor(cfg))
	stranded := ""

	for _, wt := range targets {
		if err := repo.RemoveWorktree(wt.Dir); err != nil {
			env.warnf("could not remove %s: %v", wt.Slug, err)
			continue
		}
		if wt.Branch != "" {
			_ = repo.DeleteBranch(wt.Branch)
		}
		if wdErr == nil && insideDir(wd, wt.Dir) {
			stranded = wt.Dir
		}
		env.progressf("removed %s", wt.Slug)
		fmt.Fprintln(env.Stdout, wt.Dir)
	}
	_ = repo.FetchPrune("origin")

	if stranded != "" {
		escapeDeletedDir(env, cfg.MainDir, stranded)
	}
	// Asked per worktree, and never assumed: --yes answered "remove these
	// worktrees", which is not the same as "close my windows" — one of them may
	// have a session still running in it.
	for _, wt := range targets {
		offerWindowClose(env, windows[wt.Dir], false)
	}
	return nil
}

// ---- resume ----------------------------------------------------------------

func cmdResume(env *Env, args []string) error {
	positional, err := parseArgs("resume", args, nil, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig("")
	if err != nil {
		return err
	}
	slug := at(positional, 0)
	if slug != "" {
		slug = stripPrefix(env, cfg, slug)
	}

	repo := repoFor(cfg)
	managed, err := repo.Managed()
	if err != nil {
		return err
	}
	// A repository nobody has started a worktree in yet is an ordinary state and
	// not a fault, so this is a message rather than an error — and it is printed
	// above the menu rather than instead of it.
	//
	// Instead of it was the first attempt, on the reasoning that the base
	// checkout would be the session's only window and offering it would answer a
	// question nobody asked. That holds only while you are sitting in that
	// window. Attached elsewhere, or freshly rebooted with no session at all, the
	// menu refused to offer the base window exactly when it was the only thing
	// there was to reach — which is the opposite of why it is in the list.
	//
	// So both: the sentence that says how to start work, and under it the one row
	// there is.
	if len(managed) == 0 && slug == "" {
		noWorktreesHint(env, repo.Name())
	}

	target, err := chooseWorktree(env, cfg, repo, managed, slug)
	switch {
	case errors.Is(err, errCancelled):
		// Nothing was asked for, so nothing failed. Exiting 0 is what lets the
		// popup this usually runs in close on the same Escape that dismissed the
		// picker, rather than staying up to report a refusal as an error.
		return nil
	case err != nil:
		return err
	}

	// The base checkout opens the way `base` opens it, under the base window's
	// name — with resume_command, this being resume.
	if target.Base {
		return openBaseWindow(env, cfg, cfg.ResumeCommand)
	}

	// openWindow does the rest: a window already open on that worktree is the
	// session being asked for, so it is switched to rather than duplicated.
	return openWindow(env, cfg, tmux.Spec{
		Dir:     target.Dir,
		Name:    cfg.WindowName(target.Slug, ""),
		Command: cfg.ResumeCommand,
		Slug:    target.Slug,
		Branch:  target.Branch,
	})
}

// choice is what `resume` and `cd` act on: one row of the menu.
//
// The base checkout is a row like any other to the person reading the list, and
// unlike any other to the code: it has no slug, `rm` and `prune` cannot name it,
// and it opens under its own window name. So it is flagged rather than dressed
// up as a worktree — a synthetic slug would be a name that means nothing to
// `cfg.DirFor`, and the first command to forget the difference would be one
// that deletes something.
type choice struct {
	git.Worktree
	Base bool
}

// baseChoice is the main checkout as a selectable row.
func baseChoice(cfg *config.Config) choice {
	branch, _ := git.CurrentBranch(cfg.MainDir)
	return choice{Worktree: git.Worktree{Dir: cfg.MainDir, Branch: branch}, Base: true}
}

// baseNames are what selects the base checkout when a name is typed rather than
// picked. Both spellings, because both are what comes to mind: "base" is the
// command that opens it and stays right whatever the checkout is parked on,
// while the branch is what the menu displays in the SLUG column, and a name you
// can read off the list and not type back is a small betrayal.
//
// Exact matches only. Slug resolution accepts a prefix, but stretching that here
// would let a "b" that used to mean the "bugfix" worktree quietly start meaning
// the base checkout instead. A worktree whose slug is literally "base" loses the
// short spelling to the checkout and keeps its own full one, which is as it
// should be: the checkout is the thing you cannot otherwise name.
func baseNames(cfg *config.Config, base choice) []string {
	names := []string{"base", cfg.BaseBranch}
	if base.Branch != "" {
		names = append(names, base.Branch)
	}
	return names
}

// chooseWorktree picks what a command should act on: the row named, or one the
// user selects from a menu.
//
// The menu is the `ls` table with a number beside each row, so the thing being
// chosen from is the listing the user already reads, showing the status and
// divergence that make the choice — not a bare list of names.
func chooseWorktree(env *Env, cfg *config.Config, repo git.Repo, managed []git.Worktree, slug string) (choice, error) {
	base := baseChoice(cfg)

	if slug != "" {
		for _, name := range baseNames(cfg, base) {
			if slug == name {
				return base, nil
			}
		}
		wt, err := resolveSlug(env, repo, managed, slug)
		return choice{Worktree: wt}, err
	}

	// The base checkout heads the list, pinned above the slugs rather than sorted
	// among them, so the row you return to most is the one your fingers already
	// know — and stays row 1 as worktrees come and go around it.
	infos := make([]git.Info, 0, len(managed)+1)
	infos = append(infos, repo.BaseCheckout(cfg.BaseBranch))
	for _, wt := range managed {
		infos = append(infos, repo.Inspect(wt, cfg.BaseBranch))
	}
	session := sessionFor(cfg)
	// The menu is a prompt, not an answer, so it goes to stderr: stdout still
	// carries only the path the caller may be capturing.
	header, rows := worktreeTable(infos, tmux.Windows(session), session).Lines(ui.ColorEnabled(env.Stderr))
	idx, err := ui.Pick(env.Stderr, header, rows)
	if err != nil {
		env.progressf("cancelled")
		return choice{}, errCancelled
	}
	if idx == 0 {
		return base, nil
	}
	return choice{Worktree: managed[idx-1]}, nil
}

// errCancelled reports that the menu was dismissed rather than answered.
//
// Kept apart from ErrSilent because declining is not a failure, and the two
// callers cannot treat it alike. `resume` prints nothing on stdout, so it can
// honestly exit 0 and say nothing more. `cd` cannot: its answer is a path, and
// `cd "$(treemux cd)"` with an empty answer would move the shell to the home
// directory — so there it stays a failure.
//
// The difference is visible in a tmux popup, which closes on success and stays up
// on failure so an error can be read. Exiting non-zero for a cancel made Escape
// need pressing twice: once to dismiss the picker, once to clear the popup that
// was holding "cancelled" on screen.
var errCancelled = errors.New("cancelled")

// ---- cd --------------------------------------------------------------------

// cmdCd moves the calling shell into a worktree.
//
// treemux cannot change its parent's directory, so this leans on the same
// eval-file protocol `rm` uses to escape a deleted directory: the shell wrapper
// sources what treemux appends. Without the integration the path on stdout is
// still the answer, so `cd "$(treemux cd foo)"` works unaided.
func cmdCd(env *Env, args []string) error {
	positional, err := parseArgs("cd", args, nil, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig("")
	if err != nil {
		return err
	}
	slug := at(positional, 0)
	if slug != "" {
		slug = stripPrefix(env, cfg, slug)
	}

	repo := repoFor(cfg)
	managed, err := repo.Managed()
	if err != nil {
		return err
	}
	// As in resume, and for the same reason: the hint goes above the menu rather
	// than in place of it, so the base checkout stays reachable in a repository
	// nobody has forked yet.
	if len(managed) == 0 && slug == "" {
		noWorktreesHint(env, repo.Name())
	}

	target, err := chooseWorktree(env, cfg, repo, managed, slug)
	switch {
	case errors.Is(err, errCancelled):
		// Unlike resume, this one stays a failure. The path on stdout is the
		// answer, and `cd "$(treemux cd)"` succeeding with nothing to print would
		// send the shell home.
		return ErrSilent
	case err != nil:
		return err
	}

	fmt.Fprintln(env.Stdout, target.Dir)
	emitEval(env, "cd "+shellQuote(target.Dir))
	if env.EvalFile == "" {
		env.progressf("no shell integration loaded — run: cd %s", target.Dir)
	}
	return nil
}

// ---- base ------------------------------------------------------------------

func cmdBase(env *Env, args []string) error {
	positional, err := parseArgs("base", args, nil, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(at(positional, 0))
	if err != nil {
		return err
	}
	return openBaseWindow(env, cfg, cfg.Command)
}

// openBaseWindow selects the base window on the main checkout, opening it if it
// is not there. It is what `base` does, and what picking the base row out of the
// `resume` menu does, so there is one definition of what that window is.
//
// The command is the caller's, because that is the one thing the two ways in
// disagree about and each is right in its own terms. `base` opens a
// general-purpose window and runs `command`. `resume` reopens something you were
// already using and runs `resume_command`, the same "carry on where I left off"
// every other row in that menu gets — which after a reboot is the point, the
// triage you were doing in the base window being exactly what you want back.
//
// The disagreement shows once and then never again: every later call finds the
// window by its directory and switches to it, whatever it was started with.
func openBaseWindow(env *Env, cfg *config.Config, command string) error {
	if branch, err := git.CurrentBranch(cfg.MainDir); err == nil && branch != cfg.BaseBranch {
		where := branch
		if where == "" {
			where = "a detached HEAD"
		}
		env.warnf("base checkout is on %s, not %s", where, cfg.BaseBranch)
	}

	// The base window is found by the directory it sits in, like any other, so a
	// session that already has one gets it selected rather than gaining a second
	// window on the main checkout.
	//
	// It carries no slug and no branch: it is a checkout rather than a worktree, and
	// the branch it is parked on is the one thing here the user can change from
	// inside the window, so recording it would be recording something that goes
	// stale the moment they do.
	if tmux.Available() {
		return openWindow(env, cfg, tmux.Spec{
			Dir:     cfg.MainDir,
			Name:    strings.ToUpper(cfg.BaseBranch),
			Command: command,
		})
	}

	// Without tmux there is no window to open, so run the command here and hand
	// it the terminal.
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = cfg.MainDir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// ---- attach ------------------------------------------------------------------

// cmdAttach puts the caller in a repository's tmux session.
//
// Three commands already print the way to reach a session they have just opened a
// window in, and until now that was a `tmux attach -t <session>` for the user to
// copy. That spelling is one treemux can get right and a person cannot always:
// it has to name the session exactly, and under TREEMUX_TMUX_LABEL it has to
// reach a server the default `tmux attach` never looks at.
//
// It deliberately does not create the session. `base` is the command that opens a
// repository's first window, and two commands that both bring a session into
// existence — with different windows in it — is one more than the tool needs.
func cmdAttach(env *Env, args []string) error {
	positional, err := parseArgs("attach", args, nil, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(at(positional, 0))
	if err != nil {
		return err
	}
	if !tmux.Available() {
		return fmt.Errorf("tmux is not installed, so there is no session to attach to")
	}

	session := sessionFor(cfg)
	if !tmux.HasSession(session) {
		return fmt.Errorf("no tmux session %s is running — open one with \"treemux base %s\"", session, cfg.Name)
	}

	// Inside tmux there is already a client holding this terminal, and attaching a
	// second one to it is the nesting tmux warns about. Moving the client is the
	// same thing from where the user sits, and it leaves the session's own current
	// window current — arriving where you left off is the difference between this
	// and `resume`.
	if tmux.Inside() {
		if tmux.CurrentSession() == session {
			env.progressf("already attached to %s", session)
			return nil
		}
		return tmux.SwitchTo(session)
	}

	// Outside it, tmux wants the terminal for as long as the client stays
	// attached, so it inherits treemux's own worktrees rather than the pipes every
	// other tmux call here runs through, and this returns when the user detaches.
	attach := exec.Command("tmux", tmux.AttachArgs(session)...)
	attach.Stdin, attach.Stdout, attach.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := attach.Run(); err != nil {
		// tmux has already said what went wrong, on the stderr it was handed.
		return ErrSilent
	}
	return nil
}
