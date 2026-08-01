package agentinit

import "embed"

// The Claude Code module.
//
// Everything treewright installs for Claude Code is one directory — a
// skills-directory plugin, which is any folder under a skills directory holding
// a .claude-plugin/plugin.json manifest. Claude Code loads it as
// `treewright@skills-dir` on its next start, with no marketplace, no install
// step, and no settings file edited. The layout, which is the layout of
// plugins/claude in this package:
//
//	.claude/skills/treewright/
//	├── SKILL.md                    the driving guide
//	├── .claude-plugin/plugin.json  the manifest that makes it a plugin
//	└── hooks/hooks.json            the wiring, in settings' own hooks format
//
// Only plugin.json goes inside .claude-plugin/; hooks/ and SKILL.md are at the
// plugin root, and Claude Code's own documentation names putting them inside
// the manifest directory as the common mistake. A plugin shipping exactly one
// skill puts its SKILL.md at the root rather than under skills/, which is what
// leaves the path treewright already claimed unchanged.
//
// That path is the point. `.claude/skills/treewright/` was already treewright's
// own directory, holding the skill; the hooks used to be printed as a JSON
// fragment for the user to paste into a settings file treewright would never
// read again, because JSON has no `eval "$(...)"` to make the copy live the way
// the shell shim and the tmux snippet are. A paste is a snapshot: rename a
// signal verb and every installed user is silently wired to the old one. The
// manifest turns the directory treewright was already writing into a place the
// hooks can live too, which makes the wiring treewright's to keep current.
//
// The hook mapping is the whole of the wiring: each hook fires on one of the
// agent's own transitions and reports the matching signal state, so the AGENT
// column of `ls` answers which window wants a person.
//
//	UserPromptSubmit  the user sent a prompt; the agent is about to work
//	Notification      the agent needs permission, or has sat idle waiting
//	Stop              the agent finished responding; there is a result
//	SessionEnd        the claude process ended while its window lives on
//
// SessionStart deliberately maps to nothing. A fresh window sits at the
// agent's prompt because the human just made it, and signaling `waiting` there
// would make every `new` open a window already demanding attention.
//
// `--continue` is what makes resuming per-worktree exact: each worktree is its
// own directory, and claude resumes the session that last ran in the directory
// it is started from. {prompt} is where a --prompt lands, as a positional
// argument in both templates — claude takes an initial prompt that way fresh
// or resumed. The templates must agree with the config package's defaults,
// which a test holds them to.
var claude = Agent{
	Name:            "claude",
	Command:         "claude {prompt}",
	ResumeCommand:   "claude --continue {prompt}",
	ProjectSettings: ".claude/settings.local.json",
	UserSettings:    "~/.claude/settings.json",
	ProjectPlugin:   ".claude/skills/treewright",
	UserPlugin:      "~/.claude/skills/treewright",
	Plugin:          pluginFiles(claudeFiles, claudeRoot),
}

// claudeFiles is the plugin as it is checked in, which is the plugin as it is
// installed: what agent-init writes into a checkout is these bytes, and what a
// contributor reads to learn the layout is the same files rather than a
// rendering of a Go string they have to imagine unquoted. `claude plugin
// validate internal/agentinit/plugins/claude` therefore validates the real
// thing, and the JSON is JSON its editor will format and lint.
//
// The `all:` prefix is load-bearing: without it //go:embed skips paths whose
// name begins with a dot, and .claude-plugin/plugin.json — the one file that
// makes the directory a plugin at all — would be silently absent from the
// build. Dot directories do survive into a module zip, so `go install` gets
// the same tree.
//
//go:embed all:plugins/claude
var claudeFiles embed.FS

const claudeRoot = "plugins/claude"

// SKILL.md is claude's own document, whole, and that is a decision rather than
// duplication waiting to be factored out. An earlier shape kept the driving
// guide as one agent-neutral file that every module wrapped in its own
// frontmatter, on the reasoning that the CLI is the same whoever runs it. It
// is — but the document is not the CLI. It assumes a reader that loads
// instructions from a description, is told which of its own transitions the
// hooks cover, and is asked to leave `signal` alone; an agent whose skills are
// prompt files with no frontmatter, or whose state reporting works some other
// way, needs those paragraphs written differently rather than inherited. So a
// module owns its guide, and the shared text lives in whatever a second module
// chooses to copy from this one.
//
// What holds the copies honest is not a shared file but a test: internal/cli
// reads every module's plugin and checks that every `treewright <command>` it
// names exists in the command table and every JSON field it explains is a tag
// of the ls schema. A rename that forgets one module's skill fails the build
// there, which is the property the shared file was really providing.
//
// Two things about the frontmatter, both load-bearing. The description is what
// decides when Claude loads the skill, so it names the moments the guide is for
// — starting parallel work, checking what is in flight, tearing down — and
// claims the ground `git worktree` would otherwise take. And the `name` is what
// a single-skill plugin is invoked by, under the plugin's namespace: this is
// `/treewright:treewright`, where the same file without the manifest beside it
// was `/treewright`. The doubled word is the cost of the plugin and it is only
// the typed spelling, since what makes Claude reach for the skill by itself is
// the description. Naming the skill something that reads better under the
// namespace would make the model-facing name and the tool's name disagree,
// which is worse than a repetition nobody has to type.
//
// Every command in it spells the canonical binary name. That is the Argv0
// rule's own case made sharper: the consumer is an agent running commands in a
// non-interactive shell, where `tw` — a function defined by the shell
// integration in a startup file — may simply not exist, while the binary is on
// PATH under its own name.
