# Agent integration — design draft

Status: **phases 1 and 2 shipped; phase 3 remains**. This is the plan for
making treewright and coding agents better at working together, written before
the code so the decisions get argued about once, here. When a piece ships, its
"why" moves into `docs/design-notes.md` next to the behavior it explains, and
the corresponding section here dissolves; when everything has shipped, this
file goes away.

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

## Phase 2 — `agent-init` and the `agent` key (shipped)

Built, and documented in the "Agent modules" section of `docs/design-notes.md`:
the module pattern, why `agent-init` prints rather than applies, the two hook
placements and the carried-file trap between them, the `agent` key's
defaults-bundle semantics with the silent-when-absent carry, and `doctor`'s two
wiring checks. What later phases need to know from it:

- Facts about an agent live in `internal/agentinit`, one module per agent —
  phase 3's launch templates belong to the module as much as to the config
  defaults.
- The module today supplies the plain `claude` / `claude --continue`; phase 3
  upgrades those to the `{prompt}` placeholder forms.

### Still in the module's future

A second artifact the module can emit now that hooks have proven the pattern: a
skill or `CLAUDE.md` fragment that teaches the agent to *drive* treewright —
see the estate with `tw ls --json`, spawn parallel work with `tw new`, never
touch `git worktree` directly, `rm` is guarded. That is the reverse direction
(agent → treewright) and it needs no new core surface at all, which is the
sign the protocol boundary is drawn right.

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

## Checklists phase 3 must clear

Per `CLAUDE.md`: `--prompt` lands in the `commands` table's flag docs and in
completion (`TestEveryFlagIsDocumented` ties the two), the README's config
reference gains the placeholder, and the agent module's command templates move
to their `{prompt}` forms in the same change as `DefaultCommand` — two
spellings of one template drifting is exactly what the module exists to
prevent. `new`'s output-contract row is unchanged: still the path alone.

## Open questions

1. **The verb.** Settled: `signal` shipped, and the hooks agent-init prints
   spell it into users' settings files, so a rename is now a breaking change
   nobody should make casually.
2. **The `waiting` marker on window names.** Shipped as proposed — `clear` and
   the wrapper tail both remove it — and now in the listening period: it is
   still treewright mutating a name the user can see, and the fallback
   (status-line-only via `#{@treewright_agent_state}`) remains available if it
   grates.
3. **`Notification` granularity.** Claude's Notification hook covers both
   "needs permission" and "idle too long". Both genuinely mean `waiting`, so
   one state may be fine — but if not, the hook's stdin payload can
   distinguish them, which is the `@treewright_agent_message` door.
