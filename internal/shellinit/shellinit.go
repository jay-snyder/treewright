// Package shellinit produces the shell integration treemux needs.
//
// treemux is a compiled binary, so it runs in its own process and cannot change
// the calling shell's working directory. Two small things therefore have to live
// in the shell itself: a wrapper function that lets treemux hand back a command
// to run (see the eval-file protocol in internal/cli), and tab completion.
//
// Rather than installing files per shell, the binary prints its own integration:
//
//	eval "$(treemux init zsh)"     # or bash
//	treemux init fish | source
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
// binary wrote anything. Only `treemux rm` writes to it today, and only when
// the shell is standing in the directory being deleted.

const zshScript = `# treemux shell integration for zsh. Load with: eval "$(treemux init zsh)"
# Note: rc, not status — status is a special parameter in zsh and cannot be a local.
treemux() {
  local evalfile rc
  evalfile="$(mktemp "${TMPDIR:-/tmp}/treemux-eval.XXXXXX")" || return 1
  # "command" skips this function and runs the real binary.
  TREEMUX_EVAL_FILE="$evalfile" command treemux "$@"
  rc=$?
  [[ -s "$evalfile" ]] && source "$evalfile"
  rm -f "$evalfile"
  return $rc
}

_treemux() {
  local -a cmds
  cmds=(
    'new:create a worktree and branch, and open a tmux window in it'
    'rm:tear down a worktree and its branch'
    'ls:list worktrees with their status'
    'prune:remove every merged, clean worktree'
    'resume:reopen a window on an existing worktree'
    'base:open a window on the main checkout'
    'init:print the shell integration'
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
    rm|resume)     compadd -- ${(f)"$(command treemux __complete slugs 2>/dev/null)"} ;;
    ls|prune|base) compadd -- ${(f)"$(command treemux __complete repos 2>/dev/null)"} ;;
    init)          compadd -- ${(f)"$(command treemux __complete shells 2>/dev/null)"} ;;
  esac
}
(( $+functions[compdef] )) && compdef _treemux treemux
`

const bashScript = `# treemux shell integration for bash. Load with: eval "$(treemux init bash)"
treemux() {
  local evalfile rc
  evalfile="$(mktemp "${TMPDIR:-/tmp}/treemux-eval.XXXXXX")" || return 1
  # "command" skips this function and runs the real binary.
  TREEMUX_EVAL_FILE="$evalfile" command treemux "$@"
  rc=$?
  [[ -s "$evalfile" ]] && source "$evalfile"
  rm -f "$evalfile"
  return $rc
}

_treemux_completions() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=($(compgen -W "new rm ls prune resume base init" -- "$cur"))
    return
  fi
  if [[ "$cur" == -* ]]; then
    COMPREPLY=($(compgen -W "$(command treemux __complete flags "${COMP_WORDS[1]}" 2>/dev/null)" -- "$cur"))
    return
  fi
  local candidates=""
  case "${COMP_WORDS[1]}" in
    rm|resume)     candidates="$(command treemux __complete slugs 2>/dev/null)" ;;
    ls|prune|base) candidates="$(command treemux __complete repos 2>/dev/null)" ;;
    init)          candidates="$(command treemux __complete shells 2>/dev/null)" ;;
  esac
  COMPREPLY=($(compgen -W "$candidates" -- "$cur"))
}
complete -F _treemux_completions treemux
`

const fishScript = `# treemux shell integration for fish. Load with: treemux init fish | source
function treemux
    set -l tmp /tmp
    if set -q TMPDIR
        set tmp $TMPDIR
    end
    set -l evalfile (mktemp $tmp/treemux-eval.XXXXXX)
    or return 1
    # "command" skips this function and runs the real binary.
    set -lx TREEMUX_EVAL_FILE $evalfile
    command treemux $argv
    set -l saved $status
    if test -s $evalfile
        source $evalfile
    end
    rm -f $evalfile
    return $saved
end

complete -c treemux -f
complete -c treemux -n __fish_use_subcommand -a new    -d 'create a worktree and branch, and open a tmux window in it'
complete -c treemux -n __fish_use_subcommand -a rm     -d 'tear down a worktree and its branch'
complete -c treemux -n __fish_use_subcommand -a ls     -d 'list worktrees with their status'
complete -c treemux -n __fish_use_subcommand -a prune  -d 'remove every merged, clean worktree'
complete -c treemux -n __fish_use_subcommand -a resume -d 'reopen a window on an existing worktree'
complete -c treemux -n __fish_use_subcommand -a base   -d 'open a window on the main checkout'
complete -c treemux -n __fish_use_subcommand -a init   -d 'print the shell integration'
complete -c treemux -n '__fish_seen_subcommand_from rm resume' -a '(command treemux __complete slugs)'
complete -c treemux -n '__fish_seen_subcommand_from ls prune base' -a '(command treemux __complete repos)'
complete -c treemux -n '__fish_seen_subcommand_from init' -a '(command treemux __complete shells)'
complete -c treemux -n '__fish_seen_subcommand_from rm' -s f -l force -d 'remove even when unsaved work would be lost'
complete -c treemux -n '__fish_seen_subcommand_from rm' -s y -l yes -d 'do not ask before closing the tmux window'
complete -c treemux -n '__fish_seen_subcommand_from prune' -s y -l yes -d 'actually remove them, instead of listing'
complete -c treemux -n '__fish_seen_subcommand_from ls' -l json -d 'print machine-readable output'
`
