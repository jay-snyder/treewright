package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/agentinit"
)

// The agent side of the integration: agent-init installing the plugin, the
// agent key carrying it into worktrees, and doctor catching the half-installed
// states. The signal protocol itself is covered in signal_test.go.

// agentFixture is a fixture whose HOME is this test's own, so the developer's
// real ~/.claude can never decide what these tests see — in either direction —
// and so a test of --user has somewhere harmless to install.
func agentFixture(t *testing.T, extraConfig string) *fixture {
	t.Helper()
	f := newFixture(t, extraConfig)
	t.Setenv("HOME", t.TempDir())
	return f
}

// pluginPaths is every file the claude module installs, relative to a checkout.
func pluginPaths(t *testing.T) []string {
	t.Helper()
	agent, ok := agentinit.Lookup("claude")
	if !ok {
		t.Fatal("no claude module")
	}
	var out []string
	for _, f := range agent.Plugin {
		out = append(out, agent.ProjectPlugin+"/"+f.Path)
	}
	return out
}

func TestAgentInitInstallsThePluginIntoTheMainCheckout(t *testing.T) {
	f := agentFixture(t, "")

	r := f.exec("agent-init", "claude")
	if r.err != nil {
		t.Fatalf("agent-init claude: %v\n%s", r.err, r.both())
	}
	// stdout is the directory alone, so the path can be piped somewhere useful.
	want := filepath.Join(f.MainDir, ".claude", "skills", "treewright") + "\n"
	if r.stdout != want {
		t.Errorf("stdout = %q, want exactly %q", r.stdout, want)
	}
	for _, rel := range pluginPaths(t) {
		if _, err := os.Stat(filepath.Join(f.MainDir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not installed: %v", rel, err)
		}
	}

	// The instructions are narration: the carry the plugin needs to reach a
	// worktree, and the fact that nothing ignores a directory treewright
	// invented.
	if !strings.Contains(r.stderr, `agent = "claude"`) {
		t.Errorf("stderr = %q, want the carry named", r.stderr)
	}
	if !strings.Contains(r.stderr, ".gitignore") {
		t.Errorf("stderr = %q, want the reader told the plugin is not ignored for them", r.stderr)
	}
	// Hooks do not reload themselves, so a session already open stays wired to
	// whatever it loaded at start.
	if !strings.Contains(r.stderr, "/reload-plugins") {
		t.Errorf("stderr = %q, want the reload named", r.stderr)
	}
}

// TestAgentInitUpdatesRatherThanRepeats is the whole reason the wiring moved out
// of a settings file: run again after an upgrade it rewrites what changed and
// says so, where a pasted fragment could only ever be pasted a second time.
func TestAgentInitUpdatesRatherThanRepeats(t *testing.T) {
	f := agentFixture(t, "")
	f.mustRun("agent-init", "claude")

	r := f.exec("agent-init", "claude")
	if r.err != nil {
		t.Fatalf("agent-init claude: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "nothing to update") {
		t.Errorf("stderr = %q, want the second run reported as a no-op", r.stderr)
	}

	// What an upgrade looks like from here: a file left by an older treewright,
	// named on the way past rather than silently replaced.
	hooks := filepath.Join(f.MainDir, ".claude", "skills", "treewright", "hooks", "hooks.json")
	if err := os.WriteFile(hooks, []byte(`{"hooks":{"Stop":[]}}`), 0o644); err != nil {
		t.Fatalf("write the stale hooks: %v", err)
	}
	r = f.exec("agent-init", "claude")
	if r.err != nil {
		t.Fatalf("agent-init claude: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "hooks/hooks.json") {
		t.Errorf("stderr = %q, want the file that had drifted named", r.stderr)
	}
	body, err := os.ReadFile(hooks)
	if err != nil || !strings.Contains(string(body), "treewright signal working") {
		t.Errorf("hooks.json = %q, want the current wiring", body)
	}
}

// TestAgentInitPrintWritesNothing keeps the read-before-you-run path: these are
// hooks that will run on every transition of an agent, and tmux-init prints by
// default for the same reason.
func TestAgentInitPrintWritesNothing(t *testing.T) {
	f := agentFixture(t, "")

	r := f.exec("agent-init", "claude", "--print")
	if r.err != nil {
		t.Fatalf("agent-init claude --print: %v\n%s", r.err, r.both())
	}
	for _, rel := range pluginPaths(t) {
		if !strings.Contains(r.stdout, "==> "+rel+" <==") {
			t.Errorf("stdout does not announce %s:\n%s", rel, r.stdout)
		}
		if _, err := os.Stat(filepath.Join(f.MainDir, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was written by a command that only prints", rel)
		}
	}
	if !strings.Contains(r.stdout, "# Driving treewright") || !strings.Contains(r.stdout, "treewright signal working") {
		t.Errorf("stdout = %.200q..., want both directions of the wiring", r.stdout)
	}
	if !strings.Contains(r.stderr, "nothing was written") {
		t.Errorf("stderr = %q, want it said that nothing was installed", r.stderr)
	}
}

// TestAgentInitUserScopeNeedsNoRepository covers the placement offered second:
// every repository at once, treewright-managed or not, which is safe because
// signal is silent outside a treewright window.
func TestAgentInitUserScopeNeedsNoRepository(t *testing.T) {
	f := agentFixture(t, "")
	home := os.Getenv("HOME")
	t.Chdir(t.TempDir()) // outside any repository at all

	r := f.exec("agent-init", "claude", "--user")
	if r.err != nil {
		t.Fatalf("agent-init claude --user: %v\n%s", r.err, r.both())
	}
	if got, want := strings.TrimSpace(r.stdout), filepath.Join(home, ".claude", "skills", "treewright"); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "treewright", "hooks", "hooks.json")); err != nil {
		t.Errorf("the user-level plugin was not installed: %v", err)
	}
	// Nothing to carry at user scope: ~/.claude covers every directory the
	// agent is ever started in, worktrees included.
	if strings.Contains(r.stderr, `agent = "claude"`) {
		t.Errorf("stderr = %q, want no carry advice for a placement that needs none", r.stderr)
	}
}

// TestAgentInitOutsideARepositorySaysWhatToDo: the per-repo install is the only
// form that needs a config, and the error names the two that do not.
func TestAgentInitOutsideARepositorySaysWhatToDo(t *testing.T) {
	f := agentFixture(t, "")
	registry := t.TempDir() // empty, so nothing resolves
	t.Setenv("TREEWRIGHT_CONFIG_DIR", registry)
	t.Chdir(t.TempDir())

	r := f.exec("agent-init", "claude")
	if r.err == nil {
		t.Fatalf("agent-init claude succeeded outside a repository:\n%s", r.both())
	}
	if !strings.Contains(r.err.Error(), "--user") {
		t.Errorf("err = %v, want the placement that needs no config named", r.err)
	}
}

// TestThePluginTeachesTheCLIThatExists holds the plugin's own text to the
// command table: every `treewright <command>` it shows, in a code block or
// inline code, must be a command lookup resolves, and every JSON field it
// teaches must be a tag of the ls schema. A rename that forgets the guide fails
// here rather than teaching agents a CLI that is gone.
func TestThePluginTeachesTheCLIThatExists(t *testing.T) {
	agent, ok := agentinit.Lookup("claude")
	if !ok {
		t.Fatal("no claude module")
	}
	var b strings.Builder
	for _, f := range agent.Plugin {
		b.WriteString(f.Body)
	}
	plugin := b.String()

	named := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile("`treewright ([a-z-]+)"),         // inline code
		regexp.MustCompile(`(?m)^    treewright ([a-z-]+)`), // command examples
		regexp.MustCompile(`"command": "treewright ([a-z-]+)`),
	} {
		for _, m := range re.FindAllStringSubmatch(plugin, -1) {
			named[m[1]] = true
		}
	}
	if len(named) < 5 {
		t.Fatalf("only %d commands found in the plugin — the extraction is likely broken: %v", len(named), named)
	}
	for name := range named {
		if lookup(name) == nil {
			t.Errorf("the plugin teaches %q, which is not a command", name)
		}
	}

	// The JSON fields the guide explains, checked against the schema's actual
	// tags so `ls --json` and its documentation cannot part company.
	tags := map[string]bool{}
	for _, field := range reflect.VisibleFields(reflect.TypeFor[worktreeJSON]()) {
		tags[field.Tag.Get("json")] = true
	}
	for _, field := range []string{"base", "status", "agent_state", "ahead", "behind"} {
		if !strings.Contains(plugin, "`"+field+"`") && !strings.Contains(plugin, `"`+field+`"`) {
			t.Errorf("the plugin no longer explains the %s field", field)
		}
		if !tags[field] {
			t.Errorf("the plugin explains %q, which ls --json does not report", field)
		}
	}

	// Never `tw` as an invocation: the reader runs commands in a shell where
	// the function may not exist. Mentioning `tw` by name is fine — the guide
	// explains exactly that — but no command may start with it.
	if strings.Contains(plugin, "`tw ") || regexp.MustCompile(`(?m)^    tw `).MatchString(plugin) {
		t.Error("the plugin shows a command invoked as tw, which non-interactive shells may not have")
	}
}

func TestAgentInitRejectsAnUnknownAgent(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("agent-init", "copilot")
	if !errors.Is(r.err, ErrUsage) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
	if !strings.Contains(r.stderr, "claude") {
		t.Errorf("stderr = %q, want the modules that do exist listed", r.stderr)
	}
	if r := f.exec("agent-init"); !errors.Is(r.err, ErrUsage) {
		t.Errorf("err = %v, want a usage error for the missing agent", r.err)
	}
}

// TestAgentKeyCarriesThePluginAndTheSettings pins the key's reason to exist:
// the agent's gitignored per-project state — the plugin's hooks and skill, and
// the permission decisions beside them — reaches every new worktree with no
// carry_files line to remember. The plugin is a tree, and every file of it is
// named on the carried list rather than the directory holding them, so a
// worktree cannot arrive with the skill and no hooks.
func TestAgentKeyCarriesThePluginAndTheSettings(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")
	settings := filepath.Join(f.MainDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settings, []byte(`{"permissions":{}}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	f.mustRun("agent-init", "claude")

	f.mustRun("new", "alpha")
	for _, rel := range append(pluginPaths(t), ".claude/settings.local.json") {
		if _, err := os.Stat(filepath.Join(f.DirFor("alpha"), filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not carried into the worktree: %v", rel, err)
		}
	}
}

// TestAgentCarryIsSilentWhenAbsent is the one way the implicit carry differs
// from a carry_files entry: nobody asserted the file exists, so a checkout
// that has never run the agent gets no warning about it.
func TestAgentCarryIsSilentWhenAbsent(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")

	r := f.exec("new", "alpha")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "carry") {
		t.Errorf("stderr = %q, want no carry warning for a file nobody asserted", r.stderr)
	}
}

func TestConfigReportsWhatTheAgentKeySupplies(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")

	out := f.mustRun("config")
	for _, want := range []string{
		"agent",
		"claude",
		// Every per-project artifact the module carries, marked as the module's
		// rather than the file's: the settings file and all three files of the
		// plugin, since a directory is not what the carry copies.
		".claude/settings.local.json, .claude/skills/treewright/.claude-plugin/plugin.json",
		".claude/skills/treewright/SKILL.md, .claude/skills/treewright/hooks/hooks.json  (from agent)",
		"claude {prompt}  (from agent)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config = %q, want it to contain %q", out, want)
		}
	}

	// An explicit command is the file's own, and says so by carrying no marker.
	f2 := newFixture(t, "agent = 'claude'\ncommand = 'nvim'\n")
	if out := f2.mustRun("config"); strings.Contains(out, "nvim  (from agent)") {
		t.Errorf("config = %q, want the explicit command unmarked", out)
	}
}

// TestSetupDetectsAnInstalledAgent relies on the fixture's stubbed claude
// being on PATH, which is exactly what detection looks for.
func TestSetupDetectsAnInstalledAgent(t *testing.T) {
	f := newFixture(t, "")

	// The fixture's repo is already registered as "proj", and setup refuses a
	// second config for the same repository — so ask for the same one, dry.
	r := f.exec("setup", "-n", "proj")
	if r.err != nil {
		t.Fatalf("setup: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stdout, "agent = \"claude\"") {
		t.Errorf("generated config = %q, want the detected agent written", r.stdout)
	}
	if !strings.Contains(r.stderr, "agent claude") {
		t.Errorf("stderr = %q, want the detection reported like every other guess", r.stderr)
	}
}

// ---- doctor ------------------------------------------------------------------

func TestDoctorWarnsWhenTheAgentReportsNoState(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")

	out, _ := f.run("doctor")
	if !strings.Contains(out, "does not report state") || !strings.Contains(out, "agent-init claude") {
		t.Errorf("doctor = %q, want the missing wiring named with its fix", out)
	}
}

func TestDoctorNamesTheCarryTrap(t *testing.T) {
	// No agent key and no carry entry — but the plugin sitting in the main
	// checkout, where it reaches the MAIN window and no worktree at all. The
	// module is sniffed from the default command, claude, which is the guess a
	// warn-level hint is allowed to rest on.
	f := agentFixture(t, "")
	f.mustRun("agent-init", "claude")

	out, _ := f.run("doctor")
	if !strings.Contains(out, "reaches no worktree") {
		t.Errorf("doctor = %q, want the trap named", out)
	}

	// The agent key is the fix, and doctor stops warning once it is taken: the
	// automatic carry leaves the trap nowhere to occur.
	f.setConfig("main_dir = '" + f.MainDir + "'\nagent = 'claude'\n")
	out, _ = f.run("doctor")
	if strings.Contains(out, "reaches no worktree") {
		t.Errorf("doctor = %q, want the trap gone under the agent key", out)
	}
	if !strings.Contains(out, "reports state through its plugin") {
		t.Errorf("doctor = %q, want the wired state reported ok", out)
	}
}

// TestDoctorNoticesAPluginAnOlderTreewrightWrote is the check the settings-file
// paste made impossible. Wiring treewright owns can be compared with the wiring
// treewright would write, so a hook mapping that has moved on is a warning
// rather than an installation that looks finished and reports the wrong words.
func TestDoctorNoticesAPluginAnOlderTreewrightWrote(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")
	f.mustRun("agent-init", "claude")

	hooks := filepath.Join(f.MainDir, ".claude", "skills", "treewright", "hooks", "hooks.json")
	old := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"treewright signal finished"}]}]}}`
	if err := os.WriteFile(hooks, []byte(old), 0o644); err != nil {
		t.Fatalf("write the older wiring: %v", err)
	}

	out, _ := f.run("doctor")
	if !strings.Contains(out, "not what this treewright would write") {
		t.Errorf("doctor = %q, want the stale plugin named", out)
	}

	// And the fix is the command it names, which is the point of naming it.
	f.mustRun("agent-init", "claude")
	out, _ = f.run("doctor")
	if strings.Contains(out, "not what this treewright would write") {
		t.Errorf("doctor = %q, want the warning gone after the rerun", out)
	}
}

// TestDoctorNamesHooksLeftInASettingsFile covers the upgrade path: an install
// wired by an older treewright's paste still works, and doctor says so in the
// terms that matter — it is a copy nothing can update.
func TestDoctorNamesHooksLeftInASettingsFile(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")
	settings := filepath.Join(f.MainDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"treewright signal done"}]}]}}`
	if err := os.WriteFile(settings, []byte(body), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	out, _ := f.run("doctor")
	if !strings.Contains(out, "pasted copy treewright cannot update") {
		t.Errorf("doctor = %q, want the paste named as the thing the plugin replaces", out)
	}

	// Once the plugin is installed the two run side by side, which is worth
	// saying: the pasted half is frozen wherever it was written.
	f.mustRun("agent-init", "claude")
	out, _ = f.run("doctor")
	if !strings.Contains(out, "run alongside the plugin's") {
		t.Errorf("doctor = %q, want the duplicate wiring named", out)
	}
}
