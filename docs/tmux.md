# tmux: sessions, windows, and reaching them

How treewright arranges what it opens, and why. Part of the design notes —
[`design-notes.md`](design-notes.md) has the rest, and
[`agents.md`](agents.md) covers what runs *inside* these windows.

---

## One session per repository

Every repository's windows live in a tmux session of its own, named after its
config: the base window in the main checkout, and one window per worktree.

```
tmux session "storefront"                      tmux session "checkout-api"
├── main      ~/code/storefront                ├── main    ~/code/checkout-api
├── eng-2318  ~/code/storefront-eng-2318       └── pay-88  ~/code/checkout-api-pay-88
└── eng-2324  ~/code/storefront-eng-2324
```

The alternative — opening windows in whatever session the caller happens to be
attached to — mixes repositories in one status line, where two windows both
called `main` belong to different projects and a ticket key says nothing about which
checkout it is in. Worse, it made `resume` a silent no-op across sessions:
selecting a window in a session your client is not attached to succeeds and
changes nothing you can see.

What follows from it:

- **`new` creates the session** when it is not running yet, so the first command
  of the day establishes it. `resume`, `base` and `restore` do the same.
- **`base` is the same window every time.** A window already sitting in the main
  checkout is selected rather than a second one opened beside it — and being the
  session's first window, it is what keeps the session alive as worktrees come
  and go.
- **Commands follow their window across sessions.** Resuming a worktree while
  attached to another repository's session switches you there — unless the
  repository was named, which is the next section.
- **Outside tmux nothing is skipped.** The session and window are created
  detached, and treewright says to run `treewright attach` — its own command
  rather than a `tmux attach` to copy, because that spelling names the session
  exactly and finds the right server under `TREEWRIGHT_TMUX_LABEL`. A window that
  turns out to be in some *other* session is the exception: `attach` takes a
  repository and would send you to this repository's session, which is not where
  the window is and may not be running at all, so that message spells out the
  `tmux attach-session` for the session the window is really in.
- **A window in the wrong session is used rather than duplicated** — one you
  opened by hand, or one from before the repository had a session of its own.
  `ls` shows it as `session:window`, and `resume` switches to it there.
- **Except the pane you are typing into.** Standing in the main checkout is
  where you are when you type `tw base`, so the shell you type it from is found
  as the window on that directory — and switching to it is a switch to the
  session the client is already in. `base` warned that the window was in another
  session "rather than" one that did not exist yet, then did nothing: no session,
  no window, no agent, and an attach hint pointing at the session it had just
  failed to create. A window treewright opened on the directory is exempt, so
  `base` inside the base window, and `resume` inside a worktree's own window,
  stay the no-ops they should be — you are already there.

`tmux_session` overrides the name, for one already taken by something else or
two repositories that deliberately want to share. `tw doctor` reports which
session each repository maps to, and warns when two configs name the same one.

**Session names are matched as prefixes by tmux.** With only `checkout-api`
running, a target of `checkout` resolves to it, so a repo named `checkout` would
silently drop its windows into the other project's session. Every session target in
`internal/tmux` is therefore written in tmux's exact form, `=api`. The one
command that does not understand that form is `set-option`, which is why nothing
in that package sets session options.

## After a restart

A tmux server does not outlive a reboot; the checkouts it had windows on do. So a
repository's session is something you reopen, and `tw restore` reopens all of one
at once — the base window, a window for each scratch session the reboot
interrupted, a window per worktree, each resuming its conversation with
`command` behind it, and then this terminal attached to the session. What it
opens is what `tw ls` lists. Why no layout is saved, what is kept instead, and
why it stays out of the session on the one run that has something to report, is
in [`design-notes.md`](design-notes.md).

**One tab per repository is the pattern, and generating it is not treewright's
job.** `restore` takes one repository, so the terminal side is a tab per
repository with its working directory set and `treewright restore` as what it
runs — a kitty session file, a WezTerm config, an iTerm arrangement, a script
that opens tabs, whichever your terminal has. As a kitty session file:

```
# ~/.config/kitty/after-reboot.conf — kitty --session after-reboot.conf
new_tab storefront
cd ~/code/storefront
launch treewright restore

new_tab checkout-api
cd ~/code/checkout-api
launch treewright restore
```

A launcher with no working directory to set names the repository instead —
`treewright restore storefront` — which is why the positional is there.

Generating that file is deliberately left out: it would be a promise to generate
one for the next terminal emulator, and for the one after that, in exchange for
six lines a person writes once.

## Whose client is it

One session per repository has a consequence that took two incidents in one
sitting to surface: a window opened in another repository is *always* in
another session, so `Focus`, which follows a window across sessions, always
switched. An agent standing in one repository and spawning work in another
therefore moved the operator's attached client out of the repository they were
watching and into one they had not asked for — arriving, from that seat, as
their session being replaced by a duplicate of somebody else's, with detach and
reattach the only way back.

**So the client moves only when the repository acted on is the one the caller
is standing in.** Name another repository and the window is created, left
current in its own session so whoever attaches lands on it, and your client
stays where it was; treewright says so and names `tw attach <repo>` as the way
over. That is `Select` rather than `Focus` — the same pair `restore` already
used, and for a version of the same reason: focusing each window it opened
would drag an attached client through every one of them.

The select half is not a formality. tmux makes a *new* window current by
itself, but a window that was merely found is not, so without it "the window is
ready over there" would be a promise that lands whoever attaches somewhere
else.

Three details of the rule are deliberate:

- **It is not a flag.** A flag puts the operator's client at the mercy of every
  caller remembering to pass it, and the callers likeliest to forget are the
  agents this exists to contain. The behavior follows from whose session is
  being acted on, which is not something a caller can neglect to mention.
- **Naming your own repository suppresses nothing.** The agent guide tells
  agents to pass `--repo` always, so a flag that changed where you land
  depending on whether you bothered to name the repository you were already in
  would be one nobody could pass by default.
- **`attach` and `restore` still move you**, because moving you is what they
  were asked for. The line is between a command whose answer is a *window* —
  `new`, `resume`, `base` — where arriving is a convenience, and one whose
  answer is a *session*, where arriving is the whole request. `restore` keeps
  its `-d` for the same reason it always had it.

Outside tmux none of this arises: there is no client, `Focus` skips the switch
itself, and both paths print the way in. `send` never had the problem, since it
types at a pane and calls neither — and that is a property to keep rather than
a gap to fill.

## Window identity

Every window treewright opens carries the worktree it belongs to, as tmux user
options:

| Option | Value |
|---|---|
| `@treewright_repo` | The config's name. |
| `@treewright_worktree` | The checkout the window was opened on. |
| `@treewright_slug` | The worktree. Unset on the base window and on a scratch window, which are not worktrees. |
| `@treewright_branch` | The branch that worktree is on. |
| `@treewright_scratch` | The name a scratch window was opened under. Unset on every other window. |
| `@treewright_agent_state` | What the agent in it last signaled. Written by `signal`, not at creation — see "Agent state" in [`agents.md`](agents.md). |

Of these, the worktree is what *identifies* a window. A pane's
directory moves with every `cd`, and two windows can stand in one directory at
once — the base window does exactly that after `tw cd` — so which window a
worktree owns cannot be read off where its shell happens to be standing.

**Identity has two forms.** A worktree's window is identified by its worktree; a
scratch window — a second agent session standing on the base checkout, opened by
`tw scratch <name>` — is identified by itself, the name in `@treewright_scratch`.
It still carries `@treewright_worktree`, set to the main checkout, since that
option is where a window stands as well as what it is. But it claims no
directory: `tmux.Windows`, the directory-to-window map every other lookup reads,
never contains one, which is what keeps the window on the main checkout the base
window however many scratch windows stand beside it. `tmux.Scratch` is where they
are found instead, by name, one repository's at a time. See "Scratch windows" in
[`design-notes.md`](design-notes.md) for why skipping them is the only version of
this that is true.

Before the stamp existed, `list-panes -a` walked windows in index order and two
windows standing in one directory resolved to whichever the user had arranged
first: a wrong name in `ls`, the wrong window focused by `resume`, and the wrong
window offered up for closing by `rm`, all changing under a `swap-window`. The
resolution order is now rank (stamped for this worktree beats unstamped beats
stamped for another), then the repository's own session, then the older window
id. See `Window.beats` in `internal/tmux/tmux.go`.

The stamp is carried on `Window` as the path it holds rather than as a bool,
because two questions are asked of it and only one of them is "did treewright
open this window". `openWindow` asks the other — did treewright open it *on this
directory* — to tell the checkout's own window apart from a shell standing in it,
and a bool cannot answer that. `Window.Stamped` remains for the callers that only
want the first, such as deciding whose window name treewright may decorate.

The options are written at creation, so a window treewright merely finds and
switches to keeps whatever it already had. Every stamp is best-effort but the
scratch name: a scratch window that missed it would read as a window on the main
checkout, and so as the base window, so one that cannot be stamped is closed
rather than left to be mistaken. The rest are there for your own status
line — `#{@treewright_repo}` costs nothing to render, where the alternative is
shelling out to git on every status interval. They keep the full `@treewright_`
prefix because they are a public, greppable interface, and a cryptic `@tw_` would
save nothing anyone types by hand.

## Typing at the window

`--prompt` reaches an agent only where the resume actually starts one: the
command carrying it runs in a window that was *created*, so a resume that finds
one already open switches to it and warns that the prompt went nowhere. What is
left in that window is an agent, and an agent is an ordinary TUI on an ordinary
tty — so tmux can type at it. `tw send <slug> <message>` is that.

It was taught as raw `tmux send-keys` in the claude skill before it was a
command, and everything wrong with that is in the four rules the prose had to
carry, each of which fails silently and differently:

- **Capture the pane first.** An agent sitting on a question with options takes
  the next keystrokes as the answer to it, so a message sent blind can pick an
  option nobody read. Reading the pane changes nothing and costs one call, so
  `send` does it every time rather than behind a flag — a flag would be a way to
  turn off the check, and what it prevents leaves no trace. `--dry-run` captures
  and sends nothing, which makes looking an action of its own; the message may
  be left off entirely there.
- **`-l`, or the words arrive as key names.** "Enter the review comments" is
  four keystrokes to tmux without it.
- **`Enter` as its own call**, since under `-l` it would arrive as five
  characters. The flags end with `--` as well, so a message starting with a dash
  is a message.
- **One line.** Enter submits, so a newline posts the rest as further turns —
  refused rather than split, treewright having no way to tell an accident from an
  intention and no way to undo either. The way through is `--prompt-file`'s: put
  the text in a file, send a line naming it.

Two things it refuses that the prose never mentioned. **The caller's own
window**, since an agent typing at itself puts the message into this very
session ahead of whatever it was answering — `tmux.CurrentWindow` has the id, so
this needs no vigilance from the sender. And **a window held open after its
command died**, which is the one place a treewright window outlives its agent:
what is reading the keyboard there is a shell blocked on `read`, so the message
reaches nobody and the Enter after it closes the window, erasing the output the
wrapper exists to preserve. It is recognized from the capture — the held-open
notice is the last line such a window shows — rather than from the agent state,
which the wrapper clears and which is equally absent from every window whose
agent never signaled.

Nothing writes `@treewright_agent_state`. The receiving agent's own
`UserPromptSubmit` hook fires `signal working` when the message lands, which is
the protocol working as designed; a sender stamping the window would be guessing
at a state only the agent can report.

### Reaching an agent in another repository

`send --repo <name> <slug>` types at an agent in a repository the sender is not
standing in, which is what makes one agent able to hand work to another across
sessions. `base` is a target like any slug, so a repository's base window — the
one an operator most often has an agent in — is addressable from outside it;
inside its own repository it always was, and what is new is being able to say
whose base is meant.

Nothing about the mechanics changes, and that is the point: the target is a
window id, which is server-unique, so the capture, the literal send, the Enter,
the refusal of the caller's own window and the refusal of a held-open window all
work across sessions exactly as they work within one. **Every one of them
matters more here, not less.** The sender cannot see the receiving session at
all, so the capture stops being a convenience and becomes the only look anybody
gets before a keystroke answers a question nobody read.

A scratch window is reached by its name, `tw send ask "…"`, and the window comes
with the name rather than being looked up by directory — the window standing for
the base checkout's directory is the base window, and typing there in a scratch
window's name is the mistake the name exists to prevent. Everything after the
lookup is by window id, so the capture and both refusals hold unchanged.

`send` goes through `internal/tmux` like everything else, which is the other half
of why it is a command. The raw form honored no `TREEWRIGHT_TMUX_LABEL`, went
through no `exact()`, and knew nothing of the `@treewright_worktree` stamp — so
it typed into whichever window happened to be standing in the directory.

## Terminal and tab titles

Attaching tends to leave the terminal tab titled with the command line that
attached rather than with the session. tmux is not the one writing it:
`set-titles` is off by default, so tmux never sets a title at all, and whatever
the shell wrote before it started — under kitty, iTerm2, or any terminal with
shell integration, the command being run — stays there until the next prompt,
which inside tmux never comes.

`tw tmux-init` turns it on, which is the one thing in that snippet not about
treewright:

```tmux
set -g set-titles on
set -g set-titles-string "#S: #W"
```

`#S` is the session, so the repository, and `#W` the window, so the worktree.

It is set there rather than by treewright itself, per session, for two reasons.
Session options are set with `set-option -t`, whose target does not accept the
exact-match `=name` form, so treewright would be back to the prefix matching the
section above avoids. And a title format is yours: a file you loaded on purpose
can change it, a `tw new` should not.

## Reaching treewright from inside a worktree

treewright runs your agent as the tmux window's own command, so a worktree's pane
*is* the agent — there is no shell in it to type `tw resume` into. Reaching
treewright from inside a worktree meant splitting a pane or going to find a
window that has a prompt. The key bindings close that gap by opening treewright
in a popup over whatever is running.

**Why the bindings go through `tw popup` rather than `display-popup` directly.**
tmux fixes a popup's size when it creates one, and `-w`/`-h` take only cells or a
percentage of the terminal. A percentage is the wrong unit for a picker, whose
height is the number of worktrees and whose width is the widest slug — neither of
which grows when the terminal does. Three worktrees need 83×8; 70%×60% of a
237×62 terminal is 165×37, ten times the area. Working the size out needs a
program, and `run-shell` is the only way a binding can run one.

**Why the bindings pass `#{client_tty}`.** A tmux command run from a backgrounded
`run-shell` has no association with the client that asked for it, so tmux falls
back to the most recently active one — and with two terminals attached, the popup
opens over whichever has been busier.

**Why the bindings pass `#{pane_current_path}`.** `run-shell` does not run in the
calling pane's directory. It runs in the tmux server's, which is wherever the
server was started — one repository's checkout, usually, and never the pane's.
Left to work out where it is, a popup therefore answers about that one repository
from every window on the server, and marks its worktree as the one you are
standing in. The visible symptom is the `ls` table putting its asterisk on the
same row whichever worktree the key was pressed in; the part that matters is that
`resume` in a second repository's window offers the first one's worktrees. It
hides behind the single-config fallback, which is why it survived so long: with
one config registered, `config.Resolve` returns it whatever directory you ask
from, and the wrong answer and the right one coincide.

`run-shell` has a `-c` flag that takes a start directory, and it is not the fix:
it will not expand a format into one, so `-c "#{pane_current_path}"` lands in the
home directory. The path has to travel inside the command string, where formats
are expanded, exactly as the client does. In the `new` binding its quotes are
escaped, that string already being the double-quoted argument of a `run-shell`
held by `command-prompt` — a directory can contain a space where a tty cannot, so
the quoting is load-bearing rather than decorative.

`--dir` is then acted on by moving into it, rather than being threaded through as
an argument, because everything downstream reads the working directory: sizing
resolves the config to count the worktrees, the popup starts where the process
stands, and the command inside it resolves the repository the same way again. The
process is put back afterwards, since the tests call `Run` in-process and a
command that wandered off and stayed there would decide where the next one thinks
it is.

**Why a binding that cannot find treewright says nothing at all.** tmux resolves
the command in the server's environment, which is whatever the server was started
from rather than the shell that edited the config, and it discards what a
`run-shell` at config load reports. A binary that is not on that `PATH` therefore
produces no bindings and no message — the keys simply do nothing, which reads as a
treewright bug rather than an installation one. Two things compound it: a server
keeps the environment it started with, so making the binary reachable afterwards
takes a `kill-server` rather than a new window; and the check that would report it,
`checkTmuxIntegration`, has to skip when no server is running, because `list-keys`
would start the very server whose emptiness it then described. So the one session
where the keys never loaded is also the one where nothing says so.

**Why `-EE` and not `-E`.** A single `-E` closes the popup however the command
exited, so anything it reported on the way out is gone before it can be read.
Doubled, tmux closes it only on success. That is also why `PopupHint` exists:
inside a popup, a non-zero exit leaves text on screen with nothing saying how to
dismiss it.

**Why cancelling `resume` exits 0 but cancelling `cd` does not.** Declining a
menu is not a failure, and in a popup a non-zero exit made Escape need pressing
twice — once to dismiss the picker, once to clear the popup holding "cancelled"
on screen. But `cd`'s answer is a path, and `cd "$(tw cd)"` succeeding with
nothing to print would send the shell home. So the two callers treat the same
cancellation differently, deliberately.

**The keys.** Being unbound is the smaller half of choosing one; the larger half
is what a missed shift does, since these get reached for in a hurry. `t` is
clock-mode and `n` is next-window, both harmless. The first version bound
`prefix + W`, for the mnemonic with tmux's own `w` window picker — but a great
many configs rebind lowercase `w` to `kill-window`, and there a missed shift
destroys the very window the binding exists to reach. Hence `T` and `N`, and
hence the flags that move them.

Keys are validated as letters, digits, dashes, and underscores, which covers `G`,
`C-n`, `M-Left`, and `F5`. Anything with punctuation tmux reads as config syntax
is refused; writing that binding by hand in the printed file is the documented
way to have it.

