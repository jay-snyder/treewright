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
// can only hope is current. See "Agent modules" in docs/design-notes.md.
package agentinit

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// pluginFiles reads a module's plugin out of the files checked in beside it,
// one PluginFile per file, in the order fs.WalkDir yields them — which is
// lexical, so a listing of the directory and the order agent-init reports
// writing them in are the same order.
//
// The directory is the plugin, and no Go code here touches a byte of it: a file
// added to the folder ships, is installed, and is carried into every worktree
// with no list to update anywhere. That is the derived-list rule from
// LocalState below taken one step further back, to the artifacts themselves,
// and it is why a module is a folder plus a dozen lines of Go rather than a
// wall of quoted JSON — the plugin a contributor reads is the plugin that gets
// written, and `claude plugin validate` can be pointed at the checkout.
//
// The panic is unreachable by construction: the only argument ever passed is an
// embed.FS whose root //go:embed already proved exists at compile time, and
// reading from one cannot fail at runtime. It is here because the alternative
// is a discarded error, which would turn a broken embed directive into a plugin
// silently missing a file.
func pluginFiles(fsys fs.FS, root string) []PluginFile {
	var out []PluginFile
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out = append(out, PluginFile{
			Path: strings.TrimPrefix(strings.TrimPrefix(p, root), "/"),
			Body: string(body),
		})
		return nil
	})
	if err != nil {
		panic("agentinit: reading the embedded plugin under " + root + ": " + err.Error())
	}
	return out
}

// PluginFile is one file of the plugin a module installs: a path relative to
// the plugin's own directory, and the body treewright writes there.
//
// Kept as a list rather than as a field per artifact because two things read it
// and both want all of them at once — agent-init writes every file, and doctor
// compares every file against what agent-init would write, which is how a
// plugin installed by an older treewright is reported as out of date rather
// than mistaken for current.
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
	// UserSettings is the same file made global. It is named for two reasons
	// that outlived the paste it used to receive: doctor recognizes hooks an
	// older treewright told the user to put there, and someone who wired the
	// agent by hand put them in one of these two files.
	ProjectSettings string
	UserSettings    string

	// ProjectPlugin is the directory the agent loads treewright's plugin from,
	// relative to a checkout's root, and is where the wiring belongs by
	// default: a repository you use treewright in gets it, and one you do not
	// stays untouched. UserPlugin is the same made global, for someone who
	// wants every repository covered at once.
	ProjectPlugin string
	UserPlugin    string

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
