// Package agentinit holds what treewright knows about particular coding agents.
//
// treewright's core is agent-agnostic on purpose: it provides neutral protocols
// — a command to launch, a `signal` verb for state — and knows no agent's name.
// But the facts about a particular agent are facts every user of that agent
// needs identically: what launches it, what resumes it, which gitignored files
// hold its per-project state, and how its hooks are wired to `signal`. This
// package is where those facts live, one module per agent, so that supporting
// another agent is a folder and a file beside claude.go rather than edits
// across the tree.
//
// A module shares nothing with another by construction, including the document
// teaching the agent to drive treewright. The CLI is the same whoever runs it,
// but a skill is written for a particular reader — one that loads instructions
// from a description, is told which of its own transitions the hooks cover, and
// is asked to leave `signal` to them — and a second agent that parses
// instructions or reports state differently wants those paragraphs rewritten
// rather than inherited. What keeps the copies honest is a test rather than a
// shared file: internal/cli holds every module's plugin to the command table.
//
// Two consumers read the modules. `treewright agent-init <agent>` installs the
// agent's plugin — its hooks and its skill — emitted by the binary the way the
// shell shims and the tmux snippet are, so it can never drift out of sync with
// the `signal` vocabulary it targets. And a config's `agent` key names a module
// to take launch defaults from — and to have the agent's local-state files
// carried into every new worktree, which is what makes per-repo wiring reach
// the worktrees it exists for.
//
// The plugin is the shape that closed the last gap between this package and its
// two siblings. A shell shim is re-evaluated at every shell start and a tmux
// snippet re-read at every server start, so both propagate a treewright upgrade
// on their own; the hooks, printed as JSON for a person to paste into a settings
// file, could not — JSON has no eval, and the pasted copy was frozen at the
// version that printed it. A plugin directory is a place of treewright's own to
// write, so the wiring is a file treewright can rewrite rather than a copy it
// can only hope is current. See "Agent modules" in docs/agents.md.
package agentinit

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// PluginFile is one file of the plugin a module installs: a path relative to
// the plugin's own directory, and the body treewright writes there.
//
// Kept as a list rather than as a field per artifact because three things read
// it and all of them want every file at once — agent-init writes them, doctor
// compares them against what agent-init would write, and LocalState turns them
// into the carry. The list is also the module's declaration of what may ship at
// all: see the note on claude's, which is where the reasoning for naming each
// file rather than embedding the folder lives.
type PluginFile struct {
	// Path is slash-separated and relative to the plugin root, in the layout
	// the agent expects. Never absolute, and never leading out of the plugin.
	Path string
	Body string
}

// Agent is one agent module.
type Agent struct {
	// Name is what the config's `agent` key and `agent-init` call it.
	Name string

	// Command launches the agent fresh; ResumeCommand picks up where it left
	// off. They are the defaults a config naming this module gets for its
	// command and resume_command, each still overridable in the file.
	Command       string
	ResumeCommand string

	// ProjectSettings is where the agent reads a checkout's own configuration,
	// relative to its root. treewright does not write there — its wiring lives
	// in the plugin below, a directory of its own — but the file still holds
	// the "always allow" permission decisions the agent records as it works,
	// which every worktree wants, so it stays on the carried list.
	//
	// UserSettings is the same file made global, relative to UserDir. It is
	// named for two reasons that outlived the paste it used to receive: doctor
	// recognizes hooks an older treewright told the user to put there, and
	// someone who wired the agent by hand put them in one of these two files.
	ProjectSettings string
	UserSettings    string

	// UserDir is where the agent keeps its user-level configuration, and
	// UserDirVar names the environment variable the agent itself reads to
	// relocate it. Every user-level path below is relative to whichever of the
	// two wins, which is the whole point of naming the variable here: an agent
	// that lets its directory be moved has users who have moved it, and to
	// those users the default is not a good guess but a directory nothing
	// reads. The wiring then installs successfully, doctor confirms it, and the
	// agent never loads it — a failure with no symptom except the feature
	// quietly not working. Leave UserDirVar empty for an agent that has no such
	// variable; UserDir alone is then the answer.
	UserDir    string
	UserDirVar string

	// UserPlugin is the agent's own user-level plugin directory, relative to
	// UserDir, and is where the wiring belongs by default: one install covers
	// every checkout the agent is ever started in, worktrees included, where a
	// per-repo copy reaches the main checkout and nothing else until something
	// carries it. ProjectPlugin is the directory the agent loads a checkout's
	// own plugin from, relative to its root — what `agent-init --local`
	// installs into — for a machine where treewright's wiring should touch one
	// repository and no other. Both keep the names the agent uses for the two
	// scopes, matching UserSettings and ProjectSettings above; the flag is
	// spelled `--local` instead, that being git's own word for the scope of a
	// repository and one every user of this tool has typed.
	UserPlugin    string
	ProjectPlugin string

	// Plugin is what goes in either of them — the agent's own hooks wired to
	// `treewright signal`, the skill teaching it to drive treewright, and
	// whatever manifest the agent needs to load the two. Every file spells the
	// canonical binary name, being destined for files a program reads.
	Plugin []PluginFile
}

// LocalState are the per-project files a config naming this module has carried
// into every new worktree — silently skipped when the main checkout has none,
// since unlike a carry_files entry nobody asserted they exist.
//
// Derived from the project paths rather than listed beside them, because the
// two must not disagree: a module that gained a per-project artifact and did
// not also carry it would put that artifact in the main checkout and in no
// worktree, which is the trap the carry exists to close. Deriving it means a
// module cannot describe a per-project file it forgets to carry.
//
// The plugin is a tree rather than a file, and it is spelled out here one file
// at a time rather than as the directory holding them. Teaching the carry to
// copy directories was the alternative and it loses twice: a carry_files entry
// naming a directory has no meaning for the warn-when-missing rule that entry
// exists for, and a module could then ship a plugin file no list names, which
// is the very omission deriving the list is here to make impossible.
//
// Whether these end up in git is the repository's business, not treewright's.
// The agent's settings file is conventionally ignored already; the plugin is a
// directory like any other, and a developer manages their own .gitignore.
func (a Agent) LocalState() []string {
	var out []string
	if a.ProjectSettings != "" {
		out = append(out, a.ProjectSettings)
	}
	for _, f := range a.Plugin {
		out = append(out, path.Join(a.ProjectPlugin, f.Path))
	}
	return out
}

// UserConfigDir is the directory the agent actually keeps its user-level
// configuration in: the environment variable the agent reads when it is set,
// else the module's default.
//
// Asking the environment rather than hard-coding the default is what makes the
// user-level placement work for the people most likely to have moved it. Claude
// Code reads $CLAUDE_CONFIG_DIR, and anyone keeping their home directory to the
// XDG layout has pointed it somewhere under ~/.local/state — so `agent-init`
// wrote a correct plugin into ~/.claude, doctor compared it against ~/.claude
// and pronounced it current, and the agent, reading the directory it was told
// to, loaded nothing. Every part reported success, which is why this is
// resolved in one place that all of them call rather than at each call site.
//
// The variable wins over the default whenever it holds anything, and an empty
// value is treated as unset because that is what an exported-but-empty variable
// means to the agent too. It is expanded for a leading ~ like the default: the
// value comes from a shell profile, where writing ~ is ordinary and the shell
// has usually already expanded it, but a value set in a config file or a
// process environment has not been through a shell at all.
func (a Agent) UserConfigDir() string {
	if a.UserDirVar != "" {
		if dir := os.Getenv(a.UserDirVar); dir != "" {
			return expandHome(dir)
		}
	}
	return expandHome(a.UserDir)
}

// UserPluginDir is where `agent-init` installs the plugin by default, resolved.
func (a Agent) UserPluginDir() string {
	return filepath.Join(a.UserConfigDir(), filepath.FromSlash(a.UserPlugin))
}

// UserSettingsFile is the agent's user-level settings file, resolved. Nothing
// writes it — doctor reads it to recognize hooks pasted by an older treewright.
func (a Agent) UserSettingsFile() string {
	return filepath.Join(a.UserConfigDir(), filepath.FromSlash(a.UserSettings))
}

// expandHome resolves the leading ~ the modules spell their user-level paths
// with. A path that does not start with one is returned untouched, including an
// absolute one, which is what an agent's own environment variable usually holds.
func expandHome(dir string) string {
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/"))
		}
	}
	return dir
}

// Install writes the module's plugin into dir and returns the files it changed,
// in the module's own order. The caller chooses dir — a checkout's own, or the
// agent's user-level one — because which repositories the wiring covers is the
// user's decision and not the module's.
//
// Unchanged files are left alone rather than rewritten with identical bytes,
// which is what makes a second run reportable: agent-init is what you run after
// upgrading treewright, and it can only say what the upgrade moved if it knows
// what was already there. A file the user edited is overwritten without asking,
// deliberately — the directory is treewright's, its contents are generated, and
// an edit that survived would be a hook nobody could see going stale.
//
// The layout lives here rather than in the command for the same reason the hook
// mapping does: it is a fact about one agent. A second module ships a different
// tree and this writes it unchanged.
//
// Every file is written 0o644. That is a limitation, not a policy: a plugin's
// bin/ entries are meant to be executables on the agent's PATH, and a module
// that ships one will need a mode on PluginFile before what this installs can
// run. Nothing ships one today, and saying so here is the whole of the support
// until something does.
func (a Agent) Install(dir string) ([]string, error) {
	var written []string
	for _, f := range a.Plugin {
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if existing, err := os.ReadFile(target); err == nil && string(existing) == f.Body {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, []byte(f.Body), 0o644); err != nil {
			return nil, err
		}
		written = append(written, f.Path)
	}
	return written, nil
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
