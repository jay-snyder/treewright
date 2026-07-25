package cli

import (
	"fmt"
	"strings"

	"github.com/jay-snyder/treemux/internal/config"
	"github.com/jay-snyder/treemux/internal/shellinit"
)

// cmdInit prints the shell integration for the named shell.
func cmdInit(env *Env, args []string) error {
	positional, err := parseArgs("shell-init", args, nil, 1)
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
	case "slugs":
		cfg, err := resolveConfig("")
		if err != nil {
			return nil
		}
		managed, err := repoFor(cfg).Managed()
		if err != nil {
			return nil
		}
		for _, wt := range managed {
			fmt.Fprintln(env.Stdout, wt.Slug)
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
