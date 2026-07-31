# Agent integration — design draft

Status: **draft, not implemented**. This is the plan for making treewright and
coding agents better at working together, written before the code so the
decisions get argued about once, here. When a piece ships, its "why" moves into
`docs/design-notes.md` next to the behavior it explains, and the corresponding
section here dissolves; when everything has shipped, this file goes away.

## The principle: protocols in core, wiring in modules

treewright knows nothing about Claude today except two default strings, and
that is a property to keep, not a gap to fill. Every idea below is therefore
split in two:

- **Core provides a protocol** — an agent-neutral verb or contract. No agent's
  name appears in it, and nothing in core changes when an agent is added.
- **A module provides the wiring** — the agent-specific glue connecting one
  agent to that protocol, emitted by the binary the way the shell shims and the
  tmux snippet already are, so it can never drift out of sync with it.

The repo already has this pattern three times over: `internal/shellinit` for
each shell, `internal/tmuxinit` for tmux. Agents become the third instance:
`internal/agentinit`, one file per agent, `claude.go` first, `copilot.go`
whenever that day comes.

What stays out on purpose: an MCP server. MCP is the cross-agent standard, but
agents already drive treewright through the CLI — `ls --json` exists for
exactly that — and a stdio JSON-RPC server is real surface area in a project
whose whole build is two dependencies. Revisit if an agent shows up that cannot
run shell commands.

---

## Phase 1 — the signal protocol (shipped)

Built, and documented where shipped behavior lives: the "Agent state" section
of `docs/design-notes.md` carries the reasoning — the closed vocabulary, state
on the window rather than on disk, the silence contract, the `!` waiting
marker, the held-open exception. What later phases need to know from it:

- The verb is `treewright signal <state>`, states `working` / `waiting` /
  `done` / `clear` — the hook mapping below targets exactly these.
- Out of scope it exits 0 in silence, which is what makes the user-level hook
  placement below cost nothing.
- The state option is `@treewright_agent_state`; `ls --json` reports it as
  `agent_state`.

---

## Phase 2 — `agent-init` and the `agent` key, the module pattern

### The command

```sh
tw agent-init claude
```

prints the Claude Code configuration that wires its hooks to `signal`, with
instructions, exactly as `shell-init zsh` prints a shim. The emitted commands
spell `treewright`, never `tw` — they are destined for a file a program reads,
which is the Argv0 rule already in force.

No `--apply` in the first version. tmux-init's `--apply` is safe because
`source-file` is additive; applying hooks means *rewriting*
`~/.claude/settings.json`, a user-owned JSON file that a merge-and-rewrite
would reorder and reformat. Printing the snippet and naming the file is the
honest version until there is a merge strategy worth trusting.

### The Claude module

`internal/agentinit/claude.go`, string constants, tested by parsing the JSON
(the same way the shells parse the shims and a real tmux server loads the
snippet — the check is "would the consumer accept this", not "does it look
right"). The hook mapping:

| Claude Code hook | Fires when | Signal |
|---|---|---|
| `UserPromptSubmit` | The user sends a prompt | `working` |
| `Notification` | Claude needs permission, or has sat idle waiting for input | `waiting` |
| `Stop` | Claude finishes responding | `done` |
| `SessionEnd` | The claude process ends | `clear` |

`SessionStart` deliberately maps to nothing: a fresh window sits at the
agent's prompt because the human *just made it* — signaling `waiting` there
would make every `tw new` open a window that is already demanding attention.

As JSON, what the command prints:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "treewright signal working" }] }
    ],
    "Notification": [
      { "hooks": [{ "type": "command", "command": "treewright signal waiting" }] }
    ],
    "Stop": [
      { "hooks": [{ "type": "command", "command": "treewright signal done" }] }
    ],
    "SessionEnd": [
      { "hooks": [{ "type": "command", "command": "treewright signal clear" }] }
    ]
  }
}
```

### Placement: two that work, and the trap between them

Claude Code merges hooks from every settings scope and runs them all, so the
hooks need to exist in exactly one place. Two placements work, and the
instructions `agent-init` prints describe both:

- **User-level, `~/.claude/settings.json`.** One file, every repo and every
  worktree covered, nothing per-repo to remember. The cost is that the hooks
  fire in every claude session everywhere — which is nothing, because outside
  a treewright window `signal` silently no-ops; that is what the exit-0
  contract above is *for*.

- **Per-repo, `.claude/settings.local.json` carried by treewright.** The file
  is gitignored, which makes it precisely the class of file `carry_files`
  exists for: present in the main checkout, copied into each new worktree at
  creation, imposed on no teammate. One config line does it:

  ```toml
  carry_files = [".claude/settings.local.json"]
  ```

  No committed `.claude/settings.json` is needed — local settings load on
  their own. This placement also carries a bonus: Claude Code records
  "always allow" permission decisions in that same file, so every new
  worktree starts with the permissions you have already granted in the main
  checkout, instead of re-asking. The known cost is carry semantics: a
  worktree gets a *copy*, so editing the main checkout's hooks later does not
  reach worktrees that already exist — the same property `.env` carries have
  today.

  `setup` already guesses `carry_files` by scanning what git ignores, so it
  should propose this entry whenever the main checkout has a
  `.claude/settings.local.json`, hooks or no hooks — the permissions
  carry-over is worth it by itself. (The `agent` key below makes the carry
  automatic for configs that set it; this entry is the spelling for configs
  that don't.)

The committed project file, `.claude/settings.json`, works mechanically but
imposes treewright on every teammate who clones the repo, so the instructions
do not offer it.

The trap is the half-configured middle: hooks in the main checkout's
`settings.local.json` with **no** `carry_files` entry. That works in the MAIN
window and silently does nothing in every worktree — the windows the feature
exists for — which is exactly the shape of failure `doctor` is for, below.

### The `agent` key

This phase ships a config key alongside the command, because the two answer
each other: `agent-init` wires the hooks, and the key is what makes the
wiring reach every worktree without a line to remember.

An earlier draft held the key to a low bar as a distant phase 4 — a preset
saving two short strings, to be cut if the bundle never grew a third member.
The settings.local.json finding is the third member, and it is a different
kind from the first two: knowledge rather than typing. Which gitignored file
holds an agent's per-project local state — its hooks, and the "always allow"
permission decisions Claude Code writes to the same file — is a fact about
the agent, not about any repository, and a fact every user of that agent
needs identically. A module is where facts about an agent live.

`agent = "claude"` names the module, and the module supplies three defaults:

- `command = "claude {prompt}"`
- `resume_command = "claude --continue {prompt}"`
- the agent's local-state files are **always carried**:
  `.claude/settings.local.json` is copied into every new worktree, exactly as
  if `carry_files` listed it.

(Until phase 3 ships the placeholder, the first two are the plain `claude`
and `claude --continue` the config already defaults to.)

The third is what dissolves the placement trap above for anyone who sets the
key: hooks in the main checkout's `settings.local.json` reach every worktree
with no `carry_files` line to remember, and worktrees inherit the permissions
already granted in the main checkout whether or not the hooks are installed
at all.

The implicit carry differs from an explicit entry in exactly one way: absent
from the main checkout, it is silently skipped rather than warned about. An
explicit `carry_files` entry warns when missing because the user asserted the
file exists and a missing one is a stale config; the implicit one was
asserted by nobody, and a checkout that has never run the agent has nothing
to carry yet. Everything else follows the rules the config already has:

- `tw config` reports the carry in force, marked as coming from `agent`, so
  the gap between the file and the behavior it produces stays closed.
- Listing the same path in `carry_files` too changes nothing — deduped, not
  doubled, not an error.
- Explicit `command` / `resume_command` override the module's, field by
  field. This is a defaults bundle like `base_branch`'s default, not a second
  spelling like `branch_prefix`/`branch_prefixes`, so agent-plus-command is
  override rather than a load error: the file still says which command runs,
  because the command key is sitting right there in it.
- The key's value must name a module that exists; anything else is a load
  error listing the modules there are, like an unknown branch prefix.

The module is *not* inferred from `command`'s first word. Two configs saying
`command = "claude"` behaving differently depending on an invisible guess is
the kind of rule the config format exists to avoid; the key is one line, and
writing it is what asks for the bundle.

`setup` detects an installed agent with a module and proposes the key,
commented like every other guess — which subsumes the placement section's
"propose the carry entry" behavior for repos that take it.

Opt-out is deliberately not designed. Someone who wants worktrees that start
without their local settings can leave the key unset and write the two
command lines by hand — that is the escape hatch already, and inventing an
`agent_carry = false` spelling before anyone has asked for it would be a rule
to learn against a need that may not exist.

### `doctor` learns about it

Two warn-level checks alongside the existing "tmux integration not loaded"
one, both in the shape of `doctor.go`'s binding check — the integration is
optional, but *silently half-installed* is the state doctor exists to catch:

- When a config's `command` starts with an agent that has a module, and
  neither the user-level settings nor the main checkout's
  `.claude/settings.local.json` mention `treewright signal`, doctor says the
  signal wiring is absent and names `agent-init claude` as the fix.
- When the main checkout's `.claude/settings.local.json` *does* mention
  `treewright signal` but `carry_files` does not list it, doctor names the
  trap: hooks that fire in MAIN and in no worktree, fixed by one
  `carry_files` line.

Both checks read the `agent` key where a config sets it, and fall back to
sniffing `command`'s first word where it doesn't — a warn-level hint may rest
on a guess in a way behavior never would. The second check applies only to
configs without the key: the automatic carry leaves the trap nowhere to
occur.

### Later, in the same module

A second artifact the module can emit once hooks have proven the pattern: a
skill or `CLAUDE.md` fragment that teaches the agent to *drive* treewright —
see the estate with `tw ls --json`, spawn parallel work with `tw new`, never
touch `git worktree` directly, `rm` is guarded. That is the reverse direction
(agent → treewright) and it needs no new core surface at all, which is the
sign the protocol boundary is drawn right. Out of scope for the first cut.

---

## Phase 3 — the kickoff prompt

`tw new` opens a window whose agent sits waiting to be told what the ticket
is, when the person typing the command knew perfectly well:

```sh
tw new eng-2318-cart-total-rounding --prompt "fix the cart rounding per the ticket"
```

The agent-neutral mechanism is a placeholder in the command template:

```toml
command = "claude {prompt}"
```

- With `--prompt`, `{prompt}` becomes the shell-quoted text, through the same
  `shellQuote` the eval file trusts.
- Without it, the placeholder is removed entirely — **not** substituted as
  `""`, which would hand the agent an empty first argument that means
  something.
- `--prompt` against a command with no `{prompt}` is an error telling the user
  to add the placeholder. Appending the text instead would be treewright
  guessing at an arbitrary agent's CLI, and a guess that happens to work for
  claude is still a guess.

`DefaultCommand` becomes `claude {prompt}` and `DefaultResumeCommand` becomes
`claude --continue {prompt}`, so both work out of the box and
`tw resume eng-2318 --prompt "now address the review comments"` falls out of
the same mechanism. `new`'s stdout is unchanged: still the path, nothing else.

This is the piece that turns treewright from a workspace tool into a dispatch
tool — `tw new` becomes "assign this ticket", not "prepare a desk".

---

## Reserved, not designed

Two option names are reserved now so nothing else squats on them, but their
design waits for evidence the states alone aren't enough:

- `@treewright_agent_message` — a short "why", e.g. the permission being asked
  for, from the hook's stdin payload.
- `@treewright_agent_since` — when the state was set, for "waiting for 25m" in
  `ls`.

---

## Checklists the phases must clear

Per `CLAUDE.md`'s subcommand list, for `signal` and `agent-init`: the
`commands` table, completion in all three shims, a README commands row, and an
output-contract row in design-notes (`signal`: stdout empty; `agent-init`: the
snippet). Both commands' prose passes `TestMessagesShareOneVoice`.

Testing, against the fixture's private tmux server as always:

- `signal` set-and-read-back through the package, never bare `tmux`; the
  silent-no-op cases (outside tmux, unregistered repo, no window) asserted as
  *silent* — empty stderr, exit 0.
- `TestStdoutCarriesOnlyTheAnswer` extended to `signal`.
- The claude hooks snippet parsed as JSON and its hook names checked against a
  fixed list, the consumer-side check the shims set the precedent for.
- The `AGENT` column: present when a state exists, absent when none does, and
  `TestPopupSizeCoversTheTable` re-proving the size estimate over the wider
  table.
- The `heldOpenOnFailure` tail: a held-open window's state option is gone.

## Open questions

1. **The verb.** `signal` reads well but brushes against unix signals;
   `mark` and `state` were the runners-up. Cheap to rename until phase 2
   ships, expensive after — the hooks in users' settings files spell it.
2. **The `waiting` marker on window names.** `!ENG-2318` is visible in any
   status line with zero setup, but it is treewright mutating a name the user
   can see and may have opinions about. The fallback is status-line-only
   (document `#{@treewright_agent_state}`, decorate nothing), which costs
   discoverability. Proposed: ship the marker, make sure `clear` and the
   wrapper tail both remove it, and listen.
3. **`Notification` granularity.** Claude's Notification hook covers both
   "needs permission" and "idle too long". Both genuinely mean `waiting`, so
   one state may be fine — but if not, the hook's stdin payload can
   distinguish them, which is the `@treewright_agent_message` door.
