package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/agentinit"
)

// The agent side of the integration: agent-init printing the wiring, the agent
// key carrying the agent's own settings into worktrees, and doctor catching
// the half-installed states. The signal protocol itself is covered in
// signal_test.go.

func TestAgentInitPrintsTheHooksAndNothingElse(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("agent-init", "claude")
	if r.err != nil {
		t.Fatalf("agent-init claude: %v\n%s", r.err, r.both())
	}
	// stdout is the fragment alone, so it can be piped somewhere useful.
	var fragment struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &fragment); err != nil {
		t.Fatalf("stdout is not the JSON fragment: %v\n%s", err, r.stdout)
	}
	if len(fragment.Hooks) == 0 {
		t.Errorf("stdout = %q, want the hooks fragment", r.stdout)
	}
	// The instructions are narration: where the fragment goes, and the per-repo
	// alternative the agent key exists for.
	if !strings.Contains(r.stderr, "~/.claude/settings.json") {
		t.Errorf("stderr = %q, want it to name the user-level file", r.stderr)
	}
	if !strings.Contains(r.stderr, `agent = "claude"`) {
		t.Errorf("stderr = %q, want the per-repo alternative named", r.stderr)
	}
}

func TestAgentInitSkillPrintsTheGuideAndItsHome(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("agent-init", "claude", "--skill")
	if r.err != nil {
		t.Fatalf("agent-init claude --skill: %v\n%s", r.err, r.both())
	}
	if !strings.HasPrefix(r.stdout, "---\n") || !strings.Contains(r.stdout, "# Driving treewright") {
		t.Errorf("stdout = %.120q..., want the skill alone", r.stdout)
	}
	// The install line has to be runnable as printed, which means it names the
	// real path and repeats this very invocation.
	if !strings.Contains(r.stderr, "~/.claude/skills/treewright/SKILL.md") {
		t.Errorf("stderr = %q, want the skill's home named", r.stderr)
	}
	if !strings.Contains(r.stderr, "treewright agent-init claude --skill >") {
		t.Errorf("stderr = %q, want the redirect one-liner", r.stderr)
	}
}

// TestTheSkillTeachesTheCLIThatExists holds the driving guide to the command
// table: every `treewright <command>` it shows, in a code block or inline
// code, must be a command lookup resolves, and every JSON field it teaches
// must be a tag of the ls schema. A rename that forgets the guide fails here
// rather than teaching agents a CLI that is gone.
func TestTheSkillTeachesTheCLIThatExists(t *testing.T) {
	agent, ok := agentinit.Lookup("claude")
	if !ok {
		t.Fatal("no claude module")
	}
	skill := agent.Skill

	named := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile("`treewright ([a-z-]+)"),         // inline code
		regexp.MustCompile(`(?m)^    treewright ([a-z-]+)`), // command examples
	} {
		for _, m := range re.FindAllStringSubmatch(skill, -1) {
			named[m[1]] = true
		}
	}
	if len(named) < 5 {
		t.Fatalf("only %d commands found in the skill — the extraction is likely broken: %v", len(named), named)
	}
	for name := range named {
		if lookup(name) == nil {
			t.Errorf("the skill teaches %q, which is not a command", name)
		}
	}

	// The JSON fields the guide explains, checked against the schema's actual
	// tags so `ls --json` and its documentation cannot part company.
	tags := map[string]bool{}
	for _, field := range reflect.VisibleFields(reflect.TypeFor[worktreeJSON]()) {
		tags[field.Tag.Get("json")] = true
	}
	for _, field := range []string{"base", "status", "agent_state", "ahead", "behind"} {
		if !strings.Contains(skill, "`"+field+"`") && !strings.Contains(skill, `"`+field+`"`) {
			t.Errorf("the skill no longer explains the %s field", field)
		}
		if !tags[field] {
			t.Errorf("the skill explains %q, which ls --json does not report", field)
		}
	}

	// Never `tw` as an invocation: the reader runs commands in a shell where
	// the function may not exist. Mentioning `tw` by name is fine — the guide
	// explains exactly that — but no command may start with it.
	if strings.Contains(skill, "`tw ") || regexp.MustCompile(`(?m)^    tw `).MatchString(skill) {
		t.Error("the skill shows a command invoked as tw, which non-interactive shells may not have")
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

// TestAgentKeyCarriesTheAgentsSettings pins the key's reason to exist: the
// agent's gitignored settings — hooks, permission decisions — reach every new
// worktree with no carry_files line to remember.
func TestAgentKeyCarriesTheAgentsSettings(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")
	settings := filepath.Join(f.MainDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settings, []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	// The skill is the module's other per-project artifact, and it rides across
	// on the same key. Placed in the main checkout and not carried, it would
	// teach the agent in the MAIN window and in no worktree — the same trap the
	// hooks had, one directory deeper.
	skill := filepath.Join(f.MainDir, ".claude", "skills", "treewright", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(skill, []byte("---\nname: treewright\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	f.mustRun("new", "alpha")
	for _, rel := range []string{
		".claude/settings.local.json",
		".claude/skills/treewright/SKILL.md",
	} {
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
		// Both per-project artifacts the module carries, marked as the module's
		// rather than the file's.
		".claude/settings.local.json, .claude/skills/treewright/SKILL.md  (from agent)",
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

// doctorAgentFixture is a fixture whose HOME is this test's own, so the
// developer's real ~/.claude/settings.json can never decide what these tests
// see — in either direction.
func doctorAgentFixture(t *testing.T, extraConfig string) *fixture {
	t.Helper()
	f := newFixture(t, extraConfig)
	t.Setenv("HOME", t.TempDir())
	return f
}

func TestDoctorWarnsWhenTheAgentReportsNoState(t *testing.T) {
	f := doctorAgentFixture(t, "agent = 'claude'\n")

	out, _ := f.run("doctor")
	if !strings.Contains(out, "does not report state") || !strings.Contains(out, "agent-init claude") {
		t.Errorf("doctor = %q, want the missing wiring named with its fix", out)
	}
}

func TestDoctorNamesTheCarryTrap(t *testing.T) {
	// No agent key and no carry entry — but hooks sitting in the main
	// checkout's gitignored settings, which reach the MAIN window and no
	// worktree at all. The module is sniffed from the default command, claude,
	// which is the guess a warn-level hint is allowed to rest on.
	f := doctorAgentFixture(t, "")
	settings := filepath.Join(f.MainDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"treewright signal done"}]}]}}`
	if err := os.WriteFile(settings, []byte(body), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	out, _ := f.run("doctor")
	if !strings.Contains(out, "reach no worktree") {
		t.Errorf("doctor = %q, want the trap named", out)
	}

	// The agent key is one of the fixes, and doctor stops warning once it is
	// taken: the automatic carry leaves the trap nowhere to occur.
	f.setConfig("main_dir = '" + f.MainDir + "'\nagent = 'claude'\n")
	out, _ = f.run("doctor")
	if strings.Contains(out, "reach no worktree") {
		t.Errorf("doctor = %q, want the trap gone under the agent key", out)
	}
	if !strings.Contains(out, "reports state through its hooks") {
		t.Errorf("doctor = %q, want the wired state reported ok", out)
	}
}
