package cli

import (
	"fmt"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/shellinit"
	"github.com/jay-snyder/treewright/internal/tmux"
	"github.com/jay-snyder/treewright/internal/tmuxinit"
)

// cmdInit prints the shell integration for the named shell.
func cmdInit(env *Env, args []string) error {
	positional, err := parseArgs("shell-init", args, nil, nil, 1)
	if err != nil {
		return err
	}
	shell := at(positional, 0)
	if shell == "" {
		return usageErrorf("shell-init", "a shell is required (one of: %s)", strings.Join(shellinit.Shells(), ", "))
	}
	script, err := shellinit.Script(shell)
	if err != nil {
		return usageErrorf("shell-init", "%v", err)
	}
	fmt.Fprint(env.Stdout, script)
	return nil
}

// cmdTmuxInit prints the tmux integration, or loads it into the running server.
//
// Printing is the default for a reason the shell side does not have: a shell
// integration is a function you call, while this is a set of key bindings that
// change what your existing keys do. That is worth reading before it is loaded.
//
// --apply exists because tmux has no eval "$(...)" — there is no way to write one
// line in tmux.conf that both fetches the snippet and runs it. With it, the line
// is `run-shell 'treewright tmux-init --apply'`, and what the server gets is byte for
// byte what printing produces.
//
// The keys are flags rather than settings in a repo's config, because a key
// binding belongs to a tmux server and not to a repository: one treewright config
// per repo would give several answers to a question that has one. As flags they
// also work identically down both routes — a --apply line in tmux.conf can carry
// them, where editing the printed file could not.
func cmdTmuxInit(env *Env, args []string) error {
	var apply bool
	keys := tmuxinit.DefaultKeys()
	if _, err := parseArgs("tmux-init", args,
		map[string]*bool{"--apply": &apply},
		map[string]*string{"--resume-key": &keys.Resume, "--new-key": &keys.New},
		0); err != nil {
		return err
	}
	// Checked before anything is printed or loaded, so a bad key is one message
	// naming the flag rather than tmux's own complaint about a line of config the
	// user never wrote.
	if err := keys.Validate(); err != nil {
		return usageErrorf("tmux-init", "%v", err)
	}

	if !apply {
		fmt.Fprint(env.Stdout, tmuxinit.Script(keys))
		return nil
	}
	if !tmux.Available() {
		return fmt.Errorf("tmux is not installed, so there is nothing to load the integration into")
	}
	if err := tmux.Source(tmuxinit.Script(keys)); err != nil {
		// Much the likeliest cause, and not otherwise obvious: key bindings live in
		// a server, so with none running there is nowhere for them to go. From
		// tmux.conf this cannot happen — the server is what is reading the file.
		return fmt.Errorf("%w — is a tmux server running? key bindings live in one", err)
	}
	env.progressf("loaded the treewright key bindings into the running tmux server")
	return nil
}

// cmdComplete backs tab completion in every shell, so the candidate lists live
// here once instead of being reimplemented in three shell dialects.
//
// It never reports an error and never writes to stderr: completion runs while
// the user is mid-keystroke, and a diagnostic printed there would corrupt their
// prompt. An unresolvable request simply offers nothing.
func cmdComplete(env *Env, args []string) error {
	// No arity check here: completion must never fail, and an unrecognized
	// request simply offers nothing.
	switch at(args, 0) {
	// Two lists, differing by one name, because the commands that ask differ by
	// what they can do. "slugs" is what rm completes: the worktrees, which are
	// what it can remove. "targets" is what resume and cd complete: the same
	// worktrees plus the base checkout, which they can reach and rm cannot.
	// Offering "base" to rm would be completing a word that only ever errors.
	case "slugs", "targets":
		cfg, err := resolveConfig("")
		if err != nil {
			return nil
		}
		managed, err := repoFor(cfg).Managed()
		if err != nil {
			return nil
		}
		if at(args, 0) == "targets" {
			// Just the one spelling, though resume and cd also answer to the base
			// branch: completion is for finding a name, and a list offering the
			// same row twice makes it harder, not easier.
			fmt.Fprintln(env.Stdout, "base")
		}
		for _, wt := range managed {
			fmt.Fprintln(env.Stdout, wt.Slug)
		}
	// What `new` completes, in a repo that namespaces branches by kind of work: the
	// prefixes are a convention nothing else surfaces, and a list you cannot see is
	// one you misspell. The slugs are deliberately not offered — `new`'s argument is
	// a name that does not exist yet, and completing the ones that do would suggest
	// the opposite.
	case "prefixes":
		cfg, err := resolveConfig("")
		if err != nil {
			return nil
		}
		// A single prefix is not a choice, and offering it would put a word the user
		// never has to type in front of every new slug.
		if prefixes := cfg.Prefixes(); len(prefixes) > 1 {
			for _, p := range prefixes {
				// The empty prefix is a legitimate entry and completes to nothing:
				// a blank candidate is a blank row in the menu.
				if p == "" {
					continue
				}
				fmt.Fprintln(env.Stdout, p)
			}
		}
	case "repos":
		names, err := config.Names()
		if err != nil {
			return nil
		}
		for _, name := range names {
			fmt.Fprintln(env.Stdout, name)
		}
	case "shells":
		for _, shell := range shellinit.Shells() {
			fmt.Fprintln(env.Stdout, shell)
		}
	case "flags":
		// Derived from the same table that renders help, so a flag can never be
		// documented in one place and missing from the other.
		cmd := lookup(at(args, 1))
		if cmd == nil {
			return nil
		}
		for _, f := range cmd.flags {
			for _, name := range strings.Split(f.names, ",") {
				fmt.Fprintln(env.Stdout, strings.TrimSpace(name))
			}
		}
	}
	return nil
}
