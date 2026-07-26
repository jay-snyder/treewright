# treewright — orientation for Claude

A Go CLI that gives each piece of work its own git worktree, tmux window, and
agent session, created and torn down together. No runtime dependencies beyond
the `git` and `tmux` binaries; two Go modules (`BurntSushi/toml`, `x/term`).

This file is the map of the code and the rules that are easy to break. Two
companions:

- **`README.md`** — the user-facing tour: pitch, install, quickstart, command and
  config reference. Deliberately short and non-technical. When user-visible
  behavior changes, it changes with it — but keep new rationale out of it.
- **`docs/design-notes.md`** — why the behavior is what it is: session per repo,
  window identity, popup sizing, the base checkout, squash-merge detection, the
  full output contract, safety rules, the eval-file protocol. Read the relevant
  section before changing behavior in that area; add to it when you decide
  something a future reader would otherwise re-litigate.

## Build, test, lint

```sh
go build -o treewright .   # /treewright is gitignored; version stays "dev"
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
returns, `//nolint:usetesting` on the three tmux socket directories, and errcheck's
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
| `internal/cli/setup.go` | `setup` (config generation) and `config`. |
| `internal/cli/doctor.go` | `doctor`: the four-way health check. |
| `internal/cli/session.go` | One session per repo; `openWindow`/`focusWindow`. |
| `internal/cli/render.go` | Tables, JSON, `parseArgs`, slug resolution. |
| `internal/cli/popup.go` | `popup`, popup sizing, the no-worktrees message. |
| `internal/cli/eval.go` | The eval-file protocol and shell quoting. |
| `internal/cli/init.go` | `shell-init`, `tmux-init`, `__complete`. |
| `internal/config` | TOML loading, defaults, and which config applies. |
| `internal/refname` | git's branch-name rules, restated for slugs and prefixes. |
| `internal/git` | Every git call, including merged/unpushed/dirty logic. |
| `internal/tmux` | Every tmux call, window identity, popups. |
| `internal/ui` | Picker, table, color. |
| `internal/shellinit` | zsh/bash/fish shims, as Go string constants. |
| `internal/tmuxinit` | tmux key bindings and titles, as Go string constants. |
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
`ErrUsage` → exit 2, `ErrSilent` → exit 1 silently, any other error → printed as
`error: ...` then exit 1.

## Invariants that are easy to break

**Output contract.** stdout carries the answer and nothing else — paths, tables,
JSON, generated scripts. Progress, warnings, prompts, and errors go to stderr,
prefixed `warning:` / `error:` or unprefixed for narration. `cd "$(tw new x)"`
and `tw ls --json | jq` must both stay clean. Enforced by
`TestStdoutCarriesOnlyTheAnswer`.

**Errors, never exits.** Subcommands return errors; only `main` chooses an exit
code. That is what makes every command testable through `Run`.

**No globals for I/O.** Streams, args, and the eval file arrive on `Env`. Tests
point them at buffers.

**Argv0 vs. the canonical name.** Anything the user is told to *type* uses
`env.Argv0` (`tw`, usually). Anything destined for a *file* a program reads —
tmux.conf lines, shell startup evals, help prose — spells out `treewright`.

**Everything works without the shell integration.** `emitEval` is a no-op when
`TREEWRIGHT_EVAL_FILE` is unset, so every caller must also print what to run by
hand.

**tmux session targets are exact.** tmux matches session names as prefixes, so
every session target goes through `exact()` → `=name`. Window targets are window
ids (`@3`), which are server-unique.

**Window identity comes from the `@treewright_worktree` option**, not from a
pane's current directory — panes wander. See `claim.beats` in `tmux.go` for the
resolution order.

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

**A background failure needs somewhere to be reported.** Nothing waits for
post_create, so a failing step leaves a marker beside its log and
`warnIfSetupFailed` reports it from `ls`, `cd` and `resume`. A new mechanism that
runs unattended needs the same, or it fails silently.

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
   command name goes in all three shims in `shellinit.go` (zsh's `cmds`, bash's
   `compgen -W`, and fish's `complete` lines) plus any per-flag fish completions.
4. Add a row to the Commands table in `README.md`, and to the output-contract
   table in `docs/design-notes.md` if it prints to stdout.

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
(`— open it with "tw resume eng-1"`).

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
| `TREEWRIGHT_ARGV0` | The name the user typed (`tw`), since the wrapper erases it from argv[0]. |
| `TREEWRIGHT_TMUX_LABEL` | Drive a non-default tmux server (`tmux -L <label>`). |
| `TREEWRIGHT_POPUP` | Set inside a popup, so exit paths can say "press Esc to close". |
| `NO_COLOR`, `TERM=dumb` | Turn color off; color is also off whenever stdout is not a terminal. |
