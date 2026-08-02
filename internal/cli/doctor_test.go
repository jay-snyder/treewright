package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/tmux"
)

// The findings about configs are what these tests mostly pin, since those are
// the ones treewright computes. The healthy-setup exit code is asserted too,
// gated on tmux being installed: whether it is is a property of the machine
// running the tests, and CI installs it on both platforms — so there the gate
// is always open, while a developer without tmux still gets the config
// assertions.

// reportLine is one finding of doctor's report, flattened back to the sentence
// it would have been: "<group>: <check> <detail>", with the column padding
// collapsed to single spaces.
//
// Flattened because that is the unit an assertion is about. What the report
// does with a finding — a group heading above it, the check in its own column,
// the detail running to three lines under that — is layout, and a test that
// pinned the layout would fail every time the layout improved. What must not
// change silently is which facts a finding carries.
type reportLine struct{ level, detail string }

// findings parses doctor's report into one entry per finding, in the order
// printed.
//
// A finding is neither a line nor a row. Its detail can run to several lines,
// indented past the check column, and those belong to the finding above rather
// than being findings of their own — read as findings they would each claim
// their first word as a level, so a report with one warning came back holding
// three. Group headings sit flush left, findings are indented under them.
//
// A slice rather than a map, and the reason is a test that passed on Linux and
// failed on macOS: with map iteration, a substring matching two findings returned
// whichever the runtime reached first. Order here is doctor's own.
func findings(t *testing.T, f *fixture) []reportLine {
	t.Helper()
	r := f.exec("doctor")

	var found []reportLine
	group, indent := "", ""
	for line := range strings.SplitSeq(strings.TrimRight(r.stdout, "\n"), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			continue
		case !strings.HasPrefix(line, " "):
			// A heading, or the summary line that closes the report — which is
			// the last thing printed, so nothing is ever filed under it.
			group = strings.TrimSpace(line)
			continue
		case len(line)-len(strings.TrimLeft(line, " ")) > len(indent) && len(found) > 0:
			found[len(found)-1].detail += " " + strings.TrimSpace(line)
			continue
		}
		indent = line[:len(line)-len(strings.TrimLeft(line, " "))]
		level, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		found = append(found, reportLine{
			level:  level,
			detail: group + ": " + strings.Join(strings.Fields(rest), " "),
		})
	}
	if len(found) == 0 {
		t.Fatalf("doctor printed nothing to stdout\nstderr: %s", r.stderr)
	}
	return found
}

// has reports the level of the finding containing substr, and fails when substr
// matches more than one.
//
// The ambiguity check is the point: "not on PATH" appears both in the tmux check
// and in the command check, so on a machine without tmux an assertion meant for
// one silently read the other. A substring that no longer identifies a single
// finding is a broken assertion, not a passing one.
func has(t *testing.T, found []reportLine, substr string) string {
	t.Helper()
	var matched []reportLine
	for _, fi := range found {
		if strings.Contains(fi.detail, substr) {
			matched = append(matched, fi)
		}
	}
	switch len(matched) {
	case 0:
		return ""
	case 1:
		return matched[0].level
	default:
		t.Fatalf("%q matches %d findings, so the assertion is ambiguous: %v", substr, len(matched), matched)
		return ""
	}
}

func TestDoctorApprovesAHealthySetup(t *testing.T) {
	f := newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	found := findings(t, f)

	for _, tc := range []struct{ substr, want string }{
		{"1 config in", "ok"},
		{"proj: main_dir " + f.MainDir, "ok"},
		{"forks from origin/main", "ok"},
		{`commands use "proj"`, "ok"},
	} {
		if got := has(t, found, tc.substr); got != tc.want {
			t.Errorf("finding for %q = %q, want %q\nall: %v", tc.substr, got, tc.want, found)
		}
	}

	// With tmux present nothing here fails, so a setup script gating on doctor
	// has to see exit 0 — warnings alone must not fail the run. Without tmux
	// that check is a levelFail, so the gate stays closed on such a machine.
	if tmux.Available() {
		if r := f.exec("doctor"); r.err != nil {
			t.Errorf("doctor on a healthy setup = %v, want exit 0\n%s", r.err, r.stdout)
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
	// treewright itself still works.
	if got := has(t, found, "carry_files .env is missing"); got != "warn" {
		t.Errorf("finding = %q, want a warning\nall: %v", got, found)
	}
	if got := has(t, found, "apps/api/.env is missing"); got != "warn" {
		t.Errorf("finding = %q, want a warning for the nested path", got)
	}
}

// TestDoctorReportsStrayRegistryFiles covers a file left behind by a migration or
// a rename: treewright ignores it, and the user believes it is in force.
func TestDoctorReportsStrayRegistryFiles(t *testing.T) {
	f := newFixture(t, "")
	if err := os.WriteFile(filepath.Join(f.registry, "proj.zsh"), []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	found := findings(t, f)
	switch got := has(t, found, "proj.zsh"); got {
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

// TestDoctorReportsAnUnusableBranchPrefix is the payoff of validating prefixes when
// the config is read rather than when a branch is created: the check a user runs
// after editing the file is where the mistake surfaces, instead of three steps into
// the next `new`.
func TestDoctorReportsAnUnusableBranchPrefix(t *testing.T) {
	f := newFixture(t, "")
	f.writeConfig("main_dir = '" + f.MainDir + "'\nbranch_prefixes = ['feature/', 'fea ture/']\n")

	r := f.exec("doctor")
	if !errors.Is(r.err, ErrSilent) {
		t.Errorf("err = %v, want ErrSilent", r.err)
	}
	// The offending prefix, not just the key: the list has two entries and only one
	// of them is wrong.
	if !strings.Contains(r.stdout, `"fea ture/"`) {
		t.Errorf("stdout = %q, want the bad prefix named", r.stdout)
	}
}

func TestDoctorReportsAMissingCommand(t *testing.T) {
	f := newFixture(t, "command = 'definitely-not-installed-anywhere'\nresume_command = 'true'\n")

	found := findings(t, f)
	if got := has(t, found, `runs "definitely-not-installed-anywhere"`); got != "warn" {
		t.Errorf("finding = %q, want a warning that the command is missing\nall: %v", got, found)
	}
}

func TestDoctorOutsideARepository(t *testing.T) {
	f := newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	t.Chdir(t.TempDir())

	found := findings(t, f)
	// Not a fault, but it explains why commands here ask for a [repo].
	if got := has(t, found, "not inside a git repository"); got != "warn" {
		t.Errorf("finding = %q, want a warning\nall: %v", got, found)
	}
}
