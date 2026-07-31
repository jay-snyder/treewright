package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	f.mustRun("new", "alpha")
	carried := filepath.Join(f.DirFor("alpha"), ".claude", "settings.local.json")
	if _, err := os.Stat(carried); err != nil {
		t.Errorf("the agent's settings were not carried: %v", err)
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
		".claude/settings.local.json  (from agent)",
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
