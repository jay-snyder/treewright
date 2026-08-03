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
directory and switches to it. It also gets what every other row gets when there
turns out to be nothing to carry on from: `command` behind the failure, and
`--fresh` to ask for it outright.

**Its status is `base`, outside the safe-to-remove scale.** A base checkout
sitting level with origin has no commits outside it, which would read as
`merged`: the green that means "safe to delete", about the one directory that
must never go. Its divergence column still means something, and something
slightly different — for the checkout parked on the base branch, it is how far
behind origin you are, and so whether what you are reading there is stale.

**In a repository with no worktrees yet**, the menu is that one row with "start
one with `prefix + N`" printed above it; `ls` prints nothing, and `ls --json`
still carries the base row. Three answers to one state, because the three are
for different readers.

A menu is a way through, and must offer the base checkout exactly when there is
nothing else to offer. A table is an answer read by a person, and "no worktrees"
is the answer — printed on stderr, where it cannot be mistaken for a row. The
JSON is a schema, and a row that appears only sometimes is not one: a consumer
deciding where a piece of work should go reads row 0, and making it check first
whether row 0 exists pushes the special case into every reader.

An empty array cost exactly that, and worse: it was read as "this repository is
not registered", which sent its reader through `--help` and the config files
looking for a registration that was already in place. That state was never
ambiguous — an unregistered repository exits 1 with `no registered config
matches repo <path> (have: …)` — so the fault was not an unanswerable question
but one schema with two shapes.

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

### An empty command, and the one key that overrules it

The command keys have the same shape and reached it later. `tmux.Spec` has
always documented a blank command as "leaves a shell", and `newWindow` omits a
blank rather than passing an empty string — so the plumbing for a window with no
agent in it was there the whole time, and no config could ask for it. `Load`
defaulted on the value, so `command = ""` came back as `claude {prompt}`, and
`treewright config` — the command whose entire subject is the gap between a file
and its behavior — reported that value with the FROM column blank, meaning *the
file's own*. About a file that said no such thing.

Both command keys now default on `!Explicit(...)`, so `command = ""` opens the
window on a shell and only a file that never mentions the key takes `claude`.
The half-deleted-line worry that argued for the old collapse does not survive
contact with the failure it produces: a window holding a shell is visible the
moment it opens, where a wrong `ticket_pattern` is invisible for weeks.

**`agent` is the exception, deliberately.** It fills a blank command however the
blank got there, so `agent = "claude"` with `command = ""` runs claude, and a
repository that wants the shell removes the agent key too. The alternative —
carry claude's settings and plugin into every worktree, then open a shell — is a
combination nobody asked for, and supporting it would make one key's empty value
mean two different things depending on whether another key is present.

That exception costs one piece of bookkeeping. Once the module has filled a
blank, an explicit `""` and an absent key are the same value under two different
answers from `Explicit`, so `setup --refresh` cannot tell whether to write the
line back. `Config.AgentFilled` records the fill for exactly that reader:
a value the module supplied follows treewright's changes to that module, and the
identical string written into the file stops following them — the
default-becomes-a-setting trap every other key here is guarded against.

The old generated config carried the other half of this. "Remove this key for a
window with no agent in it," it said about `agent`, which was false: removing it
stopped the carry and the module's defaults, and left `DefaultCommand`, which is
`claude {prompt}`. You got claude either way. It now says to set `command = ""`
as well, which is the thing that is actually true.

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

**And the generated file says which of the two it got.** A prefix read off
origin's branches is a convention observed; one derived from a git email is a
guess about a person made from an address that need not be about a person —
`codeberg@example.org` yields `codeberg/`, a namespace nobody would choose,
which is how the guess is usually met. Written with the same confidence as
`main_dir`, under a header claiming everything below was detected, it reads as
something treewright found out. So the header distinguishes the two kinds of
value and invites a read, and the email-derived prefix carries its provenance on
the line above it, where a reader checking one setting will actually see it. The
guess itself stays: attributable branches on a shared remote are why it exists,
and the fix for a wrong one is a one-line edit in a file that is meant to be
edited.

## Creating a branch

`new` reuses a branch that already exists rather than recreating it, which is
also how you get a worktree onto a colleague's pull request after fetching it.
Branches always fork from `origin/<base_branch>` — there is deliberately no flag
to base one on anything else, the point being that every worktree starts from the
same known-current place. When origin is unreachable, `new` says so and forks
from the local base branch.

**A base checkout ahead of origin is warned about**, because that same rule is
what makes it invisible: commits made in the main checkout and not yet pushed are
not in the new worktree, and neither are the files they added. Nothing is wrong —
the fork point is the one treewright promises, and pushing is the user's call —
but the discovery otherwise arrives as an empty file three steps into the work,
looking like anything except a fork point. So `new` counts the local base branch
against `origin/<base_branch>` on the path that forks from origin, says what it
means for the worktree just made, and names the two ways out: push and recreate,
or cherry-pick the commits over. It is the branch that is compared, not whatever
the checkout has out — that is `base`'s question, and it asks it separately.

## Moving work that was started in the wrong place

Typing in the main checkout and then realizing the change wants a branch of its
own is ordinary. What makes it dangerous is that the work is uncommitted: until
it is somewhere else, the base checkout is the only copy of it, and the sequence
that moves it has a verification gate in the middle whose entire purpose is to
be passed before anything is thrown away.

That sequence lived in the claude module's guide as six commands in a strict
order, and the order *was* the safety. Prose cannot hold that. An agent
improvising anywhere in it loses work, and the improvisation to expect is
`git clean -fd` for the last step — which reads as "delete the untracked files"
and means "delete every untracked file", including the ones this move never
touched and any ignored file sitting inside an untracked directory. So it is
`tw move <slug>`, and the order is in the binary.

**The base checkout is the last thing touched and never the first.** What runs,
in order: list the untracked files, mark them intent-to-add so they reach a
diff at all, write `git diff HEAD --binary` to `.git/treewright/move-<slug>.patch`,
put the index straight back, make the worktree exactly as `new` does, apply
with `--3way`, check the result, and only then clear the checkout the work came
from. Every failure before that check leaves the checkout as it was found, says
so in those words, and names the patch — which is a second copy of the work and
the way in by hand.

Four details carry more weight than they look:

- **The index goes back immediately**, not at the end. From the moment the patch
  is written the checkout is byte for byte what it was, so every later failure
  is honest without a cleanup path of its own to get wrong.
- **It goes back by path.** A bare `git reset` would also unstage whatever the
  user had staged themselves, which is theirs and none of a move's business.
  `git reset -- <the untracked files>` undoes exactly the intent-to-add entries
  treewright wrote.
- **The check is `git diff HEAD --stat`, not `git diff`.** `--3way` applies
  through the index, so the work arrives staged and a plain `diff` has nothing
  to show — which reads exactly like a patch that never applied. Verifying with
  the wrong command is worse than not verifying: it is a gate that opens on
  failure.
- **What gets deleted is read from the diff, not from the untracked listing.**
  `git diff HEAD --name-only --diff-filter=A` is every path the patch creates,
  which includes a file the user had already `git add`ed — one the untracked
  listing does not mention and which would otherwise survive as a second copy.

Files git ignores stay where they are. They are what `carry_files` copies into
every worktree, so a move that swept them up would take the `.env` out of the
checkout every future worktree is carried from. Empty directories are left
behind too, where a deleted file was the last thing in one: removing directories
is precisely the reach that makes `clean -fd` dangerous, and an empty directory
costs a reader nothing.

**`git stash` is not the shortcut it looks like**, and this is the note for
whoever proposes it next: one stash stack is shared by every worktree of a
repository. A `pop` in the wrong checkout is a keystroke away, and the work is
then in neither place anybody expected — a failure with no error message in it.
A patch file is worse to type and cannot be popped anywhere by accident.

**The window opens last**, after the work has landed, so the agent's first sight
of the worktree is the work already in it rather than an empty checkout it is
being asked to carry on with. `--keep` leaves the base checkout alone on success
too, for when the work is wanted in both places.

## Output contract

stdout carries the answer and nothing else, so any command can be piped:

| Command | stdout |
|---|---|
| `new` | the new worktree's path — `cd "$(tw new eng-1)"` works |
| `move` | the same, printed once the work has arrived — until then there is no honest answer to where it went |
| `cd` | the chosen worktree's path, so `cd "$(tw cd eng-1)"` works unaided |
| `rm` | the removed worktree's path |
| `prune` | the paths it removed, or would remove |
| `ls` | the table, or a JSON array with `--json` |
| `setup` | the config file's path, or the config itself with `--dry-run` |
| `config`, `doctor` | the report you asked for |
| `shell-init`, `tmux-init`, `help`, `version` | the script or text you asked for — `version --check` puts what it found on stderr, so the version line stays one line |
| `setup --refresh` | the config file's path, or the config itself with `--dry-run` |
| `refresh` | nothing — what it did is a report, and the answer is the state it left behind |
| `agent-init` | the plugin directory it installed into, or the plugin's files with `--print` — with what it wrote, and where else it could go, on stderr |
| `send` | nothing — there is no answer, only something done; what the window was showing and what was typed go to stderr |
| `close` | nothing — there is no answer, only a window that is gone; what it closed and what that cost go to stderr |
| `signal` | nothing — the answer is the stamp on the window, and out of scope it is silent on stderr too |
| `guard` | nothing — the answer is the exit code, that being what a PreToolUse hook reads, and the refusal it carries goes to stderr for the agent |

Progress, warnings, prompts, and errors go to stderr, prefixed `warning:` or
`error:` following git's convention, and unprefixed when it is just narration. So
`tw ls --json | jq` and `tw prune --yes > removed.txt` both stay clean.

### Messages are written for scanning, not for reading

The messages with the most to say used to say it as one line: a finding, an em
dash, what to do about it, a second em dash, why it mattered. At the length
those reached — `doctor`'s tmux finding was a hundred and eighty columns — the
terminal wrapped them wherever it happened to run out of room, mid-path and
mid-command, so the part meant to be copied was the part hardest to pick out.

Breaking the line was the first fix and it was not enough on its own. It left
the same prose sitting on three lines instead of one, and the prose was the
other half of the problem: these were written in the register the rest of this
repository is written in, and a trailing appositive that reads well in a
paragraph is a thing to parse in a terminal, where nobody is reading. They are
looking for one fact.

So a message now front-loads its subject, says one thing per line, and names its
parts. **What is wrong, what it costs, then what to type** — the copyable part
last, where a reader who has decided to act finds it without reading past it
twice. Nothing was shortened to fit: splitting costs a line of screen, cutting a
clause costs the reader what it said.

    warning: post_create failed in eng-9
             failed step  npm ci
             log          .git/treewright/post-create-eng-9.log

Three mechanisms hold that up, all in `internal/cli/message.go`.

**The indent** is what makes two lines one message. A continuation starts where
the first line's text started, and the writers apply it rather than the
messages — thirty call sites spelling their own indent is thirty chances to
spell it differently, which is the state it replaced: one hand-typed run of
seven spaces in `rm`, and no way for anything else to match it. Progress is the
one exception, and only because it has no prefix to align under; its text starts
at the margin, so it takes a two-column hanging indent instead. For the same
reason a single thought never spans two `progressf` calls: the second starts at
the margin and reads as a new subject.

**Labelled fields** (`asFields`) are what replaced the em dashes. A message with
a what, a where and a how ran them together and a reader after the log path had
to read the whole sentence to find it. A list (`asLines`) gets a line per entry,
one step further in — the files `agent-init` wrote, the worktrees a slug prefix
matched, the prefixes `setup` read off origin. Comma-joined, those were the
messages that grew without limit: a repository with a dozen worktrees is exactly
the one where the list is worth reading, and exactly the one where a joined list
is unreadable. `ui.Table` renders a cell holding newlines as a row spanning that
many lines, which lets a `doctor` finding and a `config` value do the same
inside a table rather than laying out their own text beside one.

**Color** marks the two spans worth finding without reading: the severity, and
the part meant to be typed. It is decoration and nothing more — off down a pipe,
off under `NO_COLOR`, off on a dumb terminal — so every message carries its
whole meaning in the text. What color buys is the eye landing in the right place
first, never a fact only the colored copy has.

Counts read as sentences — `1 uncommitted file`, `2 commits not on origin` —
rather than covering both cases at once with `file(s)`, which reads as neither.

### doctor is a report, so it is laid out as one

Its findings are grouped: the installation checks first, because a broken one of
those breaks every repository, then a section per config, then a count of what
was found. Three things came out of that. The repository name stopped being a
`proj: ` prefix repeated down the left margin and became the heading it always
was. The thing being checked got a column of its own, so a reader told the shell
integration is missing can find the word "shell" rather than reading for it
inside the seventh sentence. And the count at the end answers the question a
report is read for — ten green lines with two yellow ones in the middle is
exactly the shape an eye slides off.

    installation
      ok    tmux               /usr/sbin/tmux
      warn  shell integration  not loaded
                               cd and rm cannot move your shell
                               add to your startup file:  eval "$(treewright shell-init zsh)"

    proj
      ok    main_dir           ~/proj
      ok    origin             forks from origin/main

    1 warning, nothing failed

The status words stayed words. Symbols were the alternative, and they buy less
than they look: the column is already colored, and `ok`/`warn`/`fail` need no
font to render.

`config` gained a `FROM` column for the same reason — where a value came from is
a third fact about it, not a suffix of it — and it sits *before* `VALUE` rather
than after. A last column is the only one a table never pads, so markers after a
column of absolute paths end up fifty columns from what they mark.

Exit codes: `0` success, `1` the command ran and failed, `2` it was invoked
wrong. `doctor` exits `1` when a check fails, so it can gate a setup script.

Color is on only when writing to a terminal, and off under `NO_COLOR` or
`TERM=dumb`.

`--json` always carries the base checkout as its first row, in every repository
it can answer about, so a consumer reads row 0 rather than testing whether there
is one — see "The base checkout" for what the table does instead, and why the two
differ. It reports `ahead` and `behind` as `null` rather than `0` when the branch
cannot be compared to its base — an unknown is not a zero. An open window is
described with three fields, because they are consumed differently: `window` is
the name a human reads, `window_id` is what `tmux kill-window -t` takes, and
`window_session` is what `tmux attach -t` takes. All three are empty strings when
no window is open.

Two booleans go with them, and both exist because they were being worked out by
hand. `window_is_current` marks the window the command is running in — the one
whose closing ends the session doing the reporting, and the one an agent must
not take down before it has finished answering. Reading that off the listing
used to mean a `tmux display-message -p '#{window_id}'` of your own and a
comparison, which is a fact treewright already has. `window_last_in_session`
says that closing the window ends its session with it, which had no answer short
of counting. Both are false when no window is open, and `window_is_current` is
false outside tmux, where there is no such window — spelled explicitly, because
the empty current window would otherwise match every row's empty `window_id`.

**Neither costs a tmux call per row.** `#{session_windows}` rides in the same
`list-panes` pass that fills the rest, so a table of a dozen worktrees still
costs one round trip; the current window is one question about the caller, asked
once. The per-window `display-message` that used to answer the second of them,
for `rm`'s one window, is gone — `Window.LastInSession` reads the count the
listing already carried.

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

**The line that names the command does not repeat it.** It used to, and that made
the wrapper grow with what it wrapped instead of by a fixed amount — worse than
twice as fast, since `fillPrompt` has already shell-quoted the prompt into the
command, and quoting that copy again turns one apostrophe of ordinary English
possessive into sixteen bytes. What it bought was tmux's own ceiling. A tmux
client carries a command to the server in a single imsg, which holds 16384 bytes
less its header and the argument count, so a command list past 16364 is refused
outright with `command too long` — and since `new` deliberately does not fail on
a window it could not open, an over-long `--prompt` left a branch, a worktree, no
window and no agent, under tmux's raw refusal with the doubled script quoted into
it. So the report names the command's first line, cut to eighty columns, and the
copy that actually runs stays byte-exact.

**A resume window is handed two commands**, `resume_command` with `command`
behind it, so that a resume which finds nothing to continue starts an agent
rather than parking on the error — see "When there is nothing to resume" in
`agents.md`. Everything above holds unchanged: one script, one shell, the last
command's status deciding whether the window is held open, and the line that
names what exited naming whichever of the two it was.

**And the length is checked before anything is created**, in `fillPrompt`, beside
the refusal of a prompt the template cannot take: both are this invocation being
wrong, and both are cheap to say while there is still nothing to clean up. What
is measured is the script tmux is handed rather than the setting it came from,
which for `resume` is both commands with the prompt in each of them: a check
against either alone would pass an invocation tmux then refuses. The
error names the size and the limit, since the only useful thing to know about a
prompt that will not fit is how far over it is. The limit treewright holds to is
tmux's less a kilobyte for the rest of `new-window`'s argument list — the
session, the worktree's path, the window name — because measuring those exactly
would make the limit a property of a call site that does not exist yet at the
point the check runs, and what fills the budget is prose, where nobody is writing
to the last hundred bytes.

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

## Upgrading treewright itself

Three of treewright's integrations follow an upgrade on their own, and the way
they do it is the same trick each time: what sits in the user's file is a
*reference* rather than a copy. `eval "$(treewright shell-init zsh)"` re-runs at
every shell start, `run-shell 'treewright tmux-init --apply'` re-reads at every
tmux start, and the agent plugin is a directory treewright owns and rewrites.
None of those can go stale in the sense a pasted snapshot could.

What they can all be is *not yet reloaded*, and that turns out to be the whole
problem. A tmux server runs for weeks. A terminal stays open for days. And a
worktree's copy of the plugin is made once, by the carry, and then nothing ever
looks at it again — which is a genuine snapshot, of exactly the kind
[`agents.md`](agents.md) says the plugin exists to abolish, reintroduced one
directory further down.

None of that breaks anything loudly. The old bindings still open popups, the old
wrapper still moves your shell, the old hooks still fire — right up until a
signal verb is renamed, and then every pre-upgrade worktree errors on every
agent transition while `doctor` reports `ok`. So each integration gained a way
to say which treewright it came from, and one command puts them right.

### Asking a running integration which treewright it came from

Each of the three answers differently, and the differences are forced:

- **The agent plugin** is a set of files treewright wrote, so the question is a
  byte comparison against what `agent-init` would write today. That was already
  how the main checkout was checked; what is new is that `doctor` now walks the
  repository's worktrees and asks the same of each, and reports the answer as at
  most two findings — one for the copies that are out of date, one for the
  worktrees that never got one — each carrying its worktrees as a list. A
  finding per worktree per file would turn one upgrade into thirty lines of a
  report nobody then reads to the end of.

- **The tmux bindings** cannot be compared that way, and the reason is worth
  writing down because it looks like they could. tmux echoes a binding back in
  its own normalized spelling — single quotes rewritten as double, backslashes
  doubled, its own column padding — so byte-comparing `list-keys` against the
  emitted snippet reports every server as stale, including one loaded a second
  ago. What a server *does* hold verbatim is a value treewright puts there, so
  the snippet ends with `set -g @treewright_tmux_init "<digest>"`, a fingerprint
  of the snippet's own source. `tmux.HasBindings` — a substring test for
  "treewright" in `list-keys` — stays what it was, and the stamp is what
  separates "some version's bindings are loaded" from "this version's are".

  The keys are deliberately *not* in the digest. Which key opens the picker is
  the user's decision, made at the `tmux.conf` line; a fingerprint that moved
  with it would report a server loaded by this very binary as out of date for as
  long as the custom key survived, which is forever.

- **The shell wrapper** cannot be asked at all: it lives in the user's shell,
  and a child process cannot read its parent's function table. `doctor` used to
  infer "loaded" from `TREEWRIGHT_EVAL_FILE` being set, which a terminal opened
  two releases ago reports exactly as one opened a minute ago does. So the shim
  exports `TREEWRIGHT_SHELL_INIT_VERSION`, its own fingerprint, and `doctor`
  compares — accepting *any* of the three shims' fingerprints, since the
  question is "is this one of mine" and working out which shell is running would
  mean trusting `$SHELL`, which names the login shell rather than the running
  one.

Both fingerprints are digests of the checked-in text rather than the release
number, for the same reason: a shim or a snippet built from an unstamped tree
still has to be distinguishable from an older one, and a `dev` build compared
against a `dev` build would be no comparison at all.

### `refresh`, and what it deliberately will not do

`refresh` is the one command to run after an upgrade. It rewrites the plugin
wherever it is installed — the user-level copy, the main checkout's, and every
worktree's — reloads the tmux bindings, and reports what moved in each place,
naming the files the way `agent-init` does, because
the interesting run is the second one and "wrote `hooks/hooks.json` in eng-1"
says which part of the wiring had gone stale where "updated 6 checkouts" says
only that something did.

**It refreshes what is installed and installs nothing new.** A checkout with no
plugin is left alone unless the config carries one — a worktree with nothing in
it and a carry configured is older than the carry and is owed a copy; without
one, writing there would be treewright choosing a placement the config never
asked for. The tmux bindings go back only into a server that already holds some,
on the keys they are already on, since which keys a server binds is a decision
made in a file the user owns. This is the command people will run without
reading it, and `agent-init` and `tmux-init` are where that decision belongs.

**The shell is said rather than done.** No process can define a function in its
parent, so `refresh` reports a stale wrapper and names the fix — a new terminal
— which is the whole of what is available to it.

### Checking for a newer release

Explicit only: `doctor` asks, `version --check` asks, and nothing else ever
does. An upgrade check on `new` or `ls` is a network call in the middle of a
command that had no reason to make one — slow on a bad connection, a privacy
question on any connection, and a warning arriving while the user was doing
something else. There is no cache file for the same reason there is no
background check: both exist to make an *automatic* check cheap, and this one is
not automatic.

Two properties matter more than the answer. It must not hang, since `doctor` is
what a person runs when something is already wrong — hence a short timeout on
the whole request. And an unanswerable check must be silent in `doctor`: an
offline laptop must not come back with a warning about the network it is not on.
`version --check` is the one place that outcome is spoken, because somebody who
typed it and got nothing would reasonably conclude they are up to date.

A build with no release version says so rather than guessing. `dev` is not older
than anything, and reporting it as behind would send somebody upgrading a binary
they compiled an hour ago. That check happens *first*, before any request, which
is also what keeps the test suite off the network.

The upgrade command is named only for a route that can be told from the path the
binary is running from — a Homebrew prefix, or the `go install` directory. Naming
the wrong one is worse than naming none: `brew upgrade` told to somebody who used
a tarball fails in a way that reads as treewright being broken, so the rest get a
sentence instead.

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

**What they name is `treewright close <slug>`, not a `tmux kill-window`.** The
raw line was the last place driving treewright meant typing tmux, and it failed
in the way that is hardest to notice. It is run from a shell holding none of
treewright's environment, so under `TREEWRIGHT_TMUX_LABEL` a bare
`tmux kill-window -t @3` looks in the *default* server — where `@3` is some
other window entirely. tmux closes it and exits 0. The window that was meant to
close stays open, one nobody asked about is gone, and nothing anywhere says so:
a wrong session name fails loudly, a wrong server does not.

`close` closes the window on a worktree and nothing else — the worktree, the
branch and the work in them are `rm`'s business. **It works after the worktree
has been deleted**, which is mostly what it is for: the window is found by the
`@treewright_worktree` path recorded on it, and that record outlives the
directory, so the same lookup answers before and after. The path is computed
from the slug rather than looked up among the worktrees for the same reason. A
prefix resolves while the worktree is still there and there is nothing to match
against once it is gone, so a removed one is named in full.

**Everything it says, it says before the window goes.** Closing a session's last
window can detach the client that would have read the report, and closing the
caller's own window kills the pane treewright is running in — there is no
afterwards to report from. Both are said rather than refused: the second is a
real thing to want, and the agent guide asks for exactly it as the last step of
a teardown.

**An agent still working is warned about, wherever a window is closed.** The
agent is the window's command, so there is no detaching from this and coming
back — the work stops and the session goes with it. That is the one thing about
a window treewright knows and the caller may not, since the state comes from the
agent's own hooks rather than from anything visible in the window's name, and it
is exactly the "something may still be running in it" that stops `rm` and
`prune` closing a window unasked in the first place. So `rm --yes` says it too,
and `rm`'s own prompt puts it above the question, where it is the caveat most
likely to change the answer.

It warns rather than refuses. The caller asked for this, and treewright is in no
position to judge whether what the agent is doing still matters; a refusal would
need a `--force` to get past, which is a flag people learn to pass by reflex and
then pass everywhere. What a warning buys is the loss being on the record at the
moment it happens rather than discovered later.

Only `working` warns. `waiting` is an agent blocked on a person and `done` is
one with nothing in flight — those are the states an ordinary teardown closes,
and a warning that fires on the ordinary case is one that stops being read.

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
| `<main_dir>/.git/treewright/move-*.patch` | The uncommitted work `move` is carrying, written before anything is created and deleted once it has landed. What is left behind is left after a failure, deliberately: it is a second copy of work that exists in one place, and the way back in by hand. |
| `~/.claude/skills/treewright/` | The agent plugin, written by `agent-init` — one copy covering every checkout the agent is started in. The only thing on this list outside a repository besides the registry, and running the command that installs the wiring *is* the consent: nothing else writes there, `rm -r` and it is gone, and `claude plugin disable treewright@skills-dir` stops it loading without deleting anything. |
| `<main_dir>/.claude/skills/treewright/` | The same plugin, when `agent-init --local` is asked for it — inside the repository, in a directory treewright named and nothing else writes to. |
| The worktrees themselves | What the tool is for, and `rm` takes each one back. |

One path has come off that list rather than been added to it.
`.git/treewright/no-agent-yet-*` was a note that a worktree had never had an
agent in it, and `resume` read it to decide whether to run `command` instead of
`resume_command`; the recovery is triggered by the failure now, and nothing
reads the files. treewright neither writes them nor deletes the ones it wrote —
deleting a user's files to tidy up after itself is not a licence this list
grants — so they sit inert beside post_create's logs, and
`rm .git/treewright/no-agent-yet-*` takes them off a repository that still has
some. See "When there is nothing to resume" in `agents.md` for why the mechanism
went.

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

That the default placement is under `$HOME` rather than inside the repository
does not change the rule, only who is asked. A flag used to be the consent, and
now the command is: `agent-init` exists to install the wiring and does nothing
else, it prints the directory it wrote on stdout, and `--print` writes nothing
and dumps the files for anyone who wants to read hooks before they run. What the
flag bought in exchange was a copy per repository, each one a directory `git
status` reports as untracked and each one going stale on its own — a worse
bargain for the same list. See "Where the wiring goes" in
[`agents.md`](agents.md).

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
the base branch is still `main`. The error offers the other reading too — a
config is a file two treewrights may see, on a laptop and a desktop or either
side of a downgrade, and the newer one's settings arrive in the older one looking
exactly like typos.

### The version key, and refreshing a config in place

Generated configs carry `version = <n>`, and it counts revisions of the
*generator* rather than treewright releases: it moves when what `setup` writes
moves, and stays put across releases that change nothing here. A file without
one is an old config and not an error — every config in the wild predates the
key, and refusing them would make an upgrade break every repository already
registered. `doctor` warns; nothing fails.

There is deliberately no rename or migration table. No key has ever been renamed,
and that machinery would be built for a hypothetical. What the version supports
is one warning naming one command.

That command is `setup --refresh`, and it exists because `setup` refuses an
existing config outright — rightly, since overwriting one discards edits — with
the consequence that every later improvement to the generated file reached new
repositories only. A repository registered two releases ago names none of the
keys added since and explains none of what the commentary now explains, and the
only way out was to delete the file and answer the detection again.

**Nothing is re-detected**, which is the whole difference between this and
running `setup` twice. The values are read back out of the file that is there: a
base branch someone corrected by hand, a prefix chosen over the guess, a command
that is not the agent's default. Those are decisions, and a refresh that
re-derived them would quietly undo the ones that disagree with what treewright
would guess today. What moves is the version, the commentary, and any key the
generator has since learned to write.

Three details in the rewrite are load-bearing. The prefixes come back under
whichever of the two spellings the file used, since no config may set both —
and their commentary claims no provenance, because none is on record: whether a
single prefix began as origin evidence or as an email guess was never written
down, and a rewrite that re-emitted the guess's paragraph would assert facts
about a git email this run never consulted. An explicit `branch_prefix = ""`
survives as the live line it is, for the same reason `ticket_pattern` does:
that key is written on whether it was *there* rather than on whether it holds
anything — `ticket_pattern = ""` is how a repository that tracks no tickets
turns the search off, and a refresh that dropped it for looking empty would
turn every window name in that repository back into a ticket hunt. And every
key with a default is read back through `Explicit`, `base_branch` included: a
file that never set one gets the commented default back, never a live line,
because a default written into the file as a setting is one that stops
following treewright's own changes.

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

`setup` writes the file with every value commented, and marks which of them were
detected and which were guessed, on the principle that the file remains the
record: it is a way to start one, not a layer above it. `config`
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
the `cd` to run. An eval file that exists and cannot be written — a swept
tmpdir, a full disk — is the same failure with a cause worth naming, so it is
reported as a warning with the same by-hand line under it; both halves live in
one helper, `moveShell`, so a new caller cannot keep the emit and forget the
fallback.

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

**The shim says which treewright emitted it.** Each one exports
`TREEWRIGHT_SHELL_INIT_VERSION`, a fingerprint of its own checked-in text, and
that is the only way the question can be asked at all: a shell keeps whatever it
loaded at start, and a binary cannot read its parent's function table. Exported
rather than merely set, because the only thing that reads it is a child process —
`doctor`, which compares it against the shims this binary emits and says when the
wrapper in the shell is somebody else's. See "Upgrading treewright itself".

**`tw` and `TREEWRIGHT_ARGV0`.** `tw` calls the `treewright` *function*, resolved
at call time, so the eval-file protocol works identically under either name. That
function runs the binary as `command treewright`, which erases the typed name
from `argv[0]` — so `tw` exports `TREEWRIGHT_ARGV0=tw` for just that call, and
help and every runtime hint answer in the name the user actually typed. What
keeps the canonical name is help prose and anything destined for a file —
tmux.conf lines, shell startup evals — which programs read and shell functions
never reach.

