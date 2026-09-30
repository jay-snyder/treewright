# treewright shell integration for zsh. Load with: eval "$(treewright shell-init zsh)"

# Which treewright emitted the wrapper below, and for which shell. A shell keeps
# whatever it loaded at start, and a binary cannot read its parent's function
# table, so these are the only way either can be asked: "treewright doctor"
# compares the first against itself, and "treewright refresh" reads the second
# to name the line that reloads a shell it cannot reach. Exported because both
# are child processes.
export TREEWRIGHT_SHELL_INIT_VERSION="{{version}}"
export TREEWRIGHT_SHELL_INIT_SHELL=zsh

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
    'move:move uncommitted work out of the main checkout into a new worktree'
    'resume:reopen a window on an existing worktree'
    'send:type one line at the agent in a worktree window'
    'cd:move your shell into a worktree'
    'base:open a window on the main checkout'
    'scratch:open another window on the main checkout, under a name of its own'
    'restore:reopen every window of a repository after a restart, and attach'
    'popup:run a treewright command in a tmux popup sized to its output'
    'attach:attach this terminal to the repository tmux session'
    'signal:record the state of the agent running in this worktree'
    'guard:refuse a tool call that would mutate another worktree'
    'session-start:run the optional features this repository switched on'
    'ls:list worktrees with their status'
    'rm:tear down a worktree and its branch'
    'prune:remove every merged, clean worktree'
    'close:close the tmux window open on a worktree'
    'setup:write a config for the repository you are standing in'
    'config:print the settings in force, defaults included'
    'doctor:check the installation and every registered config'
    'shell-init:print the shell integration'
    'tmux-init:print the tmux integration'
    'agent-init:install the plugin that wires an agent to treewright'
    'refresh:bring every checkout and the tmux server up to date with this treewright'
    'version:print the version, and with --check say whether a newer one is out'
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
  # --prompt-file takes a path, and treewright knows nothing about the caller's
  # filesystem: the shell's own file completer is the only sensible candidate
  # list, so the flag's value is answered before the command's arguments are.
  if [[ "$words[CURRENT-1]" == --prompt-file ]]; then
    _files
    return
  fi
  # --repo takes a registered config name on any command, which is the same list
  # the repo-taking commands complete their positional from.
  if [[ "$words[CURRENT-1]" == --repo ]]; then
    compadd -- ${(f)"$(command treewright __complete repos 2>/dev/null)"}
    return
  fi
  case "$words[2]" in
    new|move)                    compadd -S '' -- ${(f)"$(command treewright __complete prefixes 2>/dev/null)"} ;;
    rm)                          compadd -- ${(f)"$(command treewright __complete slugs 2>/dev/null)"} ;;
    resume|cd|send|close)        compadd -- ${(f)"$(command treewright __complete targets 2>/dev/null)"} ;;
    ls|prune|base|restore|attach|config|refresh) compadd -- ${(f)"$(command treewright __complete repos 2>/dev/null)"} ;;
    shell-init)                  compadd -- ${(f)"$(command treewright __complete shells 2>/dev/null)"} ;;
    signal)                      compadd -- ${(f)"$(command treewright __complete states 2>/dev/null)"} ;;
    agent-init)                  compadd -- ${(f)"$(command treewright __complete agents 2>/dev/null)"} ;;
  esac
}
# tw calls the treewright *function*, resolved at call time, so the eval-file
# protocol works identically under either name. That call runs the binary as
# "command treewright", which erases the name the user typed from argv[0] — so
# tw reports it in TREEWRIGHT_ARGV0 instead, and help and hints answer as "tw".
tw() { local -x TREEWRIGHT_ARGV0=tw; treewright "$@" }
# A compdef can exist where compinit never ran: autoloaded by hand, or copied into
# a snapshot of a shell's functions. Calling it there fails with an error about
# zsh's internals. Asking whether compinit ran instead ($+_comps) would skip
# that, and would also skip the compdef that znap and zcomet define to queue
# registrations until they run compinit themselves — znap only at the first
# prompt, after every line of .zshrc. Nothing short of the call tells those
# apart, so the call is made and its complaint dropped.
(( $+functions[compdef] )) && compdef _treewright treewright tw 2>/dev/null
