package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jay-snyder/treewright/internal/agentinit"
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

// cmdAgentInit installs an agent's plugin: the hooks wired to `treewright
// signal`, which fill the AGENT column of ls, and the skill teaching the agent
// to drive treewright — the same knowledge in the other direction, in one
// directory the agent loads whole.
//
// This one writes, where its two siblings print, and the reason is that the
// directory is treewright's own. `shell-init` and `tmux-init` print because
// their output belongs in a file the user owns and has edited fifty times, and
// so did the hooks, back when they were a JSON fragment for a settings file —
// applying that would have meant a merge that reordered somebody else's file,
// which is why it never applied. The plugin has no such problem: nothing else
// lives in `.claude/skills/treewright/`, treewright named it, and treewright
// is the only thing that will ever write there. The half-measure this replaces
// was already a write in all but name — `--skill` printed a `mkdir -p ... &&
// treewright ... > ...` one-liner for the user to paste back, and with three
// files that recipe stops being reviewable and starts being a way to install a
// plugin with one file missing.
//
// Writing is also what makes a second run mean something. A pasted fragment
// could only ever be layered on: rename a signal verb and the old copy stays
// where it was, wired to a word that no longer exists. Rewriting the files
// treewright wrote makes agent-init an update rather than a second install.
//
// stdout is the directory, so `cd "$(treewright agent-init claude)"` and
// friends work, with everything else on stderr — the same contract as every
// other command. --print writes nothing and dumps the files instead, for
// reading the hooks before they run, which is the reason tmux-init prints by
// default.
//
// The default placement is the agent's own user-level directory, and --local is
// what confines it to one repository. Per-repo was the default for a while and
// the argument for it — treewright is a tool you use in some repositories and
// not others — turned out to describe the config registry rather than the
// wiring: a repository treewright does not manage has no window for `signal` to
// find, so the hooks there cost nothing and say nothing. What per-repo costs is
// real and recurs, since that directory is one every worktree starts without:
// the plugin reaches the MAIN window and nowhere else until `agent = "claude"`
// carries it, which is a second step nobody is told about until doctor says so.
// One install that covers every checkout, made and to be made, has no such
// gap. --local is still there for a machine where treewright's wiring should
// touch one repository and no other. See "Where the wiring goes" in
// docs/agents.md.
func cmdAgentInit(env *Env, args []string) error {
	var local, dump bool
	positional, err := parseArgs("agent-init", args,
		map[string]*bool{"--local": &local, "--print": &dump}, nil, 1)
	if err != nil {
		return err
	}
	name := at(positional, 0)
	if name == "" {
		return usageErrorf("agent-init", "an agent is required (one of: %s)", strings.Join(agentinit.Names(), ", "))
	}
	agent, ok := agentinit.Lookup(name)
	if !ok {
		return usageErrorf("agent-init", "no module for %q (built-in modules: %s)", name, strings.Join(agentinit.Names(), ", "))
	}

	if dump {
		return dumpPlugin(env, agent, local)
	}
	if !local {
		return installAgentPlugin(env, agent, agent.UserPluginDir(), nil)
	}
	// The repository decides where a per-repo plugin goes, so this is the one
	// path that needs a config — and having one is what lets the carry warning
	// below know whether it applies. Someone with no config yet still has the
	// default placement and --print, and the error says so.
	cfg, err := resolveConfig("")
	if err != nil {
		return fmt.Errorf("%w%s", err, asFields(
			field("or cover every repository", env.copyable(env.Argv0+" agent-init "+agent.Name)),
		))
	}
	return installAgentPlugin(env, agent, filepath.Join(cfg.MainDir, filepath.FromSlash(agent.ProjectPlugin)), cfg)
}

// installAgentPlugin writes the plugin into dir and says what happened. A nil
// cfg is the default placement, where nothing has to be carried anywhere: the
// agent's own user-level directory already covers whatever directory it is
// started in.
func installAgentPlugin(env *Env, agent agentinit.Agent, dir string, cfg *config.Config) error {
	written, err := agent.Install(dir)
	if err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, dir)

	// Named rather than counted, because the interesting run is the second one:
	// "wrote hooks/hooks.json" after an upgrade says exactly which part of the
	// wiring had gone stale, where "wrote 1 file" says only that something did.
	// Set out one per line, since the names are what the run is worth reading
	// for and comma-joining them put three of them after an absolute path.
	if len(written) == 0 {
		env.progressf("the %s plugin is already up to date%s", agent.Name, asFields(field("directory", dir)))
	} else {
		env.progressf("wrote the %s plugin%s", agent.Name, asFields(
			field("directory", dir),
			field("files", strings.Join(written, "\n")),
		))
	}
	// Three facts about when the wiring starts working, and they were one line
	// of ninety-odd columns: it loads at the next start, you have to trust the
	// folder first, and there is a way to skip the wait in a session that is
	// already running. Whole clauses rather than the fragments the split first
	// left behind — "once you have trusted the folder" on a line of its own is
	// a sentence with its subject three lines up.
	env.progressf("%s loads it as treewright@skills-dir the next time it starts\n"+
		"you will need to trust the folder first\n"+
		"in a session that is already open, run %s",
		agent.Name, env.copyable("/reload-plugins"))

	// Said in that order because the second line is what makes the first one
	// cheap: covering a repository treewright knows nothing about sounds like
	// more than it is, there being no window there for `signal` to find. The
	// alternative comes last, as the only line here anyone types back.
	if cfg == nil {
		env.progressf("every repository is covered now, treewright-managed or not\n"+
			"outside a treewright window, signal does nothing and says nothing\n"+
			"to wire up one repository and no other, run %s",
			env.copyable(env.Argv0+" agent-init "+agent.Name+" --local"))
		return nil
	}
	if cfg.Agent != agent.Name {
		env.progressf("no worktree gets a copy until the config says which agent this is%s", asFields(
			field("add to "+cfg.Path(), env.copyable(fmt.Sprintf("agent = %q", agent.Name))),
		))
	}
	// Said unconditionally rather than after a `git check-ignore`: the answer
	// costs a git call to learn and the sentence is worth reading either way.
	// The agent's settings file is conventionally ignored already; this path is
	// treewright's own invention, so nothing ignores it until someone says so,
	// and a committed plugin is treewright imposed on everyone who clones.
	//
	// One message, not two: the caveat is the second half of the same thought,
	// and as its own progressf it started a new block at the margin — which
	// reads as a new subject rather than as the rest of this one.
	env.progressf("git will not ignore the plugin on its own\n"+
		"add %s to .gitignore\n"+
		"unless you mean to commit it for everyone who clones",
		env.copyable(agent.ProjectPlugin+"/"))
	return nil
}

// dumpPlugin prints the plugin's files rather than installing them, each under
// the path it would be written to. The headers are cat's own multi-file form,
// because that is what the output is: several files at once, for reading.
func dumpPlugin(env *Env, agent agentinit.Agent, local bool) error {
	dir := agent.UserPluginDir()
	if local {
		dir = agent.ProjectPlugin
	}
	for i, f := range agent.Plugin {
		if i > 0 {
			fmt.Fprintln(env.Stdout)
		}
		fmt.Fprintf(env.Stdout, "==> %s/%s <==\n", dir, f.Path)
		fmt.Fprint(env.Stdout, f.Body)
	}
	env.progressf("nothing was written%s", asFields(
		field("to install it for every repository", env.copyable(env.Argv0+" agent-init "+agent.Name)),
		field("for this repository alone", env.copyable(env.Argv0+" agent-init "+agent.Name+" --local")),
	))
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
	// what it can remove. "targets" is what resume, cd and send complete: the
	// same worktrees plus the base checkout, which they can reach and rm cannot.
	// Offering "base" to rm would be completing a word that only ever errors.
	case "slugs", "targets":
		cfg, err := resolveConfig("")
		if err != nil {
			return nil //nolint:nilerr // completion never fails; it offers nothing
		}
		managed, err := repoFor(cfg).Managed()
		if err != nil {
			return nil //nolint:nilerr // completion never fails; it offers nothing
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
			return nil //nolint:nilerr // completion never fails; it offers nothing
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
			return nil //nolint:nilerr // completion never fails; it offers nothing
		}
		for _, name := range names {
			fmt.Fprintln(env.Stdout, name)
		}
	case "shells":
		for _, shell := range shellinit.Shells() {
			fmt.Fprintln(env.Stdout, shell)
		}
	// What `signal` completes: its closed vocabulary. Typed rarely — hooks are
	// the callers — but the list living here keeps the shims from each spelling
	// out a vocabulary that internal/cli owns.
	case "states":
		for _, state := range signalStates {
			fmt.Fprintln(env.Stdout, state)
		}
	case "agents":
		for _, name := range agentinit.Names() {
			fmt.Fprintln(env.Stdout, name)
		}
	case "flags":
		// Derived from the same table that renders help, so a flag can never be
		// documented in one place and missing from the other.
		cmd := lookup(at(args, 1))
		if cmd == nil {
			return nil
		}
		for _, f := range cmd.flags {
			for name := range strings.SplitSeq(f.names, ",") {
				fmt.Fprintln(env.Stdout, strings.TrimSpace(name))
			}
		}
	}
	return nil
}
