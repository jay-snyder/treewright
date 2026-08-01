package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jay-snyder/treewright/internal/agentinit"
	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/shellinit"
	"github.com/jay-snyder/treewright/internal/tmux"
	"github.com/jay-snyder/treewright/internal/tmuxinit"
)

// The command to run after upgrading treewright.
//
// Three integrations propagate an upgrade on their own and one does not. The
// shell shim is re-evaluated at every shell start; the tmux snippet is re-read
// at every server start; the agent plugin in the main checkout is rewritten by
// the next `agent-init`. What none of that reaches is a worktree: its copy of
// the plugin is made once, when `new` carries it in, and nothing has looked at
// it since — so a worktree created before an upgrade runs its agent against the
// old hooks and the old skill for as long as the worktree lives, which is the
// same frozen-copy problem the plugin was invented to abolish, one directory
// further down.
//
// The other two are here because "after upgrading, run this" is worth being one
// command rather than three, and because a tmux server routinely outlives an
// upgrade by weeks with nobody noticing. What is deliberately not here is the
// shell: a wrapper function lives in the shell that loaded it, and no child
// process can replace its parent's. That one is said rather than done.
//
// It refreshes and does not install. A checkout with no plugin in it is left
// alone unless the config carries one, the tmux bindings are reloaded only into
// a server that already holds some, and the keys they go back on are the keys
// they are already on. The difference matters because this is the command
// people will run without reading it: `agent-init` and `tmux-init` are how you
// decide what treewright touches, and refreshing must never be how that decision
// gets made for you.
func cmdRefresh(env *Env, args []string) error {
	positional, err := parseArgs("refresh", args, nil, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(at(positional, 0))
	if err != nil {
		return err
	}

	refreshAgentPlugin(env, cfg)
	refreshTmuxBindings(env)
	reportStaleShell(env)
	return nil
}

// checkout is one directory the plugin is refreshed in, under the name the user
// knows it by — "base" for the main checkout, the slug for a worktree, the path
// itself for the user-level install, which belongs to no repository.
type checkout struct {
	label string
	dir   string
}

// refreshAgentPlugin rewrites the module's plugin wherever this repository keeps
// it, and reports what moved in each place.
//
// The report names files rather than counting them, the way `agent-init` does
// and for the same reason: after an upgrade, "wrote hooks/hooks.json" says which
// part of the wiring had gone stale, where "updated 6 checkouts" says only that
// something did. Checkouts that were already current are left out of it — a list
// where most rows say nothing happened is a list nobody reads to the end of.
func refreshAgentPlugin(env *Env, cfg *config.Config) {
	module, ok := agentModuleFor(cfg)
	if !ok || len(module.Plugin) == 0 {
		env.progressf("no agent module applies to %q, so there is no plugin to refresh%s", cfg.Name,
			asFields(field("set in the config", env.copyable(`agent = "claude"`))))
		return
	}

	targets := pluginCheckouts(env, cfg, module)
	if len(targets) == 0 {
		env.progressf("the %s plugin is installed nowhere, so nothing was refreshed\n"+
			"install it here:  %s", module.Name, env.copyable(env.Argv0+" agent-init "+module.Name))
		return
	}

	var moved [][2]string
	for _, target := range targets {
		written, err := module.Install(target.dir)
		if err != nil {
			env.warnf("could not write the %s plugin into %s%s", module.Name, target.label,
				asFields(field("directory", target.dir), field("error", err.Error())))
			continue
		}
		if len(written) > 0 {
			moved = append(moved, field(target.label, strings.Join(written, "\n")))
		}
	}

	if len(moved) == 0 {
		env.progressf("the %s plugin is already up to date in %s", module.Name,
			count(len(targets), "checkout", "checkouts"))
		return
	}
	env.progressf("wrote the %s plugin in %s%s", module.Name,
		count(len(moved), "checkout", "checkouts"), asFields(moved...))
	// The same three facts installAgentPlugin ends on, and the reason they are
	// worth repeating here is that this is the run after an upgrade: the files on
	// disk are current and every session already open is not.
	env.progressf("%s loads the new files the next time it starts\n"+
		"in a session that is already open, run %s",
		module.Name, env.copyable("/reload-plugins"))
}

// pluginCheckouts lists where this repository's plugin lives, in the order a
// person would look: the main checkout, then the worktrees, then the user-level
// copy that covers every repository at once.
//
// A worktree qualifies on either of two grounds, and the pair is the whole
// policy. One that already holds part of the plugin is refreshed however it got
// it, because a stale copy is stale whoever made it. One that holds none is
// given a copy only where the config carries the plugin into new worktrees —
// there, an empty worktree is a worktree that predates the carry and the copy is
// owed to it; without the carry, writing one would be treewright choosing a
// placement the config never asked for.
func pluginCheckouts(env *Env, cfg *config.Config, module agentinit.Agent) []checkout {
	var found []checkout

	projectDir := filepath.Join(cfg.MainDir, filepath.FromSlash(module.ProjectPlugin))
	inRepo := inspectPlugin(module, projectDir) != pluginAbsent
	if inRepo {
		found = append(found, checkout{label: baseName, dir: projectDir})
	}

	// Only asked once the main checkout has one: with the plugin installed at
	// user level, or not at all, a repository's worktrees are not where it lives
	// and walking them would be a git call spent to find nothing.
	if inRepo {
		managed, err := repoFor(cfg).Managed()
		if err != nil {
			env.warnf("could not list the worktrees of %q, so only the main checkout was refreshed%s",
				cfg.Name, asFields(field("error", err.Error())))
		}
		carried := cfg.Agent == module.Name || carriesPlugin(module, cfg)
		for _, wt := range managed {
			dir := filepath.Join(wt.Dir, filepath.FromSlash(module.ProjectPlugin))
			if inspectPlugin(module, dir) == pluginAbsent && !carried {
				continue
			}
			found = append(found, checkout{label: wt.Slug, dir: dir})
		}
	}

	if userDir := expandHome(module.UserPlugin); inspectPlugin(module, userDir) != pluginAbsent {
		found = append(found, checkout{label: userDir, dir: userDir})
	}
	return found
}

// refreshTmuxBindings reloads the key bindings into the running server, on
// whatever keys they are currently on.
//
// Reusing the keys is the load-bearing part. The snippet's keys are flags, set
// at the tmux.conf line that loads it, and a reload that quietly bound the
// defaults as well would hand back keys the user deliberately moved away from —
// including, where T and N are now something of theirs, by taking them.
//
// Nothing is loaded into a server holding no treewright bindings at all. That is
// not a refresh, it is an install, and which keys a tmux server binds is a
// decision made in a file the user owns.
func refreshTmuxBindings(env *Env) {
	if !tmux.Available() {
		return // checkTmux's business, and doctor is where it gets reported
	}
	// Asked in this order for the reason doctor asks it in this order: list-keys
	// starts a server when none is running, which would have refresh create the
	// server it then reported on.
	if !tmux.ServerRunning() {
		env.progressf("no tmux server is running, so its key bindings load fresh at the next start")
		return
	}
	bound, err := tmux.HasBindings()
	if err != nil || !bound {
		env.progressf("no key binding in the tmux server runs treewright, so none was reloaded\n"+
			"add to tmux.conf:  %s", env.copyable("run-shell 'treewright tmux-init --apply'"))
		return
	}

	// Both keys as the server holds them, empty included: an empty one is a
	// binding somebody omitted with --new-key "", and putting it back would be
	// this command undoing a choice rather than refreshing one.
	resume, create := tmux.BoundKeys()
	keys := tmuxinit.Keys{Resume: resume, New: create}
	if err := tmux.Source(tmuxinit.Script(keys)); err != nil {
		env.warnf("could not reload the tmux key bindings\n%v", err)
		return
	}
	env.progressf("reloaded the treewright key bindings into the running tmux server%s", asFields(
		field("keys", describeKeys(keys)),
	))
}

// describeKeys names the keys the bindings went back on, so the report says what
// it did rather than that it did something. An omitted binding is named as
// omitted: silence there would read as a key this failed to mention.
func describeKeys(keys tmuxinit.Keys) string {
	// Padded to the longer of the two, so the keys make a column of their own
	// inside the field's value — the same shape asFields gives the labels, one
	// level in.
	const width = len("switch worktree")
	var lines []string
	for _, k := range []struct{ what, key string }{
		{"switch worktree", keys.Resume},
		{"start one", keys.New},
	} {
		key := k.key
		if key == "" {
			key = "(not bound)"
		}
		lines = append(lines, fmt.Sprintf("%-*s  %s", width, k.what, key))
	}
	return strings.Join(lines, "\n")
}

// reportStaleShell says when the wrapper in the calling shell is not the one
// this binary emits — the one part of the integration refresh cannot fix.
//
// treewright runs in its own process and cannot define a function in its
// parent's, which is the same limitation the eval-file protocol exists to work
// around and cannot help with here: there is no shell command that replaces a
// function whose text this process would have to produce first. Opening a new
// terminal is the fix, and saying so is the whole of what this can do.
//
// Silent when nothing is loaded at all. That is a state doctor reports with the
// line to paste, and a command about upgrading is not where somebody finds out
// they never installed it.
func reportStaleShell(env *Env) {
	if env.EvalFile == "" {
		return
	}
	if shellinit.Current(os.Getenv(shellinit.VersionVar)) {
		return
	}
	env.progressf("your shell still holds the wrapper an older treewright emitted\n" +
		"nothing here can replace a function in the shell that loaded it\n" +
		"open a new terminal, or re-run the line in your startup file")
}
