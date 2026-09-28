package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/refname"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// A scratch window: a second agent session on a repository, standing in the base
// checkout, with no worktree of its own.
//
// The base window is one window by design — `base` finds the window standing in
// the main checkout and switches to it — and that is the right answer to "take
// me to the base window" and the wrong one to "I need another agent here, for
// something that will commit nothing". Investigation, a question, a review, an
// agent orchestrating the others: none of it wants a branch, and a throwaway
// worktree per question is a teardown chore invented for nothing.
//
// What makes a worktree expensive is the disk, not the identity. Until now the
// two were one thing, since @treewright_worktree was both where a window stood
// and what it was. A scratch window pulls them apart: a worktree's window is
// identified by its worktree, and a scratch window is identified by itself — the
// name it was opened under, recorded on it as @treewright_scratch. It claims no
// directory at all, which is what keeps every command that asks for the window
// on the main checkout answering with the base window. See parsePanes.
//
// The name is required rather than generated. Every other command that creates
// something takes a name, and an addressable thing should be named by whoever is
// going to address it: an "ask-1" that has to be looked up before it can be sent
// to is worse than typing a word.
//
// Its conversation survives its window. The agent's own SessionStart hook
// records it under the name, and resume and restore reopen the window on that
// conversation after a restart takes it — the name being the stable handle that
// always made that possible. The same record is kept for the base window, whose
// --continue would otherwise pick up a scratch session's conversation, a
// scratch session being the most recent conversation in the directory as often
// as not. `close` ends one, and so does quitting its agent; see sessions.go for
// why nothing else does.

func cmdScratch(env *Env, args []string) error {
	var prompt, promptFile, repoName string
	positional, err := parseArgs("scratch", args, nil, repoValues(&repoName, promptValues(&prompt, &promptFile)), 2)
	if err != nil {
		return err
	}
	name, override := at(positional, 0), at(positional, 1)
	if name == "" {
		return usageErrorf("scratch", "a name is required\nit is how send, close and resume will reach the window")
	}
	// A slug's rules, so that what can be typed here is what can be typed at
	// `new`. The reasons refname gives are a worktree's, and the second line says
	// why they apply to a window that has none.
	if err := refname.CheckSlug(name); err != nil {
		return usageErrorf("scratch", "%v\na scratch name follows a slug's rules, since send, close and resume take either", err)
	}

	cfg, err := resolveConfig(repoName)
	if err != nil {
		return err
	}
	if err := refuseScratchName(env, cfg, name); err != nil {
		return err
	}
	window := cfg.WindowName(name, override)
	warnIfWindowNameIsARepo(env, cfg, window)
	// command, never resume_command: there is nothing to resume in a window that
	// did not exist a moment ago. Filled here, before anything opens, so the
	// placeholder and length refusals are the ones `new` gives.
	prompt, err = resolvePrompt("scratch", prompt, promptFile)
	if err != nil {
		return err
	}
	command, err := fillPrompt(cfg.Command, "command", prompt)
	if err != nil {
		return err
	}
	run := windowCommand{Command: command}

	warnIfBaseIsElsewhere(env, cfg)
	if !tmux.Available() {
		_, err := runInTheBaseCheckout(env, cfg, run)
		return err
	}
	_, err = openWindow(env, cfg, tmux.Spec{
		Dir:     cfg.MainDir,
		Name:    window,
		Scratch: name,
	}, run, arrivalFor(cfg, repoName))
	return err
}

// refuseScratchName refuses a name that something else already answers to.
//
// send, close and resume take a worktree's slug and a scratch window's name
// alike, so the two share one namespace, and a name that meant two things would
// leave `send ask` unable to say which it had reached. The collision is refused
// when a name is given out — here, and in `new` by refuseSlugOfAScratchWindow —
// rather than detected at every lookup. The alternative was a sigil marking
// scratch names, which is punctuation typed for the life of the tool to settle a
// question that only arises once.
//
// The base checkout's names are refused too. They win every lookup, so a scratch
// window called "base" could be opened and never reached.
//
// So is the name of a recorded scratch session whose window is gone. It is still
// that session's name — resume reopens it by that name, and restore will — so a
// second window under it would be a second session answering to one word, and
// the first time its agent started, its record would overwrite the one it
// collided with.
func refuseScratchName(env *Env, cfg *config.Config, name string) error {
	if slices.Contains(baseNames(cfg, baseChoice(cfg)), name) {
		return fmt.Errorf("%q already names the base checkout in %s\n"+
			"send, close and resume would reach the base window by it, and never this one\n"+
			"pick another name", name, cfg.Name)
	}
	if managed, err := repoFor(cfg).Managed(); err == nil && slices.ContainsFunc(managed, func(wt git.Worktree) bool {
		return wt.Slug == name
	}) {
		return fmt.Errorf("%s is already a worktree in %s\n"+
			"send, close and resume could not tell a scratch window by that name from it\n"+
			"pick another name, or reach the worktree's agent with %s",
			name, cfg.Name, hint(env, cfg, "resume", name))
	}
	if w, known := scratchSessions(cfg)[name]; known {
		if w.ID != "" {
			return fmt.Errorf("a scratch window called %s is already open in %s\n"+
				"pick another name for a second one, or go to it with %s",
				name, cfg.Name, hint(env, cfg, "resume", name))
		}
		return fmt.Errorf("a scratch session called %s is recorded in %s, waiting to be reopened\n"+
			"its window went, but its conversation did not%s",
			name, cfg.Name, asFields(
				field("reopen it with", hint(env, cfg, "resume", name)),
				field("or end it with", hint(env, cfg, "close", name)),
			))
	}
	return nil
}

// refuseSlugOfAScratchWindow is refuseScratchName from the other side: a slug
// `new` or `move` is about to give a worktree, already answering for a scratch
// session — open, or recorded and waiting to be reopened.
func refuseSlugOfAScratchWindow(env *Env, cfg *config.Config, slug string) error {
	w, known := scratchSessions(cfg)[slug]
	if !known {
		return nil
	}
	what := "a scratch window open"
	if w.ID == "" {
		what = "a scratch session recorded"
	}
	return fmt.Errorf("%s is the name of %s in %s\n"+
		"send, close and resume could not tell a worktree by that name from it\n"+
		"pick another slug, or end the scratch session first with %s",
		slug, what, cfg.Name, hint(env, cfg, "close", slug))
}

// namesNoWorktree reports that no worktree answers to a name, even as a prefix —
// the miss resolveSlug reports, as distinct from a prefix too ambiguous to pick
// from.
func namesNoWorktree(managed []git.Worktree, name string) bool {
	return !slices.ContainsFunc(managed, func(wt git.Worktree) bool {
		return strings.HasPrefix(wt.Slug, name)
	})
}

// nothingToResume is `resume`'s answer to a name nothing answers to: no worktree,
// and no scratch session under it, open or recorded.
//
// It names `scratch`, because a name typed at resume that matches no worktree is
// as likely to be a scratch window's as a mistyped slug — and it says which
// scratch sessions are not kept, since a reader told only "no such worktree"
// would go looking for a session that is not recorded anywhere. Which those are
// depends on whether this repository records sessions at all.
func nothingToResume(env *Env, cfg *config.Config, managed []git.Worktree, name string) error {
	worktrees := "none yet"
	if len(managed) > 0 {
		worktrees = strings.Join(slugsOf(managed), "\n")
	}
	gone := "a scratch session ended by quitting its agent, or by close, has nothing to resume"
	if !sessionsKept(cfg) {
		gone = "this repository's agent is not resumed by conversation, so a closed scratch window has nothing to resume"
	}
	// The worktree half first, since a mistyped slug is the commoner reason to
	// be here and its reader is looking for the list.
	return fmt.Errorf("no worktree %q in %s, and no scratch session by that name\n%s%s",
		name, cfg.Name, gone, asFields(
			field("worktrees", worktrees),
			field("open a new scratch window with", hint(env, cfg, "scratch", name)),
		))
}

// agentWorkingBeside finds another agent at work in the base checkout: a window
// standing on it, other than the caller's own, whose agent reports `working`.
//
// Two agents could not share the base checkout before scratch windows, and the
// two things treewright does that rewrite files in it — fresh-base
// fast-forwarding it, `move` clearing it — were written for one. Both now ask
// this first, since each changes files under whatever agent is standing there.
//
// The windows standing on it are the base window and the scratch windows. The
// caller's own is left out: it is the agent asking — the one running `move`, or
// the session now starting, whose last reported state belongs to the session
// that just ended. Outside tmux the caller has no window, so every window on the
// checkout counts.
//
// Only `working`, as warnIfAgentWorking has it: `waiting` and `done` are agents
// with nothing in flight, and an agent in flight is the one a moving checkout
// can hurt.
func agentWorkingBeside(cfg *config.Config) (tmux.Window, bool) {
	own := tmux.CurrentWindow()
	var standing []tmux.Window
	if w, ok := tmux.Windows(sessionFor(cfg))[cfg.MainDir]; ok {
		standing = append(standing, w)
	}
	scratch := tmux.Scratch(cfg.Name)
	for _, name := range slices.Sorted(maps.Keys(scratch)) {
		standing = append(standing, scratch[name])
	}
	for _, w := range standing {
		if w.ID != own && w.State == stateWorking {
			return w, true
		}
	}
	return tmux.Window{}, false
}
