// Package shellinit produces the shell integration treewright needs.
//
// treewright is a compiled binary, so it runs in its own process and cannot change
// the calling shell's working directory. Two small things therefore have to live
// in the shell itself: a wrapper function that lets treewright hand back a command
// to run (see the eval-file protocol in internal/cli), and tab completion.
//
// Rather than installing files per shell, the binary prints its own integration:
//
//	eval "$(treewright shell-init zsh)"     # or bash
//	treewright shell-init fish | source
//
// Each script also defines tw, the everyday short name: the same wrapper under
// fewer keystrokes, with the same completion. treewright is the name of the
// product; tw is the name of the habit.
//
// The shims are versioned with the binary that emits them, so they can never
// drift out of sync with it. This is the same approach fzf, zoxide, direnv and
// starship take, all of which are compiled binaries that still need a shell-side
// shim for exactly this reason.
package shellinit

import (
	"fmt"
	"sort"
	"strings"
)

// Shells lists the supported shell names.
func Shells() []string {
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Script returns the integration snippet for a shell.
func Script(shell string) (string, error) {
	s, ok := scripts[shell]
	if !ok {
		return "", fmt.Errorf("unsupported shell %q (supported: %s)", shell, strings.Join(Shells(), ", "))
	}
	return s, nil
}

var scripts = map[string]string{
	"zsh":  zshScript,
	"bash": bashScript,
	"fish": fishScript,
}

// The wrapper in each shell follows the same three steps: make a temp file,
// hand its path to the binary as $TREEWRIGHT_EVAL_FILE, then source it if the
// binary wrote anything. Two commands write to it: `treewright cd`, and `treewright rm`
// when the shell is standing in the directory being deleted.
//
// Every external program the wrappers call is invoked through `command`, for the
// same reason the binary itself is: zsh and bash expand aliases in a function
// body when the function is defined, so an alias in the user's startup file
// silently rewrites the words below. `alias rm='rm -i'` is common enough to be
// the expected case, and turns `rm -f` into `rm -i -f` — harmless only because
// BSD and GNU rm both let the later flag win. Nothing here should depend on that.
//
// The subcommand names each script lists are checked against the real command
// table by a test, so a command added to treewright cannot silently go missing from
// completion.

const zshScript = `# treewright shell integration for zsh. Load with: eval "$(treewright shell-init zsh)"
# Note: rc, not status — status is a special parameter in zsh and cannot be a local.
treewright() {
  local evalfile rc
  evalfile="$(command mktemp "${TMPDIR:-/tmp}/treewright-eval.XXXXXX")" || return 1
  # "command" skips this function and runs the real binary, and shields mktemp
  # and rm from any alias of the same name.
  TREEWRIGHT_EVAL_FILE="$evalfile" command treewright "$@"
  rc=$?
  [[ -s "$evalfile" ]] && source "$evalfile"
  command rm -f "$evalfile"
  return $rc
}

_treewright() {
  local -a cmds
  cmds=(
    'new:create a worktree and branch, and open a tmux window in it'
    'resume:reopen a window on an existing worktree'
    'cd:move your shell into a worktree'
    'base:open a window on the main checkout'
    'popup:run a treewright command in a tmux popup sized to its output'
    'attach:attach this terminal to the repository tmux session'
    'ls:list worktrees with their status'
    'rm:tear down a worktree and its branch'
    'prune:remove every merged, clean worktree'
    'setup:write a config for the repository you are standing in'
    'config:print the settings in force, defaults included'
    'doctor:check the installation and every registered config'
    'shell-init:print the shell integration'
    'tmux-init:print the tmux integration'
  )
  if (( CURRENT == 2 )); then
    _describe -t commands 'treewright command' cmds
    return
  fi
  # A word being typed as a flag gets that command's flags, which treewright
  # reports from the same table that renders its help.
  if [[ "$words[CURRENT]" == -* ]]; then
    compadd -- ${(f)"$(command treewright __complete flags "$words[2]" 2>/dev/null)"}
    return
  fi
  case "$words[2]" in
    rm)                          compadd -- ${(f)"$(command treewright __complete slugs 2>/dev/null)"} ;;
    resume|cd)                   compadd -- ${(f)"$(command treewright __complete targets 2>/dev/null)"} ;;
    ls|prune|base|config|attach) compadd -- ${(f)"$(command treewright __complete repos 2>/dev/null)"} ;;
    shell-init)                  compadd -- ${(f)"$(command treewright __complete shells 2>/dev/null)"} ;;
  esac
}
# tw calls the treewright *function*, resolved at call time, so the eval-file
# protocol works identically under either name. That call runs the binary as
# "command treewright", which erases the name the user typed from argv[0] — so
# tw reports it in TREEWRIGHT_ARGV0 instead, and help and hints answer as "tw".
tw() { local -x TREEWRIGHT_ARGV0=tw; treewright "$@" }
(( $+functions[compdef] )) && compdef _treewright treewright tw
`

const bashScript = `# treewright shell integration for bash. Load with: eval "$(treewright shell-init bash)"
treewright() {
  local evalfile rc
  evalfile="$(command mktemp "${TMPDIR:-/tmp}/treewright-eval.XXXXXX")" || return 1
  # "command" skips this function and runs the real binary, and shields mktemp
  # and rm from any alias of the same name.
  TREEWRIGHT_EVAL_FILE="$evalfile" command treewright "$@"
  rc=$?
  [[ -s "$evalfile" ]] && source "$evalfile"
  command rm -f "$evalfile"
  return $rc
}

_treewright_completions() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=($(compgen -W "new resume cd base attach popup ls rm prune setup config doctor shell-init tmux-init" -- "$cur"))
    return
  fi
  if [[ "$cur" == -* ]]; then
    COMPREPLY=($(compgen -W "$(command treewright __complete flags "${COMP_WORDS[1]}" 2>/dev/null)" -- "$cur"))
    return
  fi
  local candidates=""
  case "${COMP_WORDS[1]}" in
    rm)                          candidates="$(command treewright __complete slugs 2>/dev/null)" ;;
    resume|cd)                   candidates="$(command treewright __complete targets 2>/dev/null)" ;;
    ls|prune|base|config|attach) candidates="$(command treewright __complete repos 2>/dev/null)" ;;
    shell-init)                  candidates="$(command treewright __complete shells 2>/dev/null)" ;;
  esac
  COMPREPLY=($(compgen -W "$candidates" -- "$cur"))
}
# tw calls the treewright *function*, resolved at call time, so the eval-file
# protocol works identically under either name. That call runs the binary as
# "command treewright", which erases the name the user typed from argv[0] — so
# tw reports it in TREEWRIGHT_ARGV0 instead, and help and hints answer as "tw".
tw() { local -x TREEWRIGHT_ARGV0=tw; treewright "$@"; }
complete -F _treewright_completions treewright tw
`

const fishScript = `# treewright shell integration for fish. Load with: treewright shell-init fish | source
function treewright
    set -l tmp /tmp
    if set -q TMPDIR
        set tmp $TMPDIR
    end
    set -l evalfile (command mktemp $tmp/treewright-eval.XXXXXX)
    or return 1
    # "command" skips this function and runs the real binary. fish resolves
    # functions at call time rather than expanding aliases at definition time, so
    # this is for consistency with the other shims rather than for safety.
    set -lx TREEWRIGHT_EVAL_FILE $evalfile
    command treewright $argv
    set -l saved $status
    if test -s $evalfile
        source $evalfile
    end
    command rm -f $evalfile
    return $saved
end

complete -c treewright -f
complete -c treewright -n __fish_use_subcommand -a new        -d 'create a worktree and branch, and open a tmux window in it'
complete -c treewright -n __fish_use_subcommand -a resume     -d 'reopen a window on an existing worktree'
complete -c treewright -n __fish_use_subcommand -a cd         -d 'move your shell into a worktree'
complete -c treewright -n __fish_use_subcommand -a base       -d 'open a window on the main checkout'
complete -c treewright -n __fish_use_subcommand -a attach     -d 'attach this terminal to the repository tmux session'
complete -c treewright -n __fish_use_subcommand -a popup      -d 'run a treewright command in a tmux popup sized to its output'
complete -c treewright -n __fish_use_subcommand -a ls         -d 'list worktrees with their status'
complete -c treewright -n __fish_use_subcommand -a rm         -d 'tear down a worktree and its branch'
complete -c treewright -n __fish_use_subcommand -a prune      -d 'remove every merged, clean worktree'
complete -c treewright -n __fish_use_subcommand -a setup      -d 'write a config for the repository you are standing in'
complete -c treewright -n __fish_use_subcommand -a config     -d 'print the settings in force, defaults included'
complete -c treewright -n __fish_use_subcommand -a doctor     -d 'check the installation and every registered config'
complete -c treewright -n __fish_use_subcommand -a shell-init -d 'print the shell integration'
complete -c treewright -n __fish_use_subcommand -a tmux-init  -d 'print the tmux integration'
complete -c treewright -n '__fish_seen_subcommand_from rm' -a '(command treewright __complete slugs)'
complete -c treewright -n '__fish_seen_subcommand_from resume cd' -a '(command treewright __complete targets)'
complete -c treewright -n '__fish_seen_subcommand_from ls prune base config attach' -a '(command treewright __complete repos)'
complete -c treewright -n '__fish_seen_subcommand_from shell-init' -a '(command treewright __complete shells)'
complete -c treewright -n '__fish_seen_subcommand_from rm' -s f -l force -d 'remove even when unsaved work would be lost'
complete -c treewright -n '__fish_seen_subcommand_from rm' -s y -l yes -d 'do not ask before closing the tmux window'
complete -c treewright -n '__fish_seen_subcommand_from prune' -s y -l yes -d 'actually remove them, instead of listing'
complete -c treewright -n '__fish_seen_subcommand_from ls' -l json -d 'print machine-readable output'
complete -c treewright -n '__fish_seen_subcommand_from setup' -s n -l dry-run -d 'print the config instead of writing it'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l apply -d 'load it into the running tmux server'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l resume-key -r -d 'prefix key that switches worktrees'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l new-key -r -d 'prefix key that starts a worktree'

# tw calls the treewright function, and --wraps inherits its completions. The
# function runs the binary as "command treewright", which erases the name the
# user typed from argv[0] — so tw reports it in TREEWRIGHT_ARGV0 instead, and
# help and hints answer as "tw".
function tw --wraps treewright
    set -lx TREEWRIGHT_ARGV0 tw
    treewright $argv
end
`
