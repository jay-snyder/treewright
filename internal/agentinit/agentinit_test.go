package agentinit

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The hooks fragment is destined for another program's settings file, so the
// tests here take the consumer's side: would that program accept it, and does
// every command in it target signal's actual vocabulary — the checks the shell
// shims and the tmux snippet set the precedent for.

func TestClaudeHooksParseAsJSON(t *testing.T) {
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(claude.Hooks), &settings); err != nil {
		t.Fatalf("the claude hooks fragment is not valid JSON: %v\n%s", err, claude.Hooks)
	}

	// The mapping is the design: each of these four transitions reports, and
	// SessionStart deliberately does not — a fresh window sits at the agent's
	// prompt because the human just made it.
	want := map[string]string{
		"UserPromptSubmit": "working",
		"Notification":     "waiting",
		"Stop":             "done",
		"SessionEnd":       "clear",
	}
	if len(settings.Hooks) != len(want) {
		t.Errorf("fragment wires %d events, want %d: %v", len(settings.Hooks), len(want), settings.Hooks)
	}
	for event, state := range want {
		matchers, ok := settings.Hooks[event]
		if !ok {
			t.Errorf("no hook for %s", event)
			continue
		}
		command := "treewright signal " + state
		found := false
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if h.Type == "command" && h.Command == command {
					found = true
				}
				// The canonical name, never tw: the fragment lands in a file a
				// program reads, where the shell function does not exist.
				if strings.HasPrefix(h.Command, "tw ") {
					t.Errorf("%s runs %q — files spell the canonical name", event, h.Command)
				}
			}
		}
		if !found {
			t.Errorf("%s does not run %q: %+v", event, command, matchers)
		}
	}
}

func TestClaudeModuleFacts(t *testing.T) {
	if !slices.Contains(Names(), "claude") {
		t.Fatalf("Names() = %v, want claude among them", Names())
	}
	module, ok := Lookup("claude")
	if !ok {
		t.Fatal("Lookup(claude) found nothing")
	}
	// The launch defaults have to agree with what the config package defaults
	// to without the key, or setting agent = "claude" would change which
	// command runs — the key is a bundle, not a behavior change. The strings
	// are spelled out here because importing config from an in-package test
	// would be a cycle; TestAgentKeyIsADefaultsBundle in config holds the same
	// values against the config constants from the other side.
	if module.Command != "claude {prompt}" || module.ResumeCommand != "claude --continue {prompt}" {
		t.Errorf("launch defaults = %q / %q, want the {prompt} template forms", module.Command, module.ResumeCommand)
	}
	if !slices.Contains(module.LocalState, ".claude/settings.local.json") {
		t.Errorf("LocalState = %v, want the gitignored settings file", module.LocalState)
	}
	if module.UserSettings != "~/.claude/settings.json" {
		t.Errorf("UserSettings = %q, want the user-level file", module.UserSettings)
	}

	if _, ok := Lookup("copilot"); ok {
		t.Error("Lookup(copilot) found a module that does not exist yet")
	}
}

// TestClaudeSkillIsWellFormed checks the packaging from the consumer's side:
// Claude Code reads a SKILL.md as YAML frontmatter between --- fences, then
// markdown, and loads it by the description. The guide's own accuracy — do the
// commands it teaches exist? — is checked in internal/cli, which has the
// command table to check against.
func TestClaudeSkillIsWellFormed(t *testing.T) {
	skill := claude.Skill
	if !strings.HasPrefix(skill, "---\n") {
		t.Fatalf("skill does not open with a frontmatter fence:\n%.80s", skill)
	}
	rest := skill[len("---\n"):]
	frontmatter, body, found := strings.Cut(rest, "\n---\n")
	if !found {
		t.Fatal("skill frontmatter never closes")
	}
	if !strings.Contains(frontmatter, "name: treewright") {
		t.Errorf("frontmatter = %q, want the skill named treewright", frontmatter)
	}
	if !strings.Contains(frontmatter, "description: ") {
		t.Errorf("frontmatter = %q, want a description for the model to load it by", frontmatter)
	}
	if !strings.Contains(body, drivingGuide) {
		t.Error("the claude skill does not carry the shared driving guide")
	}
	if claude.SkillPath == "" {
		t.Error("no SkillPath — agent-init has nowhere to tell the user to put it")
	}
}
