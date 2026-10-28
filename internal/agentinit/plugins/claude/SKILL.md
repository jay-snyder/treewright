---
name: treewright
description: Manage parallel work in repositories that use treewright (tw) — a git worktree, tmux window, and agent session per piece of work. Use when starting a task in parallel, spawning another agent on one, putting a second agent on the main checkout for work that needs no branch (an investigation, a question, a pull request review), checking which worktrees and agents are in flight or need attention, handing work to or messaging the agent in another worktree, resuming earlier work, or cleaning up merged branches. Use instead of raw git worktree in a treewright-managed repository.
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

## Which repository a command acts on

**Every command resolves its repository from the working directory unless you
name one.** That is right for a person, who is standing in the checkout they
mean, and wrong for you: you stand in exactly one repository and are often
asked about another. Run `treewright new` for a different repository from here
and you get a worktree in *this* one — this repository's branch prefix, base
branch and path, under the slug you meant for somewhere else.

So name it whenever the work is not for the repository you are standing in:

    treewright new --repo cibo eng-142-null-user --prompt "..."

`--repo <name>` takes the name the repository is registered under, which is the
`config:` line `treewright ls` reports and what `treewright doctor` lists. It
works on every command — `new`, `scratch`, `resume`, `send`, `rm`, `close`,
`cd`, `ls`, `prune`, `base`, `attach`, `restore`, `config`, `refresh` — and the
commands
whose only argument is a repository also take it as a bare positional, so
`treewright ls cibo` and `treewright ls --repo cibo` are the same request.
Naming it twice is a usage error rather than a precedence rule.

Passing `--repo` for the repository you are already in is harmless and reads
better than relying on where you happen to be standing, so prefer it in any
command you write down.

**Naming another repository does not move the person's tmux client.** The
window is opened and left current in that repository's own session, and
treewright says so and names `treewright attach <repo>` as the way over. Do not
go looking for a flag to change that: an operator watching one repository
having their screen replaced by another's is the thing this prevents.

Every command has its own help, and it is fuller than this file: read
`treewright help <command>` before improvising around something that seems
missing. `help new` alone covers why there is deliberately no flag to fork from
anywhere but origin, what happens when origin cannot be reached, how the
worktree, slug and branch names diverge under a branch prefix, and what `new`
does outside tmux.

## See what is in flight

    treewright ls --json

One JSON object per checkout, and one per scratch window:

- `"base": true` marks the main checkout. It is not a worktree,
  never a target for rm or prune, and new work should fork from it rather than
  happen in it. It is always the first row, in every repository this answers
  about — so an array of one is a registered repository with nothing in flight,
  where an unregistered one is an error naming the configs there are.
- `"scratch": true` marks a scratch window: another agent session standing in
  the main checkout, listed under the base row. Its `slug` is the name it was
  opened under — what send, close and resume take — and it has no branch and a
  `null` divergence of its own. It is not a worktree either: rm and prune
  cannot name it, and work that will be committed never goes there. Its row
  with `window_is_current` set is how an agent in one knows it is in one. A row
  with an empty `window_id` is one a restart interrupted, waiting for `resume`
  or `restore` to reopen it on its conversation.
- `status`: `dirty` and `unpushed` mean work
  that exists nowhere else; `merged` has landed and is safe to
  remove; `active` is pushed and unmerged — an open pull request;
  `fresh` has nothing committed yet, so it is work just started
  rather than work to clean up.
- `agent_state`: what the agent in that window last reported —
  `working`, `waiting` (blocked on a person), or `done`.
  Empty when nothing has signaled.
- `agent_name`: the name the agent in that window answers to, which is what
  `SendMessage` takes — see Continue or hand work onward. Read it here rather
  than building it: it is empty wherever the agent has none, which is the
  sign to reach it with `treewright send` instead.
- `ahead`/`behind` measure against origin's base branch, and
  `null` means the comparison was impossible — unknown, not zero.
- `window_is_current` marks the window you are running in, and
  `window_last_in_session` the window whose closing would end its session.
  `window_yielded` says that window's agent has run `treewright yield`, which
  closing it from the base window waits for. All three matter under Clean up,
  and all are false where no window is open.
- The listing does not fetch, so a branch merged since the last fetch still
  reads active; rm and prune fetch before they judge, and they are the ones to
  trust.

## Start a piece of work

    treewright new eng-142-null-user --prompt "the instructions for that agent"

Forks a branch from the latest origin base branch, makes the worktree, copies
the env files in, starts the install step in the background, and opens a tmux
window whose agent begins on the prompt. stdout is the new worktree's path and
nothing else.

**A worktree is created with a prompt.** The work in it belongs to the agent in
its window, so hand the work over and let that agent do it. Omitting the prompt
readies a worktree for a *person* — name that out loud when it is what you mean;
it is never where a command lands because the work was going to be done from
here.

**Finished work is not an exception.** A branch that needs only a commit and a
pull request still goes to the agent standing in it. There is no amount of
work-remaining small enough to make doing it yourself the shorter path.

**The tripwire is any command naming that worktree's path, run from here.** Not
a principle to weigh — a thing to notice the first time it happens. An `Edit` or
a `Write` beneath the path, `git -C <path> commit`, `cd <path> && …`, a
`gh pr create` from inside it: each is the handoff not having happened. Reading
that worktree is fine and always allowed; changing it is not.

A `PreToolUse` hook refuses those, so this may arrive as a blocked tool call
rather than as a rule you remembered. It means what the tripwire means: route
the work to the agent rather than looking for a spelling the hook misses.

**When the instructions depend on this conversation, write them to a file** and
pass `--prompt-file`: a commit body arguing a design decision, a description
recounting an audit. A fresh agent has none of that.

    treewright new eng-142-null-user --prompt-file /tmp/eng-142-brief.md

Recovering is not what it looks like. `resume --prompt` does not repair a
worktree whose agent was never told what to do: the window is already open, so
it is switched to and the prompt is warned as undelivered. While the worktree is
clean — the state it is in whenever this is caught early — remove it and make it
again with the prompt it should have carried:

    treewright rm eng-142-null-user
    treewright new eng-142-null-user --prompt-file /tmp/eng-142-brief.md

A refusal from rm is the guard under Clean up doing its job: there is work in
there that exists nowhere else, and that is the person's to decide about. Once
the worktree holds anything worth keeping, reaching the idle agent where it
stands — under Continue or hand work onward — is the only route left.

- The slug may not contain "/". A leading "feature/" or "bug/" chooses a
  configured branch prefix — `treewright new bug/eng-142` — and one
  the repository has not configured is refused rather than guessed at.
- A third positional renames the tmux window: `treewright new eng-142 review`.
  Without it the window is named after a ticket key found in the slug, or after
  the slug cut to fifteen characters. Leave it off unless you have a reason —
  and never name a window after another repository. A session is one per
  repository, so a window called `cibo` sitting in this repository's session
  tells whoever reads the window list that cibo's work is here. treewright
  warns when a name collides with a registered repository; the warning is worth
  acting on rather than passing.
- A branch that already exists — a colleague's pull request after fetching —
  is checked out rather than recreated, so this is also how work is picked up.

## Hand over a long brief

    treewright new eng-142-null-user --prompt-file /tmp/eng-142-brief.md

Anything longer than a few sentences goes in a file, and `--prompt-file` writes
the prompt: one line telling the agent to read that file in full. That is the
default for a handoff worth the name rather than a fallback for when something
breaks. The agent can read the file again after a compaction, when the prompt it
started on is long gone, and it outlives the session that wrote it. It is also
the shape a long brief takes to an agent already running: a message naming the
file, under Continue or hand work onward.

`--prompt` is the same setting for an instruction short enough to type — passing
both is an error — and it is refused past the length tmux will run a command of,
before anything at all is created. `--prompt-file` has no such limit.

treewright neither copies the file nor deletes it, so it has to stay where it is
for as long as the agent may want it. Delete it once the work has landed;
nothing else will.

## Move work already started in the base checkout

    treewright move eng-142-null-user --prompt "carry on with the null-user fix"

Typing in the main checkout and then realizing the change wants a branch of its
own is ordinary. `move` makes the worktree exactly as `new` does — same fork
point, same window, same `--prompt` and `--prompt-file` — and carries the
uncommitted work into it: staged and unstaged changes, and the files git does
not yet track. Files git ignores stay where they are, those being what gets
copied into every worktree anyway.

Do not do this by hand. Until the work is somewhere else the main checkout is
the only copy of it, and what protects it is an ordering `move` holds to: write
the patch, apply it in the worktree, check that it arrived, and only then clear
the checkout. A failure before that check leaves the checkout untouched and
names the patch. `git stash` is especially the wrong reach — one stash stack is
shared by every worktree of a repository, so a `pop` in the wrong checkout is a
keystroke away and the work is then in neither place you expected.

`--keep` leaves the work in the main checkout as well, for when you want it in
both places. It is also the way past the one refusal `move` has of its own: while
another agent reports working in the main checkout, clearing it would change
files under that agent and could take its work, so the move waits for it —
wait with it, or keep both copies.

## Put a second agent on a question

    treewright scratch retry-loop --prompt-file /tmp/retry-loop-question.md

Opens another agent window standing in the main checkout, beside the base
window, with no worktree and no branch behind it. It is for work whose result is
an answer rather than a commit: tracing a bug before deciding where the fix
goes, answering a question about the code, reviewing a pull request, reading CI
logs, keeping track of the agents in the worktrees. Nothing is created on disk,
so the window is all there is to put away afterwards.

**Reach for one when the work would otherwise cost this conversation.** A long
investigation run here fills your context with everything it read; run in a
scratch window it goes on beside you, and what comes back is the conclusion.
The same holds for the person — a question they want answered while you carry
on with something else is a scratch window, not an interruption of you.

**It is not free.** A fresh agent starts with none of this conversation. A
question a few reads would settle is faster answered here, and one that depends
on what you have been discussing is only worth asking with a brief that says
it, which is why the example above passes `--prompt-file`. Open one with a
prompt, as a worktree is opened with one: that prompt is all its agent begins
with.

**Anything that will be committed goes in a worktree, never here.** A scratch
agent stands in the main checkout, which is where the base window's agent
stands and where every new branch forks from, and nothing guards it — an edit
made there lands under that agent and in front of the next `new`. Never check a
branch out there from a scratch window either: a pull request whose code has to
run gets a worktree of its own (`new` on an existing branch checks it out), and
one that only needs reading is `gh pr diff` away.

**If you are the agent in a scratch window**, the paragraph above is about you:
change nothing in the checkout you are standing in. `treewright ls --json` says
whether you are — your row is the one with `window_is_current`, and it carries
`"scratch": true`. When the investigation turns into a fix, hand it on with
`treewright new <slug> --prompt-file <brief>` like anyone else, and the
worktrees are no more yours to edit than they are the base agent's — the
tripwire under Start a piece of work holds unchanged.

**Name it for what it is doing.** The name is required, follows a slug's rules,
and is how everything else reaches it: `treewright close retry-loop`,
`treewright resume retry-loop` to switch to it, and its agent answers to the
repository's name and this one — the `agent_name` its row reports, such as
`cibo-retry-loop` — for `SendMessage`. `review-36` and `ci-watch` tell whoever
reads the window list what each window is for; `ask2` tells them nothing. A
name already answering for something — a worktree's slug, an open scratch
window, `base` or the base branch — is refused, since those commands take any
of them.

Unlike `base`, which always means the one base window, `scratch` opens a new
window. `--prompt`, `--prompt-file`, `--repo` and a third positional that
renames the window all work as they do on `new`.

**When the name is a handle you will come back to, pass `--reuse`:**

    treewright scratch --reuse review-42 --prompt-file /tmp/review-42-brief.md

A scratch session already answering to the name is reached instead of refused.
If nothing has the name, a new session is opened on the prompt. If a restart
took the window, the session is reopened on its conversation, with the prompt.
If its window is open, the window is switched to and **nothing is typed**:
treewright says the prompt was not delivered and names the agent, and you send
it with `SendMessage`. Use it for work that recurs under one name, such as a
review asked for again, instead of reading `ls --json` and choosing between
`scratch` and `resume` yourself.

**A slash command runs only as the prompt a new session starts on.**
`treewright scratch review-42 --prompt "/review-pr 42"` opens a session that
runs the review. In a message it is plain text, and the receiving agent will
not run it — so the open session that did the first review is asked for the
second in words: "the review on #42 was re-requested; review it again".

Never reach a scratch name you are not sure exists with `send` or `resume`.
With no scratch session by that name they fall back to a worktree's prefix, so
`review-4` reaches `review-42`. Under `--reuse` a prompt has to be one line
whatever it finds, since it may be typed. `--prompt-file` always is one line.

**A restart does not end it; quitting its agent or closing it does.** After a
reboot, `resume <name>` and `restore` reopen it on the conversation it was
having. Once it has ended there is nothing to reopen, so what it finds has to
land somewhere that lasts before then — a file, a pull request comment, or a
message back to whoever asked.

A message back is a reply: `SendMessage` to the `from` address of the message
that asked. When you open a scratch window with a brief, say in it whom to
report to — your own `agent_name`, from your row in `ls --json`, the one with
`window_is_current` — so its agent has an address before anyone has messaged
it.

**A scratch window is closed by its own agent, or from the base window once
that agent has yielded.** Its agent closes it with a bare `treewright close`
once its answer is somewhere: run with no name in a scratch window, close takes
that window and nothing else, and from a scratch window every other close is
refused, the base window's above all. If you opened one from the base window,
ask its agent to yield as Clean up describes, and close it by name only once
`window_yielded` says it has. Closing one ends its conversation for good —
there is no resuming it — and rm and prune never touch a scratch window, there
being no worktree to remove. One the person opened is theirs.

## Continue or hand work onward

    treewright resume eng-142 --prompt "address the review comments"

An unambiguous prefix of a slug is enough, and the expansion is reported; a
scratch window's name is taken in full. The prompt reaches the agent only when
the resume actually starts one: a window that was already open is switched to
instead, with a warning that the prompt went undelivered and how to reach the
agent standing there.

**An agent already running is reached with `SendMessage`, to its
`agent_name`** from `treewright ls --json`:

    SendMessage to "cibo-eng-142": "read /tmp/eng-142-review.md and address the comments in it"

Check `ListAgents` first. A name it does not list has no live session behind
it — the window closed, or the agent in it exited — and `treewright resume` is
the way back, with `--prompt` carrying what you would have sent.

The message arrives between that agent's tool calls, marked as coming from
another session, so a busy agent reads it at its next step and an idle one
starts a turn on it. It may run to several lines. It cannot answer a question
or a permission prompt the agent is sitting on — a `waiting` agent blocked on
one of those still needs its person — and a slash command in it is text.

**To hear when it is done, pass `notify_when_idle: true`** — on its own, or with
the message — rather than watching `agent_state`: one notice arrives when that
agent next goes idle. Never poll `ls` or `ListAgents` in a loop for it.

It is still somebody else's session: send instructions rather than steering
their work, and say what you need back. The answer comes as a reply to the
`from` address your message arrived with.

**An agent with an empty `agent_name` is reached with `send`**, which types at
its window — one opened before treewright named agents, or a repository whose
command gives its agent no name:

    treewright send eng-142 "read /tmp/eng-142-review.md and address the comments in it"

It shows what the window is displaying before it types, and that is worth
reading; `treewright help send` has the rest.

## Clean up

    treewright rm eng-142
    treewright prune --yes

rm refuses a worktree with uncommitted changes or commits on no origin ref;
prune only takes worktrees that are both merged and clean. Neither touches a
scratch window — closing one is under Put a second agent on a question. The refusals mean
work that exists nowhere else: do not pass --force on your own judgment —
surface the refusal and let the person decide.

Do not pass --yes to rm. The only thing rm's --yes answers is this section's
question: it closes the work's tmux window with nobody asked, and that
decision is the person's — the question below exists to reach them. prune's
--yes is not the same flag: it confirms only the removals, and every window
still gets its question.

**From the base window, another window closes only once its agent has
yielded.** Closing a window stops its agent, and only that agent knows whether
anything would be lost, so `close` refuses until the agent has run
`treewright yield` in its own window. Ask before you remove the worktree, while
the agent still stands somewhere it can run a command, the way you reach any
agent — `SendMessage` to its `agent_name`, with `notify_when_idle: true`:

    SendMessage to "cibo-eng-142": "your window is about to be closed: if nothing in it would be lost, run treewright yield"

and `send` where its `agent_name` is empty:

    treewright send --repo cibo eng-142 "your window is about to be closed: if nothing in it would be lost, run treewright yield"

Then wait for its reply or the idle notice — with `send`, for its row's
`agent_state` to read `done` — and read `window_yielded` in
`treewright ls --json`. True means its window can be closed
on a yes. False means it said no or could not answer — a window held open on a
dead agent's output, a shell, an agent stuck `waiting` on a person — and the
window stays for the person to close from outside treewright's windows; say so
in the question. A refusal is the answer rather than an obstacle: there is no
flag past it, and no other spelling of the close to try.

**When you are the agent asked to yield**, run `treewright yield` only if
nothing you are doing would be lost: no work in flight, nothing uncommitted you
have not handed on. Otherwise say why not, to whoever asked, and do not yield.
It marks only your own window, and the next prompt you are handed takes it
back.

Removal leaves the work's tmux window open on a directory that no longer
exists, and with no tty on this end treewright names the command that closes
each one rather than asking:

    treewright close --repo cibo eng-142

**Slugs collide across repositories.** Two pieces of work called `fix` in two
repositories is the ordinary case, and by the time this window needs closing
the worktree that would have told them apart has been deleted. So the hint
treewright prints names the repository, and the command you run must too — copy
it as printed rather than shortening it. Never fall back to `tmux kill-window`
on a window index: it reaches whatever server that shell happens to find, closes
whatever window holds that index there, and exits 0 when it was wrong.

**A removed worktree always ends in AskUserQuestion: close its window?** The
question is the final step of the cleanup, unconditionally — there is nothing
to weigh first, and a cleanup reported done without it is a cleanup with a step
missing. Not skipped because the answer seems obvious, not skipped because the
window is your own, and never traded for the printed command in your summary:
that command is what you run on a yes, not what you hand back. On a no the
window stays and the cleanup is still done. prune can remove several worktrees
in one run, and each window it leaves gets asked about — one AskUserQuestion
carries them all. The only removal with no question is one where treewright
named no window, because none was open.

What treewright printed above the command goes in the question rather than
after the answer: an agent that is still `working` in that window stops when
the window closes, and a window that is the last in its session ends the
session with it, and detaches whoever was attached — which
`window_last_in_session` in the JSON also says. `close` takes the window and
nothing else; the worktree is already gone by then, and it finds the window
anyway.

**The window you are running in gets the same question**, and it is the case
the question exists for. An agent asked to tear down the worktree it is
standing in meets that window every time, so exempting it is an exception that
swallows the rule: nothing gets asked, and the person is handed back exactly
the printed command the paragraph above refuses to hand back. Being yours
changes the ordering of a yes, never whether you ask. Closing it ends this
session, so a yes on it is honoured last — after the final message rather than
instead of it. Report the teardown, say whatever is left to say, and then run a
bare `treewright close` as the closing action of the turn: with no name, close
takes the window it is running in and nothing else. It is the final step of the
cleanup you were asked for, and the session ending is what that step costs.

**Never work out which window is yours with `tmux display-message`.** Without
`-t "$TMUX_PANE"` it answers about whichever window the attached client is
showing, which is how a scratch agent once closed the base window believing it
was its own. `window_is_current` in `treewright ls --json` is the row that is
yours, and a bare close never needs to know.

## Leave to the machinery

- `treewright signal` is run by the agent's own hooks already; do
  not call it by hand.
- `treewright setup`, `shell-init`, `tmux-init`, `agent-init`, and
  `refresh` change a person's configuration; run them only when asked
  to. `refresh` rewrites files in every checkout and reloads their tmux
  key bindings, so it is theirs to run after an upgrade, not yours.

## Trying it out is not free

Driving real work through treewright is what this skill is for and needs no
care beyond the above. Standing up a scratch repo to see what a command does is
another thing: your shell inherits `$TMUX` from the session the person is
attached to, so the scratch repo gets a tmux session of its own and
`treewright new` switches their client into it — away from the work they were
watching, and back only by detaching. Aim an experiment at a server nobody is
attached to, with `TREEWRIGHT_TMUX_LABEL=twdemo` in its environment, or do not
run it.
