# Design notes

Why treewright behaves the way it does. `README.md` is the user-facing tour and
`CLAUDE.md` is the map of the code; this is the reasoning behind the behavior —
the alternatives that were tried, and what made them lose.

Most of it also lives as comments next to the code it explains. Where the two
disagree, the code comment is the one being maintained.

---

## One session per repository

Every repository's windows live in a tmux session of its own, named after its
config: the base window in the main checkout, and one window per worktree.

```
tmux session "storefront"                      tmux session "checkout-api"
├── MAIN      ~/code/storefront                ├── MAIN    ~/code/checkout-api
├── ENG-2318  ~/code/storefront-eng-2318       └── PAY-88  ~/code/checkout-api-pay-88
└── ENG-2324  ~/code/storefront-eng-2324
```

The alternative — opening windows in whatever session the caller happens to be
attached to — mixes repositories in one status line, where two windows named
`MAIN` belong to different projects and a ticket key says nothing about which
checkout it is in. Worse, it made `resume` a silent no-op across sessions:
selecting a window in a session your client is not attached to succeeds and
changes nothing you can see.

What follows from it:

- **`new` creates the session** when it is not running yet, so the first command
  of the day establishes it. `resume` and `base` do the same.
- **`base` is the same window every time.** A window already sitting in the main
  checkout is selected rather than a second one opened beside it — and being the
  session's first window, it is what keeps the session alive as worktrees come
  and go.
- **Commands follow their window across sessions.** Resuming a worktree while
  attached to another repository's session switches you there.
- **Outside tmux nothing is skipped.** The session and window are created
  detached, and treewright says to run `treewright attach` — its own command
  rather than a `tmux attach` to copy, because that spelling names the session
  exactly and finds the right server under `TREEWRIGHT_TMUX_LABEL`.
- **A window in the wrong session is used rather than duplicated** — one you
  opened by hand, or one from before the repository had a session of its own.
  `ls` shows it as `session:window`, and `resume` switches to it there.

`tmux_session` overrides the name, for one already taken by something else or
two repositories that deliberately want to share. `tw doctor` reports which
session each repository maps to, and warns when two configs name the same one.

**Session names are matched as prefixes by tmux.** With only `checkout-api`
running, a target of `checkout` resolves to it, so a repo named `checkout` would
silently drop its windows into the other project's session. Every session target in
`internal/tmux` is therefore written in tmux's exact form, `=api`. The one
command that does not understand that form is `set-option`, which is why nothing
in that package sets session options.

## Window identity

Every window treewright opens carries the worktree it belongs to, as tmux user
options:

| Option | Value |
|---|---|
| `@treewright_repo` | The config's name. |
| `@treewright_worktree` | The checkout the window was opened on. |
| `@treewright_slug` | The worktree. Unset on the base window, which is not one. |
| `@treewright_branch` | The branch that worktree is on. |

Only the worktree is read back, and it is what *identifies* a window. A pane's
directory moves with every `cd`, and two windows can stand in one directory at
once — the base window does exactly that after `tw cd` — so which window a
worktree owns cannot be read off where its shell happens to be standing.

Before the stamp existed, `list-panes -a` walked windows in index order and two
windows standing in one directory resolved to whichever the user had arranged
first: a wrong name in `ls`, the wrong window focused by `resume`, and the wrong
window offered up for closing by `rm`, all changing under a `swap-window`. The
resolution order is now rank (stamped for this worktree beats unstamped beats
stamped for another), then the repository's own session, then the older window
id. See `claim.beats` in `internal/tmux/tmux.go`.

The options are written at creation, so a window treewright merely finds and
switches to keeps whatever it already had. The rest are there for your own status
line — `#{@treewright_repo}` costs nothing to render, where the alternative is
shelling out to git on every status interval. They keep the full `@treewright_`
prefix because they are a public, greppable interface, and a cryptic `@tw_` would
save nothing anyone types by hand.

## Terminal and tab titles

Attaching tends to leave the terminal tab titled with the command line that
attached rather than with the session. tmux is not the one writing it:
`set-titles` is off by default, so tmux never sets a title at all, and whatever
the shell wrote before it started — under kitty, iTerm2, or any terminal with
shell integration, the command being run — stays there until the next prompt,
which inside tmux never comes.

`tw tmux-init` turns it on, which is the one thing in that snippet not about
treewright:

```tmux
set -g set-titles on
set -g set-titles-string "#S: #W"
```

`#S` is the session, so the repository, and `#W` the window, so the worktree.

It is set there rather than by treewright itself, per session, for two reasons.
Session options are set with `set-option -t`, whose target does not accept the
exact-match `=name` form, so treewright would be back to the prefix matching the
section above avoids. And a title format is yours: a file you loaded on purpose
can change it, a `tw new` should not.

## Reaching treewright from inside a worktree

treewright runs your agent as the tmux window's own command, so a worktree's pane
*is* the agent — there is no shell in it to type `tw resume` into. Reaching
treewright from inside a worktree meant splitting a pane or going to find a
window that has a prompt. The key bindings close that gap by opening treewright
in a popup over whatever is running.

**Why the bindings go through `tw popup` rather than `display-popup` directly.**
tmux fixes a popup's size when it creates one, and `-w`/`-h` take only cells or a
percentage of the terminal. A percentage is the wrong unit for a picker, whose
height is the number of worktrees and whose width is the widest slug — neither of
which grows when the terminal does. Three worktrees need 83×8; 70%×60% of a
237×62 terminal is 165×37, ten times the area. Working the size out needs a
program, and `run-shell` is the only way a binding can run one.

**Why the bindings pass `#{client_tty}`.** A tmux command run from a backgrounded
`run-shell` has no association with the client that asked for it, so tmux falls
back to the most recently active one — and with two terminals attached, the popup
opens over whichever has been busier.

**Why the bindings pass `#{pane_current_path}`.** `run-shell` does not run in the
calling pane's directory. It runs in the tmux server's, which is wherever the
server was started — one repository's checkout, usually, and never the pane's.
Left to work out where it is, a popup therefore answers about that one repository
from every window on the server, and marks its worktree as the one you are
standing in. The visible symptom is the `ls` table putting its asterisk on the
same row whichever worktree the key was pressed in; the part that matters is that
`resume` in a second repository's window offers the first one's worktrees. It
hides behind the single-config fallback, which is why it survived so long: with
one config registered, `config.Resolve` returns it whatever directory you ask
from, and the wrong answer and the right one coincide.

`run-shell` has a `-c` flag that takes a start directory, and it is not the fix:
it will not expand a format into one, so `-c "#{pane_current_path}"` lands in the
home directory. The path has to travel inside the command string, where formats
are expanded, exactly as the client does. In the `new` binding its quotes are
escaped, that string already being the double-quoted argument of a `run-shell`
held by `command-prompt` — a directory can contain a space where a tty cannot, so
the quoting is load-bearing rather than decorative.

`--dir` is then acted on by moving into it, rather than being threaded through as
an argument, because everything downstream reads the working directory: sizing
resolves the config to count the worktrees, the popup starts where the process
stands, and the command inside it resolves the repository the same way again. The
process is put back afterwards, since the tests call `Run` in-process and a
command that wandered off and stayed there would decide where the next one thinks
it is.

**Why `-EE` and not `-E`.** A single `-E` closes the popup however the command
exited, so anything it reported on the way out is gone before it can be read.
Doubled, tmux closes it only on success. That is also why `PopupHint` exists:
inside a popup, a non-zero exit leaves text on screen with nothing saying how to
dismiss it.

**Why cancelling `resume` exits 0 but cancelling `cd` does not.** Declining a
menu is not a failure, and in a popup a non-zero exit made Escape need pressing
twice — once to dismiss the picker, once to clear the popup holding "cancelled"
on screen. But `cd`'s answer is a path, and `cd "$(tw cd)"` succeeding with
nothing to print would send the shell home. So the two callers treat the same
cancellation differently, deliberately.

**The keys.** Being unbound is the smaller half of choosing one; the larger half
is what a missed shift does, since these get reached for in a hurry. `t` is
clock-mode and `n` is next-window, both harmless. The first version bound
`prefix + W`, for the mnemonic with tmux's own `w` window picker — but a great
many configs rebind lowercase `w` to `kill-window`, and there a missed shift
destroys the very window the binding exists to reach. Hence `T` and `N`, and
hence the flags that move them.

Keys are validated as letters, digits, dashes, and underscores, which covers `G`,
`C-n`, `M-Left`, and `F5`. Anything with punctuation tmux reads as config syntax
is refused; writing that binding by hand in the printed file is the documented
way to have it.

## The base checkout

`resume` and `cd` list the main checkout above the worktrees, under the branch it
is parked on. It belongs there on both of the list's own terms: it is where you
land between worktrees — investigating, reviewing a pull request, asking an agent
to start the next piece of work — and, since a tmux session does not survive a
reboot while a checkout on disk does, it is something you reopen. Left out, the
one window that is always there, and that keeps the session alive, was the one
window the resume key could not reach.

It is not a worktree, and nothing pretends otherwise. In the code it is a
`choice` with a `Base` flag rather than a synthetic slug, because a fake slug
means nothing to `cfg.DirFor` and the first command to forget the difference
would be one that deletes something. `rm` and `prune` work off the worktrees
treewright created, so neither can name it; `ls --json` flags its row with
`"base": true` for anything reading the listing to decide where work should go.
Name it `base`, or name the branch it is on — exact matches only, since
stretching prefix resolution here would let a `b` that used to mean the `bugfix`
worktree quietly start meaning the base checkout.

**Two ways in, one window.** `tw base` opens it fresh with `command`; picking it
out of the resume menu runs `resume_command`, the same "carry on where I left
off" every other row gets — which after a reboot is the point. The difference
shows only on the first open, since every later call finds the window by its
directory and switches to it.

**Its status is `base`, outside the safe-to-remove scale.** A base checkout
sitting level with origin has no commits outside it, which would read as
`merged`: the green that means "safe to delete", about the one directory that
must never go. Its divergence column still means something, and something
slightly different — for the checkout parked on the base branch, it is how far
behind origin you are, and so whether what you are reading there is stale.

**In a repository with no worktrees yet**, the menu is that one row with "start
one with `prefix + N`" printed above it, while `ls` prints nothing at all. The
listing and the menu part company in that one state because they are for
different things: a menu is a way through, and must offer the base checkout
exactly when there is nothing else to offer, while a listing is an answer, and
"no worktrees" is the answer.

## Naming a worktree

`rm`, `resume` and `cd` take an unambiguous prefix of a slug, because a slug
carries both a ticket key and a description while people refer to that work by
the key alone:

```
$ tw cd eng-2318
eng-2318 matches worktree eng-2318-cart-total-rounding
```

The expansion is always reported rather than applied silently — `rm` is on that
list. An exact slug wins over any prefix, so a slug that is a prefix of another
stays reachable by its own name, and an ambiguous prefix is an error listing the
candidates rather than a guess.

Slugs are validated up front rather than left to git, so the answer is one
sentence naming the slug instead of several lines of git's advice about ref
formats arriving three steps deeper — by which point treewright has already
announced what it was about to do. A slug may not contain `/`, because a nested
one leaves a stray parent directory behind when the worktree is removed; the rest
of the rules are `git check-ref-format`'s.

A slug that already carries your `branch_prefix` has one copy stripped, and
treewright says so, rather than producing `john/john/eng-2318`. Only one copy:
if you really do want a slug named `john/eng-2318` under prefix `john/`,
stripping repeatedly would make it unreachable.

`new` reuses a branch that already exists rather than recreating it, which is
also how you get a worktree onto a colleague's pull request after fetching it.
Branches always fork from `origin/<base_branch>` — there is deliberately no flag
to base one on anything else, the point being that every worktree starts from the
same known-current place. When origin is unreachable, `new` says so and forks
from the local base branch.

## Output contract

stdout carries the answer and nothing else, so any command can be piped:

| Command | stdout |
|---|---|
| `new` | the new worktree's path — `cd "$(tw new eng-1)"` works |
| `cd` | the chosen worktree's path, so `cd "$(tw cd eng-1)"` works unaided |
| `rm` | the removed worktree's path |
| `prune` | the paths it removed, or would remove |
| `ls` | the table, or a JSON array with `--json` |
| `setup` | the config file's path, or the config itself with `--dry-run` |
| `config`, `doctor` | the report you asked for |
| `shell-init`, `tmux-init`, `help`, `version` | the script or text you asked for |

Progress, warnings, prompts, and errors go to stderr, prefixed `warning:` or
`error:` following git's convention, and unprefixed when it is just narration. So
`tw ls --json | jq` and `tw prune --yes > removed.txt` both stay clean.

Exit codes: `0` success, `1` the command ran and failed, `2` it was invoked
wrong. `doctor` exits `1` when a check fails, so it can gate a setup script.

Color is on only when writing to a terminal, and off under `NO_COLOR` or
`TERM=dumb`.

`--json` reports `ahead` and `behind` as `null` rather than `0` when the branch
cannot be compared to its base — an unknown is not a zero. An open window is
described with three fields, because they are consumed differently: `window` is
the name a human reads, `window_id` is what `tmux kill-window -t` takes, and
`window_session` is what `tmux attach -t` takes. All three are empty strings when
no window is open.

## Statuses

`ls` reports one status per worktree, in this precedence: `dirty` outranks
everything because it is the most easily lost, then `merged`, then `unpushed`; a
pushed-but-unmerged branch is `active`.

The counts shown — `dirty (3)`, `unpushed (2)` — are the numbers the removal
guards refuse over, so a listing says how much a `--force` would discard.

`ls` does not fetch. It changes no working tree, branch, or ref, so a branch that
landed since your last fetch still reads as `active`. `rm` and `prune` both fetch
before they judge, and so can disagree with a stale listing; they are the ones to
trust.

**Squash merges are recognized.** When a forge squash-merges a pull request, the
branch's own commits never land upstream — they are collapsed into one new commit
and the remote branch is deleted. A naive "are these commits upstream?" check
calls that landed work unpushed and refuses to clean it up. treewright instead
synthesizes a single commit holding the branch's whole tree on top of its
merge-base — the same patch a squash merge produces — and asks `git cherry`
whether an equivalent patch is already upstream.

That synthetic commit is written to the object database as a dangling object, so
**treewright needs write access to `.git` even for commands that only report.**
Its author, committer, and dates are fixed, so its hash depends only on the tree
and parent being tested: repeated runs reuse the same object rather than leaving
a new one behind each time, and `git gc` reaps it.

## Safety

`rm` refuses, absent `--force`, when the worktree has uncommitted changes or
commits reachable from no origin ref. It refreshes `origin/<base_branch>` first,
so a branch that merged moments ago is recognized as merged rather than tripping
the guard on a stale ref. `prune` only ever targets worktrees that are both
merged and clean.

A destructive command never acts on a name it had to guess at: a slug prefix must
match exactly one worktree, the expansion is printed, and anything ambiguous or
unknown is an error naming the alternatives. `setup` will not overwrite an
existing config, or add a second one for a repository already registered — which
would make the config that applies depend on registry order.

Removing a worktree leaves its tmux window pointing at a directory that is gone,
so `rm` offers to close it. The window is identified from the worktree's own path
rather than from wherever you ran the command, because a teardown is normally run
from somewhere else — closing "the window I am in" left the window named after
the worktree behind, still sitting in the deleted directory. It is closed even
when it turns out to be in another session, or when you are not in tmux at all.

`prune` asks per worktree it removed. Neither closes a window without asking
unless you pass `--yes` to `rm`, because a window may still have a session
running in it; with nobody to prompt — a script, an agent — both print the
`tmux kill-window` to run instead.

Closing a session's last window ends the session, which moves an attached client
elsewhere or detaches it, so the prompt says when that is what is about to
happen. Normally it is not: the base window outlives every worktree.

## Configuration

One TOML file per repository, in
`${TREEWRIGHT_CONFIG_DIR:-${XDG_CONFIG_HOME:-~/.config}/treewright/repos}/<name>.toml`.

TOML rather than a sourced shell script so that reading a config cannot execute
code: configs are meant to be shared, linted, and generated, none of which should
require trusting their author. Unknown keys are rejected outright, because a typo
like `base-branch` would otherwise be silently ignored, leaving you to wonder why
the base branch is still `main`.

Which config applies, in order: an explicit name; the config whose `main_dir` is
the repository you are standing in; the only config, when the registry holds
exactly one; otherwise an error listing the names. A broken config elsewhere in
the registry is skipped while matching rather than blocking work on a repository
whose own config is fine.

`main_dir` is resolved through symlinks, not merely cleaned. git reports fully
resolved paths for every worktree, so a `main_dir` that reaches the repo through
a symlink would never match what git says and its worktrees would be invisible.

`setup` writes the file with every guess commented, on the principle that the
file remains the record: it is a way to start one, not a layer above it. `config`
prints what is in force with defaults marked as such, because the gap between a
config file and the behavior it produces — invisible defaults, unexpanded paths,
and which of several configs applies — is where the confusion lives.

## The shell integration

treewright is a compiled binary, so it runs in its own process and cannot change
the calling shell's working directory. The wrapper function closes that gap: it
makes a temp file, passes its path in `$TREEWRIGHT_EVAL_FILE`, and sources it
after treewright exits. Two commands write to it — `cd`, and `rm` when your shell
is standing in the directory being deleted. Everything must still behave
correctly when the file is never sourced, which is why those commands also print
the `cd` to run.

The shims are emitted by the binary rather than installed as files, so they can
never drift out of sync with it — the same approach fzf, zoxide, direnv, and
starship take, and for the same reason. The commands written to the eval file are
restricted to what zsh, bash, and fish all parse identically, so one writer serves
every shell.

Every external program the wrappers call is invoked through `command`, because
zsh and bash expand aliases in a function body when the function is *defined*: an
`alias rm='rm -i'` in a startup file would otherwise rewrite the wrapper's own
`rm -f`. It is also why the migration advice is to write
`eval "$(command treewright shell-init zsh)"` when a shell function named
`treewright` already exists — `command` skips functions, so the line cannot ask
the thing being replaced for its own replacement.

**`tw` and `TREEWRIGHT_ARGV0`.** `tw` calls the `treewright` *function*, resolved
at call time, so the eval-file protocol works identically under either name. That
function runs the binary as `command treewright`, which erases the typed name
from `argv[0]` — so `tw` exports `TREEWRIGHT_ARGV0=tw` for just that call, and
help and every runtime hint answer in the name the user actually typed. What
keeps the canonical name is help prose and anything destined for a file —
tmux.conf lines, shell startup evals — which programs read and shell functions
never reach.
