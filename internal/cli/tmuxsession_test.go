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
	"time"

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
	if got, want := windowStamp(t, "ENG-142", "@treewright_worktree"), f.DirFor("eng-142-white-screen"); got != want {
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
		if got := windowStamp(t, "ENG-142", option); got != value {
			t.Errorf("window ENG-142 has %s = %q, want %q", option, got, value)
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
// pattern would read as key API-2 — keeps its whole name here.
func TestNewWorksFromADescriptionWithNoTicket(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\nticket_pattern = ''\n")

	for _, tc := range []struct{ slug, window string }{
		{"dark-mode-toggle", "DARK-MODE…"},
		{"payment-retries", "PAYMENT-RE…"},
		{"api-2-fix", "API-2-FIX"},
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

	if got := windowStamp(t, "FLAKY-PAYM…", "@treewright_slug"); got != "flaky-payment-test" {
		t.Errorf("window FLAKY-PAYM… has @treewright_slug = %q, want %q", got, "flaky-payment-test")
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

	if got, want := windowStamp(t, "MAIN", "@treewright_worktree"), f.MainDir; got != want {
		t.Errorf("base window records worktree %q, want the main checkout %q", got, want)
	}
	if got := windowStamp(t, "MAIN", "@treewright_repo"); got != "proj" {
		t.Errorf("base window records repo %q, want proj", got)
	}
	for _, option := range []string{"@treewright_slug", "@treewright_branch"} {
		if got := windowStamp(t, "MAIN", option); got != "" {
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
	t.Chdir(other.MainDir)
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

	// The command has to have run and failed before there is anything to see, and
	// nothing waits for it, so this is what "the window is still there" means.
	var pane string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pane, _ = tmuxctl(t, "capture-pane", "-p", "-t", windowIDNamed(t, "proj", "BOOM"))
		if strings.Contains(pane, "press Enter") {
			break
		}
		time.Sleep(20 * time.Millisecond)
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

// TestABlankCommandIsLeftAlone keeps the wrapper out of the one case tmux handles
// by opening a plain shell: a script around nothing would run the shell, exit 0,
// and close the window that was meant to stay.
func TestABlankCommandIsLeftAlone(t *testing.T) {
	for _, command := range []string{"", "   "} {
		if got := heldOpenOnFailure(command); got != command {
			t.Errorf("heldOpenOnFailure(%q) = %q, want it untouched", command, got)
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
