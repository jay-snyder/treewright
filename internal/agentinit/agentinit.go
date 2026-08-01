// Package agentinit holds what treewright knows about particular coding agents.
//
// treewright's core is agent-agnostic on purpose: it provides neutral protocols
// — a command to launch, a `signal` verb for state — and knows no agent's name.
// But the facts about a particular agent are facts every user of that agent
// needs identically: what launches it, what resumes it, which gitignored files
// hold its per-project state, and how its hooks are wired to `signal`. This
// package is where those facts live, one module per agent, so that supporting
// another agent is a file beside claude.go rather than edits across the tree.
//
// Two consumers read the modules. `treewright agent-init <agent>` prints the
// agent's hook configuration, emitted by the binary the way the shell shims and
// the tmux snippet are, so it can never drift out of sync with the `signal`
// vocabulary it targets. And a config's `agent` key names a module to take
// launch defaults from — and to have the agent's local-state files carried into
// every new worktree, which is what makes per-repo hooks reach the worktrees
// they exist for.
package agentinit

import (
	"sort"
)

// Agent is one agent module.
type Agent struct {
	// Name is what the config's `agent` key and `agent-init` call it.
	Name string

	// Command launches the agent fresh; ResumeCommand picks up where it left
	// off. They are the defaults a config naming this module gets for its
	// command and resume_command, each still overridable in the file.
	Command       string
	ResumeCommand string

	// ProjectSettings is where the agent reads a checkout's own configuration,
	// relative to its root, and is where treewright's wiring belongs by
	// default: a repository you use treewright in gets it, and one you do not
	// stays untouched. Its counterpart UserSettings is the same wiring made
	// global, for someone who wants every repository covered at once.
	ProjectSettings string
	UserSettings    string

	// ProjectSkillPath and UserSkillPath are the same split for the skill: the
	// checkout's own skills directory, and the agent's user-level one.
	ProjectSkillPath string
	UserSkillPath    string

	// Hooks is the configuration fragment agent-init prints: the agent's own
	// hooks wired to `treewright signal`. It spells the canonical binary name
	// throughout, being destined for a file a program reads.
	Hooks string

	// Skill is the document teaching the agent to drive treewright — the
	// reverse of Hooks, which teaches treewright about the agent. Each module
	// wraps the shared drivingGuide below in its own agent's packaging.
	Skill string
}

// LocalState are the per-project files a config naming this module has carried
// into every new worktree — silently skipped when the main checkout has none,
// since unlike a carry_files entry nobody asserted they exist.
//
// Derived from the project paths rather than listed beside them, because the
// two must not disagree: a module that gained a per-project artifact and did
// not also carry it would put that artifact in the main checkout and in no
// worktree, which is the trap the carry exists to close. Deriving it means a
// module cannot describe a per-project file it forgets to carry.
//
// Whether these end up in git is the repository's business, not treewright's.
// The agent's settings file is conventionally ignored already; the skill is a
// file like any other, and a developer manages their own .gitignore.
func (a Agent) LocalState() []string {
	var out []string
	for _, p := range []string{a.ProjectSettings, a.ProjectSkillPath} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// drivingGuide teaches an agent to drive treewright, and it is deliberately a
// package-level document rather than claude's: the knowledge is the CLI's own
// — the same commands whoever runs them — and only the packaging around it
// (frontmatter, install path) belongs to a module. A second agent wraps this
// same text.
//
// Every command in it spells the canonical binary name. That is the Argv0
// rule's own case made sharper: the consumer is an agent running commands in a
// non-interactive shell, where `tw` — a function defined by the shell
// integration in a startup file — may simply not exist, while the binary is on
// PATH under its own name.
//
// What it teaches is held to the code by tests in internal/cli: every command
// it names must exist in the command table, every flag in that command's
// parser, every JSON field in the ls schema — so a rename that forgets the
// guide fails the build rather than teaching agents a CLI that is gone.
const drivingGuide = `# Driving treewright

treewright gives each piece of work its own git worktree, tmux window, and
agent session, created and torn down together. In a repository it manages,
reach for it before ` + "`git worktree`, `git branch`, or `git checkout -b`" + `:
it also copies gitignored env files into the new checkout, runs the configured
install step, names the tmux window after the work, and guards teardown.

Run it as ` + "`treewright`" + `. The short ` + "`tw`" + ` is a shell function
from the interactive shell's startup file, and may not exist in the shell
running your commands.

## See what is in flight

    treewright ls --json

One JSON object per checkout:

- ` + "`\"base\": true`" + ` marks the main checkout. It is not a worktree,
  never a target for rm or prune, and new work should fork from it rather than
  happen in it.
- ` + "`status`" + `: ` + "`dirty`" + ` and ` + "`unpushed`" + ` mean work
  that exists nowhere else; ` + "`merged`" + ` has landed and is safe to
  remove; ` + "`active`" + ` is pushed and unmerged — an open pull request.
- ` + "`agent_state`" + `: what the agent in that window last reported —
  ` + "`working`, `waiting`" + ` (blocked on a person), or ` + "`done`" + `.
  Empty when nothing has signaled.
- ` + "`ahead`/`behind`" + ` measure against origin's base branch, and
  ` + "`null`" + ` means the comparison was impossible — unknown, not zero.
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
  configured branch prefix — ` + "`treewright new bug/eng-142`" + ` — and one
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

- ` + "`treewright signal`" + ` is run by the agent's own hooks already; do
  not call it by hand.
- ` + "`treewright setup`, `shell-init`, `tmux-init`, and `agent-init`" + `
  change a person's configuration; run them only when asked to.
`

// modules is the registry, keyed by name. One entry today; a second agent is a
// file defining its Agent and an init adding it here.
var modules = map[string]Agent{
	claude.Name: claude,
}

// Names lists the modules, sorted, for errors and completion.
func Names() []string {
	names := make([]string, 0, len(modules))
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Lookup finds a module by name.
func Lookup(name string) (Agent, bool) {
	a, ok := modules[name]
	return a, ok
}
