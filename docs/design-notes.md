# Design notes

Why treewright behaves the way it does. `README.md` is the user-facing tour and
`CLAUDE.md` is the map of the code; this is the reasoning behind the behavior —
the alternatives that were tried, and what made them lose.

Most of it also lives as comments next to the code it explains. Where the two
disagree, the code comment is the one being maintained.

Two subsystems are large enough, and separate enough, to be read on their own:

- **[`tmux.md`](tmux.md)** — sessions, window identity, terminal titles, and the
  key bindings that reach treewright from a window running an agent.
- **[`agents.md`](agents.md)** — the agent-state protocol, the agent modules and
  the plugin they install, and the kickoff prompt.

A fourth file is not a subsystem but a commitment about all of them:
**[`compatibility.md`](compatibility.md)** — which of the surfaces described
here are contracts, what a version number promises about them, and how a break
is made when one is needed. Where the two disagree, this file is the definition
and that one is the promise.

What stays here is everything else: what a worktree and its branch are called,
what the commands promise their output looks like, what refuses to run, and how
a repository is configured.

---

## The base checkout

`resume` and `cd` list the main checkout above the worktrees, under the branch it
is parked on. It belongs there on both of the list's own terms: it is where you
land between worktrees — investigating, reviewing a pull request, asking an agent
to start the next piece of work — and, since a tmux session does not survive a
reboot while a checkout on disk does, it is something you reopen. Left out, the
one window that is always there, and that keeps the session alive, was the one
window the resume key could not reach.

It is not a worktree, and nothing pretends otherwise. In the code it is a
`choice` with a `Base` flag rather than a synthetic slug, because a fake slug
means nothing to `cfg.DirFor` and the first command to forget the difference
would be one that deletes something. `rm` and `prune` work off the worktrees
treewright created, so neither can name it; `ls --json` flags its row with
`"base": true` for anything reading the listing to decide where work should go.
Name it `base`, or name the branch it is on — exact matches only, since
stretching prefix resolution here would let a `b` that used to mean the `bugfix`
worktree quietly start meaning the base checkout.

**Two ways in, one window.** `tw base` opens it fresh with `command`; picking it
out of the resume menu resumes it, the same "carry on where I left off" every
other row gets — which after a reboot is the point. What it carries on is the
base window's own conversation where one is recorded, and `resume_command`
otherwise; see "A scratch session outlives its window" for why the directory
alone stopped being enough. The difference
shows only on the first open, since every later call finds the window by its
directory and switches to it. It also gets what every other row gets when there
turns out to be nothing to carry on from: `command` behind the failure, and
`--fresh` to ask for it outright.

One window *for the directory*, that is. The base checkout can have other agent
windows standing in it — scratch windows, below — and none of them is a third way
in: a scratch window claims no directory, so the window `base` finds on the main
checkout is always this one, however many stand beside it.

**Its status is `base`, outside the safe-to-remove scale.** A base checkout
sitting level with origin has no commits outside it, which would read as
`merged`: the green that means "safe to delete", about the one directory that
must never go. Its divergence column still means something, and something
slightly different — for the checkout parked on the base branch, it is how far
behind origin you are, and so whether what you are reading there is stale.

**In a repository with no worktrees yet**, the menu is that one row with "start
one with `prefix + N`" printed above it; `ls` prints nothing, and `ls --json`
still carries the base row. Three answers to one state, because the three are
for different readers.

A menu is a way through, and must offer the base checkout exactly when there is
nothing else to offer. A table is an answer read by a person, and "no worktrees"
is the answer — printed on stderr, where it cannot be mistaken for a row. The
JSON is a schema, and a row that appears only sometimes is not one: a consumer
deciding where a piece of work should go reads row 0, and making it check first
whether row 0 exists pushes the special case into every reader.

An empty array cost exactly that, and worse: it was read as "this repository is
not registered", which sent its reader through `--help` and the config files
looking for a registration that was already in place. That state was never
ambiguous — an unregistered repository exits 1 with `no registered config
matches repo <path> (have: …)` — so the fault was not an unanswerable question
but one schema with two shapes.

## Scratch windows

**A worktree's window is identified by its worktree. A scratch window is
identified by itself.**

A repository often needs a second agent session that needs no worktree and will
commit nothing: an investigation, a question, a pull request to review, an agent
orchestrating the ones in the worktrees. The base window cannot be it — `base` is
one window by design, and a second call switches to the first. A second config
for the same repository does not help either, because window lookup spans every
session on the server and finds the existing window wherever it is. The
workaround, a throwaway worktree per question, was rejected: a worktree holding
no branch work is a teardown chore invented for nothing.

What makes a worktree expensive is the disk, not the identity. Until scratch
windows, the two were one thing: `@treewright_worktree` was simultaneously where
a window stood and what it *was*. A scratch window separates them. It stands in
the base checkout, and it answers to its own name, recorded on it as
`@treewright_scratch`. It keeps `@treewright_worktree` too, set to the main
checkout, because that option is the path a window is on as well as its identity
— which keeps `Stamped()` true and the `!` waiting marker working on it.

**It claims no directory at all.** The pane listing that maps directories to
windows skips both of a scratch window's claims — the directory its pane stands
in and the one it was opened on. That one rule is what keeps `tw base`,
`close base`, `send base`, `ls` and `restore` correct without any of them knowing
scratch windows exist: every one of them asks for the window on the main
checkout, and the only window that can answer is the base window.

Skipping is the only version of this that is true. Giving scratch windows a low
rank would not do it: a window takes any directory nobody else has claimed, and
rank is consulted only on a collision, so a scratch window would win the main
checkout whenever the base window was closed — and `base` would switch to it
instead of opening the window it was asked for. Leaving the worktree stamp in
play at its usual rank is worse again: a tie with the base window, broken by
session and then by age, which the base window wins until the day it is closed
and reopened.

**They have an index of their own.** `tmux.Scratch` maps a repository's scratch
windows by name, beside `tmux.Windows` rather than inside it. One map with two
kinds of key is a type that lies about what its keys are, and — the reason that
matters more — the consumers that must never be handed a scratch window (the base
window, `restore`, `signal`'s directory fallback, the guard's view of the
worktrees) cannot grow a dependency on them by accident while this is the only
way to get one. It is keyed by repository rather than by session, because a
scratch name means something only within one repository, and a session is not a
repository's alone once `tmux_session` points two configs at it. In the code a
scratch window is a `choice` with a `Scratch` flag carrying its window, for the
reason the base checkout is a flag rather than a synthetic slug: the first
command to forget the difference is the one that reaches the wrong window, or
deletes something.

**One namespace, and a collision is refused when the name is given out.** `send`,
`close` and `resume` take a worktree's slug and a scratch window's name alike, so
`tw scratch <name>` refuses a name that is a live worktree's slug or a scratch
session's — open, or recorded and waiting to be reopened, unless `--reuse` asks
for that session — and `new` and `move` refuse a slug that is a scratch
session's name. The base checkout's own names
are refused as well: they win every lookup, so a scratch window called `base`
could be opened and never reached. Checking once, where a name is handed out, is
cheap; the alternative was a sigil marking scratch names (`:ask`), which is
punctuation typed for the life of the tool to settle a question that arises
once. Lookups take a scratch name exactly
— never a prefix, for the reason the base names do not, since a window opened
today would quietly change what a prefix typed yesterday meant — and an exact
scratch name is tried before a worktree prefix, exactness beating a prefix being
the rule among worktrees already.

**The name is required, and follows a slug's rules.** Every other command that
creates something takes a name, and an addressable thing should be named by
whoever will address it: a generated `ask-1` you have to look up before you can
`tw send` to it is worse than typing a word. What can be typed at `scratch` is
what can be typed at `new`, so a name is checked by `refname.CheckSlug`.

**It opens a new window**, running `command` — never `resume_command`, since a
window that did not exist a moment ago has nothing to resume — with `--prompt`
and `--prompt-file` filled through `fillPrompt` exactly as `new` fills them.
Without tmux it does what `base` does, running the command in the terminal you
are in, rather than inventing a third answer. This used to say *always*, and
the one exception is asked for by name: `--reuse`, under "Coming back to a
scratch session by its name".

**`ls --json` flags a scratch row with `"scratch": true`**, beside the base row's
`"base": true`, and lists scratch rows under the base row: a consumer deciding
where work goes still reads row 0 and is never handed a scratch window by
mistake. The row's slug is the name — what `send`, `close` and `resume` take —
and it carries no branch and no divergence. It stands on the base checkout, and
repeating the base row's numbers under it would be noise.

**Two things that used to follow the directory now ask the pane.** `signal`
stamps the window the agent is running in when treewright opened that window for
this repository, and only otherwise falls back to the checkout it stands in —
without which a scratch agent's `waiting` lands on the base window, `!` and all.
That is a fix rather than a feature: the base window's shell walked into a
worktree by `tw cd` and running an agent there was already stamping the
worktree's window. And `fresh-base` waits for any other agent working in the base
checkout, since a session starting in a scratch window can now find the base
window's agent mid-edit — see `fresh-base` under "Optional behaviors".

The guard is deliberately unchanged. It already leaves the base checkout
unguarded — its foreign worktrees are the managed ones, which exclude the main
checkout — so two agents standing in it is a likelier version of an existing
hazard rather than a new kind, and the rules already describe a scratch agent
correctly: it stands exactly where the base agent stands.

### A scratch session outlives its window

A restart takes the window and leaves the conversation, and the name was always
meant to be the handle back to it. So each agent standing in the base checkout
has its conversation recorded; `resume <name>` reopens a scratch window whose
window is gone on that conversation, `restore` reopens every one of them, and
`ls` and the menu list one with no window, as they list a worktree whose window
is closed. `resume <name>` for a name nothing answers to still says so, and
names `tw scratch <name>` as the way to a new one.

**The record covers the base window too, because the hole started there.**
claude's `--continue` resumes the most recent conversation *in a directory*, and
a scratch session is a conversation in the base checkout — so from the first
scratch window on, a `resume` or `restore` of the base window could pick up the
scratch conversation rather than the base window's own. The directory was an
exact key for a conversation exactly as long as one agent stood in each. A
record kept for scratch windows alone would have been half a fix: the base
window would still resume with `--continue`, and still choose between itself
and every scratch session that ran after it. So the base window resumes on its
own record whenever it has one, and `--continue` stays the fallback in exactly
two places — a base window with no record yet, and every worktree, where one
agent per directory keeps the directory exact.

**The agent writes it, at the moment it knows.** The record is the
conversation's `session_id`, and the one party that has it is the agent: its
`SessionStart` hook is handed the id as the session begins. That hook already
ran `treewright session-start`, so recording needed no new hook and no plugin
copy rewritten — which is the property `session-start` was built as a single
dispatch verb for. It records for a window treewright opened for this
repository, found through the pane as `signal` finds its window, when that
window is the base window or a scratch one and the agent is standing in the base
checkout. `/clear` starts a conversation under a new id and fires `SessionStart`
again, so the record follows what the window is running rather than pinning the
one it opened with. An id minted by treewright when the window opened was the
alternative, and `/clear` is what sank it: the pin would have pointed at the
conversation before the clear for ever.

**It lives at `.git/treewright/sessions/<name>`**, `base` for the base window
and the scratch name otherwise, holding the id and nothing else.
`.git/treewright/` was already treewright's, with post_create's logs and
`move`'s patches in it; the record is per checkout, never committed, gone with
the repository, and findable by anyone who wants it gone. This directory was
first reserved as `scratch/`, in the same breath as saying the record had to
cover the base window too — which the name then contradicted.

**What ends one was settled on the binary, not on its documentation.**
`close <name>` ends a scratch session, window and record both, and ends one
whose window is already gone. The other ending is quitting its agent, and there
the obvious rule — remove the record on `SessionEnd` — would have undone the
feature. claude (2.1.283) ends a session with reason `other` when its process is
killed, and `tmux kill-window`, the tmux server going away and a `SIGTERM` all
count: a reboot is that ending, delivered to every agent at once. So only
`prompt_input_exit` — `/exit`, a double Ctrl-C — ends a record, and only a
record still naming the session that is ending. `/clear` reports `clear` and
starts the next session in the same window, which rewrites the record rather
than needing it removed. `SessionEnd` was already wired to `signal clear`, which
now reads the hook's payload for this, rather than a hook of its own that no
installed copy would have. The reason vocabulary is the module's, as its
`QuitReason`.

**The base window's record is never removed.** Quitting the base agent is not
forgetting its conversation — the next `resume base` should carry that one on,
as `--continue` carries on a worktree whose agent was quit — and closing the
base window is not either. The record changes only when the base window starts
another session.

A record left behind by a kill, a server death or a reboot is correct: the agent
did not finish, so its session comes back. What accumulates instead is a
scratch session somebody stopped caring about and never quit, which `restore`
reopens once and `close` ends for good.

**Its name stays taken while it is recorded.** `scratch` refuses a recorded
name, naming `resume` to reopen the session and `close` to end it — short of
`--reuse`, which reopens it — and `new` and `move` refuse one as they refuse an
open scratch window's. A second window under
the name would be a second session answering to one word — and the first time
its agent started, its record would overwrite the one it collided with.

**Resuming by id is the module's, not a setting.** The id form is
`claude --resume {session} {prompt}`, a field on the claude module rather than a
third config key: `command` and `resume_command` are settings people override
for their own reasons, and a key they had to know about to keep resume exact
would be a trap. It applies while `resume_command` is the module's own, compared
by value. A config that wrote its own — claude with a model pinned, say — has
flags in it the module's template knows nothing about, and the right
conversation resumed without them is a different agent from the one the file
asks for; so that config keeps its own command, keeps no records, and resumes
the base window by directory as before. With no `agent` key there is no module
to ask, for the reason a module is never guessed from `command`. A recorded id
with nothing behind it — a transcript cleaned up, a session that never said
anything — makes `--resume` exit 1 within a second, which is the fast failure
the resume fallback already runs `command` behind.

**Why a record is allowed when restore's snapshot was not.** "Putting a session
back after a restart" turns down saving the session: a snapshot of the layout
would have to be rewritten by every command that opens or closes a window, and
would drift from a session people rearrange by hand all day. This is not that.
It has one writer — the agent whose conversation it names — writing at the
moments that change the answer, and it says nothing about layout: which windows
a session should have is still read off the disk. Nor is it the agent's own
session storage, which "When there is nothing to resume" in
[`agents.md`](agents.md) turns down for being a layout treewright does not own:
the id arrives in a documented hook payload, and `--resume` is a documented
flag.

### Coming back to a scratch session by its name

**`scratch --reuse <name>` reaches the scratch session already answering to the
name instead of refusing it.** An open window has the prompt typed at its agent
and is brought forward. One whose window a restart took is reopened on its
conversation. With no session by that name, one is opened as `scratch` opens
one. The name becomes a handle a caller keeps, rather than one it is handed
once.

The caller that asked for it wanted one agent per pull request review —
`review-42`, opened on `/review-pr 42` — with a re-requested review landing in
the conversation that did the first. Outside treewright that took `ls --json`, a
`jq` filter, and a branch to `scratch`, `resume --prompt`, or `send` and then
`resume`: about a second in a repository with half a dozen worktrees, three
invocations where one would do, and state free to change between them. Every
caller wanting a named agent it can come back to would write the same branch,
and getting it right takes a fact few of them will know. `send` and `resume`
try a scratch name exactly, but fall through to a worktree's prefix when there
is none, so a blind `resume --prompt … review-4` hands the prompt to the agent
in `review-42`. The `ls --json` round trip was there only to avoid that.

**On `scratch`, because that is where a scratch name is handed out**, and it is
the one command that never matches a name by prefix. A new verb would have
been a second command taking `scratch`'s arguments and restating its refusals.
An exact-only mode on `send` and `resume` would have closed the prefix trap and
left every caller writing the three-way branch, three calls and all.

**A flag rather than the default**, because the refusal it gets past is a
person's protection as well as a namespace rule. `scratch ask`, typed by
someone who has forgotten an `ask` is open, is a new question. Typing it at the
old conversation hands that agent a prompt meant for nobody there. The flag is
the caller saying the name means the same agent every time.

**It reaches a scratch session and nothing else.** The base checkout's names
and a live worktree's slug are refused first, exactly as they are without the
flag, and only then is an existing scratch session something to reuse. That
order also covers a slug that collides with a scratch name because a worktree
was made by hand, which the lookups would otherwise settle in the scratch
session's favor.

**An open window is typed at through `send`'s own path** (`deliver`, in
`send.go`), not a copy of it. So all of `send`'s protections come along: the
pane shown before anything is typed, the refusal of the caller's own window,
and the refusal of a window held open after its agent died. A tmux popup has
no `$TMUX_PANE`, so a caller running in one has no window of its own to be
refused. With no prompt there is nothing to type, and reaching an open window is
what `resume` does: switch to it, held-open or not, since going to a dead
agent's window is how its output gets read.

**What a call can be refused for does not depend on which case it meets.** A
caller passes `--reuse` precisely because it does not know whether a window is
open. A call that worked whenever none was, and failed the day one was, would
be the dependence on unseen state the flag exists to remove. So a prompt with a
line break in it is refused in all three cases, although only the open window
types it, and Enter only submits there. `--prompt-file` is the way through,
since what it builds is one line. `command` is filled in all three as well, so
a prompt the repository's template has no `{prompt}` for, or one too long for
tmux, is refused the same way whether or not the template would have run.

**A reopened window takes the window name passed on this call.**
`reopenScratchWindow` otherwise names a window without the override, on the
argument that an override is a fact about the window rather than the session —
so it went with the window, and `resume` and `restore`, which have no override
to give, still name the window by its session. That argument holds here too.
Nothing is stored, and the window `--reuse` reopens is this call's window, so
the name is this call's. A `--reuse` caller passes the name on every call,
which is what makes that the name the window should have. A window already open
keeps the name it has: this call did not open it, and tmux names are a
person's to change.

**Without tmux, it is `scratch` without tmux, one step on.** No window can be
open. A name with no session runs `command` in this terminal, as before. A
recorded session runs its conversation here. That beats a fresh agent, which
would leave the conversation that was asked for behind.

**The client follows `scratch`'s rule**, `arrivalFor`: brought to the window,
unless `--repo` names another repository. **stdout stays empty**, and under
`--reuse` one line on stderr says which case it met — `opened scratch session`,
`send`'s own pane and `sent to` report, `scratch window … is already open`, or
`reopened scratch session … on its recorded conversation`. A plain `scratch`
says nothing new, since it can only have done the first.

What it does not fix is the hazard `send` already has. The pane is shown so the
sender can see an agent sitting on a question before typing an answer into it,
and a caller that discards stderr — the popup that asked for this does — never
sees it. `waiting` is the state `send` exists to reach, so refusing it here
would refuse the case the feature is most often for.

## Putting a session back after a restart

A tmux session does not survive a reboot while a checkout on disk does — the
sentence the base checkout's place in the resume menu comes from. `restore` is
that sentence applied to the whole list at once: the base window, then a window
for each scratch session the restart interrupted, then one window per worktree,
each resuming with `command` behind it, and then this terminal attached to the
session.

The morning it replaces is `tw base`, `tw attach`, and one `tw resume` per
worktree, in a terminal tab per repository. Three repositories with three
worktrees each is fifteen commands to arrive back where you were.

**No layout is saved, and none may be.** The worktrees on disk are the record,
and restore reads them. Recording the live layout and replaying it is the obvious
alternative, and it loses on every count:

- to cover the reboot that actually hurts — the unexpected one — the snapshot
  would have to be rewritten by every command that opens or closes a window;
- it would drift the moment somebody rearranged tmux by hand, which is a thing
  people do to a session all day;
- it could point at a worktree since removed;
- it cuts against the precedent in [`agents.md`](agents.md) that keeps agent
  state on the window and never on disk;
- and it is not even free in the registry: `config.Names` globs `*.toml` and
  `doctor`'s registry check calls anything else a stray, so a snapshot would need
  a directory of its own to live in.

What is kept is narrower and of a different kind: which conversation each agent
in the base checkout is running, written by that agent's own hook. It is how the
base window gets its own conversation back rather than the latest one in the
directory, and how a scratch session lost to the restart is known about at all —
see "A scratch session outlives its window" above for why it passes where the
snapshot did not.

So what you get is a **tidied session rather than a photocopy** of the one you
lost: the base window first, then its scratch windows by name, then the worktrees
in slug order, without your window order or your splits. The name risks promising the photocopy, which is why
the help says this out loud. **What restore opens is what `tw ls` lists**, and
that is also why there is no `--dry-run` — the listing is the preview. A window
already open is left exactly as it is, which makes `tw restore` a reasonable
thing to type in a session that is already up, where it means "open whatever is
missing here". That promise is also why `ls` lists a recorded scratch session
whose window is gone: restore reopens it, and a listing that left it out would
be a preview of less than restore does.

**One repository per invocation.** There is no `--all`. A terminal tab per
repository is the shape of the day anyway, so batching across repositories would
save one command per tab and in exchange would spin up sessions and agents for
every repository ever registered. The optional `[repo]` is the same positional
`base`, `attach`, `ls`, `prune`, `config` and `refresh` take — and the same
repository `--repo` names, which every command accepts; see "Naming the
repository a command acts on". It is there because a terminal tab launches in no
particular directory — see the one-tab-per-repo pattern in
[`tmux.md`](tmux.md).

**It attaches by default, and `-d, --detached` opts out.** The common caller is a
person in a fresh tab who wants to land in their session; the scripted caller is
the exception and can pay for a flag. `--detached` is tmux's own word for the
state it leaves you in — a session running with nobody watching — and it is
literal rather than a metaphor. Not `--no-attach`: there is no `--no-*` flag
anywhere in the tool.

**And it attaches only on a clean restore, which is what makes the report
readable.** The objection to attaching by default is that tmux paints over the
screen before you can read what restore reported. It dissolves: on a clean
restore there is nothing to read, because the report would say "opened five
windows" to somebody who is about to look at five windows — the session is its
own report. So when every window opened, restore attaches at once and prints
nothing. When any window did not, it prints the report, stays out of the session,
and names `tw attach <repo>` as the way in. The one time there is something to
read is the one time you are not swallowed by the session; no pause, and no
"press Enter" gate.

Each failure is also reported where it happened, naming the worktree, and the
worktrees behind it still get their windows. One window failing is not the rest
of them failing, and the ones behind it are exactly what the reader would
otherwise be opening by hand.

**Not a terminal means do not attach, and do not fail.** Outside tmux the attach
is a foreground `tmux attach-session`, which takes stdin and stdout over and
cannot take a pipe. A scripted restore whose author forgot `-d` would otherwise
do every bit of the work correctly and then exit non-zero on the last line, so
the check is treewright's: `term.IsTerminal` on both streams, then the attach
skipped and said out loud. That is the pattern `ui.Picker` already uses for the
same reason. Inside tmux the question is not asked at all — there the attach is a
`switch-client`, which needs no terminal.

**No `--prompt` and no picker.** A prompt broadcast to eight agents is not a
thing, and a command that has to be safe to run from a startup file cannot stop
to ask a question. `--fresh` is worth taking, for symmetry with `resume`: it runs
`command` rather than `resume_command`, a new agent session in every window.

Inside the code, the one thing restore needed that nothing else did is a way to
open a window without moving the client — `arrival` in `session.go`, and
`tmux.Select` under it. Focusing a dozen windows in turn is a `switch-client` per
window, which for an attached client is the session flickering past under their
hands and landing wherever the loop ended. The window it should land on is the
base window, so that one is selected once every window is there.

**A warning that is not a failure is the one thing a restore can lose.** A
worktree whose `post_create` failed is reported as `resume` and `cd` report it,
and it must not hold you out of the session — so on the attaching path it is on
screen for the instant before tmux takes the screen. One line through tmux's own
status bar after you land is the only channel that would survive, and it is not
built: `display-message` needs a client, and outside tmux the attach blocks until
you detach, so saying it afterwards means saying it to somebody who has already
left. It is readable under `-d`, and on the failure path, where nothing attaches.

## Naming the repository a command acts on

Every command resolves a config, and until recently most of them resolved it
from the working directory with no way to say otherwise. That is right for a
person, who is nearly always standing in the checkout they mean, and wrong for
an agent, which stands in exactly one repository and has business in others.
`ls`, `prune`, `base`, `attach`, `restore`, `config` and `refresh` already took
an optional `[repo]`, on the argument that a terminal tab launches in no
particular directory — and an agent is in that position with respect to every
repository but its own, permanently.

The failure it produced was silent. An agent standing in one repository ran
`tw new` for another and got a worktree in the repository it was standing in:
that repository's branch prefix, that repository's base branch, that
repository's directory, under the slug meant for somewhere else. Nothing in
the output contradicted it, because nothing in the output named a repository at
all.

**The repository is named by `--repo`, not by qualifying the target as
`<repo>/<slug>`.** A qualified target is the tidier-looking option and it
cannot be had: a slash already means a branch prefix. `new` reads a leading
`feature/` as one, and `rm`, `close`, `send`, `resume` and `cd` all strip one
before using what is left. So `tw rm feature/eng-1` would have two readings,
and which one applied would depend on the registry rather than on anything in
the repository being acted on — register a config called `feature` and the
meaning of that line changes, in every repository, from "the eng-1 worktree
here" to "the eng-1 worktree over there". That is a deletion moving under
somebody on the strength of a file they did not edit. A flag cannot be confused
with a prefix, a slug or a window name, and it leaves the prefix mechanism
working on the slug: `tw new --repo cibo bug/eng-1` branches `bug/eng-1` in
cibo.

**The flag works everywhere and the positional stays where it was.** These are
not two spellings of one thing so much as a general form and a shorthand: the
seven commands whose only argument is a repository read better with a
positional and have taken one since they existed, while every other command's
positionals are slugs and messages. Accepting `--repo` on all of them is what
makes it learnable as universal, which is the whole point — an agent that finds
the flag rejected by one command has no reason to trust it on the next. Naming
the repository twice is a usage error rather than a precedence rule, following
`--prompt`/`--prompt-file` and the `branch_prefix` pair before them.

`move` is the one command that deliberately has no `--repo`. What it moves is
the uncommitted work in the base checkout, and the only base checkout it can
read is the one the caller is standing in; a flag there would name where the
worktree is made while the work still came from wherever the caller happened to
be, which is either a no-op or a way to put one repository's changes onto
another repository's branch.

### Every message names the repository it is about

`new` says whose base branch it is forking from, in the line that was already
naming the branch: `creating branch bug/eng-1 off cibo's origin/main`. The
possessive puts the repository against the thing that identifies it least
reliably. Reading `creating branch integrate/x off origin/megastructure` and
catching a mistake in it requires already knowing whose base branch
`megastructure` is, which is exactly what the agent that got this wrong did not
know. stdout is unchanged and still carries the worktree path alone.

**Every hint naming a command names the repository too**, in every repository,
including single-repository installs where it is redundant. A slug identifies
nothing on its own: two pieces of work called `fix` in two repositories is the
ordinary case rather than a contrived one. And the reader of a message is not
reliably standing where it was printed — an agent has its own working
directory, a transcript is read from somewhere else, and `rm`'s hint about a
stale window is read after the worktree that would have disambiguated it has
been deleted. In the incident, an operator holding `close it with tw close
eng-1` had nothing that worked and fell back to `tmux kill-window` on a window
index, which is the one thing `close` exists to stop. `hint` in
`internal/cli/address.go` is the one place that spelling is built.

### A window named after another repository

A session is one per repository, so the window list is where a person checks
which repository they are looking at. A window called `cibo` sitting in another
repository's session tells the only reader it will ever have that cibo's work
is here. Nothing misbehaves — treewright targets sessions by `=name` and
windows by id, never by display name — so this is a warning rather than a
refusal, on the same argument `warnIfAgentWorking` is a warning: the caller may
have meant it, and a refusal would need a `--force`, which is a flag people
learn to pass by reflex. The repository's own name is exempt, a window named
after the repository it sits in misleading nobody. `new` and `move` both check,
before anything is created, because a window name is worth changing while
changing it is still free.

## Naming a worktree

`rm`, `resume` and `cd` take an unambiguous prefix of a slug, because a slug
carries both a ticket key and a description while people refer to that work by
the key alone:

```
$ tw cd eng-2318
eng-2318 matches worktree eng-2318-cart-total-rounding
```

Prefix matching is not really about ticket keys, though, which is why it is worth
having where there are none: `tw cd dark-mode` finds `dark-mode-toggle` for the
same reason. What people type is the start of the name, whatever the name is made
of.

The expansion is always reported rather than applied silently — `rm` is on that
list. An exact slug wins over any prefix, so a slug that is a prefix of another
stays reachable by its own name, and an ambiguous prefix is an error listing the
candidates rather than a guess.

Slugs are validated up front rather than left to git, so the answer is one
sentence naming the slug instead of several lines of git's advice about ref
formats arriving three steps deeper — by which point treewright has already
announced what it was about to do. A slug may not contain `/`, because a nested
one leaves a stray parent directory behind when the worktree is removed; the rest
of the rules are `git check-ref-format`'s.

## Naming a window, with or without a ticket

A window name goes in the tmux status line beside every other window's, so it has
to be short. A ticket key already is; a description is not, and `ticket_pattern`
exists to prefer the key when the slug carries one.

Everything else here works the same either way — the worktree is named after the
slug, the branch after the prefix and the slug, and `resume`, `cd` and `rm` all
take the slug — so the window name is the *only* place a repository that tracks
no tickets was treated as a degraded version of one that does. It was treated
that way twice.

**The pattern matched things that were not keys.** `(?i)^([a-z]+-[0-9]+)` let the
digits end anywhere, so `fix-2fa-login` — work on two-factor login — opened a
window called `fix-2`. The fix is to require the key to be a whole word, `(?:-|$)`
at the end, which costs a real key nothing: a key is followed by the description
or by the end of the slug in either case. What survives is genuine ambiguity —
`refactor-2-pass-parser` is a ticket key to any pattern that does not know the
scheme — and no regexp settles that, because the answer is a fact about the
repository rather than about the string.

So the answer is a setting, and the setting is the pattern itself:
`ticket_pattern = ""` means there are no tickets here, stop looking. It needs no
second key, and it reads as what it is. The catch is that `Load` fills every
other empty string in the file with a default, which would make the opt-out
unwritable; it therefore tests `Explicit("ticket_pattern")` rather than the
value, so an omitted key defaults and a key set to `""` stays empty. A
never-matching regexp was the alternative — undiscoverable, and RE2 has no
negative lookahead to write one with.

### An empty command, and the one key that overrules it

The command keys have the same shape and reached it later. `tmux.Spec` has
always documented a blank command as "leaves a shell", and `newWindow` omits a
blank rather than passing an empty string — so the plumbing for a window with no
agent in it was there the whole time, and no config could ask for it. `Load`
defaulted on the value, so `command = ""` came back as `claude {prompt}`, and
`treewright config` — the command whose entire subject is the gap between a file
and its behavior — reported that value with the FROM column blank, meaning *the
file's own*. About a file that said no such thing.

Both command keys now default on `!Explicit(...)`, so `command = ""` opens the
window on a shell and only a file that never mentions the key takes `claude`.
The half-deleted-line worry that argued for the old collapse does not survive
contact with the failure it produces: a window holding a shell is visible the
moment it opens, where a wrong `ticket_pattern` is invisible for weeks.

**`agent` is the exception, deliberately.** It fills a blank command however the
blank got there, so `agent = "claude"` with `command = ""` runs claude, and a
repository that wants the shell removes the agent key too. The alternative —
carry claude's settings and plugin into every worktree, then open a shell — is a
combination nobody asked for, and supporting it would make one key's empty value
mean two different things depending on whether another key is present.

That exception costs one piece of bookkeeping. Once the module has filled a
blank, an explicit `""` and an absent key are the same value under two different
answers from `Explicit`, so `setup --refresh` cannot tell whether to write the
line back. `Config.AgentFilled` records the fill for exactly that reader:
a value the module supplied follows treewright's changes to that module, and the
identical string written into the file stops following them — the
default-becomes-a-setting trap every other key here is guarded against.

The old generated config carried the other half of this. "Remove this key for a
window with no agent in it," it said about `agent`, which was false: removing it
stopped the carry and the module's defaults, and left `DefaultCommand`, which is
`claude {prompt}`. You got claude either way. It now says to set `command = ""`
as well, which is the thing that is actually true.

**The fallback spent characters saying characters were missing.** At the
ten-column cap this was written for, `rewrite-css` — eleven characters — arrived
as `REWRITE-CS…`: eleven columns again, one of them spent to report that a
character was missing. So `shorten` keeps a shortened name only when it is
genuinely narrower than what it replaced. The cap has moved since and the guard
has not; at fifteen the slug it hands back whole is `dark-mode-toggle`.

**The cap is fifteen, and the status line is what sets it.** Several of these
names sit side by side there, so a name that fits is worth more than a name that
is whole — that part has not changed. The number has. It was ten on the argument
that a ticket key is ten wide and a description should be held to the same, which
took a key's width for a budget rather than for the length keys happen to be. A
description is not a key: it is read for what the work is, and cut to ten it
mostly reported that something had been cut. Fifteen is what one needs to survive
being read at a glance.

**The cut is blunt, and the wider cap is not licence to revisit that.** Cutting at
a word boundary has to give back a whole word to find one, and the names it takes
that word from are the ones only just over the cap — where the blunt cut runs into
the guard above and hands the name back whole. `dark-mode-toggle` is that case:
sixteen characters, intact, where a boundary rule would spend six that fit to
arrive at `dark-mode…`. Widening makes it more common rather than less, since
fewer slugs are cut at all and a larger share of the ones that are sit just over
the line. What the boundary rule buys in exchange is two columns and a stranded
letter on a long name — `flaky-payment…` where the blunt cut gives
`flaky-payment-t…`. The one thing kept from it is a trailing hyphen trim, since a
cut landing after one leaves it against the mark: `checkout-total-fix` as
`checkout-total…` rather than `checkout-total-…`.

**The name keeps the case it was typed in.** It used to be uppercased —
`ENG-2318`, `MAIN`, `FLAKY-PAYM…` — on the reasoning that a status line reads
better with one shape in it. What that cost is a window spelling its own subject
differently from everywhere else the subject appears: the slug in `tw ls` and in
every command that takes one, the branch in git, the key in whatever tracker it
came from. The key is where it showed most, because `ticket_pattern` matches
case-insensitively by design — a team whose keys are lower-case got them back
shouting, and a team whose keys are upper-case could not tell whether treewright
had read the shape or imposed it. Which of the two a person sees is a fact about
their tracker, and following what was typed is the rule that needs no explaining.

The mark is `…` rather than `...`: one column instead of three, in the one place
where columns are the whole problem, and three of a fifteen-column budget is a lot
to spend. That makes the calculation a matter of runes rather than bytes — `refname`
forbids control characters and git's own metacharacters, not the rest of Unicode,
so a slug may legitimately hold multi-byte characters, and a byte-wise cut would
misjudge the width and split one down the middle. `ui.Table` already measures in
runes, so the table stays aligned. `popupSize` still measures in bytes and so
allows two columns more than the mark needs, which is the direction it errs on
purpose.

None of it is load-bearing for anyone who disagrees: `tw new <slug> <window-name>`
has always named a window outright, and remains the answer for the one worktree
whose name does not fit whatever rule is in force.

## Branch prefixes

What you type is `[prefix]slug`: a leading run matching a configured prefix names
that prefix, and the rest is the slug. Under a single `branch_prefix` that is just
the correction it always was — `tw new john/eng-2318` gives `john/eng-2318` rather
than `john/john/eng-2318`, and treewright says it stripped a copy. Only one copy:
if you really do want a slug named `john/eng-2318` under prefix `john/`, stripping
repeatedly would make it unreachable.

Under `branch_prefixes`, the same rule is how you choose. Teams that namespace by
kind of work rather than by person list several — `["feature/", "bug/", "chore/"]`
— and `tw new bug/eng-2318` branches `bug/eng-2318`. A bare slug takes the first
in the list, which is why the list's order is the setting and not an accident of
it. The longest match wins, so `feature/` and `feature/exp/` can coexist. A prefix
the list does not contain is an error naming the ones it does, rather than a new
namespace invented on the spot: a branch pushed under a misspelled prefix looks
fine locally and is invisible to whatever the team's tooling watches.

One flat list, and no composition: someone who wants `alice/feature/` writes that
literally. Composing a personal prefix with a kind-of-work prefix would be a rule
to learn in a file whose whole point is that it is data.

The prefix reaches the branch and stops there. The worktree stays
`<repo>-eng-2318`, the window stays `eng-2318`, and `resume`, `cd` and `rm` still
take the slug alone. Folding the kind of work into the directory name was the
alternative, and it loses on all three of its own terms: `ticket_pattern` stops
matching (`feature-eng-142-white-screen` has no ticket key at its head, so the
window falls to a cut description — `feature-eng-142…`, which spends more than
half its width on a word every other window has too), every row of the table grows
a word that repeats down the column, and you would have to remember which kind of
work something was to reopen it — a question git already answers. Two worktrees whose
slugs are both `auth` therefore collide whatever their prefixes, and `new` says so
and names the command that opens the one that exists. Slugs that carry a ticket
key never reach that case, and two pieces of work that really are both called
`auth` are ambiguous to the person reading the list too.

Because the prefix is only ever *written* at `new` — every other command reads the
branch back from git — a worktree's branch does not have to match what its slug
would produce today. Renaming a prefix in the config leaves the worktrees created
under the old one working, listed, and removable.

The two spellings are one setting, and a config may not use both. Any precedence
we picked would be a rule to learn, and the file itself would no longer say which
prefix a branch gets. `config` reports the row under whichever spelling the file
used, so the line it names is the line that is there.

Prefixes are checked when the config is read, not when a branch is created — the
same restatement of `git check-ref-format` that validates a slug, in
`internal/refname`, so the two halves of a branch name are held to one set of
rules. A prefix is hand-written, often several at a time, and the alternative is
git refusing a branch three steps into a `new` that has already announced what it
was doing, about a value the user last looked at when they wrote the file. Because
every command loads the config, `doctor` reports it too, naming the offending entry
rather than the key.

Both checks are tested against the real `git check-ref-format`, which is the only
way a restatement stays honest: reading the rules off the documentation had a
component ending in a dot wrong (`feature./eng-1` is legal, and looks the least
legal of any of them). Two divergences are deliberate and declared in the tests: a
slug may not contain `/`, because of the stray parent directory, and neither half
may start with `-`, which git allows in a ref and every command that takes flags
does not.

### Guessing them at setup

`setup` counts the leading namespace of every branch on origin — not of the local
branches, so a fresh clone with none of its own still sees the convention — and
proposes the ones that name a kind of work, most used first. The most used becomes
the default a bare slug gets, and the counts are printed beside the list so the
ordering the config depends on is visible rather than asserted. Two branches make
a namespace worth proposing; one is an incident.

Only names from a built-in vocabulary (`feature`, `bug`, `hotfix`, `chore`, …)
qualify, and that is the load-bearing decision. `alice/x, alice/y, bob/z` and
`feature/x, feature/y, bug/z` are the same shape, so counting alone cannot tell a
per-person scheme from a per-kind one — and mistaking the first for the second
writes colleagues' names into your config as kinds of work, then sends every branch
you make into somebody else's namespace. The opposite mistake costs nothing: an
unrecognized scheme leaves the git-email guess and a commented example, which is
where this started. So the vocabulary is a floor, not a filter — a team using
`squad-a/` writes it in, once.

Only `/` delimits a namespace. `feature-eng-1` and `eng-142-white-screen` are the
same shape too, and reading `eng-` as a namespace would be wrong far more often
than right. A dashed convention is a one-line edit; a wrong guess is a branch
pushed somewhere nobody is looking.

What origin says beats what the git email says, including when origin yields a
single prefix — a repo where every branch is a `feature/` means new work is a
`feature/` too. The email guess exists to make branches attributable on a shared
remote, and a repo that already namespaces has answered that question its own way.

**And the generated file says which of the two it got.** A prefix read off
origin's branches is a convention observed; one derived from a git email is a
guess about a person made from an address that need not be about a person —
`codeberg@example.org` yields `codeberg/`, a namespace nobody would choose,
which is how the guess is usually met. Written with the same confidence as
`main_dir`, under a header claiming everything below was detected, it reads as
something treewright found out. So the header distinguishes the two kinds of
value and invites a read, and the email-derived prefix carries its provenance on
the line above it, where a reader checking one setting will actually see it. The
guess itself stays: attributable branches on a shared remote are why it exists,
and the fix for a wrong one is a one-line edit in a file that is meant to be
edited.

## Creating a branch

`new` reuses a branch that already exists rather than recreating it, which is
also how you get a worktree onto a colleague's pull request after fetching it.
Branches always fork from `origin/<base_branch>` — there is deliberately no flag
to base one on anything else, the point being that every worktree starts from the
same known-current place. When the fetch fails, `new` says so and forks from the
local base branch — see the next section for what it says and how long it waits
before saying it.

**A base checkout ahead of origin is warned about**, because that same rule is
what makes it invisible: commits made in the main checkout and not yet pushed are
not in the new worktree, and neither are the files they added. Nothing is wrong —
the fork point is the one treewright promises, and pushing is the user's call —
but the discovery otherwise arrives as an empty file three steps into the work,
looking like anything except a fork point. So `new` counts the local base branch
against `origin/<base_branch>` on the path that forks from origin, says what it
means for the worktree just made, and names the two ways out: push and recreate,
or cherry-pick the commits over. It is the branch that is compared, not whatever
the checkout has out — that is `base`'s question, and it asks it separately.

### When the fetch fails

A `new` once reported `origin unreachable`, forked from the local base branch,
and was right about the fork point by luck. The same `git fetch --quiet origin
main` run by hand immediately afterwards succeeded twelve times out of twelve at
about three seconds each, `doctor` reported `ok origin`, and the session-start
`fresh-base` fetch had succeeded half an hour earlier. Establishing that nothing
was wrong took an investigation, and three separate things made it necessary.

`doctor` agreeing was not a fourth. Its origin check is `git remote get-url` and
a `RefExists` on `origin/<base_branch>` — both local, both answered out of this
checkout, neither reaching the network. That is the right check for what
`doctor` is for, and it means `ok origin` was never evidence about the fetch;
the fetch is the only thing here that asks the network anything.

**The message asserted more than it knew.** `Fetch` returns an error for any
failure: a `base_branch` that is not on origin, credentials that expired, a
repository that was renamed, a `git` missing from PATH, a deadline. None of those
is unreachability, and naming the network sends the reader at the one part of the
system that is working. So the warning now says what it did — `could not fetch
origin/main — forking from the local main instead` — says what that costs, and
then quotes git under a `git said` field. `fresh-base` already hedged this way
about the identical failure, and this is the same register.

That quote is git's, verbatim: its capitals, its full stops, its advice
paragraph. The house voice governs treewright's own sentences, and a field
holding a foreign program's output is a value, not prose — paraphrasing it would
be the guessing the change is meant to end. The one thing `git.Said` does change
is dropping git's blank lines, because `asFields` pads a value's later lines to
the value column and a blank one arrives as a run of spaces under a label,
reading as the message having stopped there.

**Nothing bounded the wait.** The runner was a plain `exec.Command`, so a network
that black-holes rather than refusing — a captive portal, a dead VPN, a stalled
TLS handshake — hung `new` with no worktree, no window and nothing on screen. That
is a worse failure than the one being fixed, and `doctor`'s release check already
holds the opposite standard for the same reason. The deadline is on the fetch
family and not on the shared runner: every other call here is local and answers
in milliseconds, so a limit on `worktree add` or on the object-walking
`commit-tree` behind squash-merge detection would be a limit on work that is slow
only because the repository is large — and its expiry would be indistinguishable
from the network failure this one reports.

Thirty seconds, against a warm fetch that measures about three. The headroom is
the whole point: every caller answers a failed fetch by carrying on from
something older, so a budget tight enough to expire on an ordinary slow link
would not report a problem — it would hand back a stale fork point and call it
success, which is this bug inverted. The deadline also sets `cmd.WaitDelay`,
without which it would be advisory: `git fetch` runs its transport in a child
process that inherits the pipes treewright reads git's output through, so killing
git leaves them open and the wait moves one process down rather than ending.

**One failure decided the outcome.** A blip that a second attempt would have
answered leaves a branch forked from a stale base, discovered at merge time or
not at all. So `FetchRetrying` asks once more, and only the two callers where a
failed fetch changes *what the user gets* use it: `new`'s fork point, and the
base checkout `fresh-base` reports as current. The four housekeeping fetches in
`rm` and `prune` stay on `Fetch`, because a stale `origin/<base>` there only makes
`IsMerged` say no — `rm` refuses, `prune` skips — and that is the safe direction
already. Retrying would add latency to teardown to reach an answer nothing acts
on.

One retry, not a loop, on the argument `session.go` already makes about falling
back to a command that has just failed. And never after a timeout, which is the
part worth spelling out because it looks backwards: a stalled handshake is the
most transient-looking failure of the lot. But the budget has already been spent
establishing that nothing is answering, and spending it twice doubles the wait
for exactly the case the offline fallback exists to reach quickly — an offline
laptop must not come back slowly with a warning about the network it is not on. A
fast, definite refusal is the opposite trade: it cost nothing to obtain, so
asking again costs only the backoff, and that is a cheaper mistake than never
covering the blip at all. The rule is one line, `worthRetrying`, so that the
distinction is a named thing rather than a condition inside a loop.

## Moving work that was started in the wrong place

Typing in the main checkout and then realizing the change wants a branch of its
own is ordinary. What makes it dangerous is that the work is uncommitted: until
it is somewhere else, the base checkout is the only copy of it, and the sequence
that moves it has a verification gate in the middle whose entire purpose is to
be passed before anything is thrown away.

That sequence lived in the claude module's guide as six commands in a strict
order, and the order *was* the safety. Prose cannot hold that. An agent
improvising anywhere in it loses work, and the improvisation to expect is
`git clean -fd` for the last step — which reads as "delete the untracked files"
and means "delete every untracked file", including the ones this move never
touched and any ignored file sitting inside an untracked directory. So it is
`tw move <slug>`, and the order is in the binary.

**The base checkout is the last thing touched and never the first.** What runs,
in order: list the untracked files, mark them intent-to-add so they reach a
diff at all, write `git diff HEAD --binary` to `.git/treewright/move-<slug>.patch`,
put the index straight back, make the worktree exactly as `new` does, apply
with `--3way`, check the result, and only then clear the checkout the work came
from. Every failure before that check leaves the checkout as it was found, says
so in those words, and names the patch — which is a second copy of the work and
the way in by hand.

Four details carry more weight than they look:

- **The index goes back immediately**, not at the end. From the moment the patch
  is written the checkout is byte for byte what it was, so every later failure
  is honest without a cleanup path of its own to get wrong.
- **It goes back by path.** A bare `git reset` would also unstage whatever the
  user had staged themselves, which is theirs and none of a move's business.
  `git reset -- <the untracked files>` undoes exactly the intent-to-add entries
  treewright wrote.
- **The check is `git diff HEAD --stat`, not `git diff`.** `--3way` applies
  through the index, so the work arrives staged and a plain `diff` has nothing
  to show — which reads exactly like a patch that never applied. Verifying with
  the wrong command is worse than not verifying: it is a gate that opens on
  failure.
- **What gets deleted is read from the diff, not from the untracked listing.**
  `git diff HEAD --name-only --diff-filter=A` is every path the patch creates,
  which includes a file the user had already `git add`ed — one the untracked
  listing does not mention and which would otherwise survive as a second copy.

Files git ignores stay where they are. They are what `carry_files` copies into
every worktree, so a move that swept them up would take the `.env` out of the
checkout every future worktree is carried from. Empty directories are left
behind too, where a deleted file was the last thing in one: removing directories
is precisely the reach that makes `clean -fd` dangerous, and an empty directory
costs a reader nothing.

**`git stash` is not the shortcut it looks like**, and this is the note for
whoever proposes it next: one stash stack is shared by every worktree of a
repository. A `pop` in the wrong checkout is a keystroke away, and the work is
then in neither place anybody expected — a failure with no error message in it.
A patch file is worse to type and cannot be popped anywhere by accident.

**The window opens last**, after the work has landed, so the agent's first sight
of the worktree is the work already in it rather than an empty checkout it is
being asked to carry on with. `--keep` leaves the base checkout alone on success
too, for when the work is wanted in both places.

**It will not clear the checkout under another agent.** Until scratch windows,
the base checkout held one agent, and a move could assume the uncommitted work
in it was the caller's. With two standing there it may be the other one's, in
the middle of being written — and anything that agent changes between the patch
and the clear is not moved but lost, restored to HEAD with the rest. So while a
window on the base checkout other than the caller's reports `working`, `move` is
refused before anything is written. It refuses rather than warns, unlike closing
a window with a working agent, because the way past it is not a `--force`:
`--keep` is the safe variant of the same command, copying the work and leaving
the checkout exactly as it was, and passing it by reflex costs nothing.

## Output contract

stdout carries the answer and nothing else, so any command can be piped:

| Command | stdout |
|---|---|
| `new` | the new worktree's path — `cd "$(tw new eng-1)"` works |
| `move` | the same, printed once the work has arrived — until then there is no honest answer to where it went |
| `cd` | the chosen worktree's path, so `cd "$(tw cd eng-1)"` works unaided |
| `rm` | the removed worktree's path |
| `prune` | the paths it removed, or would remove |
| `ls` | the table, or a JSON array with `--json` |
| `setup` | the config file's path, or the config itself with `--dry-run` |
| `config`, `doctor` | the report you asked for |
| `shell-init`, `tmux-init`, `help`, `version` | the script or text you asked for — `version --check` puts what it found on stderr, so the version line stays one line |
| `setup --refresh` | the config file's path, or the config itself with `--dry-run` |
| `refresh` | nothing — what it did is a report, and the answer is the state it left behind |
| `agent-init` | the plugin directory it installed into, or the plugin's files with `--print` — with what it wrote, and where else it could go, on stderr |
| `send` | nothing — there is no answer, only something done; what the window was showing and what was typed go to stderr |
| `close` | nothing — there is no answer, only a window that is gone; what it closed and what that cost go to stderr |
| `restore` | nothing — there is no answer, only a session that is back; what it could not open, and the way in when it stayed out, go to stderr |
| `scratch` | nothing — the answer is a window, and the name you gave it is how everything else reaches it; where it opened goes to stderr, and under `--reuse` so does which of its three cases it met |
| `signal` | nothing — the answer is the stamp on the window, and out of scope it is silent on stderr too |
| `guard` | nothing — the answer is the exit code, that being what a PreToolUse hook reads, and the refusal it carries goes to stderr for the agent |
| `session-start` | what each optional feature did, one message per feature — the reader is the agent, whose SessionStart hook adds a hook's stdout to the session as context, and with nothing to report it prints nothing at all; recording which conversation the session is prints nothing ever |

Progress, warnings, prompts, and errors go to stderr, prefixed `warning:` or
`error:` following git's convention, and unprefixed when it is just narration. So
`tw ls --json | jq` and `tw prune --yes > removed.txt` both stay clean.

### Messages are written for scanning, not for reading

The messages with the most to say used to say it as one line: a finding, an em
dash, what to do about it, a second em dash, why it mattered. At the length
those reached — `doctor`'s tmux finding was a hundred and eighty columns — the
terminal wrapped them wherever it happened to run out of room, mid-path and
mid-command, so the part meant to be copied was the part hardest to pick out.

Breaking the line was the first fix and it was not enough on its own. It left
the same prose sitting on three lines instead of one, and the prose was the
other half of the problem: these were written in the register the rest of this
repository is written in, and a trailing appositive that reads well in a
paragraph is a thing to parse in a terminal, where nobody is reading. They are
looking for one fact.

So a message now front-loads its subject, says one thing per line, and names its
parts. **What is wrong, what it costs, then what to type** — the copyable part
last, where a reader who has decided to act finds it without reading past it
twice. Nothing was shortened to fit: splitting costs a line of screen, cutting a
clause costs the reader what it said.

    warning: post_create failed in eng-9
             failed step  npm ci
             log          .git/treewright/post-create-eng-9.log

Three mechanisms hold that up, all in `internal/cli/message.go`.

**The indent** is what makes two lines one message. A continuation starts where
the first line's text started, and the writers apply it rather than the
messages — thirty call sites spelling their own indent is thirty chances to
spell it differently, which is the state it replaced: one hand-typed run of
seven spaces in `rm`, and no way for anything else to match it. Progress is the
one exception, and only because it has no prefix to align under; its text starts
at the margin, so it takes a two-column hanging indent instead. For the same
reason a single thought never spans two `progressf` calls: the second starts at
the margin and reads as a new subject.

**Labelled fields** (`asFields`) are what replaced the em dashes. A message with
a what, a where and a how ran them together and a reader after the log path had
to read the whole sentence to find it. A list (`asLines`) gets a line per entry,
one step further in — the files `agent-init` wrote, the worktrees a slug prefix
matched, the prefixes `setup` read off origin. Comma-joined, those were the
messages that grew without limit: a repository with a dozen worktrees is exactly
the one where the list is worth reading, and exactly the one where a joined list
is unreadable. `ui.Table` renders a cell holding newlines as a row spanning that
many lines, which lets a `doctor` finding and a `config` value do the same
inside a table rather than laying out their own text beside one.

**Color** marks the two spans worth finding without reading: the severity, and
the part meant to be typed. It is decoration and nothing more — off down a pipe,
off under `NO_COLOR`, off on a dumb terminal — so every message carries its
whole meaning in the text. What color buys is the eye landing in the right place
first, never a fact only the colored copy has.

Counts read as sentences — `1 uncommitted file`, `2 commits not on origin` —
rather than covering both cases at once with `file(s)`, which reads as neither.

### doctor is a report, so it is laid out as one

Its findings are grouped: the installation checks first, because a broken one of
those breaks every repository, then a section per config, then a count of what
was found. Three things came out of that. The repository name stopped being a
`proj: ` prefix repeated down the left margin and became the heading it always
was. The thing being checked got a column of its own, so a reader told the shell
integration is missing can find the word "shell" rather than reading for it
inside the seventh sentence. And the count at the end answers the question a
report is read for — ten green lines with two yellow ones in the middle is
exactly the shape an eye slides off.

    installation
      ok    tmux               /usr/sbin/tmux
      warn  shell integration  not loaded
                               cd and rm cannot move your shell
                               add to your startup file:  eval "$(treewright shell-init zsh)"

    proj
      ok    main_dir           ~/proj
      ok    origin             forks from origin/main

    1 warning, nothing failed

The status words stayed words. Symbols were the alternative, and they buy less
than they look: the column is already colored, and `ok`/`warn`/`fail` need no
font to render.

`config` gained a `FROM` column for the same reason — where a value came from is
a third fact about it, not a suffix of it — and it sits *before* `VALUE` rather
than after. A last column is the only one a table never pads, so markers after a
column of absolute paths end up fifty columns from what they mark.

Exit codes: `0` success, `1` the command ran and failed, `2` it was invoked
wrong. `doctor` exits `1` when a check fails, so it can gate a setup script.

Color is on only when writing to a terminal, and off under `NO_COLOR` or
`TERM=dumb`.

`--json` always carries the base checkout as its first row, in every repository
it can answer about, so a consumer reads row 0 rather than testing whether there
is one — see "The base checkout" for what the table does instead, and why the two
differ. It reports `ahead` and `behind` as `null` rather than `0` when the branch
cannot be compared to its base — an unknown is not a zero. An open window is
described with three fields, because they are consumed differently: `window` is
the name a human reads, `window_id` is what `tmux kill-window -t` takes, and
`window_session` is what `tmux attach -t` takes. All three are empty strings when
no window is open.

Two booleans go with them, and both exist because they were being worked out by
hand. `window_is_current` marks the window the command is running in — the one
whose closing ends the session doing the reporting, and the one an agent must
not take down before it has finished answering. Reading that off the listing
used to mean a `tmux display-message -p '#{window_id}'` of your own and a
comparison, which is a fact treewright already has. `window_last_in_session`
says that closing the window ends its session with it, which had no answer short
of counting. Both are false when no window is open, and `window_is_current` is
false outside tmux, where there is no such window — spelled explicitly, because
the empty current window would otherwise match every row's empty `window_id`.

**Neither costs a tmux call per row.** `#{session_windows}` rides in the same
`list-panes` pass that fills the rest, so a table of a dozen worktrees still
costs one round trip; the current window is one question about the caller, asked
once. The per-window `display-message` that used to answer the second of them,
for `rm`'s one window, is gone — `Window.LastInSession` reads the count the
listing already carried.

## Statuses

`ls` reports one status per worktree, in this precedence: `dirty` outranks
everything because it is the most easily lost, then `merged`, then `unpushed`; a
pushed-but-unmerged branch is `active`.

Two rows stand outside that scale, because it answers "how safe is this to
remove" and neither is anything `rm` could remove: the base checkout is `base`,
and a scratch window is `scratch` — see "The base checkout" and "Scratch
windows".

The counts shown — `dirty (3)`, `unpushed (2)` — are the numbers the removal
guards refuse over, so a listing says how much a `--force` would discard.

`ls` does not fetch. It changes no working tree, branch, or ref, so a branch that
landed since your last fetch still reads as `active`. `rm` and `prune` both fetch
before they judge, and so can disagree with a stale listing; they are the ones to
trust.

**Squash merges are recognized.** When a forge squash-merges a pull request, the
branch's own commits never land upstream — they are collapsed into one new commit
and the remote branch is deleted. A naive "are these commits upstream?" check
calls that landed work unpushed and refuses to clean it up. treewright instead
synthesizes a single commit holding the branch's whole tree on top of its
merge-base — the same patch a squash merge produces — and asks `git cherry`
whether an equivalent patch is already upstream.

That synthetic commit is written to the object database as a dangling object, so
**treewright needs write access to `.git` even for commands that only report.**
Its author, committer, and dates are fixed, so its hash depends only on the tree
and parent being tested: repeated runs reuse the same object rather than leaving
a new one behind each time, and `git gc` reaps it.

## Reporting what failed

Two of the things `new` sets in motion run where treewright cannot watch them, and
each needed its own answer.

**A window's command** — `command`, or `resume_command` — is run by tmux, which
closes the window the moment it exits. A command that cannot start erases its own
explanation at the speed it appeared: the window flashes, and the `command not
found` goes with it. treewright can say afterwards that the window "closed as soon
as it opened", and does, but that is a guess arriving in another terminal without
the one thing needed, which is the message. So the command is wrapped: one that
exits nonzero leaves its window up with its output still on screen, above a line
naming the command, its status, and the Enter that closes it.

A command that succeeds closes its window exactly as before — holding every window
open would turn finishing normally into a keypress. So would reporting a stop the
user asked for, which is why anything above 128, the range of a command killed by a
signal and nearly always a Ctrl-C, is let through untouched. The command runs in a
subshell so that an `exit` of its own — from a wrapper script, a shell function, an
`activate` — ends the command rather than the wrapper, which would close the window
with the output erased and is the whole case this exists for.

**The line that names the command does not repeat it.** It used to, and that made
the wrapper grow with what it wrapped instead of by a fixed amount — worse than
twice as fast, since `fillPrompt` has already shell-quoted the prompt into the
command, and quoting that copy again turns one apostrophe of ordinary English
possessive into sixteen bytes. What it bought was tmux's own ceiling. A tmux
client carries a command to the server in a single imsg, which holds 16384 bytes
less its header and the argument count, so a command list past 16364 is refused
outright with `command too long` — and since `new` deliberately does not fail on
a window it could not open, an over-long `--prompt` left a branch, a worktree, no
window and no agent, under tmux's raw refusal with the doubled script quoted into
it. So the report names the command's first line, cut to eighty columns, and the
copy that actually runs stays byte-exact.

**A resume window is handed two commands**, `resume_command` with `command`
behind it, so that a resume which finds nothing to continue starts an agent
rather than parking on the error — see "When there is nothing to resume" in
`agents.md`. Everything above holds unchanged: one script, one shell, the last
command's status deciding whether the window is held open, and the line that
names what exited naming whichever of the two it was.

**And the length is checked before anything is created**, in `fillPrompt`, beside
the refusal of a prompt the template cannot take: both are this invocation being
wrong, and both are cheap to say while there is still nothing to clean up. What
is measured is the script tmux is handed rather than the setting it came from,
which for `resume` is both commands with the prompt in each of them: a check
against either alone would pass an invocation tmux then refuses. The
error names the size and the limit, since the only useful thing to know about a
prompt that will not fit is how far over it is. The limit treewright holds to is
tmux's less a kilobyte for the rest of `new-window`'s argument list — the
session, the worktree's path, the window name — because measuring those exactly
would make the limit a property of a call site that does not exist yet at the
point the check runs, and what fills the budget is prose, where nobody is writing
to the last hundred bytes.

**post_create** cannot be reported as it happens at all: nothing waits for it, so
treewright has already exited by the time it fails. The failing step writes the
command that stopped it beside the log, and the next `ls`, `cd` or `resume` that
mentions that worktree warns, naming the command and the log. It keeps warning: a
half-installed worktree stays half installed, and a warning shown once, in
whichever command happened to run first, is one a user who stepped away never sees.
Finishing the install by hand and deleting the marker is what ends it. The marker
is cleared whenever a worktree is created under that slug, so a recreated worktree
does not inherit the failure of the one before it.

`doctor` covers what can be seen in advance rather than after: `command` and
`resume_command` are checked for a first word that is on PATH. post_create is not,
because its commands are shell lines where a first word is as likely to be a
builtin as a program, and a false warning about `cd` is worse than no check.

## Upgrading treewright itself

Three of treewright's integrations follow an upgrade on their own, and the way
they do it is the same trick each time: what sits in the user's file is a
*reference* rather than a copy. `eval "$(treewright shell-init zsh)"` re-runs at
every shell start, `run-shell 'treewright tmux-init --apply'` re-reads at every
tmux start, and the agent plugin is a directory treewright owns and rewrites.
None of those can go stale in the sense a pasted snapshot could.

What they can all be is *not yet reloaded*, and that turns out to be the whole
problem. A tmux server runs for weeks. A terminal stays open for days. And a
worktree's copy of the plugin is made once, by the carry, and then nothing ever
looks at it again — which is a genuine snapshot, of exactly the kind
[`agents.md`](agents.md) says the plugin exists to abolish, reintroduced one
directory further down.

None of that breaks anything loudly. The old bindings still open popups, the old
wrapper still moves your shell, the old hooks still fire — right up until a
signal verb is renamed, and then every pre-upgrade worktree errors on every
agent transition while `doctor` reports `ok`. So each integration gained a way
to say which treewright it came from, and one command puts them right.

### Asking a running integration which treewright it came from

Each of the three answers differently, and the differences are forced:

- **The agent plugin** is a set of files treewright wrote, so the question is a
  byte comparison against what `agent-init` would write today. That was already
  how the main checkout was checked; what is new is that `doctor` now walks the
  repository's worktrees and asks the same of each, and reports the answer as at
  most two findings — one for the copies that are out of date, one for the
  worktrees that never got one — each carrying its worktrees as a list. A
  finding per worktree per file would turn one upgrade into thirty lines of a
  report nobody then reads to the end of.

- **The tmux bindings** cannot be compared that way, and the reason is worth
  writing down because it looks like they could. tmux echoes a binding back in
  its own normalized spelling — single quotes rewritten as double, backslashes
  doubled, its own column padding — so byte-comparing `list-keys` against the
  emitted snippet reports every server as stale, including one loaded a second
  ago. What a server *does* hold verbatim is a value treewright puts there, so
  the snippet ends with `set -g @treewright_tmux_init "<digest>"`, a fingerprint
  of the snippet's own source. `tmux.HasBindings` — a substring test for
  "treewright" in `list-keys` — stays what it was, and the stamp is what
  separates "some version's bindings are loaded" from "this version's are".

  The keys are deliberately *not* in the digest. Which key opens the picker is
  the user's decision, made at the `tmux.conf` line; a fingerprint that moved
  with it would report a server loaded by this very binary as out of date for as
  long as the custom key survived, which is forever.

- **The shell wrapper** cannot be asked at all: it lives in the user's shell,
  and a child process cannot read its parent's function table. `doctor` used to
  infer "loaded" from `TREEWRIGHT_EVAL_FILE` being set, which a terminal opened
  two releases ago reports exactly as one opened a minute ago does. So the shim
  exports `TREEWRIGHT_SHELL_INIT_VERSION`, its own fingerprint, and `doctor`
  compares — accepting *any* of the three shims' fingerprints, since the
  question is "is this one of mine" and working out which shell is running would
  mean trusting `$SHELL`, which names the login shell rather than the running
  one.

  Beside it the shim exports `TREEWRIGHT_SHELL_INIT_SHELL`, the shell it was
  written for, which is the one thing a stale fingerprint cannot say: it
  matches none of this binary's scripts, which is what makes it stale. `refresh`
  reads it to name the line for the shells it cannot reach (below), and falls
  back to `$SHELL` for the shims already out there, which predate it.

Both fingerprints are digests of the checked-in text rather than the release
number, for the same reason: a shim or a snippet built from an unstamped tree
still has to be distinguishable from an older one, and a `dev` build compared
against a `dev` build would be no comparison at all.

### `refresh`, and what it deliberately will not do

`refresh` is the one command to run after an upgrade. It rewrites the plugin
wherever it is installed — the user-level copy, the main checkout's, and every
worktree's — reloads the tmux bindings and the wrapper in the shell it was run
from, and reports what moved in each place, naming the files the way
`agent-init` does, because the interesting run is the second one and "wrote
`hooks/hooks.json` in eng-1" says which part of the wiring had gone stale where
"updated 6 checkouts" says only that something did.

**It refreshes what is installed and installs nothing new.** A checkout with no
plugin is left alone unless the config carries one — a worktree with nothing in
it and a carry configured is older than the carry and is owed a copy; without
one, writing there would be treewright choosing a placement the config never
asked for. The tmux bindings go back only into a server that already holds some,
on the keys they are already on, since which keys a server binds is a decision
made in a file the user owns. This is the command people will run without
reading it, and `agent-init` and `tmux-init` are where that decision belongs.

**A server already holding this treewright's bindings is left alone.** `refresh`
used to go from "some treewright's bindings are loaded" straight to sourcing the
snippet and a three-line report naming the keys, on every run, while `doctor`
read the stamp and called the same server `ok`. So `refresh` now asks `doctor`'s
question, through the one function both call, and a matching stamp gets one
line saying the bindings are current, the way a current plugin gets "already up
to date". An unstamped server is out of date, as it is to `doctor`: it was loaded
before the stamp existed. The keys play no part in the answer, because they play
none in the fingerprint.

The alternative was to source it anyway and word the report more quietly, and
it loses on three counts. It is not a no-op: the snippet sets the title format,
and a `tmux.conf` line below the treewright one that sets its own wins at server
start and loses to every mid-session reload. A reload of a changed snippet pays
that until the next start, which is the price of delivering the change; paying
it on every run delivers nothing. What it would buy instead is a repair: a
matching stamp says nothing about whether a binding was edited by hand since, and
sourcing again would put the snippet's command back on a key that still runs
treewright. Undoing a hand edit is the same act as restoring a key somebody
omitted, which `refresh` already refuses. And the report matters on one run, the
one after an upgrade, which is the run a reader skims past if the same three
lines print every other time.

**The shell it was run from is reloaded; every other one is told the line.** No
process can define a function in its parent, and that used to be the end of it:
`refresh` reported a stale wrapper and said to open a new terminal. But
`refresh` is not a bare child process when the wrapper runs it. The wrapper
sources the eval file after the binary exits — the channel `cd` moves the shell
through — so a shim appended there redefines `treewright`, `tw` and the
completion in the live shell, and exports the new fingerprint with them.
The call doing the sourcing finishes on the body it started with and the
next call gets the new one. A function redefining itself mid-call sounds like the
thing that would break, which is why the shells are held to it by a test rather
than by this paragraph.

What goes in the eval file is all three shims, each behind a test only its own
shell passes. The obvious design writes one shim, for the shell `$SHELL` names,
and `$SHELL` is the login shell: somebody running fish from a bash login would
have bash's shim sourced into fish. The shell-name variable above arrives only
with the shims that will need replacing *next* time. With every shim behind its
own guard, nobody has to know which shell is on the other end — and the
eval-file rule that every line parses the same in all three shells still holds,
since to the two shells a shim is not for, it is a quoted string.

The guards are the part that looks overbuilt, and each piece is load-bearing.
The natural test, `test -n "$ZSH_VERSION"`, fails under `set -u`: bash abandons
the whole file at the first unset variable, taking the wrapper's cleanup with
it. `${ZSH_VERSION-}` is safe there, and fish cannot parse it, even on a line it
never runs. So fish is told apart first by quoting alone: fish reads `'\\'` as
one backslash where zsh and bash read two, and all three read `"\\"` as one. The variable test then sits inside a quoted `eval` that only zsh
and bash ever evaluate. The same difference is why the shims are quoted with
backslashes written outside the quotes. None contains one today, but a quoting
rule that holds only for today's text would stop holding at somebody's next edit.

That quoting, `shellinit.Quote`, is now the only quoting treewright has.
`internal/cli` kept its own copy of the POSIX form after this one was fixed,
and the copy wrote the eval file's `cd` line. A repository whose path held `\'`
sent fish a line it could not parse, and one holding `\\` sent it looking for a
directory with one backslash in its name. Everything treewright quotes into a
shell line now goes through the one function: the `cd`, a `--prompt`, a
post_create step's name in its log, a popup's command. The eval file is read
by fish for certain, and a popup or a window by tmux's `default-shell`, which
starts out as the login shell and so can be fish as well. A backslash outside
the quotes is one backslash to `sh`, zsh and bash too, so moving every call site
onto the stricter rule changed nothing that already worked in them.
One rule that is right in every shell is simpler to own than two rules and a
judgement, at each call site, about which one a line needs. The cost is four
bytes per backslash where there was one, which is what an apostrophe already
cost. That spends a `--prompt`'s share of `tmux.MaxCommandLength` faster. It
takes thousands of backslashes to matter, and a prompt pushed over by its own
quoting is refused before anything is created, like any prompt too long to run.

Reloading made one latent problem live: fish keeps every completion it is
given, so a second `source` doubled each one. The fish shim now erases its
completions before adding them. Re-running the startup line by hand did the
same thing before any of this.

It made a second one live in zsh. The shim registers its completion only where
a `compdef` exists, and a `compdef` can exist where `compinit` never ran. It
may have been autoloaded by hand, or copied into a snapshot of an interactive
shell's functions, which is how some agents start the shell they run commands
in. Calling it there prints an error from zsh's internals. A startup file
rarely ran in that state, and a mid-session reload easily can. The narrower test,
whether `compinit` ran (`$+_comps`), skips that call. It also skips the
`compdef` that znap and zcomet define, which queues registrations until the
manager runs `compinit` itself. znap does that at the first prompt, after every
line of `.zshrc`, so no placement of the treewright line would come after it.
Nothing short of calling `compdef` tells those states apart, so the shim calls
whatever `compdef` it finds and discards what it prints. A `.zshrc` that runs
`compinit` below the line gets no completion and no message, as it always did,
since `compinit` is what defines `compdef`. That is why the README says where
the line goes. `doctor` does not check the order. It is a child process and
cannot see its shell's completion state, and the finding would be about the
user's `.zshrc` rather than about treewright's installation.

Every shell but that one keeps the wrapper it started with, so the report ends
with the line that reloads one, which is the line in the startup file, spelled
for the shell the shim says it is. Without an eval file it says nothing,
exactly as before. The fingerprint alone is inherited by every process below a
shell that loaded it, including a shell that never did, so it says nothing about
the shell `refresh` is running in. The eval file is the one fact that does.
Loading the integration into a shell that never had it would be an install,
and that is `shell-init`'s decision. It is also why `doctor`'s finding for a
stale wrapper names `tw refresh` rather than a new terminal: doctor makes that
finding only where the wrapper is calling, which is exactly where refresh can
reach.

### What the cask says after an upgrade

Homebrew prints a cask's caveats on an upgrade exactly as it prints them on a
first install: `Cask::Upgrade` calls `caveats` on the incoming cask and `puts`
the result, near the top of the run and before the fetch. So one fixed string
spends the only moment an upgrader is reading treewright's output on the steps
they took months ago — add the shell-init line, run `tw setup`, both long done —
while the three integrations above are all still running the version that was
just replaced, and nothing has said so.

The caveats cannot put any of that right. They are text, and the one thing text
can do is name the command, so the cask asks which of the two is happening and
says the other thing on an upgrade: `tw refresh`, what it rewrites and reloads —
the shell it is typed into among them — a new terminal for every other shell,
and `tw doctor` for whatever is still behind. It is the same advice `refresh` itself would give,
arriving at the one moment somebody is looking at treewright's output without
having asked it anything.

**The question is asked of the Caskroom, not of the cask.**
`cask.installed_version` is a directory read — the version whose metadata is on
disk while the incoming cask is being loaded — so it is nil on a first install
and the outgoing version on an upgrade. What it is compared against is
GoReleaser's `{{ .Version }}`, interpolated into the Ruby at generation time, and
deliberately *not* the cask's own `version` stanza. GoReleaser emits
`custom_block` at the top of the cask, a cask body is `instance_eval`'d top to
bottom, and `caveats` evaluates its block immediately rather than at print time —
so `version` is still nil that early, and comparing against it silently never
matches. Nothing raises. The two arms just collapse into "is anything installed
at all", and the cost lands on the cases nobody tests: `brew info` and
`brew reinstall` on an installed cask both read as an upgrade. Reaching for
`version` there is the obvious tidy-up, and it is the bug.

**A caveats block that raises is the failure to design against.** It does not
print a bad message — it stops the cask from loading, and `brew upgrade` breaks
for everybody who has the tap. That is why the Ruby is a nil check and a string
compare and nothing else, and why what it compares against is a literal
GoReleaser has already resolved rather than anything worked out on the user's
machine.

There is also no `caveats:` key beside the block, and that is not tidiness. A
cask accumulates every `caveats` stanza it is given rather than the last one
winning, so a key left in place would print underneath the block — the install
text and the upgrade text together, on every install and every upgrade alike.
That is what rules out the other way round the load order, too: GoReleaser
renders `caveats:` last and its heredoc interpolates, so `custom_block` could
define a lambda for the key to call, and the whole of what that buys is a
`version` stanza that has run by then. It splits one message across two stanzas
in two halves of the file to learn something a generation-time literal already
knows.

What the approach gives up is that versions are all the cask can see. Somebody
who installed treewright and never added the shell line gets the upgrade text on
their next `brew upgrade`, because from the Caskroom that is exactly what they are
doing, and the first-install advice they still need is left to `doctor`. Nor does
any of this reach a Linux install, which arrives by `go install` or a tarball and
has no caveats to print — one more reason `refresh` and `doctor` have to carry the
same information on their own.

### Checking for a newer release

Explicit only: `doctor` asks, `version --check` asks, and nothing else ever
does. An upgrade check on `new` or `ls` is a network call in the middle of a
command that had no reason to make one — slow on a bad connection, a privacy
question on any connection, and a warning arriving while the user was doing
something else. There is no cache file for the same reason there is no
background check: both exist to make an *automatic* check cheap, and this one is
not automatic.

Two properties matter more than the answer. It must not hang, since `doctor` is
what a person runs when something is already wrong — hence a short timeout on
the whole request. And an unanswerable check must be silent in `doctor`: an
offline laptop must not come back with a warning about the network it is not on.
`version --check` is the one place that outcome is spoken, because somebody who
typed it and got nothing would reasonably conclude they are up to date.

A build with no release version says so rather than guessing. `dev` is not older
than anything, and reporting it as behind would send somebody upgrading a binary
they compiled an hour ago. That check happens *first*, before any request, which
is also what keeps the test suite off the network.

The upgrade command is named only for a route that can be told from the path the
binary is running from — a Homebrew prefix, or the `go install` directory. Naming
the wrong one is worse than naming none: `brew upgrade` told to somebody who used
a tarball fails in a way that reads as treewright being broken, so the rest get a
sentence instead.

## Safety

`rm` refuses, absent `--force`, when the worktree has uncommitted changes or
commits reachable from no origin ref. It refreshes `origin/<base_branch>` first,
so a branch that merged moments ago is recognized as merged rather than tripping
the guard on a stale ref. `prune` only ever targets worktrees that are both
merged and clean.

A destructive command never acts on a name it had to guess at: a slug prefix must
match exactly one worktree, the expansion is printed, and anything ambiguous or
unknown is an error naming the alternatives. `setup` will not overwrite an
existing config, or add a second one for a repository already registered — which
would make the config that applies depend on registry order.

Removing a worktree leaves its tmux window pointing at a directory that is gone,
so `rm` offers to close it. The window is identified from the worktree's own path
rather than from wherever you ran the command, because a teardown is normally run
from somewhere else — closing "the window I am in" left the window named after
the worktree behind, still sitting in the deleted directory. It is closed even
when it turns out to be in another session, or when you are not in tmux at all.

`prune` asks per worktree it removed. Neither closes a window without asking
unless you pass `--yes` to `rm`, because a window may still have a session
running in it; with nobody to prompt — a script, an agent — both print the
`tmux kill-window` to run instead.

**What they name is `treewright close <slug>`, not a `tmux kill-window`.** The
raw line was the last place driving treewright meant typing tmux, and it failed
in the way that is hardest to notice. It is run from a shell holding none of
treewright's environment, so under `TREEWRIGHT_TMUX_LABEL` a bare
`tmux kill-window -t @3` looks in the *default* server — where `@3` is some
other window entirely. tmux closes it and exits 0. The window that was meant to
close stays open, one nobody asked about is gone, and nothing anywhere says so:
a wrong session name fails loudly, a wrong server does not.

`close` closes the window on a worktree and nothing else — the worktree, the
branch and the work in them are `rm`'s business. **It works after the worktree
has been deleted**, which is mostly what it is for: the window is found by the
`@treewright_worktree` path recorded on it, and that record outlives the
directory, so the same lookup answers before and after. The path is computed
from the slug rather than looked up among the worktrees for the same reason. A
prefix resolves while the worktree is still there and there is nothing to match
against once it is gone, so a removed one is named in full.

**Everything it says, it says before the window goes.** Closing a session's last
window can detach the client that would have read the report, and closing the
caller's own window kills the pane treewright is running in — there is no
afterwards to report from. Both are said rather than refused: the second is a
real thing to want, and the agent guide asks for exactly it as the last step of
a teardown.

**An agent still working is warned about, wherever a window is closed.** The
agent is the window's command, so there is no detaching from this and coming
back — the work stops and the session goes with it. That is the one thing about
a window treewright knows and the caller may not, since the state comes from the
agent's own hooks rather than from anything visible in the window's name, and it
is exactly the "something may still be running in it" that stops `rm` and
`prune` closing a window unasked in the first place. So `rm --yes` says it too,
and `rm`'s own prompt puts it above the question, where it is the caveat most
likely to change the answer.

It warns rather than refuses. The caller asked for this, and treewright is in no
position to judge whether what the agent is doing still matters; a refusal would
need a `--force` to get past, which is a flag people learn to pass by reflex and
then pass everywhere. What a warning buys is the loss being on the record at the
moment it happens rather than discovered later.

Only `working` warns. `waiting` is an agent blocked on a person and `done` is
one with nothing in flight — those are the states an ordinary teardown closes,
and a warning that fires on the ordinary case is one that stops being read.

Closing a session's last window ends the session, which moves an attached client
elsewhere or detaches it, so the prompt says when that is what is about to
happen. Normally it is not: the base window outlives every worktree.

**An optional behavior moves somebody's checkout, which is new — and what makes
it defensible is that the move is one treewright could not have got wrong.**
Everything above is treewright refusing to destroy something without being asked
twice; `features` is the first setting under which treewright changes a working
tree at a moment nobody typed anything. Three things carry it. It is off unless a
config named it, and it never becomes on by default. The move is
`git merge --ff-only`, which advances a branch that has only fallen behind and
refuses a branch that has diverged or has local changes in the way — so the worst
outcome is a message and a checkout exactly as it was. And it happens only in the
base checkout, on `base_branch`, at the start of a session: never in a worktree,
which is the case `guard` refuses on an agent's behalf, and never mid-session,
which is why the hook's matcher excludes `compact` and `fork`.

The refusal is reported rather than swallowed, and so is a fetch that could not
reach origin. Silence there would say "you are current" to an agent about to
trust the checkout, which is the one wrong answer available.

## What treewright is allowed to write

Setup touches as few files outside a repository as it can, and never a file it
does not own. Two reasons, and the second is the one that binds.

It is somebody's machine. A tool that appends to shell startup files, tmux
configuration and agent settings on the way in has made itself a fact about the
whole system to solve a problem about one repository.

And **there is no uninstall.** treewright ships a binary and nothing that undoes
what it did, so every file it writes outside a repository is a file a developer
has to find and reverse by hand on the day they stop using it — from memory,
possibly months later, in a config they have edited fifty times since. The
smaller that list, the more honestly the tool can be tried at all. A trial you
cannot cleanly abandon is not a trial.

What it writes, in full:

| Path | Why it is unavoidable |
|---|---|
| `<config dir>/<name>.toml` | The registry *is* the configuration; there is no treewright without it. One directory, `rm -r` and it is gone. |
| `<main_dir>/.git/treewright/post-create-*` | A background step's log and failure marker, inside the repository's own `.git`, which goes when the repository does. |
| `<main_dir>/.git/treewright/sessions/<name>` | Which conversation the base window and each scratch window is running, one id per file, written from the agent's own SessionStart hook — so resume and restore reopen that one rather than the latest in the directory. Removed by `close` and by quitting a scratch agent; the base window's is rewritten rather than removed. `rm -r` takes the lot, and a missing record only means resuming by directory, as before. |
| `<main_dir>/.git/treewright/move-*.patch` | The uncommitted work `move` is carrying, written before anything is created and deleted once it has landed. What is left behind is left after a failure, deliberately: it is a second copy of work that exists in one place, and the way back in by hand. |
| `~/.claude/skills/treewright/` | The agent plugin, written by `agent-init` — one copy covering every checkout the agent is started in. The only thing on this list outside a repository besides the registry, and running the command that installs the wiring *is* the consent: nothing else writes there, `rm -r` and it is gone, and `claude plugin disable treewright@skills-dir` stops it loading without deleting anything. |
| `<main_dir>/.claude/skills/treewright/` | The same plugin, when `agent-init --local` is asked for it — inside the repository, in a directory treewright named and nothing else writes to. |
| The worktrees themselves | What the tool is for, and `rm` takes each one back. |

One path has come off that list rather than been added to it.
`.git/treewright/no-agent-yet-*` was a note that a worktree had never had an
agent in it, and `resume` read it to decide whether to run `command` instead of
`resume_command`; the recovery is triggered by the failure now, and nothing
reads the files. treewright neither writes them nor deletes the ones it wrote —
deleting a user's files to tidy up after itself is not a licence this list
grants — so they sit inert beside post_create's logs, and
`rm .git/treewright/no-agent-yet-*` takes them off a repository that still has
some. See "When there is nothing to resume" in `agents.md` for why the mechanism
went.

Everything else is *printed for a person to place*, which is why `shell-init`
and `tmux-init` exist at all. The line in your `.zshrc`, the line in your
`.tmux.conf` — treewright wrote neither, so a developer removing them is
undoing edits they made themselves rather than hunting for edits a program made
while they were not looking.

**`agent-init` is the one that writes, and the rule it is held to is the same
one.** The test was never "does it write" but "whose file is it". The hooks
used to be printed because they belonged in a settings file the user owns,
where applying them would have meant a merge reordering somebody else's JSON;
moved into a plugin directory of treewright's own, there is no file of the
user's to touch. One directory, named after the tool, holding nothing a person
put there — which is a thing you can find and delete on the day you stop using
treewright, and the whole point of keeping this list short.

That the default placement is under `$HOME` rather than inside the repository
does not change the rule, only who is asked. A flag used to be the consent, and
now the command is: `agent-init` exists to install the wiring and does nothing
else, it prints the directory it wrote on stdout, and `--print` writes nothing
and dumps the files for anyone who wants to read hooks before they run. What the
flag bought in exchange was a copy per repository, each one a directory `git
status` reports as untracked and each one going stale on its own — a worse
bargain for the same list. See "Where the wiring goes" in
[`agents.md`](agents.md).

`tmux-init --apply` looks like the exception and is not: it applies key bindings
to a running tmux server, in memory, and writes nothing. The one line in
`~/.tmux.conf` that invokes it is still yours to add.

This is also why no `.gitignore` is generated. The per-project agent artifacts
land in files a repository may or may not want tracked, and that judgment
belongs to whoever owns the repository — a tool that edited `.gitignore` on the
way past would be writing into the project's history to tidy up after itself.

## Configuration

One TOML file per repository, in
`${TREEWRIGHT_CONFIG_DIR:-${XDG_CONFIG_HOME:-~/.config}/treewright/repos}/<name>.toml`.

TOML rather than a sourced shell script so that reading a config cannot execute
code: configs are meant to be shared, linted, and generated, none of which should
require trusting their author. Unknown keys are rejected outright, because a typo
like `base-branch` would otherwise be silently ignored, leaving you to wonder why
the base branch is still `main`. The error offers the other reading too — a
config is a file two treewrights may see, on a laptop and a desktop or either
side of a downgrade, and the newer one's settings arrive in the older one looking
exactly like typos.

### The version key, and refreshing a config in place

Generated configs carry `version = <n>`, and it counts revisions of the
*generator* rather than treewright releases: it moves when what `setup` writes
moves, and stays put across releases that change nothing here. A file without
one is an old config and not an error — every config in the wild predates the
key, and refusing them would make an upgrade break every repository already
registered. `doctor` warns; nothing fails.

There is deliberately no rename or migration table. No key has ever been renamed,
and that machinery would be built for a hypothetical. What the version supports
is one warning naming one command.

That command is `setup --refresh`, and it exists because `setup` refuses an
existing config outright — rightly, since overwriting one discards edits — with
the consequence that every later improvement to the generated file reached new
repositories only. A repository registered two releases ago names none of the
keys added since and explains none of what the commentary now explains, and the
only way out was to delete the file and answer the detection again.

**Nothing is re-detected**, which is the whole difference between this and
running `setup` twice. The values are read back out of the file that is there: a
base branch someone corrected by hand, a prefix chosen over the guess, a command
that is not the agent's default. Those are decisions, and a refresh that
re-derived them would quietly undo the ones that disagree with what treewright
would guess today. What moves is the version, the commentary, and any key the
generator has since learned to write.

Three details in the rewrite are load-bearing. The prefixes come back under
whichever of the two spellings the file used, since no config may set both —
and their commentary claims no provenance, because none is on record: whether a
single prefix began as origin evidence or as an email guess was never written
down, and a rewrite that re-emitted the guess's paragraph would assert facts
about a git email this run never consulted. An explicit `branch_prefix = ""`
survives as the live line it is, for the same reason `ticket_pattern` does:
that key is written on whether it was *there* rather than on whether it holds
anything — `ticket_pattern = ""` is how a repository that tracks no tickets
turns the search off, and a refresh that dropped it for looking empty would
turn every window name in that repository back into a ticket hunt. And every
key with a default is read back through `Explicit`, `base_branch` included: a
file that never set one gets the commented default back, never a live line,
because a default written into the file as a setting is one that stops
following treewright's own changes.

`features` is the one key copied back unconditionally, and that is not the
collapse the rule above forbids: nothing there has a default to be overwritten
by, an absent key and an empty list say the same thing, and there is no value
the generator could pin in that the file did not already hold. Adding the key was
what first moved the version, from 1 to 2 — the generator now writes the
registry's own prose above it, and a config written before that is a config where
the decision was never offered.

Which config applies, in order: an explicit name; the config whose `main_dir` is
the repository you are standing in; the only config, when the registry holds
exactly one; otherwise an error listing the names. A broken config elsewhere in
the registry is skipped while matching rather than blocking work on a repository
whose own config is fine — but if nothing matched, the skipped error is what gets
reported, since the unreadable config is nearly always this repo's own, edited a
moment ago, and "no config matches" alone would send you looking for a file that is
sitting right there with a typo in it.

`main_dir` is resolved through symlinks, not merely cleaned. git reports fully
resolved paths for every worktree, so a `main_dir` that reaches the repo through
a symlink would never match what git says and its worktrees would be invisible.

`setup` writes the file with every value commented, and marks which of them were
detected and which were guessed, on the principle that the file remains the
record: it is a way to start one, not a layer above it. `config`
prints what is in force with defaults marked as such, because the gap between a
config file and the behavior it produces — invisible defaults, unexpanded paths,
and which of several configs applies — is where the confusion lives.

### post_create

Either one command or a list of them, under one key rather than the two spellings
`branch_prefix` and `branch_prefixes` are. The plural of this setting has no name
that reads like anything, and a second key would let a file set both and leave a
reader to work out which won; one key taking either shape has neither problem, and
every config written before the list existed still means what it meant. An empty
string stays "nothing to run", that being how a config that once had a setup step
says it no longer does, but an empty entry *inside* a list is refused as the
half-finished edit it is.

The commands run in one `sh -c`, because treewright exits as soon as the window is
open and is not there to run the second one. Each is wrapped in a subshell, which
makes it a step in the sense every CI steps-list already means: it starts in the
worktree root whatever the last one did, and its failure is its own. `set -e` over
a flat script was the alternative and it fails in both directions — it reaches
inside a step to stop on a failure the user had already handled with `||`, and a
step that calls `exit`, or sources something that does, ends the whole run with
nothing reported. A step that wants to work elsewhere writes `cd sub && ...`.

Each step is echoed into the log as `$ command`, and a failing one names itself
before the run stops, because a log truncated halfway through a five-step install
is otherwise indistinguishable from one still being written. The log lives in
`.git/treewright/` rather than in the worktree, where it would show up as an
untracked file and make the tree read as dirty — and rather than nowhere, which is
what discarding the output would leave you with when an install fails. How the
failure reaches the user is below.

### Optional behaviors

`features` is a flat list of names, and everything about the setting follows from
what the entries are: work treewright starts on its own initiative, at a moment
nobody typed anything.

```toml
features = ["fresh-base"]
```

**Opt-in, and permanently so.** Every other thing treewright does is asked for at
the moment it happens — a command is typed, or an agent hook reports a transition
that has just occurred and treewright writes it down. A feature is the other
shape, and a tool that fetches and moves somebody's checkout is welcome exactly
when they asked for it and alarming otherwise. So nothing here is on by default
and nothing here ever becomes so, and a name nothing in the registry matches is a
load error rather than a no-op: a misspelled feature is indistinguishable by
behavior from a feature that is off, which leaves a repository looking configured
with nothing to find.

**A list of names and not a `[features]` boolean table**, and the reason is
mechanical rather than aesthetic. A TOML table swallows every bare key written
after it, so a table would have to be rendered last in the generated file for
ever — a constraint `renderConfig` and `setup --refresh` would both have to hold,
and one nothing in the file's text would announce when it broke. A flat list is
order-free, renders with the `tomlList` that already exists, and validates
against a closed vocabulary the way `agent` does. An absent key and
`features = []` are deliberately the same value: a feature is on or it is not, so
there is no third state for an empty list to express, unlike `ticket_pattern`
and the command keys where an empty value is a setting of its own.

**One dispatch verb, `treewright session-start`, and not one command per
feature.** This is the decision the whole shape rests on, and it is the
compatibility argument made elsewhere about the plugin. The agent wiring lives in
a plugin copy on somebody's disk: installed once, carried into worktrees as a
snapshot, and rewritten only when its owner runs `agent-init` or `refresh`. A
verb per feature would mean every feature shipped after that copy was made is a
hook the copy does not have — enabled in the config, silent in practice, with
nothing to say so. One verb makes the plugin's line a constant, so a feature
added two releases from now runs in wiring installed today.

**The toggle is read when the hook fires, never installed into the hook.** The
plugin's default placement is user-level — one copy covering every repository —
so putting the enabled list in the hook JSON would mean one machine-wide answer
to a per-repository question. `features` is read at the moment the hook runs,
which is `signal`'s discipline exactly: the same hooks fire in every session the
agent has, most of them in repositories treewright has never heard of, and what
decides whether anything happens is the config found from where the session is.

**Everything out of scope is a silence.** A hook that narrated its own no-op
would narrate it at the start of every session in every repository on the
machine. So an unregistered directory, a repository that switched nothing on, and
a feature that found nothing to do all exit 0 and print nothing.

**What it prints goes to stdout.** A `SessionStart` hook's plain stdout is added
to the session as context, so the reader is the agent rather than a person, and
this is one of the few messages whose whole point is being machine-consumed.
stderr would put it in a transcript for a human and nowhere else — which for
"the checkout under you just moved" is the wrong audience entirely.

**The moment excludes the middle of a session.** `SessionStart` fires with source
`startup`, `resume`, `clear`, `compact` or `fork`; the module's matcher names the
first three. `compact` and `fork` happen *mid-session*, so a feature that moves a
checkout would move it underneath an agent already working in it. That is not a
narrower matcher for tidiness — see `docs/agents.md`.

#### `fresh-base`

Fetch `origin` and fast-forward, when an agent session starts in the base
checkout with `base_branch` checked out. It generalizes a hook a repository can
already write by hand, and everything such a hook hard-codes — the checkout, the
branch, which repositories it applies to — treewright already knows.

**`--ff-only` is the whole of the safety and not a stylistic preference.** It
moves a branch that has only fallen behind and refuses everything else: a branch
that has diverged, a branch with local changes standing in the way. treewright
moving somebody's checkout without being asked *at that moment* is defensible
exactly while the move is one it could not have got wrong, and this flag is what
makes that true. A refusal is reported and the checkout is left exactly as it
was.

**It acts only in the base checkout, and only on `base_branch`.** A session in a
worktree is an agent working somewhere else, and moving a checkout it is not
standing in is the very thing `guard` refuses on an agent's behalf — worse here,
since the base checkout may have an agent of its own with work in flight. And the
base checkout is the one place a person switches branches by hand, so a checkout
parked somewhere else is parked there deliberately.

**The failed fetch is the one out-of-scope-looking case that speaks**, because it
is not out of scope: the session *is* in a repository that asked for this, and
the honest answer is that freshness is now unknown. Saying nothing there would be
indistinguishable from saying "you are current", which is the one wrong thing to
tell an agent about to read the checkout.

**It yields to another agent working in the base checkout, and says so.** The
base window's agent and a scratch window's both stand in the base checkout, so a
session starting in either can find the other mid-edit. A fast-forward there
changes files under an agent that is working, so while any window on the base
checkout other than the caller's own reports `working`, the checkout is left
where it is. The caller's own is excluded because a session starting in a window
*is* that window's agent: whatever it last reported belongs to the session now
ending. Unlike the scope checks this one speaks — "another agent is working here,
so I left it alone" is exactly the answer to whether what this agent is about to
read is current — and it runs last, after the fetch and the count, because a
fetch moves nothing anybody stands on and is what lets the answer say how far
behind the checkout is rather than only that it might be. When nothing is behind,
there is nothing to yield and nothing is said.

#### Where a feature's parts live

`internal/feature` holds the vocabulary — the name a config writes, the moment it
runs at, the summary and the prose the generated config explains it with — and
`internal/cli/features.go` holds the behavior. The split is `agentinit`'s: facts
in one package, behavior in the other, because `internal/config` validates the
names and must not depend on `internal/git`.

What holds the two together is `TestEveryFeatureHasAnImplementation` rather than
a shared interface, for the same reason the guard and its matcher are held
together by a test: the coupling is a list on each side, and a test that names
the missing half is a better failure than a signature nobody can implement
wrong. A registered feature with nothing wired to run it is the failure mode
worth catching — the config loads, the hook fires, and nothing happens.

The generated config's commentary is rendered from the registry, so a feature
added later documents itself in every config `setup --refresh` rewrites. That
file is where somebody decides whether to switch one on, which makes it the one
place the prose has to be complete.

## The shell integration

treewright is a compiled binary, so it runs in its own process and cannot change
the calling shell's working directory. The wrapper function closes that gap: it
makes a temp file, passes its path in `$TREEWRIGHT_EVAL_FILE`, and sources it
after treewright exits. Three commands write to it — `cd`, `rm` when your shell
is standing in the directory being deleted, and `refresh` when the wrapper doing
the sourcing came from an older treewright. Everything must still behave
correctly when the file is never sourced, which is why those commands also print
the line to run. An eval file that exists and cannot be written — a swept
tmpdir, a full disk — is the same failure with a cause worth naming, so it is
reported as a warning with the same by-hand line under it; both halves live in
one helper — `moveShell` for a `cd`, `reloadShell` for a reload — so a new
caller cannot keep the emit and forget the fallback.

The shims are emitted by the binary rather than installed as files, so they can
never drift out of sync with it — the same approach fzf, zoxide, direnv, and
starship take, and for the same reason. The commands written to the eval file are
restricted to what zsh, bash, and fish all parse identically, so one writer serves
every shell.

The fish shim needs fish 3.1 or later, because that is where `complete -F`
arrived. The reload `refresh` writes needs only 3.0, for `&&`, so it adds no
requirement of its own. Both were checked by running the suite against real 3.0,
3.1 and 3.7 builds rather than read off changelogs. On 3.0 the shim fails to
load at the `-F`.

They are *stored* as files even so: `internal/shellinit/scripts/init.zsh` and
its two siblings, embedded into the binary by name. Emitting from the binary was
never an argument for keeping 173 lines of shell quoted inside Go, where nothing
highlights it, no shell parses it and an editor indents it as a string — the two
questions are separate, and only the first one was ever about the user. Naming
each file in a `//go:embed` rather than walking the directory is the same rule
the agent plugin's files are held to, and it bites harder here: this text is
`eval`'d into the user's interactive shell at every start, so a file that
shipped merely by being in the folder would run on every terminal they open.
`TestEveryScriptIsDeclared` fails on one that no shell claims.

Every external program the wrappers call is invoked through `command`, because
zsh and bash expand aliases in a function body when the function is *defined*: an
`alias rm='rm -i'` in a startup file would otherwise rewrite the wrapper's own
`rm -f`. It is also why the migration advice is to write
`eval "$(command treewright shell-init zsh)"` when a shell function named
`treewright` already exists — `command` skips functions, so the line cannot ask
the thing being replaced for its own replacement.

**Why the documented line is a bare `eval` rather than a guarded one.** The line
runs the binary to get its own text, so it fails when treewright is not on `PATH`
— and the shell reports that against the startup file's line number, which names
the wrong culprit and sends people looking at the integration instead of the
install. Wrapping it in `command -v treewright >/dev/null 2>&1 &&`, the shape
tool-init lines often take, is the wrong trade for a first-time setup: it turns a
loud wrong-culprit error into a silent no-op, where `tw` is undefined and nothing
at all has been said. The install section carries the `PATH` check instead, before
either integration is added. A dotfiles repo shared across machines, some without
treewright on them, is the case where the guard earns its keep — but that is a
choice about absent installs, not the instruction to hand someone installing it.

**The shim says which treewright emitted it, and for which shell.** Each one
exports `TREEWRIGHT_SHELL_INIT_VERSION`, a fingerprint of its own checked-in
text, and `TREEWRIGHT_SHELL_INIT_SHELL`, and that is the only way either question
can be asked at all: a shell keeps whatever it loaded at start, and a binary
cannot read its parent's function table. Exported rather than merely set,
because what reads them is a child process — `doctor`, which compares the
fingerprint against the shims this binary emits and says when the wrapper in the
shell is somebody else's, and `refresh`, which reloads it. See "Upgrading
treewright itself".

**`tw` and `TREEWRIGHT_ARGV0`.** `tw` calls the `treewright` *function*, resolved
at call time, so the eval-file protocol works identically under either name. That
function runs the binary as `command treewright`, which erases the typed name
from `argv[0]` — so `tw` exports `TREEWRIGHT_ARGV0=tw` for just that call, and
help and every runtime hint answer in the name the user actually typed. What
keeps the canonical name is help prose and anything destined for a file —
tmux.conf lines, shell startup evals — which programs read and shell functions
never reach.

