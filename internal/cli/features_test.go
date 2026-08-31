package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/feature"
)

// The optional behaviors a config switches on by name, and the one verb that
// runs them.
//
// What these mostly cover is the silences. A feature runs from an agent hook
// that fires in every session the agent has, so nearly every invocation is in a
// repository or a checkout the feature has nothing to do with — and each of
// those cases is indistinguishable, from the outside, from a feature that ran
// and did its job. The tests are what tell them apart.

// aheadOrigin pushes a commit to origin from a clone of its own, leaving the
// fixture's base checkout behind by one.
//
// A clone rather than a second worktree, and a push rather than a ref written by
// hand, because the point is that the fetch has real work to do: origin/main in
// the base checkout has to be a ref the checkout has never seen, or the test
// passes on a comparison the fetch never had to make. Once per fixture — a
// second call would fail on the clone, which is the right shape for a helper
// whose whole job is putting the checkout one commit behind.
func aheadOrigin(t *testing.T, f *fixture) string {
	t.Helper()
	dir := filepath.Join(f.Root, "elsewhere")
	f.Git(f.Root, "clone", "--quiet", f.Origin, dir)
	f.Git(dir, "config", "user.email", "other@example.com")
	f.Git(dir, "config", "user.name", "Other")
	f.Git(dir, "config", "commit.gpgsign", "false")
	f.Write(dir, "a.txt", "landed on main\n")
	f.Git(dir, "commit", "--quiet", "-am", "work: landed on main")
	f.Git(dir, "push", "--quiet", "origin", "main")
	return f.Git(dir, "rev-parse", "HEAD")
}

// headOf reads a checkout's current commit, which is how these tests tell a
// fast-forward that happened from a line that merely said so.
func headOf(t *testing.T, f *fixture, dir string) string {
	t.Helper()
	return f.Git(dir, "rev-parse", "HEAD")
}

// TestEveryFeatureHasAnImplementation is the seam between the registry and the
// behavior, standing in for the interface neither side has.
//
// internal/feature holds the names and internal/cli holds what they do, because
// internal/config validates the names and must not depend on git. The cost of
// that split is that a feature can be registered — nameable in a config,
// accepted by Load, listed in the generated file's commentary — with nothing
// wired to run it, and every symptom of that is a success: the config loads, the
// hook fires, nothing happens. Modelled on TestTheGuardAndItsMatcherAgree, which
// holds the same kind of two-sided list together the same way.
func TestEveryFeatureHasAnImplementation(t *testing.T) {
	for _, f := range feature.All() {
		switch f.Moment {
		case feature.AtSessionStart:
			if _, ok := sessionStartFeatures[f.Name]; !ok {
				t.Errorf("%q is registered at %s and sessionStartFeatures has no entry for it — a config could name it and nothing would run",
					f.Name, f.Moment)
			}
		default:
			t.Errorf("%q runs at %q, which no dispatcher in this package knows about", f.Name, f.Moment)
		}
	}
	// And the other direction: an implementation nobody can reach is a name a
	// config is refused for naming.
	for name := range sessionStartFeatures {
		if _, ok := feature.Lookup(name); !ok {
			t.Errorf("sessionStartFeatures implements %q, which is not in the registry — no config may name it", name)
		}
	}
}

// TestHelpNamesEveryFeature keeps the list a person reads before switching
// something on from falling behind the list that exists. It is the reason the
// prose renders the registry rather than spelling it out.
func TestHelpNamesEveryFeature(t *testing.T) {
	cmd := lookup("session-start")
	if cmd == nil {
		t.Fatal("session-start is not in the command table")
	}
	for _, ft := range feature.All() {
		if !strings.Contains(cmd.long, ft.Name) {
			t.Errorf("the session-start help never names %q", ft.Name)
		}
		if !strings.Contains(cmd.long, ft.Summary) {
			t.Errorf("the session-start help never says what %q does", ft.Name)
		}
	}
}

func TestSessionStartFastForwardsTheBaseCheckout(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")
	want := aheadOrigin(t, f)

	r := f.exec("session-start")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stdout, "fast-forwarded") {
		t.Errorf("stdout = %q, want the fast-forward reported", r.stdout)
	}
	if !strings.Contains(r.stdout, "1 commit behind origin/main") {
		t.Errorf("stdout = %q, want how far behind it was", r.stdout)
	}
	// On stdout by design, and nowhere else: a SessionStart hook's plain stdout
	// is added to the session as context, so this is the one message whose
	// reader is the agent rather than a person in a transcript.
	if r.stderr != "" {
		t.Errorf("stderr = %q, want nothing — the answer is for the agent to read", r.stderr)
	}
	if got := headOf(t, f, f.MainDir); got != want {
		t.Errorf("HEAD = %s, want origin's %s", got, want)
	}
}

func TestSessionStartSaysNothingWhenTheBaseCheckoutIsCurrent(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")
	aheadOrigin(t, f)
	f.mustRun("session-start")

	// Run again, with nothing left to do. The silence is the contract: the hook
	// fires at the start of every session, and a line saying "already current"
	// would be in every one of them.
	r := f.exec("session-start")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("output = %q / %q, want silence for a checkout with nothing to do", r.stdout, r.stderr)
	}
}

// TestSessionStartInAWorktreeLeavesTheBaseCheckoutAlone covers the scope rule
// that matters most. A session in a worktree is an agent working somewhere
// else, and the base checkout may have an agent of its own with work in flight
// — moving it from here is what `guard` refuses on an agent's behalf.
func TestSessionStartInAWorktreeLeavesTheBaseCheckoutAlone(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")
	f.Worktree("eng-1")
	aheadOrigin(t, f)
	before := headOf(t, f, f.MainDir)

	t.Chdir(f.DirFor("eng-1"))
	r := f.exec("session-start")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("output = %q / %q, want silence from a worktree", r.stdout, r.stderr)
	}
	if got := headOf(t, f, f.MainDir); got != before {
		t.Errorf("the base checkout moved to %s from a session in a worktree", got)
	}
}

// TestSessionStartIsSilentOffBaseBranch: the base checkout is the one place a
// person switches branches by hand, and one parked somewhere else is parked
// there deliberately.
func TestSessionStartIsSilentOffBaseBranch(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")
	aheadOrigin(t, f)
	f.Git(f.MainDir, "checkout", "--quiet", "-b", "hand-rolled")
	before := headOf(t, f, f.MainDir)

	r := f.exec("session-start")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("output = %q / %q, want silence off base_branch", r.stdout, r.stderr)
	}
	if got := headOf(t, f, f.MainDir); got != before {
		t.Errorf("a branch that is not base_branch moved to %s", got)
	}
}

func TestSessionStartIsSilentOutsideARepository(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")
	aheadOrigin(t, f)
	before := headOf(t, f, f.MainDir)

	// Not a git checkout at all, which is where most of an agent's sessions
	// happen: the hooks fire everywhere the agent runs.
	t.Chdir(t.TempDir())
	r := f.exec("session-start")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("output = %q / %q, want silence outside a repository", r.stdout, r.stderr)
	}
	if got := headOf(t, f, f.MainDir); got != before {
		t.Errorf("the base checkout moved to %s from outside the repository", got)
	}
}

func TestSessionStartDoesNothingForAConfigThatDidNotAskForIt(t *testing.T) {
	f := newFixture(t, "")
	aheadOrigin(t, f)
	before := headOf(t, f, f.MainDir)

	r := f.exec("session-start")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("output = %q / %q, want silence where nothing was switched on", r.stdout, r.stderr)
	}
	if got := headOf(t, f, f.MainDir); got != before {
		t.Errorf("the base checkout moved to %s in a repository that enabled nothing", got)
	}
}

// TestSessionStartReportsABranchItCannotFastForward: --ff-only refuses a
// diverged branch, and that refusal is the safety working rather than a fault
// to swallow. The session is in a repository that asked for this, so it is told.
func TestSessionStartReportsABranchItCannotFastForward(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")
	aheadOrigin(t, f)
	// A commit of the main checkout's own, on the same branch, touching the same
	// file: now neither side is an ancestor of the other.
	f.Commit(f.MainDir, "local and unpushed")
	before := headOf(t, f, f.MainDir)

	r := f.exec("session-start")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stdout, "could not be fast-forwarded") {
		t.Errorf("stdout = %q, want the refusal reported", r.stdout)
	}
	if got := headOf(t, f, f.MainDir); got != before {
		t.Errorf("HEAD = %s, want the diverged branch left exactly as it was (%s)", got, before)
	}
}

// TestSessionStartTakesNoArguments: being invoked wrong is the one loud failure,
// as it is for `signal`. Everything else about this command is a silence, so a
// mistyped invocation has to be the exception or it is a hook that does nothing
// with nothing to say.
func TestSessionStartTakesNoArguments(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")

	r := f.exec("session-start", "fresh-base")
	if r.err == nil {
		t.Fatalf("session-start took an argument: %s", r.both())
	}
}

// TestSetupRefreshKeepsTheFeaturesKey is the regression CLAUDE.md names
// outright: configSettings and settingsFrom must both know a key, or --refresh
// rewrites the file without it and the repository quietly stops doing what it
// was doing.
func TestSetupRefreshKeepsTheFeaturesKey(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")

	f.mustRun("setup", "--refresh")
	body, err := os.ReadFile(filepath.Join(f.registry, "proj.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `features = ["fresh-base"]`) {
		t.Fatalf("the refreshed config lost the features key:\n%s", body)
	}
	// And it still means what it meant, which the file's text alone does not
	// prove — a commented line looks much like a live one in a diff.
	if !strings.Contains(f.mustRun("config"), "fresh-base") {
		t.Errorf("config no longer reports the feature as set:\n%s", f.mustRun("config"))
	}
	aheadOrigin(t, f)
	if out := f.mustRun("session-start"); !strings.Contains(out, "fast-forwarded") {
		t.Errorf("session-start after a refresh = %q, want the feature still running", out)
	}
}

// TestSetupRefreshLeavesAnEmptyFeaturesListOff: absent and empty are the same
// value, so the rewrite is allowed to spell it either way — but it must not come
// back as something switched on.
func TestSetupRefreshLeavesAnEmptyFeaturesListOff(t *testing.T) {
	f := newFixture(t, "features = []\n")

	f.mustRun("setup", "--refresh")
	body, err := os.ReadFile(filepath.Join(f.registry, "proj.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "\nfeatures = ") {
		t.Errorf("the refreshed config switched something on:\n%s", body)
	}
	aheadOrigin(t, f)
	before := headOf(t, f, f.MainDir)
	f.mustRun("session-start")
	if got := headOf(t, f, f.MainDir); got != before {
		t.Errorf("the base checkout moved to %s after an empty features list was refreshed", got)
	}
}

// TestConfigReportsTheFeaturesInForce: the command exists to close the gap
// between a file and the behavior it produces, and a feature is behavior with no
// other line in the report to trace to.
func TestConfigReportsTheFeaturesInForce(t *testing.T) {
	f := newFixture(t, "features = ['fresh-base']\n")

	out := f.mustRun("config")
	if !strings.Contains(flat(out), "features fresh-base") {
		t.Errorf("config = %q, want a features row naming what is on", out)
	}

	bare := newFixture(t, "")
	if got := flat(bare.mustRun("config")); !strings.Contains(got, "features default (none)") {
		t.Errorf("config = %q, want features reported as unset", got)
	}
}

// TestDoctorWarnsAboutFeaturesWithNothingToRunThem covers the trap the key has.
// The moment a feature fires at is an agent hook, so a repository that switched
// one on without the wiring has done something whose every symptom is a success.
func TestDoctorWarnsAboutFeaturesWithNothingToRunThem(t *testing.T) {
	t.Run("no agent module to run them", func(t *testing.T) {
		f := newFixture(t, "features = ['fresh-base']\ncommand = 'nvim'\n")

		found := findings(t, f)
		if got := has(t, found, "no agent module to run them"); got != "warn" {
			t.Errorf("level = %q, want warn: %v", got, found)
		}
		if got := has(t, found, `agent = "claude"`); got == "" {
			t.Errorf("the finding never says what to write: %v", found)
		}
	})

	t.Run("a module whose plugin is installed nowhere", func(t *testing.T) {
		f := newFixture(t, "features = ['fresh-base']\n")

		found := findings(t, f)
		if got := has(t, found, "plugin is installed nowhere"); got != "warn" {
			t.Errorf("level = %q, want warn: %v", got, found)
		}
	})

	t.Run("wired, and reported as working", func(t *testing.T) {
		f := newFixture(t, "features = ['fresh-base']\n")
		f.mustRun("agent-init", "claude")

		found := findings(t, f)
		if got := has(t, found, "1 feature enabled"); got != "ok" {
			t.Errorf("level = %q, want ok: %v", got, found)
		}
	})

	t.Run("silent where nothing is switched on", func(t *testing.T) {
		f := newFixture(t, "")

		for _, fi := range findings(t, f) {
			if strings.Contains(fi.detail, "feature") {
				t.Errorf("doctor mentions features in a repository that wants none: %q", fi.detail)
			}
		}
	})
}
