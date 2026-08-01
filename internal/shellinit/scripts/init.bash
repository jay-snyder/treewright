# treewright shell integration for bash. Load with: eval "$(treewright shell-init bash)"
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
    COMPREPLY=($(compgen -W "new resume cd base attach popup signal ls rm prune setup config doctor shell-init tmux-init agent-init" -- "$cur"))
    return
  fi
  if [[ "$cur" == -* ]]; then
    COMPREPLY=($(compgen -W "$(command treewright __complete flags "${COMP_WORDS[1]}" 2>/dev/null)" -- "$cur"))
    return
  fi
  local candidates=""
  case "${COMP_WORDS[1]}" in
    new)
      candidates="$(command treewright __complete prefixes 2>/dev/null)"
      # A branch prefix is half a word — the slug is typed straight onto it — so the
      # space bash appends would have to be deleted again. compopt arrived in bash 4;
      # under the 3.2 that macOS ships, the space simply stays.
      type compopt >/dev/null 2>&1 && compopt -o nospace
      ;;
    rm)                          candidates="$(command treewright __complete slugs 2>/dev/null)" ;;
    resume|cd)                   candidates="$(command treewright __complete targets 2>/dev/null)" ;;
    ls|prune|base|config|attach) candidates="$(command treewright __complete repos 2>/dev/null)" ;;
    shell-init)                  candidates="$(command treewright __complete shells 2>/dev/null)" ;;
    signal)                      candidates="$(command treewright __complete states 2>/dev/null)" ;;
    agent-init)                  candidates="$(command treewright __complete agents 2>/dev/null)" ;;
  esac
  COMPREPLY=($(compgen -W "$candidates" -- "$cur"))
}
# tw calls the treewright *function*, resolved at call time, so the eval-file
# protocol works identically under either name. That call runs the binary as
# "command treewright", which erases the name the user typed from argv[0] — so
# tw reports it in TREEWRIGHT_ARGV0 instead, and help and hints answer as "tw".
tw() { local -x TREEWRIGHT_ARGV0=tw; treewright "$@"; }
complete -F _treewright_completions treewright tw
