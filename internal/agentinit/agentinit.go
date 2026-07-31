// Package agentinit holds what treewright knows about particular coding agents.
//
// treewright's core is agent-agnostic on purpose: it provides neutral protocols
// — a command to launch, a `signal` verb for state — and knows no agent's name.
// But the facts about a particular agent are facts every user of that agent
// needs identically: what launches it, what resumes it, which gitignored files
// hold its per-project state, and how its hooks are wired to `signal`. This
// package is where those facts live, one module per agent, so that supporting
// another agent is a file beside claude.go rather than edits across the tree.
//
// Two consumers read the modules. `treewright agent-init <agent>` prints the
// agent's hook configuration, emitted by the binary the way the shell shims and
// the tmux snippet are, so it can never drift out of sync with the `signal`
// vocabulary it targets. And a config's `agent` key names a module to take
// launch defaults from — and to have the agent's local-state files carried into
// every new worktree, which is what makes per-repo hooks reach the worktrees
// they exist for.
package agentinit

import (
	"sort"
)

// Agent is one agent module.
type Agent struct {
	// Name is what the config's `agent` key and `agent-init` call it.
	Name string

	// Command launches the agent fresh; ResumeCommand picks up where it left
	// off. They are the defaults a config naming this module gets for its
	// command and resume_command, each still overridable in the file.
	Command       string
	ResumeCommand string

	// LocalState are the gitignored files holding the agent's per-project
	// state, relative to a checkout's root. A config naming this module has
	// them carried into every new worktree — silently skipped when the main
	// checkout has none, since unlike a carry_files entry nobody asserted they
	// exist.
	LocalState []string

	// UserSettings is the file, in its user-facing ~ spelling, where the
	// agent reads user-global configuration — where the hooks belong when they
	// should cover every repository at once.
	UserSettings string

	// Hooks is the configuration fragment agent-init prints: the agent's own
	// hooks wired to `treewright signal`. It spells the canonical binary name
	// throughout, being destined for a file a program reads.
	Hooks string
}

// modules is the registry, keyed by name. One entry today; a second agent is a
// file defining its Agent and an init adding it here.
var modules = map[string]Agent{
	claude.Name: claude,
}

// Names lists the modules, sorted, for errors and completion.
func Names() []string {
	names := make([]string, 0, len(modules))
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Lookup finds a module by name.
func Lookup(name string) (Agent, bool) {
	a, ok := modules[name]
	return a, ok
}
