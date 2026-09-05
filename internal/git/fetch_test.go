// The tests here are in package git, unlike the rest of the package's tests
// next door: what they cover is the deadline on a git call and the rule that
// decides whether a failed fetch is asked a second time, and neither is
// reachable from outside. git_test.go is external because it imports gittest,
// which imports this package; nothing in this file does.
package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitThatRuns puts a shim named "git" first on PATH. Every test here needs a
// git that misbehaves in one particular way, which the real one cannot be asked
// for — a fetch that never answers has no repository that reliably produces it.
func gitThatRuns(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the git shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestADeadlineEndsAGitCallThatNeverAnswers covers the failure the budget
// exists for. A network that black-holes rather than refusing — a captive
// portal, a dead VPN, a stalled handshake — leaves git waiting with nothing to
// report, and `new` waiting behind it with no worktree, no window and nothing
// on screen to explain the silence.
//
// The second case is why cmd.WaitDelay is set, and it is the realistic one:
// git runs its transport in a child process, which inherits the pipes this
// package reads git's output through. Killing git leaves those pipes open, so a
// Wait that waited for them to close would sit there for exactly as long as the
// hang it was meant to end — the deadline would fire and change nothing.
func TestADeadlineEndsAGitCallThatNeverAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
	}{
		{"git itself hangs", "#!/bin/sh\nexec sleep 60\n"},
		{"a child of git holds the pipes open", "#!/bin/sh\nsleep 60\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gitThatRuns(t, tc.script)

			start := time.Now()
			_, err := runLimitedIn(t.TempDir(), nil, 100*time.Millisecond, "fetch", "--quiet", "origin", "main")
			elapsed := time.Since(start)

			var e *ExecError
			if !errors.As(err, &e) || !e.TimedOut {
				t.Fatalf("err = %v (%T), want an ExecError marked as timed out", err, err)
			}
			// Generous against the shim's own minute: what is being asserted is
			// that the call came back at all, not how promptly a loaded machine
			// gets round to it.
			if limit := 10 * time.Second; elapsed > limit {
				t.Errorf("came back after %s, want under %s — the deadline did not end it", elapsed, limit)
			}
			// A killed git says "signal: killed" if it says anything, which is
			// not a reason. The budget is.
			if said := Said(err); !strings.Contains(said, "timed out") {
				t.Errorf("the failure does not say it timed out: %q", said)
			}
		})
	}
}

// TestOnlyATimeoutIsWithheldFromARetry pins the rule FetchRetrying turns on.
// Everything except a timeout cost nothing to find out and can be asked again
// for the price of the backoff; a timeout has already spent the whole budget
// establishing that nothing is answering, and asking twice doubles the wait for
// the one case the offline fallback exists to reach quickly.
func TestOnlyATimeoutIsWithheldFromARetry(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{
			"a timeout has already spent the budget",
			&ExecError{Command: "git fetch", TimedOut: true, Err: errors.New("timed out after 30s")},
			false,
		},
		{
			"a refusal came back at once",
			&ExecError{Command: "git fetch", Stderr: "fatal: couldn't find remote ref nope", Err: errors.New("exit status 128")},
			true,
		},
		{
			"a failure from somewhere other than a git invocation",
			errors.New("something else entirely"),
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := worthRetrying(tc.err); got != tc.want {
				t.Errorf("worthRetrying(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestAFailureCarriesWhatGitSaid is what the `new` warning is built on: without
// git's stderr a failed fetch reads only as "exit status 128", which sends the
// reader nowhere.
func TestAFailureCarriesWhatGitSaid(t *testing.T) {
	gitThatRuns(t, "#!/bin/sh\necho \"fatal: 'origin' does not appear to be a git repository\" >&2\nexit 128\n")

	_, err := runLimitedIn(t.TempDir(), nil, 0, "fetch", "--quiet", "origin", "main")
	if err == nil {
		t.Fatal("want an error from a git that exited 128, got none")
	}
	if got, want := Said(err), "fatal: 'origin' does not appear to be a git repository"; got != want {
		t.Errorf("Said = %q, want %q", got, want)
	}
	// The whole error still names the command, which is what an `error: ...`
	// needs and what Said deliberately leaves out.
	if !strings.Contains(err.Error(), "git fetch --quiet origin main") {
		t.Errorf("the error does not name the command that failed: %v", err)
	}
}

// TestGitsBlankLinesDoNotBecomeALineOfSpaces covers the one thing Said changes
// about what git wrote. git separates its fatal from its advice with a blank
// line, and the caller puts the whole of it in a field — where asFields pads
// every later line to the value column, so a blank one arrives as a run of
// spaces under a label and reads as the message having stopped there.
func TestGitsBlankLinesDoNotBecomeALineOfSpaces(t *testing.T) {
	gitThatRuns(t, "#!/bin/sh\nprintf 'fatal: no\\n\\nPlease check.\\n' >&2\nexit 128\n")

	_, err := runLimitedIn(t.TempDir(), nil, 0, "fetch", "--quiet", "origin", "main")
	if err == nil {
		t.Fatal("want an error from a git that exited 128, got none")
	}
	// Both of git's sentences survive, in git's own voice — the capital and the
	// full stop included, since paraphrasing them would be the guessing this is
	// all meant to stop.
	if got, want := Said(err), "fatal: no\nPlease check."; got != want {
		t.Errorf("Said = %q, want %q", got, want)
	}
}
