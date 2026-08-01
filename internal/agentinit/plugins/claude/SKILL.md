---
name: treewright
description: Manage parallel work in repositories that use treewright (tw) — a git worktree, tmux window, and agent session per piece of work. Use when starting a task in parallel, spawning another agent on one, checking which worktrees and agents are in flight or need attention, resuming earlier work, or cleaning up merged branches. Use instead of raw git worktree in a treewright-managed repository.
---

# Driving treewright

treewright gives each piece of work its own git worktree, tmux window, and
agent session, created and torn down together. In a repository it manages,
reach for it before `git worktree`, `git branch`, or `git checkout -b`:
it also copies gitignored env files into the new checkout, runs the configured
install step, names the tmux window after the work, and guards teardown.

Run it as `treewright`. The short `tw` is a shell function
from the interactive shell's startup file, and may not exist in the shell
running your commands.

## See what is in flight

    treewright ls --json

One JSON object per checkout:

- `"base": true` marks the main checkout. It is not a worktree,
  never a target for rm or prune, and new work should fork from it rather than
  happen in it.
- `status`: `dirty` and `unpushed` mean work
  that exists nowhere else; `merged` has landed and is safe to
  remove; `active` is pushed and unmerged — an open pull request.
- `agent_state`: what the agent in that window last reported —
  `working`, `waiting` (blocked on a person), or `done`.
  Empty when nothing has signaled.
- `ahead`/`behind` measure against origin's base branch, and
  `null` means the comparison was impossible — unknown, not zero.
- The listing does not fetch, so a branch merged since the last fetch still
  reads active; rm and prune fetch before they judge, and they are the ones to
  trust.

## Start a piece of work

    treewright new eng-142-null-user --prompt "the instructions for that agent"

Forks a branch from the latest origin base branch, makes the worktree, copies
the env files in, starts the install step in the background, and opens a tmux
window whose agent begins on the prompt. stdout is the new worktree's path and
nothing else.

- The slug may not contain "/". A leading "feature/" or "bug/" chooses a
  configured branch prefix — `treewright new bug/eng-142` — and one
  the repository has not configured is refused rather than guessed at.
- A branch that already exists — a colleague's pull request after fetching —
  is checked out rather than recreated, so this is also how work is picked up.

## Continue or hand work onward

    treewright resume eng-142 --prompt "address the review comments"

An unambiguous prefix of a slug is enough, and the expansion is reported. The
prompt reaches the agent only when the resume actually starts one: a window
that was already open is switched to instead, with a warning that the prompt
went undelivered.

## Clean up

    treewright rm eng-142
    treewright prune --yes

rm refuses a worktree with uncommitted changes or commits on no origin ref;
prune only takes worktrees that are both merged and clean. The refusals mean
work that exists nowhere else: do not pass --force on your own judgment —
surface the refusal and let the person decide. With no tty, window-closing
prompts are skipped and treewright prints the command to run instead.

## Leave to the machinery

- `treewright signal` is run by the agent's own hooks already; do
  not call it by hand.
- `treewright setup`, `shell-init`, `tmux-init`, and `agent-init`
  change a person's configuration; run them only when asked to.
