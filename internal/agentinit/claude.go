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
// it is started from.
var claude = Agent{
	Name:          "claude",
	Command:       "claude",
	ResumeCommand: "claude --continue",
	LocalState:    []string{".claude/settings.local.json"},
	UserSettings:  "~/.claude/settings.json",
	Hooks:         claudeHooks,
}

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
