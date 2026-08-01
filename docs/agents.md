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

**The manifest's version is not treewright's.** A skills-directory plugin is
discovered in place rather than copied into a cache, so nothing compares
versions to decide whether to reload — the files on disk *are* the plugin.
Stamping the running binary's version into the manifest would rewrite it on
every release to report a change the wiring did not make. What actually catches
a stale install is `doctor`, which compares the installed files with the ones
`agent-init` would write today: a byte comparison, with no false positives, and
the check the pasted fragment made impossible.

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
write; does the per-repo copy reach the worktrees; does git either ignore it or
track it; and is there a hooks paste left in a settings file. The paste is the
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

**A prompt that reached no agent is warned about.** The command carrying it
runs only in a window that was actually created, and `resume` mostly finds
windows rather than creating them — so `openWindow` reports which of the two
happened, and a prompt that landed on an already-open window gets a warning
naming the recovery: paste it there. Silently dropping text the user typed
once and cannot retype from memory would be the quiet failure everything else
here refuses to be. `base` takes no `--prompt` at all, since reusing its one
window is its normal case — the flag would warn more often than it worked.

