# treewright

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

That gets you:

- a checkout at `~/code/storefront-eng-2318-cart-total-rounding`
- a branch `john/eng-2318-cart-total-rounding`, forked off the latest `origin/main`
- your gitignored `.env` files copied into it
- `npm install` already running in the background
- a tmux window called `ENG-2318` with your agent in it

Add `--prompt "the cart total rounds down at checkout"` and the agent in that
window starts on it before you've even switched to it — `tw new` stops being
"prepare a desk" and becomes "assign the work".

That example carries a ticket key, because the repo it comes from uses one.
Nothing here needs one: `tw new dark-mode-toggle` is the same command, and gets
you the same worktree, branch and window. See [Do I need a ticket
system?](#do-i-need-a-ticket-system).

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

That binds two keys, which open a popup over whatever's running — a treewright
window runs your agent as the window's command, so there's no shell in there to
type into:

| Key | What it does |
|---|---|
| `prefix + T` | Pick a worktree and jump to it |
| `prefix + N` | Type a slug, get a worktree |

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
and writes them to a commented TOML file that says which values it detected and
which it guessed, so fixing a bad one is a two-second edit. The email-derived
branch prefix is the one worth a look: a git address that names a forge rather
than a person gives you a namespace called `codeberg/`. Use `--dry-run` if
you'd rather look before it writes anything.

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

Any agent that can run a command when its state changes can report this way, and
`tw agent-init claude` installs the wiring for the ones it knows. What it writes
is a plugin — `.claude/skills/treewright/` in your main checkout, which claude
loads whole on its next start — and it goes both directions: the hooks that fill
the AGENT column, and a skill teaching the agent to drive treewright. Read
what's in flight, spawn a sibling worktree with a prompt, respect the teardown
guards — so you can ask the agent in your MAIN window to farm three jobs out to
three worktrees and it knows exactly how.

Nothing else is edited: no settings file, no dotfile, no `.gitignore`. Set
`agent = "claude"` in the config and the plugin is carried into every new
worktree, so the repos you use treewright in are wired and the ones you don't
are untouched. Run it again after upgrading treewright and it updates the wiring
in your main checkout in place — that's the reason it's a directory of
treewright's own rather than a fragment you paste somewhere.

The copies in your worktrees are a different matter: each got its own when it
was created, and `tw agent-init` doesn't reach them. `tw refresh` rewrites all
of them, and `tw doctor` names any that have fallen behind. See
[Upgrading](#upgrading).

Since it won't write your `.gitignore` for you, that directory shows up as
untracked until you decide what it is: ignore it and the wiring stays yours,
commit it and everyone who clones gets it. `tw doctor` keeps mentioning it until
you've picked one.

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
| `tw new <slug> [window-name]` | Fork a branch off the latest `origin/<base_branch>`, make the worktree, open a window on it. `--prompt` hands the agent its first instruction, `--prompt-file` points it at a brief too long to type |
| `tw resume [slug]` | Go back to a worktree's window, or open it again — `--prompt` hands the agent its next instruction. Shows the menu if you don't name one |
| `tw send <slug> <message>` | Type one line at the agent already running in that worktree's window, showing you the pane first. `--dry-run` just looks |
| `tw cd [slug]` | Move your shell into a worktree |
| `tw base [repo]` | Go to the main checkout's window |
| `tw attach [repo]` | Attach this terminal to a repo's tmux session |
| `tw ls [--json] [repo]` | The table above. Touches nothing |
| `tw rm [-f] [-y] <slug>` | Delete the worktree, branch, stale remote ref, and window |
| `tw prune [-y] [repo]` | Delete every merged, clean worktree. Lists them first unless you pass `--yes` |
| `tw setup [-n] [--refresh] [name]` | Register the repo you're standing in, or regenerate its config in place |
| `tw config [repo]` | Show the settings actually in force, defaults and all |
| `tw doctor` | Check your install and every config you've registered |
| `tw shell-init <shell>` | Print the shell integration for zsh, bash, or fish |
| `tw tmux-init [--apply]` | Print the tmux integration, or load it straight into the server |
| `tw agent-init [--global] [--print] <agent>` | Install the plugin that wires an agent to treewright — hooks in, skill out |
| `tw refresh [repo]` | After an upgrade: rewrite the plugin in every checkout, reload the tmux bindings |
| `tw version [--check]` | Print the version, and with `--check` say whether a newer one is out |

`tw help <command>` has the details on any of them. There's also `tw popup`,
which is what the key bindings run, and `tw signal`, which is what agent hooks
run to fill the AGENT column; you won't type either yourself.

You don't have to spell slugs out. `tw cd eng-2318` finds
`eng-2318-cart-total-rounding` and tells you that's what it did. If a
prefix matches two worktrees you get an error listing both, because guessing on
`tw rm` is how people lose work.

Output is pipe-friendly. stdout is the answer and nothing else, so
`cd "$(tw new eng-2318)"` and `tw ls --json | jq` both do what you'd hope. The
JSON always leads with your main checkout's row, even in a repo with no
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
command        = "claude {prompt}"    # what the window launches; {prompt} takes --prompt's text
resume_command = "claude --continue {prompt}"
post_create    = "npm install"        # background setup after `new`; a list runs in order
ticket_pattern = '(?i)^(eng-[0-9]+)'  # names the window; "" if you don't track work by ticket
tmux_session   = "shop"               # session for this repo (default: this file's name)
```

`main_dir` is the only one you need, and `tw setup` writes the rest as a
commented file explaining each one, so this is the shape rather than the manual.
Misspell a key and you get an error instead of a setting that silently does
nothing — or, if it came from a newer treewright than the one you're running,
the error says that too. If you're unsure what's in effect, `tw config` prints
the lot with defaults filled in, and `tw doctor` checks it.

Nothing that runs for you fails quietly. If `post_create` stops, the next `ls`,
`cd` or `resume` for that worktree tells you which command it stopped at and where
the log is. If `command` fails, its window stays open with the error still on
screen rather than closing before you can read it. If the window never opened at
all, `tw resume` starts the agent from scratch instead of trying to continue a
conversation that was never had, and says that's what it's doing. And a
`--prompt` too long for tmux to carry is refused before the worktree is made,
rather than after.

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
- `tw new` forks from `origin/<base_branch>`, so it tells you when your main
  checkout is holding commits you haven't pushed: they aren't in the new
  worktree, and neither are the files they added.
- Deleting a worktree strands its tmux window in a directory that no longer
  exists, so treewright offers to close it for you. If that window is the last
  one in the session, it says so first, because closing it would detach you.

## Upgrading

```sh
brew upgrade --cask treewright   # or: go install github.com/jay-snyder/treewright@latest
tw refresh                       # from a repo you use treewright in
tw doctor                        # says whether anything is still behind
```

Most of it keeps up on its own. The line in your shell startup file and the line
in `~/.tmux.conf` both ask the binary for their own text, so a new shell and a
new tmux server get the new version without you doing anything. Three things
don't, and all three are quiet about it:

- **Your worktrees.** Each gets its copy of the agent plugin when it's created,
  and nothing looks at it again — so a worktree made before the upgrade keeps
  running the old hooks and the old skill for as long as it lives. `tw refresh`
  rewrites every copy and tells you which files moved where.
- **The tmux server you're attached to.** It keeps what it loaded at start,
  which may have been weeks ago. `tw refresh` reloads the bindings, onto
  whatever keys they're already on — if you moved them with `--resume-key`,
  they stay moved.
- **The shell you're sitting in.** Nothing can replace a function in a running
  shell except that shell. Open a new terminal. `tw doctor` and `tw refresh`
  both say when yours is out of date, since it's the one thing neither can fix.

Your config file is yours and never gets rewritten behind you. When you want the
newer commentary and any key added since, `tw setup --refresh` regenerates one
in place: it keeps every setting you'd chosen and re-detects nothing, so a base
branch or a prefix you corrected by hand stays corrected. `tw doctor` says when
a config predates the generator.

`tw version --check` asks GitHub whether there's a newer release and names the
upgrade command for the route you installed by. It's the only thing here that
touches the network, it only does it when asked, and `tw doctor` — which asks
too — skips it in silence when you're offline.

## Uninstalling

There's no uninstall script, and the list is short on purpose: treewright writes
its own registry and nothing else outside a repository. Everything else is a line
you pasted in yourself.

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
they leave with the repo. Leftover tmux sessions close with `tmux kill-session
-t <repo>`.

## Questions you might have

### Why not just use `git worktree`?

You should, and treewright does. Git makes the directory. It doesn't copy your
`.env` files, run your install step, name a tmux window after the work,
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
  behaves the way it does — with [`docs/tmux.md`](docs/tmux.md) for sessions,
  windows and key bindings, and [`docs/agents.md`](docs/agents.md) for how an
  agent reports what it is doing.
- [`CLAUDE.md`](CLAUDE.md) if you're working on treewright itself.
