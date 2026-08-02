# treewright — orientation for Claude

A Go CLI that gives each piece of work its own git worktree, tmux window, and
agent session, created and torn down together. No runtime dependencies beyond
the `git` and `tmux` binaries; two Go modules (`BurntSushi/toml`, `x/term`).

This file is the map of the code and the rules that are easy to break. Two
companions:

- **`README.md`** — the user-facing tour: pitch, install, quickstart, command and
  config reference. Deliberately short and non-technical. When user-visible
  behavior changes, it changes with it — but keep new rationale out of it.
- **`docs/`** — why the behavior is what it is, in three files. Read the relevant
  section before changing behavior in that area; add to it when you decide
  something a future reader would otherwise re-litigate.
  - **`design-notes.md`** — the base checkout, worktree and window naming, branch
    prefixes, the full output contract, statuses and squash-merge detection,
    what gets reported when a background step fails, the safety rules, what
    treewright is allowed to write, configuration, the eval-file protocol.
  - **`tmux.md`** — session per repo, window identity, terminal titles, popup
    sizing and the key bindings.
  - **`agents.md`** — the agent-state protocol, agent modules and the plugin
    they install, the kickoff prompt.

## Committing is the maintainer's call, every time

**Never run `git commit`, `git push`, or open a pull request without being asked
for that specific action.** Permission for one commit is not permission for the
next one, and a task description that says "commit as you go" describes the
shape of the work rather than granting it in advance.

Finish the work and leave it in the working tree. Then say what you would
commit and with what message, and wait. The point is that what lands in this
repository's history is read before it lands, not audited afterwards — a commit
made unasked is not a step forward, it is work the maintainer now has to inspect
and undo. If one was made in error, `git reset --soft HEAD~1` puts every change
back in the working tree with nothing lost.

## Driving the built binary by hand happens on a scratch server

**Any hands-on exercise of the binary — a throwaway repo, a demo `new`, a
popup, anything that reaches tmux or the registry — sets a scratch
`TREEWRIGHT_TMUX_LABEL` and a scratch `TREEWRIGHT_CONFIG_DIR` first.**

```sh
export TREEWRIGHT_TMUX_LABEL=twscratch       # a server nobody is attached to
export TREEWRIGHT_CONFIG_DIR="$(mktemp -d)"  # a registry that is not the maintainer's
./treewright setup demo                      # from inside the throwaway repo
./treewright new eng-1
tmux -L twscratch kill-server                # the socket outlives the run
```

The danger is not that treewright misbehaves. It is that it behaves correctly on
a client you did not realize you shared. A Claude Code Bash call inherits `$TMUX`
from the pane its session was started in, so `tmux.Focus` finds `Inside()` true,
finds the scratch session's name different from the current one, and does exactly
what it is for: `switch-client`. There is one client and it is the maintainer's.
That is how a demo `new eng-1` against a repo in a scratchpad threw an attached
maintainer out of their real treewright session into a fresh one holding a single
ENG-1 window, with detach-and-reattach the only way back. `TREEWRIGHT_CONFIG_DIR`
is the same argument one layer down: a scratch repo registered in the real
registry is a repo that `ls`, `doctor` and every popup keep answering about long
after the demo is gone.

The test suite is already isolated and needs no change — `newFixture` gives every
test that reaches a server its own `-L` label under its own `TMUX_TMPDIR`. To
*prove* that rather than read it, put a shim ahead of `tmux` on `PATH` that
resolves the socket each call would land on the way tmux itself does — `-L`/`-S`,
then `$TMUX`, then `$TMUX_TMPDIR/tmux-$UID/default` — and fails on the developer's
default socket. An escape then arrives as a failing test rather than as a
surprise for whoever is attached.

## Build, test, lint

```sh
go build -o treewright .   # /treewright is gitignored; reports "v0.1.0+dirty" and
                           # the like, from the build info (see internal/cli/version.go)
go test ./... -count=1     # unit + end-to-end, against throwaway git repos
go vet ./...
gofmt -l .                 # must print nothing — CI fails on any output

# The linter, pinned to the version CI runs and .golangci.yaml was written
# against. Not a module dependency: it would put dozens of indirect requirements
# in go.sum for a project whose whole build is two.
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 fmt ./...  # applies gofumpt + goimports
```

The lint config is meant to be read before it is edited. Style is enforced rather
than exempted — staticcheck's `ST` checks (stylecheck, which golangci-lint disables
by default), revive, godot, gofumpt, funcorder's constructor rule — and the linters
left out are listed in `.golangci.yaml` with the reason, each grounded in Go's own
guidance rather than in this repo's habits. Three exemptions exist, all narrow and
all written where they apply: `//nolint:nilerr` on completion's four
returns, `//nolint:usetesting` on `testenv.PrivateTmuxServer`'s socket directory
(the one place every tmux-driving test gets its isolation from), and errcheck's
standard exclusion of the `fmt.Fprint` family. `nolintlint` requires every one of
them to name its linter and say why, so a bare `//nolint` will not pass.

CI (`.github/workflows/ci.yaml`) runs all of it on ubuntu and macOS, plus
golangci-lint and a coverage floor of 85% per platform (the suite measures 86.4%
on each, so the margin is about a point and a half). It runs on pushes to `main`
and on every push to a pull request. Every YAML file in the repo is `.yaml`,
including the workflows.

**Work reaches `main` through a pull request.** A repository ruleset on the default
branch requires one, takes squash merges only, refuses force pushes and deletion,
and holds the merge until `lint`, `release-config`, and `test` on both platforms
have passed. Nobody bypasses it — there are no bypass actors, so a direct push to
`main` is rejected whoever makes it.

**The required checks are matched by job name**, so **renaming a job, or dropping
one from the matrix, leaves the rule waiting on a check that never arrives and a
pull request that can never merge**. Nothing fails at the moment of the rename; it
surfaces later, on a pull request that has done nothing wrong. Change `ci.yaml` and
the ruleset together:

```sh
# what merging currently waits for
gh api repos/jay-snyder/treewright/rulesets/19776162 \
  --jq '.rules[] | select(.type == "required_status_checks")
        | .parameters.required_status_checks[].context'

# what the last run actually reported, which is what those must match
gh run list --limit 1 --json databaseId --jq '.[0].databaseId' \
  | xargs -I{} gh run view {} --json jobs --jq '.jobs[].name'
```

`strict_required_status_checks_policy` is on, so a branch also has to be up to date
with `main` before it can merge.

Tests drive `git`, `tmux`, and each shell the shims target. Missing one is a skip
locally and a **failure under `CI`** — see `internal/testenv`, and note that this
makes the workflow's install steps load-bearing: a skipped tmux test in CI would
mean the whole integration silently left the run. A full local run wants `git`,
`tmux`, `zsh`, `bash` and `fish` installed.

Releases are tag-driven: push `v*`, GoReleaser builds the binaries, publishes a
Homebrew cask to `jay-snyder/homebrew-tap`, and stamps `main.version` via
ldflags. Validate config changes with `goreleaser check`.

## Layout

| Path | Owns |
|---|---|
| `main.go` | The only place errors become exit codes. Nothing else calls `os.Exit`. |
| `internal/cli/cli.go` | `Env`, the command table, dispatch, help rendering. |
| `internal/cli/commands.go` | `new`, `rm`, `ls`, `prune`, `resume`, `cd`, `base`, `attach`. |
| `internal/cli/move.go` | `move`: uncommitted work out of the base checkout and into a worktree. |
| `internal/cli/send.go` | `send`: one line typed at the agent in an open window. |
| `internal/cli/close.go` | `close`: the tmux window on a worktree, gone worktree or not. |
| `internal/cli/prompt.go` | `{prompt}`, the two flags that fill it, and what `--prompt-file` builds. |
| `internal/cli/setup.go` | `setup` (config generation, `--refresh`) and `config`. |
| `internal/cli/refresh.go` | `refresh`: the one post-upgrade action. |
| `internal/cli/release.go` | Whether a newer treewright exists, and how this one was installed. |
| `internal/cli/doctor.go` | `doctor`: the four-way health check. |
| `internal/cli/session.go` | One session per repo; `openWindow`/`focusWindow`. |
| `internal/cli/render.go` | Tables, JSON, `parseArgs`, slug resolution. |
| `internal/cli/message.go` | How a message is shaped on the way out: `progressf`/`warnf`/`errorf`, the continuation indent, `asLines`, `under`, `count`. |
| `internal/cli/popup.go` | `popup`, popup sizing, the no-worktrees message. |
| `internal/cli/signal.go` | `signal`: the agent-state protocol's one verb, run by agent hooks. |
| `internal/cli/guard.go` | `guard`: the PreToolUse decision, run by agent hooks — which tool calls may change another worktree, and the shell reader that answers it. |
| `internal/cli/eval.go` | The eval-file protocol and shell quoting. |
| `internal/cli/init.go` | `shell-init`, `tmux-init`, `agent-init`, `__complete`. |
| `internal/cli/version.go` | What `version` reports: the ldflags stamp, else the build info — and `--check`. |
| `internal/config` | TOML loading, defaults, and which config applies. |
| `internal/refname` | git's branch-name rules, restated for slugs and prefixes. |
| `internal/git` | Every git call, including merged/unpushed/dirty logic. |
| `internal/tmux` | Every tmux call, window identity, popups. |
| `internal/ui` | Picker, table, color. |
| `internal/shellinit` | zsh/bash/fish shims, checked in under `scripts/` and embedded by named file. |
| `internal/tmuxinit` | tmux key bindings and titles, as Go string constants. |
| `internal/agentinit` | Facts about coding agents, one module per agent: launch/resume defaults, local-state carries, and the plugin `agent-init` installs — checked in under `plugins/<agent>/`, embedded file by named file. |
| `internal/gittest` | Scratch-repo builder for tests (bare origin + checkout). |
| `internal/testenv` | Whether a missing tool is a skip or, under `CI`, a failure. |

Package doc comments carry the design rationale — `internal/tmux`,
`internal/shellinit`, and `internal/config` are worth reading before changing
anything in them.

## How a command runs

`main` reads `TREEWRIGHT_ARGV0` and calls `cli.Run(cli.Env{...})` → dispatch
looks the name up in the `commands` table → the command calls `resolveConfig("")`
(explicit name wins, else the repo you are standing in) → talks to
`internal/git` and `internal/tmux` → returns an error or nil. `main` translates:
`ErrUsage` → exit 2, `ErrSilent` → exit 1 silently, `ErrRefused` → exit 2
silently, any other error → printed as `error: ...` then exit 1. `ErrRefused`
shares a code with `ErrUsage` because the code was chosen by somebody else: a
PreToolUse hook blocks on 2 and on nothing else. Nothing reads the two apart —
one is answered to a shell and the other to an agent hook.

## Invariants that are easy to break

**Output contract.** stdout carries the answer and nothing else — paths, tables,
JSON, generated scripts. Progress, warnings, prompts, and errors go to stderr,
prefixed `warning:` / `error:` or unprefixed for narration. `cd "$(tw new x)"`
and `tw ls --json | jq` must both stay clean. Enforced by
`TestStdoutCarriesOnlyTheAnswer`.

**Errors, never exits.** Subcommands return errors; only `main` chooses an exit
code. That is what makes every command testable through `Run`.

**No globals for I/O.** Streams, args, and the eval file arrive on `Env`. Tests
point them at buffers — `Env.Stdin` included, which only `guard` reads, and
which is on `Env` rather than taken from `os.Stdin` for exactly that reason.

**Argv0 vs. the canonical name.** Anything the user is told to *type* uses
`env.Argv0` (`tw`, usually). Anything destined for a *file* a program reads —
tmux.conf lines, shell startup evals, help prose — spells out `treewright`.

**Everything works without the shell integration.** A shell command may never
run — no integration loaded, or an eval file that cannot be written — so the
by-hand line and the failure report belong beside the emit, which is what
`moveShell` in `eval.go` owns. Don't call `appendEval` directly from a command.

**tmux session targets are exact.** tmux matches session names as prefixes, so
every session target goes through `exact()` → `=name`. Window targets are window
ids (`@3`), which are server-unique.

**Window identity comes from the `@treewright_worktree` option**, not from a
pane's current directory — panes wander. See `Window.beats` in `tmux.go` for the
resolution order, and note that the option is kept on `Window` as the path it is
rather than as a bool: "treewright opened this window" and "treewright opened this
window *here*" are different questions, and the second is the one `openWindow`
asks.

**The pane treewright is typed into is not a window to switch to** —
`isTheCallersOwnShell` in `session.go`. A window treewright opened on the
directory answers for it however it is reached, but a shell that merely stands
there is where the user already is, and "switching" to it is a no-op dressed up
as an action. That is how `tw base`, typed in the main checkout from a session of
the user's own, came to warn about switching to a session that did not exist and
then do nothing at all — no session, no window, no agent.

**Branches always fork from `origin/<base_branch>`.** No flag overrides this;
offline falls back to the local base branch and says so.

**The branch prefix lives in the branch, never in the slug.**
`config.SplitPrefix` turns what the user typed into a prefix plus a slug — that is
how `branch_prefixes` picks between `feature/` and `bug/` — and the slug alone
names the directory, the window, and everything typed back at `resume`/`rm`.
`cmdNew` is the *only* place a branch name is constructed; everywhere else reads
it from `git.Worktree.Branch`, which is what lets a prefix be renamed without
orphaning the worktrees created under the old one. Never rebuild a branch name
from a slug.

**The config is data, never code.** TOML, unknown keys rejected, no shelling out
to read it. `branch_prefix` and `branch_prefixes` are two spellings of one
setting: read them through `Prefixes()`, and a file setting both is a load error
rather than a precedence rule. `post_create` deliberately does *not* follow that
pattern — it is one key taking either a string or a list, via
`config.Commands.UnmarshalTOML`, so there is no second key to set as well. Don't
"make it consistent" by adding one.

**An empty `command` is a setting too, and the agent key is its one exception.**
`command = ""` opens the window on a shell — `tmux.Spec` has always honored a
blank that way, and it is the only way a repo asks for a window with no agent in
it — so `Load` defaults both command keys on `!Explicit(...)` as it does
`ticket_pattern`. The exception is `agent`, which fills a blank command however
the blank got there: `agent = "claude"` plus `command = ""` runs claude, and a
repo that wants the shell drops the agent key as well. That fill is *recorded*
(`Config.AgentFilled`), because once it has happened an explicit blank and an
absent key are the same value — and `setup --refresh` has to tell them apart or
it writes the module's own command back as a setting that stops following it.

**An empty `ticket_pattern` is a setting, not a missing one.** `Load` defaults it
on `!Explicit("ticket_pattern")` rather than on `== ""`, because `""` is how a
repository that tracks no tickets turns the search off — collapsing that back to
the usual `if x == "" { x = default }` removes the only way to opt out, and no
test of the value alone can tell the two apart. `WindowName` then falls to
`shorten`, which is the name for every worktree in such a repository: the same
ten-column cap a ticket key gets, counted in runes, never leaving a hyphen
against the `…`, and never returning something as wide as what it replaced. See
"Naming a window, with or without a ticket" in `docs/design-notes.md`.

**`{prompt}` is resolved by `fillPrompt`, everywhere.** Every consumer of
`command` and `resume_command` fills the template before using it — `base`
too, with an empty prompt — or the literal placeholder leaks into a shell
line. No prompt removes the placeholder entirely, never substitutes `''` (an
empty argument is an instruction to most agents), and a prompt aimed at a
template without the placeholder is refused *before anything is created*.
`openWindow` reports created-vs-found so callers can warn when a prompt landed
on a window that was already open and its command never ran.

**treewright writes its own registry, its own plugin directory, and nothing
else.** The whole list is `<config dir>/<name>.toml`,
`.git/treewright/post-create-*`, `.git/treewright/no-agent-yet-*` and
`.git/treewright/move-*.patch` inside the
repo, the worktrees, and `.claude/skills/treewright/` —
`~/.claude/skills/treewright/` only when `agent-init --global` names it.
Everything else — the shell line, the tmux line, any `.gitignore` entry — is
*printed for a person to place*. `tmux-init --apply` is not an exception: it
sets bindings on a running server and writes no file.

The test is not "does it write" but **whose file is it**. A new feature that
wants to edit a dotfile, a settings file or a `.gitignore` must print instead;
a directory named after treewright, holding nothing a person put there, is
treewright's to rewrite. There is no uninstall script, so every path on that
list has to be one a developer can find and delete the day they stop using
treewright. See "What treewright is allowed to write" in
`docs/design-notes.md`.

**Anything that is a fact about a particular agent lives in `internal/agentinit`.**
Core stays agent-agnostic: it provides protocols (`signal`, `command`, the
carry), and a module provides the wiring. `agent = "claude"` is a defaults
bundle — explicit `command`/`resume_command` override it field-by-field (unlike
the branch_prefix pair, setting both is not an error), and the module's
local-state files are carried into new worktrees *silently when absent*, which
is the one way `AgentCarries()` differs from `carry_files`. The module is never
inferred from `command`'s first word for behavior; only doctor's warn-level
wiring check may sniff it. See "Agent modules" in `docs/agents.md`.

**A module's per-project artifacts are carried, and the list is derived.**
`Agent.LocalState()` is computed from `ProjectSettings` and the plugin's own
files rather than written out beside them, so a module cannot name a
per-project file it forgets to carry — that omission puts the file in the main
checkout and in no worktree, which is the trap `agent = "claude"` exists to
close. It names each plugin file rather than the directory, because `carryOne`
copies files and skips directories, and a `carry_files` entry naming a
directory has no meaning for the warn-when-missing rule. Per-repo is the
placement `agent-init` leads with, because treewright is a tool you use in some
repositories and not others; `--global` is the second option it names, never the
first. treewright writes to no `.gitignore` and generates none.

**What ships as a file is checked in as a file, and named in Go one at a
time.** Two directories work this way — `internal/agentinit/plugins/<agent>/`
and `internal/shellinit/scripts/` — so the bytes a contributor reads and lints
are the bytes that get written, shell as shell and JSON as JSON. Each file
reaches the binary through a `//go:embed` naming *it*, never a pattern that
walks the folder. **Nothing ships because it was sitting in the directory.**
Both of these run: a plugin directory also honors `bin/` (executables on the
Bash tool's PATH) and `.mcp.json` (servers to start), and a shim is `eval`'d
into the user's interactive shell at every start — so a file added there is
code on someone's machine, and "a file was added" is the easiest thing to miss
in a diff. `TestThePluginShipsOnlyWhatItDeclares` and
`TestEveryScriptIsDeclared` close the other direction, failing on a file that
is checked in and claimed by nobody; they stay separate because what each is
really made of is the sentence it fails with, which names a different fix.

Nothing in Go touches a byte of those files, including the driving guide: each
module owns its whole skill, because a skill is written for a particular
reader, and what holds the copies to the CLI is
`TestThePluginTeachesTheCLIThatExists` rather than a shared file.

**Branch-name rules live in `internal/refname`, not in the code that uses them.**
`CheckSlug` runs in `new`, `CheckPrefix` runs in `config.Load`, and both are
restatements of `git check-ref-format` so a bad name is one sentence naming it
rather than git's ref-syntax advice three steps later.
`TestCheckPrefixAgreesWithGit` runs the real binary against every case — a rule
added on one side and not the other fails there. Two divergences are deliberate
and marked `stricter`: a slug may not contain `/`, and neither may start with `-`.

**A window's command is wrapped; every message about it is not.** `openWindow`
hands tmux `heldOpenOnFailure(command)`, which holds a failing window open so its
output can be read, and keeps the plain string the caller passed for the prose that
names it. Both it and `postCreateScript` run the user's command in a subshell so an
`exit` of its own does not end the wrapper — the case that erases the output.

**The wrapper's size does not grow with the command's.** The line reporting what
exited names the command through `abbreviated` — first line, eighty columns —
rather than carrying a second copy, which was shell-quoted on top of the quoting
`fillPrompt` had already applied and so cost sixteen bytes per apostrophe. That
copy spent tmux's own command-length budget twice; `tmux.MaxCommandLength` is
what is left of it, and `checkCommandFits` runs inside `fillPrompt` so an
over-long `--prompt` is refused **before the worktree exists** rather than
arriving as tmux's raw `command too long` over a worktree with no window.

**`resume` runs `command`, not `resume_command`, where no agent has ever run.**
`new` writes `.git/treewright/no-agent-yet-<slug>` as it makes a worktree and
`clearNoAgentYet` removes it once a window has actually run the command, so a
worktree whose window never opened stays reachable instead of meeting
`claude --continue` with no conversation to continue. **The marker is the
negative on purpose**: absence has to keep meaning "as before", or every worktree
made by an older treewright would be met by a fresh agent in place of the session
it had. It records that an *agent started*, not that a *window opened* — the two
differ exactly in the case it exists for — and `base` is deliberately outside it.
See "When there is nothing to resume" in `docs/agents.md`.

**`move` touches the base checkout last, and only after the work has been seen
somewhere else.** The base checkout is the only copy of uncommitted work until
the patch has landed, so the order is the safety: record the untracked files,
`add -N` them, write the patch, put the index back *immediately* — by path, so
the user's own staging survives — make the worktree, apply with `--3way`, check
with `git diff HEAD --stat`, and only then clear. `git diff` is the wrong check
because `--3way` staged everything, so it has nothing to show and reads exactly
like a patch that never applied: a gate that opens on failure. What gets deleted
is `--diff-filter=A`'s list, which includes a file the user had already staged;
`git clean -fd` is what an improvising hand reaches for and it takes unrelated
files and ignored ones inside untracked directories. `git stash` is not
available to this — one stash stack is shared by every worktree of a repository.
See "Moving work that was started in the wrong place" in `docs/design-notes.md`.

**treewright never prints a tmux command for someone to run.** `rm`, `prune` and
`send` name `treewright close <slug>`; the one command still spelled as tmux is
`attach`'s hint for a session in some *other* repository, and that goes through
`tmux.AttachArgs` so it carries the server flags. A printed
`tmux kill-window -t @3` is run from a shell holding none of treewright's
environment, so under `TREEWRIGHT_TMUX_LABEL` it reached the default server,
closed whatever `@3` was there and exited 0 — a wrong session name fails loudly,
a wrong server does not.

**`close` finds a window whose worktree is gone**, which is most of why it
exists: the lookup is `Windows()[cfg.DirFor(slug)]`, and `@treewright_worktree`
still holds that path after the directory is deleted. The path is computed from
the slug rather than resolved among the worktrees for the same reason — a prefix
resolves only while there is something to match against. Everything it reports
is reported *before* the window goes: closing the caller's own window kills the
pane treewright runs in, and closing a session's last window can detach whoever
would have read it.

**A window whose agent is `working` is warned about wherever one is closed** —
`close`, `rm --yes`, and above `rm`/`prune`'s own prompt — through
`warnIfAgentWorking`. The agent is the window's command, so closing the window
stops the work and takes the session with it, and that is the one thing about a
window treewright knows and the caller may not. It warns rather than refuses: a
refusal would need a `--force`, which is a flag people learn to pass by reflex.
Only `working` warns; `waiting` and `done` are the states an ordinary teardown
closes, and a warning that fires on the ordinary case stops being read.

**No tmux call lives outside `internal/tmux`, including the ones `send` makes.**
`Send` is `send-keys -l -- <text>` then `send-keys Enter`, two calls because tmux
reads its arguments as key names otherwise, and `Capture` is what puts "look
before you type" in the transcript rather than in a rule. `send` refuses a
message with a newline (Enter submits, so the rest would post as further turns),
the caller's own window, and a window held open after its command died —
recognized from `heldOpenNotice` being the capture's last line, since the
wrapper clears the agent state and so it cannot be told from a window that never
signaled. Nothing there writes `@treewright_agent_state`: the receiving agent's
own hook does.

**A background failure needs somewhere to be reported.** Nothing waits for
post_create, so a failing step leaves a marker beside its log and
`warnIfSetupFailed` reports it from `ls`, `cd` and `resume`. A new mechanism that
runs unattended needs the same, or it fails silently.

**`signal` is silent out of scope — deliberately outside the rule above.** Agent
hooks run it in every session the agent has, so outside tmux, outside a
registered repo, or with no window it exits 0 and prints nothing; a hook that
warns is an integration that nags. The state it writes lives on the window
option `@treewright_agent_state`, never on disk — the agent is the window's
command, so agent death is state death — and the `!` waiting marker is display
only: `tmux.Windows` strips it at parse, so `Window.Name` is always the clean
name. The held-open wrapper is the one place a window outlives its agent, and it
clears both itself. See "Agent state" in `docs/agents.md`.

**`guard` is `signal`'s discipline with a sharper reason, and it is the one
command that never returns a usage error.** It reads a PreToolUse payload on
stdin and refuses a tool call that would mutate a worktree other than the one
the calling agent stands in — the handoff rule made mechanical, after five
revisions of the prose failed to hold it. Everything out of scope exits 0 in
silence, as `signal` does, and the reason is stronger here: `signal` nags, where
this one *blocks*, and a refusal fired where treewright has no business is work
somebody has to argue their agent past. A PreToolUse hook blocks on exit 2 and
on nothing else, which is why the refusal is `ErrRefused` — and why being
invoked wrong is out of scope rather than an `ErrUsage`, that being exit 2 as
well: a mis-wired hook would otherwise refuse every tool call in the session and
hand back this command's help as the reason.

**Reads of another worktree are never refused, and neither is treewright
itself.** Reviewing another agent's work is the ordinary way to check on it, and
the two commands the refusal names — `resume --prompt-file` and `send` — are
full of the worktree path it just refused. The read/write split is a closed list
of read-only programs and git subcommands with everything else treated as a
write, which is only conservative in appearance: nothing consults it until a
command has already been found reaching into somebody else's worktree. The
plugin's PreToolUse matcher and `guardedTools` must name the same tools, which
`TestTheGuardAndItsMatcherAgree` holds. See "Enforcing the handoff" in
`docs/agents.md`.

**An integration that propagates an upgrade must be able to say which
treewright it came from.** The shim, the tmux snippet and the plugin all follow
an upgrade by being re-read — but a shell, a tmux server and a worktree's copy
all keep what they last loaded, for days or weeks, and that is invisible. So the
shim exports `TREEWRIGHT_SHELL_INIT_VERSION`, the snippet ends with `set -g
@treewright_tmux_init`, and the plugin is byte-compared in every checkout rather
than only in the main one. A fourth integration added later needs the same, or it
goes stale with nothing to say so. **Fingerprints are digests of the checked-in
text, never the release version** — a `dev` build compared against a `dev` build
is no comparison — **and never include the user's keys**, which are theirs to
move. `tmuxinit.Version` and `shellinit.Version` each hold the four lines that
do it; they are duplicated on purpose, a package existing to share four lines
being the worse trade.

**`refresh` refreshes what is installed and installs nothing new.** It rewrites
plugin copies that exist, gives one to a worktree only where the config carries
the plugin, reloads tmux bindings only into a server already holding some, and
puts them back on the keys they are already on. `agent-init` and `tmux-init` are
where a user decides what treewright touches; `refresh` is the command people run
without reading it, and it must never be how that decision gets made for them.
The shell wrapper is the one thing it reports rather than fixes — no process can
define a function in its parent.

**The release check is explicit-only.** `doctor` and `version --check`, and
nothing else — no cache file, no background check, no HTTP on `new` or `ls`. An
unreachable API is a silent skip in `doctor` (an offline laptop is not a fault)
and a spoken one in `version --check` (it was asked for). A build reporting no
release version says so instead of guessing, and that test runs *before* any
request, which is also what keeps the suite off the network — see
`stubReleaseAPI` in `upgrade_test.go` for the tests that do exercise it.

**The config's `version` key counts generator revisions, not releases**
(`config.FormatVersion`). A file without one is an old config and never an error:
every config in the wild predates the key. `setup --refresh` is the only thing
that rewrites one, and it **re-detects nothing** — every value comes back out of
the file, read through `Explicit` so a default never gets written back as a
setting. Adding a key the generator writes means adding it to `configSettings`
*and* to `settingsFrom`, or `--refresh` silently drops it.

**Read-only-looking commands still write to `.git`.** Squash-merge detection
synthesizes a dangling commit object (`IsMerged`), with fixed author/committer
env so the hash is deterministic and repeat runs reuse the object.

**Popup size is estimated, not measured** (`popupSize` in `render.go`). It
mirrors `worktreeTable`'s layout; `TestPopupSizeCoversTheTable` renders a real
table to check the estimate still covers it. Change one, check the other.

**A key binding must hand `popup` the pane's directory.** `run-shell` runs in the
tmux server's working directory, not the calling pane's, so a binding without
`-d "#{pane_current_path}"` makes every popup answer about whichever repository
the server was started from. One registered config hides it, because
`config.Resolve` falls back to the only one. Guarded by
`TestPopupAnswersAboutTheDirectoryItIsGiven` and by the two `pane_current_path`
assertions in `tmuxinit_test.go`.

## Adding a subcommand

1. Add an entry to `commands` in `cli.go` — name, args, summary, `long`, flags,
   `run`. Aliases are accepted but deliberately absent from help and completion.
2. Implement it in `commands.go` (or its own file if substantial), parsing with
   `parseArgs` and returning `usageErrorf` for bad invocations.
3. Add completion: the candidate lists live in `cmdComplete` (`init.go`), and the
   command name goes in all three shims under `internal/shellinit/scripts/`
   (zsh's `cmds`, bash's `compgen -W`, and fish's `complete` lines) plus any
   per-flag fish completions.
4. Add a row to the output-contract table in `docs/design-notes.md` if it prints
   to stdout. `README.md` has no command reference to update — `tw help` is the
   list, and the README names a command only where it is part of the tour. Add
   it there if a reader who has not gone looking would otherwise never learn the
   command exists.

`TestEveryFlagIsDocumented` will fail if a flag is in help but not the parser or
completion; `TestHelpTextIsCleanlyIndented` will fail on stray whitespace in the
raw-string help prose.

## Conventions

**Comments explain why, not what.** The prevailing style is prose paragraphs
above a function or a tricky block, explaining the alternative that was tried and
why it lost. Match the density of the surrounding file — this codebase is heavily
commented on purpose, and a bare implementation will look out of place.

**Message voice** (enforced by `TestMessagesShareOneVoice`): lowercase first
word, no trailing period, em dashes rather than `->`, no `treewright:` or `note:`
prefixes. Errors say what to do next and name it in the user's own vocabulary
(`open it with "tw resume eng-1"`).

**A message is one message, however many lines it takes**, and it is written in
the terminal's register rather than the docs'. Front-load the subject, say one
thing per line, and put the rest in labelled fields — not one sentence with each
clause hung off another em dash. A trailing appositive reads well in a paragraph
and is a thing to parse in a terminal, where nobody is reading: they are looking
for one fact. The order that works is **what is wrong, what it costs, then what
to type**, so the copyable part is last and nobody reads past it twice.

Put a `\n` in the format string and the writers in `message.go` indent every
line after the first: to the prefix's own width for `warnf`/`errorf`, so all the
text sits in one column, and to a two-column hanging indent for `progressf`,
which has no prefix to align under. **Never spell that indent at the call
site** — a hand-typed run of spaces in `rm` was the whole of the old mechanism,
and nothing else could match it. Never split one thought across two `progressf`
calls either: the second starts a new block at the margin and reads as a new
subject.

The voice rules apply to every line, not just the first: a continuation starts
lowercase and ends without a period. `TestEveryLineOfAMessageKeepsTheVoice`
checks that of the writers, `TestMessagesShareOneVoice` of what commands
actually print. Four helpers cover the shapes that recur — `asFields` for the
labelled what/where/how, `asLines` for a list that would otherwise be
comma-joined off the side of the terminal, `under` for a caveat a message
carries only sometimes, and `count` for the number-plus-noun that used to read
`1 file(s)`.

**Color marks two spans and decorates nothing else**: the severity, through
`warnf`/`errorf`, and the part meant to be typed, through `env.copyable`. It is
off down a pipe, off under `NO_COLOR` and off on a dumb terminal — so a message
must carry its whole meaning in the text, and what color buys is only the eye
landing in the right place first.

`ui.Table` renders a cell holding newlines as a row that spans lines, keeping
each in its column. That is how a `doctor` finding says what to do about itself
underneath itself, and how `config` lists `carry_files`. The picker must never
be handed one: it numbers each row, so a second line would arrive unnumbered and
flat against the margin. A marker column goes **before** the ragged one, not
after — `config`'s `FROM` sits between `SETTING` and `VALUE` because a last
column is the only one never padded, and markers after a column of absolute
paths end up fifty columns from what they mark.

**`doctor` is grouped**: `installation`, then a section per config, then a count.
`report.in` opens a section and `addf` takes the check name as its own column, so
nothing repeats `proj: ` down the left. Tests read it back through `findings`,
which flattens a finding to `"<group>: <check> <detail>"` — assert on that, not
on the layout, and use `flat()` in the other tests when a message's column widths
are not the subject.

**Test names read as sentences** about behavior: `TestNewStripsAnAlreadyPrefixedSlug`,
`TestTheWorktreesWindowIsFoundHoweverWindowsAreArranged`. Tests carry doc
comments when the reason for the test is not obvious from the name.

**Commit messages**: subject is a sentence describing the behavior change from
the user's side, imperative and unprefixed ("Offer the base checkout in a
repository with no worktrees yet"). The body explains the why at length —
what was wrong, what was tried, what the tradeoff is. Non-user-facing work takes
a `ci:`/`docs:`/`chore:` prefix (GoReleaser filters those out of the changelog).
Commits are co-authored with the trailer.

## Tests

`newFixture(t, extraConfig)` in `internal/cli/cli_test.go` is the workhorse: a
real git repo with a bare origin, a config registry pointing at it, an inert
`claude` stub first on `PATH`, and a **private tmux server** (own socket dir, own
`-L` label, killed on cleanup) so tests never touch your own sessions. Never call
bare `tmux` in a test — go through the package, which honors
`TREEWRIGHT_TMUX_LABEL`.

Fixture helpers: `exec` (streams kept apart), `run`/`mustRun` (combined),
`runWithEvalFile` (returns what treewright asked the shell to run), `statusOf`.
`gittest` adds `Worktree`, `Commit`, `Push`, `SquashMerge`, `Symlink`,
`LooseObjects`.

The shims are checked by the shells themselves (`TestScriptsParse`), and the tmux
snippet is loaded into a real server and read back — a snippet that does not
parse would break the startup of whatever loads it.

## Environment variables

| Variable | Meaning |
|---|---|
| `TREEWRIGHT_CONFIG_DIR` | Registry directory; overrides `$XDG_CONFIG_HOME/treewright/repos`. |
| `TREEWRIGHT_EVAL_FILE` | Set by the shell wrapper; commands append shell lines for it to source. |
| `TREEWRIGHT_SHELL_INIT_VERSION` | Exported by the shell wrapper: the fingerprint of the shim that defined it, which is how `doctor` tells a wrapper this binary emitted from one an older build did. |
| `TREEWRIGHT_ARGV0` | The name the user typed (`tw`), since the wrapper erases it from argv[0]. |
| `TREEWRIGHT_TMUX_LABEL` | Drive a non-default tmux server (`tmux -L <label>`). |
| `TREEWRIGHT_POPUP` | Set inside a popup, so exit paths can say "press Esc to close". |
| `NO_COLOR`, `TERM=dumb` | Turn color off; color is also off whenever stdout is not a terminal. |
