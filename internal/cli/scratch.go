package cli

import (
	"fmt"
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
// Nothing about one survives its window. That is the state of affairs rather than
// a principle — see "Scratch windows" in docs/design-notes.md — and the name is
// kept as the stable handle, and .git/treewright/scratch/ kept free, so that a
// scratch session can learn to come back after a restart without either moving.

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
	if _, open := tmux.Scratch(cfg.Name)[name]; open {
		return fmt.Errorf("a scratch window called %s is already open in %s\n"+
			"pick another name for a second one, or go to it with %s",
			name, cfg.Name, hint(env, cfg, "resume", name))
	}
	return nil
}

// refuseSlugOfAScratchWindow is refuseScratchName from the other side: a slug
// `new` or `move` is about to give a worktree, already answering for an open
// scratch window.
func refuseSlugOfAScratchWindow(env *Env, cfg *config.Config, slug string) error {
	if _, open := tmux.Scratch(cfg.Name)[slug]; !open {
		return nil
	}
	return fmt.Errorf("%s is the name of a scratch window open in %s\n"+
		"send, close and resume could not tell a worktree by that name from it\n"+
		"pick another slug, or close the scratch window first with %s",
		slug, cfg.Name, hint(env, cfg, "close", slug))
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
// and no scratch window open under it.
//
// It names `scratch`, because a name typed at resume that matches no worktree is
// as likely to be a scratch window's as a mistyped slug — and it says outright
// that a scratch window which has closed has nothing to resume, since that is
// the state of things, and a reader told only "no such worktree" would go looking
// for a session that is not kept anywhere.
func nothingToResume(env *Env, cfg *config.Config, managed []git.Worktree, name string) error {
	worktrees := "none yet"
	if len(managed) > 0 {
		worktrees = strings.Join(slugsOf(managed), "\n")
	}
	// The worktree half first, since a mistyped slug is the commoner reason to
	// be here and its reader is looking for the list.
	return fmt.Errorf("no worktree %q in %s, and no scratch window open by that name\n"+
		"a scratch window keeps nothing once it closes, so a closed one has nothing to resume%s",
		name, cfg.Name, asFields(
			field("worktrees", worktrees),
			field("open a new scratch window with", hint(env, cfg, "scratch", name)),
		))
}
