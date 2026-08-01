# treewright shell integration for fish. Load with: treewright shell-init fish | source
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
complete -c treewright -n __fish_use_subcommand -a signal     -d 'record the state of the agent running in this worktree'
complete -c treewright -n __fish_use_subcommand -a ls         -d 'list worktrees with their status'
complete -c treewright -n __fish_use_subcommand -a rm         -d 'tear down a worktree and its branch'
complete -c treewright -n __fish_use_subcommand -a prune      -d 'remove every merged, clean worktree'
complete -c treewright -n __fish_use_subcommand -a setup      -d 'write a config for the repository you are standing in'
complete -c treewright -n __fish_use_subcommand -a config     -d 'print the settings in force, defaults included'
complete -c treewright -n __fish_use_subcommand -a doctor     -d 'check the installation and every registered config'
complete -c treewright -n __fish_use_subcommand -a shell-init -d 'print the shell integration'
complete -c treewright -n __fish_use_subcommand -a tmux-init  -d 'print the tmux integration'
complete -c treewright -n __fish_use_subcommand -a agent-init -d 'install the plugin that wires an agent to treewright'
complete -c treewright -n '__fish_seen_subcommand_from new' -a '(command treewright __complete prefixes)' -d 'branch prefix'
complete -c treewright -n '__fish_seen_subcommand_from rm' -a '(command treewright __complete slugs)'
complete -c treewright -n '__fish_seen_subcommand_from resume cd' -a '(command treewright __complete targets)'
complete -c treewright -n '__fish_seen_subcommand_from ls prune base config attach' -a '(command treewright __complete repos)'
complete -c treewright -n '__fish_seen_subcommand_from shell-init' -a '(command treewright __complete shells)'
complete -c treewright -n '__fish_seen_subcommand_from signal' -a '(command treewright __complete states)'
complete -c treewright -n '__fish_seen_subcommand_from agent-init' -a '(command treewright __complete agents)'
complete -c treewright -n '__fish_seen_subcommand_from rm' -s f -l force -d 'remove even when unsaved work would be lost'
complete -c treewright -n '__fish_seen_subcommand_from rm' -s y -l yes -d 'do not ask before closing the tmux window'
complete -c treewright -n '__fish_seen_subcommand_from prune' -s y -l yes -d 'actually remove them, instead of listing'
complete -c treewright -n '__fish_seen_subcommand_from ls' -l json -d 'print machine-readable output'
complete -c treewright -n '__fish_seen_subcommand_from setup' -s n -l dry-run -d 'print the config instead of writing it'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l apply -d 'load it into the running tmux server'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l resume-key -r -d 'prefix key that switches worktrees'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l new-key -r -d 'prefix key that starts a worktree'
complete -c treewright -n '__fish_seen_subcommand_from new' -s p -l prompt -r -d 'text the agent starts working on'
complete -c treewright -n '__fish_seen_subcommand_from resume' -s p -l prompt -r -d 'text for the resumed agent'
complete -c treewright -n '__fish_seen_subcommand_from agent-init' -l global -d 'install it for every repository instead of this one'
complete -c treewright -n '__fish_seen_subcommand_from agent-init' -l print -d 'print the plugin files instead of installing them'

# tw calls the treewright function, and --wraps inherits its completions. The
# function runs the binary as "command treewright", which erases the name the
# user typed from argv[0] — so tw reports it in TREEWRIGHT_ARGV0 instead, and
# help and hints answer as "tw".
function tw --wraps treewright
    set -lx TREEWRIGHT_ARGV0 tw
    treewright $argv
end
