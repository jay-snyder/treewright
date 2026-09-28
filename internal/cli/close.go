package cli

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// Closing a worktree's tmux window, as a verb of treewright's own.
//
// This is the last thing driving treewright required typing raw tmux for.
// `rm` and `prune` do not close a window without being asked — something may
// still be running in it — so with nobody to prompt they printed a
// `tmux kill-window -t @16` for the caller to run, and `send` named the same
// line for a window whose agent had died. Every objection that made `send` a
// command applies: the raw line honors no TREEWRIGHT_TMUX_LABEL, goes through
// no exact(), and knows nothing of the @treewright_worktree stamp — and it
// fails silently and destructively when it is wrong, since tmux closes whatever
// window id it finds on whatever server it reaches and exits 0.
//
// The window is found by the worktree's directory, the way every other window
// is. That is what makes this work after `rm`: the directory is gone from disk,
// but @treewright_worktree still holds the path, so the window answers for a
// worktree that no longer exists — which is the case the command is mostly for.
// The path is computed from the slug rather than looked up among the worktrees
// for the same reason.
//
// A scratch window is the exception in both directions. It is found by its name,
// since the window standing for its directory is the base window — and a name
// that finds no open scratch window finds nothing, since a scratch window has no
// worktree whose record could outlive it.

func cmdClose(env *Env, args []string) error {
	var repoName string
	positional, err := parseArgs("close", args, nil, repoValues(&repoName, nil), 1)
	if err != nil {
		return err
	}
	slug := at(positional, 0)
	if slug == "" {
		return usageErrorf("close", "a slug is required")
	}

	cfg, err := resolveConfig(repoName)
	if err != nil {
		return err
	}
	// Only the slug names a window; the branch prefix the user may have typed is
	// git's business, as it is everywhere but `new`.
	_, slug = splitPrefix(env, cfg, slug)

	if !tmux.Available() {
		return fmt.Errorf("tmux is not installed, so there is no window to close")
	}
	session := sessionFor(cfg)
	windows, scratch := tmux.Windows(session), tmux.Scratch(cfg.Name)
	target := closeTarget(env, cfg, slug, scratch)
	window, ok := target.Window, target.Scratch
	if !target.Scratch {
		window, ok = windows[target.Dir]
	}
	if !ok || window.ID == "" {
		// Said for every name but the base checkout's: the reader may have meant a
		// scratch window, and needs to hear that none is open by that name rather
		// than only that nothing stands on a worktree directory they never meant.
		notScratch := ""
		if !target.Base {
			notScratch = "and no scratch window by that name is open"
		}
		return fmt.Errorf("no window is open on %s in %s%s%s", target.name(), cfg.Name, under(notScratch), asFields(
			field("looked for a window on", target.Dir),
			field("open in that session now", strings.Join(openWindowNames(windows, scratch), "\n")),
		))
	}

	// Everything is said before the window goes, not after, and that ordering is
	// the whole of what this function has to get right. Closing the caller's own
	// window kills the pane treewright is running in, so there is no "afterwards"
	// to report from — and closing a session's last window can detach the client
	// that would have read it. A message that arrives only when nothing important
	// happened is not a message.
	warnIfAgentWorking(env, window)
	env.progressf("closing tmux window %s%s", window.Name, under(strings.Join(closeCosts(window), "\n")))
	if err := tmux.KillWindow(window.ID); err != nil {
		return err
	}
	return nil
}

// closeTarget works out which directory's window is meant, and what to call it
// in a message.
//
// A live worktree resolves the way `resume` and `rm` resolve one — an
// unambiguous prefix is enough and the expansion is reported. What is different
// here is the fallback: a slug matching no worktree at all is not an error but
// the ordinary case, since the command a reader reaches this through is `rm`,
// which has just deleted the worktree the window is still sitting in. So the
// directory is computed from the slug, which needs nothing to exist.
//
// The base checkout answers to its own names, as it does in the resume menu.
// Closing its window is a legitimate thing to want — it usually ends the
// repository's session, which is said rather than refused. An open scratch
// window answers to its exact name next, as it does in chooseWorktree and for
// its reasons.
func closeTarget(env *Env, cfg *config.Config, slug string, scratch map[string]tmux.Window) choice {
	if base := baseChoice(cfg); slices.Contains(baseNames(cfg, base), slug) {
		return base
	}
	if w, ok := scratch[slug]; ok {
		return scratchChoice(cfg, w)
	}
	if managed, err := repoFor(cfg).Managed(); err == nil {
		// resolveSlug reports the expansion and errors when nothing matches; only
		// the match is wanted here, the miss being the removed-worktree case.
		if wt, err := resolveSlug(env, cfg, managed, slug); err == nil {
			return choice{Worktree: wt}
		}
	}
	return choice{Worktree: git.Worktree{Slug: slug, Dir: cfg.DirFor(slug)}}
}

// agentWorkingNote is the caveat about closing a window whose agent says it is
// working, as a format taking the window's name. A constant because it is said
// in two registers — warnIfAgentWorking prints it as a warning, and rm's prompt
// writes it to the tty above its question — and the two must stay one sentence.
const agentWorkingNote = "the agent in %s says it is working\nit is the window's command, so closing the window stops it"

// warnIfAgentWorking says so before a window closes with an agent still working
// in it.
//
// The agent is the window's command, so there is no detaching from this and
// coming back: closing the window stops the work and takes the session with it.
// That is the one thing about a window treewright knows and the caller may not,
// since the state comes from the agent's own hooks rather than from anything
// visible in the window's name.
//
// It warns rather than refuses. The caller asked to close this window, and
// treewright is in no position to judge whether what the agent is doing still
// matters — a refusal would need a --force to get past, which is a flag people
// learn to pass by reflex. What a warning buys is that the loss is on the record
// at the moment it happens rather than discovered later, and that is also why it
// is said before the window goes: afterwards there may be no session left to say
// it in.
//
// Only `working` warns. `waiting` is an agent blocked on a person and `done` is
// one with nothing in flight — those are the states an ordinary teardown closes,
// and a warning that fires on the ordinary case is one that stops being read.
func warnIfAgentWorking(env *Env, window tmux.Window) {
	if window.State != stateWorking {
		return
	}
	env.warnf(agentWorkingNote, window.Name)
}

// closeCosts lists what closing this window will take with it, one per line.
//
// Both of them change what a reader should do rather than merely describing the
// window, which is why they are said at all: a session ending moves or detaches
// whoever was attached to it, and the caller's own window ending means this is
// the last thing that happens in this session — the ordering the agent guide
// asks for, made visible at the moment it applies.
func closeCosts(window tmux.Window) []string {
	var costs []string
	if note := lastInSessionNote(window); note != "" {
		costs = append(costs, note)
	}
	if window.ID == tmux.CurrentWindow() {
		costs = append(costs, "it is the window this command is running in, so nothing after this runs")
	}
	return costs
}

// openWindowNames lists the windows treewright can see, for the error about one
// it cannot find. Sorted, since a map's order would make the same repository
// answer differently each time.
//
// The scratch windows are listed beside the rest: they claim no directory, so the
// first map never holds them, and a list of what is open that left them out
// would be wrong about the very windows a reader of this error may have meant.
func openWindowNames(windows, scratch map[string]tmux.Window) []string {
	seen := make(map[string]bool, len(windows)+len(scratch))
	var names []string
	for _, w := range slices.Concat(slices.Collect(maps.Values(windows)), slices.Collect(maps.Values(scratch))) {
		if w.Name == "" || seen[w.ID] {
			continue
		}
		seen[w.ID] = true
		names = append(names, w.Name)
	}
	if len(names) == 0 {
		return []string{"nothing — no tmux window is open"}
	}
	sort.Strings(names)
	return names
}

// closeHint is how the commands that leave a window behind say to close it: a
// treewright command rather than the `tmux kill-window` they used to print.
//
// Named once because `rm`, `prune` and `send` all reach for it, and built by
// hint so it carries the repository — a stale window is the case where naming
// only the slug is least defensible, since the worktree that would have
// disambiguated it has just been deleted.
func closeHint(env *Env, cfg *config.Config, slug string) string {
	return hint(env, cfg, "close", slug)
}
