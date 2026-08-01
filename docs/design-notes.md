# Design notes

Why treewright behaves the way it does. `README.md` is the user-facing tour and
`CLAUDE.md` is the map of the code; this is the reasoning behind the behavior —
the alternatives that were tried, and what made them lose.

Most of it also lives as comments next to the code it explains. Where the two
disagree, the code comment is the one being maintained.

Two subsystems are large enough, and separate enough, to be read on their own:

- **[`tmux.md`](tmux.md)** — sessions, window identity, terminal titles, and the
  key bindings that reach treewright from a window running an agent.
- **[`agents.md`](agents.md)** — the agent-state protocol, the agent modules and
  the plugin they install, and the kickoff prompt.

What stays here is everything else: what a worktree and its branch are called,
what the commands promise their output looks like, what refuses to run, and how
a repository is configured.

---

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

Prefix matching is not really about ticket keys, though, which is why it is worth
having where there are none: `tw cd dark-mode` finds `dark-mode-toggle` for the
same reason. What people type is the start of the name, whatever the name is made
of.

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

## Naming a window, with or without a ticket

A window name goes in the tmux status line beside every other window's, so it has
to be short. A ticket key already is; a description is not, and `ticket_pattern`
exists to prefer the key when the slug carries one.

Everything else here works the same either way — the worktree is named after the
slug, the branch after the prefix and the slug, and `resume`, `cd` and `rm` all
take the slug — so the window name is the *only* place a repository that tracks
no tickets was treated as a degraded version of one that does. It was treated
that way twice.

**The pattern matched things that were not keys.** `(?i)^([a-z]+-[0-9]+)` let the
digits end anywhere, so `fix-2fa-login` — work on two-factor login — opened a
window called `FIX-2`. The fix is to require the key to be a whole word, `(?:-|$)`
at the end, which costs a real key nothing: a key is followed by the description
or by the end of the slug in either case. What survives is genuine ambiguity —
`refactor-2-pass-parser` is a ticket key to any pattern that does not know the
scheme — and no regexp settles that, because the answer is a fact about the
repository rather than about the string.

So the answer is a setting, and the setting is the pattern itself:
`ticket_pattern = ""` means there are no tickets here, stop looking. It needs no
second key, and it reads as what it is. The catch is that `Load` fills every
other empty string in the file with a default, which would make the opt-out
unwritable; it therefore tests `Explicit("ticket_pattern")` rather than the
value, so an omitted key defaults and a key set to `""` stays empty. A
never-matching regexp was the alternative — undiscoverable, and RE2 has no
negative lookahead to write one with.

**The fallback spent characters saying characters were missing.** `rewrite-css` is
eleven characters and arrived as `REWRITE-CS...` — thirteen, to save one. So
`shorten` now keeps a shortened name only when it is genuinely narrower than what
it replaced, which is what hands that slug back whole.

The cap stays at ten. It is a ticket key's width, and holding a description to the
same one is the point rather than an oversight: the status line is as wide for a
repository that names work by description as for one that names it by key, and a
name that fits is worth more there than a name that is whole. Cutting mid-word is
the cost, and it is the cheaper one.

Cutting at a word boundary instead was tried, and it loses at this width. It has to
give back a whole word to find the boundary, so `flaky-payment-test` arrives as
`FLAKY…` rather than `FLAKY-PAYM…`, and — because cutting further escapes the
guard above — `rewrite-css` arrives as `REWRITE…` where the blunt cut hands it back
whole. It pays only at a cap wide enough that the nearest boundary is usually near
it, which is an argument for a wider cap and not for the rule. The one thing kept
from it is a trailing hyphen trim, since a cut landing after one leaves it against
the mark: `dark-mode-toggle` as `DARK-MODE…` rather than `DARK-MODE-…`.

The mark is `…` rather than `...`: one column instead of three, in the one place
where columns are the whole problem, and three of a ten-column budget is a lot to
spend. That makes the calculation a matter of runes rather than bytes — `refname`
forbids control characters and git's own metacharacters, not the rest of Unicode,
so a slug may legitimately hold multi-byte characters, and a byte-wise cut would
misjudge the width and split one down the middle. `ui.Table` already measures in
runes, so the table stays aligned. `popupSize` still measures in bytes and so
allows two columns more than the mark needs, which is the direction it errs on
purpose.

None of it is load-bearing for anyone who disagrees: `tw new <slug> <window-name>`
has always named a window outright, and remains the answer for the one worktree
whose name does not fit whatever rule is in force.

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
matching (`feature-eng-142-white-screen` has no ticket key at its head, so every
window would be called `FEATURE-EN…`), every row of the table grows a word
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
| `agent-init` | the plugin directory it installed into, or the plugin's files with `--print` — with what it wrote, and where else it could go, on stderr |
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

## What treewright is allowed to write

Setup touches as few files outside a repository as it can, and never a file it
does not own. Two reasons, and the second is the one that binds.

It is somebody's machine. A tool that appends to shell startup files, tmux
configuration and agent settings on the way in has made itself a fact about the
whole system to solve a problem about one repository.

And **there is no uninstall.** treewright ships a binary and nothing that undoes
what it did, so every file it writes outside a repository is a file a developer
has to find and reverse by hand on the day they stop using it — from memory,
possibly months later, in a config they have edited fifty times since. The
smaller that list, the more honestly the tool can be tried at all. A trial you
cannot cleanly abandon is not a trial.

What it writes, in full:

| Path | Why it is unavoidable |
|---|---|
| `<config dir>/<name>.toml` | The registry *is* the configuration; there is no treewright without it. One directory, `rm -r` and it is gone. |
| `<main_dir>/.git/treewright/post-create-*` | A background step's log and failure marker, inside the repository's own `.git`, which goes when the repository does. |
| `<main_dir>/.claude/skills/treewright/` | The agent plugin, written by `agent-init` — inside the repository, in a directory treewright named and nothing else writes to. `rm -r` and it is gone, and `claude plugin disable treewright@skills-dir` stops it loading without deleting anything. |
| `~/.claude/skills/treewright/` | The same plugin, when `agent-init --global` is asked for it. The only thing on this list outside a repository besides the registry, and the flag *is* the consent: it is not written unless it is named. |
| The worktrees themselves | What the tool is for, and `rm` takes each one back. |

Everything else is *printed for a person to place*, which is why `shell-init`
and `tmux-init` exist at all. The line in your `.zshrc`, the line in your
`.tmux.conf` — treewright wrote neither, so a developer removing them is
undoing edits they made themselves rather than hunting for edits a program made
while they were not looking.

**`agent-init` is the one that writes, and the rule it is held to is the same
one.** The test was never "does it write" but "whose file is it". The hooks
used to be printed because they belonged in a settings file the user owns,
where applying them would have meant a merge reordering somebody else's JSON;
moved into a plugin directory of treewright's own, there is no file of the
user's to touch. One directory, named after the tool, holding nothing a person
put there — which is a thing you can find and delete on the day you stop using
treewright, and the whole point of keeping this list short.

`tmux-init --apply` looks like the exception and is not: it applies key bindings
to a running tmux server, in memory, and writes nothing. The one line in
`~/.tmux.conf` that invokes it is still yours to add.

This is also why no `.gitignore` is generated. The per-project agent artifacts
land in files a repository may or may not want tracked, and that judgment
belongs to whoever owns the repository — a tool that edited `.gitignore` on the
way past would be writing into the project's history to tidy up after itself.

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

They are *stored* as files even so: `internal/shellinit/scripts/init.zsh` and
its two siblings, embedded into the binary by name. Emitting from the binary was
never an argument for keeping 173 lines of shell quoted inside Go, where nothing
highlights it, no shell parses it and an editor indents it as a string — the two
questions are separate, and only the first one was ever about the user. Naming
each file in a `//go:embed` rather than walking the directory is the same rule
the agent plugin's files are held to, and it bites harder here: this text is
`eval`'d into the user's interactive shell at every start, so a file that
shipped merely by being in the folder would run on every terminal they open.
`TestEveryScriptIsDeclared` fails on one that no shell claims.

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

