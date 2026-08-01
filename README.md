# treewright

Give every ticket its own git worktree, tmux window, and agent session. One
command to start it, one to tear it down.

You're halfway through a bug fix when a review lands. So you stash, switch
branches, wait for `npm install`, lose your agent's context, do the review,
switch back, unstash, and spend five minutes remembering where you were.

Git worktrees fix that. You can have ten branches checked out in ten directories
at once. What you get in exchange is ten directories to keep track of, ten sets
of gitignored `.env` files to copy around, ten terminal tabs to keep straight,
and a cleanup chore you'll skip until your disk fills up.

treewright is the part git leaves out:

```sh
tw new eng-2318-cart-total-rounding
```

That gets you:

- a checkout at `~/code/storefront-eng-2318-cart-total-rounding`
- a branch `john/eng-2318-cart-total-rounding`, forked off the latest `origin/main`
- your gitignored `.env` files copied into it
- `npm install` already running in the background
- a tmux window called `ENG-2318` with your agent in it

Add `--prompt "the cart total rounds down at checkout"` and the agent in that
window starts on the ticket before you've even switched to it — `tw new` stops
being "prepare a desk" and becomes "assign the work".

Do that three times and `prefix + T` switches between all three from anywhere,
including from inside a running agent. When the PR merges, `tw rm eng-2318`
takes the whole thing away, and stops you if there's unpushed work in there.

## One session per repo

Every repo's windows live in a tmux session named after it, so your storefront's
windows never end up next to your payments service's:

```
tmux session "storefront"
├── MAIN      ~/code/storefront
├── ENG-2295  ~/code/storefront-eng-2295-flaky-payment-test
├── ENG-2318  ~/code/storefront-eng-2318-cart-total-rounding
└── ENG-2324  ~/code/storefront-eng-2324-apple-pay-retry

tmux session "checkout-api"
├── MAIN    ~/code/checkout-api
└── PAY-88  ~/code/checkout-api-pay-88-idempotency-keys
```

`MAIN` is your home base. It sits in the main checkout, parked on `main`. Start
new work from there, ask your general questions there, and keep feature work out
of it.

## Install

```sh
brew install jay-snyder/tap/treewright
```

You get two names for one binary: `treewright`, and `tw`, which is the one
you'll actually type. The cask pulls in git and tmux, and clears the quarantine
flag macOS puts on unsigned downloads.

The cask is macOS only. On Linux, grab a tarball from
[releases](https://github.com/jay-snyder/treewright/releases) or:

```sh
go install github.com/jay-snyder/treewright@latest
```

That puts the binary in `$(go env GOPATH)/bin`, which is not on your `PATH`
unless you have put it there. Check before going on:

```sh
command -v treewright
```

If that prints nothing, add the directory to your `PATH`. Both setup steps below
run `treewright` to get their output, and so does everything after them.

### Set up your shell

Add one line to your shell's startup file. It defines `tw`, sets up tab
completion, and lets `tw cd` move your shell:

```sh
# in ~/.zshrc
eval "$(treewright shell-init zsh)"

# in ~/.bashrc
eval "$(treewright shell-init bash)"

# in ~/.config/fish/config.fish
treewright shell-init fish | source
```

Open a new terminal afterwards, or re-source the file, and `tw` is there.

That line runs `treewright`, so an unreachable binary makes it print
`command not found` and define nothing — the error names your startup file,
not the `PATH` behind it. If you put the `go install` directory on your `PATH`
in a login-only file like `~/.profile`, `~/.zprofile` or `~/.bash_profile`, a new
terminal is not enough to pick it up: log out and back in.

### Set up tmux

Add one line to `~/.tmux.conf`:

```tmux
run-shell 'treewright tmux-init --apply'
```

That binds two keys:

| Key | What it does |
|---|---|
| `prefix + T` | Pick a worktree and jump to it |
| `prefix + N` | Type a slug, get a worktree |

You need them because a treewright window runs your agent as the window's
command. There's no shell in there to type into. The keys open a popup on top of
whatever's running, and close it again when you've picked.

`run-shell` looks up `treewright` in the tmux server's `PATH`, not your shell's,
and tmux says nothing when a line in your config fails. So if the keys do nothing,
make treewright reachable and then restart the server with `tmux kill-server` — a
running server keeps the `PATH` it started with, and a new window inherits it.

Both are unbound in stock tmux. If your config already uses them, choose your
own by adding flags to the same line:

```tmux
run-shell 'treewright tmux-init --apply --resume-key G --new-key C-n'
```

## Getting started

Go to the repo you want to work in and run two commands:

```sh
tw setup      # writes a config for this repo
tw doctor     # tells you if anything's missing
```

`setup` works out most of it for you: where your main checkout is, which branch
to fork from (it reads `origin/HEAD`), your branch prefixes — read off the
branches already on origin, or your git email if they say nothing — and
which gitignored `.env` files to carry into new worktrees. It prints every guess
and writes them to a commented TOML file, so fixing a bad one is a two-second
edit. Use `--dry-run` if you'd rather look before it writes anything.

After that it's four commands:

```sh
tw new eng-2318-cart-total-rounding   # worktree + branch + window
tw ls                                 # what's going on
tw resume eng-2318                    # back to that window from anywhere
tw rm eng-2318                        # PR merged, clean it up
```

## Seeing where everything stands

```
$ tw ls
   SLUG                          STATUS     AHEAD/BEHIND  WINDOW
*  main                          base       +0/-2         MAIN
   eng-2295-flaky-payment-test   merged     +1/-3         ENG-2295
   eng-2318-cart-total-rounding  dirty (1)  +0/-3         ENG-2318
   eng-2324-apple-pay-retry      active     +1/-3         ENG-2324
```

The `*` is where you're standing. `+1/-3` is how far ahead of and behind
`origin/main` that branch is, so the `-2` on the top row is your main checkout
going stale.

| Status | What it means |
|---|---|
| `dirty (n)` | `n` uncommitted files. Beats every other status, since it's the easiest thing to lose. |
| `merged` | Landed on `origin/main`. Safe to delete, and `tw prune` will. |
| `unpushed (n)` | `n` commits that exist nowhere else. `tw rm` won't touch it without `--force`. |
| `active` | Pushed, not merged. An open PR. `tw prune` leaves these alone. |
| `base` | Your main checkout. Not a worktree, never deleted. |

Squash merges count as merged, which sounds obvious but isn't: a squash merge
leaves none of your branch's commits upstream, so the usual check says your work
is unpushed and refuses to clean up. treewright rebuilds the patch and asks git
whether it already landed.

With three agents running at once, the question the table can't answer from git
alone is which one needs you. If your agent's hooks report what it's doing —
they run `tw signal` with `working`, `waiting`, or `done` — the table grows an
AGENT column saying exactly that, and a window whose agent is waiting on you
gets a `!` in front of its name in the tmux status line. Agents that report
nothing cost nothing: the column only exists once something has signaled.

Any agent that can run a command when its state changes can report this way,
and treewright prints the wiring for the ones it knows:

```sh
tw agent-init claude
```

That's the hooks for `~/.claude/settings.json`, with instructions alongside.
They're safe to keep global — outside a treewright window, `signal` does
nothing, quietly — or scope them to one repo by putting them in the main
checkout's `.claude/settings.local.json` and setting `agent = "claude"` in the
config, which carries that file into every new worktree.

The same command teaches the agent the other direction. `tw agent-init claude
--skill` prints a skill that shows Claude how to drive treewright itself —
list what's in flight, spawn a sibling worktree with a prompt, respect the
teardown guards — so you can ask the agent in your MAIN window to farm three
tickets out to three worktrees and it knows exactly how.

Hit `prefix + T` and that same table becomes a menu, in a popup sized to fit it:

```
      SLUG                          STATUS     AHEAD/BEHIND  WINDOW
1) *  main                          base       +0/-2         MAIN
2)    eng-2295-flaky-payment-test   merged     +1/-3         ENG-2295
3)    eng-2318-cart-total-rounding  dirty (1)  +0/-3         ENG-2318
4)    eng-2324-apple-pay-retry      active     +1/-3         ENG-2324

select 1-4 (Esc to cancel):
```

One keypress, no Enter. Your main checkout is always row 1, so home base doesn't
move around on you.

## Commands

| Command | What it does |
|---|---|
| `tw new <slug> [window-name]` | Fork a branch off the latest `origin/<base_branch>`, make the worktree, open a window on it. `--prompt` hands the agent its first instruction |
| `tw resume [slug]` | Go back to a worktree's window, or open it again — `--prompt` hands the agent its next instruction. Shows the menu if you don't name one |
| `tw cd [slug]` | Move your shell into a worktree |
| `tw base [repo]` | Go to the main checkout's window |
| `tw attach [repo]` | Attach this terminal to a repo's tmux session |
| `tw ls [--json] [repo]` | The table above. Touches nothing |
| `tw rm [-f] [-y] <slug>` | Delete the worktree, branch, stale remote ref, and window |
| `tw prune [-y] [repo]` | Delete every merged, clean worktree. Lists them first unless you pass `--yes` |
| `tw setup [-n] [name]` | Register the repo you're standing in |
| `tw config [repo]` | Show the settings actually in force, defaults and all |
| `tw doctor` | Check your install and every config you've registered |
| `tw shell-init <shell>` | Print the shell integration for zsh, bash, or fish |
| `tw tmux-init [--apply]` | Print the tmux integration, or load it straight into the server |
| `tw agent-init <agent>` | Print the hooks that make an agent report its state |

`tw help <command>` has the details on any of them. There's also `tw popup`,
which is what the key bindings run, and `tw signal`, which is what agent hooks
run to fill the AGENT column; you won't type either yourself.

You don't have to spell slugs out. `tw cd eng-2318` finds
`eng-2318-cart-total-rounding` and tells you that's what it did. If a
prefix matches two worktrees you get an error listing both, because guessing on
`tw rm` is how people lose work. Several commands take a second name too, if the
first isn't what came to mind: `create`, `remove`, `list`, `reopen`, `main`.

Output is pipe-friendly. stdout is the answer and nothing else, so
`cd "$(tw new eng-2318)"` and `tw ls --json | jq` both do what you'd hope.

## Configuring it

One TOML file per repo, written by `tw setup`, in
`~/.config/treewright/repos/<name>.toml`. The filename is what you pass to
commands that take a `[repo]`.

```toml
main_dir      = "~/code/storefront"  # required: your main checkout
base_branch   = "staging"            # fork from and compare against this (default: main)
branch_prefix = "john/"              # branch name is <prefix><slug> (default: none)

# Or, if your team namespaces by kind of work rather than by person, list them
# instead and pick one by naming it: `tw new bug/eng-1` branches bug/eng-1, and
# the worktree is still repo-eng-1. A bare slug gets the first. One key or the
# other, not both.
# branch_prefixes = ["feature/", "bug/", "chore/"]

# Files git ignores, like your .env. A new worktree starts without them,
# so treewright copies them in from your main checkout.
carry_files = ["apps/api/.env", ".env.local"]

# The agent these windows run. Fills in command and resume_command, and copies
# the agent's own gitignored settings — permissions, hooks — into each new
# worktree. `tw setup` writes it when it finds the agent installed.
agent = "claude"

# What the windows launch; either overrides what agent supplies. {prompt} is
# where --prompt's text lands, shell-quoted. No prompt, no placeholder — it
# just disappears.
command        = "claude {prompt}"
resume_command = "claude --continue {prompt}"

post_create    = "npm install"         # runs in the background after `new`

# Or a list of commands, run in order and stopped at the first failure. Each one
# is its own step, starting in the worktree root. `new` prints where the log is.
# post_create = ["npm install", "npm run codegen", "npm run build"]

ticket_pattern = '(?i)^(eng-[0-9]+)'   # first capture group names the window
tmux_session   = "shop"                # session for this repo (default: this file's name)
```

`main_dir` is the only one you need. Misspell a key and you get an error instead
of a setting that silently does nothing. If you're ever unsure what's in effect,
`tw config` prints the lot with defaults filled in, and `tw doctor` checks it.

Nothing that runs for you fails quietly. If `post_create` stops, the next `ls`,
`cd` or `resume` for that worktree tells you which command it stopped at and where
the log is. If `command` fails, its window stays open with the error still on
screen rather than closing before you can read it.

You don't have to say which repo you mean. treewright matches on where you're
standing, and that works from inside a worktree too.

## It won't let you lose work

- `tw rm` refuses if the worktree is dirty or has commits that aren't on origin.
  It fetches first, so something you merged two minutes ago still counts as
  merged. `--force` if you mean it.
- `tw prune` only takes worktrees that are both merged and clean. Your open PRs
  are never on the list.
- Nothing destructive runs on a guess. An ambiguous slug is an error, not a
  coin flip.
- Deleting a worktree strands its tmux window in a directory that no longer
  exists, so treewright offers to close it for you. If that window is the last
  one in the session, it says so first, because closing it would detach you.

## Questions you might have

### Why not just use `git worktree`?

You should, and treewright does. Git makes the directory. It doesn't copy your
`.env` files, run your install step, name a tmux window after the ticket,
remember which window belongs to which checkout, or stop you deleting a branch
you never pushed.

### Do I need to be using an AI agent?

No, though that's what it was built for, and it shows. The defaults launch
`claude`, and the tmux key bindings exist because a window running an agent has
no shell in it to type into. But `command` is just a shell command. Set it to
`nvim`, or `$SHELL` for a plain prompt, and you've got a worktree and tmux
manager with no AI in it anywhere.

### Will it mess with my existing tmux setup?

No. Every repo's windows go in their own session, named after its config. The
one global thing the tmux snippet does is turn terminal titles on, and it's two
lines you can delete.

### What if I'm not in tmux?

Everything still works. Windows get created detached and treewright tells you
how to attach. Without the shell integration, `tw cd` prints the path instead of
moving you.

## More

- `tw help <command>` for detail on anything above.
- [`docs/design-notes.md`](docs/design-notes.md) if you want to know why it
  behaves the way it does.
- [`CLAUDE.md`](CLAUDE.md) if you're working on treewright itself.
