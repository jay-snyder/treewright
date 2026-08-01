package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The kickoff prompt: --prompt's text lands at the command template's {prompt},
// and the three rules of docs/agents.md's account each get the test that
// would catch their absence — quoting, removal-not-emptying, and refusal.

// TestNewDeliversThePromptShellQuoted proves the prompt arrives as one literal
// argument, by making the window's command write it back out. The text carries
// two spaces and an apostrophe, the characters a quoting bug eats first.
func TestNewDeliversThePromptShellQuoted(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "prompt")
	f := newFixture(t, "command = \"printf %s {prompt} > "+marker+"\"\n")

	const prompt = "it's two  words"
	f.mustRun("new", "alpha", "--prompt", prompt)
	waitForContent(t, marker, prompt, "the window's command")
}

// TestThePlaceholderVanishesRatherThanEmptying pins the difference between
// removing {prompt} and substituting ”: the window's command counts its own
// arguments. An empty argument would be an instruction — a blank prompt for
// the agent to answer — which is exactly what a promptless `new` must not
// hand it.
func TestThePlaceholderVanishesRatherThanEmptying(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "argc")
	f := newFixture(t, "command = \"sh -c 'echo $# > "+marker+"' x {prompt}\"\n")

	f.mustRun("new", "alpha")
	waitForContent(t, marker, "0", "the window's argument count")
}

// TestPromptRefusedWithoutAPlaceholder is the third rule, plus its timing:
// the refusal comes before anything is created, so a flag mistake does not
// leave a half-made worktree behind.
func TestPromptRefusedWithoutAPlaceholder(t *testing.T) {
	f := newFixture(t, "command = 'sleep 300'\n")

	r := f.exec("new", "alpha", "--prompt", "fix the rounding")
	if r.err == nil {
		t.Fatal("want an error for a prompt the command cannot take")
	}
	if msg := r.err.Error(); !strings.Contains(msg, "{prompt}") || !strings.Contains(msg, "command") {
		t.Errorf("error = %q, want the placeholder and the setting named", msg)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want no path for a worktree that must not exist", r.stdout)
	}
	if _, err := os.Stat(f.DirFor("alpha")); err == nil {
		t.Error("the worktree was created despite the refusal")
	}

	// resume refuses the same way, naming its own setting.
	f2 := newFixture(t, "resume_command = 'sleep 300'\n")
	f2.mustRun("new", "beta")
	r = f2.exec("resume", "beta", "--prompt", "carry on")
	if r.err == nil || !strings.Contains(r.err.Error(), "resume_command") {
		t.Errorf("err = %v, want resume_command named", r.err)
	}
}

// TestResumePromptDelivery covers both halves of delivery: a resume that
// starts the agent hands it the prompt, and one that merely switches to an
// open window says the prompt went nowhere — the text was typed once, and
// dropping it silently is the quiet failure everything else refuses to be.
//
// The subtest names are curt because they end up in a tmux socket path, which
// a unix socket caps at little over a hundred characters.
func TestResumePromptDelivery(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "resumed")

	t.Run("fresh", func(t *testing.T) {
		f := newFixture(t, "command = 'true'\nresume_command = \"printf %s {prompt} > "+marker+"\"\n")
		f.mustRun("new", "solo")
		// The window `new` opened runs `true` and closes by itself; resume must
		// then create one, which is the case where the prompt is deliverable.
		waitForNoPanes(t, f.DirFor("solo"))

		out := f.mustRun("resume", "solo", "-p", "address the review comments")
		waitForContent(t, marker, "address the review comments", "resume_command")
		if strings.Contains(out, "not delivered") {
			t.Errorf("output = %q, want no warning about a prompt that was delivered", out)
		}
	})

	t.Run("open", func(t *testing.T) {
		f := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300 {prompt}'\n")
		f.mustRun("new", "solo")

		out := f.mustRun("resume", "solo", "--prompt", "you never see this")
		if !strings.Contains(out, "not delivered") {
			t.Errorf("output = %q, want the undelivered prompt warned about", out)
		}
	})
}

// waitForNoPanes polls until no pane is left sitting in dir, for tests that
// need the window a command opened to have closed by itself.
func waitForNoPanes(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if panesOn(t, dir) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("a pane is still sitting in %s", dir)
}
