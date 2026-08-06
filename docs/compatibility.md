# Compatibility: what treewright promises not to break

What a version number means here, which surfaces are contracts, and how a break
is made when one is needed. Part of the design notes —
[`design-notes.md`](design-notes.md) defines most of the surfaces this file
promises things about, and where the two could disagree, that file's definition
wins.

treewright's compatible surface is unusually wide for a CLI, because its
consumers are not only people. A shim is `eval`'d into someone's interactive
shell at every start; a tmux server holds key bindings it loaded weeks ago; an
agent's hooks shell out to `signal` and `guard` on every transition of every
session it has; a script somewhere is parsing `ls --json`. Each of those reads
a contract treewright defines, and none of them re-reads the documentation when
treewright upgrades underneath it. That is the fact this document is built on:
the surfaces most worth freezing are the ones no person looks at.

---

## Today, at 0.x, nothing is promised

Semantic versioning says a 0.x may break anything in any release, and this
project has used that licence, deliberately and twice:

- **v0.3.0 removed `agent-init --global` outright**, along with the per-repo
  default it used to select. No deprecation period, no alias — the tag message
  says "a script that passes it now gets a usage error, and the fix is to drop
  the flag."
- **v0.2.1 began refusing a `--prompt` aimed at a command template with no
  `{prompt}` in it**, where it had previously been silently dropped. The tag
  message names it plainly: "one previously working invocation changes."

Both were right, and both were cheap only because nothing had been promised.
The alternative — carrying a dead flag and a silent drop into 1.0 so that
nobody's script ever notices — is how a CLI accretes the behavior it then
spends years apologizing for.

What 0.x already owes, and has paid both times, is *notice*: the tag message
names the invocation that changes and the one-line fix, before anyone hits it.
That habit is the part of this document that is already in force.

## What 1.0 freezes

Two tiers, and the difference between them is how a break surfaces. A broken
flag is a usage error on the next run, loud and self-describing. A broken
machine-read contract fails inside a file somebody wrote months ago — a hook, a
status line, a `jq` expression — with nothing pointing back at the upgrade that
did it.

### The machine-read contracts

The strong tier. Within a major version these only grow — a new JSON field, a
new config key, a new window option — and nothing existing is removed, renamed,
or changed in meaning.

| Surface | The promise |
|---|---|
| The output contract | stdout carries the answer and nothing else, and each command's answer is the one the table under "Output contract" in [`design-notes.md`](design-notes.md#output-contract) gives it. `cd "$(tw new x)"` and `tw ls --json \| jq` keep working. `TestStdoutCarriesOnlyTheAnswer` is the enforcement. |
| `ls --json` | The field names and types: `slug`, `base`, `dir`, `branch`, `status`, `ahead`, `behind`, `dirty_files`, `unpushed`, `window`, `window_id`, `window_session`, `window_is_current`, `window_last_in_session`, `agent_state`. `ahead` and `behind` stay nullable — `null` means the comparison was impossible, which is not `0`. The base row stays row 0 in every repository the command can answer about. Fields are added, never removed or retyped. |
| Exit codes | `0` success, `1` ran and failed, `2` invoked wrong — and `2` also for `guard`'s refusal, frozen the hardest of all because the number is owned by somebody else's specification: a PreToolUse hook blocks on 2 and on nothing else. |
| The config file format | The keys and their meanings — `version`, `main_dir`, `base_branch`, `branch_prefix`, `branch_prefixes`, `agent`, `carry_files`, `command`, `resume_command`, `post_create`, `ticket_pattern`, `tmux_session` — including the load-bearing empty strings: `ticket_pattern = ""` turns the search off, `command = ""` opens a shell. Unknown keys stay rejected. The registry stays at `${TREEWRIGHT_CONFIG_DIR:-${XDG_CONFIG_HOME:-~/.config}/treewright/repos}`. A config written today loads in every later 1.x. |
| The pasted lines | `eval "$(treewright shell-init zsh)"` and its two siblings, and `run-shell 'treewright tmux-init --apply'`, keep meaning what they mean. These are the two edits treewright asks a person to make in files it will never touch again — breaking either breaks every install at once, in a file its owner has long since stopped thinking about. |
| The eval-file protocol | Commands append lines to the file named by `TREEWRIGHT_EVAL_FILE`, restricted to what zsh, bash, and fish all parse identically. The shim sourcing that file may be weeks older than the binary writing it, so the writer holds to the oldest reader. |
| The `signal` vocabulary | The verbs a hook passes — `working`, `waiting`, `done`, `clear` — and the states they leave readable, which are the first three and empty. Closed, and each keeping its meaning: the verbs are wired into hook configurations sitting in plugin copies treewright may never rewrite, so renaming one errors every pre-upgrade worktree on every agent transition, invisibly. |
| `guard`'s behavior at the edges | Exit 2 refuses; everything out of scope — outside tmux, an unparseable payload, an unknown tool, wrong arguments — exits 0 in silence. Failing open is the promise rather than a detail of how it currently happens: a `guard` that started failing closed would refuse every tool call in a mis-wired session, and hand back its own help as the reason. |
| The tmux window options | `@treewright_repo`, `@treewright_worktree`, `@treewright_slug`, `@treewright_branch`, `@treewright_agent_state` — names and values, the public, greppable interface [`tmux.md`](tmux.md#window-identity) declares for status lines. The two reserved names, `@treewright_agent_message` and `@treewright_agent_since`, stay reserved. |
| Statuses | `dirty`, `merged`, `unpushed`, `active`, and `base` on the base row, with the precedence "Statuses" in [`design-notes.md`](design-notes.md#statuses) gives them. These reach machines through `ls --json`'s `status` field. |
| Environment variables | `TREEWRIGHT_CONFIG_DIR`, `TREEWRIGHT_EVAL_FILE`, `TREEWRIGHT_SHELL_INIT_VERSION`, `TREEWRIGHT_ARGV0`, `TREEWRIGHT_TMUX_LABEL`, `TREEWRIGHT_POPUP` keep their meanings, and `NO_COLOR` and `TERM=dumb` stay honored. A variable an agent owns — `CLAUDE_CONFIG_DIR` — follows that agent, named by its module and never by core. |

Why this tier is strong: its consumers hold on. A shell keeps the shim it
loaded at start until the terminal closes; a tmux server keeps its bindings
until it is killed; a worktree's plugin copy is a genuine snapshot, made once
by the carry. The fingerprint machinery — `TREEWRIGHT_SHELL_INIT_VERSION`, the
`@treewright_tmux_init` stamp, the byte-compared plugin, all of it under
"Upgrading treewright itself" in [`design-notes.md`](design-notes.md) — exists
so `doctor` can *say* a copy is stale; it cannot keep a stale copy working.
Detection is not a licence to break, and the contract has to be that old wiring
keeps working until its owner gets around to `refresh`.

### Command and flag names

The second tier. At 1.0, every command and flag name keeps working through the
major version: new ones may be added freely, and an existing one is retired
only through the deprecation path below, never by vanishing between minor
releases the way `--global` did.

The tier is weaker because the failure is louder. A script hitting a removed
flag gets a usage error naming the invocation, at exit 2, the next time it
runs — an unpleasant morning, not a silent corruption. That difference in how
the failure arrives is the whole reason the tiers are worth separating: the
strong tier gets the stronger promise because its breaks are the ones nobody
sees happen.

Help text, aliases, and completion are not part of this tier. Aliases are
deliberately absent from help and completion already, and what `tw help`
prints is prose.

## What is never promised

This section matters as much as the freeze, because a promise that quietly
covered any of these would stop work the project depends on doing freely.

- **The exact prose of any message.** Everything on stderr — narration,
  warnings, errors, prompts, `doctor`'s findings — is written for a person and
  rewritten whenever scanning improves, which "Messages are written for
  scanning, not for reading" in [`design-notes.md`](design-notes.md) treats as
  ongoing work. Scraping stderr was never supported; the split
  `TestStdoutCarriesOnlyTheAnswer` enforces is what makes that safe to say,
  because everything machine-readable was already on stdout.
- **Table layout.** Column widths, column order, and which columns appear at
  all — the AGENT column only exists when some window carries a state, and the
  marker column comes and goes the same way. The table is an answer for eyes;
  the JSON is the schema.
- **Color.** Decoration by rule — off down a pipe, off under `NO_COLOR` — so
  no meaning may ever live in it, and none of it is promised.
- **Window display names.** The fifteen-column cap, the case rule, and the
  `…` mark are display decisions, and v0.3.0 moved two of them. A window's
  identity is `@treewright_worktree`, never its name, so a script that finds a
  window by name is reading a label: cut to fit, and decorated with `!` while
  its agent waits.
- **The contents of `.git/treewright/*`.** Log formats, marker files, patch
  file names — internal bookkeeping, named in messages when a person needs to
  find one, never a format to parse.
- **The agent plugin's internal layout.** The files under a
  `skills/treewright/` directory are treewright's own wiring, byte-compared
  against what this binary would write and rewritten wholesale by `refresh`.
  They are a build artifact that happens to be readable, not an API.
- **The fingerprints' values.** `TREEWRIGHT_SHELL_INIT_VERSION` and
  `@treewright_tmux_init` exist and carry a digest — that much is the strong
  tier — but the digest changes whenever the text it fingerprints does, by
  design. A value that survived an upgrade would be the bug.

## How a break is made

At 0.x, the procedure is the one already in use: make the change, and write
the tag message so it names the working invocation that changes and the
one-line fix. The v0.3.0 and v0.2.1 messages are the house style — "a script
that passes it now gets a usage error, and the fix is to drop the flag" says
everything a stranded user needs, in the order they need it.

After 1.0, a name in the second tier is retired in two steps. First a
deprecation: the old spelling keeps working and says so on stderr each use —
which is one more reason stderr stays unpromised, since a frozen stderr could
never grow a warning. The deprecation lasts the remainder of the major
version; the removal lands in the next one, with the same tag-message shape.
That is `--global`'s retirement with a waiting period added, which is the part
1.0 buys.

The strong tier does not get a deprecation path, because its consumers cannot
read warnings — a hook config does not have a stderr to be warned on. A change
there is additive within a major version or it waits for the next one, and
when it lands, the stale copies are the migration problem: `doctor` names what
is out of date, `refresh` rewrites everything treewright installed, and the
tag message says both. What a user is owed, in every tier and at every
version, is that the tag message names what changed out from under them before
they find out by hitting it.

## The version scheme

Releases follow semantic versioning, and 1.0.0 is the release at which the two
tiers above harden from practice into promise — nothing else about the number
is special. Two other version-shaped values deliberately do not follow it:

- **`config.FormatVersion` counts revisions of the generator, not releases.**
  It moves when what `setup` writes moves, and stays put across releases that
  change nothing there — it is `1` today and has never moved since it arrived.
  A 1.0.0 of the binary does not drag the config format anywhere or imply
  anything about it; the config file is the one artifact that lives on a
  user's disk and outlasts any binary, and its compatibility story is the
  format row in the table above, not the release number. See "The version
  key" in [`design-notes.md`](design-notes.md).
- **The integration fingerprints are digests, not versions.** They order
  nothing and compare across releases to nothing; their one job is telling
  "this binary's text" from "any other text", including between two `dev`
  builds no release number could tell apart.

## What has to settle before 1.0

Two things, and neither is a feature.

**A second agent module.** `internal/agentinit` is built as a module system —
one `Agent` per coding agent — and has exactly one implementation. Several of
its fields are shaped by that one agent's layout: a user directory and the
variable that moves it, a `skills/` directory, a settings JSON. An interface
with one implementation has not been tested as an interface, and the module
boundary is precisely where a wrong shape would otherwise force a breaking
change just after 1.0 instead of just before it.

**Use by people who are not the maintainer.** Every surface above froze first
as a habit; what promotes a habit to a promise is somebody else depending on
it, and the breaks that matter are the ones discovered in other people's
scripts. A promise made before anyone is holding the other end costs nothing
and proves nothing.

No dates, and this is not a roadmap. When both have happened, 1.0 is a tag
rather than a milestone.
