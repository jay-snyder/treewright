package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/shellinit"
	"github.com/jay-snyder/treewright/internal/tmuxinit"
)

// What happens to an installation when treewright itself moves on.
//
// Every one of these covers a state that used to be invisible: a worktree whose
// copy of the agent plugin was made two releases ago, a tmux server holding
// bindings from a binary that is no longer installed, a shell whose wrapper
// predates both, a config written before the generator learned half of what it
// now writes, and a machine three releases behind the newest published one.
// None of them is broken enough to fail, which is exactly why each needs
// something that says so out loud.

// atVersion runs treewright as a build that calls itself version, since what a
// binary reports about itself is the input to the release check and the fixture
// deliberately reports something that is not a version at all.
func atVersion(version string, args ...string) result {
	var out, errOut bytes.Buffer
	err := Run(Env{Args: args, Version: version, Stdout: &out, Stderr: &errOut})
	return result{stdout: out.String(), stderr: errOut.String(), err: err}
}

// stubReleaseAPI stands in for GitHub, so the suite exercises the request, the
// parse and the comparison without ever leaving the machine.
func stubReleaseAPI(t *testing.T, status int, body string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	was := releaseAPI
	releaseAPI = server.URL
	t.Cleanup(func() { releaseAPI = was })
}

// detailOf returns the whole of the one finding containing substr, where has
// returns only its level — for the assertions whose subject is what a finding
// says rather than how much it matters.
func detailOf(t *testing.T, found []reportLine, substr string) string {
	t.Helper()
	for _, fi := range found {
		if strings.Contains(fi.detail, substr) {
			return fi.detail
		}
	}
	t.Fatalf("no finding contains %q: %v", substr, found)
	return ""
}

// stalePlugin overwrites a checkout's copy of the hooks with something an older
// treewright would plausibly have written: still a plugin, wired to a verb that
// no longer exists.
func stalePlugin(t *testing.T, checkout string) {
	t.Helper()
	hooks := filepath.Join(checkout, ".claude", "skills", "treewright", "hooks", "hooks.json")
	body := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"treewright signal finished"}]}]}}`
	if err := os.WriteFile(hooks, []byte(body), 0o644); err != nil {
		t.Fatalf("write the older wiring into %s: %v", checkout, err)
	}
}

// ---- the plugin in a worktree ------------------------------------------------

// TestDoctorNoticesAWorktreeLeftOnAnOlderPlugin is the gap the carry left.
//
// A worktree gets its copy of the plugin once, when `new` copies it in, and
// nothing looked at it again: doctor inspected the main checkout and the
// user-level directory and enumerated no worktrees at all. So a worktree made
// the day before an upgrade ran its agent against the old hooks for the rest of
// its life, and the report stayed green the whole time — the same frozen copy
// the plugin exists to abolish, one directory further down.
func TestDoctorNoticesAWorktreeLeftOnAnOlderPlugin(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")
	f.mustRun("agent-init", "claude")
	f.mustRun("new", "alpha")
	f.mustRun("new", "beta")

	if got := has(t, findings(t, f), "agent plugin current in 2 worktrees"); got != "ok" {
		t.Errorf("finding = %q, want both worktrees reported current to start with", got)
	}

	stalePlugin(t, f.DirFor("alpha"))

	found := findings(t, f)
	if got := has(t, found, "agent plugin out of date in 1 worktree"); got != "warn" {
		t.Errorf("finding = %q, want the stale worktree named\nall: %v", got, found)
	}
	stale := detailOf(t, found, "agent plugin out of date")
	// Named, not just counted: a repository with six worktrees needs to know
	// which one, and the one that is fine must not be swept in with it.
	if !strings.Contains(stale, "alpha") {
		t.Errorf("finding = %q, want the stale worktree named", stale)
	}
	if strings.Contains(stale, "beta") {
		t.Errorf("finding = %q, sweeps in a worktree whose copy is current", stale)
	}
	// And the command it names is the one that ends it.
	if !strings.Contains(stale, "refresh") {
		t.Errorf("finding = %q, want it to name the command that fixes this", stale)
	}
	f.mustRun("refresh")
	if got := has(t, findings(t, f), "agent plugin out of date"); got != "" {
		t.Errorf("finding = %q, want the warning gone after a refresh", got)
	}
}

// TestDoctorNoticesAWorktreeThePluginNeverReached covers the other half: a
// worktree made before the plugin was installed at all. The carry runs once, at
// `new` time, so it had nothing to copy — and every worktree older than the
// wiring stays unwired with nothing to say so.
func TestDoctorNoticesAWorktreeThePluginNeverReached(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")
	f.mustRun("new", "early") // before there is any plugin to carry
	f.mustRun("agent-init", "claude")

	found := findings(t, f)
	if got := has(t, found, "agent plugin missing from 1 worktree"); got != "warn" {
		t.Errorf("finding = %q, want the unwired worktree named\nall: %v", got, found)
	}

	f.mustRun("refresh")
	if _, err := os.Stat(filepath.Join(f.DirFor("early"), ".claude", "skills", "treewright", "hooks", "hooks.json")); err != nil {
		t.Errorf("refresh did not give the older worktree a copy: %v", err)
	}
	if got := has(t, findings(t, f), "agent plugin missing from"); got != "" {
		t.Errorf("finding = %q, want the warning gone after a refresh", got)
	}
}

// TestAnUncarriedWorktreeIsNotReportedTwice: without the agent key nothing
// carries the plugin anywhere, and that is already one finding about the
// repository. Repeating it per worktree would turn one misconfiguration into a
// column of them.
func TestAnUncarriedWorktreeIsNotReportedTwice(t *testing.T) {
	f := agentFixture(t, "")
	f.mustRun("agent-init", "claude")
	f.mustRun("new", "alpha")

	found := findings(t, f)
	if got := has(t, found, "reaches no worktree"); got != "warn" {
		t.Errorf("finding = %q, want the carry trap named once\nall: %v", got, found)
	}
	if got := has(t, found, "agent plugin missing from"); got != "" {
		t.Errorf("finding = %q, want no per-worktree finding where nothing carries the plugin", got)
	}
}

// ---- refresh -----------------------------------------------------------------

// TestRefreshNamesWhatMovedInEachCheckout: the interesting run is the one after
// an upgrade, and "wrote hooks/hooks.json in eng-1" says which part of the
// wiring had gone stale where "updated 6 checkouts" says only that something
// did.
func TestRefreshNamesWhatMovedInEachCheckout(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")
	f.mustRun("agent-init", "claude")
	f.mustRun("new", "alpha")
	f.mustRun("new", "beta")
	stalePlugin(t, f.DirFor("alpha"))

	r := f.exec("refresh")
	if r.err != nil {
		t.Fatalf("refresh: %v\n%s", r.err, r.both())
	}
	// Nothing on stdout: refresh is an action, and what it did is narration.
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing for a consumer to read", r.stdout)
	}
	flatOut := flat(r.stderr)
	if !strings.Contains(flatOut, "alpha hooks/hooks.json") {
		t.Errorf("stderr = %q, want the checkout and the file it rewrote", r.stderr)
	}
	if strings.Contains(flatOut, "beta") {
		t.Errorf("stderr = %q, want checkouts that were already current left out", r.stderr)
	}
	// Hooks do not hot-reload, so a session already open keeps what it loaded.
	if !strings.Contains(r.stderr, "/reload-plugins") {
		t.Errorf("stderr = %q, want the reload named", r.stderr)
	}

	// Run twice, it says so rather than reporting the same files again.
	second := f.exec("refresh")
	if !strings.Contains(second.stderr, "already up to date in 3 checkouts") {
		t.Errorf("stderr = %q, want the second run reported as a no-op over every checkout", second.stderr)
	}
}

// TestRefreshInstallsNothingNew is the line between this and agent-init. It is
// the command people run without reading it, and which repositories treewright
// writes into is a decision `agent-init` exists to take — never one that gets
// made for you by an upgrade.
func TestRefreshInstallsNothingNew(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")
	f.mustRun("new", "alpha")

	r := f.exec("refresh")
	if r.err != nil {
		t.Fatalf("refresh: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "installed nowhere") || !strings.Contains(r.stderr, "agent-init claude") {
		t.Errorf("stderr = %q, want it to say nothing was installed and name what installs it", r.stderr)
	}
	for _, dir := range []string{f.MainDir, f.DirFor("alpha")} {
		if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "treewright")); err == nil {
			t.Errorf("refresh installed a plugin into %s, which had none", dir)
		}
	}
}

// TestRefreshSaysWhatItCannotReach: a wrapper function lives in the shell that
// loaded it, and no process can define one in its parent. Saying so is the whole
// of what this can do about it, and leaving it unsaid is how somebody runs
// refresh after an upgrade and keeps the old wrapper for the rest of the day.
func TestRefreshSaysWhatItCannotReach(t *testing.T) {
	f := agentFixture(t, "agent = 'claude'\n")
	t.Setenv("TREEWRIGHT_EVAL_FILE", filepath.Join(t.TempDir(), "eval"))
	t.Setenv(shellinit.VersionVar, "000000000000")

	if got := f.exec("refresh").stderr; !strings.Contains(got, "open a new terminal") {
		t.Errorf("stderr = %q, want the one thing refresh cannot do named", got)
	}

	current, err := shellinit.Version("zsh")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	t.Setenv(shellinit.VersionVar, current)
	if got := f.exec("refresh").stderr; strings.Contains(got, "open a new terminal") {
		t.Errorf("stderr = %q, want silence about a wrapper that is already current", got)
	}
}

// ---- the tmux server ---------------------------------------------------------

// TestDoctorTellsALoadedTmuxIntegrationFromACurrentOne covers what
// tmux.HasBindings alone could never answer. It is a substring test for
// "treewright" in list-keys, so a binding loaded by a two-release-old binary
// reads exactly like a current one — and a tmux server routinely outlives an
// upgrade by weeks.
func TestDoctorTellsALoadedTmuxIntegrationFromACurrentOne(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "proj", "MAIN", f.MainDir)

	// A binding an older treewright loaded: it reaches treewright, and it left no
	// stamp because the treewright that wrote it had none to leave.
	if out, err := tmuxctl(t, "bind-key", "T", "run-shell", "-b", "treewright popup resume"); err != nil {
		t.Fatalf("bind an older treewright's key: %v\n%s", err, out)
	}

	found := findings(t, f)
	if got := has(t, found, "tmux integration loaded, but not by this treewright"); got != "warn" {
		t.Errorf("finding = %q, want the bindings reported as somebody else's\nall: %v", got, found)
	}

	if r := f.exec("tmux-init", "--apply"); r.err != nil {
		t.Fatalf("tmux-init --apply: %v\n%s", r.err, r.both())
	}
	after := findings(t, f)
	if got := has(t, after, "tmux integration loaded"); got != "ok" {
		t.Errorf("finding = %q, want the reloaded bindings reported as current\nall: %v", got, after)
	}
}

// TestRefreshReloadsTheBindingsOnTheKeysTheyAreOn is the hazard in reloading at
// all. The keys are flags, set at the tmux.conf line that loads the snippet, so
// a reload that quietly bound the defaults as well would hand back keys the user
// deliberately moved away from — and take whatever they had put on T and N.
func TestRefreshReloadsTheBindingsOnTheKeysTheyAreOn(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "proj", "MAIN", f.MainDir)

	if r := f.exec("tmux-init", "--apply", "--resume-key", "G", "--new-key", "C-n"); r.err != nil {
		t.Fatalf("tmux-init --apply: %v\n%s", r.err, r.both())
	}
	// Wound back to a stamp no treewright ever wrote, so the reload has something
	// to correct.
	if out, err := tmuxctl(t, "set", "-g", tmuxinit.VersionOption, "000000000000"); err != nil {
		t.Fatalf("wind the stamp back: %v\n%s", err, out)
	}

	r := f.exec("refresh")
	if r.err != nil {
		t.Fatalf("refresh: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(flat(r.stderr), "switch worktree G") {
		t.Errorf("stderr = %q, want the keys it put the bindings back on", r.stderr)
	}

	keys, err := tmuxctl(t, "list-keys", "-T", "prefix")
	if err != nil {
		t.Fatalf("list the key bindings: %v\n%s", err, keys)
	}
	for line := range strings.SplitSeq(keys, "\n") {
		if !strings.Contains(line, "treewright") {
			continue
		}
		if strings.Contains(line, "-T prefix T ") || strings.Contains(line, "-T prefix N ") {
			t.Errorf("refresh bound a default key over the user's own choice: %q", line)
		}
	}
	if got, err := tmuxctl(t, "show-options", "-gv", tmuxinit.VersionOption); err != nil || got != tmuxinit.Version() {
		t.Errorf("stamp = %q (%v), want %q", got, err, tmuxinit.Version())
	}
}

// TestRefreshLoadsNoBindingsIntoAServerHoldingNone: which keys a tmux server
// binds is a decision made in a file the user owns, and an upgrade is not where
// it gets made for them.
func TestRefreshLoadsNoBindingsIntoAServerHoldingNone(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "proj", "MAIN", f.MainDir)

	r := f.exec("refresh")
	if r.err != nil {
		t.Fatalf("refresh: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "tmux-init --apply") {
		t.Errorf("stderr = %q, want the line that loads them named instead", r.stderr)
	}
	keys, err := tmuxctl(t, "list-keys", "-T", "prefix")
	if err != nil {
		t.Fatalf("list the key bindings: %v\n%s", err, keys)
	}
	if strings.Contains(keys, "treewright") {
		t.Errorf("refresh bound keys in a server that had none:\n%s", keys)
	}
}

// ---- the shell wrapper -------------------------------------------------------

// TestDoctorTellsALoadedShellWrapperFromACurrentOne covers the integration that
// can only be inferred. The binary cannot read its parent's function table, so
// "loaded" was the whole of what doctor could say — and a terminal open since
// two releases ago says it exactly as one opened a minute ago does.
func TestDoctorTellsALoadedShellWrapperFromACurrentOne(t *testing.T) {
	f := newFixture(t, "")
	t.Setenv("TREEWRIGHT_EVAL_FILE", filepath.Join(t.TempDir(), "eval"))

	t.Setenv(shellinit.VersionVar, "")
	if got := has(t, findings(t, f), "shell integration loaded, but not by this treewright"); got != "warn" {
		t.Errorf("finding = %q, want a wrapper with no version reported as an older one", got)
	}

	t.Setenv(shellinit.VersionVar, "000000000000")
	found := findings(t, f)
	if got := has(t, found, "shell integration loaded, but not by this treewright"); got != "warn" {
		t.Errorf("finding = %q, want a wrapper from another build reported\nall: %v", got, found)
	}
	if got := has(t, found, "open a new terminal"); got != "warn" {
		t.Errorf("finding = %q, want the fix named — treewright cannot do this one itself", got)
	}

	current, err := shellinit.Version("bash")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	t.Setenv(shellinit.VersionVar, current)
	after := findings(t, f)
	if got := has(t, after, "shell integration loaded"); got != "ok" {
		t.Errorf("finding = %q, want a current wrapper reported ok\nall: %v", got, after)
	}
}

// ---- the release check -------------------------------------------------------

func TestComparingVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.2.0", -1},
		{"v0.2.0", "v0.1.0", 1},
		{"v0.1.0", "v0.1.0", 0},
		{"v0.9.0", "v0.10.0", -1}, // ordered as numbers, not as text
		{"v1.0.0", "v0.99.99", 1},
		// A build from a modified tree is still that release: "+dirty" says
		// something about the tree it came from, not about which version it is.
		{"v0.1.0+dirty", "v0.1.0", 0},
		// A pseudo-version names the release it is on the way to, and is not it.
		{"v0.1.1-0.20250101000000-abcdef123456", "v0.1.1", -1},
		{"v0.1.1-0.20250101000000-abcdef123456", "v0.1.0", 1},
		// Nothing comparable compares equal, so a caller that skipped the check
		// reports no upgrade rather than one to a version that does not exist.
		{"dev", "v9.9.9", 0},
		{"v0.1.0", "not-a-version", 0},
	}
	for _, tc := range tests {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestTheUpgradeCommandFollowsTheInstallRoute(t *testing.T) {
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", filepath.Join("/home/dev", "go"))

	tests := []struct{ exe, want string }{
		{"/opt/homebrew/Cellar/treewright/0.1.0/bin/treewright", "brew upgrade --cask treewright"},
		{"/opt/homebrew/Caskroom/treewright/0.1.0/treewright", "brew upgrade --cask treewright"},
		{"/home/dev/go/bin/treewright", "go install github.com/jay-snyder/treewright@latest"},
		// A tarball unpacked wherever the reader felt like it. Naming the wrong
		// command is worse than naming none: `brew upgrade` told to somebody who
		// never used brew fails in a way that reads as treewright being broken.
		{"/home/dev/bin/treewright", ""},
		{"/usr/local/bin/treewright", ""},
	}
	for _, tc := range tests {
		if got := upgradeCommandFor(tc.exe); got != tc.want {
			t.Errorf("upgradeCommandFor(%q) = %q, want %q", tc.exe, got, tc.want)
		}
	}
}

// TestVersionCheckNamesANewerRelease pins the stream split as much as the
// finding: --check is safe to leave in a shell profile because the version on
// stdout stays the one line it has always been.
func TestVersionCheckNamesANewerRelease(t *testing.T) {
	stubReleaseAPI(t, http.StatusOK, `{"tag_name": "v9.9.9"}`)

	r := atVersion("v0.1.0", "version", "--check")
	if r.err != nil {
		t.Fatalf("version --check: %v\n%s", r.err, r.both())
	}
	if r.stdout != "treewright v0.1.0\n" {
		t.Errorf("stdout = %q, want the version line alone", r.stdout)
	}
	if !strings.Contains(r.stderr, "v9.9.9 is out") {
		t.Errorf("stderr = %q, want the newer release named", r.stderr)
	}

	// The plain form asks nothing and says nothing: this is the only thing in
	// treewright that leaves the machine, and it happens when it is asked for.
	if plain := atVersion("v0.1.0", "version"); plain.stderr != "" {
		t.Errorf("stderr = %q, want no check without --check", plain.stderr)
	}
}

func TestVersionCheckOnTheLatestRelease(t *testing.T) {
	stubReleaseAPI(t, http.StatusOK, `{"tag_name": "v0.1.0"}`)

	r := atVersion("v0.1.0", "version", "--check")
	if !strings.Contains(r.stderr, "v0.1.0 is the latest release") {
		t.Errorf("stderr = %q, want it said that this is the newest", r.stderr)
	}
}

// TestABuildWithNoVersionIsSaidRatherThanGuessedAt: "dev" is not older than
// anything, and reporting it as behind would send somebody upgrading a build
// they compiled themselves an hour ago.
func TestABuildWithNoVersionIsSaidRatherThanGuessedAt(t *testing.T) {
	// The API is stubbed to fail loudly, because a build with nothing to compare
	// must not reach it at all — which is also what keeps the rest of this suite
	// off the network.
	stubReleaseAPI(t, http.StatusInternalServerError, "the check should never get here")

	r := atVersion("dev", "version", "--check")
	if !strings.Contains(r.stderr, "not a released version") {
		t.Errorf("stderr = %q, want the reason nothing was compared", r.stderr)
	}
}

// TestAnOfflineCheckIsSilentInDoctorAndSpokenInVersion is the one place the two
// callers deliberately differ. An offline laptop must not come back from doctor
// with a warning about the network it is not on; somebody who typed --check and
// got nothing would reasonably conclude they are up to date.
func TestAnOfflineCheckIsSilentInDoctorAndSpokenInVersion(t *testing.T) {
	stubReleaseAPI(t, http.StatusForbidden, `{"message": "API rate limit exceeded"}`)

	if got := atVersion("v0.1.0", "version", "--check").stderr; !strings.Contains(got, "could not reach") {
		t.Errorf("stderr = %q, want the check reported as unanswered", got)
	}

	// The fixture is here for its registry and its working directory, so that
	// doctor has a repository to answer about at all.
	newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	if got := atVersion("v0.1.0", "doctor").stdout; strings.Contains(got, "release") {
		t.Errorf("doctor = %q, want no finding about a check it could not make", got)
	}
}

func TestDoctorReportsANewerRelease(t *testing.T) {
	newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	stubReleaseAPI(t, http.StatusOK, `{"tag_name": "v9.9.9"}`)

	out := atVersion("v0.1.0", "doctor").stdout
	if !strings.Contains(out, "treewright v9.9.9 is out, and this is v0.1.0") {
		t.Errorf("doctor = %q, want the newer release reported", out)
	}
	if !strings.Contains(out, "upgrade") {
		t.Errorf("doctor = %q, want the finding to say what to do about it", out)
	}
	// A stale binary is a warning and never a failure: it works, and the exit
	// code is what a setup script gates on.
	if strings.Contains(out, "fail  release") {
		t.Errorf("doctor = %q, want a warning rather than a failure", out)
	}
}

// ---- the config format -------------------------------------------------------

func TestSetupWritesTheConfigVersion(t *testing.T) {
	f := newFixture(t, "")

	body := f.exec("setup", "-n", "proj").stdout
	if !strings.Contains(body, "version = 1") {
		t.Errorf("generated config = %q, want the format version recorded", body)
	}
	// And it has to be a key Load accepts, or every generated config is refused
	// by the very next command.
	path := filepath.Join(t.TempDir(), "generated.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err != nil {
		t.Errorf("the generated config does not load: %v", err)
	}
}

// TestDoctorWarnsAboutAConfigOlderThanTheGenerator: every config in the wild
// predates the version key, so this is the finding most people meet first — and
// what it costs is real, since `setup` refused to overwrite an existing config
// and so every improvement to the generated file reached new repositories only.
func TestDoctorWarnsAboutAConfigOlderThanTheGenerator(t *testing.T) {
	f := newFixture(t, "") // the fixture writes a config by hand, with no version

	found := findings(t, f)
	if got := has(t, found, "config version not recorded"); got != "warn" {
		t.Errorf("finding = %q, want the versionless config named\nall: %v", got, found)
	}
	if got := has(t, found, "setup --refresh proj"); got != "warn" {
		t.Errorf("finding = %q, want the command that answers it", got)
	}

	f.mustRun("setup", "--refresh")
	if got := has(t, findings(t, f), "config version"); got != "" {
		t.Errorf("finding = %q, want nothing said once the file is current", got)
	}
}

func TestDoctorWarnsAboutAConfigFromANewerTreewright(t *testing.T) {
	f := newFixture(t, "version = 99\n")

	found := findings(t, f)
	if got := has(t, found, "from a newer treewright than this one"); got != "warn" {
		t.Errorf("finding = %q, want the skew named\nall: %v", got, found)
	}
}

// TestSetupRefreshKeepsEverySettingItFinds is the whole risk in regenerating a
// file somebody has edited. A refresh that re-detected would undo every
// correction that disagrees with what treewright would guess today; one that
// rendered from a struct too small to hold a command would come back launching
// the wrong program.
func TestSetupRefreshKeepsEverySettingItFinds(t *testing.T) {
	f := newFixture(t, "")
	f.Write(f.MainDir, ".env", "SECRET=1\n")
	f.writeConfig("main_dir = '" + f.MainDir + "'\n" +
		"base_branch = 'main'\n" +
		"branch_prefixes = ['feature/', 'bug/']\n" +
		"carry_files = ['.env']\n" +
		"command = 'nvim'\n" +
		"resume_command = 'nvim -c :Resume'\n" +
		"post_create = ['echo one', 'echo two']\n" +
		"ticket_pattern = ''\n" +
		"tmux_session = 'shop'\n")

	if _, err := f.run("setup", "--refresh"); err != nil {
		t.Fatalf("setup --refresh: %v", err)
	}

	cfg, err := config.Load(filepath.Join(f.registry, "proj.toml"))
	if err != nil {
		t.Fatalf("the refreshed config does not load: %v", err)
	}
	if cfg.Version != config.FormatVersion {
		t.Errorf("version = %d, want %d — the version is what a refresh is for", cfg.Version, config.FormatVersion)
	}
	for _, tc := range []struct{ what, got, want string }{
		{"base_branch", cfg.BaseBranch, "main"},
		{"command", cfg.Command, "nvim"},
		{"resume_command", cfg.ResumeCommand, "nvim -c :Resume"},
		{"tmux_session", cfg.TmuxSession, "shop"},
		{"branch_prefixes", strings.Join(cfg.BranchPrefixes, ","), "feature/,bug/"},
		{"carry_files", strings.Join(cfg.CarryFiles, ","), ".env"},
		{"post_create", strings.Join(cfg.PostCreate, ","), "echo one,echo two"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q after a refresh, want %q", tc.what, tc.got, tc.want)
		}
	}
	// The setting whose empty value is a decision. Dropped for looking empty, it
	// would turn every window name in this repository back into a ticket hunt.
	if !cfg.Explicit("ticket_pattern") || cfg.TicketPattern != "" {
		t.Errorf("ticket_pattern = %q (explicit %v), want the opt-out kept",
			cfg.TicketPattern, cfg.Explicit("ticket_pattern"))
	}
	// The prefixes keep the spelling the file used: a config that listed several
	// must not come back under the singular key, and no config may set both.
	body, err := os.ReadFile(filepath.Join(f.registry, "proj.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "branch_prefixes = [") {
		t.Errorf("refreshed config = %q, want the plural spelling kept", body)
	}
}

// TestSetupRefreshDryRunWritesNothing keeps the look-before-it-writes path that
// -n is for, on the one command here that rewrites a file somebody has edited.
func TestSetupRefreshDryRunWritesNothing(t *testing.T) {
	f := newFixture(t, "")
	before, err := os.ReadFile(filepath.Join(f.registry, "proj.toml"))
	if err != nil {
		t.Fatal(err)
	}

	r := f.exec("setup", "--refresh", "-n")
	if r.err != nil {
		t.Fatalf("setup --refresh -n: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stdout, "version = 1") {
		t.Errorf("stdout = %q, want the config it would write", r.stdout)
	}
	if !strings.Contains(r.stderr, "nothing was written") {
		t.Errorf("stderr = %q, want it said that nothing was written", r.stderr)
	}
	after, err := os.ReadFile(filepath.Join(f.registry, "proj.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("the config was rewritten by a dry run:\n%s", after)
	}
}

// TestAnUnknownSettingMentionsVersionSkew: a config is a file two treewrights
// may see, and the newer one's settings arrive in the older one as typos. The
// reader otherwise hunts for a misspelling that is not there.
func TestAnUnknownSettingMentionsVersionSkew(t *testing.T) {
	f := newFixture(t, "")
	f.setConfig("main_dir = '" + f.MainDir + "'\nsomething_from_the_future = true\n")

	out, err := f.run("ls")
	if err == nil {
		t.Fatal("want an error for an unknown setting")
	}
	combined := out + err.Error()
	if !strings.Contains(combined, "something_from_the_future") {
		t.Errorf("error = %q, want the setting named", combined)
	}
	if !strings.Contains(combined, "a newer one may have added them") {
		t.Errorf("error = %q, want version skew offered as the other reading", combined)
	}
}
