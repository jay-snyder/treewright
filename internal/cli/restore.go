package cli

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// Putting a repository's session back after the machine has restarted.
//
// The design notes already carry the sentence this command is: a tmux session
// does not survive a reboot while a checkout on disk does, so it is something you
// reopen. `resume` applies that one worktree at a time, and that was the whole of
// the morning after a restart — `treewright base`, then `treewright attach`, then
// one `treewright resume` per worktree, per repository. Three repositories with
// three worktrees each is fifteen commands to arrive back where you were.
//
// **There is no snapshot, and there must not be one.** The worktrees on disk are
// the record; restore reads them and opens what they say. Recording the live
// layout and replaying it was the obvious alternative and it loses on every
// count: to cover the reboot that actually hurts — the unexpected one — the
// snapshot would have to be written by every command that opens or closes a
// window, it would drift the moment somebody rearranged tmux by hand, and it
// could name a worktree since removed. It also cuts against the precedent in
// docs/agents.md that keeps agent state on the window and never on disk, and it
// is not even free in the registry: config.Names globs *.toml and doctor's
// registry check calls anything else a stray, so a snapshot would need a
// directory of its own to live in.
//
// What follows from that is what the help says out loud: restore gives you a
// tidied session rather than a photocopy of the one you lost. The base window,
// then the worktrees in the order `ls` prints them, and a window already open on
// one left exactly as it is — which is also what makes this safe to type into a
// session that is already up, where it means "open whatever is missing here".
//
// **One repository, the one you are standing in.** No --all: a terminal tab per
// repository is the shape of the day anyway, so batching across repositories
// would save one command per tab and in exchange would spin up sessions and
// agents for every repository ever registered. The optional [repo] stays,
// spelled as every other command spells it, because a terminal tab launches in
// no particular directory and a session file has to name the repository it means.

// cmdRestore opens every window a repository's session should have, and attaches
// this terminal to it.
func cmdRestore(env *Env, args []string) error {
	var detached, fresh bool
	var repoName string
	positional, err := parseArgs("restore", args, map[string]*bool{
		"-d": &detached, "--detached": &detached, freshFlag: &fresh,
	}, repoValues(&repoName, nil), 1)
	if err != nil {
		return err
	}
	named, err := namedRepo(env, "restore", repoName, at(positional, 0))
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(named)
	if err != nil {
		return err
	}
	if !tmux.Available() {
		return fmt.Errorf("tmux is not installed, so there is no session to restore")
	}

	// resume's own pair, built by resume's own function: resume_command with
	// command behind it, or command alone under --fresh. That is what gives every
	// restored agent its conversation back — claude --continue picks the
	// conversation by directory, and the directory is what survived the reboot —
	// and what starts a fresh agent in a worktree that has nothing to continue.
	//
	// Built before anything is opened, so a command too long for tmux is one
	// refusal rather than a dozen.
	run, err := resumeCommand(cfg, "", fresh)
	if err != nil {
		return err
	}

	managed, err := repoFor(cfg).Managed()
	if err != nil {
		return err
	}

	opened, failed := restoreWindows(env, cfg, run, managed)
	selectBaseWindow(cfg)
	session := sessionFor(cfg)

	// The one time there is something to read is the one time you are not
	// swallowed by the session. A window that could not be opened is work the
	// reader has to finish by hand, so the report stays on screen and the way in
	// is named rather than taken.
	if len(failed) > 0 {
		stayedOut := ""
		if !detached {
			stayedOut = "this terminal was not attached"
		}
		env.errorf("%s could not be opened%s%s", count(len(failed), "window", "windows"),
			under(stayedOut), asFields(
				// One per line, as `close` lists the windows it can see: a
				// repository with a dozen worktrees is exactly the one where this
				// list is worth reading, and exactly the one where a comma-joined
				// one runs off the side of the terminal.
				field("missing", strings.Join(failed, "\n")),
				field("attach anyway with", attachCommand(env, cfg)),
			))
		return ErrSilent
	}

	// Every window this repository wanted turned out to be open in some other
	// session — the warnings above say which — so there is no session of its own
	// to report on or to attach to. Rare, and worth four lines: the alternative is
	// handing tmux a target that is not there and passing on its complaint about
	// it.
	if !tmux.HasSession(session) {
		env.warnf("no tmux session %s is running\nevery window this restore would have opened is already in another session", session)
		return nil
	}

	if detached {
		// Nobody is about to look at the session, so the line saying what is in it
		// is worth printing: the shape focusWindow prints about one window, for the
		// whole session.
		env.progressf("%s open in tmux session %s%s", count(opened, "window", "windows"), session,
			asFields(field("attach with", attachCommand(env, cfg))))
		return nil
	}

	// Inside tmux the attach is a switch-client, which needs no terminal at all,
	// so the question is only asked outside one.
	if !tmux.Inside() && !onATerminal() {
		env.progressf("no terminal here, so the session is running with nobody attached%s",
			asFields(field("attach with", attachCommand(env, cfg))))
		return nil
	}

	// And on a clean restore, nothing is printed at all. The report would say
	// "opened five windows" to somebody who is about to look at five windows: the
	// session is its own report, and tmux paints over the screen either way.
	return attachTo(env, session)
}

// restoreWindows opens the base window and one window per worktree, and says
// which of them it could not open.
//
// The base window goes first, so that it is the session's first window — the one
// that keeps the session alive as worktrees come and go — and so that the status
// line reads in the order `ls` prints. The worktrees follow in the order
// repo.Managed returns them, which is slug order.
//
// A worktree whose window fails does not take the worktrees behind it down with
// it. Each failure is reported where it happened, naming the worktree, because
// that is the one thing the summary afterwards cannot say and the reader needs:
// which windows they are about to open by hand.
func restoreWindows(env *Env, cfg *config.Config, run windowCommand, managed []git.Worktree) (opened int, failed []string) {
	if _, err := openBaseWindow(env, cfg, run, leaveTheClient); err != nil {
		env.warnf("could not open the base window%s", asFields(field("tmux said", err.Error())))
		failed = append(failed, baseName)
	} else {
		opened++
	}

	for _, wt := range managed {
		// Said before the window opens, as `resume` says it: afterwards the agent
		// has the screen.
		warnIfSetupFailed(env, cfg, wt.Slug)

		if _, err := openWindow(env, cfg, tmux.Spec{
			Dir:    wt.Dir,
			Name:   cfg.WindowName(wt.Slug, ""),
			Slug:   wt.Slug,
			Branch: wt.Branch,
		}, run, leaveTheClient); err != nil {
			env.warnf("could not open a window on %s%s", wt.Slug,
				asFields(field("tmux said", err.Error())))
			failed = append(failed, wt.Slug)
			continue
		}
		opened++
	}
	return opened, failed
}

// selectBaseWindow leaves the base window current in the repository's session,
// which is where attaching then lands.
//
// tmux makes each new window current as it is created, and treewright does not
// pass -d when it opens one, so without this a restored session opens on
// whichever worktree sorted last — the window the user is least likely to have
// meant. Selecting is deliberately not focusing: tmux.Select moves no client,
// where tmux.Focus would drag an attached one here and then to the next window
// after it.
//
// The window is looked up rather than carried back out of openBaseWindow because
// it is found the same way whether this run created it or a session that was
// already up had it: by the directory it sits in.
//
// Best effort and silent. What is at stake is which window a client arrives on,
// which nothing the reader could type would change — and a base window that is
// not there at all is a failure already reported by the caller.
func selectBaseWindow(cfg *config.Config) {
	session := sessionFor(cfg)
	w, ok := tmux.Windows(session)[cfg.MainDir]
	if !ok || w.Session != session {
		// A base window in some other session is somebody else's arrangement, and
		// selecting a window there would change what that session shows.
		return
	}
	_ = tmux.Select(w)
}

// onATerminal reports whether treewright has a terminal to hand tmux.
//
// Outside tmux the attach is a foreground `tmux attach-session`, which takes
// stdin and stdout over for as long as the client stays attached and cannot take
// a pipe: it exits with "open terminal failed: not a terminal". A scripted
// restore whose author forgot -d would otherwise do every bit of the work
// correctly and then exit non-zero on the last line. So the check is
// treewright's, and what follows it is a skip that says why rather than a
// failure — the pattern ui.Picker already uses, for the same reason, one syscall
// over.
//
// Both streams are asked about, because the attach needs both: tmux reads keys
// from one and paints the screen on the other. os.Stdin and os.Stdout rather than
// env's streams for the same reason — those two are what the attach inherits,
// where Env's are buffers under test.
//
// A variable for the reason openTTY is one: attaching is the path the suite must
// never take, since a client attached to a test's session would be holding the
// developer's own terminal, with nothing left to type a detach into.
var onATerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
