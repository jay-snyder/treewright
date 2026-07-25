package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Nothing here asserts on doctor's exit code for a healthy setup: whether tmux is
// installed is a property of the machine running the tests, not of the code, and
// CI deliberately has no tmux. The findings about configs are what these tests
// pin, since those are the ones treemux computes.

// findings splits doctor's report into level and detail per line.
func findings(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	r := f.exec("doctor")
	found := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(r.stdout, "\n"), "\n") {
		level, detail, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		found[strings.TrimSpace(detail)] = level
	}
	if len(found) == 0 {
		t.Fatalf("doctor printed nothing to stdout\nstderr: %s", r.stderr)
	}
	return found
}

// has reports the level of the first finding containing substr.
func has(found map[string]string, substr string) string {
	for detail, level := range found {
		if strings.Contains(detail, substr) {
			return level
		}
	}
	return ""
}

func TestDoctorApprovesAHealthySetup(t *testing.T) {
	f := newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	found := findings(t, f)

	for _, tc := range []struct{ substr, want string }{
		{"1 config(s)", "ok"},
		{"proj: main_dir " + f.MainDir, "ok"},
		{"proj: forks from origin/main", "ok"},
		{`here, commands use "proj"`, "ok"},
	} {
		if got := has(found, tc.substr); got != tc.want {
			t.Errorf("finding for %q = %q, want %q\nall: %v", tc.substr, got, tc.want, found)
		}
	}
}

func TestDoctorReportsAMovedRepository(t *testing.T) {
	f := newFixture(t, "")
	f.setConfig("main_dir = '" + filepath.Join(f.Root, "gone") + "'\n")

	r := f.exec("doctor")
	// A config pointing at nothing is the failure that makes every other command
	// behave inexplicably, so it must both be reported and set the exit code.
	if !errors.Is(r.err, ErrSilent) {
		t.Errorf("err = %v, want ErrSilent so a setup script can gate on it", r.err)
	}
	if !strings.Contains(r.stdout, "does not exist") {
		t.Errorf("stdout = %q, want the missing main_dir named", r.stdout)
	}
}

func TestDoctorReportsANonRepository(t *testing.T) {
	f := newFixture(t, "")
	plain := filepath.Join(f.Root, "notarepo")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	f.setConfig("main_dir = '" + plain + "'\n")

	r := f.exec("doctor")
	if !errors.Is(r.err, ErrSilent) {
		t.Errorf("err = %v, want ErrSilent", r.err)
	}
	if !strings.Contains(r.stdout, "is not a git repository") {
		t.Errorf("stdout = %q, want the directory called out", r.stdout)
	}
}

func TestDoctorReportsMissingCarryFiles(t *testing.T) {
	f := newFixture(t, "carry_files = ['.env', 'apps/api/.env']\n")

	found := findings(t, f)
	// A stale carry_files entry breaks the app inside a fresh worktree and says
	// nothing until then, so it is worth a warning here — but not a failure, since
	// treemux itself still works.
	if got := has(found, "carry_files .env is missing"); got != "warn" {
		t.Errorf("finding = %q, want a warning\nall: %v", got, found)
	}
	if got := has(found, "apps/api/.env is missing"); got != "warn" {
		t.Errorf("finding = %q, want a warning for the nested path", got)
	}
}

// TestDoctorReportsStrayRegistryFiles covers a file left behind by a migration or
// a rename: treemux ignores it, and the user believes it is in force.
func TestDoctorReportsStrayRegistryFiles(t *testing.T) {
	f := newFixture(t, "")
	if err := os.WriteFile(filepath.Join(f.registry, "proj.zsh"), []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	found := findings(t, f)
	switch got := has(found, "proj.zsh"); got {
	case "warn": // as intended
	case "":
		t.Errorf("the stray file went unmentioned\nall: %v", found)
	default:
		t.Errorf("finding for the stray file = %q, want a warning", got)
	}
}

func TestDoctorReportsAnUnreadableConfig(t *testing.T) {
	f := newFixture(t, "")
	f.setConfig("main_dir = '" + f.MainDir + "'\nbase-branch = 'typo'\n")

	r := f.exec("doctor")
	if !errors.Is(r.err, ErrSilent) {
		t.Errorf("err = %v, want ErrSilent", r.err)
	}
	if !strings.Contains(r.stdout, "unknown setting") {
		t.Errorf("stdout = %q, want the bad key named", r.stdout)
	}
}

func TestDoctorReportsAMissingCommand(t *testing.T) {
	f := newFixture(t, "command = 'definitely-not-installed-anywhere'\nresume_command = 'true'\n")

	found := findings(t, f)
	if got := has(found, "not on PATH"); got != "warn" {
		t.Errorf("finding = %q, want a warning that the command is missing\nall: %v", got, found)
	}
}

func TestDoctorOutsideARepository(t *testing.T) {
	f := newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	t.Chdir(t.TempDir())

	found := findings(t, f)
	// Not a fault, but it explains why commands here ask for a [repo].
	if got := has(found, "not inside a git repository"); got != "warn" {
		t.Errorf("finding = %q, want a warning\nall: %v", got, found)
	}
}
