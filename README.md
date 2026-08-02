# treewright

[![CI](https://github.com/jay-snyder/treewright/actions/workflows/ci.yaml/badge.svg)](https://github.com/jay-snyder/treewright/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/jay-snyder/treewright)](https://github.com/jay-snyder/treewright/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Give every piece of work its own git worktree, tmux window, and agent session.
One command to start it, one to tear it down.

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

That one line gets you:

- a checkout at `~/code/storefront-eng-2318-cart-total-rounding`
- a branch `john/eng-2318-cart-total-rounding`, forked off the latest `origin/main`
- your gitignored `.env` files copied into it
- `npm install` already running in the background
- a tmux window called `ENG-2318` with your agent in it

Add `--prompt "the cart total rounds down at checkout"` and the agent in that
window starts on it before you've even switched to it — `tw new` stops being
"prepare a desk" and becomes "assign the work".

Do that three times and `prefix + T` switches between all three from anywhere,
including from inside a running agent. When the PR merges, `tw rm eng-2318`
takes the whole thing away, and stops you if there's unpushed work in there.

No ticket system required. The examples carry a key because the repo they come
from does; `tw new dark-mode-toggle` is the same command and gets you the same
worktree, branch and window.

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

`MAIN` is home base, and `tw base` opens it from anywhere. It sits in the main
checkout, parked on `main` — start new work there, ask your general questions
there, and keep feature work out of it.

## Install

```sh
brew install jay-snyder/tap/treewright
```

Two names for one binary: `treewright`, and `tw`, which is the one you'll
actually type. The cask brings git and tmux along and clears the quarantine flag
macOS puts on unsigned downloads.

Not on macOS? Grab a tarball from
[releases](https://github.com/jay-snyder/treewright/releases), or:

```sh
go install github.com/jay-snyder/treewright@latest
```

### Set up your shell

One line in your shell's startup file defines `tw`, turns on tab completion, and
lets `tw cd` actually move your shell:

```sh
# in ~/.zshrc
eval "$(treewright shell-init zsh)"

# in ~/.bashrc
eval "$(treewright shell-init bash)"

# in ~/.config/fish/config.fish
treewright shell-init fish | source
```

Open a new terminal, or re-source the file, and `tw` is there.

### Set up tmux

One line in `~/.tmux.conf`:

```tmux
run-shell 'treewright tmux-init --apply'
```

That binds two keys. Both open a popup over whatever's running — a treewright
window runs your agent as the window's command, so there's no shell in there to
type into:

| Key | What it does |
|---|---|
| `prefix + T` | Pick a worktree and jump to it |
| `prefix + N` | Type a slug, get a worktree |

Both keys are free in stock tmux. Already using them? Pick your own on the same
line:

```tmux
run-shell 'treewright tmux-init --apply --resume-key G --new-key C-n'
```

### If something didn't take

`tw doctor` checks all of it and names whatever is missing. Three answers cover
almost every case:

- **`tw: command not found`** — the `eval` line ran before `treewright` was
  reachable, so it defined nothing. Note that a `PATH` set in a login-only file
  like `~/.profile`, `~/.zprofile` or `~/.bash_profile` needs a logout to take;
  a new terminal isn't enough.
- **The keys do nothing** — `run-shell` searches the tmux server's `PATH`, not
  your shell's, and tmux stays quiet when a line in your config fails. Make
  treewright reachable, then `tmux kill-server`: a running server keeps the
  `PATH` it started with.
- **`go install` finished, `treewright` isn't found** — it went to
  `$(go env GOPATH)/bin`, which is on your `PATH` only if you put it there.

## Getting started

From inside the repo you want to work in:

```sh
tw setup      # writes a config for this repo
tw doctor     # confirms it's all wired up
```

`setup` works the rest out for you, and shows its work:

- **your main checkout, and the branch to fork from** — read from `origin/HEAD`
- **your branch prefixes** — read off the branches already on origin, or from
  your git email if they say nothing
- **the gitignored `.env` files** a fresh worktree needs

Those go into a commented TOML file marking what it detected and what it
guessed, so correcting one is a two-second edit — and `--dry-run` looks without
writing anything. Give the email-derived prefix a glance, though: a git address
that names a forge rather than a person gets you a namespace called `codeberg/`.

After that it's four commands:

```sh
tw new eng-2318-cart-total-rounding   # worktree + branch + window
tw ls                                 # what's going on
tw resume eng-2318                    # back to that window from anywhere
tw rm eng-2318                        # PR merged, clean it up
```

Prefixes are enough. `tw cd eng-2318` finds `eng-2318-cart-total-rounding` and
tells you that's what it did. A prefix matching two worktrees is an error
listing both, because guessing on `tw rm` is how people lose work.

Started something in your main checkout before realizing it wanted a branch?
`tw move eng-2318` builds the worktree exactly as `new` does and carries the
uncommitted changes over. Nothing is cleared out of the checkout until the work
has demonstrably arrived on the other side.

`tw help` lists every command; `tw help <command>` has its flags and the detail.

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
| `dirty (n)` | `n` uncommitted files. Outranks every other status, since it's the easiest thing to lose. |
| `merged` | Landed on `origin/main`. Safe to delete, and `tw prune` will. |
| `unpushed (n)` | `n` commits that exist nowhere else. `tw rm` won't touch it without `--force`. |
| `active` | Pushed, not merged. An open PR. `tw prune` leaves these alone. |
| `base` | Your main checkout. Not a worktree, never deleted. |

Squash merges count as merged, which sounds obvious but isn't: a squash merge
leaves none of your branch's commits upstream, so the usual check calls landed
work unpushed and refuses to clean it up.

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

It pipes, too. stdout carries the answer and nothing else, so
`cd "$(tw new eng-2318)"` and `tw ls --json | jq` both do what you'd hope, and
the JSON always leads with your main checkout's row — even in a repo with no
worktrees yet, so anything reading it has somewhere to start.

## Configuring it

One TOML file per repo, written by `tw setup`, in
`~/.config/treewright/repos/<name>.toml`. The filename is what you pass to
commands that take a `[repo]`.

```toml
version        = 1                    # which layout tw setup wrote; doctor checks it
main_dir       = "~/code/storefront"  # required: your main checkout
base_branch    = "staging"            # fork from and compare against this (default: main)
branch_prefix  = "john/"              # branch is <prefix><slug> (default: none)
# branch_prefixes = ["feature/", "bug/"]   # or several — pick one: `tw new bug/eng-1`
carry_files    = ["apps/api/.env"]    # files a fresh worktree needs and doesn't have
agent          = "claude"             # fills in the two commands, carries its settings and wiring
command        = "claude {prompt}"    # what the window launches; "" for a shell and no agent
resume_command = "claude --continue {prompt}"
post_create    = "npm install"        # background setup after `new`; a list runs in order
ticket_pattern = '(?i)^(eng-[0-9]+)'  # names the window; "" if you don't track work by ticket
tmux_session   = "shop"               # session for this repo (default: this file's name)
```

`main_dir` is the only one you need. `tw setup` writes the rest as a commented
file explaining each key, so treat this as the shape rather than the manual. Not
sure what's in effect? `tw config` prints the lot with defaults filled in, and
`tw doctor` checks it. Misspell a key and you get an error rather than a setting
that silently does nothing — and if the key came from a newer treewright than
the one you're running, the error says so.

Nothing that runs for you fails quietly:

- **`post_create` stops** — the next `ls`, `cd` or `resume` on that worktree
  names the command it stopped at and where the log is.
- **`command` fails** — its window stays open with the error still on screen,
  rather than closing before you can read it.
- **The window never opened** — `tw resume` starts the agent fresh instead of
  trying to continue a conversation that was never had, and says so.
- **A `--prompt` too long for tmux to carry** — refused before the worktree is
  made, not after.

You never have to say which repo you mean, either. treewright matches on where
you're standing, and that works from inside a worktree too.

## Working with an agent

Run three agents at once and the question git can't answer is which one needs
you. So the ones that can report, do: an agent's hooks run `tw signal` with
`working`, `waiting` or `done`, the table grows an AGENT column saying exactly
that, and a window whose agent is waiting on you gets a `!` in front of its name
in the tmux status line. Agents that report nothing cost nothing — the column
appears only once something has signaled.

Any agent that can run a command when its state changes can report this way, and
`tw agent-init claude` installs the wiring for the ones treewright knows. It goes
both directions:

- **Hooks in**, which fill the AGENT column and the `!`.
- **A skill out**, teaching the agent to drive treewright: read what's in
  flight, spawn a sibling worktree with a prompt, respect the teardown guards.
  Ask the agent in your MAIN window to farm three jobs out to three worktrees
  and it knows exactly how.

The hooks also hold the rule the skill teaches. A tool call that would change
*another* worktree from outside its window is refused, and the refusal names the
two commands that hand the work over instead. Reading another worktree is never
blocked — that is how you review it.

What it writes is a plugin — `.claude/skills/treewright/` in your main checkout,
which claude loads whole on its next start. Nothing else is edited: no settings
file, no dotfile, no `.gitignore`. Set `agent = "claude"` in the config and every
new worktree gets a copy, so the repos you use treewright in are wired and the
ones you don't are left alone.

Since it won't write your `.gitignore` for you, that directory shows up as
untracked until you decide what it is: ignore it and the wiring stays yours,
commit it and everyone who clones gets it. `tw doctor` keeps mentioning it until
you've picked one.

Already have an agent running? `tw send eng-2318 "rebase on main first"` types
one line into its window and presses Enter — where `--prompt` only reaches an
agent that a `new` or `resume` actually starts, this reaches the one standing
there. It shows you the pane first, every time: an agent sitting on a question
with options takes your next keystrokes as the answer, so a message sent blind
can pick an option nobody read.

## It won't let you lose work

- `tw rm` refuses if the worktree is dirty or has commits that aren't on origin.
  It fetches first, so something you merged two minutes ago still counts as
  merged. `--force` if you mean it.
- `tw prune` only takes worktrees that are both merged and clean. Your open PRs
  are never on the list.
- Nothing destructive runs on a guess. An ambiguous slug is an error, not a
  coin flip.
- `tw new` forks from `origin/<base_branch>`, so it tells you when your main
  checkout is holding commits you haven't pushed — they aren't in the new
  worktree, and neither are the files they added.
- The agent *is* the window's command, so closing a window while its agent is
  `working` stops the work and loses the conversation with it. Anything that
  closes one says so first. A warning rather than a refusal: a refusal would
  need a `--force`, and those get passed by reflex.
- Deleting a worktree strands its tmux window in a directory that no longer
  exists, so treewright offers to close it for you — `tw close <slug>` does it
  later if you decline. If that window is the last one in the session, it says
  so first, because closing it would detach you.

## Upgrading

```sh
brew upgrade --cask treewright   # or: go install github.com/jay-snyder/treewright@latest
tw refresh                       # from a repo you use treewright in
tw doctor                        # says whether anything is still behind
```

Most of it keeps up on its own: the line in your shell startup file and the line
in `~/.tmux.conf` both ask the binary for their own text, so a new shell and a
new tmux server pick up the new version for free. Three things don't, and all
three are quiet about it:

- **Your worktrees.** Each got its copy of the agent plugin when it was created,
  and nothing looks at it again — so one made before the upgrade keeps running
  the old hooks and the old skill for as long as it lives. `tw refresh` rewrites
  every copy and names what moved where.
- **The tmux server you're attached to.** It keeps what it loaded at start,
  which may have been weeks ago. `tw refresh` reloads the bindings onto whatever
  keys they're already on.
- **The shell you're sitting in.** Only that shell can replace its own
  functions, so open a new terminal. `tw doctor` and `tw refresh` both say when
  yours is out of date, since it's the one thing neither can fix.

Your config file is yours and never gets rewritten behind you. When you want the
newer commentary and any key added since, `tw setup --refresh` regenerates one in
place: it keeps every setting you'd chosen and re-detects nothing, so a base
branch or a prefix you fixed by hand stays fixed.

`tw version --check` asks GitHub whether there's a newer release and names the
upgrade command for the route you installed by. It's the only thing here that
touches the network, and only when you ask it to.

## Uninstalling

Short list, on purpose: treewright writes its own registry and nothing else
outside a repository. Everything else is a line you pasted in yourself.

**Take the worktrees back first, while treewright is still here to do it** —
afterwards they're ordinary git worktrees, removed one at a time by hand:

```sh
tw prune --yes                      # every merged, clean worktree
tw rm <slug>                        # whatever prune left
brew uninstall --cask treewright    # or: rm $(go env GOPATH)/bin/treewright
rm -r ~/.config/treewright          # the one thing it wrote for itself
```

Then delete the `shell-init` line from your shell startup file and the
`tmux-init` line from `~/.tmux.conf`. Agent hooks and skills were per-repo, so
they leave with the repo. Leftover tmux sessions close with
`tmux kill-session -t <repo>`.

## Questions you might have

### Why not just use `git worktree`?

You should, and treewright does — git makes the directory. What it doesn't do is
copy your `.env` files, run your install step, name a tmux window after the work,
remember which window belongs to which checkout, or stop you deleting a branch
you never pushed.

### Do I need a ticket system?

No. Nothing reads a tracker or requires a key — a slug is just a name, and
`tw new dark-mode-toggle` works exactly like the ticket examples above.

The only place a ticket shows up is the tmux window's name, which has to be
short. treewright uses a leading key like `eng-142` when it finds one, and
otherwise the slug, cut to ten characters with a `…`. Set `ticket_pattern = ""`
if your slugs sometimes *look* like keys and you'd rather they never be read as
one, or name a window outright with `tw new dark-mode-toggle DARKMODE`.

### Do I need to be using an AI agent?

No, though that's what it was built for and it shows. The defaults launch
`claude`, and the tmux key bindings exist because a window running an agent has
no shell in it to type into. But `command` is just a shell command: set it to
`nvim`, or to `""` for a window holding nothing but your shell, and you've got a
worktree and tmux manager with no AI in it anywhere. Drop the `agent` key too if
you have one — it supplies a command when yours is blank, which is the one place
an empty setting doesn't win.

### Will it mess with my existing tmux setup?

No. Every repo's windows go in their own session, named after its config. The
one global thing the tmux snippet does is turn terminal titles on, and that's two
lines you can delete.

### What if I'm not in tmux?

Everything still works. Windows get created detached, and `tw attach` puts this
terminal in a repo's session when you want one. Without the shell integration,
`tw cd` prints the path instead of moving you.

## Read more

- `tw help <command>` for detail on any command, including its flags.
- [`docs/design-notes.md`](docs/design-notes.md) for why it behaves the way it
  does — with [`docs/tmux.md`](docs/tmux.md) for sessions, windows and key
  bindings, and [`docs/agents.md`](docs/agents.md) for how an agent reports what
  it's doing.
- [`CLAUDE.md`](CLAUDE.md) if you're working on treewright itself.
