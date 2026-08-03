# treewright shell integration for fish. Load with: treewright shell-init fish | source

# Which treewright emitted the wrapper below, for "treewright doctor" to compare
# against itself. A shell keeps whatever it loaded at start, and a binary cannot
# read its parent's function table, so this is the only way the two can be told
# apart. Exported because doctor is a child process.
set -gx TREEWRIGHT_SHELL_INIT_VERSION "{{version}}"

function treewright
    # ${TMPDIR:-/tmp}, as the other shims spell it: the fallback has to cover a
    # TMPDIR that is set but empty as well as one that is unset. `set -q` is
    # true for a defined-but-empty variable, so testing with it aimed the temp
    # file at "/treewright-eval.XXXXXX" in the root, where the failure — and
    # the `or return 1` after it — took every invocation down with it.
    set -l tmp $TMPDIR
    if test -z "$tmp"
        set tmp /tmp
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
complete -c treewright -n __fish_use_subcommand -a move       -d 'move uncommitted work out of the main checkout into a new worktree'
complete -c treewright -n __fish_use_subcommand -a resume     -d 'reopen a window on an existing worktree'
complete -c treewright -n __fish_use_subcommand -a send       -d 'type one line at the agent in a worktree window'
complete -c treewright -n __fish_use_subcommand -a cd         -d 'move your shell into a worktree'
complete -c treewright -n __fish_use_subcommand -a base       -d 'open a window on the main checkout'
complete -c treewright -n __fish_use_subcommand -a attach     -d 'attach this terminal to the repository tmux session'
complete -c treewright -n __fish_use_subcommand -a popup      -d 'run a treewright command in a tmux popup sized to its output'
complete -c treewright -n __fish_use_subcommand -a signal     -d 'record the state of the agent running in this worktree'
complete -c treewright -n __fish_use_subcommand -a guard      -d 'refuse a tool call that would mutate another worktree'
complete -c treewright -n __fish_use_subcommand -a ls         -d 'list worktrees with their status'
complete -c treewright -n __fish_use_subcommand -a rm         -d 'tear down a worktree and its branch'
complete -c treewright -n __fish_use_subcommand -a prune      -d 'remove every merged, clean worktree'
complete -c treewright -n __fish_use_subcommand -a close      -d 'close the tmux window open on a worktree'
complete -c treewright -n __fish_use_subcommand -a setup      -d 'write a config for the repository you are standing in'
complete -c treewright -n __fish_use_subcommand -a config     -d 'print the settings in force, defaults included'
complete -c treewright -n __fish_use_subcommand -a doctor     -d 'check the installation and every registered config'
complete -c treewright -n __fish_use_subcommand -a shell-init -d 'print the shell integration'
complete -c treewright -n __fish_use_subcommand -a tmux-init  -d 'print the tmux integration'
complete -c treewright -n __fish_use_subcommand -a agent-init -d 'install the plugin that wires an agent to treewright'
complete -c treewright -n __fish_use_subcommand -a refresh    -d 'bring every checkout and the tmux server up to date with this treewright'
complete -c treewright -n __fish_use_subcommand -a version    -d 'print the version, and with --check say whether a newer one is out'
complete -c treewright -n '__fish_seen_subcommand_from new move' -a '(command treewright __complete prefixes)' -d 'branch prefix'
complete -c treewright -n '__fish_seen_subcommand_from rm' -a '(command treewright __complete slugs)'
complete -c treewright -n '__fish_seen_subcommand_from resume cd send close' -a '(command treewright __complete targets)'
complete -c treewright -n '__fish_seen_subcommand_from ls prune base config attach refresh' -a '(command treewright __complete repos)'
complete -c treewright -n '__fish_seen_subcommand_from shell-init' -a '(command treewright __complete shells)'
complete -c treewright -n '__fish_seen_subcommand_from signal' -a '(command treewright __complete states)'
complete -c treewright -n '__fish_seen_subcommand_from agent-init' -a '(command treewright __complete agents)'
complete -c treewright -n '__fish_seen_subcommand_from rm' -s f -l force -d 'remove even when unsaved work would be lost'
complete -c treewright -n '__fish_seen_subcommand_from rm' -s y -l yes -d 'do not ask before closing the tmux window'
complete -c treewright -n '__fish_seen_subcommand_from prune' -s y -l yes -d 'actually remove them, instead of listing'
complete -c treewright -n '__fish_seen_subcommand_from ls' -l json -d 'print machine-readable output'
complete -c treewright -n '__fish_seen_subcommand_from setup' -s n -l dry-run -d 'print the config instead of writing it'
complete -c treewright -n '__fish_seen_subcommand_from setup' -l refresh -d 'regenerate an existing config in place'
complete -c treewright -n '__fish_seen_subcommand_from send' -s n -l dry-run -d 'show the pane and send nothing'
complete -c treewright -n '__fish_seen_subcommand_from version' -l check -d 'say whether a newer treewright has been released'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l apply -d 'load it into the running tmux server'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l resume-key -r -d 'prefix key that switches worktrees'
complete -c treewright -n '__fish_seen_subcommand_from tmux-init' -l new-key -r -d 'prefix key that starts a worktree'
complete -c treewright -n '__fish_seen_subcommand_from new move' -s p -l prompt -r -d 'text the agent starts working on'
complete -c treewright -n '__fish_seen_subcommand_from move' -l keep -d 'leave the work in the main checkout as well'
complete -c treewright -n '__fish_seen_subcommand_from resume' -s p -l prompt -r -d 'text for the resumed agent'
complete -c treewright -n '__fish_seen_subcommand_from resume' -l fresh -d 'run command rather than resume_command'
# -F is what turns the argument back into a filename completion: the file is on
# the caller's disk, which treewright knows nothing about.
complete -c treewright -n '__fish_seen_subcommand_from new move resume' -l prompt-file -r -F -d 'a file holding the brief'
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
