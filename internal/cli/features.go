package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/feature"
	"github.com/jay-snyder/treewright/internal/git"
)

// `session-start` is the moment a repository's optional features run at: an
// agent's own hooks run it when a session begins, and every feature the config
// switched on gets its turn. It is `signal`'s discipline applied to a different
// question — silent everywhere it has no business, loud only when invoked wrong
// — and for the same reason, since the same hooks fire in every session the
// agent has, most of them in repositories treewright has never heard of.
//
// One verb rather than one per feature, and this is the decision the shape
// rests on. The wiring lives in a plugin copy on somebody's disk: installed
// once, carried into worktrees as a snapshot, and rewritten only when its owner
// runs `agent-init` or `refresh`. A verb per feature would mean every feature
// added after that copy was made is a hook that copy does not have — enabled in
// the config, silent in practice, with nothing to say so. One dispatch verb
// makes the plugin's line a constant: a feature shipped two releases from now
// runs in wiring installed today.
//
// The other half of that decision is that the toggle cannot live in the hook.
// The plugin's default placement is user-level, one copy covering every
// repository, so `features` is read here at the moment the hook fires rather
// than baked into the JSON at install time. That is what lets one plugin serve
// a repository that asked for this and a repository that did not.

// cmdSessionStart runs whichever of the repository's features are due at the
// start of an agent session, and prints what each of them did.
//
// What it prints goes to stdout, which for this command is the answer in the
// output contract's own terms: the consumer is a program, and the agent's
// SessionStart hook adds a hook's stdout to the session as context. A warning
// on stderr would be read by the human in a transcript and by nothing else —
// and the whole point of the one feature there is is telling an agent that the
// checkout under it just moved.
//
// Being invoked wrong is the only loud failure, as it is for `signal`.
// Everything else — no registered config, no feature switched on, a feature
// with nothing to do — exits 0 in silence. A hook that narrated its own
// no-op would narrate it in every session in every repository on the machine.
func cmdSessionStart(env *Env, args []string) error {
	if _, err := parseArgs("session-start", args, nil, nil, 0); err != nil {
		return err
	}
	cfg, err := resolveConfig("")
	if err != nil {
		//nolint:nilerr // out of scope, not a fault: agent hooks fire in repositories treewright has never heard of
		return nil
	}
	for _, f := range feature.At(feature.AtSessionStart) {
		if !cfg.Enabled(f.Name) {
			continue
		}
		run, ok := sessionStartFeatures[f.Name]
		if !ok {
			continue
		}
		if said := run(cfg); said != "" {
			fmt.Fprintln(env.Stdout, said)
		}
	}
	return nil
}

// sessionStartFeatures is what each feature that runs at session start
// actually does. A feature returns the line it wants said, or "" for the
// silence that means "nothing happened here".
//
// The vocabulary is in internal/feature and the behavior is here, because a
// feature is made of git calls and internal/config — which validates the names
// — must not depend on git. What holds the two halves together is
// TestEveryFeatureHasAnImplementation rather than a shared interface: the
// coupling is a list on each side, and a test that names the missing half is a
// better failure than a method signature nobody can implement wrong.
var sessionStartFeatures = map[string]func(*config.Config) string{
	feature.FreshBase: freshBase,
}

// freshBase fast-forwards the base checkout when an agent session starts in it
// and the branch has only fallen behind.
//
// Everything before the fetch is a scope check, and all of them are silences.
// The narrow scope is the point rather than a limitation:
//
//   - Only from the base checkout. A session in a worktree is an agent working
//     somewhere else, and moving a checkout it is not standing in is the very
//     thing `guard` refuses on its behalf — worse here, since the base checkout
//     may have an agent of its own with work in flight.
//   - Only on base_branch. The base checkout is the one place a person switches
//     branches by hand, and a checkout parked somewhere else is parked there
//     deliberately.
//   - Only with an origin to ask. A repository with no remote has nothing to be
//     behind, and `ls` already treats offline as a state rather than a fault.
//
// The fetch failing is the one out-of-scope-looking case that speaks, because
// it is not out of scope: the session is in a repository that asked for this,
// and the honest answer is that freshness is now unknown. Saying nothing there
// would be indistinguishable from saying "you are current".
func freshBase(cfg *config.Config) string {
	if !inTheBaseCheckout(cfg) {
		return ""
	}
	repo := repoFor(cfg)
	branch, err := git.CurrentBranch(cfg.MainDir)
	if err != nil || branch != cfg.BaseBranch {
		return ""
	}
	if !repo.HasRemote("origin") {
		return ""
	}
	// FetchRetrying, as `new` does: this is the second of the two places where a
	// fetch that failed once leaves the caller acting on something unverified
	// rather than merely waiting longer for it.
	if err := repo.FetchRetrying("origin", cfg.BaseBranch); err != nil {
		return fmt.Sprintf("could not reach origin, so whether %s is current here is unknown\n"+
			"check before relying on anything you read in this checkout", cfg.BaseBranch)
	}
	// AheadBehind measures against origin/<base> itself, which is what makes the
	// count worth printing: it is the number the person would have got from a
	// `git status` they did not run.
	_, behind, ok := repo.AheadBehind(branch, cfg.BaseBranch)
	if !ok || behind == 0 {
		return ""
	}
	if err := repo.FastForward("origin/" + cfg.BaseBranch); err != nil {
		return fmt.Sprintf("%s here is %s behind origin/%s and could not be fast-forwarded\n"+
			"it has diverged, or local changes are in the way\n"+
			"bring it up to date before starting work in this checkout",
			branch, count(behind, "commit", "commits"), cfg.BaseBranch)
	}
	return fmt.Sprintf("%s here was %s behind origin/%s and has been fast-forwarded",
		branch, count(behind, "commit", "commits"), cfg.BaseBranch)
}

// inTheBaseCheckout reports whether the caller is standing in the config's main
// checkout rather than in one of its worktrees.
//
// Two different questions are being asked of git and both are needed.
// resolveConfig finds the config by the repository's *main* directory, so it
// answers the same from a worktree as from the base checkout — which is what
// makes a hook in a worktree find the right config at all. TopLevel is the
// caller's own checkout root, which is what tells the two apart. Comparing
// git's spelling to the config's works because Load canonicalizes main_dir, so
// a repository reached through a symlink still matches.
func inTheBaseCheckout(cfg *config.Config) bool {
	wd, err := os.Getwd()
	if err != nil {
		return false
	}
	top, err := (git.Repo{Dir: wd}).TopLevel()
	if err != nil {
		return false
	}
	return top == cfg.MainDir
}

// featureHelp lists the registry for `session-start`'s help, rather than the
// help prose naming the features itself.
//
// The list in a command's help is the one place a person goes looking for what
// they may switch on, and a hand-written copy of it is a copy that falls behind
// the first time a feature is added — enabled-and-undocumented being the state
// this whole shape exists to avoid. Rendered from feature.All(), a feature
// documents itself in the help of the command that runs it.
// TestHelpNamesEveryFeature holds the other direction, since a renderer that
// stopped being called would leave the prose passing its own indentation check.
func featureHelp() string {
	width := 0
	for _, f := range feature.All() {
		width = max(width, len(f.Name))
	}
	var b strings.Builder
	for _, f := range feature.All() {
		fmt.Fprintf(&b, "    %-*s  %s\n", width, f.Name, f.Summary)
	}
	return b.String()
}
