# Plugins

What a treewright plugin is, the rules every one of them is held to, and the
first of them: `tw reviews`, the GitHub review queue for the repository a pane
is standing in. Part of the design notes —
[`design-notes.md`](design-notes.md) has the rest, and
[`compatibility.md`](compatibility.md) says which of what follows is a promise.

treewright is growing, slowly, into a small terminal ecosystem. Tools that begin
life as somebody's scripts in `~/.local/bin`, wired by hand into their
`tmux.conf`, come in as opt-in parts of treewright when they are about the same
work treewright is: the worktrees, the windows, and the agents in them. Two
candidates so far, and this file is written to cover both:

- **reviews** — pull requests waiting on your review, in the repository the
  pane is standing in: a pill in the status line, a popup picker, and a review
  run in an agent window of its own. Built, and described in full below.
- **cmdq** — commands an agent queues for the person to run (a `sudo`, an
  interactive login), with a popup to run them from. Described at the end.

A plugin is **opt-in and separately scoped**. It is nothing an existing user
meets without asking, it may depend on the one thing it is about and on nothing
else, and it lives in the same binary as everything else. "Plugin" does not
mean "written in bash" or "loaded from somewhere else"; the sections below are
why it means neither.

---

## The rules

Six rules, agreed before anything was built. Each is here with what it costs
and what it was chosen over, since every one of them will look like an obstacle
to somebody adding the third plugin.

### Run from the binary; nothing is installed

**A plugin is a subcommand of treewright, and its code is inside the binary.**
A tmux line that runs `treewright reviews segment` names the plugin by
reference, so the next status-interval after an upgrade runs the upgraded code,
and there is no copy anywhere that could be older than the binary.

That is the whole point, and the history of this repository is the argument for
it. Every integration that left a copy somewhere grew the same three things to
cope with the copy: a fingerprint so the copy could say which treewright it came
from, a `refresh` path to rewrite it, and a `doctor` check to say when it had
not been. The shim, the tmux snippet and the agent plugin each have all three —
see "Upgrading treewright itself" in [`design-notes.md`](design-notes.md) — and
none of them is optional, because a copy that cannot be compared is a copy that
goes stale invisibly. A plugin that is never copied needs none of it.

Four alternatives were weighed, and each is a copy under another name:

- **Install the script into `~/.local/bin`**, which is where these tools come
  from. A file on somebody's `PATH` that treewright wrote is a file treewright
  has to update, compare and, on the day they stop using it, have them find and
  delete — the "there is no uninstall" argument in "What treewright is allowed
  to write", on a path that holds everything else the person ever installed.
- **git's and gh's extension model**, where any `treewright-<name>` executable on
  `PATH` becomes `tw <name>`. It is the right model for a tool whose extensions
  come from strangers, and the wrong one here: an extension is code treewright
  cannot hold to its output contract, its tmux discipline or its test suite, it
  drifts from the binary it calls at whatever pace its author upgrades, and it
  has to be found on the tmux server's `PATH` — the least reliable environment
  treewright already deals with.
- **A plugin directory read at runtime**, `~/.config/treewright/plugins/`. The
  same objections, plus a second registry to keep in step with the first.
- **Embed the script, and write it out to a cache before running it.** This is
  the subtle one, because the bytes come from the binary. But the file on disk
  is then a copy, with every question a copy raises: is the one on disk this
  build's, who removes it, what runs if two builds race to write it.

So a plugin's files, where it has any that are not Go, follow the rule the shell
shims and the agent plugin already follow — "What ships as a file is checked in
as a file, and named in Go one at a time" in `CLAUDE.md`. A plugin's script
would live under `internal/plugin/<name>/` and reach the binary through a
`//go:embed` naming that file and no other, with a test failing on any file in
the directory that nobody claims, as `TestEveryScriptIsDeclared` does for the
shims. It is run from memory — handed to `sh -c` as an argument, never written
out — so the only copy is the one in the binary. `reviews` has no script (see
"Script or Go" below), so the test that closes the directory arrives with the
first plugin that does.

**What a plugin writes is its own state, and only under a directory named after
treewright and the plugin.** `$XDG_CACHE_HOME/treewright/<plugin>/` for data that
can be fetched again, `$XDG_STATE_HOME/treewright/<plugin>/` for data that
cannot, each falling back to the XDG default under `$HOME` the way the registry
does — on macOS as well, where `~/Library/Caches` would be the platform's
answer, because a person removing treewright looks for it beside
`~/.config/treewright/` and not in a second convention. Every such path is a row
in "What treewright is allowed to write", so `rm -r` on one directory still
takes everything a plugin ever left.

### A plugin asks treewright; it never works out what treewright knows

This is the line `guard` drew, in "It is a subcommand rather than a shell
script" in [`agents.md`](agents.md): treewright already knows every worktree,
every registered repository, every window and the name of every agent in one,
and a reimplementation drifts from it the first time something moves.

For a plugin written in Go, asking means calling what core calls —
`resolveConfig`, `scratchSessions`, the tmux package's window lookups — and
never a second version of any of them. For a script it means the public surface
and nothing else: `ls --json`, `config`, the exit codes, and the `@treewright_*`
window options [`tmux.md`](tmux.md) declares for status lines. A script that
needs something none of those says has found the boundary of rule six: that
part is Go, or the fact goes onto the public surface — where it becomes a
promise, which is the cost that keeps the surface small.

**For `reviews` it settles which repository a pane belongs to.** The script it
came from read the pane's own `git remote`, which made "a repository" mean "any
directory with a GitHub origin". treewright already has a meaning for the word —
a registered config, found from the directory's main checkout — and the pill,
the click and the picker all use that one: the directory's main checkout is
matched against the registry exactly, and only then is that checkout's `origin`
read for the `owner/name` GitHub knows it by. A pane outside a registered
repository gets no pill. That is narrower than the script, deliberately: the
picker's whole point is handing a review to treewright, which can only happen in
a repository treewright manages, and a status line showing a queue its own
picker cannot act on would be two meanings of "repository" on one screen.

Exactly, rather than through `resolveConfig`'s fallback. With one config
registered, the ordinary resolution answers with it from any directory at all —
the trap the popup's `--dir` exists to close, described in [`tmux.md`](tmux.md)
— and a status line drawn from a pane in `/tmp` would show that repository's
queue. The guard resolves the same way, for the same reason.

**And the handoff goes through `scratch --reuse`**, called in-process with the
arguments a person would type. Every rule `scratch` holds — the name's slug
rules, the agent-name collision, the line-break refusal, what happens when the
window is open or was lost to a restart — holds for a review because it is the
same code, not because the plugin remembered to.

### Opt in through machinery that already exists

A plugin adds no new way of opting in. Each of the things a person turns on is
turned on where treewright already asks for that kind of decision.

**Key bindings are `tmux-init` flags, and empty by default.** `--reviews-key`
sits beside `--resume-key` and `--new-key` and binds nothing unless given a key.
That keeps "`refresh` refreshes what is installed and installs nothing new" true
without a special case: `refresh` reloads the bindings onto the keys a server
already holds them on, and a server that never bound the picker has no key for
`refresh` to find, so it binds none. A default key was the alternative, and it
would have meant every existing user's next `refresh`, or next server start
under `tmux-init --apply`, quietly took a key they had not given up for a tool
they had not asked for.

**What goes in the status line is printed for a person to place.** `tmux-init`
prints the `status-right` fragment and the click binding as comments in the
reviews section of its snippet, and `tw help reviews` prints them as well;
treewright never sets either. `status-right` is the person's own format — a
`set -g status-right` in a snippet would replace theirs wholesale, and the next
reload of their own `tmux.conf` would replace it back — so it is the
`set-titles` argument in [`tmux.md`](tmux.md) one step further: a title format is
kept in a file the person chose to load, and a status line is not kept even
there. The click binding is printed rather than bound for a sharper reason. It
takes `MouseDown1Status`, which stock tmux binds, and loading it would replace a
binding of tmux's own without a word — what `TestBindingsDoNotOverwriteTmuxsOwn`
exists to stop. Printed, it is a line the person adds knowing what it replaces,
and the binding keeps tmux's own behavior for every click that is not on a pull
request.

**Teaching an agent about a plugin goes through `features` and
`session-start`.** `reviews` needs none — nothing about it is for an agent to
use — but cmdq will: an agent has to know there is a queue before it can put
anything in one. The route is a feature in `internal/feature`, switched on per
repository in `features`, whose `session-start` hook prints the context at the
start of a session. Everything "Optional behaviors" in
[`design-notes.md`](design-notes.md) argues applies unchanged, and the reason it
is the route rather than a skill edit is the one given there: the plugin copy is
a snapshot, and a sentence added to it would reach no copy already installed,
where a feature runs in wiring installed today.

**Network work happens only on a path the person wired or typed.** The release
check is the model, in "Checking for a newer release" in
[`design-notes.md`](design-notes.md): explicit only, no background check, no
cache, nothing on `new` or `ls`. `reviews` refreshes on its own and keeps a
cache, and is still inside that rule, because placing the pill is the request.
A pill is a periodically refreshed view of GitHub or it is nothing; the person
who put `treewright reviews segment` in their status line asked for exactly the
background query the release check refuses to make unasked. What carries over
from the release check is everything else:

- **No core command ever calls it.** Not `new`, not `ls`, not `doctor` — which
  reports on the cache but asks GitHub nothing — and no agent hook. A person who
  never placed the pill, never bound the key and never typed `tw reviews` makes
  no request to GitHub, ever.
- **The cache exists because the caller is a status line**, not to make an
  automatic check cheap. tmux runs the segment every status-interval, for every
  attached client, and a query per redraw would be a rate limit within the hour.
  So the segment reads only the cache, and starts a refresh in the background
  when the cache is more than five minutes old, at most one at a time.
- **An unanswerable query is silent where nobody asked and spoken where somebody
  did.** The status line keeps the last good list when GitHub cannot be reached.
  The picker, which a person opened, says the list is stale and why; `tw reviews
  refresh`, which a person typed, says what failed; and `doctor` reports the last
  failure — read from disk, so an offline laptop still gets no network call made
  on its behalf.

### tmux goes through treewright

**"No tmux call lives outside `internal/tmux`" holds for plugins too.** The
script opened its picker with a bare `tmux display-popup`, which knows nothing of
`TREEWRIGHT_TMUX_LABEL` — so on a scratch server it opened on the default one,
over whoever was attached there. That is the shared-client hazard `CLAUDE.md`'s
scratch-server section is about, reached through a plugin instead of through a
demo. The picker now opens through `tw popup reviews`, the same command the
worktree picker uses: the label, `-EE`, the client and the directory are handled
once, and the popup is sized from the cache by the same `sizeFor` that sizes the
worktree picker.

The test is whether something reaches the tmux server, not whether it mentions
tmux. The segment prints a tmux format to stdout and calls nothing; the click
handler is run by tmux and calls nothing either.

### Dependencies: the thing a plugin is about, and nothing incidental

Core needs `git` and `tmux`. A plugin **may depend on the thing it is about** —
`gh` for a plugin about GitHub — and on nothing else.

**The dependency is checked and named.** `doctor` reports whether `gh` is on
`PATH` and whether it holds a login, from `gh auth token`, which reads the local
credential store and asks the network nothing. A plugin command that cannot run
without it refuses in one sentence naming what is missing and how to get it.
The one exception is the segment, which prints nothing whatever goes wrong — a
status line has nowhere to put an error — and records the failure for the
picker and `doctor` to report instead, which is "A background failure needs
somewhere to be reported" applied to a refresh nothing waits for.

**Incidental tooling is ported away, not depended on.** The script needed BSD
`stat -f` and `date -v`/`-j` for its timestamps, `awk` and `sort` for its cache,
and bash for its arrays. On Linux each of those is a different program or a
different flag, and each would be a dependency of treewright's that nobody chose.
In Go they are a few lines each and work the same on both of the platforms CI
runs. `gh`'s own `--json` replaces its `--jq`, so jq is not needed either.

Opening a pull request in the browser is the one call that is still a program,
because Go has no standard way to do it: `open` on macOS, `xdg-open` elsewhere,
which is how the Go toolchain does it too. Those are the desktop's own URL
handlers rather than tooling — what "open this link" means on each platform — and
`gh browse`, the alternative that would have used only the dependency already
allowed, makes an API request before it opens anything. A machine without
`xdg-open` gets a click that does nothing, which is also what a click on a pull
request that has left the cache gets.

**`gh` is found on the `PATH` that found treewright.** tmux runs a status-line
job and a `run-shell` binding the same way: `/bin/sh -c` in the server's global
environment, which is whatever environment the server started in. The bindings
`tmux-init` prints have always spelled `treewright` bare for that reason, and it
works wherever the server's `PATH` holds the binary — "Why a binding that cannot
find treewright says nothing at all" in [`tmux.md`](tmux.md) is the case where it
does not. The pill is one more line of the same kind, so wherever it can run
treewright at all, `gh` is looked up on that same `PATH`.

The script hard-coded `/opt/homebrew/bin/gh` instead, with a comment that tmux
runs `#()` with a minimal `PATH`. That is true of a server started by something
other than a login shell and false of the usual one, and an absolute path is
right on exactly one kind of machine. Two other answers were weighed and lost. A
list of well-known directories to try after `PATH` is a guess with a stale
entry waiting in it, and finds a `gh` the person's own shell would not run. A
config key or variable naming the binary is a setting for a fact the
environment already holds. What the plugin does instead is make the environment
visible when it is wrong: a refresh that finds no `gh` records the `PATH` it
searched, so the failure the picker and `doctor` report puts the server's `PATH`
in front of the reader, rather than leaving a status line that is simply empty.

### Script or Go

**Language is secondary; what a part touches decides it.** A script is fine
where it only consumes treewright — its public CLI and window options, rule two —
and calls no tmux, rule four. The moment a part needs either, that part is Go.
Two further costs lean the same way. The coverage floor is measured in Go, so a
script is code CI does not measure unless it brings tests of its own that run
there, as `TestScriptsParse` does for the shims. And a script is run by `sh`,
not bash, since a particular bash is incidental tooling under rule five — macOS
still ships bash 3.2, and the script `reviews` came from uses arrays that need
4.

**`reviews` is Go, entirely.** Its picker opens a popup and its handoff opens a
window, so both are Go by rule six before anything else is weighed; it resolves
repositories from the registry, which is Go by rule two. What would have been
left for a script is the refresh — five `gh` queries and some joining — and that
is exactly where the script's portability lived: the `stat`, the `date`, the
`awk` and the bash 4. Splitting would have kept the part that most needed
porting in the language that made it need porting, with a second test harness
to cover it. In Go the refresh is a function given a way to run `gh`, tested
against canned answers, and end to end against a stub `gh` on `PATH` the way the
suite stubs `claude`.

---

## How a plugin is named and reached

**One top-level command per plugin, named after what it shows**, with
sub-commands for the parts tmux runs rather than a person. `tw reviews` is the
picker; `tw reviews segment`, `tw reviews click` and `tw reviews refresh` are
what a status line, a mouse binding and the segment itself call. A person types
the first and pastes the other three.

**It is in the command table, and `tw help` lists it under a heading of its
own.** Being in the table is what makes it a treewright command: dispatch,
`--help`, the usage error that prints its help, completion in all three shims,
and `TestEveryFlagIsDocumented` all work without a special case. The separate
heading — `plugins:`, after the core commands — is what keeps it separately
scoped in the one place a newcomer reads the whole list, so the first screen of
`tw help` is still the worktree tool, and a plugin reads as something that can
be ignored.

The alternative was a namespace, `tw plugin reviews …`. It adds a word to every
line pasted into a `tmux.conf` and every name typed, and buys nothing: plugin
names and command names share one namespace either way, since a plugin named
after a future core command would collide with it whichever word came first.

A plugin's command does not take a repository positionally, because its first
positional is a sub-command and a config named `segment` would make `tw reviews
segment` mean two things. It takes `--repo`, like every command that resolves a
config, and the parts tmux runs take `--dir`, like `popup`, since what tmux knows
is a pane's directory and not a config's name.

---

## reviews

The queue of pull requests waiting on your review, for the repository the pane
is standing in.

```
tmux session "storefront"          status line:   PR #412 #418
├── main      ~/code/storefront                      │
├── eng-2318  …                     prefix + R ──────┴─ picker:
└── review-418  "review: 418"                            PR    AGE     AUTHOR  KIND        TITLE
                                                      1)  #418  2d ago  dana    re-request  Retry the payment webhook
                                                      2)  #412  today   lee                 Move cart totals server-side
                                                      3)  #377  3w ago  dana    commented   Drop the legacy coupon path
```

### What it shows, and why the two surfaces disagree

**The pill is what other people are blocked on**: requests that are fresh — made
in the last fourteen days — and none of your own pull requests. **The picker is
everything the cache knows**: stale requests too, marked by the color of their
age, and your own open pull requests, so you can put a reviewer on your own work
before asking a person to. The pill is a thing to glance at and has to stay
short to stay read; the picker is where opting into extra work happens.

Every row carries a kind, and both surfaces show it:

| Kind | Meaning | Pill | Picker |
|---|---|---|---|
| new | Requested, and you have not reviewed it | plain | (blank) |
| re-request | Requested again after a review you submitted | underlined | `re-request` |
| changes | Your latest review requested changes | dim | `changes` |
| commented | Your latest review was comment-only | italic | `commented` |
| yours | Your own open pull request | not shown | `yours` |

GitHub drops a review request the moment any review is submitted, so the
`changes` and `commented` rows are rebuilt from a second search — pull requests
you have reviewed — with your own latest review on each looked up one at a time,
since search can filter only on a pull request's overall decision, which a
colleague's approval satisfies. They stay until you approve, the pull request
closes, or its author asks again, which moves it back into the queue as a
re-request. Replying in a thread, or submitting with the "Comment" choice still
selected, records a comment-only review; a round of changes meant as a request
that lands as `commented` is what the separate kind is there to show.

The script showed kinds as Nerd Font glyphs. Words take more columns and need no
particular font, and a plugin of treewright's cannot assume a font treewright
does not otherwise need.

### One cache, read through one repository

**The refresh is repository-blind**: every query is scoped to `@me`, the cache
holds every repository you have pull requests in, and each surface reads one
repository's rows out of it. Filtering at read time is what makes a second
repository free — no per-session configuration, nothing to add when a repository
is registered.

**Each surface reads the repository its pane is in.** tmux draws the status line
per client and expands `#{pane_current_path}` inside `#()` before running the
job, so each attached terminal runs its own segment with its own path, and a
terminal per repository gets a pill per repository. The picker is handed the
same path by its binding, which is what makes the handoff sound: the scratch
window it opens is opened in the repository the list was about.

The cache is `${XDG_CACHE_HOME:-~/.cache}/treewright/reviews/`: the pull requests
as JSON, the lock a refresh holds, and the last refresh's failure when it
failed. Its format is not promised to anybody. A refresh that fails keeps the
previous cache rather than writing part of a new one, and a lock older than two
minutes is broken, so a refresh killed halfway cannot wedge the next. A refresh
costs four searches and a user lookup, plus one request for each pull request you
have reviewed that is not already in the queue.

### The pill

**The segment reads the cache and asks git one question**, both local, because
tmux runs it every status-interval. It prints nothing at all when nothing is
waiting or the pane is outside a registered repository, so an empty queue leaves
the status line exactly as it was.

**Each number is a status-line range named after it alone.** tmux keeps a user
range's name in a sixteen-byte field and cuts anything longer without a word —
`pr-acme/storefront:412` reaches the click handler as `pr-acme/storefr` and matches
nothing. So the range is `tw-pr-<number>`, prefixed so it cannot collide with a
range of the person's own, and the repository comes from the clicking pane,
which is the pane the pill was drawn for.

**Its look is neutral, and the person's to change.** The script's pill was one
person's theme: hex colors tuned to it, powerline caps and a Nerd Font GitHub mark.
treewright's draws `PR` and the numbers in reverse video, which stands out in any
theme without naming a color, and marks the kinds with text attributes —
underline, dim, italic — rather than hues, since an attribute reads against
whatever background the person picks and a hue chosen against one background
vanishes on another. Each number resets to the pill's style after it, so a kind
never leaks into the next. `--style` replaces the pill's style with any tmux
style, and `--label` replaces `PR` — which is how a person who wants their
GitHub glyph back puts it there:

```tmux
set -g status-right "#(treewright reviews segment -d '#{pane_current_path}' --style 'fg=black,bg=red,bold')<your status-right>"
```

### The click

`tw reviews click -d <dir> <range>` opens the pull request a range names, and is
what the printed `MouseDown1Status` binding runs for a click on a number. It
looks the number up among the rows the pill could have drawn — never a `yours`
row, since a pull request that is both yours and requested of you is cached
under both kinds and only the request is in the pill — first in the pane's
repository, then by number alone, which covers a pane that has since left it.
**A miss exits 0**: tmux reports a binding's failing exit status as a message
over the status line, which is what a stale cache would otherwise look like.

The binding, which the person places:

```tmux
bind-key -T root MouseDown1Status if-shell -F '#{m:tw-pr-*,#{mouse_status_range}}' \
    { run-shell -b 'treewright reviews click -d "#{pane_current_path}" "#{mouse_status_range}"' } \
    { switch-client -t = }
```

User ranges fire the plain `Status` mouse key, not `StatusRight`, and stock tmux
binds that key to `switch-client -t =`; the second branch is that binding kept
for every click that is not on a pull request.

### The picker, and the handoff

`tw reviews` lists the repository's rows, newest first and strictly by date —
your own interleaved rather than sunk to the bottom, the kind column being what
marks them — and choosing one opens a review in a scratch window of that pull
request's own:

```
treewright scratch --reuse --prompt "<review_prompt>" review-<number> "review: <number>"
```

A scratch window because a review needs an agent and no branch, and leaves
nothing on disk to clean up. `review-<number>` because a scratch name follows a
slug's rules, which allow neither the colon nor the space, and `review: <number>`
as the window's title. `--reuse` because a re-request should land in the
conversation that did the first review: it opens the window the first time,
reaches the open one after that, and reopens it on its conversation after a
restart. Closing it once the review is posted is the person's, with
`tw close review-<number>`.

**What is typed is the repository's `review_prompt`**, with `{number}` replaced
by the pull request's number. The default is plain words that any agent can act
on, `review pull request #{number}`; a repository whose agent has a command for
it sets its own:

```toml
review_prompt = "/review {number}"
```

It is a per-repository key because what starts a review is a fact about the
repository's agent and its tooling, not about the person. A value without
`{number}` is refused when the config loads, since every pull request would then
be handed the same instruction, and so is one with a line break, which `--reuse`
would refuse at the moment of the pick instead — the config being the earlier
and the clearer place to hear it.

**A prompt that went undelivered keeps the popup open.** `scratch --reuse` does
not type at an open window whose agent can be messaged by name; it switches to
the window and says the prompt was not delivered. In a popup that closes on
success, that sentence would be gone before it was read, so the picker exits
non-zero in exactly that case and the popup holds it.

**An empty picker says which of the two nothings it is** — no registered
repository under the pane, or a repository with nothing waiting — and either way
the popup stays up until it is read. The first is an error, which `-EE` holds
open as it holds any; the second is not a failure, so the picker waits for a key
itself rather than exit non-zero for an answer that was simply "none".

The popup is sized from the cache before it opens, by `sizeFor`, the way the
worktree picker's is: a row per pull request, columns as wide as their widest
cell, the whole capped at 110 columns, with titles cut to fit what the popup
turned out to be.

### Opting in

All three are lines a person adds, and each works without the others:

| To have | Add |
|---|---|
| The picker on a key | `run-shell 'treewright tmux-init --apply --reviews-key R'` — or the flag on whichever `tmux-init` line is already there |
| The pill | the `#(treewright reviews segment …)` fragment, in your own `status-right` |
| Clicking a number | the `MouseDown1Status` binding above |

`tw reviews` works typed at a prompt with none of them. Nothing in the generated
config mentions the plugin except `review_prompt`, as a commented default.

---

## cmdq

Commands an agent needs a person to run — a `sudo`, an interactive login,
anything that wants the person's password or their terminal — put in a queue
the agent can wait on, with a list in a popup to run them from. It began as a
bash script in `~/.local/bin`, and this section is what was kept, what was
ported away, and the decisions the port had to make.

```
prefix + Q:

  waiting to run · 2   ↑↓ pick  Enter run  d dismiss  h history  q close

  storefront · queued just now
  log gh in, so the reviews plugin can refresh
  $ gh auth login
  in ~/code/storefront

▌ storefront-eng-2318 · queued 4m ago
▌ install the toolchain the build now pins
▌ $ sudo installer -pkg ./toolchain.pkg -target /
▌ in ~/code/storefront-eng-2318
```

Newest first, as the script had it, so a command queued while the list is open
arrives at the top.

### What it keeps

The script is trusted for a handful of properties, and each is a rule here with
a test of its own:

- **Only Enter in the list runs a command.** Nothing else ever runs one — not
  `add`, not a refresh, not a key typed during the last run — and nothing
  announces itself: no notification, no bell, no window opening on its own.
- **The text shown is the text run.** An entry's command is read once, when the
  list lays it out, and that string is what Enter hands to the shell. A file
  rewritten between the two cannot slip a different command in.
- **Hidden characters are shown, never sent to the terminal.** C0 controls
  appear as their Control Pictures (ESC as ␛, tab as ␉), DEL as ␡, and the
  zero-width and direction-override characters (U+200B–200F, U+202A–202E,
  U+2060–2064, U+2066–2069, U+FEFF) as �, highlighted — and so are the C1
  controls, which some terminals read as escapes of their own, and bytes that
  are not UTF-8, which would otherwise reach the terminal raw. Newlines stay
  newlines.
  This holds for everything printed that somebody else wrote — the reason, who
  asked, the directory and the output kept for `status` — and not only the
  command, since an escape sequence in a `--why` can repaint the command
  beneath it.
- **The pick is an entry, not a row.** The list rereads the queue every few
  seconds, and an entry queued meanwhile does not slide a different command
  under the cursor. When the picked entry has left the list elsewhere — another
  popup ran or dismissed it — Enter does nothing until the person picks again,
  and the screen says so.
- **An entry only partly on screen is scrolled into view before Enter runs
  it**, so nothing runs that the person could not see whole: the first Enter
  brings it into view and says so, and the next one runs it. One taller than
  the popup runs as it is, there being no view that shows it all.
- **Keys typed while a command ran are drained** before the list reads another,
  so they do not answer it. Ctrl+C during a run goes to the command and the list
  survives it; Ctrl+Z in the list is ignored, since it would stop the popup's
  only program.
- **`add` refuses** an empty command, a NUL byte, an empty `--why` and a
  directory that does not exist. IDs are unique under concurrent `add`s: an
  entry is written in a staging directory and renamed into the queue, a rename
  that fails rather than replace an entry already there, and a clash retries
  with a fresh ID.
- **A failed run stays on the list**, marked "last run failed: exit N"; a
  successful one moves to the history. `d` dismisses, `h` shows the history,
  `u` there puts an entry back, and the history keeps the last fifty.
- **`status` shows the last run's final twenty lines**, escape sequences
  stripped.
- **`wait` returns** once the entry ran, a run failed after the wait began, it
  was dismissed or withdrawn, or the timeout passed — an hour unless told.

### What it leaves behind

The script's key was a terminal emulator's mapping, and its messages named it.
A plugin of treewright's cannot know what somebody's terminal is mapped to, so
every message names the key `tmux-init` bound, read from the server the way
`newWorktreeKeys` reads the worktree picker's, and `tw cmdq` where none is.

Its interpreter was a particular bash from Homebrew, its `date` and `mv` were
GNU's through Homebrew's `gnubin`, and its pseudo-terminal came from python.
All of that is incidental tooling under rule five, and Go does every part of it.

Its queue at `~/.local/state/cmdq/` is not read. The two live side by side until
the script is retired by hand, and an entry in one is invisible to the other.

### Where the queue lives, and who asked

**One queue per machine**, under `${XDG_STATE_HOME:-~/.local/state}/treewright/cmdq/`
— state rather than cache, because a queued command cannot be fetched again from
anywhere. Not one per repository, as `reviews` reads: the list is everything
blocked on the person, and a `sudo` an agent in one repository asked for is no
less theirs because the popup was opened from another repository's pane. Each
entry says who asked, and the name says which repository. Inside the directory
are `pending/`, `history/` and `staging/`, a directory per entry and a file per
field, as the script had them; the layout is promised to nobody, and the IDs
`add` prints are what `wait`, `status` and `drop` take.

**`--from` defaults to the name of the agent asking**, read off the calling
window's `@treewright_agent_name` through `tmux.CallersWindow`. The script used
the git toplevel's folder name, which cannot tell the base agent from a scratch
agent, both standing in the main checkout, and agents took to improvising names
to make up for it. The agent's name is the one treewright gave it and the one
other agents message it by, so the list, `ls` and a message all call it the same
thing. Outside a window with a name it is the registered repository's config
name, and outside a registered repository the checkout's folder name.

### Running a command: in the popup, under a terminal of its own

"A pane plus `pipe-pane`" was the shape this section first proposed, on the
grounds that tmux is already a pseudo-terminal per pane. It was prototyped, and
it works as far as it goes: the command sees a terminal on stdin and stdout, a
password typed at a no-echo prompt stays out of the capture, everything else is
in it down to the escape sequences, and the exit status can be written beside
it. What sinks it is that **a popup is not a pane**. `pipe-pane` takes a pane,
and a popup has none for it to take, so the run would have to leave the popup
for a window or a split. What that costs the person, in order:

1. **The list goes away, and does not come back.** The script's popup gives way
   to the command and returns when it ends. Here the popup closes, a window
   opens, and when the command ends the window closes too, leaving whoever ran
   it where they started with the list gone. Bringing it back means the window's
   wrapper opening a popup on the client that pressed Enter — a second trip
   through `treewright popup`, with a window flashing past in between.
2. **A window appears in somebody's session.** A split was the alternative, and
   is worse: `send` and `capture-pane` aim at a window's active pane, so a split
   under an agent's window would put the next `tw send` to that agent into the
   password prompt.
3. **A handshake, in shell.** The command must not print before `pipe-pane` is
   attached, so the wrapper blocks on `tmux wait-for` — a shell line in the
   window carrying the server flags, and one more place `TREEWRIGHT_TMUX_LABEL`
   has to arrive intact, with an escape to the default server the price of a
   slip.
4. **The list would only work inside tmux.** The script's runs in any terminal,
   so somebody ssh'd in with no client attached can still clear their queue.

tmux 3.8's floating panes are a pane that sits over a window as a popup does,
which would answer the first two. But Ubuntu's LTS ships tmux 3.4, which is what
CI installs, and a plugin that needs the newest tmux is a dependency nobody
chose.

So the run stays where the script had it: **in the popup, under a pseudo-terminal
treewright opens itself.** The code is Go and `golang.org/x/sys`, which is
already in `go.mod` as a requirement of `x/term` and moves from indirect to
direct — no new module. Opening one is `/dev/ptmx` and three ioctls whose names
differ between macOS and Linux, a short file per platform. The command starts as
the leader of a session of its own with the pty as its controlling terminal, so
`sudo` asks for its password as it would at a prompt. The popup's terminal is
in raw mode while the list is open, so during a run every key, Ctrl+C among
them, is copied to the command rather than read by the list; output is copied
to the screen with the last 64 KiB of it kept for `status`; and the pty's size
follows the popup's. That is the shape the python had, in the language the rest
is written in, and it runs on both CI platforms under the test suite, which
needs no terminal of its own to drive one.

Two details are easy to get wrong. **One reader takes the person's keys for as
long as the list is open**, and hands them to the command during a run and to
the list between runs: a read cannot be taken back, so a reader the run had
left blocked on the terminal would swallow the first key typed at the list
afterwards. And **the output is read with a timeout rather than a plain read**,
because something the command started in the background can hold its terminal
open, and print on, after the command itself has exited; what it prints in the
next three seconds is shown, and then the list comes back, as the script's did.
On macOS the name of the pty's other end comes from an ioctl that fills a
buffer, which none of x/sys's wrappers takes, so that one call goes through the
standard library's `syscall`.

The command runs under `bash -c`, in the entry's directory, as it did under the
script. Every shell command an agent writes is written for a bash-like shell,
and a person's login shell is not one to guess at: a queued command handed to
fish would fail on its first `&&`.

### What an agent is told, and what `wait` says back

**Agents learn about the queue through a feature**, `cmdq`, switched on per
repository in `features` and explained by `session-start` at the start of each
session — the route "Opt in through machinery that already exists" sets out.
What it says is written for an agent: queue a command only when it needs the
person (a `sudo`, an interactive login), never as a way to hand off work it can
run itself; pass it on a quoted heredoc so nothing in it is expanded on the way
in; and start `tw cmdq wait <id>` in the background straight after `add`, so the
answer arrives as soon as there is one. `add` prints the ID on stdout and on
stderr which key opens the list, for the agent to pass on.

**`wait`'s exit codes are the script's, but for the one that collided:**

| Exit | Meaning |
|---|---|
| 0 | it ran, and exited 0 |
| 1 | an error: no such entry, or a queue that could not be read |
| 2 | invoked wrong, as everywhere in treewright |
| 3 | dismissed by the person, or withdrawn with `drop` |
| 4 | still waiting when the time ran out |
| 5 | a run failed after the wait began, and the entry stays waiting |

The script said 2 for a failed run. In treewright 2 is a usage error, so an
agent that mistyped a flag would have read "the run failed" and reported it.
That one number moves, to 5, and the rest keep their meanings, so an agent still
holding the old instruction misreads nothing but the number that had to change.
Whatever the code, `wait` prints the entry's status on stdout first, so the
outcome also arrives in words.

The numbers are chosen in `main`, as every exit code is. `wait` returns an error
carrying its outcome — reported already, so `main` prints nothing for it — and
`main` holds the table from outcome to number beside its other three. They are a
machine-read contract from the first release, since an agent's next step is
built on them, and [`compatibility.md`](compatibility.md) lists them with the
rest.

### The asking agent's window keeps its own state

An agent that queues a command and ends its turn is blocked on the person, and
its window says otherwise: the agent's Stop hook signals `done`, "a result to
look at", and the name gets no `!`. Reading `waiting` instead would need either
`signal done` looking for a pending entry from that window — core consulting a
plugin, which nothing does — or the plugin writing a state the agent's next hook
overwrites a moment later.

So this does neither, and the state stays the agent's. Its last message says
what it queued and which key opens the list, so `done` still sends the person to
the right place, without the `!`. The shape that would fix it belongs to
`@treewright_agent_message`, which [`agents.md`](agents.md) reserves for "a short
why": a window option core owns, that a plugin may write and `signal` reads, so
the dependency runs from the plugin to core and never back. Designing it is a
compatibility step of its own.

### An entry whose directory has gone

`tw rm` takes a worktree away and leaves any entry queued in it pointing at
nothing. The dependency runs one way — cmdq knows about treewright, and core
knows nothing about cmdq — so `rm` does not look in the queue and says nothing
about it. The list does the work instead: such an entry is marked as gone, Enter
on it says so in place rather than clearing the screen to say it, and `d`
dismisses it. It is not retired on its own. `wait` would report a dismissal
nobody made, and the command is still worth reading, perhaps to run somewhere
else. `doctor` counts such entries in its cmdq section.

### A command aimed at another agent's worktree

`guard` refuses an agent's tool call that would change a worktree other than its
own, and never refuses treewright's own commands, since they name other
worktrees constantly. Without a check of its own, cmdq is the way around that
refusal, one Enter away: a command the guard turned back, queued for a person to
run in the other worktree instead — and a person reading a list of commands
from several agents is the reader least likely to notice whose worktree a path
is in.

So **`add` asks the guard's question**. The queued command is read by the
guard's own shell reader as a Bash call starting in `--cwd`, and refused when
the guard would refuse it — which covers a `--cwd` in another worktree and a
`cd` into one inside the command alike. Reads stay allowed, as they are for the
guard, and the refusal names the handoff the guard's names. Outside the guard's
scope — no tmux, no registered repository, no worktree but the caller's own —
there is nothing to refuse.

### The screen

The list is not `internal/ui`'s picker. That one numbers single-line rows and
must never be handed a newline, where an entry here is several lines: who and
when, how its last run went, the reason wrapped at words, the command wrapped
hard so every character stays where it is, and the directory. It lives in
`internal/plugin/cmdq` as a screen of its own — the alternate screen, raw mode
from `x/term`, redrawn every few seconds and on a resize. The layout — the
wrapping, which lines belong to which entry, where the view sits and which entry
is picked after a refresh — is a function of the entries and the size alone, and
tested as one; the key loop reads from a reader the tests script. Color follows
`internal/ui`'s rules, off under `NO_COLOR` and on a dumb terminal, and the
stand-ins for hidden characters are in reverse video, so they are seen with or
without it. A second plugin wanting a list of multi-line entries is the time to
move it into `internal/ui`, and not before.

The popup is sized to most of the terminal it opens on, since a command's
output is what it ends up holding, and nobody can predict how wide that is.

### Opting in

| To have | Add |
|---|---|
| Agents that know the queue is there | `"cmdq"` in the repository's `features` |
| The list on a key | `--cmdq-key Q` on the `tmux-init` line |

`tw cmdq` works typed at a prompt with neither, in any terminal. The key has no
default, for the reason `--reviews-key` has none.
