// Package tmuxinit produces the tmux-side integration, the way shellinit
// produces the shell-side one.
//
// The gap it closes is particular to how treewright opens a window. The configured
// command is the window's own command, so a worktree's pane is the agent itself and
// there is no shell in it to type "treewright resume" into: switching worktrees meant
// splitting a pane or going to find a window that has a prompt. A key binding
// that opens treewright in a popup reaches it from anywhere, including from inside a
// running agent, and closes again afterwards.
//
// tmux has no equivalent of eval "$(...)", so the snippet is loaded one of two
// ways, both of which come from this one text:
//
//	run-shell 'treewright tmux-init --apply'   # the binary loads it into the server
//
//	treewright tmux-init > ~/.config/treewright/treewright.tmux
//	source-file ~/.config/treewright/treewright.tmux
//
// Printing is the default because a file of key bindings is worth reading before
// it is loaded, and because it is then yours to edit.
package tmuxinit

import (
	"fmt"
	"strings"
)

// Keys are the prefix keys the integration binds. An empty key omits its binding
// entirely, for someone who wants one of them and not the other.
type Keys struct {
	Resume string // switch to another worktree
	New    string // start one
}

// DefaultKeys are what the snippet binds when the caller asks for nothing else.
//
// Both are unbound in stock tmux, whose own uppercase keys are C, D, E, L and M.
// But being free is the smaller half of the test: the larger half is what a
// missed shift does, because these are keys reached for in a hurry. Here the
// twins are t, clock-mode, and n, next-window — both harmless and both dismissed
// by carrying on.
//
// W was the first choice, for the mnemonic with tmux's own w window picker, and
// it is the reason these are configurable at all. A great many configurations
// rebind lowercase w to kill-window, and there the slip destroys the very window
// the binding exists to reach — a treewright worktree being a window with an agent
// running in it and nothing else.
func DefaultKeys() Keys { return Keys{Resume: "T", New: "N"} }

// Validate rejects a key that would not survive being written into a config file.
//
// The permitted set is letters, digits, dash and underscore, which covers a plain
// key, a modified one such as C-t or M-Left, and the named keys — F5, Space,
// PageUp. It excludes quotes, semicolons, hashes and whitespace, every one of
// which is punctuation in tmux's own config syntax and would end the binding
// somewhere other than where it looked like it ended.
//
// tmux does bind some of those characters by default, so this does turn away keys
// that would in principle work. Anyone who wants prefix + ; can print the snippet
// and write that line themselves, which is a documented route rather than a
// workaround.
func (k Keys) Validate() error {
	for _, named := range []struct{ flag, value string }{
		{"--resume-key", k.Resume},
		{"--new-key", k.New},
	} {
		if named.value == "" {
			continue // asked for deliberately: bind nothing to this one
		}
		if len(named.value) > 12 {
			return fmt.Errorf("%s %q is too long to be a tmux key", named.flag, named.value)
		}
		for _, r := range named.value {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return fmt.Errorf("%s %q contains %q, which is punctuation in tmux's config syntax — "+
					"print the snippet and write that binding by hand instead", named.flag, named.value, r)
			}
		}
	}
	return nil
}

// Script returns the tmux configuration treewright suggests, binding keys.
func Script(k Keys) string {
	var b strings.Builder
	b.WriteString(header)

	if k.Resume != "" || k.New != "" {
		b.WriteString(reachHeader)
		if k.Resume != "" {
			b.WriteString(fill(resumeBinding, k.Resume))
		}
		if k.New != "" {
			b.WriteString(fill(newBinding, k.New))
		}
	}

	b.WriteString(titles)
	b.WriteString(recorded)
	return b.String()
}

// fill substitutes the key into a binding.
//
// By replacement rather than by formatting, because these lines are dense with
// characters a format string reads as verbs: "70%", "60%", and the "%1" that
// carries what tmux's prompt collected.
func fill(template, key string) string {
	return strings.ReplaceAll(template, "{{key}}", key)
}

const header = `# treewright tmux integration. Load it from ~/.tmux.conf with either:
#
#     run-shell 'treewright tmux-init --apply'
#
#     treewright tmux-init > ~/.config/treewright/treewright.tmux
#     source-file ~/.config/treewright/treewright.tmux
#
# The first is always in step with the installed binary; the second is a file you
# can read and edit. Both load exactly what "treewright tmux-init" prints.
#
# To move the keys, pass them through rather than editing after the fact, so the
# two ways of loading this stay identical:
#
#     treewright tmux-init --resume-key G --new-key C-n
#
# An empty key omits its binding: --new-key "" binds only the first.
`

// Four details in these bindings are load-bearing.
//
// They go through `treewright popup` rather than calling display-popup directly,
// because tmux fixes a popup's size when it creates one and -w and -h take only
// cells or a percentage of the terminal. A percentage is the wrong unit for a
// picker — its height is the number of worktrees and its width the widest slug,
// neither of which grows when the terminal does — so on a wide terminal the popup
// came out mostly empty. Working the size out needs a program, and run-shell is
// the only way a binding can run one.
//
// That indirection costs the client, which #{client_tty} buys back: a tmux
// command run from a backgrounded process has no association with the client that
// asked for it, so tmux falls back to the most recently active one and the popup
// opens over whichever terminal has been busier. run-shell expands formats in the
// command it runs, so the binding can say which client it means.
//
// #{pane_current_path} buys back the directory, and is easy to think you get for
// free. run-shell does not run in the calling pane's directory — it runs in the
// tmux server's, which is wherever the server was started, usually one
// repository's checkout and never the pane's. Without this the popup answers
// about that repository from every window on the server, and marks its worktree
// as the one you are standing in. tmux offers no way to fix it from outside the
// command: run-shell's own -c takes a directory but does not expand a format into
// one, so the path has to travel in the command string, where formats are
// expanded, exactly as the client does.
//
// The binding runs `resume` rather than `cd`, because a popup's shell exits with
// the popup: moving it somewhere is an operation with nowhere to land.
const reachHeader = `
# --- reaching treewright from inside a worktree -----------------------------------
#
# A window treewright opens runs the agent as the window's own command, so there is
# no shell in it to type into. These open treewright in a popup over whatever is
# running, and close it again once you have chosen.
#
# Each binding hands treewright the current pane's directory, which is how it knows
# which repository you mean. run-shell would otherwise run in the tmux server's
# directory, and answer about whichever repository that happens to be.
`

const resumeBinding = `
# prefix + {{key}} — switch to another worktree.
bind-key {{key}} run-shell -b 'treewright popup -c "#{client_tty}" -d "#{pane_current_path}" resume'
`

// The path is quoted here where the client is bare, because a directory can
// contain a space and a tty cannot. The quotes are escaped because they are
// already inside the double-quoted run-shell argument: tmux unescapes them when
// it parses that argument, so the shell underneath receives one quoted word.
const newBinding = `
# prefix + {{key}} — start one. tmux asks for the slug; treewright does the rest.
bind-key {{key}} command-prompt -p "new worktree:" 'run-shell -b "treewright popup -c #{client_tty} -d \"#{pane_current_path}\" new %1"'
`

const titles = `
# --- terminal and tab titles -------------------------------------------------
#
# The one part of this file that is not about treewright. tmux sets no title at all
# by default, so attaching leaves your terminal tab named after the command line
# that attached — "tmux attach -t myrepo" — until a prompt overwrites it, which
# inside tmux never comes. Delete these two lines if you set your own format.
set -g set-titles on
set -g set-titles-string "#S: #W"
`

const recorded = `
# --- what treewright records on a window ----------------------------------------
#
# Every window treewright opens carries the worktree it belongs to, so a status line
# can name it without shelling out to git on each interval:
#
#     @treewright_repo      the config's name
#     @treewright_worktree  the checkout the window was opened on
#     @treewright_slug      the worktree — unset on the base window, which is not one
#     @treewright_branch    the branch that worktree is on
#
# For instance, to keep the repository in view while attached:
#
#     set -g status-right " #{@treewright_repo} "
`
