package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/refname"
	"github.com/jay-snyder/treewright/internal/tmux"
	"github.com/jay-snyder/treewright/internal/ui"
)

// ---- new -------------------------------------------------------------------

func cmdNew(env *Env, args []string) error {
	var prompt string
	positional, err := parseArgs("new", args, nil, map[string]*string{"-p": &prompt, "--prompt": &prompt}, 2)
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
	prefix, slug := splitPrefix(env, cfg, slug)
	if err := validateSlug(cfg, slug); err != nil {
		return err
	}
	// Resolved before anything is created: a prompt the command cannot take is
	// this invocation being wrong, and finding that out after the worktree
	// exists would leave a half-made one behind an error about a flag.
	command, err := fillPrompt(cfg.Command, "command", prompt)
	if err != nil {
		return err
	}

	// The prefix reaches the branch and stops there. The directory, the window
	// name, and every later resume or rm are the slug alone: which kind of work
	// this is matters while the branch is being made, and afterwards git holds the
	// answer — so carrying it into the slug would only lengthen every row of the
	// table and every name the user has to type back.
	repo := repoFor(cfg)
	dir := cfg.DirFor(slug)
	branch := prefix + slug

	// Checked before anything is reported, because git's own refusal arrives
	// several steps in — after "reusing existing branch" has already been printed
	// — and says "already exists" about a path rather than naming the command
	// that opens what is already there.
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("worktree %s already exists%s", slug, asFields(
			field("path", dir),
			field("open it with", env.copyable(env.Argv0+" resume "+slug)),
		))
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
			warnIfBaseIsAhead(env, repo, cfg)
		} else if repo.BranchExists(cfg.BaseBranch) {
			// Offline: a local base branch is a stale but usable fork point.
			env.warnf("origin unreachable — forking from the local %s instead\nit may be behind what is on origin", cfg.BaseBranch)
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
	// True from the moment the worktree exists until a window has run the
	// command in it, which is the window opened below — and stays true when that
	// window never opens, which is the state `resume` needs to recognize.
	markNoAgentYet(env, cfg, slug)

	// The worktree path is this command's answer, so it goes to stdout and
	// nowhere else: `cd "$(treewright new foo)"` is meant to work.
	fmt.Fprintln(env.Stdout, dir)

	// Reported rather than returned: the worktree exists, the branch exists, and
	// the path is already on stdout, so `cd "$(treewright new eng-1)"` must not fail
	// because tmux could not be made to open a window. resume and base do return
	// it, a window being the whole of what they were asked for.
	created, err := openWindow(env, cfg, tmux.Spec{
		Dir:     dir,
		Name:    cfg.WindowName(slug, override),
		Command: command,
		Slug:    slug,
		Branch:  branch,
	})
	if err != nil {
		env.warnf("%v", err)
	}
	warnIfPromptUndelivered(env, prompt, created, err)
	if created {
		clearNoAgentYet(cfg, slug)
	}
	return nil
}

// warnIfBaseIsAhead says so when the base branch holds commits origin has not
// got, on the path that forks from origin.
//
// Nothing is wrong here — the fork point is the one treewright promises, and
// pushing is the user's own call — but what is easy to miss is what it means
// for the worktree just made: work committed in the base checkout and not
// pushed is not in it, and files that exist only in those commits do not exist
// there at all. That is a message worth one line now rather than an empty file
// three steps into the work, which is where it otherwise surfaces.
//
// The comparison is the local base branch against origin's, not whatever the
// base checkout happens to have out: the branch is what `new` would have forked
// from, and a checkout parked elsewhere is a separate question that `base`
// already asks.
//
// Three lines, in the order every message here takes: the finding, what it
// costs, then what to do about it with the copyable part last. Said as one
// sentence it ran to three clauses hung off each other, and the reader who most
// needs it is the one who has just watched a worktree open and is already
// typing in it.
func warnIfBaseIsAhead(env *Env, repo git.Repo, cfg *config.Config) {
	ahead, _, ok := repo.AheadBehind(cfg.BaseBranch, cfg.BaseBranch)
	if !ok || ahead == 0 {
		return
	}
	env.warnf("%s has %s not on origin/%s\n"+
		"the worktree forks from origin, so that work is not in it\n"+
		"push %s and remake the worktree, or bring them over from inside it:  %s",
		cfg.BaseBranch, count(ahead, "commit", "commits"), cfg.BaseBranch, cfg.BaseBranch,
		env.copyable(fmt.Sprintf("git cherry-pick origin/%s..%s", cfg.BaseBranch, cfg.BaseBranch)))
}

// warnIfPromptUndelivered says so when a --prompt never reached an agent: the
// command carrying it runs only in a window openWindow created, so one merely
// found — or one that failed to open — leaves the user believing work was
// kicked off that nobody is doing. The prompt was typed once and is gone, so
// the warning names the recovery.
func warnIfPromptUndelivered(env *Env, prompt string, created bool, err error) {
	if prompt == "" || created || err != nil {
		return
	}
	env.warnf("prompt not delivered — the window was already open\npaste it to the agent there")
}

// validateSlug rejects slugs that would not round-trip through a directory name
// or a branch name.
//
// The rules themselves live in internal/refname, which owns the branch-name syntax
// for both halves of a name: the restatement that refuses a bad slug here is the
// one that refuses a bad branch prefix when a config is loaded. What stays here is
// the part that depends on the configuration — a leading word that could have been
// a prefix — and turning a refusal into a usage error.
func validateSlug(cfg *config.Config, slug string) error {
	if slug == "" {
		return usageErrorf("new", "the slug is empty once the branch prefix is removed")
	}
	// Where several prefixes are configured, a leading "feature/" is meaningful, so
	// one treewright does not recognize is far likelier to be the scheme misspelled
	// than a slug with a slash in it. Naming the configured set answers both
	// readings at once, and it has to come first: refname's account of "/" is about
	// stray directories, which is the right answer only when a prefix is not what
	// was meant.
	if leading, _, found := strings.Cut(slug, "/"); found && len(cfg.Prefixes()) > 1 {
		return usageErrorf("new", "%q does not name a configured branch prefix\nthis repo uses: %s",
			leading+"/", prefixList(cfg))
	}
	if err := refname.CheckSlug(slug); err != nil {
		return usageErrorf("new", "%v", err)
	}
	return nil
}

// carryFiles copies in what a new worktree starts without and the app needs:
// .env files, local credentials, editor settings. Usually files git ignores,
// which is why they are missing from a fresh checkout, but the rule is what the
// worktree needs rather than what git hides.
//
// The agent module's local-state files ride along when the config names an
// agent — the plugin among them, which nothing ignores until the repository
// says so — differing in one way: missing ones are skipped silently. An explicit
// carry_files entry warns because the user asserted the file exists and a
// missing one is a stale config; the agent's were asserted by nobody, and a
// checkout that has never run the agent has nothing to carry yet.
func carryFiles(env *Env, cfg *config.Config, dir string) {
	for _, rel := range cfg.CarryFiles {
		carryOne(env, cfg, dir, rel, true)
	}
	for _, rel := range cfg.AgentCarries() {
		carryOne(env, cfg, dir, rel, false)
	}
}

// carryOne copies a single carried file, asserted saying whether the config
// wrote the entry itself — which is what decides if a missing source is worth
// a warning. A copy that starts and fails warns either way: that is a real
// failure, not an absence.
func carryOne(env *Env, cfg *config.Config, dir, rel string, asserted bool) {
	src := filepath.Join(cfg.MainDir, rel)
	info, err := os.Stat(src)
	if err != nil || info.IsDir() {
		if asserted {
			env.warnf("carry_files: %s not found in %s", rel, cfg.MainDir)
		}
		return
	}
	dst := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		env.warnf("carry_files: %v", err)
		return
	}
	if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
		env.warnf("carry_files: %s: %v", rel, err)
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

// startPostCreate launches the configured setup without waiting, so the window
// opens immediately.
//
// Its output goes to a log file under .git rather than to the terminal or to
// /dev/null: inside the worktree it would show up as an untracked file and make
// the tree read as dirty, and discarded entirely there would be no way to find
// out why an install failed.
func startPostCreate(env *Env, cfg *config.Config, dir, slug string) error {
	logPath, failedPath := postCreatePaths(cfg, slug)
	// Cleared before the early return as well as before a run: a slug can be
	// recreated after its worktree was removed, and a marker left by the last one
	// would otherwise report a failure this worktree never had.
	_ = os.Remove(failedPath)
	if len(cfg.PostCreate) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	// The child gets its own duplicated descriptor, so closing this one does not
	// cut off its output after treewright exits.
	defer logFile.Close()

	cmd := exec.Command("sh", "-c", postCreateScript(cfg.PostCreate, failedPath))
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	// Deliberately not waited on: the process outlives treewright.
	env.progressf("running %s in the background%s",
		count(len(cfg.PostCreate), "post_create command", "post_create commands"),
		asFields(field("log", logPath)))
	return nil
}

// postCreatePaths returns where a worktree's setup writes its log, and the marker
// a failed one leaves behind. Both live beside the repository rather than inside
// the worktree, where they would read as untracked files and make the tree dirty.
func postCreatePaths(cfg *config.Config, slug string) (logPath, failedPath string) {
	stem := filepath.Join(cfg.MainDir, ".git", "treewright", "post-create-"+strings.ReplaceAll(slug, "/", "-"))
	return stem + ".log", stem + ".failed"
}

// warnIfSetupFailed reports a post_create that stopped, on the commands a user
// reaches a worktree through.
//
// Nothing waits for post_create — that is the point of running it in the
// background — so treewright has already exited by the time it fails, and the
// failure has nowhere to be said. Left at that, the log is the only record, and
// reading it requires already suspecting there is something to read: the window
// opened, the agent started, and the first sign of trouble is a build failing for
// reasons that have nothing to do with the work. So the failing step leaves a
// marker, and the next command that mentions this worktree says so.
//
// It keeps saying so, rather than clearing the marker once reported. A half
// installed worktree stays half installed, and a warning that appears once, in
// whichever command happened to run first, is one a user who stepped away never
// sees. Finishing the setup by hand and removing the file is what ends it.
func warnIfSetupFailed(env *Env, cfg *config.Config, slug string) {
	logPath, failedPath := postCreatePaths(cfg, slug)
	body, err := os.ReadFile(failedPath)
	if err != nil {
		return
	}
	if failing := strings.TrimSpace(string(body)); failing != "" {
		env.warnf("post_create failed in %s%s", slug, asFields(
			field("failed step", failing),
			field("log", logPath),
		))
		return
	}
	env.warnf("post_create did not finish in %s%s", slug, asFields(field("log", logPath)))
}

// postCreateScript renders the configured commands as one shell script that runs
// them in order and stops at the first failure.
//
// The sequencing has to happen inside the shell rather than here, because
// treewright exits as soon as the window is open — there is no treewright left to
// run the second command. So the whole sequence goes to one `sh -c`.
//
// Each command runs in a subshell, which makes it a step in the sense every
// steps-list a user already knows means it: one that starts in the worktree root
// whatever the last one did, and whose failure is its own. `set -e` over a flat
// script was the alternative, and it fails in both directions — it reaches inside
// a step to stop on a failure the user had already handled with `||`, and a step
// that calls `exit` itself, or sources something that does, ends the whole run
// silently. A step wanting to work elsewhere says `cd sub && ...` in its own step.
//
// Each step is announced into the log in the `$ command` form a terminal would
// have shown, and a failing one says so by name before exiting. Without that, a
// log truncated halfway through a five-step install is indistinguishable from one
// still being written.
//
// A failing step also writes the command that failed to failedPath, which is the
// only way the failure reaches the user: nothing waits for this script, so by the
// time it stops there is no treewright left to report it. See warnIfSetupFailed.
func postCreateScript(commands []string, failedPath string) string {
	var b strings.Builder
	for _, c := range commands {
		fmt.Fprintf(&b, "printf '\\n$ %%s\\n' %s\n", shellQuote(c))
		fmt.Fprintf(&b, "( %s\n) || { tw_status=$?; printf '\\npost_create stopped: %%s failed\\n' %s; printf '%%s\\n' %s > %s; exit $tw_status; }\n",
			c, shellQuote(c), shellQuote(c), shellQuote(failedPath))
	}
	return b.String()
}

// ---- the worktree whose first agent never started ---------------------------

// A worktree that got made and never had an agent in it used to be a worktree
// nothing could open. `resume` runs resume_command, which is "carry on where I
// left off" — `claude --continue` and its like — and there is nothing to carry
// on from, so it exits on an error and the held-open wrapper parks the window on
// that. `new` refuses the slug, the worktree being right there, and points at
// `resume`: the one command that could not work. `ls` shows a healthy row with an
// empty WINDOW column. The only way out was to remove the worktree and start
// again, which is a long way to go for a window that failed to open.
//
// treewright cannot ask an agent whether it has a conversation in a directory, so
// it keeps a note of its own: a marker saying no agent has started here yet,
// written when the worktree is made and taken off the moment a window actually
// runs the command. `resume` reads it and runs command instead, which is the
// honest reading of "reopen a window on this worktree" when nothing was ever
// open.
//
// The marker is the negative on purpose. One saying an agent *has* run here
// would be missing from every worktree made by a treewright that never wrote one
// — worktrees in use for weeks, holding exactly the conversation --continue
// wants — and the fallback would greet each of them with a fresh agent and no
// history. A first-run heuristic that silently discards someone's session is a
// worse bug than the one being fixed, so absence has to keep meaning "as
// before": the state that already existed stays silent, and the marker is the
// news.
//
// It records that an *agent* was started, not that a *window* was opened, which
// is where the two part company — the case this exists for is the window that
// was never opened at all.

// firstAgentNote is what the marker holds. Nothing reads it back — its presence
// is the whole record — but a file in .git/treewright that cannot say what wrote
// it is a file nobody can safely delete.
const firstAgentNote = "no agent has started in this worktree yet\n" +
	"treewright resume opens it with command rather than resume_command until one has\n" +
	"delete this file to make resume use resume_command instead\n"

// firstAgentPath names the marker: beside post_create's log, inside the .git
// directory treewright already writes to, so this adds no new place to look for
// what treewright left behind.
func firstAgentPath(cfg *config.Config, slug string) string {
	return filepath.Join(cfg.MainDir, ".git", "treewright", "no-agent-yet-"+strings.ReplaceAll(slug, "/", "-"))
}

// markNoAgentYet records that a worktree exists and nothing has started an agent
// in it.
//
// Written as the worktree is made rather than once a window has failed to open,
// for the reason startPostCreate clears its failure marker there: a slug can be
// recreated after its worktree was removed, and the answer left by the last one
// would otherwise be inherited by a worktree it is not about.
//
// A failure to write is worth saying, because what it costs is the recovery this
// file exists for — and the state it leaves is the one a user cannot work out
// from anything treewright shows them.
func markNoAgentYet(env *Env, cfg *config.Config, slug string) {
	path := firstAgentPath(cfg, slug)
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		err = os.WriteFile(path, []byte(firstAgentNote), 0o644)
	}
	if err != nil {
		env.warnf("could not record that no agent has run in %s yet: %v", slug, err)
	}
}

// clearNoAgentYet takes the marker off, a window having run the command.
//
// Nothing is reported when the removal fails. What it costs is one resume
// opening a fresh agent where --continue would have worked, which is the
// direction this whole mechanism already errs in, and a warning about a file the
// user has never heard of would be louder than the thing it describes.
//
// A config with a blank command runs nothing either way, so there the
// distinction has nothing to bite on.
func clearNoAgentYet(cfg *config.Config, slug string) {
	_ = os.Remove(firstAgentPath(cfg, slug))
}

// noAgentYet reports that nothing has started an agent in a worktree, so
// `resume` should run command rather than resume_command.
func noAgentYet(cfg *config.Config, slug string) bool {
	_, err := os.Stat(firstAgentPath(cfg, slug))
	return err == nil
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
	// Only the slug is wanted here: a worktree is named by its slug, and the prefix
	// the user may have typed is the branch's, which git already knows.
	_, slug = splitPrefix(env, cfg, slug)

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
			reasons = append(reasons, count(n, "uncommitted file", "uncommitted files"))
		}
		// Unpushed commits are only lost work if they are not already merged: a
		// squash merge leaves them unreachable from origin even though their
		// content landed.
		if n := repo.Unpushed(branch); n > 0 && !repo.IsMerged(branch, cfg.BaseBranch) {
			reasons = append(reasons, count(n, "commit not on origin", "commits not on origin"))
		}
		if len(reasons) > 0 {
			// The reasons are listed rather than joined with "and": there can be
			// two of them, each carrying a count, and what the reader is weighing
			// is how much each one is worth — which is a column to run an eye
			// down, not a clause to parse.
			env.errorf("won't remove %s — it has unsaved work%s\nuse %s to remove it anyway",
				slug, asLines(reasons), env.copyable("--force"))
			return ErrSilent
		}
	}

	// Note the caller's location before deleting anything, so we can tell whether
	// their shell is about to be standing in a directory that is gone.
	wd, wdErr := os.Getwd()
	// And which window is open on the worktree, asked while the worktree still
	// exists. This is usually not the caller's own window: `treewright rm eng-1` is
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
	// git reports one only for worktrees created outside treewright.
	if branch != "" {
		if err := repo.DeleteBranch(branch); err != nil {
			env.warnf("could not delete branch %s: %v", branch, err)
		}
	}
	_ = repo.FetchPrune("origin")
	removed := []([2]string){field("worktree", dir)}
	if branch == "" {
		removed = append(removed, field("branch", "none — it was on no branch"))
	} else {
		removed = append(removed, field("branch", branch))
	}
	env.progressf("removed %s%s", slug, asFields(removed...))
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
// exists. Without the shell integration treewright can only say so.
func escapeDeletedDir(env *Env, mainDir, goneDir string) {
	emitEval(env, "cd "+shellQuote(mainDir))
	if env.EvalFile == "" {
		env.progressf("your shell is in %s, which no longer exists%s", goneDir,
			asFields(field("run", env.copyable("cd "+mainDir))))
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
	//
	// It gets a line of its own rather than a clause inside the parenthetical
	// naming the window. What it describes is a consequence of answering yes —
	// the session goes, and an attached client goes with it — and a reader
	// weighing that should not have to find it in the middle of the question.
	last := ""
	if tmux.LastInSession(window.ID) {
		last = fmt.Sprintf("it is the last in session %s, which ends with it", window.Session)
	}

	if assumeYes {
		_ = tmux.KillWindow(window.ID)
		env.progressf("closed its tmux window %s%s", window.Name, under(last))
		return
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		// Nobody to ask — treewright was run by a script or an agent. Say what to
		// run rather than closing a window unasked, since something may still be
		// running in it.
		env.progressf("tmux window %s now points at a deleted directory%s%s",
			window.Name, under(last),
			asFields(field("close it with", env.copyable("tmux kill-window -t "+window.ID))))
		return
	}
	defer tty.Close()

	// Above the question rather than through progressf, because the caveat and
	// the question have to arrive together on the same terminal, and stderr may
	// have been sent somewhere else entirely.
	if last != "" {
		fmt.Fprintf(tty, "%s\n", last)
	}
	fmt.Fprintf(tty, "close its tmux window (%s)? [y/N] ", window.Name)
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

	// The base checkout heads the listing as it heads the menu, and in a
	// repository with no worktrees yet the two modes part company: the table
	// prints nothing, the JSON still carries the base row.
	//
	// They differ because they are for different things. The table is for a
	// person, and one holding only the row that is always there says nothing a
	// repository with no worktrees needs to hear — "no worktrees" is the answer,
	// and it goes on stderr where an answer's readers will not trip over it.
	// The JSON is a schema, and a row that appears only sometimes is not one: a
	// consumer working out where to put a piece of work reads row 0, and having
	// to check first whether row 0 exists makes every reader of the schema carry
	// the special case.
	//
	// The empty array was also read as "this repository is not registered",
	// which sent its reader through --help and the config files looking for a
	// registration that was already there. That state has an answer of its own —
	// an unregistered repo exits 1 with "no registered config matches repo
	// <path>" — so the fault was never an unanswerable question, it was one
	// schema saying two things.
	infos := make([]git.Info, 0, len(managed)+1)
	if asJSON || len(managed) > 0 {
		infos = append(infos, repo.BaseCheckout(cfg.BaseBranch))
	}
	for _, wt := range managed {
		infos = append(infos, repo.Inspect(wt, cfg.BaseBranch))
	}

	// After the output rather than before it, in either mode: a warning above a
	// table is scrolled off by the table, and stderr is not part of the answer, so
	// `ls --json | jq` is unaffected either way.
	defer func() {
		for _, wt := range managed {
			warnIfSetupFailed(env, cfg, wt.Slug)
		}
	}()

	if asJSON {
		// An array, never a message: a caller parsing this needs valid JSON
		// whether or not there is anything to report, and the base row is there
		// in every repository this command can answer about.
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
		env.progressf("%s can be removed\nre-run with %s to delete them",
			count(len(targets), "merged worktree", "merged worktrees"), env.copyable("--yes"))
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
	var prompt string
	positional, err := parseArgs("resume", args, nil, map[string]*string{"-p": &prompt, "--prompt": &prompt}, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig("")
	if err != nil {
		return err
	}
	// Before the menu, so a resume_command that cannot take the prompt is
	// refused before anyone has picked a row to send it to.
	command, err := fillPrompt(cfg.ResumeCommand, "resume_command", prompt)
	if err != nil {
		return err
	}
	slug := at(positional, 0)
	if slug != "" {
		_, slug = splitPrefix(env, cfg, slug)
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
	// name — with resume_command, this being resume. The prompt rides along:
	// the base window runs an agent too, and "resume the base and hand it this"
	// is the same sentence as for any worktree.
	if target.Base {
		// The first-agent fallback below deliberately stops here. The base
		// checkout is not a worktree treewright made, so there is no moment at
		// which it could honestly write that nothing had run in it — and `tw
		// base` opens that window with command already, so the way in that needs
		// no conversation is a command of its own rather than a fallback.
		created, err := openBaseWindow(env, cfg, command)
		warnIfPromptUndelivered(env, prompt, created, err)
		return err
	}

	// Said before the window opens, since afterwards the agent has the screen.
	warnIfSetupFailed(env, cfg, target.Slug)

	// Nothing has ever started an agent here, so there is no conversation for
	// resume_command to continue and it would exit on saying so. command is what
	// this worktree is still owed.
	//
	// Filled here rather than beside resume_command at the top, because a
	// template with no {prompt} in it is only a problem for the invocation that
	// needs it: refusing every `resume --prompt` in a repository whose command
	// cannot take one, for a fallback most of them will not use, is the wrong
	// half of that trade. Nothing has been created either way, so the refusal is
	// as cheap here as it is there.
	if noAgentYet(cfg, target.Slug) {
		fresh, err := fillPrompt(cfg.Command, "command", prompt)
		if err != nil {
			return err
		}
		command = fresh
		env.progressf("no agent has run in %s yet — opening it with command rather than resume_command", target.Slug)
	}

	// openWindow does the rest: a window already open on that worktree is the
	// session being asked for, so it is switched to rather than duplicated.
	created, err := openWindow(env, cfg, tmux.Spec{
		Dir:     target.Dir,
		Name:    cfg.WindowName(target.Slug, ""),
		Command: command,
		Slug:    target.Slug,
		Branch:  target.Branch,
	})
	warnIfPromptUndelivered(env, prompt, created, err)
	if created {
		clearNoAgentYet(cfg, target.Slug)
	}
	return err
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

// baseName is what the main checkout answers to wherever it stands beside the
// worktrees: the row in the resume menu, the word typed back at it, and the
// label `refresh` reports its checkout under. Named once because those have to
// agree — a user who reads "base" off one and types it at another is entitled to
// reach the same place.
const baseName = "base"

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
	names := []string{baseName, cfg.BaseBranch}
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
		if slices.Contains(baseNames(cfg, base), slug) {
			return base, nil
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
// `cd "$(treewright cd)"` with an empty answer would move the shell to the home
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
// treewright cannot change its parent's directory, so this leans on the same
// eval-file protocol `rm` uses to escape a deleted directory: the shell wrapper
// sources what treewright appends. Without the integration the path on stdout is
// still the answer, so `cd "$(treewright cd foo)"` works unaided.
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
		_, slug = splitPrefix(env, cfg, slug)
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
		// answer, and `cd "$(treewright cd)"` succeeding with nothing to print would
		// send the shell home.
		return ErrSilent
	case err != nil:
		return err
	}

	fmt.Fprintln(env.Stdout, target.Dir)
	// A shell about to run the project's own commands in there is exactly who needs
	// to know the install stopped half way.
	if !target.Base {
		warnIfSetupFailed(env, cfg, target.Slug)
	}
	emitEval(env, "cd "+shellQuote(target.Dir))
	if env.EvalFile == "" {
		env.progressf("no shell integration loaded, so your shell did not move%s",
			asFields(field("run", env.copyable("cd "+target.Dir))))
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
	// No --prompt here — base reuses its one window nearly every time, which is
	// exactly when a prompt would go undelivered — but the placeholder still has
	// to come out of the template on the first open of the day.
	command, err := fillPrompt(cfg.Command, "command", "")
	if err != nil {
		return err
	}
	_, err = openBaseWindow(env, cfg, command)
	return err
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
func openBaseWindow(env *Env, cfg *config.Config, command string) (created bool, err error) {
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
	// it the terminal — which is the command running, prompt included, so this
	// counts as created for the caller weighing whether a prompt was delivered.
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = cfg.MainDir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return true, cmd.Run()
}

// ---- attach ------------------------------------------------------------------

// cmdAttach puts the caller in a repository's tmux session.
//
// Three commands already print the way to reach a session they have just opened a
// window in, and until now that was a `tmux attach -t <session>` for the user to
// copy. That spelling is one treewright can get right and a person cannot always:
// it has to name the session exactly, and under TREEWRIGHT_TMUX_LABEL it has to
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
		return fmt.Errorf("no tmux session %s is running%s", session,
			asFields(field("open one with", env.Argv0+" base "+cfg.Name)))
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
	// attached, so it inherits treewright's own worktrees rather than the pipes every
	// other tmux call here runs through, and this returns when the user detaches.
	attach := exec.Command("tmux", tmux.AttachArgs(session)...)
	attach.Stdin, attach.Stdout, attach.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := attach.Run(); err != nil {
		// tmux has already said what went wrong, on the stderr it was handed.
		return ErrSilent
	}
	return nil
}
