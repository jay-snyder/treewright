---
name: treewright
description: Manage parallel work in repositories that use treewright (tw) — a git worktree, tmux window, and agent session per piece of work. Use when starting a task in parallel, spawning another agent on one, putting a second agent on the main checkout for work that needs no branch (an investigation, a question, a pull request review), checking which worktrees and agents are in flight or need attention, resuming earlier work, or cleaning up merged branches. Use instead of raw git worktree in a treewright-managed repository.
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

One JSON object per checkout, and one per open scratch window:

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
  with `window_is_current` set is how an agent in one knows it is in one.
- `status`: `dirty` and `unpushed` mean work
  that exists nowhere else; `merged` has landed and is safe to
  remove; `active` is pushed and unmerged — an open pull request.
- `agent_state`: what the agent in that window last reported —
  `working`, `waiting` (blocked on a person), or `done`.
  Empty when nothing has signaled.
- `ahead`/`behind` measure against origin's base branch, and
  `null` means the comparison was impossible — unknown, not zero.
- `window_is_current` marks the window you are running in, and
  `window_last_in_session` the window whose closing would end its session.
  Both matter under Clean up, and both are false where no window is open.
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
the shape that reaches an agent whose window is already open, under Continue or
hand work onward.

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
both places.

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
and is how everything else reaches it: `treewright send retry-loop "…"`,
`treewright close retry-loop`, and `treewright resume retry-loop` to switch to
it. `review-36` and `ci-watch` tell whoever reads the window list what each
window is for; `ask2` tells them nothing. A name already answering for
something — a worktree's slug, an open scratch window, `base` or the base
branch — is refused, since those commands take any of them.

Unlike `base`, which always means the one base window, `scratch` always opens a
new window. `--prompt`, `--prompt-file`, `--repo` and a third positional that
renames the window all work as they do on `new`.

**Nothing about it outlives its window.** Once it is closed, or lost when the
machine restarts, there is nothing for `resume` to find, and `restore` does not
reopen it. So what it finds has to land somewhere that lasts before the window
goes — a file, a pull request comment, or a message back to whoever asked:

    treewright send --repo cibo base "findings are in /tmp/retry-loop-findings.md"

**Close one you opened once its answer is somewhere, and ask before closing one
you did not.** `treewright close <name>` ends that agent's conversation for
good — there is no resuming it — so never while it reports `working`, and rm and
prune never touch a scratch window, there being no worktree to remove. One the
person opened is theirs.

## Continue or hand work onward

    treewright resume eng-142 --prompt "address the review comments"

An unambiguous prefix of a slug is enough, and the expansion is reported; a
scratch window's name is taken in full. The prompt reaches the agent only when
the resume actually starts one: a window that was already open is switched to
instead, with a warning that the prompt went undelivered.

That warning is not the end of the road. The agent in that window is an ordinary
TUI on an ordinary tty, and `send` types at it — a worktree's agent by its slug,
a scratch window's by its name:

    treewright send eng-142 "read /tmp/eng-142-review.md and address the comments in it"

What the window is showing is printed before anything is typed, and it is worth
reading: an agent sitting on a question with options takes keystrokes as the
answer to it, so a message sent to one answers a question you never saw.
`--dry-run` shows that and sends nothing, for when looking is all you wanted.

One line. Enter is what submits in these TUIs, so a message with a newline in it
is refused rather than posting the rest as further turns: anything longer goes
in a file and the line names it, exactly as a long brief does. A message that
lands mid-turn is queued and picked up when that turn ends, so this works
whether the row reads `working` or `waiting`.

It refuses the window you are running in — typing at yourself puts the message
into this session, ahead of whatever you were answering — and a window whose
command has died and is being held open on its output, there being no agent
left in it to reach.

`waiting` is the case this is for — that agent is blocked on a person, and this
is how it gets unblocked without one walking over to the window. It is still
somebody else's session: send instructions, not keystrokes that drive their UI.
And a window a person is typing in is a window you will collide with, so this is
for reaching agents rather than interrupting people.

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
the printed command the paragraph above refuses to hand back.
`window_is_current` says which one is yours, and being yours changes the
ordering of a yes, never whether you ask. Closing it ends this session, so a
yes on it is honoured last — after the final message rather than instead of
it. Report the teardown, say whatever is left to say, and then kill the window
as the closing action of the turn: it is the final step of the cleanup you
were asked for, and the session ending is what that step costs.

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
