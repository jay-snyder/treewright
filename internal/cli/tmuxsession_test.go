package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/gittest"
)

// These tests cover where a window is put, rather than what is in it: one session
// per repository, joined rather than duplicated, and never confused with the
// session next to it. They drive the private tmux server every fixture owns.
//
// Every fixture here configures a command that stays running, because a window
// whose command has exited is a window that has closed, and these assertions are
// about windows that exist.

// windowsIn lists a session's window names, in index order.
func windowsIn(t *testing.T, session string) []string {
	t.Helper()
	out, err := tmuxctl(t, "list-windows", "-t", "="+session, "-F", "#{window_name}")
	if err != nil {
		return nil
	}
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// activeWindowIn names the window a session would show if it were attached to.
//
// Asked with list-windows rather than display-message, whose target is a pane:
// there the exact form "=name" matches no pane, and tmux answers with an empty
// string and no error rather than with the session's current window.
func activeWindowIn(t *testing.T, session string) string {
	t.Helper()
	out, err := tmuxctl(t, "list-windows", "-t", "="+session, "-F", "#{?window_active,#{window_name},}")
	if err != nil {
		t.Fatalf("read the active window of %s: %v\n%s", session, err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			return name
		}
	}
	return ""
}

// addConfig registers a second repository, which is where mixing two repositories
// into one session used to happen.
func addConfig(t *testing.T, registry, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(registry, name+".toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write the %s config: %v", name, err)
	}
}

func TestNewOpensItsWindowInTheRepoSession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	r := f.exec("new", "eng-142-white-screen")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}

	// The session did not exist, so creating it is part of what happened and is
	// reported; and with no client to switch, the way to reach it is too.
	if !strings.Contains(r.stderr, "created tmux session proj") {
		t.Errorf("stderr = %q, want the new session reported", r.stderr)
	}
	// The way back in is given as treewright's own command rather than as a
	// tmux attach for the user to copy: it names the session exactly, and it
	// reaches the right server when TREEWRIGHT_TMUX_LABEL has aimed treewright at one —
	// which, in this test, it has.
	if !strings.Contains(r.stderr, "attach with: treewright attach proj") {
		t.Errorf("stderr = %q, want the attach command", r.stderr)
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "ENG-142" {
		t.Errorf("windows in session proj = %v, want just ENG-142", got)
	}
	// And the window is in the worktree, not wherever treewright was run.
	if got := panesOn(t, f.DirFor("eng-142-white-screen")); got != 1 {
		t.Errorf("%d panes in the new worktree, want 1", got)
	}
	// The window also records the worktree it was opened on, which is what
	// identifies it later however its shell wanders and wherever it is dragged to.
	// Checked here because this is the call that creates the session, the other of
	// the two paths a window is opened by.
	if got, want := windowStamp(t, "proj", "ENG-142", "@treewright_worktree"), f.DirFor("eng-142-white-screen"); got != want {
		t.Errorf("window ENG-142 records worktree %q, want %q", got, want)
	}
}

// TestNewRecordsTheWorktreeOnItsWindow covers the rest of what a window carries.
// Only the worktree is read back by treewright; the others exist so that a status
// line can name the worktree with "#{@treewright_slug}" instead of shelling out to git
// on every interval, and an option that is never written is one nobody can use.
func TestNewRecordsTheWorktreeOnItsWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	f.mustRun("new", "eng-142-white-screen")

	want := map[string]string{
		"@treewright_worktree": f.DirFor("eng-142-white-screen"),
		"@treewright_repo":     "proj",
		"@treewright_slug":     "eng-142-white-screen",
		"@treewright_branch":   f.BranchFor("eng-142-white-screen"),
	}
	for option, value := range want {
		if got := windowStamp(t, "proj", "ENG-142", option); got != value {
			t.Errorf("window ENG-142 has %s = %q, want %q", option, got, value)
		}
	}
}

// TestTheBaseWindowRecordsNoWorktree is the other half: the base window sits on a
// checkout rather than on a worktree, so it has no slug, and the branch it is
// parked on is the one thing here a user can change from inside the window —
// recording it would be recording something that goes stale as soon as they do.
func TestTheBaseWindowRecordsNoWorktree(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	f.mustRun("base")

	if got, want := windowStamp(t, "proj", "MAIN", "@treewright_worktree"), f.MainDir; got != want {
		t.Errorf("base window records worktree %q, want the main checkout %q", got, want)
	}
	if got := windowStamp(t, "proj", "MAIN", "@treewright_repo"); got != "proj" {
		t.Errorf("base window records repo %q, want proj", got)
	}
	for _, option := range []string{"@treewright_slug", "@treewright_branch"} {
		if got := windowStamp(t, "proj", "MAIN", option); got != "" {
			t.Errorf("base window has %s = %q, want it left unset", option, got)
		}
	}
}

// TestNewJoinsTheSessionAlreadyRunning is the other half: a second worktree is a
// second window in the same session, not a second session.
func TestNewJoinsTheSessionAlreadyRunning(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	f.mustRun("new", "eng-1")
	r := f.exec("new", "eng-2")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "created tmux session") {
		t.Errorf("stderr = %q, want no second session for the same repo", r.stderr)
	}

	got := windowsIn(t, "proj")
	if len(got) != 2 || got[0] != "ENG-1" || got[1] != "ENG-2" {
		t.Errorf("windows in session proj = %v, want ENG-1 and ENG-2", got)
	}
}

// TestNewDoesNotJoinASimilarlyNamedSession is the regression test for tmux's
// prefix matching on session names: with only "projector" running, a target of
// "proj" resolves to it, and this repository's window would land in a session
// belonging to something else entirely.
func TestNewDoesNotJoinASimilarlyNamedSession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "projector", "UNRELATED", f.Root)

	if r := f.exec("new", "eng-1"); r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "ENG-1" {
		t.Errorf("windows in session proj = %v, want just ENG-1", got)
	}
	if got := windowsIn(t, "projector"); len(got) != 1 || got[0] != "UNRELATED" {
		t.Errorf("windows in session projector = %v, want it untouched", got)
	}
}

// TestReposDoNotShareASession is the whole point: two repositories, two sessions,
// and neither one's windows in the other's status line.
func TestReposDoNotShareASession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	other := gittest.New(t)
	addConfig(t, f.registry, "other",
		"main_dir = '"+other.MainDir+"'\nbase_branch = 'main'\ncommand = 'sleep 300'\n")

	if r := f.exec("new", "eng-1"); r.err != nil {
		t.Fatalf("new in proj: %v\n%s", r.err, r.both())
	}
	if err := os.Chdir(other.MainDir); err != nil {
		t.Fatal(err)
	}
	if r := f.exec("new", "eng-2"); r.err != nil {
		t.Fatalf("new in other: %v\n%s", r.err, r.both())
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "ENG-1" {
		t.Errorf("windows in session proj = %v, want only its own worktree", got)
	}
	if got := windowsIn(t, "other"); len(got) != 1 || got[0] != "ENG-2" {
		t.Errorf("windows in session other = %v, want only its own worktree", got)
	}
}

// TestBaseSelectsTheOneBaseWindow covers what made two repositories in one
// session unreadable: every repo's base window is named after its base branch, so
// running base twice used to leave two windows called MAIN.
func TestBaseSelectsTheOneBaseWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	if r := f.exec("base"); r.err != nil {
		t.Fatalf("base: %v\n%s", r.err, r.both())
	}
	if r := f.exec("base"); r.err != nil {
		t.Fatalf("base again: %v\n%s", r.err, r.both())
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "MAIN" {
		t.Errorf("windows in session proj = %v, want one MAIN", got)
	}
	if got := panesOn(t, f.MainDir); got != 1 {
		t.Errorf("%d panes in the main checkout, want 1", got)
	}
}

// TestResumeUsesTheWindowOpenInAnotherSession covers the failure that made
// resuming across sessions look like nothing at all: selecting a window in a
// session your client is not attached to succeeds and changes nothing you can
// see. The window is now found wherever it is, said out loud, and selected there.
//
// Moving a client to it needs a client, which a headless test has none of; what is
// checked here is that no duplicate window is opened and that the existing one is
// made current in its own session.
func TestResumeUsesTheWindowOpenInAnotherSession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")
	wt := f.Worktree("eng-9")

	startSession(t, "elsewhere", "DECOY", f.Root)
	openWindowOn(t, "elsewhere", "ENG-9", wt.Dir)

	r := f.exec("resume", "eng-9")
	if r.err != nil {
		t.Fatalf("resume: %v\n%s", r.err, r.both())
	}

	if !strings.Contains(r.stderr, "window ENG-9 is in session elsewhere rather than proj") {
		t.Errorf("stderr = %q, want the foreign session named", r.stderr)
	}
	if got := panesOn(t, wt.Dir); got != 1 {
		t.Errorf("%d panes in the worktree, want 1 — a duplicate window was opened", got)
	}
	if got := activeWindowIn(t, "elsewhere"); got != "ENG-9" {
		t.Errorf("active window in session elsewhere = %q, want ENG-9 selected", got)
	}
}

// TestLsReportsTheOpenWindow pins the WINDOW column and the JSON fields behind it:
// the window's own name for this repository's session, and session:window for one
// that is somewhere else, since that is why resuming it would move you.
func TestLsReportsTheOpenWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	mine := f.Worktree("eng-1")
	stray := f.Worktree("eng-2")

	startSession(t, "proj", "MAIN", f.MainDir)
	openWindowOn(t, "proj", "ENG-1", mine.Dir)
	startSession(t, "elsewhere", "ENG-2", stray.Dir)

	r := f.exec("ls")
	if r.err != nil {
		t.Fatalf("ls: %v\n%s", r.err, r.both())
	}
	for _, want := range []string{"ENG-1", "elsewhere:ENG-2"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("table = %q, want %q in the WINDOW column", r.stdout, want)
		}
	}

	var rows []struct {
		Slug          string `json:"slug"`
		Window        string `json:"window"`
		WindowID      string `json:"window_id"`
		WindowSession string `json:"window_session"`
	}
	j := f.exec("ls", "--json")
	if err := json.Unmarshal([]byte(j.stdout), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, j.stdout)
	}
	for _, row := range rows {
		switch row.Slug {
		case "eng-1":
			if row.Window != "ENG-1" || row.WindowSession != "proj" {
				t.Errorf("row = %+v, want the window named in session proj", row)
			}
		case "eng-2":
			if row.Window != "ENG-2" || row.WindowSession != "elsewhere" {
				t.Errorf("row = %+v, want the window named in session elsewhere", row)
			}
		}
		// The id is what `tmux kill-window -t` takes, so it must be tmux's own
		// spelling rather than an index.
		if !strings.HasPrefix(row.WindowID, "@") {
			t.Errorf("window_id = %q, want a tmux window id", row.WindowID)
		}
	}
}

// TestTheWorktreesWindowIsFoundHoweverWindowsAreArranged covers the reported bug
// end to end. Two windows stand in one worktree — the worktree's own, and a base
// window whose shell followed a `treewright cd` into it — and which one treewright
// named used to depend on where they sat, because the pane listing walks windows
// in index order. Rearranging them renamed the window in `ls` and sent `resume`
// somewhere else.
func TestTheWorktreesWindowIsFoundHoweverWindowsAreArranged(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300'\n")

	wt := twoWindowsInOneWorktree(t, f)

	var rows []struct {
		Slug   string `json:"slug"`
		Window string `json:"window"`
	}
	j := f.exec("ls", "--json")
	if err := json.Unmarshal([]byte(j.stdout), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, j.stdout)
	}
	for _, row := range rows {
		if row.Slug == "eng-1" && row.Window != "ENG-1" {
			t.Errorf("window for eng-1 = %q, want the worktree's own window ENG-1", row.Window)
		}
	}

	if r := f.exec("resume", "eng-1"); r.err != nil {
		t.Fatalf("resume: %v\n%s", r.err, r.both())
	}
	if got := activeWindowIn(t, "proj"); got != "ENG-1" {
		t.Errorf("active window in session proj = %q, want ENG-1 rather than the window visiting it", got)
	}
	// Two panes stand in the worktree, and resuming must not make a third.
	if got := panesOn(t, wt); got != 2 {
		t.Errorf("%d panes in the worktree, want 2 — a duplicate window was opened", got)
	}
}

// TestSessionFor covers the naming rules without needing a server: the config's
// name by default, tmux_session when it is set, and no character that would make
// the session unaddressable once created.
func TestSessionFor(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"the config name by default", config.Config{Name: "myrepo"}, "myrepo"},
		{"tmux_session overrides it", config.Config{Name: "myrepo", TmuxSession: "work"}, "work"},
		{"a padded override is trimmed", config.Config{Name: "myrepo", TmuxSession: "  work  "}, "work"},
		{"a blank override falls back", config.Config{Name: "myrepo", TmuxSession: "   "}, "myrepo"},
		// A config file called my.repo.toml is a repo named my.repo, and a period
		// is the pane separator in every tmux target that would name it.
		{"a period in the name", config.Config{Name: "my.repo"}, "my-repo"},
		{"a colon in the override", config.Config{Name: "myrepo", TmuxSession: "a:b"}, "a-b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionFor(&tc.cfg); got != tc.want {
				t.Errorf("sessionFor(%+v) = %q, want %q", tc.cfg, got, tc.want)
			}
		})
	}
}
