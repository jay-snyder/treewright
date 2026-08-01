package agentinit

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/testenv"
)

// The plugin is destined for another program's loader, so the tests here take
// the consumer's side: would that program accept it, does every command in it
// target signal's actual vocabulary, and is the layout the one the loader
// documents — the checks the shell shims and the tmux snippet set the precedent
// for, with one addition those two never needed. Claude Code ships a validator,
// so where the shims are checked by asking each shell to parse them, the plugin
// is checked by asking claude itself.

// pluginBody is one of the module's files, by the path it installs to.
func pluginBody(t *testing.T, path string) string {
	t.Helper()
	for _, f := range claude.Plugin {
		if f.Path == path {
			return f.Body
		}
	}
	t.Fatalf("the claude plugin has no %s", path)
	return ""
}

func TestClaudeHooksParseAsJSON(t *testing.T) {
	hooks := pluginBody(t, "hooks/hooks.json")
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(hooks), &settings); err != nil {
		t.Fatalf("the claude hooks file is not valid JSON: %v\n%s", err, hooks)
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
		t.Errorf("the hooks wire %d events, want %d: %v", len(settings.Hooks), len(want), settings.Hooks)
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
				// The canonical name, never tw: the file is read by a program,
				// where the shell function does not exist.
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

// TestTheClaudePluginIsLaidOutAsClaudeCodeExpects holds the tree to the rules
// its loader states: a manifest under .claude-plugin/ and nothing else there,
// every other component at the plugin root, and — for a plugin shipping exactly
// one skill — the SKILL.md at the root rather than under skills/. Putting a
// component inside .claude-plugin/ is the mistake the documentation calls out by
// name, and it fails quietly: the plugin loads, without the part that was moved.
//
// It also names the three files outright, which is the check that survives a
// module dropping one. `.claude-plugin/plugin.json` is the file to lose: it is
// the one that makes the directory a plugin at all, and without it the skill
// still loads — as a plain, un-namespaced skill, with the hooks beside it read
// by nothing.
func TestTheClaudePluginIsLaidOutAsClaudeCodeExpects(t *testing.T) {
	byPath := map[string]string{}
	for _, f := range claude.Plugin {
		if strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "..") {
			t.Errorf("plugin file %q leaves the plugin directory", f.Path)
		}
		if f.Body == "" {
			t.Errorf("plugin file %q is empty", f.Path)
		}
		byPath[f.Path] = f.Body
	}
	for _, want := range []string{"SKILL.md", ".claude-plugin/plugin.json", "hooks/hooks.json"} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("the plugin has no %s: %v", want, byPath)
		}
	}
	for p := range byPath {
		if dir := path.Dir(p); dir == ".claude-plugin" && p != ".claude-plugin/plugin.json" {
			t.Errorf("%s is inside .claude-plugin/, where only plugin.json belongs", p)
		}
	}

	// The manifest's name is the namespace every component of the plugin is
	// reached through, so it is the one field a rename would be felt by: the
	// skill answers to /treewright:treewright because both halves say
	// treewright.
	var manifest struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
	}
	if err := json.Unmarshal([]byte(byPath[".claude-plugin/plugin.json"]), &manifest); err != nil {
		t.Fatalf("the manifest is not valid JSON: %v\n%s", err, byPath[".claude-plugin/plugin.json"])
	}
	if manifest.Name != "treewright" {
		t.Errorf("manifest name = %q, want treewright — it is the namespace the skill is invoked through", manifest.Name)
	}
	if manifest.Description == "" || manifest.Version == "" {
		t.Errorf("manifest = %+v, want a description and a version for the /plugin list to show", manifest)
	}
}

// TestThePluginShipsOnlyWhatItDeclares is the gate on the folder, and it looks
// both ways.
//
// A plugin directory is not documentation. Claude Code loads `bin/` as
// executables on the Bash tool's PATH and `.mcp.json` as servers to start, so a
// file that appears under plugins/ and ships is code running on the machine of
// everyone who installs — and it would be carried into every worktree besides.
// Each module names its files in Go, which is the review gate: a file arrives
// in the binary because someone wrote its name, never because it was sitting in
// a folder. This test closes the other direction, where a file is checked in
// and quietly ships nothing — an editor artifact committed by accident, or a
// contributor who added hooks/extra.json and is now debugging why it has no
// effect. Either way the answer is a failure naming the file.
//
// It reads the real directory rather than an embedded copy of it, deliberately:
// what is embedded is exactly what was declared, so an embedded tree could not
// see the stray file this exists to find.
func TestThePluginShipsOnlyWhatItDeclares(t *testing.T) {
	declared := map[string]bool{}
	for _, name := range Names() {
		module, _ := Lookup(name)
		for _, f := range module.Plugin {
			// The folder is named after the module, which this asserts by
			// relying on it: a module whose files live somewhere else reports
			// every one of them as undeclared.
			declared[path.Join(name, f.Path)] = true
		}
	}

	checkedIn := map[string]bool{}
	root := "plugins"
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		checkedIn[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	for p := range checkedIn {
		if !declared[p] {
			t.Errorf("%s/%s is checked in but no module ships it — name it in the module's Plugin list, or delete it", root, p)
		}
	}
	for p := range declared {
		if !checkedIn[p] {
			t.Errorf("a module ships %s/%s, which is not checked in", root, p)
		}
	}
}

// TestClaudeValidatesTheGeneratedPlugin asks claude itself, which is the only
// check here that cannot go quietly out of date: the schema belongs to Claude
// Code, and a strings.Contains against a layout this repository believes in
// would keep passing after that layout changed. It drives Install rather than
// writing the files itself, so what is validated is what agent-init writes.
func TestClaudeValidatesTheGeneratedPlugin(t *testing.T) {
	testenv.RequireTool(t, "claude")
	// claude writes ~/.claude and ~/.claude.json on any invocation, so it is
	// pointed at a home of this test's own rather than at the developer's.
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	if _, err := claude.Install(dir); err != nil {
		t.Fatalf("install the plugin: %v", err)
	}
	// --strict, because the warnings are the interesting half: an unrecognized
	// manifest field is a typo that loads fine and does nothing.
	out, err := exec.Command("claude", "plugin", "validate", dir, "--strict").CombinedOutput()
	if err != nil {
		t.Errorf("claude rejects the plugin treewright writes: %v\n%s", err, out)
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
	if module.ProjectSettings != ".claude/settings.local.json" {
		t.Errorf("ProjectSettings = %q, want the checkout's own settings file", module.ProjectSettings)
	}
	if module.UserSettings != "~/.claude/settings.json" {
		t.Errorf("UserSettings = %q, want the user-level file", module.UserSettings)
	}
	// The plugin's home is a skills directory in both scopes, which is what
	// makes a folder with a manifest in it load as a plugin at all.
	if module.ProjectPlugin != ".claude/skills/treewright" {
		t.Errorf("ProjectPlugin = %q, want the checkout's own skills directory", module.ProjectPlugin)
	}
	if module.UserPlugin != "~/.claude/skills/treewright" {
		t.Errorf("UserPlugin = %q, want the user-level skills directory", module.UserPlugin)
	}

	if _, ok := Lookup("copilot"); ok {
		t.Error("Lookup(copilot) found a module that does not exist yet")
	}
}

// TestEveryPluginFileIsCarried is the derived list doing its job. A module that
// gains a plugin file gains a carried file with no second edit — which is what
// keeps the wiring from reaching the main checkout and no worktree, the trap the
// agent key exists to close, one directory deeper than the settings file it was
// first found in.
func TestEveryPluginFileIsCarried(t *testing.T) {
	carried := claude.LocalState()
	for _, f := range claude.Plugin {
		want := path.Join(claude.ProjectPlugin, f.Path)
		if !slices.Contains(carried, want) {
			t.Errorf("LocalState() = %v, want it to carry %s", carried, want)
		}
	}
	// The settings file is on the list for a different reason now: it holds the
	// permission decisions the agent records as it works, which a worktree
	// starting without them re-asks from scratch.
	if !slices.Contains(carried, claude.ProjectSettings) {
		t.Errorf("LocalState() = %v, want it to carry %s", carried, claude.ProjectSettings)
	}
	if len(carried) != len(claude.Plugin)+1 {
		t.Errorf("LocalState() = %v, want exactly the settings file and the plugin", carried)
	}
}

// TestClaudeSkillIsWellFormed checks the packaging from the consumer's side:
// Claude Code reads a SKILL.md as YAML frontmatter between --- fences, then
// markdown, and loads it by the description. The guide's own accuracy — do the
// commands it teaches exist? — is checked in internal/cli, which has the
// command table to check against.
func TestClaudeSkillIsWellFormed(t *testing.T) {
	skill := pluginBody(t, "SKILL.md")
	if !strings.HasPrefix(skill, "---\n") {
		t.Fatalf("skill does not open with a frontmatter fence:\n%.80s", skill)
	}
	rest := skill[len("---\n"):]
	frontmatter, body, found := strings.Cut(rest, "\n---\n")
	if !found {
		t.Fatal("skill frontmatter never closes")
	}
	// The name in the frontmatter is what a single-skill plugin is invoked by,
	// and the fallback is the directory the plugin was installed in — so a
	// skill without one is named after wherever it happens to sit.
	if !strings.Contains(frontmatter, "name: treewright") {
		t.Errorf("frontmatter = %q, want the skill named treewright", frontmatter)
	}
	if !strings.Contains(frontmatter, "description: ") {
		t.Errorf("frontmatter = %q, want a description for the model to load it by", frontmatter)
	}
	// A frontmatter block with nothing under it loads and teaches nothing, which
	// is the shape a module gets by copying another's packaging and stopping
	// there — the guide is each module's own now, so nothing else supplies it.
	if !strings.Contains(body, "# Driving treewright") {
		t.Errorf("body = %.80q, want the guide the skill exists to carry", body)
	}
}

// TestInstallRewritesOnlyWhatChanged is the property that makes agent-init an
// update rather than a second install: run twice it reports nothing the second
// time, and run against a file an older treewright wrote it reports that file
// and no other.
func TestInstallRewritesOnlyWhatChanged(t *testing.T) {
	dir := t.TempDir()

	written, err := claude.Install(dir)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if len(written) != len(claude.Plugin) {
		t.Errorf("first install wrote %v, want every file", written)
	}

	again, err := claude.Install(dir)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second install wrote %v, want nothing — the files were already current", again)
	}

	stale := filepath.Join(dir, "hooks", "hooks.json")
	if err := os.WriteFile(stale, []byte(`{"hooks":{"Stop":[]}}`), 0o644); err != nil {
		t.Fatalf("write the stale file: %v", err)
	}
	third, err := claude.Install(dir)
	if err != nil {
		t.Fatalf("third install: %v", err)
	}
	if !slices.Equal(third, []string{"hooks/hooks.json"}) {
		t.Errorf("third install wrote %v, want only the file that had drifted", third)
	}
	body, err := os.ReadFile(stale)
	if err != nil || string(body) != pluginBody(t, "hooks/hooks.json") {
		t.Errorf("hooks.json = %q, want the wiring this treewright would write", body)
	}
}
