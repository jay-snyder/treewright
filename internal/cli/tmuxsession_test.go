package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/gittest"
	"github.com/jay-snyder/treewright/internal/testenv"
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
	for line := range strings.SplitSeq(out, "\n") {
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
	if !strings.Contains(flat(r.stderr), "attach with treewright attach proj") {
		t.Errorf("stderr = %q, want the attach command", r.stderr)
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "eng-142" {
		t.Errorf("windows in session proj = %v, want just eng-142", got)
	}
	// And the window is in the worktree, not wherever treewright was run.
	if got := panesOn(t, f.DirFor("eng-142-white-screen")); got != 1 {
		t.Errorf("%d panes in the new worktree, want 1", got)
	}
	// The window also records the worktree it was opened on, which is what
	// identifies it later however its shell wanders and wherever it is dragged to.
	// Checked here because this is the call that creates the session, the other of
	// the two paths a window is opened by.
	if got, want := windowStamp(t, "eng-142", "@treewright_worktree"), f.DirFor("eng-142-white-screen"); got != want {
		t.Errorf("window eng-142 records worktree %q, want %q", got, want)
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
		if got := windowStamp(t, "eng-142", option); got != value {
			t.Errorf("window eng-142 has %s = %q, want %q", option, got, value)
		}
	}
}

// TestNewWorksFromADescriptionWithNoTicket is the repository that tracks no
// tickets, end to end. Everything but the window name is already indifferent to
// where a slug came from — this pins that the window name is too, and that two
// pieces of work named only by description still get worktrees and branches of
// their own.
//
// ticket_pattern = "" is the opt-out, so "api-2-fix" — a slug the default
// pattern would read as key api-2 — keeps its whole name here.
func TestNewWorksFromADescriptionWithNoTicket(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nticket_pattern = ''\n")

	// One slug over the cap and one under it, so this covers a description that
	// is shortened as well as one that is not: "dark-mode-toggle" is a rune over
	// and comes back whole, since marking it would cost the column it saved.
	for _, tc := range []struct{ slug, window string }{
		{"dark-mode-toggle", "dark-mode-toggle"},
		{"payment-retries-audit", "payment-retries…"},
		{"api-2-fix", "api-2-fix"},
	} {
		f.mustRun("new", tc.slug)

		if got := windowStamp(t, tc.window, "@treewright_slug"); got != tc.slug {
			t.Errorf("window %s has @treewright_slug = %q, want %q", tc.window, got, tc.slug)
		}
		if got := windowStamp(t, tc.window, "@treewright_worktree"); got != f.DirFor(tc.slug) {
			t.Errorf("window %s sits on %q, want %q", tc.window, got, f.DirFor(tc.slug))
		}
		if got := windowStamp(t, tc.window, "@treewright_branch"); got != f.BranchFor(tc.slug) {
			t.Errorf("window %s is on branch %q, want %q", tc.window, got, f.BranchFor(tc.slug))
		}
	}
}

// TestNewShortensALongSlugUnderTheDefaultPattern is the same fallback without the
// opt-out, which a slug carrying no ticket key reaches too — turning the pattern
// off is for slugs that would otherwise be *mistaken* for keys, not a requirement
// for working without one.
func TestNewShortensALongSlugUnderTheDefaultPattern(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	f.mustRun("new", "flaky-payment-test")

	if got := windowStamp(t, "flaky-payment-t…", "@treewright_slug"); got != "flaky-payment-test" {
		t.Errorf("window flaky-payment-t… has @treewright_slug = %q, want %q", got, "flaky-payment-test")
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

	if got, want := windowStamp(t, "main", "@treewright_worktree"), f.MainDir; got != want {
		t.Errorf("base window records worktree %q, want the main checkout %q", got, want)
	}
	if got := windowStamp(t, "main", "@treewright_repo"); got != "proj" {
		t.Errorf("base window records repo %q, want proj", got)
	}
	for _, option := range []string{"@treewright_slug", "@treewright_branch"} {
		if got := windowStamp(t, "main", option); got != "" {
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
	if len(got) != 2 || got[0] != "eng-1" || got[1] != "eng-2" {
		t.Errorf("windows in session proj = %v, want eng-1 and eng-2", got)
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

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "eng-1" {
		t.Errorf("windows in session proj = %v, want just eng-1", got)
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
	t.Chdir(other.MainDir)
	if r := f.exec("new", "eng-2"); r.err != nil {
		t.Fatalf("new in other: %v\n%s", r.err, r.both())
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "eng-1" {
		t.Errorf("windows in session proj = %v, want only its own worktree", got)
	}
	if got := windowsIn(t, "other"); len(got) != 1 || got[0] != "eng-2" {
		t.Errorf("windows in session other = %v, want only its own worktree", got)
	}
}

// TestBaseSelectsTheOneBaseWindow covers what made two repositories in one
// session unreadable: every repo's base window is named after its base branch, so
// running base twice used to leave two windows called main.
func TestBaseSelectsTheOneBaseWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	if r := f.exec("base"); r.err != nil {
		t.Fatalf("base: %v\n%s", r.err, r.both())
	}
	if r := f.exec("base"); r.err != nil {
		t.Fatalf("base again: %v\n%s", r.err, r.both())
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "main" {
		t.Errorf("windows in session proj = %v, want one main", got)
	}
	if got := panesOn(t, f.MainDir); got != 1 {
		t.Errorf("%d panes in the main checkout, want 1", got)
	}
}

// TestBaseDoesNotMistakeTheCallersOwnShellForTheBaseWindow is the reported bug:
// `tw base`, typed at a shell standing in the main checkout in a session of the
// user's own, did nothing at all. The shell was found as the window on the main
// checkout, so treewright warned that it was switching to a window in another
// session — naming a session that did not exist — and then switched the client to
// the session it was already in, which is nothing anyone can see. No session, no
// window, no agent.
func TestBaseDoesNotMistakeTheCallersOwnShellForTheBaseWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	// Where you are when you type it: a shell of your own, in the main checkout.
	startSession(t, "elsewhere", "SHELL", f.MainDir)
	insideSession(t, "elsewhere")

	r := f.exec("base")
	if r.err != nil {
		t.Fatalf("base: %v\n%s", r.err, r.both())
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "main" {
		t.Errorf("windows in session proj = %v, want the base window opened", got)
	}
	if strings.Contains(r.stderr, "rather than proj") {
		t.Errorf("stderr = %q, want no switch to a window that is only the caller's own shell", r.stderr)
	}
	// And the shell is left where it was: this opens the window the user asked
	// for, it does not take their own window over.
	if got := windowsIn(t, "elsewhere"); len(got) != 1 || got[0] != "SHELL" {
		t.Errorf("windows in session elsewhere = %v, want it untouched", got)
	}
}

// TestBaseFromInsideTheBaseWindowStaysWhereItIs is the exemption that keeps the
// rule above from opening a second window every time: a window treewright opened
// on the checkout is that checkout's window however it is reached, so running
// `base` inside it is the no-op it has always been — you are already there.
func TestBaseFromInsideTheBaseWindowStaysWhereItIs(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	f.mustRun("base")
	insideSession(t, "proj")

	if r := f.exec("base"); r.err != nil {
		t.Fatalf("base again: %v\n%s", r.err, r.both())
	}

	if got := windowsIn(t, "proj"); len(got) != 1 || got[0] != "main" {
		t.Errorf("windows in session proj = %v, want the one base window", got)
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
	openWindowOn(t, "elsewhere", "eng-9", wt.Dir)

	r := f.exec("resume", "eng-9")
	if r.err != nil {
		t.Fatalf("resume: %v\n%s", r.err, r.both())
	}

	if !strings.Contains(flat(r.stderr), "window eng-9 is in session elsewhere, not proj") {
		t.Errorf("stderr = %q, want the foreign session named", r.stderr)
	}
	// And the way in has to reach that session rather than this repository's,
	// which `treewright attach proj` would — a session that here is not even
	// running, so the advice would be a dead end. `attach` takes a repository, so
	// a session belonging to none is spelled as the tmux command it is, aimed at
	// the server treewright is talking to.
	if !strings.Contains(r.stderr, "attach-session -t =elsewhere") {
		t.Errorf("stderr = %q, want the way into the session the window is actually in", r.stderr)
	}
	if strings.Contains(r.stderr, "attach proj") {
		t.Errorf("stderr = %q, want no attach to a session the window is not in", r.stderr)
	}
	if got := panesOn(t, wt.Dir); got != 1 {
		t.Errorf("%d panes in the worktree, want 1 — a duplicate window was opened", got)
	}
	if got := activeWindowIn(t, "elsewhere"); got != "eng-9" {
		t.Errorf("active window in session elsewhere = %q, want eng-9 selected", got)
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

	startSession(t, "proj", "main", f.MainDir)
	openWindowOn(t, "proj", "eng-1", mine.Dir)
	startSession(t, "elsewhere", "eng-2", stray.Dir)

	r := f.exec("ls")
	if r.err != nil {
		t.Fatalf("ls: %v\n%s", r.err, r.both())
	}
	for _, want := range []string{"eng-1", "elsewhere:eng-2"} {
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
			if row.Window != "eng-1" || row.WindowSession != "proj" {
				t.Errorf("row = %+v, want the window named in session proj", row)
			}
		case "eng-2":
			if row.Window != "eng-2" || row.WindowSession != "elsewhere" {
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

// TestLsSaysWhichWindowIsYoursAndWhichEndsItsSession covers the two fields a
// consumer working out what to close cannot answer for itself.
//
// Both used to be homework handed to whoever read the listing: which id is your
// own meant a `tmux display-message` of their own to compare against, and
// whether a window is the last in its session had no answer short of counting.
// They are facts treewright already holds, and an agent asked to tear a worktree
// down is exactly the reader who gets them wrong.
func TestLsSaysWhichWindowIsYoursAndWhichEndsItsSession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	mine := f.Worktree("eng-1")
	alone := f.Worktree("eng-2")

	// proj holds two windows, so closing either leaves the session standing.
	startSession(t, "proj", "main", f.MainDir)
	openWindowOn(t, "proj", "eng-1", mine.Dir)
	// A session of one, which is the case worth warning about.
	startSession(t, "solo", "eng-2", alone.Dir)

	// Standing in the worktree's own window, as an agent running there is.
	insideSession(t, "proj")
	t.Setenv("TMUX_PANE", paneIn(t, "proj", "eng-1"))

	rows := windowRows(t, f)
	if got := rows["eng-1"]; !got.WindowIsCurrent {
		t.Errorf("row = %+v, want the window this command is running in marked", got)
	}
	if got := rows["eng-1"]; got.WindowLastInSession {
		t.Errorf("row = %+v, want a window with company not claimed to end its session", got)
	}
	// Every other row is somebody else's, including the base checkout's window.
	for slug, row := range rows {
		if slug != "eng-1" && row.WindowIsCurrent {
			t.Errorf("row %s = %+v, want only one window to be the caller's own", slug, row)
		}
	}
	if got := rows["eng-2"]; !got.WindowLastInSession {
		t.Errorf("row = %+v, want the lone window in session solo flagged", got)
	}
}

// TestLsClaimsNoWindowIsYoursOutsideTmux is the empty-string trap: outside tmux
// there is no current window, and every row with no window open carries an empty
// id — so a bare comparison would report every one of them as the caller's own.
func TestLsClaimsNoWindowIsYoursOutsideTmux(t *testing.T) {
	f := newFixture(t, "command = 'sleep 300'\n")
	f.Worktree("eng-1")

	for slug, row := range windowRows(t, f) {
		if row.WindowIsCurrent {
			t.Errorf("row %s = %+v, want no window claimed outside tmux", slug, row)
		}
		if row.WindowLastInSession {
			t.Errorf("row %s = %+v, want no session claimed for a window that is not open", slug, row)
		}
	}
}

// windowRow is the window half of the ls schema, keyed by slug — with the base
// checkout under "base", which has none of its own.
type windowRow struct {
	Slug                string `json:"slug"`
	Base                bool   `json:"base"`
	WindowID            string `json:"window_id"`
	WindowIsCurrent     bool   `json:"window_is_current"`
	WindowLastInSession bool   `json:"window_last_in_session"`
}

func windowRows(t *testing.T, f *fixture) map[string]windowRow {
	t.Helper()
	r := f.exec("ls", "--json")
	if r.err != nil {
		t.Fatalf("ls --json: %v\n%s", r.err, r.both())
	}
	var rows []windowRow
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, r.stdout)
	}
	byslug := make(map[string]windowRow, len(rows))
	for _, row := range rows {
		if row.Base {
			byslug[baseName] = row
			continue
		}
		byslug[row.Slug] = row
	}
	return byslug
}

// paneIn names a pane of a window, for a test that has to stand in one.
func paneIn(t *testing.T, session, window string) string {
	t.Helper()
	out, err := tmuxctl(t, "list-panes", "-t", windowIDNamed(t, session, window), "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("find a pane in %s: %v\n%s", window, err, out)
	}
	return strings.Split(out, "\n")[0]
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
		if row.Slug == "eng-1" && row.Window != "eng-1" {
			t.Errorf("window for eng-1 = %q, want the worktree's own window eng-1", row.Window)
		}
	}

	if r := f.exec("resume", "eng-1"); r.err != nil {
		t.Fatalf("resume: %v\n%s", r.err, r.both())
	}
	if got := activeWindowIn(t, "proj"); got != "eng-1" {
		t.Errorf("active window in session proj = %q, want eng-1 rather than the window visiting it", got)
	}
	// Two panes stand in the worktree, and resuming must not make a third.
	if got := panesOn(t, wt); got != 2 {
		t.Errorf("%d panes in the worktree, want 2 — a duplicate window was opened", got)
	}
}

// TestABlankCommandLeavesAShellInTheWindow is what `command = ""` buys, proved
// against a real server rather than asserted about the config.
//
// tmux is handed no command at all — newWindow omits a blank rather than
// passing an empty string, which would have tmux run nothing and close the
// window immediately — so the pane ends up running the server's own
// default-shell. That is the discriminator: with the old collapse the window
// ran the fixture's stubbed claude, which exits at once and takes the window
// with it.
func TestABlankCommandLeavesAShellInTheWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = ''\n")

	r := f.exec("new", "eng-1")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}

	id := windowIDNamed(t, "proj", "eng-1")
	shell, err := tmuxctl(t, "show-options", "-gv", "default-shell")
	if err != nil {
		t.Fatalf("ask tmux for its default shell: %v\n%s", shell, err)
	}
	want := filepath.Base(shell)
	got, err := tmuxctl(t, "display-message", "-p", "-t", id, "#{pane_current_command}")
	if err != nil {
		t.Fatalf("read the pane's command: %v\n%s", err, got)
	}
	if got != want {
		t.Errorf("pane runs %q, want tmux's default-shell %q — a blank command must leave a shell", got, want)
	}
	// And it is a window like any other: found by the worktree treewright
	// stamped on it, so resume switches to it rather than opening a second.
	if stamped := windowStamp(t, "eng-1", "@treewright_worktree"); stamped != f.DirFor("eng-1") {
		t.Errorf("window records worktree %q, want %q", stamped, f.DirFor("eng-1"))
	}
}

// TestAFailingCommandKeepsItsWindowOpen is the visibility a vanishing window
// takes away. tmux closes a window as soon as its command exits, so a `command`
// that cannot start — a typo, a tool not installed, a config the tool rejects —
// erases its own explanation at the speed it appeared.
func TestAFailingCommandKeepsItsWindowOpen(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'echo no such model >&2; exit 12'\n")

	r := f.exec("new", "boom")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}

	// The command has to have run and failed before there is anything to see,
	// and nothing waits for it — so wait for the wrapper's last line, then read
	// the whole pane it is holding open.
	id := windowIDNamed(t, "proj", "boom")
	waitForPane(t, id, "press Enter")
	pane, err := tmuxctl(t, "capture-pane", "-p", "-t", id)
	if err != nil {
		t.Fatalf("capture the held-open pane: %v\n%s", err, pane)
	}

	// What the user came for is the command's own output, which is why the window
	// is held rather than merely reported as having closed.
	if !strings.Contains(pane, "no such model") {
		t.Errorf("pane = %q, want the failing command's own output still on screen", pane)
	}
	if !strings.Contains(pane, `"echo no such model >&2; exit 12" exited 12`) {
		t.Errorf("pane = %q, want the command and its status named", pane)
	}
	if !strings.Contains(pane, "press Enter") {
		t.Errorf("pane = %q, want the way to close it", pane)
	}
	// And the window is a real window, found the way every other one is: a user who
	// fixes the config and resumes gets this window, not a second one beside it.
	if got := panesOn(t, f.DirFor("boom")); got != 1 {
		t.Errorf("%d panes in the worktree, want the held window to be the only one", got)
	}
}

// TestASuccessfulCommandClosesItsWindowAsBefore is the other half, and the reason
// the wrapper checks the status at all: holding every window open would turn
// finishing normally into a keypress.
func TestASuccessfulCommandClosesItsWindowAsBefore(t *testing.T) {
	// The wrapper's failure path cleans the agent state off its window, aiming
	// at $TMUX_PANE. This test runs the wrapper bare, outside any fixture, so a
	// developer running the suite from inside tmux would otherwise hand it their
	// own pane to clean.
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	for _, tc := range []struct {
		name    string
		command string
		want    string
	}{
		{"a command that succeeds", "true", ""},
		// 128 and up is a command killed by a signal, which is nearly always the
		// user's own Ctrl-C. Reporting a stop they asked for is not news.
		{"a command the user interrupted", "sh -c 'exit 130'", ""},
		{"a command that failed", "sh -c 'exit 3'", "press Enter"},
		// A command that exits rather than merely failing is the case a flat script
		// gets wrong: the exit ends the wrapper too, and the window closes.
		{"a command that exits by itself", "exit 3", "press Enter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Stdin is /dev/null, so the read that holds a window open returns at
			// once rather than hanging this test.
			out, err := exec.Command("sh", "-c", heldOpenOnFailure(tc.command)).CombinedOutput()
			if tc.want == "" && len(out) > 0 {
				t.Errorf("output = %q, want nothing said about a command that did not fail", out)
			}
			if tc.want != "" && !strings.Contains(string(out), tc.want) {
				t.Errorf("output = %q, want it to mention %q", out, tc.want)
			}
			// The wrapper exits with the command's own status either way, so
			// nothing downstream can tell it apart from running the command bare.
			var status int
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				status = exit.ExitCode()
			}
			if want := wantStatus(tc.command); status != want {
				t.Errorf("exit status = %d, want the command's own %d", status, want)
			}
		})
	}
}

// wantStatus is the status each command in the table above exits with, read off
// the command itself so the table says one thing once.
func wantStatus(command string) int {
	if _, after, found := strings.Cut(command, "exit "); found {
		n, _ := strconv.Atoi(strings.TrimSuffix(after, "'"))
		return n
	}
	return 0
}

// TestAHeldWindowNamesItsCommandWithoutRepeatingIt is the size of the wrapper,
// which used to be the size of the command twice over and then some.
//
// The report line above "press Enter" carried a second copy of the command, and
// that copy was shell-quoted again on top of the quoting fillPrompt had already
// applied to the prompt inside it — so a prompt full of ordinary English
// possessives spent sixteen bytes of tmux's command-length budget per
// apostrophe, on text nobody runs. What that bought was tmux refusing the window
// of the worktree `new` had just made.
func TestAHeldWindowNamesItsCommandWithoutRepeatingIt(t *testing.T) {
	// Built the way `new` builds it: the prompt is quoted into the command
	// before the wrapper ever sees it, which is the first of the two quotings.
	command := "claude " + shellQuote(strings.Repeat("it's a long brief. ", 500))

	wrapped := heldOpenOnFailure(command)
	// The copy that runs stays byte-exact — it is the pane's foreground process.
	if !strings.Contains(wrapped, command) {
		t.Error("the command tmux runs is no longer the command it was given")
	}
	if over := len(wrapped) - len(command); over > 1000 {
		t.Errorf("the wrapper adds %d bytes to a %d-byte command, want an overhead that does not grow with it",
			over, len(command))
	}

	// A command short enough to read is still named whole, since the point of
	// the line is telling the reader which command produced the output above it.
	const short = "echo no such model >&2; exit 12"
	if got := heldOpenOnFailure(short); !strings.Contains(got, shellQuote(short)) {
		t.Errorf("wrapper = %q, want the short command named in full", got)
	}
}

// ---- the fresh agent behind a resume that never got going -------------------

// runScript runs a window's script the way tmux does — one shell, stdin at
// /dev/null so the read that holds a window open returns at once — with a fake
// clock ahead of the real date on PATH. It returns everything the pane would
// have shown and the status the window exited with.
//
// $TMUX and $TMUX_PANE are taken away because the held-open path aims its tmux
// calls at the pane it is running in, and a developer running the suite from
// inside tmux would otherwise hand it their own.
func runScript(t *testing.T, run windowCommand, elapsed int) (output string, status int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", run.script())
	cmd.Env = append(os.Environ(),
		"PATH="+fakeClock(t, elapsed)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMUX=", "TMUX_PANE=")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status = exit.ExitCode()
	}
	return string(out), status
}

// fakeClock writes a `date` ahead of the real one on PATH and returns the
// directory holding it: the same second the first time it is asked, then elapsed
// seconds later. A negative elapsed makes it fail instead, which is a machine
// whose date cannot answer +%s.
func fakeClock(t *testing.T, elapsed int) string {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\nexit 127\n"
	if elapsed >= 0 {
		// The script asks twice, so the first call leaves a mark and the second
		// finds it. Nothing else in a window's script runs `date`.
		body = "#!/bin/sh\n" +
			`if [ -f "$0.asked" ]; then echo ` + strconv.Itoa(1000+elapsed) +
			`; else : > "$0.asked"; echo 1000; fi` + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "date"), []byte(body), 0o755); err != nil {
		t.Fatalf("write the fake date: %v", err)
	}
	return dir
}

// TestAResumeThatRanForAWhileIsHeldOpen is the direction the fallback must fail
// in, and the case the whole timing guard exists for.
//
// An agent that ran for an hour and then died leaves its stack trace on screen,
// and that is what the held-open window is for. Starting a fresh agent over it
// takes the alternate screen and erases the one thing the user needs — so a
// failure is a recovery only where the command never got going at all.
//
// The clock is faked rather than waited out: five real seconds per case is a
// slow suite bought with nothing, and the fake is aimed at the mechanism itself.
func TestAResumeThatRanForAWhileIsHeldOpen(t *testing.T) {
	failed := windowCommand{Command: "echo the agent died >&2; exit 3", Fresh: "echo FRESH-AGENT"}

	t.Run("a command that never got going", func(t *testing.T) {
		out, status := runScript(t, failed, neverGotGoing-1)
		if !strings.Contains(out, "FRESH-AGENT") {
			t.Errorf("output = %q, want the fresh command to have run", out)
		}
		if !strings.Contains(out, "starting a fresh agent") {
			t.Errorf("output = %q, want the fallback announced rather than applied silently", out)
		}
		// The fresh command succeeded, so the window closes on its status rather
		// than being held open over a failure that has been dealt with.
		if status != 0 || strings.Contains(out, heldOpenNotice) {
			t.Errorf("status = %d, output = %q, want the window closed on the fresh command", status, out)
		}
	})

	t.Run("a command that ran and then died", func(t *testing.T) {
		out, status := runScript(t, failed, neverGotGoing)
		if strings.Contains(out, "FRESH-AGENT") {
			t.Errorf("output = %q, want no fresh agent over output the user needs", out)
		}
		if !strings.Contains(out, "the agent died") || !strings.Contains(out, heldOpenNotice) {
			t.Errorf("output = %q, want the window held open on what failed", out)
		}
		if status != 3 {
			t.Errorf("status = %d, want the failing command's own 3", status)
		}
	})

	t.Run("no clock to read", func(t *testing.T) {
		// With no usable date there is no telling the two apart, and the answer
		// that cannot erase anything is the one to give: what happened before
		// any of this existed.
		out, _ := runScript(t, failed, -1)
		if strings.Contains(out, "FRESH-AGENT") {
			t.Errorf("output = %q, want no fallback where nothing timed the command", out)
		}
		if !strings.Contains(out, heldOpenNotice) {
			t.Errorf("output = %q, want the window held open as it was before", out)
		}
	})
}

// TestOnlyAFailureStartsAFreshAgent is the other half of the discrimination, and
// the half that has nothing to do with the clock: a command that finished, and
// one the user stopped, are both left exactly as they are.
//
// The clock says "brief" throughout, so the status is the only thing deciding.
// A resume the user quits with Ctrl-C matters most: restarting an agent somebody
// has just closed is worse than useless.
func TestOnlyAFailureStartsAFreshAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		status  int
	}{
		{"a resume that finished", "echo bye", 0},
		{"a resume the user interrupted", "sh -c 'exit 130'", 130},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, status := runScript(t, windowCommand{Command: tc.command, Fresh: "echo FRESH-AGENT"}, 0)
			if strings.Contains(out, "FRESH-AGENT") {
				t.Errorf("output = %q, want nothing started over a command nobody reported failing", out)
			}
			if strings.Contains(out, heldOpenNotice) {
				t.Errorf("output = %q, want the window closed as before", out)
			}
			if status != tc.status {
				t.Errorf("status = %d, want the command's own %d", status, tc.status)
			}
		})
	}
}

// TestTheFallbackRunsOnceAndOnlyOnce holds the rule that keeps a window out of a
// loop: the fresh command is the recovery, and a fresh command that also fails
// at once is held open rather than tried again.
func TestTheFallbackRunsOnceAndOnlyOnce(t *testing.T) {
	// Counted in a file rather than in the output, which names the fresh command
	// a second time in the line reporting what exited.
	//
	// The file is named relative to a directory this test stands in, rather than
	// by its absolute path, because the assertion below is about which command
	// the report names. macOS puts a test's temporary directory under
	// /var/folders/<two levels of gibberish>, which is enough on its own to push
	// the command past the eighty columns abbreviated cuts it to — so the report
	// named the right command and said so with a "…" on the end, and the test
	// failed over the wrapper working.
	dir := t.TempDir()
	t.Chdir(dir)
	out, status := runScript(t, windowCommand{
		Command: "echo nothing to continue >&2; exit 1",
		Fresh:   "echo ran >> runs; exit 4",
	}, 0)

	body, err := os.ReadFile(filepath.Join(dir, "runs"))
	if err != nil {
		t.Fatalf("the fresh command never ran: %v", err)
	}
	if got := strings.Count(string(body), "ran"); got != 1 {
		t.Errorf("the fresh command ran %d times, want exactly one recovery", got)
	}
	if !strings.Contains(out, heldOpenNotice) {
		t.Errorf("output = %q, want the second failure held open", out)
	}
	// And the window reports the command that actually failed, which by now is
	// not the one it was asked to run.
	if !strings.Contains(out, `"echo ran >> runs; exit 4" exited 4`) {
		t.Errorf("output = %q, want the fresh command named as what exited", out)
	}
	if status != 4 {
		t.Errorf("status = %d, want the fresh command's own 4", status)
	}
}

// TestAWindowScriptParsesInTheShellsTmuxMightRunIt is the check the shims get
// from TestScriptsParse, for the other script treewright emits.
//
// tmux does not run a window's command through /bin/sh: it uses the user's
// login shell, so a machine whose $SHELL is zsh gets `zsh -c '<script>'`. That
// makes "it works here" a statement about the developer's own shell, and a
// script that fails to parse takes the window with it — which is the one
// failure the wrapper exists to prevent. So it is written to POSIX and checked
// against the shells people actually log in with.
func TestAWindowScriptParsesInTheShellsTmuxMightRunIt(t *testing.T) {
	scripts := map[string]string{
		"one command": windowCommand{Command: "claude --continue"}.script(),
		"with a fresh command behind it": windowCommand{
			Command: "claude --continue 'it'\\''s a brief'",
			Fresh:   "claude 'it'\\''s a brief'",
		}.script(),
	}
	for _, shell := range []string{"sh", "bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				testenv.Unavailablef(t, "%s is not installed", shell)
			}
			for name, script := range scripts {
				path := filepath.Join(t.TempDir(), "window")
				if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
				if out, err := exec.Command(bin, "-n", path).CombinedOutput(); err != nil {
					t.Errorf("%s rejected the script for %s: %v\n%s", shell, name, err, out)
				}
			}
		})
	}
}

// TestABlankCommandIsLeftAlone keeps the wrapper out of the one case tmux handles
// by opening a plain shell: a script around nothing would run the shell, exit 0,
// and close the window that was meant to stay.
//
// A fallback behind a blank command is left alone too — a window holding a shell
// has nothing in it that can fail, so there is nothing to recover from.
func TestABlankCommandIsLeftAlone(t *testing.T) {
	for _, command := range []string{"", "   "} {
		for _, fresh := range []string{"", "claude"} {
			run := windowCommand{Command: command, Fresh: fresh}
			if got := run.script(); got != command {
				t.Errorf("windowCommand%+v.script() = %q, want it untouched", run, got)
			}
		}
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
