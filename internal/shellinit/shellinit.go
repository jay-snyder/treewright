// Package shellinit produces the shell integration treemux needs.
//
// treemux is a compiled binary, so it runs in its own process and cannot change
// the calling shell's working directory. Two small things therefore have to live
// in the shell itself: a wrapper function that lets treemux hand back a command
// to run (see the eval-file protocol in internal/cli), and tab completion.
//
// Rather than installing files per shell, the binary prints its own integration:
//
//	eval "$(treemux shell-init zsh)"     # or bash
//	treemux shell-init fish | source
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
// hand its path to the binary as $TREEMUX_EVAL_FILE, then source it if the
// binary wrote anything. Two commands write to it: `treemux cd`, and `treemux rm`
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
// table by a test, so a command added to treemux cannot silently go missing from
// completion.

const zshScript = `# treemux shell integration for zsh. Load with: eval "$(treemux shell-init zsh)"
# Note: rc, not status — status is a special parameter in zsh and cannot be a local.
treemux() {
  local evalfile rc
  evalfile="$(command mktemp "${TMPDIR:-/tmp}/treemux-eval.XXXXXX")" || return 1
  # "command" skips this function and runs the real binary, and shields mktemp
  # and rm from any alias of the same name.
  TREEMUX_EVAL_FILE="$evalfile" command treemux "$@"
  rc=$?
  [[ -s "$evalfile" ]] && source "$evalfile"
  command rm -f "$evalfile"
  return $rc
}

_treemux() {
  local -a cmds
  cmds=(
    'new:create a worktree and branch, and open a tmux window in it'
    'resume:reopen a window on an existing worktree'
    'cd:move your shell into a worktree'
    'base:open a window on the main checkout'
    'ls:list worktrees with their status'
    'rm:tear down a worktree and its branch'
    'prune:remove every merged, clean worktree'
    'setup:write a config for the repository you are standing in'
    'config:print the settings in force, defaults included'
    'doctor:check the installation and every registered config'
    'shell-init:print the shell integration'
  )
  if (( CURRENT == 2 )); then
    _describe -t commands 'treemux command' cmds
    return
  fi
  # A word being typed as a flag gets that command's flags, which treemux
  # reports from the same table that renders its help.
  if [[ "$words[CURRENT]" == -* ]]; then
    compadd -- ${(f)"$(command treemux __complete flags "$words[2]" 2>/dev/null)"}
    return
  fi
  case "$words[2]" in
    rm|resume|cd)         compadd -- ${(f)"$(command treemux __complete slugs 2>/dev/null)"} ;;
    ls|prune|base|config) compadd -- ${(f)"$(command treemux __complete repos 2>/dev/null)"} ;;
    shell-init)           compadd -- ${(f)"$(command treemux __complete shells 2>/dev/null)"} ;;
  esac
}
(( $+functions[compdef] )) && compdef _treemux treemux
`

const bashScript = `# treemux shell integration for bash. Load with: eval "$(treemux shell-init bash)"
treemux() {
  local evalfile rc
  evalfile="$(command mktemp "${TMPDIR:-/tmp}/treemux-eval.XXXXXX")" || return 1
  # "command" skips this function and runs the real binary, and shields mktemp
  # and rm from any alias of the same name.
  TREEMUX_EVAL_FILE="$evalfile" command treemux "$@"
  rc=$?
  [[ -s "$evalfile" ]] && source "$evalfile"
  command rm -f "$evalfile"
  return $rc
}

_treemux_completions() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=($(compgen -W "new resume cd base ls rm prune setup config doctor shell-init" -- "$cur"))
    return
  fi
  if [[ "$cur" == -* ]]; then
    COMPREPLY=($(compgen -W "$(command treemux __complete flags "${COMP_WORDS[1]}" 2>/dev/null)" -- "$cur"))
    return
  fi
  local candidates=""
  case "${COMP_WORDS[1]}" in
    rm|resume|cd)         candidates="$(command treemux __complete slugs 2>/dev/null)" ;;
    ls|prune|base|config) candidates="$(command treemux __complete repos 2>/dev/null)" ;;
    shell-init)           candidates="$(command treemux __complete shells 2>/dev/null)" ;;
  esac
  COMPREPLY=($(compgen -W "$candidates" -- "$cur"))
}
complete -F _treemux_completions treemux
`

const fishScript = `# treemux shell integration for fish. Load with: treemux shell-init fish | source
function treemux
    set -l tmp /tmp
    if set -q TMPDIR
        set tmp $TMPDIR
    end
    set -l evalfile (command mktemp $tmp/treemux-eval.XXXXXX)
    or return 1
    # "command" skips this function and runs the real binary. fish resolves
    # functions at call time rather than expanding aliases at definition time, so
    # this is for consistency with the other shims rather than for safety.
    set -lx TREEMUX_EVAL_FILE $evalfile
    command treemux $argv
    set -l saved $status
    if test -s $evalfile
        source $evalfile
    end
    command rm -f $evalfile
    return $saved
end

complete -c treemux -f
complete -c treemux -n __fish_use_subcommand -a new        -d 'create a worktree and branch, and open a tmux window in it'
complete -c treemux -n __fish_use_subcommand -a resume     -d 'reopen a window on an existing worktree'
complete -c treemux -n __fish_use_subcommand -a cd         -d 'move your shell into a worktree'
complete -c treemux -n __fish_use_subcommand -a base       -d 'open a window on the main checkout'
complete -c treemux -n __fish_use_subcommand -a ls         -d 'list worktrees with their status'
complete -c treemux -n __fish_use_subcommand -a rm         -d 'tear down a worktree and its branch'
complete -c treemux -n __fish_use_subcommand -a prune      -d 'remove every merged, clean worktree'
complete -c treemux -n __fish_use_subcommand -a setup      -d 'write a config for the repository you are standing in'
complete -c treemux -n __fish_use_subcommand -a config     -d 'print the settings in force, defaults included'
complete -c treemux -n __fish_use_subcommand -a doctor     -d 'check the installation and every registered config'
complete -c treemux -n __fish_use_subcommand -a shell-init -d 'print the shell integration'
complete -c treemux -n '__fish_seen_subcommand_from rm resume cd' -a '(command treemux __complete slugs)'
complete -c treemux -n '__fish_seen_subcommand_from ls prune base config' -a '(command treemux __complete repos)'
complete -c treemux -n '__fish_seen_subcommand_from shell-init' -a '(command treemux __complete shells)'
complete -c treemux -n '__fish_seen_subcommand_from rm' -s f -l force -d 'remove even when unsaved work would be lost'
complete -c treemux -n '__fish_seen_subcommand_from rm' -s y -l yes -d 'do not ask before closing the tmux window'
complete -c treemux -n '__fish_seen_subcommand_from prune' -s y -l yes -d 'actually remove them, instead of listing'
complete -c treemux -n '__fish_seen_subcommand_from ls' -l json -d 'print machine-readable output'
complete -c treemux -n '__fish_seen_subcommand_from setup' -s n -l dry-run -d 'print the config instead of writing it'
`
