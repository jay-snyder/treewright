package agentinit

// The Claude Code module.
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
	Name:          "claude",
	Command:       "claude {prompt}",
	ResumeCommand: "claude --continue {prompt}",
	LocalState:    []string{".claude/settings.local.json"},
	UserSettings:  "~/.claude/settings.json",
	Hooks:         claudeHooks,
	Skill:         claudeSkill,
	SkillPath:     "~/.claude/skills/treewright/SKILL.md",
}

// claudeSkill packages the shared driving guide as a Claude Code skill. The
// frontmatter's description is what decides when Claude loads it, so it names
// the moments the guide is for — starting parallel work, checking what is in
// flight, tearing down — and claims the ground `git worktree` would otherwise
// take. User-level rather than per-project, like the hooks: the knowledge is
// about the tool, not about any repository.
const claudeSkill = `---
name: treewright
description: Manage parallel work in repositories that use treewright (tw) — a git worktree, tmux window, and agent session per ticket. Use when starting work on a ticket in parallel, spawning another agent on a task, checking which worktrees and agents are in flight or need attention, resuming earlier work, or cleaning up merged branches. Use instead of raw git worktree in a treewright-managed repository.
---

` + drivingGuide

// claudeHooks is the fragment for a Claude Code settings file. Kept to the
// "hooks" key alone so it can sit in a file that already configures other
// things: Claude Code merges hooks across its settings scopes and runs them
// all, so these coexist with whatever hooks are already there.
const claudeHooks = `{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "treewright signal working" }] }
    ],
    "Notification": [
      { "hooks": [{ "type": "command", "command": "treewright signal waiting" }] }
    ],
    "Stop": [
      { "hooks": [{ "type": "command", "command": "treewright signal done" }] }
    ],
    "SessionEnd": [
      { "hooks": [{ "type": "command", "command": "treewright signal clear" }] }
    ]
  }
}
`
