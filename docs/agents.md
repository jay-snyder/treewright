# Agents

What treewright knows about coding agents, which is deliberately little: an
agent-neutral protocol for reporting state, a module per agent supplying the
wiring, and a way to hand one its first instruction. Part of the design notes —
[`design-notes.md`](design-notes.md) has the rest, and [`tmux.md`](tmux.md)
covers the windows agents run in.

---

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
module per agent. Supporting another agent is a folder and a file beside
`claude.go`, not edits across the tree. The modules are the third instance of a
pattern the repo already had twice: like the shell shims and the tmux snippet,
the wiring is emitted by the binary (`agent-init claude`), so it can never drift
from the `signal` vocabulary it targets.

### The wiring is a plugin, not a paste

For a long time `agent-init` printed a JSON hooks fragment and named a settings
file to paste it into. That is strictly worse than what its two siblings do
without trying. `eval "$(treewright shell-init zsh)"` re-runs at every shell
start and `run-shell` re-reads at every tmux start, so what the user's file
holds is a *reference*: treewright upgrades itself and both integrations follow.
JSON has no `eval`, so the hooks could only ever be a **snapshot** — a frozen
copy of treewright's wiring, in a file treewright would never read again. Add a
fifth signal state or rename a verb and every installed user is silently wired
to the old vocabulary, with `doctor` reporting only that *some* hooks exist.
The reason given for not applying it — a JSON merge would reorder a file the
user owns — was a true statement about the *write*, and not the actual cost.

Claude Code loads any folder under a skills directory that contains a
`.claude-plugin/plugin.json` manifest as a plugin named `<name>@skills-dir`, on
the next session, with no marketplace and no install step. The target path is
one treewright had already claimed for the skill, so the manifest turns a
directory treewright was already writing into a place the hooks can live too:

```
.claude/skills/treewright/
├── SKILL.md                     the driving guide
├── .claude-plugin/plugin.json   the manifest that makes it a plugin
└── hooks/hooks.json             the wiring, in settings' own hooks format
```

`hooks/hooks.json` takes the `hooks` object from a settings file unchanged,
which is what let the fragment move across verbatim. Only `plugin.json` goes
inside `.claude-plugin/` — putting a component in there is the mistake Claude
Code's own documentation calls out, and it fails quietly, loading the plugin
without the part that was moved. A plugin shipping exactly one skill puts its
`SKILL.md` at the root rather than under `skills/`, which is why the path
treewright already used did not have to move.

**`agent-init` writes; its siblings print, and that is not an inconsistency.**
`shell-init` and `tmux-init` print because their output belongs in a file the
user owns and has edited fifty times — and so did the hooks, which is what made
applying them mean a merge. The plugin has no such problem: nothing else lives
in `.claude/skills/treewright/`, treewright named it, and treewright is the only
thing that will ever write there. The half-measure it replaces was already a
write in all but name, since `--skill` printed a `mkdir -p … && treewright … >
…` one-liner for the user to paste back; with three files that recipe stops
being reviewable and starts being a way to install a plugin with one file
missing. Writing is also what makes a second run mean something: a paste can
only be layered on, where rewriting the files treewright wrote makes
`agent-init` after an upgrade an *update*. Unchanged files are left alone and
changed ones are named, so the run says what the upgrade moved.

`--print` keeps the read-before-you-run path that made tmux-init print by
default — these are hooks that will run on every transition of an agent — and
it is the one form that still needs no repository at all.

**Three consequences of the plugin, none of them free.** The skill is
namespaced: `/treewright:treewright`, where the same file without the manifest
beside it was `/treewright`. That is only the typed spelling, since what makes
Claude reach for the skill by itself is the description, and naming the skill
something that reads better under the namespace would make the model-facing
name and the tool's name disagree. A project-scope plugin sits behind the
workspace trust dialog, and it does **not** walk up to the repository root: it
loads from the `.claude/skills/` of the directory Claude is started in. For
treewright that is precisely the semantics wanted — every worktree is its own
root, and the carry is what puts the plugin in each one — but it means the
plugin reaches a worktree only because it was copied there, never because the
main checkout has one. And hooks do not hot-reload, so a session already open
keeps whatever it loaded at start until `/reload-plugins`.

**The plugin's files are checked in, and each is embedded by name.** They live
under `internal/agentinit/plugins/claude/`, so the plugin a contributor reads is
the plugin that gets written: `claude plugin validate` can be pointed straight
at the checkout, and the JSON is JSON an editor will format and lint. Dot
directories survive into a module zip, so `go install` gets the same tree.

The first version embedded the folder — `//go:embed all:plugins/claude`, walked
at startup — on the appealing logic that the directory *is* the plugin and a
file added to it should simply work. That is the wrong trade for what a plugin
directory can hold. Claude Code loads `bin/` as executables on the Bash tool's
PATH and `.mcp.json` as servers to start, so a file appearing in that folder is
not new documentation, it is new code running on the machine of everyone who
installs it, carried into every worktree besides. "A file was added" is the
easiest thing to miss in a diff, and a name beginning with a dot is easier
still; a stray editor artifact would ride along the same way, quietly. So each
file reaches the binary through a `//go:embed` that names it, and a file ships
because somebody wrote its name in Go. (Naming the file also sidesteps the `all:`
prefix entirely: the rule that skips paths beginning with a dot is a rule about
walking a directory, which these deliberately do not do.)

The directory is held to the list from the other side too.
`TestThePluginShipsOnlyWhatItDeclares` walks the real folder — not an embedded
copy, which by construction could not see the file it is looking for — and fails
on anything checked in that no module names. A file that ships nothing while
sitting in the plugin's own directory is its own kind of surprise: the same
failure catches the accident and the contributor who added `hooks/extra.json`
and is wondering why it has no effect.

**Each module owns its whole skill, guide included.** An earlier shape kept the
driving guide as one agent-neutral document that every module wrapped in its own
frontmatter, on the reasoning that the CLI is the same whoever runs it. It is —
but the document is not the CLI. It assumes a reader that loads instructions
from a description, is told which of its own transitions the hooks cover, and is
asked to leave `signal` to them; an agent whose skills are prompt files with no
frontmatter, or whose state reporting works some other way, needs those
paragraphs written differently rather than inherited. So the copies are held
honest by a test instead of by a shared file: `TestThePluginTeachesTheCLIThatExists`
reads every module's plugin and checks that every `treewright <command>` it
names is in the command table and every JSON field it explains is a tag of the
`ls` schema, so a rename that forgets one module's skill fails the build. The
guide's commands spell `treewright`, never `tw`, and here the Argv0 rule bites
hardest: the reader is an agent running commands in a non-interactive shell,
where the `tw` function from a startup file may simply not exist.

**A rule an agent can read and still walk past is not written yet.** Two of the
guide's paragraphs exist because that is what happened, in this repository, to
an agent that had read the section covering the case. Teardown's rule — the
printed `tmux kill-window` line is a question to put to the person, not homework
to hand back — carried an exception for the window the agent was running in, on
the reasoning that closing it ends the session mid-answer. But an agent asked to
tear down the worktree it is standing in meets that window *every* time, so the
exception fired in the only case the rule was written for, and the line went
back as homework. The maintainer's answer is that closing your own window is the
last step of a cleanup rather than an alternative to finishing it: the question
covers it like any other, and what the session ending buys is an ordering —
honour the yes after the final message, not instead of it.

That answer had to be hardened once more, because agents kept finding new
conditions to weigh where the guide had named none: the answer looked obvious,
or the caveats read like prerequisites, or `--yes` had been reflexively added
to `rm` — whose only meaning is answering the window question for the person —
so the binary closed the window and the question never existed to be asked. The
rule is now stated as unconditional: every removal that leaves a window ends in
the question, `rm` is driven without `--yes`, and the one removal with nothing
to ask about is one where treewright itself named no window. Any condition an
agent may weigh before asking eventually becomes the reason it does not ask.

The handoff rule fails differently, and shows what a stated principle is worth
on its own. "The work belongs to the agent in that window" was read and then
ignored twice in one conversation, because a value gives an agent nothing to
notice itself doing. The guide names the symptom — a command naming a worktree
path you just created — and the recovery, which is the part that could not be
guessed: `resume --prompt` looks like the repair and is not, since a window
already open is switched to with the prompt warned as undelivered, so the
worktree has to be removed and remade. That recovery is cheap exactly while the
worktree is clean and unpushed, and the `rm` guards refuse it precisely when it
would stop being cheap — the guide can recommend it without a caveat about
losing work, because the CLI already holds that line.

That paragraph was revised five times and the rule still failed, which is what
the next section is about.

### Enforcing the handoff

**Prose that has been rewritten five times and still leaks is not a wording
problem.** The handoff rule — hand a worktree's work to the agent in its window,
do not carry it out from the window you are in — went through five revisions and
failed again in the same turn an agent read it. The failure was never ignorance.
It was rationalization, and three properties of the text made it available:

- **The escape hatch was cover.** "Without `--prompt` the window opens on an
  agent waiting to be told what to do, which is a legitimate thing to want" was
  written to describe a real case — a worktree readied for a person. An agent
  that had already decided to do the work itself classified into it and moved
  on. A named alternative is a named permission; it now survives only as
  something said out loud, never as what a command falls into by omission.
- **The tell named two tools.** "An Edit or a Write beneath it is the handoff
  not having happened" is checkable, which was the point — but the agent used
  neither. It ran `git -C <worktree> commit` and `cd <worktree> && gh pr create`
  from the window it was already in, and its self-check passed honestly. The
  tell is now any command naming that path, with `git -C`, `cd` and `gh` spelled
  out, and stated as a tripwire rather than a principle: principles get weighed.
- **Finished work was not ruled on.** Every example was work in progress. The
  case that arose was work already complete, with only a commit and a pull
  request left, and nothing said which way that goes — so the agent decided.
  **Publishing is not an exception**: a worktree is created with a prompt, full
  stop, and where the instructions depend on session context a fresh agent
  cannot have — a commit body arguing a design decision, a description
  recounting an audit — the caller writes a file and passes `--prompt-file`.
  There is deliberately no judgment left in it.

**And the prose stopped being the only line of defence.** The plugin's
`PreToolUse` hook runs `treewright guard`, which refuses a tool call that
mutates a worktree other than the one the calling agent is standing in. A hook
fires whether or not the agent has talked itself into something, which is the
property no revision of the text could have.

**It is a subcommand rather than a shell script**, and that is the same call
`signal` made. treewright already knows every worktree path, which configs are
registered, and where the caller is standing; a shell reimplementation globbing
sibling directories would drift from `git worktree list` the first time somebody
moved a checkout, and it would have to be right about path containment, which
is where a string-prefix comparison quietly says `<main>-eng-12` is inside
`<main>-eng-1`.

**What it refuses, and what it must not.** A write into another worktree: an
edit under its path, a `git -C <path>` that is not a read, a `cd <path>`
followed by anything that changes it, a redirect into it. Reads are always
allowed — reviewing another agent's work is the ordinary way to check on it, and
a guard that made review expensive would be worked around rather than obeyed —
and so is everything treewright's own commands do, `rm`, `close`, `resume` and
`send` being both full of worktree paths and the way out the refusal names.

The read/write split is a closed list of read-only programs and git subcommands,
with everything absent from it treated as a write. That is conservative in one
direction on purpose: nothing is consulted until a command has already been
found reaching into somebody else's worktree, where the question is not "is this
safe" but "is this a review". The list deliberately omits the commands that read
in one form and write in another — `git branch` lists branches and also deletes
them — because splitting those on their flags would put a second, subtler parser
in the guard for a question `ls --json` already answers without touching the
worktree at all.

**It is silent and permissive out of scope, and for a sharper reason than
`signal` has.** Outside tmux, outside a registered repository, outside git, in a
repository with no worktrees, on a payload it cannot parse, on a tool it does
not know, or invoked with arguments it does not understand — exit 0, print
nothing, allow. `signal` is quiet out of scope because a hook that nags gets
ripped out; this one can *block*, and a refusal fired where treewright has no
business is not a nag but work somebody has to argue their agent past.

**Being invoked wrong is one of those out-of-scope cases**, which is the one
place `guard` steps outside the "Adding a subcommand" checklist. A PreToolUse
hook blocks on exit 2 and on nothing else, and `ErrUsage` is also exit 2 — so a
`guard` that returned a usage error would refuse every tool call in the session
and hand back its own help text as the reason. Every other non-zero exit is a
note in the transcript with the call allowed through, so the fail-open direction
is the default one.

**The refusal is the point of the hook, not a side effect of it.** A refusal
that only refuses teaches an agent to look for a spelling that gets past it;
one that names the fix ends the turn the way it should have gone. So it names
the worktree, what was refused, and the two commands that reach the agent
standing there — through the same message writers as everything else, because
the agent reads it now and the maintainer reads it in the transcript later.

**The window is deliberately not part of the test.** The guard asks where the
work lives, not whether anyone is currently home: a worktree whose window has
been closed is exactly the state an agent could manufacture to get past a
window-shaped rule, and `resume --prompt` is the way back into one either way.

### Procedure belongs in the binary, not in the guide

An audit put roughly a hundred of the guide's two hundred and forty lines in one
category: *procedure*. Ordered command sequences with a distinct silent failure
at each step, written out as prose for an agent to follow. Two of them lost work
when it did not.

Prose is the wrong container for that, and the two rules above say why in the
strongest terms available — those paragraphs exist because an agent read the
section covering its case and did the other thing anyway. A sequence an agent can
deviate from is a sequence an agent will deviate from, however carefully it is
worded, and the fix is not better wording. Four sections became commands:

- **The long brief** — a file in `/tmp` and a sentence pointing at it — is
  `--prompt-file`. Twenty-five lines of where to put the file and what to say
  about it were a path decision and a template, both of which a binary can hold.
- **Moving work out of the base checkout** is `treewright move`. Six commands in
  a strict order, where the order was the safety and `git clean -fd` was the
  improvisation to expect.
- **Reaching an agent in an open window** is `treewright send`. Four mechanical
  rules the prose could only ask for, in the one place the whole guide stepped
  outside the binary and taught raw `tmux`.
- **Which window is your own, and which ends its session**, is two fields in
  `ls --json` rather than a `tmux display-message` and some arithmetic.

**And one rule was deleted rather than moved.** The guide told an agent to read
the base row's `unpushed` before `new` and push first — which `warnIfBaseIsAhead`
had been doing all along and better, comparing the base branch against origin's
rather than against the checkout's working state, and offering the cherry-pick. A
rule asking an agent to do by hand what the tool already does on its own is worse
than no rule: it is one more thing to get right for no gain, and it goes stale
silently the day the tool's own version improves.

What stays in the guide is the judgment each of those was carrying.

For the brief, **when the file is deleted**. Placement no longer needs teaching,
there being nothing to construct — but treewright neither copies the file nor
removes it, and a temporary directory swept eventually is not cleanup. The two
alternatives the old prose warned against are worth recording here, since they
are what agents actually chose: a `BRIEF.md` in the worktree just created arrives
as an untracked file in the diff a maintainer reviews, and `.git/treewright/` —
the tidy-looking one, picked by two different agents — stays out of `git status`,
which is exactly why nothing would ever remove it and nobody would ever see it
again.

For the move, **that it is not to be done by hand**, and the shape of why: until
the work is somewhere else the main checkout is the only copy of it. The `git
stash` warning stays too, that being the shortcut a reader proposes next.

For reaching an open window, **whose session it is**. `send` refuses what the
prose could not — the caller's own window, and one held open after its agent died
— so what is left is that `waiting` is the state this exists for, that a window is
somebody else's session and takes instructions rather than keystrokes driving
their UI, and that a window a person is typing in is one to leave alone. See
"Typing at the window" in [`tmux.md`](tmux.md).

`send` also gives the botched handoff above a second way out, and the guide says
so rather than leaving two paragraphs to disagree: the idle agent `new` left in a
window can be typed at, so "the worktree has to be removed and remade" is the
recommendation while the worktree is empty rather than the only route. Reaching
the agent in place is what still works once the worktree holds something `rm`
would rightly refuse to throw away.

**The manifest's version is not treewright's.** A skills-directory plugin is
discovered in place rather than copied into a cache, so nothing compares
versions to decide whether to reload — the files on disk *are* the plugin.
Stamping the running binary's version into the manifest would rewrite it on
every release to report a change the wiring did not make. What actually catches
a stale install is `doctor`, which compares the installed files with the ones
`agent-init` would write today: a byte comparison, with no false positives, and
the check the pasted fragment made impossible.

### The copy in a worktree, and why it needed a command of its own

The plugin is treewright's to rewrite, and for a long time only one of its copies
was actually rewritten. `agent-init` writes to the main checkout — run from
inside a worktree it resolves the config and writes to the main checkout too, not
to where you are standing — and the copy in a worktree is made once, by the
carry, at `new` time. Nothing looked at it again: `doctor` inspected the main
checkout and the user-level directory and enumerated no worktrees at all.

So a worktree created the day before an upgrade ran its agent against the old
hooks and the old skill for as long as the worktree lived, no command fixed it,
and the report said `ok`. That is the frozen snapshot this whole design exists to
abolish — "a copy of treewright's wiring in a file treewright would never read
again" — reintroduced one directory below the file that abolished it. Rename a
signal verb and every pre-upgrade worktree errors on every agent transition,
invisibly.

Two things close it. `doctor` walks the repository's worktrees and byte-compares
each carried copy the way it already did for the main checkout, reporting at most
two findings — the copies that are out of date, and the worktrees that never got
one — each carrying its worktrees as a list rather than a finding apiece. And
`refresh` rewrites every copy at once, which is the command those findings name.
A missing copy is only worth reporting where the config carries the plugin: a
worktree with nothing in it and no carry configured is the "reaches no worktree"
finding already said once about the repository, and saying it again per worktree
would turn one misconfiguration into a column of them.

The carry itself stays a one-time copy, deliberately. Making `new` the moment the
plugin is checked would put a byte comparison of every plugin file in the path of
the command whose whole job is to be fast, to catch a staleness that only arises
between releases. The check belongs where the question is asked — see "Upgrading
treewright itself" in [`design-notes.md`](design-notes.md).

### Where the wiring goes, and the trap between the placements

Per-repo is the default, and it is the main checkout's
`.claude/skills/treewright/`: treewright is a tool you use in *some*
repositories, and the wiring should follow that rather than assert itself in
every checkout on the machine. Machine-wide (`--global`, into the agent's own
`~/.claude/skills/treewright/`) is offered second, for someone who does want
every repository covered at once, and costs little because `signal` silently
no-ops outside treewright windows.

The per-repo placement has a trap, and closing it is what `agent = "claude"` is
for. That directory is uncommitted — treewright invented the path, so nothing
ignores it and nothing tracks it until somebody says which — so **every
worktree treewright creates starts without it**, and wiring placed there reaches
the MAIN window and no worktree at all unless something carries it. Project-scope
plugins loading from the start directory rather than the repository root make
that exact, not approximate. The half-configured state looks finished, which is
why `doctor` checks for it. The committed project file would work mechanically
and is not offered: it imposes treewright on every teammate who clones the repo.

`.claude/settings.local.json` keeps its place on the carried list, and for a
different reason than it used to have: it no longer holds hooks, but it does
hold the "always allow" permission decisions the agent records as it works, and
a worktree starting without them re-asks every one from scratch.

**What is per-project is what is carried, derived rather than listed.**
`Agent.LocalState()` is computed from the module's project paths instead of
being a field beside them, because a module that named a per-project artifact
and forgot to carry it would recreate the trap one directory deeper — which is
exactly what happened to the skill, whose first version was user-level only.
Deriving the list means a module cannot describe a per-project file it does not
also carry, and with the plugin read off an embedded directory that now reaches
the artifacts themselves: a file added to the folder is carried without anyone
editing a list. The list names each file rather than the directory holding them,
because `carryFiles` copies files one at a time and a `carry_files` entry naming
a directory would have no meaning for the warn-when-missing rule such an entry
exists for. Whether any of it ends up in git is the repository's business:
treewright writes to no `.gitignore` and generates none.

**The `agent` config key is a defaults bundle, and the carry is the point.**
`agent = "claude"` names a module and takes its command and resume_command as
defaults — explicit keys still win field-by-field, so agent-plus-command is
override rather than a load error; the file still says which command runs. What
the key adds that has no other spelling is the carry: the module's local-state
files are copied into every new worktree as if `carry_files` listed them, which
dissolves the placement trap and gives every worktree both the plugin and the
permission decisions already granted in the main checkout. The implicit carry
differs from an explicit entry in exactly one way — absent from the main
checkout it is skipped silently, because unlike a `carry_files` entry nobody
asserted it exists. An unknown agent name is a load error naming the modules
there are: the key's whole value is the module it names, and a misspelling that
fell back to the global defaults would leave the carry quietly not happening.

**The module is never inferred from `command`.** Two configs saying
`command = "claude"` behaving differently on an invisible guess is the kind of
rule the config format exists to avoid; the key is one line, and writing it is
what asks for the bundle. The one place a guess is allowed is `doctor`'s
wiring check, which may sniff the command's first word — a warn-level hint can
rest on a guess in a way behavior never can. `setup` writes the key when it
finds the agent's binary on PATH, which is as close to consent as detection
gets — and one commented line to remove when it guessed wrong.

**What `doctor` asks about the wiring**, in order: is the plugin installed, in
the checkout or at user level; is what is installed what this treewright would
write; does the per-repo copy reach the worktrees; is each worktree's own copy
what this treewright would write; does git either ignore it or track it; and is
there a hooks paste left in a settings file. The paste is the
upgrade path — one an older treewright printed still works, because Claude Code
runs hooks from every scope it loads, so it is reported as wiring nothing can
update when it is all there is, and as a duplicate to delete once the plugin is
in.

The ignore check is the one `agent-init` already speaks to, and saying it twice
is deliberate. `agent-init` says it unconditionally, at install time, because the
answer costs a git call to learn and the sentence is worth reading either way —
but that sentence is read once, by somebody in the middle of installing, and the
state it warns about outlives it. A plugin neither ignored nor committed shows as
untracked in the main checkout and, being carried, in every worktree made after
it: a `??` beside a directory the developer did not create, in eight checkouts at
once. `doctor` is where a half-configured repository gets asked again, and it is
also where the question can be answered rather than assumed, git already being in
hand. Both ways out are named because both are the repository's decision and not
treewright's: ignoring keeps the wiring local to whoever wants it, committing
hands it to everyone who clones, and a team may well want the second. treewright
still writes to no `.gitignore` — what `doctor` reports is a state, and what it
offers is a sentence.

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
- **A prompt too long for tmux to run is refused at that same point.** tmux
  carries a command to its server in one message and refuses anything past
  16364 bytes, which `new` cannot usefully report after the fact: the branch and
  the worktree exist by the time a window is asked for, and `new` deliberately
  does not fail on a window it could not open. So the assembled command is
  measured where the placeholder is checked, and the error names the size, the
  limit, and the two ways on — shorten the prompt, or put it in a file and pass
  a prompt naming that file. See "Reporting what failed" in
  [`design-notes.md`](design-notes.md) for the ceiling itself.

  The second of those used to be "paste the text to the agent once the window is
  open", which asks for two things the reader does not have. There is no window:
  the refusal happens before anything is created, so the paste route silently
  requires opening one without a prompt first. And there is no keyboard whenever
  the caller is an agent, which for `--prompt` is the common case — the flag
  exists to hand work to an agent, and the thing typing it is increasingly
  another one. The file route needs neither, works for a person as well, and is
  now `--prompt-file` rather than a sentence to compose.

### `--prompt-file`, and why the sentence is treewright's

The file route was taught before it was built: twenty-five lines of the claude
module's guide spelling out where to put the brief, what sentence to point at it
with, and which two obvious homes fail invisibly. All of that is a path decision
and a template, and both belong in the binary — prose is something an agent can
read carefully and still deviate from, where a flag is not.

So `--prompt-file <path>` takes the file and builds the prompt:
`read <path> in full — it is your complete brief`. The wording lives in one
place, `promptPointer`, because it is the whole of what the flag adds and
because it has to survive being read by an agent that will act on it — "in full"
is what stops a reader skimming the first heading and starting work.

Three things it does that the taught version could not be relied on to:

- **The path is made absolute.** The command runs in the new worktree, so a
  relative path resolved there names a file that is not in it. A reader typing
  `--prompt-file brief.md` from the main checkout gets the file they meant.
- **The file is checked before anything is created** — missing, a directory, or
  empty — beside `fillPrompt`'s own refusals and for the same reason: a bad
  invocation should not leave a half-made worktree behind an error about a flag.
  Empty is on that list because an agent sent to read nothing has nothing to go
  on and no way to say so.
- **It fills one setting with `--prompt`, and passing both is an error.** Any
  precedence we picked would be a rule to learn, in the one place where the cost
  of guessing wrong is an agent working from the wrong instructions.

The ceiling is a consequence rather than the point. A pointer is short whatever
the file holds, so it never approaches `checkCommandFits`'s limit — but the file
is worth it well before then, since it can be re-read after a compaction and
outlives the session that wrote it.

**treewright neither copies the file nor deletes it**, and the help says so.
The path travels in the agent's command line, so the file has to be there when
the window opens and for as long as the agent may want it again; deleting it
once the work has landed stays the caller's, which is the one part of the old
prose that survives into the guide.

**A prompt that reached no agent is warned about.** The command carrying it
runs only in a window that was actually created, and `resume` mostly finds
windows rather than creating them — so `openWindow` reports which of the two
happened, and a prompt that landed on an already-open window gets a warning
naming the recovery: paste it there. Silently dropping text the user typed
once and cannot retype from memory would be the quiet failure everything else
here refuses to be. `base` takes no `--prompt` at all, since reusing its one
window is its normal case — the flag would warn more often than it worked.

## When there is nothing to resume

`resume` runs `resume_command`, and `resume_command` is "carry on where I left
off" — `claude --continue` and its like. A worktree whose first agent never
started has nothing to carry on from, so that command exits on saying so and the
held-open window parks on the error.

Such a worktree used to be unreachable. `new` refuses the slug, the worktree
being right there, and names `resume` as the way in: the one command that could
not work. `ls` shows a healthy row with an empty WINDOW column and nothing to
say why. Removing the worktree and starting over was the only way out — a long
way to go for a window that failed to open.

treewright cannot ask an agent whether it has a conversation in a directory, so
it keeps a note of its own: `.git/treewright/no-agent-yet-<slug>`, written when
`new` makes the worktree and taken off the moment a window actually runs the
command. `resume` reads it and runs `command` instead, and says so rather than
doing it quietly — a fresh agent appearing where you expected your session back
is a thing to explain. That is also what makes `new`'s "open it with
`tw resume <slug>`" true again, rather than a signpost to the one command that
cannot help.

**The marker is the negative, and that is the whole decision.** A marker saying
*an agent has run here* would be missing from every worktree made by a treewright
that never wrote one — worktrees in use for weeks, holding exactly the
conversation `--continue` wants — and the fallback would greet each of them with
a fresh agent and no history. A first-run heuristic that silently discards
somebody's session is a worse bug than the one being fixed. So absence has to
mean *as before*: the state that already existed stays silent, and the marker is
the news.

It records that an **agent was started**, not that a **window was opened**. Those
are the same thing nearly always, and they come apart in exactly the case this
exists for.

**The base checkout is left out of it.** It is not a worktree treewright made, so
there is no moment at which treewright could honestly write that nothing had ever
run in it — and `tw base` opens that window with `command` already, so the way in
that needs no conversation is a command of its own rather than a fallback.

A `--fresh` flag on `resume`, with `new`'s error naming it, was the alternative
and it loses on both counts: it is one more thing to know at exactly the moment
you are confused, and the question it puts to the user — *has an agent ever run
here?* — is one treewright is in a better position to answer.

