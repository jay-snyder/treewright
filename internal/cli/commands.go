package cli

import (
	"bufio"
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
	positional, err := parseArgs("new", args, nil, 2)
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

	if !tmux.Inside() {
		env.progressf("not in tmux; worktree ready at %s — cd in and run %s yourself", dir, cfg.Command)
		return nil
	}
	return tmux.NewWindow(dir, cfg.WindowName(slug, override), cfg.Command)
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
	}, 1)
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
	// is the window named after the stream.
	staleWindow := tmux.OpenWindows()[dir]

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
// without asking, and an empty window means there was none open.
//
// The window is identified from the worktree's path rather than from the caller's
// own pane, because a teardown is normally run from somewhere else: closing "the
// window I am in" left the window named after the stream behind, still sitting in
// the deleted directory.
func offerWindowClose(env *Env, window string, assumeYes bool) {
	if !tmux.Inside() || window == "" {
		return
	}
	name := tmux.WindowName(window)

	if assumeYes {
		_ = tmux.KillWindow(window)
		env.progressf("closed its tmux window (%s)", name)
		return
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		// Nobody to ask — treemux was run by a script or an agent. Say what to
		// run rather than closing a window unasked, since something may still be
		// running in it.
		env.progressf("its tmux window (%s) now points at a deleted directory; close it with: tmux kill-window -t %s",
			name, window)
		return
	}
	defer tty.Close()

	fmt.Fprintf(tty, "close its tmux window (%s)? [y/N] ", name)
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil {
		return
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
		_ = tmux.KillWindow(window)
	}
}

// ---- ls --------------------------------------------------------------------

func cmdLs(env *Env, args []string) error {
	var asJSON bool
	positional, err := parseArgs("ls", args, map[string]*bool{"--json": &asJSON}, 1)
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
	windows := tmux.OpenWindows()

	infos := make([]git.Info, 0, len(managed))
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
	worktreeTable(infos, windows).Render(env.Stdout, ui.ColorEnabled(env.Stdout))
	return nil
}

// ---- prune -----------------------------------------------------------------

func cmdPrune(env *Env, args []string) error {
	var yes bool
	positional, err := parseArgs("prune", args, map[string]*bool{"-y": &yes, "--yes": &yes}, 1)
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
	windows := tmux.OpenWindows()
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
	positional, err := parseArgs("resume", args, nil, 1)
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
	if len(managed) == 0 {
		return fmt.Errorf("no worktrees to resume for %s", repo.Name())
	}

	// Fetched once and used both to render the menu and to decide below whether a
	// window is already open on the chosen worktree.
	windows := tmux.OpenWindows()

	target, err := chooseWorktree(env, cfg, repo, managed, windows, slug)
	if err != nil {
		return err
	}

	if !tmux.Inside() {
		env.progressf("not in tmux; run: cd %s && %s", target.Dir, cfg.ResumeCommand)
		return nil
	}
	// A window already sitting in that directory is the session being asked for;
	// switch to it rather than opening a duplicate.
	if existing := windows[target.Dir]; existing != "" {
		return tmux.SelectWindow(existing)
	}
	return tmux.NewWindow(target.Dir, cfg.WindowName(target.Slug, ""), cfg.ResumeCommand)
}

// chooseWorktree picks the worktree a command should act on: the one named, the
// only one there is, or one the user selects from a menu.
//
// The menu is the `ls` table with a number beside each row, so the thing being
// chosen from is the listing the user already reads, showing the status and
// divergence that make the choice — not a bare list of names.
func chooseWorktree(env *Env, cfg *config.Config, repo git.Repo, managed []git.Worktree, windows map[string]string, slug string) (git.Worktree, error) {
	switch {
	case slug != "":
		return resolveSlug(env, repo, managed, slug)
	case len(managed) == 1:
		// Nothing to choose between: prompting would be a keystroke that has only
		// one possible answer.
		return managed[0], nil
	}

	infos := make([]git.Info, 0, len(managed))
	for _, wt := range managed {
		infos = append(infos, repo.Inspect(wt, cfg.BaseBranch))
	}
	// The menu is a prompt, not an answer, so it goes to stderr: stdout still
	// carries only the path the caller may be capturing.
	header, rows := worktreeTable(infos, windows).Lines(ui.ColorEnabled(env.Stderr))
	idx, err := ui.Pick(env.Stderr, header, rows)
	if err != nil {
		env.progressf("cancelled")
		return git.Worktree{}, ErrSilent
	}
	return managed[idx], nil
}

// ---- cd --------------------------------------------------------------------

// cmdCd moves the calling shell into a worktree.
//
// treemux cannot change its parent's directory, so this leans on the same
// eval-file protocol `rm` uses to escape a deleted directory: the shell wrapper
// sources what treemux appends. Without the integration the path on stdout is
// still the answer, so `cd "$(treemux cd foo)"` works unaided.
func cmdCd(env *Env, args []string) error {
	positional, err := parseArgs("cd", args, nil, 1)
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
	if len(managed) == 0 {
		return fmt.Errorf("no worktrees for %s — create one with \"treemux new <slug>\"", repo.Name())
	}

	target, err := chooseWorktree(env, cfg, repo, managed, tmux.OpenWindows(), slug)
	if err != nil {
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
	positional, err := parseArgs("base", args, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(at(positional, 0))
	if err != nil {
		return err
	}

	if branch, err := git.CurrentBranch(cfg.MainDir); err == nil && branch != cfg.BaseBranch {
		where := branch
		if where == "" {
			where = "a detached HEAD"
		}
		env.warnf("base checkout is on %s, not %s", where, cfg.BaseBranch)
	}

	if tmux.Inside() {
		return tmux.NewWindow(cfg.MainDir, strings.ToUpper(cfg.BaseBranch), cfg.Command)
	}

	// Outside tmux, run the command here and hand it the terminal.
	cmd := exec.Command("sh", "-c", cfg.Command)
	cmd.Dir = cfg.MainDir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
