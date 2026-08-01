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
| `@treewright_agent_state` | What the agent in it last signaled. Written by `signal`, not at creation — see "Agent state" below. |

Of these, the worktree is what *identifies* a window. A pane's
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

**Why a binding that cannot find treewright says nothing at all.** tmux resolves
the command in the server's environment, which is whatever the server was started
from rather than the shell that edited the config, and it discards what a
`run-shell` at config load reports. A binary that is not on that `PATH` therefore
produces no bindings and no message — the keys simply do nothing, which reads as a
treewright bug rather than an installation one. Two things compound it: a server
keeps the environment it started with, so making the binary reachable afterwards
takes a `kill-server` rather than a new window; and the check that would report it,
`checkTmuxIntegration`, has to skip when no server is running, because `list-keys`
would start the very server whose emptiness it then described. So the one session
where the keys never loaded is also the one where nothing says so.

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

## Branch prefixes

What you type is `[prefix]slug`: a leading run matching a configured prefix names
that prefix, and the rest is the slug. Under a single `branch_prefix` that is just
the correction it always was — `tw new john/eng-2318` gives `john/eng-2318` rather
than `john/john/eng-2318`, and treewright says it stripped a copy. Only one copy:
if you really do want a slug named `john/eng-2318` under prefix `john/`, stripping
repeatedly would make it unreachable.

Under `branch_prefixes`, the same rule is how you choose. Teams that namespace by
kind of work rather than by person list several — `["feature/", "bug/", "chore/"]`
— and `tw new bug/eng-2318` branches `bug/eng-2318`. A bare slug takes the first
in the list, which is why the list's order is the setting and not an accident of
it. The longest match wins, so `feature/` and `feature/exp/` can coexist. A prefix
the list does not contain is an error naming the ones it does, rather than a new
namespace invented on the spot: a branch pushed under a misspelled prefix looks
fine locally and is invisible to whatever the team's tooling watches.

One flat list, and no composition: someone who wants `alice/feature/` writes that
literally. Composing a personal prefix with a kind-of-work prefix would be a rule
to learn in a file whose whole point is that it is data.

The prefix reaches the branch and stops there. The worktree stays
`<repo>-eng-2318`, the window stays `ENG-2318`, and `resume`, `cd` and `rm` still
take the slug alone. Folding the kind of work into the directory name was the
alternative, and it loses on all three of its own terms: `ticket_pattern` stops
matching (`feature-eng-142-white-screen` has no `[a-z]+-[0-9]+` at its head, so
every window would be called `FEATURE-EN...`), every row of the table grows a word
that repeats down the column, and you would have to remember which kind of work
something was to reopen it — a question git already answers. Two worktrees whose
slugs are both `auth` therefore collide whatever their prefixes, and `new` says so
and names the command that opens the one that exists. Slugs that carry a ticket
key never reach that case, and two pieces of work that really are both called
`auth` are ambiguous to the person reading the list too.

Because the prefix is only ever *written* at `new` — every other command reads the
branch back from git — a worktree's branch does not have to match what its slug
would produce today. Renaming a prefix in the config leaves the worktrees created
under the old one working, listed, and removable.

The two spellings are one setting, and a config may not use both. Any precedence
we picked would be a rule to learn, and the file itself would no longer say which
prefix a branch gets. `config` reports the row under whichever spelling the file
used, so the line it names is the line that is there.

Prefixes are checked when the config is read, not when a branch is created — the
same restatement of `git check-ref-format` that validates a slug, in
`internal/refname`, so the two halves of a branch name are held to one set of
rules. A prefix is hand-written, often several at a time, and the alternative is
git refusing a branch three steps into a `new` that has already announced what it
was doing, about a value the user last looked at when they wrote the file. Because
every command loads the config, `doctor` reports it too, naming the offending entry
rather than the key.

Both checks are tested against the real `git check-ref-format`, which is the only
way a restatement stays honest: reading the rules off the documentation had a
component ending in a dot wrong (`feature./eng-1` is legal, and looks the least
legal of any of them). Two divergences are deliberate and declared in the tests: a
slug may not contain `/`, because of the stray parent directory, and neither half
may start with `-`, which git allows in a ref and every command that takes flags
does not.

### Guessing them at setup

`setup` counts the leading namespace of every branch on origin — not of the local
branches, so a fresh clone with none of its own still sees the convention — and
proposes the ones that name a kind of work, most used first. The most used becomes
the default a bare slug gets, and the counts are printed beside the list so the
ordering the config depends on is visible rather than asserted. Two branches make
a namespace worth proposing; one is an incident.

Only names from a built-in vocabulary (`feature`, `bug`, `hotfix`, `chore`, …)
qualify, and that is the load-bearing decision. `alice/x, alice/y, bob/z` and
`feature/x, feature/y, bug/z` are the same shape, so counting alone cannot tell a
per-person scheme from a per-kind one — and mistaking the first for the second
writes colleagues' names into your config as kinds of work, then sends every branch
you make into somebody else's namespace. The opposite mistake costs nothing: an
unrecognized scheme leaves the git-email guess and a commented example, which is
where this started. So the vocabulary is a floor, not a filter — a team using
`squad-a/` writes it in, once.

Only `/` delimits a namespace. `feature-eng-1` and `eng-142-white-screen` are the
same shape too, and reading `eng-` as a namespace would be wrong far more often
than right. A dashed convention is a one-line edit; a wrong guess is a branch
pushed somewhere nobody is looking.

What origin says beats what the git email says, including when origin yields a
single prefix — a repo where every branch is a `feature/` means new work is a
`feature/` too. The email guess exists to make branches attributable on a shared
remote, and a repo that already namespaces has answered that question its own way.

## Creating a branch

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
| `agent-init` | the hooks fragment alone, or the skill with `--skill` — pipeable either way, with where to put it on stderr |
| `signal` | nothing — the answer is the stamp on the window, and out of scope it is silent on stderr too |

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

## Agent state

treewright's whole point is several agents running in parallel, and the question
the ls table could not answer from git alone is *which one needs me?* An agent
blocked on a permission prompt looks exactly like one deep in a refactor: a
window name in a status line. `signal` closes that gap, and it is one half of a
deliberate split: treewright provides the agent-neutral protocol, and each
agent's own hook configuration provides the wiring that calls it. Nothing in
treewright knows any agent's name.

**The vocabulary is closed, and chosen from the human's chair**: `working`
(leave it alone), `waiting` (blocked on you), `done` (a result to look at), and
`clear` (no agent state). Free-form states lose the way free-form branch
prefixes would: every consumer — the AGENT column, its colors, the name marker —
needs to know what a state *means*, and a vocabulary nobody shares is a column
nobody can read.

**State lives on the window, not on disk.** A treewright window runs the agent
as the window's own command, so the agent dying *is* the window closing *is*
the option evaporating: no marker file to garbage-collect, no stale "working"
from an agent that crashed days ago. The post_create marker went the other way —
a file beside the log — because a failed install outlives any window; an agent's
state never outlives its window, except in one case treewright itself creates:
a failed command's window is deliberately held open so its output stays
readable, and there the hold-open wrapper clears the state and the name marker
itself, best-effort, straight through tmux.

**`signal` never makes noise.** It exits 0 and prints nothing everywhere except
being invoked wrong: outside tmux, outside a registered repository, in a
checkout with no window — silence, not a warning. The hooks that run it fire in
*every* session the agent has — plain terminals, ssh, repositories treewright
has never heard of — and each of those is out of scope, not broken. This is the
one command deliberately outside the "a background failure needs somewhere to be
reported" rule, and the reason is that a no-op signal is not a failure; a hook
that warns is an integration that nags, and those get ripped out.

**`waiting` — only waiting — marks the window's name** (`!ENG-2318`), because it
is the one state that needs to reach you across the room, and a name shows in
any status line with nothing added to tmux.conf. The alternatives lose
concretely: setting `window-status-format` overwrites a format that is the
user's own — the same reasoning that keeps `set-titles` in the tmux-init snippet
rather than in the binary — and ringing the pane's bell requires being the pane.
The marker is display, not identity: `Windows` strips it as it parses, so every
message, table cell, and JSON field carries the name underneath, and only
windows treewright opened (stamped ones) are ever decorated — a window the user
opened by hand on a worktree's directory keeps its name however loudly its agent
waits. A custom status line can render `#{@treewright_agent_state}` directly,
like the other `@treewright_` options.

**Nothing clears a state on focus.** Switching to a waiting window is arrival,
not help; the agent's own next transition is the truth, and the protocol stays a
one-way street — agents write, treewright displays.

**The AGENT column appears only when some window carries a state**, the same
rule as the current-worktree marker column and for a stronger reason: for the
user whose `command` is `nvim`, the whole feature is invisible, which is the
agent-agnosticism promise kept in the table itself. `--json` always carries
`agent_state`, empty when nothing has signaled — a consumer's schema should not
depend on whether anyone happens to be signaling today.

**Two option names are reserved, not designed**, so nothing else squats on
them while the states prove whether they are enough:
`@treewright_agent_message` for a short "why" (the permission being asked for,
from a hook's stdin payload), and `@treewright_agent_since` for when the state
was set ("waiting for 25m" in `ls`).

## Agent modules

`signal` is half of a deliberate split: treewright's core provides agent-neutral
protocols, and everything that is a fact about a *particular* agent — what
launches it, what resumes it, which gitignored files hold its per-project
state, how its hooks are wired to `signal` — lives in `internal/agentinit`, one
module per agent. Supporting another agent is a file beside `claude.go`, not
edits across the tree. The modules are the third instance of a pattern the repo
already had twice: like the shell shims and the tmux snippet, the hook
configuration is emitted by the binary (`agent-init claude`), so it can never
drift from the `signal` vocabulary it targets.

**`agent-init` prints; it does not apply.** tmux-init's `--apply` is safe
because `source-file` is additive; applying hooks means rewriting a settings
file the user owns, which a JSON merge would reorder and reformat. Printing the
fragment and naming the file is the honest version until there is a merge
strategy worth trusting.

**Where the hooks go, and the trap between the placements.** User-level
(`~/.claude/settings.json`) covers every repository and every worktree at once,
and costs nothing because `signal` silently no-ops outside treewright windows —
that contract is what makes the global placement free. Per-repo means the main
checkout's `.claude/settings.local.json`, which git ignores — so **every
worktree treewright creates starts without it**, and hooks placed there fire in
the MAIN window and in no worktree at all unless something carries the file.
That half-configured state looks finished, which is why `doctor` checks for
exactly it. The committed project file would work mechanically and is not
offered: it imposes treewright on every teammate who clones the repo.

**The `agent` config key is a defaults bundle, and the carry is the point.**
`agent = "claude"` names a module and takes its command and resume_command as
defaults — explicit keys still win field-by-field, so agent-plus-command is
override rather than a load error; the file still says which command runs. What
the key adds that has no other spelling is the carry: the module's local-state
files are copied into every new worktree as if `carry_files` listed them, which
dissolves the placement trap and gives every worktree the "always allow"
permission decisions already granted in the main checkout, hooks or no hooks.
The implicit carry differs from an explicit entry in exactly one way — absent
from the main checkout it is skipped silently, because unlike a `carry_files`
entry nobody asserted it exists. An unknown agent name is a load error naming
the modules there are: the key's whole value is the module it names, and a
misspelling that fell back to the global defaults would leave the carry quietly
not happening.

**The module is never inferred from `command`.** Two configs saying
`command = "claude"` behaving differently on an invisible guess is the kind of
rule the config format exists to avoid; the key is one line, and writing it is
what asks for the bundle. The one place a guess is allowed is `doctor`'s
wiring check, which may sniff the command's first word — a warn-level hint can
rest on a guess in a way behavior never can. `setup` writes the key when it
finds the agent's binary on PATH, which is as close to consent as detection
gets — and one commented line to remove when it guessed wrong.

**The skill is the module's second artifact, and its knowledge is core's.**
`agent-init --skill` prints a document teaching the agent to *drive*
treewright — read the estate with `ls --json`, spawn parallel work with `new`
and a `--prompt`, respect the teardown guards, leave `signal` to the hooks.
The text lives in `internal/agentinit` as a shared, agent-neutral guide, with
each module contributing only its packaging (for claude, SKILL.md frontmatter
and `~/.claude/skills/treewright/SKILL.md`): the CLI is the same whoever runs
it, so a second agent wraps the same words. It needed no new core surface at
all, which is the sign the protocol boundary was drawn right — and it is held
to the code by tests, so a command it names cannot be renamed out from under
it. Its commands spell `treewright`, never `tw`, and here the Argv0 rule bites
hardest: the reader is an agent running commands in a non-interactive shell,
where the `tw` function from a startup file may simply not exist.

## The kickoff prompt

`new` used to open a window whose agent sat waiting to be told what the ticket
was, when the person typing the command knew perfectly well. `--prompt` closes
that gap on `new` and `resume` both, and the mechanism is a placeholder in the
command template — `command = "claude {prompt}"` — because where an agent's
CLI wants its prompt is a fact about the agent, and the template saying so is
what keeps treewright out of guessing.

Three rules, each load-bearing:

- **With a prompt, the placeholder becomes the shell-quoted text**: one
  literal argument however many spaces and quotes the prompt holds, through
  the same `shellQuote` the eval file trusts.
- **Without one, the placeholder is removed entirely — never substituted as
  `''`.** An empty argument is not the absence of an argument: to most agents
  it is an instruction, an empty one, and `claude ''` opening every window
  with a blank prompt to answer is the bug this rule avoids. Every consumer of
  `command` and `resume_command` fills the template, `base` included, or the
  literal `{prompt}` would leak into a shell line.
- **A prompt aimed at a template with no placeholder is refused, not
  appended.** Appending happens to be right for claude and is still a guess
  about an arbitrary agent's CLI; the error names the setting and shows where
  the text belongs. It is checked before anything is created, so the refusal
  never leaves a half-made worktree behind an error about a flag.

**A prompt that reached no agent is warned about.** The command carrying it
runs only in a window that was actually created, and `resume` mostly finds
windows rather than creating them — so `openWindow` reports which of the two
happened, and a prompt that landed on an already-open window gets a warning
naming the recovery: paste it there. Silently dropping text the user typed
once and cannot retype from memory would be the quiet failure everything else
here refuses to be. `base` takes no `--prompt` at all, since reusing its one
window is its normal case — the flag would warn more often than it worked.

## Reporting what failed

Two of the things `new` sets in motion run where treewright cannot watch them, and
each needed its own answer.

**A window's command** — `command`, or `resume_command` — is run by tmux, which
closes the window the moment it exits. A command that cannot start erases its own
explanation at the speed it appeared: the window flashes, and the `command not
found` goes with it. treewright can say afterwards that the window "closed as soon
as it opened", and does, but that is a guess arriving in another terminal without
the one thing needed, which is the message. So the command is wrapped: one that
exits nonzero leaves its window up with its output still on screen, above a line
naming the command, its status, and the Enter that closes it.

A command that succeeds closes its window exactly as before — holding every window
open would turn finishing normally into a keypress. So would reporting a stop the
user asked for, which is why anything above 128, the range of a command killed by a
signal and nearly always a Ctrl-C, is let through untouched. The command runs in a
subshell so that an `exit` of its own — from a wrapper script, a shell function, an
`activate` — ends the command rather than the wrapper, which would close the window
with the output erased and is the whole case this exists for.

**post_create** cannot be reported as it happens at all: nothing waits for it, so
treewright has already exited by the time it fails. The failing step writes the
command that stopped it beside the log, and the next `ls`, `cd` or `resume` that
mentions that worktree warns, naming the command and the log. It keeps warning: a
half-installed worktree stays half installed, and a warning shown once, in
whichever command happened to run first, is one a user who stepped away never sees.
Finishing the install by hand and deleting the marker is what ends it. The marker
is cleared whenever a worktree is created under that slug, so a recreated worktree
does not inherit the failure of the one before it.

`doctor` covers what can be seen in advance rather than after: `command` and
`resume_command` are checked for a first word that is on PATH. post_create is not,
because its commands are shell lines where a first word is as likely to be a
builtin as a program, and a false warning about `cd` is worse than no check.

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
whose own config is fine — but if nothing matched, the skipped error is what gets
reported, since the unreadable config is nearly always this repo's own, edited a
moment ago, and "no config matches" alone would send you looking for a file that is
sitting right there with a typo in it.

`main_dir` is resolved through symlinks, not merely cleaned. git reports fully
resolved paths for every worktree, so a `main_dir` that reaches the repo through
a symlink would never match what git says and its worktrees would be invisible.

`setup` writes the file with every guess commented, on the principle that the
file remains the record: it is a way to start one, not a layer above it. `config`
prints what is in force with defaults marked as such, because the gap between a
config file and the behavior it produces — invisible defaults, unexpanded paths,
and which of several configs applies — is where the confusion lives.

### post_create

Either one command or a list of them, under one key rather than the two spellings
`branch_prefix` and `branch_prefixes` are. The plural of this setting has no name
that reads like anything, and a second key would let a file set both and leave a
reader to work out which won; one key taking either shape has neither problem, and
every config written before the list existed still means what it meant. An empty
string stays "nothing to run", that being how a config that once had a setup step
says it no longer does, but an empty entry *inside* a list is refused as the
half-finished edit it is.

The commands run in one `sh -c`, because treewright exits as soon as the window is
open and is not there to run the second one. Each is wrapped in a subshell, which
makes it a step in the sense every CI steps-list already means: it starts in the
worktree root whatever the last one did, and its failure is its own. `set -e` over
a flat script was the alternative and it fails in both directions — it reaches
inside a step to stop on a failure the user had already handled with `||`, and a
step that calls `exit`, or sources something that does, ends the whole run with
nothing reported. A step that wants to work elsewhere writes `cd sub && ...`.

Each step is echoed into the log as `$ command`, and a failing one names itself
before the run stops, because a log truncated halfway through a five-step install
is otherwise indistinguishable from one still being written. The log lives in
`.git/treewright/` rather than in the worktree, where it would show up as an
untracked file and make the tree read as dirty — and rather than nowhere, which is
what discarding the output would leave you with when an install fails. How the
failure reaches the user is below.

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

**Why the documented line is a bare `eval` rather than a guarded one.** The line
runs the binary to get its own text, so it fails when treewright is not on `PATH`
— and the shell reports that against the startup file's line number, which names
the wrong culprit and sends people looking at the integration instead of the
install. Wrapping it in `command -v treewright >/dev/null 2>&1 &&`, the shape
tool-init lines often take, is the wrong trade for a first-time setup: it turns a
loud wrong-culprit error into a silent no-op, where `tw` is undefined and nothing
at all has been said. The install section carries the `PATH` check instead, before
either integration is added. A dotfiles repo shared across machines, some without
treewright on them, is the case where the guard earns its keep — but that is a
choice about absent installs, not the instruction to hand someone installing it.

**`tw` and `TREEWRIGHT_ARGV0`.** `tw` calls the `treewright` *function*, resolved
at call time, so the eval-file protocol works identically under either name. That
function runs the binary as `command treewright`, which erases the typed name
from `argv[0]` — so `tw` exports `TREEWRIGHT_ARGV0=tw` for just that call, and
help and every runtime hint answer in the name the user actually typed. What
keeps the canonical name is help prose and anything destined for a file —
tmux.conf lines, shell startup evals — which programs read and shell functions
never reach.
