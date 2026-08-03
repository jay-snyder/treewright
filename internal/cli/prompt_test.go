package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jay-snyder/treewright/internal/tmux"
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

// TestNewWithAPromptAndNoTmuxDoesNotClaimAWindowWasOpen covers the pairing no
// test held: a --prompt on a machine without tmux. No window exists, open or
// otherwise, and the no-tmux branch already prints the filled command — prompt
// included — as the recovery; the old warning fired on top of it anyway,
// telling the user to paste the prompt to an agent in a window that was
// "already open" when neither existed.
func TestNewWithAPromptAndNoTmuxDoesNotClaimAWindowWasOpen(t *testing.T) {
	f := newFixture(t, "")
	hideTmux(t)

	r := f.exec("new", "eng-9", "--prompt", "fix the rounding")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "prompt not delivered") {
		t.Errorf("stderr warns about a window that was already open, with no tmux installed:\n%s", r.stderr)
	}
	// The recovery the no-tmux branch owns: the command to run by hand, with
	// the prompt already quoted into it.
	if !strings.Contains(flat(r.stderr), "run claude 'fix the rounding'") {
		t.Errorf("stderr = %q, want the filled command as the by-hand recovery", flat(r.stderr))
	}
}

// TestPromptAgainstABlankCommandNamesTheShell: with command = "" the window
// holds a shell, so there is no agent to hand a prompt to. The general refusal
// would tell the reader to write a {prompt} into the very key they emptied on
// purpose — advice that undoes the configuration to fix the invocation.
func TestPromptAgainstABlankCommandNamesTheShell(t *testing.T) {
	f := newFixture(t, "command = ''\n")

	r := f.exec("new", "eng-1", "--prompt", "fix the rounding")
	if r.err == nil {
		t.Fatal("want an error for a prompt with no agent to receive it")
	}
	msg := r.err.Error()
	if !strings.Contains(msg, "opens a shell") {
		t.Errorf("error = %q, want the blank command explained as the shell it opens", msg)
	}
	// Refused before anything exists, like every other prompt refusal.
	if f.Exists("eng-1") {
		t.Error("a worktree was created behind a refused prompt")
	}
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

	// And it refuses for command as well, which is the one it may end up
	// running: a fallback that reached the fresh agent with the prompt dropped
	// on the way would lose text the user typed once, and there is nowhere to
	// report that from once the window is somebody else's.
	f3 := newFixture(t, "command = 'sleep 300'\nresume_command = 'sleep 300 {prompt}'\n")
	f3.mustRun("new", "gamma")
	r = f3.exec("resume", "gamma", "--prompt", "carry on")
	if r.err == nil || !strings.Contains(r.err.Error(), `command has no {prompt}`) {
		t.Errorf("err = %v, want command named", r.err)
	}
	if r = f3.exec("resume", "--fresh", "gamma", "--prompt", "carry on"); r.err == nil {
		t.Errorf("err = %v, want --fresh refused by the very command it runs", r.err)
	}
}

// TestALongPromptIsRefusedForTheWindowResumeWillActuallyOpen is the length check
// meeting the one command that runs two.
//
// A resume window is handed resume_command and command in a single script, with
// the prompt in each of them, so a prompt that fits either alone can still be
// half again too long for what tmux is given. Measuring the setting rather than
// the script passed that invocation, and tmux refused it — as its own raw
// "command too long", over a window that never opened.
func TestALongPromptIsRefusedForTheWindowResumeWillActuallyOpen(t *testing.T) {
	f := newFixture(t, "command = 'claude {prompt}'\nresume_command = 'claude --continue {prompt}'\n")
	f.mustRun("new", "alpha")

	// Comfortably inside the limit on its own, and over it once the window
	// carries both — which is what makes this the pair's refusal and not either
	// command's.
	prompt := strings.Repeat("a", tmux.MaxCommandLength*2/3)
	if _, err := fillPrompt("claude --continue {prompt}", "resume_command", prompt); err != nil {
		t.Fatalf("the prompt no longer fits one command alone, so this proves nothing: %v", err)
	}

	r := f.exec("resume", "alpha", "--prompt", prompt)
	if r.err == nil {
		t.Fatal("want a refusal for a pair of commands tmux will not run")
	}
	if msg := flat(r.err.Error()); !strings.Contains(msg, "--prompt makes "+resumeCommandPair+" too long") {
		t.Errorf("error = %q, want both settings named as what is too long", msg)
	}
}

// TestALongPromptIsRefusedBeforeTheWorktreeExists is the fourth rule, and it
// exists because the alternative was silent in the only way that matters.
//
// tmux refuses a command past a length of its own, and `new` deliberately does
// not fail on a window it could not open — the worktree path is already on
// stdout for `cd "$(treewright new eng-1)"`. So an over-long prompt used to
// leave a branch, a worktree, no window and no agent, under tmux's own raw
// "command too long" with the whole script quoted into it, and `new` refusing
// the slug from then on.
func TestALongPromptIsRefusedBeforeTheWorktreeExists(t *testing.T) {
	f := newFixture(t, "command = 'claude {prompt}'\n")

	r := f.exec("new", "alpha", "--prompt", strings.Repeat("it's a long brief. ", 1200))
	if r.err == nil {
		t.Fatal("want an error for a prompt tmux will not run")
	}
	// treewright's own voice, with both numbers in it, rather than tmux's — and
	// a way out that does not assume a keyboard, since the caller of --prompt is
	// as often an agent as a person and there is no window to paste into anyway.
	msg := flat(r.err.Error())
	for _, want := range []string{
		"--prompt makes command too long",
		"limit " + strconv.Itoa(tmux.MaxCommandLength) + " bytes",
		"shorten the prompt",
		"put it in a file",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to say %q", msg, want)
		}
	}
	// An error rather than a warning about something already done, which is the
	// difference the check being here rather than at the window buys.
	if strings.Contains(r.stderr, "warning:") {
		t.Errorf("stderr = %q, want nothing reported after the fact", r.stderr)
	}

	// And nothing was made, which is the whole reason the check is here and not
	// at the window.
	if r.stdout != "" {
		t.Errorf("stdout = %q, want no path for a worktree that must not exist", r.stdout)
	}
	if f.Exists("alpha") {
		t.Error("the worktree was created despite the refusal")
	}
	if f.Git(f.MainDir, "branch", "--list", f.BranchFor("alpha")) != "" {
		t.Error("the branch was created despite the refusal")
	}
}

// TestALongPromptThatFitsStillOpensAWindow is the other side of that limit: it
// is treewright's, set below tmux's with room for the rest of the argument list,
// and a limit set too high fails here rather than in somebody's terminal.
func TestALongPromptThatFitsStillOpensAWindow(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "prompt")
	template := "printf %s {prompt} > " + marker
	f := newFixture(t, "command = \""+template+"\"\n")

	// Shrunk until treewright accepts it rather than sized to a round number, so
	// what reaches tmux is the longest command treewright says it will hand over.
	prompt := strings.Repeat("a", tmux.MaxCommandLength)
	for {
		if _, err := fillPrompt(template, "command", prompt); err == nil {
			break
		}
		prompt = prompt[:len(prompt)-1]
	}

	f.mustRun("new", "alpha", "--prompt", prompt)
	waitForContent(t, marker, prompt, "the window's command")
}

// ---- --prompt-file -----------------------------------------------------------

// TestAPromptFileSendsTheAgentToTheBriefRatherThanCarryingIt is the whole of
// what the flag does: the command line gets one line naming the file, and the
// brief itself never enters it.
//
// The path is checked to be absolute, which is not decoration — the command
// runs in the new worktree, so a relative path resolved there would name a file
// that is not in it.
func TestAPromptFileSendsTheAgentToTheBriefRatherThanCarryingIt(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "prompt")
	f := newFixture(t, "command = \"printf %s {prompt} > "+marker+"\"\n")

	// Written outside the worktree and reached by a relative path, which is how
	// a caller standing in the main checkout would type it.
	f.Write(f.Root, "brief.md", "the whole brief, several paragraphs of it\n")
	f.mustRun("new", "alpha", "--prompt-file", "../brief.md")

	want := fmt.Sprintf(promptPointer, filepath.Join(f.Root, "brief.md"))
	waitForContent(t, marker, want, "the window's command")

	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read the marker: %v", err)
	}
	if strings.Contains(string(body), "several paragraphs") {
		t.Errorf("prompt = %q, want the file named rather than quoted into the command", body)
	}
}

// TestAPromptFileIsCheckedBeforeAnythingIsCreated holds the flag to the same
// discipline as the rest of fillPrompt's refusals: a brief that is not there,
// is a directory, or is empty is this invocation being wrong, and finding out
// after the worktree exists would leave a half-made one behind an error about
// a flag.
func TestAPromptFileIsCheckedBeforeAnythingIsCreated(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write the empty brief: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"missing", filepath.Join(dir, "nowhere.md"), "no file at"},
		{"a directory", dir, "is a directory"},
		{"empty", empty, "is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "")

			r := f.exec("new", "alpha", "--prompt-file", tc.path)
			if r.err == nil {
				t.Fatalf("want an error for a brief that is %s", tc.name)
			}
			if !strings.Contains(r.err.Error(), tc.want) {
				t.Errorf("error = %q, want it to say %q", r.err, tc.want)
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want no path for a worktree that must not exist", r.stdout)
			}
			if f.Exists("alpha") {
				t.Error("the worktree was created despite the refusal")
			}
		})
	}
}

// TestPromptAndPromptFileFillOneSetting: two ways to say the same thing, so
// saying both is a wrong invocation rather than a precedence rule to learn.
func TestPromptAndPromptFileFillOneSetting(t *testing.T) {
	f := newFixture(t, "")
	f.Write(f.Root, "brief.md", "a brief\n")
	brief := filepath.Join(f.Root, "brief.md")

	r := f.exec("new", "alpha", "--prompt", "inline", "--prompt-file", brief)
	if !errors.Is(r.err, ErrUsage) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
	if !strings.Contains(r.stderr, "one setting") {
		t.Errorf("stderr = %q, want the two flags named as one setting", r.stderr)
	}
	if f.Exists("alpha") {
		t.Error("the worktree was created despite the refusal")
	}

	// resume refuses it identically: the flag is the same flag on both.
	f.mustRun("new", "beta")
	if r := f.exec("resume", "beta", "-p", "inline", "--prompt-file", brief); !errors.Is(r.err, ErrUsage) {
		t.Errorf("err = %v, want resume to refuse both as well", r.err)
	}
}

// TestAPromptFileCarriesABriefTooLongToPass is why the flag exists. The same
// text passed to --prompt is refused outright — tmux will not run a command
// that long — and as a file it is one short line whatever the size.
func TestAPromptFileCarriesABriefTooLongToPass(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "prompt")
	f := newFixture(t, "command = \"printf %s {prompt} > "+marker+"\"\n")

	brief := strings.Repeat("it's a long brief. ", 1200)
	f.Write(f.Root, "long.md", brief)
	path := filepath.Join(f.Root, "long.md")

	if r := f.exec("new", "alpha", "--prompt", brief); r.err == nil {
		t.Fatal("want the inline form refused, or this proves nothing")
	}

	f.mustRun("new", "alpha", "--prompt-file", path)
	waitForContent(t, marker, fmt.Sprintf(promptPointer, path), "the window's command")
}

// TestResumePromptDelivery covers all three halves of delivery: a resume that
// starts the agent hands it the prompt, one whose resume_command never got
// going hands it to the agent that starts in its place, and one that merely
// switches to an open window says the prompt went nowhere — the text was typed
// once, and dropping it silently is the quiet failure everything else refuses
// to be.
//
// Every fixture here gives command a {prompt} as well, because resume carries
// both commands into the window now: the prompt was aimed at the agent this
// resume starts, and either of the two may turn out to be it.
//
// The subtest names are curt because they end up in a tmux socket path, which
// a unix socket caps at little over a hundred characters.
func TestResumePromptDelivery(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "resumed")

	t.Run("fresh", func(t *testing.T) {
		f := newFixture(t, "command = 'true {prompt}'\nresume_command = \"printf %s {prompt} > "+marker+"\"\n")
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

	t.Run("fallen back", func(t *testing.T) {
		fell := filepath.Join(t.TempDir(), "fell-back")
		f := newFixture(t, "command = \"printf %s {prompt} > "+fell+"\"\n"+
			"resume_command = 'false {prompt}'\n")
		f.mustRun("new", "solo")
		waitForNoPanes(t, f.DirFor("solo"))

		f.mustRun("resume", "solo", "-p", "address the review comments")
		waitForContent(t, fell, "address the review comments", "the fresh command")
	})

	t.Run("open", func(t *testing.T) {
		f := newFixture(t, "command = 'sleep 300 {prompt}'\nresume_command = 'sleep 300 {prompt}'\n")
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
