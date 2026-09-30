package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/shellinit"
)

// The record of which conversation each agent standing in the base checkout is
// running, and the two things it is for: resuming the base window on its own
// conversation rather than the latest one in the directory, and bringing a
// scratch session back after a restart took its window.
//
// Every test here names `agent = "claude"`, since only a module with a
// resume-by-id form keeps records, and runs a stub claude that writes down how
// it was started. The log is the assertion that matters: what the window was
// actually handed, rather than what treewright said it would hand it.

// agentStub puts a claude first on PATH that writes the arguments it was started
// with to a log, one line per start, then stays up as an agent does — so the
// window it runs in is still there to be looked at.
//
// With resumeFails, a start that asks for a conversation by id fails at once
// instead, as claude does for one it cannot find, which is what a record naming
// a conversation that has since been cleaned up comes to.
//
// It has to be on PATH before the test's tmux server starts, since a window's
// environment is the server's, taken when it started.
func agentStub(t *testing.T, resumeFails bool) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "starts")
	script := "#!/bin/sh\nprintf 'start:%s\\n' \"$*\" >> " + shellinit.Quote(log) + "\n"
	if resumeFails {
		script += "if [ \"$1\" = --resume ]; then echo \"No conversation found with session ID: $2\" >&2; exit 1; fi\n"
	}
	script += "exec sleep 300\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the claude stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// sessionFixture is a repository whose config names the claude module, with the
// stub standing in for claude.
func sessionFixture(t *testing.T, extra string, resumeFails bool) (f *fixture, log string) {
	t.Helper()
	requireTmux(t)
	f = newFixture(t, "agent = 'claude'\n"+extra)
	return f, agentStub(t, resumeFails)
}

// fromPane runs fn the way an agent's hook runs, inside tmux in the pane of the
// window named, and puts things back afterwards — a command that went on
// believing it was in that pane would try to move a client that is not there.
func fromPane(t *testing.T, window string, fn func()) {
	t.Helper()
	t.Setenv("TMUX", "/dev/null,0,0")
	t.Setenv("TMUX_PANE", paneIn(t, "proj", window))
	defer func() {
		t.Setenv("TMUX", "")
		t.Setenv("TMUX_PANE", "")
	}()
	fn()
}

// hookPayload is what an agent hands its hook on stdin, as far as these tests
// need it.
func hookPayload(t *testing.T, fields map[string]string) string {
	t.Helper()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode the payload: %v", err)
	}
	return string(body)
}

// sessionStarts runs what the agent's SessionStart hook runs, from the window
// named, handing it the conversation the agent has just begun.
func sessionStarts(t *testing.T, f *fixture, window, id string) result {
	t.Helper()
	var r result
	fromPane(t, window, func() {
		r = f.execWithStdin(hookPayload(t, map[string]string{
			"session_id": id, "source": "startup", "hook_event_name": "SessionStart",
		}), "session-start")
	})
	return r
}

// sessionEnds runs what the agent's SessionEnd hook runs, from the window named,
// with the reason the agent gives for the ending.
func sessionEnds(t *testing.T, f *fixture, window, id, reason string) {
	t.Helper()
	fromPane(t, window, func() {
		r := f.execWithStdin(hookPayload(t, map[string]string{
			"session_id": id, "reason": reason, "hook_event_name": "SessionEnd",
		}), "signal", "clear")
		if r.err != nil {
			t.Fatalf("signal clear: %v\n%s", r.err, r.both())
		}
	})
}

// recordOf reads what is recorded for the window called name, "" for nothing.
func recordOf(t *testing.T, f *fixture, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.MainDir, ".git", "treewright", "sessions", name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// writeRecord leaves a record the way an earlier session's hook would have, for
// a test whose subject is what reads it.
func writeRecord(t *testing.T, f *fixture, name, id string) {
	t.Helper()
	dir := filepath.Join(f.MainDir, ".git", "treewright", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// startsOf reads the stub's log: how each claude it stood in for was started.
func startsOf(t *testing.T, log string) []string {
	t.Helper()
	body, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

// ---- recording ---------------------------------------------------------------

// TestSessionStartRecordsTheBaseWindowsConversation is the half a record kept for
// scratch windows alone would have missed. Without it the base window resumes
// with --continue, which takes the most recent conversation in the directory —
// and after a scratch session, that is the scratch session's.
func TestSessionStartRecordsTheBaseWindowsConversation(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("base")

	r := sessionStarts(t, f, "main", "base-conversation")
	if r.err != nil {
		t.Fatalf("session-start: %v\n%s", r.err, r.both())
	}
	if got := recordOf(t, f, "base"); got != "base-conversation" {
		t.Errorf("base record = %q, want the conversation the base window's agent began", got)
	}
	// Silent, as everything session-start does that has nothing to tell the agent
	// is: its stdout is added to the session as context.
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("output = %q / %q, want silence — recording is bookkeeping, not news", r.stdout, r.stderr)
	}
}

// TestSessionStartRecordsAScratchWindowUnderItsName: the name is the handle
// everything else reaches a scratch window by, so it is what the record is kept
// under, and the base window's record is not touched by it.
func TestSessionStartRecordsAScratchWindowUnderItsName(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("base")
	f.mustRun("scratch", "ask")
	sessionStarts(t, f, "main", "base-conversation")

	sessionStarts(t, f, "ask", "ask-conversation")

	if got := recordOf(t, f, "ask"); got != "ask-conversation" {
		t.Errorf("ask record = %q, want the scratch agent's conversation", got)
	}
	if got := recordOf(t, f, "base"); got != "base-conversation" {
		t.Errorf("base record = %q, want it left alone by a session in another window", got)
	}
}

// TestAClearedSessionRewritesTheRecord: /clear starts a conversation under a new
// id in the same window, and fires SessionStart again — so the record follows
// what the window is running instead of pinning the one it started with.
func TestAClearedSessionRewritesTheRecord(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("scratch", "ask")
	sessionStarts(t, f, "ask", "before-clear")

	sessionStarts(t, f, "ask", "after-clear")

	if got := recordOf(t, f, "ask"); got != "after-clear" {
		t.Errorf("ask record = %q, want the conversation after the clear", got)
	}
}

// TestSessionStartRecordsNoOtherWindow covers the silences, which
// from outside look exactly like a record that was written. The hook fires in
// every session the agent has, and a record written for the wrong window is a
// resume that reopens somebody else's conversation.
func TestSessionStartRecordsNoOtherWindow(t *testing.T) {
	records := func(t *testing.T, f *fixture) []string {
		t.Helper()
		entries, _ := os.ReadDir(filepath.Join(f.MainDir, ".git", "treewright", "sessions"))
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}

	t.Run("worktree window", func(t *testing.T) {
		// --continue is already exact there: one agent in the directory.
		f, _ := sessionFixture(t, "", false)
		f.mustRun("new", "eng-1")
		t.Chdir(f.DirFor("eng-1"))
		sessionStarts(t, f, "eng-1", "worktree-conversation")
		if got := records(t, f); len(got) != 0 {
			t.Errorf("records = %v, want none for a worktree's window", got)
		}
	})

	t.Run("hand-made window", func(t *testing.T) {
		f, _ := sessionFixture(t, "", false)
		startSession(t, "proj", "hand-made", f.MainDir)
		sessionStarts(t, f, "hand-made", "somebody-elses")
		if got := records(t, f); len(got) != 0 {
			t.Errorf("records = %v, want none for a window treewright did not open", got)
		}
	})

	t.Run("outside tmux", func(t *testing.T) {
		f, _ := sessionFixture(t, "", false)
		r := f.execWithStdin(hookPayload(t, map[string]string{"session_id": "no-window"}), "session-start")
		if r.err != nil || r.both() != "" {
			t.Errorf("session-start outside tmux = %v / %q, want silence", r.err, r.both())
		}
		if got := records(t, f); len(got) != 0 {
			t.Errorf("records = %v, want none with no window to reopen", got)
		}
	})

	t.Run("agent in a worktree", func(t *testing.T) {
		// A window holding a shell can have walked anywhere, and the agent it
		// starts there is having a conversation in that directory.
		f, _ := sessionFixture(t, "", false)
		f.mustRun("base")
		f.mustRun("new", "eng-1")
		t.Chdir(f.DirFor("eng-1"))
		sessionStarts(t, f, "main", "worktree-conversation")
		if got := records(t, f); len(got) != 0 {
			t.Errorf("records = %v, want none for an agent standing in a worktree", got)
		}
	})

	t.Run("own resume_command", func(t *testing.T) {
		f, _ := sessionFixture(t, "resume_command = 'claude --continue --model other {prompt}'\n", false)
		f.mustRun("base")
		sessionStarts(t, f, "main", "base-conversation")
		if got := records(t, f); len(got) != 0 {
			t.Errorf("records = %v, want none where nothing resumes by id", got)
		}
	})

	t.Run("not an id", func(t *testing.T) {
		f, _ := sessionFixture(t, "", false)
		f.mustRun("base")
		for _, id := range []string{"", "--dangerously-skip-permissions", "a b", "x'; touch pwned; '", "../../escape"} {
			sessionStarts(t, f, "main", id)
		}
		if got := records(t, f); len(got) != 0 {
			t.Errorf("records = %v, want none for an id that is not one", got)
		}
	})

	t.Run("no payload", func(t *testing.T) {
		f, _ := sessionFixture(t, "", false)
		f.mustRun("base")
		fromPane(t, "main", func() {
			if r := f.exec("session-start"); r.err != nil || r.both() != "" {
				t.Errorf("session-start with nothing on stdin = %v / %q, want silence", r.err, r.both())
			}
		})
		if got := records(t, f); len(got) != 0 {
			t.Errorf("records = %v, want none with no payload to read", got)
		}
	})
}

// ---- resuming --------------------------------------------------------------------

// TestResumeOfTheBaseWindowReopensItsRecordedConversation is the hole #37
// documented and left: `resume base` handed --continue, and --continue picking
// the scratch window's conversation because it ran last.
func TestResumeOfTheBaseWindowReopensItsRecordedConversation(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	writeRecord(t, f, "base", "base-conversation")
	writeRecord(t, f, "ask", "ask-conversation")

	f.mustRun("resume", "base")

	waitForContent(t, log, "start:--resume base-conversation", "the base window's agent")
	if starts := startsOf(t, log); slices.ContainsFunc(starts, func(s string) bool { return strings.Contains(s, "--continue") }) {
		t.Errorf("starts = %v, want the base window resumed by id and never by directory", starts)
	}
}

// TestResumeReopensARecordedScratchSessionWhoseWindowIsGone: a restart takes the
// window and leaves the record, and the name is still the way back in.
func TestResumeReopensARecordedScratchSessionWhoseWindowIsGone(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	writeRecord(t, f, "ask", "ask-conversation")

	r := f.exec("resume", "ask")
	if r.err != nil {
		t.Fatalf("resume: %v\n%s", r.err, r.both())
	}
	waitForContent(t, log, "start:--resume ask-conversation", "the scratch window's agent")
	if got := windowStamp(t, "ask", "@treewright_scratch"); got != "ask" {
		t.Errorf("@treewright_scratch = %q, want the window reopened as the scratch window it was", got)
	}
	// And it is a scratch window again in every sense that matters: the base
	// window is not it, so base opens a window of its own beside it.
	f.mustRun("base")
	if got := windowsIn(t, "proj"); !slices.Equal(got, []string{"ask", "main"}) {
		t.Errorf("windows = %v, want the reopened scratch window and a base window of its own", got)
	}
}

// TestAWorktreeStillResumesByDirectory: one agent per directory keeps
// --continue exact there, and a record for the base checkout changes nothing.
func TestAWorktreeStillResumesByDirectory(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	f.Worktree("eng-1")
	writeRecord(t, f, "base", "base-conversation")

	f.mustRun("resume", "eng-1")

	waitForContent(t, log, "start:--continue", "the worktree's agent")
}

// TestARecordNamingAVanishedConversationFallsBackToCommand: the agent's own
// history can be cleared out from under a record, and a session that never
// said anything was never saved at all. Either way the agent finds nothing and
// exits at once, and the window gets a fresh agent rather than the error.
func TestARecordNamingAVanishedConversationFallsBackToCommand(t *testing.T) {
	f, log := sessionFixture(t, "", true)
	writeRecord(t, f, "ask", "cleaned-up")

	f.mustRun("resume", "ask")

	waitForContent(t, log, "start:\n", "the fresh agent behind a resume that found nothing")
	if starts := startsOf(t, log); !slices.Equal(starts, []string{"start:--resume cleaned-up", "start:"}) {
		t.Errorf("starts = %q, want the resume by id and then command", starts)
	}
}

// TestResumeFreshIgnoresTheRecord: --fresh is a new session, whatever there is
// to continue, and the new session's own SessionStart is what records it.
func TestResumeFreshIgnoresTheRecord(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	writeRecord(t, f, "base", "base-conversation")

	f.mustRun("resume", "--fresh", "base")

	waitForContent(t, log, "start:", "the fresh agent")
	if starts := startsOf(t, log); !slices.Equal(starts, []string{"start:"}) {
		t.Errorf("starts = %q, want command alone under --fresh", starts)
	}
}

// TestAResumeCommandOfTheConfigsOwnKeepsItsFlags: the id form is the module's
// spelling of the module's resume, and a config that wrote its own has flags in
// it the module knows nothing about. Resuming the right conversation with them
// dropped would be running a different agent from the one the file asks for, so
// that config keeps its own command — and keeps no records to tempt anything.
func TestAResumeCommandOfTheConfigsOwnKeepsItsFlags(t *testing.T) {
	f, log := sessionFixture(t, "resume_command = 'claude --continue --model other {prompt}'\n", false)
	writeRecord(t, f, "base", "base-conversation")
	writeRecord(t, f, "ask", "ask-conversation")

	f.mustRun("resume", "base")

	waitForContent(t, log, "start:--continue --model other", "the base window's agent")
	if out := f.mustRun("ls"); strings.Contains(out, "ask") {
		t.Errorf("ls = %q, want no recorded scratch session where records are not kept", out)
	}
}

// ---- restoring ---------------------------------------------------------------------

// TestRestoreBringsBackRecordedScratchSessions is the morning after a restart:
// the base window on its own conversation, every recorded scratch session on
// its, and the worktrees on theirs — in the order ls lists them.
func TestRestoreBringsBackRecordedScratchSessions(t *testing.T) {
	f, log := sessionFixture(t, "", false)
	f.mustRun("new", "eng-1")
	writeRecord(t, f, "base", "base-conversation")
	writeRecord(t, f, "review", "review-conversation")
	writeRecord(t, f, "ask", "ask-conversation")
	killSession(t)

	r := f.exec("restore", "-d")
	if r.err != nil {
		t.Fatalf("restore: %v\n%s", r.err, r.both())
	}
	if got, want := windowsIn(t, "proj"), []string{"main", "ask", "review", "eng-1"}; !slices.Equal(got, want) {
		t.Errorf("windows = %v, want %v — the base window, its scratch sessions by name, then the worktrees", got, want)
	}
	for _, want := range []string{
		"start:--resume base-conversation",
		"start:--resume ask-conversation",
		"start:--resume review-conversation",
		"start:--continue",
	} {
		waitForContent(t, log, want, "a restored window's agent")
	}
	if got := activeWindowIn(t, "proj"); got != "main" {
		t.Errorf("session proj opens on %q, want the base window", got)
	}
	if !strings.Contains(flat(r.stderr), "4 windows open in tmux session proj") {
		t.Errorf("stderr = %q, want every window counted", r.stderr)
	}
}

// TestRestoreLeavesAnOpenScratchWindowAlone: as for a worktree's window, one
// that is open is the session, and restore means "open whatever is missing".
func TestRestoreLeavesAnOpenScratchWindowAlone(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("base")
	f.mustRun("scratch", "ask")
	sessionStarts(t, f, "ask", "ask-conversation")
	writeRecord(t, f, "review", "review-conversation")
	ask := windowIDNamed(t, "proj", "ask")

	f.mustRun("restore", "-d")

	if got, want := windowsIn(t, "proj"), []string{"main", "ask", "review"}; !slices.Equal(got, want) {
		t.Errorf("windows = %v, want %v — the missing scratch window added, nothing duplicated", got, want)
	}
	if got := windowIDNamed(t, "proj", "ask"); got != ask {
		t.Errorf("window ask is now %s, was %s — an open window must be left exactly as it is", got, ask)
	}
}

// ---- listing -----------------------------------------------------------------------

// TestLsListsARecordedScratchSessionWithNoWindow is restore's preview holding:
// what restore opens is what ls lists, so a session it will reopen is a row, as
// a worktree whose window is closed is a row.
func TestLsListsARecordedScratchSessionWithNoWindow(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	writeRecord(t, f, "ask", "ask-conversation")

	var askRow []string
	for line := range strings.SplitSeq(f.mustRun("ls"), "\n") {
		if fields := strings.Fields(strings.TrimPrefix(line, "*")); len(fields) > 0 && fields[0] == "ask" {
			askRow = fields
		}
	}
	if !slices.Equal(askRow, []string{"ask", "scratch", "-", "-"}) {
		t.Errorf("ask's row = %q, want the scratch session with no window", askRow)
	}

	var rows []worktreeJSON
	r := f.exec("ls", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, r.stdout)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want the base checkout and the recorded session: %s", len(rows), r.stdout)
	}
	if ask := rows[1]; !ask.Scratch || ask.Slug != "ask" || ask.Window != "" || ask.WindowID != "" {
		t.Errorf("row 1 = %+v, want the ask scratch session, flagged, with no window", ask)
	}
}

// TestCompletionOffersRecordedScratchSessions: the name is the only way to type
// one, and resume is the way back in.
func TestCompletionOffersRecordedScratchSessions(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	writeRecord(t, f, "ask", "ask-conversation")

	if got := f.mustRun("__complete", "targets"); got != "base\nask\n" {
		t.Errorf("targets = %q, want the recorded session offered under base", got)
	}
}

// ---- ending ------------------------------------------------------------------------

// TestQuittingAScratchAgentEndsItsSession: quitting the agent is how a scratch
// session ends, and restore must not bring back one somebody quit.
func TestQuittingAScratchAgentEndsItsSession(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("scratch", "ask")
	sessionStarts(t, f, "ask", "ask-conversation")

	sessionEnds(t, f, "ask", "ask-conversation", "prompt_input_exit")

	if got := recordOf(t, f, "ask"); got != "" {
		t.Errorf("ask record = %q, want it gone once its agent was quit", got)
	}
}

// TestOnlyAQuitEndsAScratchSession is the finding the whole rule rests on:
// claude ends a session with reason "other" when its process is killed — which
// is what a reboot, a dying tmux server and `close` all do to it — so removing
// the record on any ending would erase every scratch session at exactly the
// moment it exists for. /clear ends one too, and starts the next in the same
// window.
func TestOnlyAQuitEndsAScratchSession(t *testing.T) {
	for _, reason := range []string{"other", "clear", "resume", "logout", ""} {
		t.Run("reason "+reason, func(t *testing.T) {
			f, _ := sessionFixture(t, "", false)
			f.mustRun("scratch", "ask")
			sessionStarts(t, f, "ask", "ask-conversation")

			sessionEnds(t, f, "ask", "ask-conversation", reason)

			if got := recordOf(t, f, "ask"); got != "ask-conversation" {
				t.Errorf("ask record = %q, want it kept through an ending that was not a quit", got)
			}
		})
	}
}

// TestAQuitEndsOnlyTheSessionItNames: the window's record is rewritten whenever
// it starts another conversation, so one naming something else belongs to a
// later session than the one ending, however the two hooks happen to be
// ordered.
func TestAQuitEndsOnlyTheSessionItNames(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("scratch", "ask")
	sessionStarts(t, f, "ask", "the-later-one")

	sessionEnds(t, f, "ask", "an-earlier-one", "prompt_input_exit")

	if got := recordOf(t, f, "ask"); got != "the-later-one" {
		t.Errorf("ask record = %q, want the later session's record kept", got)
	}
}

// TestQuittingTheBaseAgentKeepsItsConversation: quitting the base agent is not
// forgetting it. The next `resume base` should carry that conversation on, as
// --continue carries on a worktree whose agent was quit.
func TestQuittingTheBaseAgentKeepsItsConversation(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("base")
	sessionStarts(t, f, "main", "base-conversation")

	sessionEnds(t, f, "main", "base-conversation", "prompt_input_exit")

	if got := recordOf(t, f, "base"); got != "base-conversation" {
		t.Errorf("base record = %q, want it kept after the base agent was quit", got)
	}
}

// TestCloseEndsAScratchSession: close is how a person says a scratch session is
// done, and it has to say so itself — killing the window kills the agent, which
// reports that as the one ending that leaves a record alone.
func TestCloseEndsAScratchSession(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("scratch", "ask")
	sessionStarts(t, f, "ask", "ask-conversation")

	r := f.exec("close", "ask")
	if r.err != nil {
		t.Fatalf("close: %v\n%s", r.err, r.both())
	}
	if got := recordOf(t, f, "ask"); got != "" {
		t.Errorf("ask record = %q, want it gone with the window", got)
	}
	if !strings.Contains(flat(r.stderr), "restore will not reopen it") {
		t.Errorf("stderr = %q, want it said that the session is over, not only the window", r.stderr)
	}
}

// TestCloseEndsARecordedSessionWhoseWindowIsGone: after a restart, a scratch
// session nobody wants back is ended the same way, without reopening it first.
func TestCloseEndsARecordedSessionWhoseWindowIsGone(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	writeRecord(t, f, "ask", "ask-conversation")

	r := f.exec("close", "ask")
	if r.err != nil {
		t.Fatalf("close: %v\n%s", r.err, r.both())
	}
	if got := recordOf(t, f, "ask"); got != "" {
		t.Errorf("ask record = %q, want it gone", got)
	}
	if !strings.Contains(r.stderr, "ended scratch session ask") {
		t.Errorf("stderr = %q, want the session named as ended", r.stderr)
	}
	if r := f.exec("resume", "ask"); r.err == nil {
		t.Errorf("resume found a session close had ended\n%s", r.both())
	}
}

// TestClosingTheBaseWindowKeepsItsRecord: closing the window is no reason for the
// next `resume base` to lose its conversation.
func TestClosingTheBaseWindowKeepsItsRecord(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	f.mustRun("base")
	sessionStarts(t, f, "main", "base-conversation")

	f.mustRun("close", "base")

	if got := recordOf(t, f, "base"); got != "base-conversation" {
		t.Errorf("base record = %q, want it kept", got)
	}
}

// ---- names -------------------------------------------------------------------------

// TestARecordedSessionsNameIsTaken: it is still that session's name — resume
// reopens it by that name, and restore will — so neither a second scratch
// window nor a worktree may be given it.
func TestARecordedSessionsNameIsTaken(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	writeRecord(t, f, "ask", "ask-conversation")

	r := f.exec("scratch", "ask")
	if r.err == nil {
		t.Fatalf("scratch took the name of a recorded session\n%s", r.both())
	}
	for _, want := range []string{"recorded", "treewright resume --repo proj ask", "treewright close --repo proj ask"} {
		if !strings.Contains(flat(r.err.Error()), want) {
			t.Errorf("error = %q, want it to say %q", r.err, want)
		}
	}

	if r := f.exec("new", "ask"); r.err == nil || !strings.Contains(r.err.Error(), "a scratch session recorded") {
		t.Errorf("new ask = %v, want the recorded session named", r.err)
	}
	if f.Exists("ask") {
		t.Error("a refused new created the worktree")
	}
}

// TestSendToARecordedSessionNamesTheWayBackIn: there is no agent to type at,
// and resume is what starts one on the conversation.
func TestSendToARecordedSessionNamesTheWayBackIn(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	writeRecord(t, f, "ask", "ask-conversation")

	r := f.exec("send", "ask", "hello")
	if r.err == nil {
		t.Fatalf("send reached a session with no window\n%s", r.both())
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "no window is open on ask") ||
		!strings.Contains(msg, "treewright resume --repo proj ask") {
		t.Errorf("error = %q, want the missing window named and resume offered", msg)
	}
}

// ---- the record itself -------------------------------------------------------------

// TestARecordThatIsNotOneIsNoRecord: what is read back goes into a shell command
// line, and the directory is one anybody can put a file in. A record that fails
// the check is ignored — the behavior from before records existed — rather than
// trusted because the quoting would probably have held.
func TestARecordThatIsNotOneIsNoRecord(t *testing.T) {
	f, _ := sessionFixture(t, "", false)
	writeRecord(t, f, "base", "x'; touch pwned; '")
	writeRecord(t, f, "ask", "--dangerously-skip-permissions")
	writeRecord(t, f, "review", "")
	writeRecord(t, f, ".writing-123", "left-by-a-crash")

	cfg, err := resolveConfig("proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"base", "ask", "review", ".writing-123"} {
		if id, ok := recordedSession(cfg, name); ok {
			t.Errorf("record %s = %q, want it refused", name, id)
		}
	}
	if got := recordedScratch(cfg); len(got) != 0 {
		t.Errorf("recorded scratch sessions = %v, want none", got)
	}
}

// TestSessionIDs holds the floor a recorded id is checked against.
func TestSessionIDs(t *testing.T) {
	for id, want := range map[string]bool{
		"224ea95e-a5a1-4986-b2aa-4410cb6c0bfe": true, // what claude hands its hooks
		"plain_word":                           true,
		"":                                     false,
		"-leading-hyphen":                      false, // the agent would read a flag
		"has space":                            false,
		"quote'd":                              false,
		"dot.dot":                              false,
		"brace{prompt}":                        false, // could be taken for the placeholder
		strings.Repeat("a", maxSessionID):      true,
		strings.Repeat("a", maxSessionID+1):    false,
	} {
		if got := validSessionID(id); got != want {
			t.Errorf("validSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}
