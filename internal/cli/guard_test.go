package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/agentinit"
)

// guardFixture is a repository with two worktrees in it, which is the smallest
// arrangement the guard has anything to say about: one for the caller to stand
// in and one to reach into.
//
// The worktrees are made with gittest rather than with `new`, since nothing
// here is about windows — but tmux still has to be installed, the guard being
// out of scope on a machine with no windows to hand work to at all.
func guardFixture(t *testing.T) *fixture {
	t.Helper()
	requireTmux(t)
	f := newFixture(t, "")
	f.Worktree("alpha")
	f.Worktree("beta")
	return f
}

// guard runs the guard on a tool call, the way a PreToolUse hook does.
func (f *fixture) guard(cwd, tool string, input map[string]any) result {
	f.t.Helper()
	payload, err := json.Marshal(map[string]any{
		"session_id":      "test",
		"hook_event_name": "PreToolUse",
		"cwd":             cwd,
		"tool_name":       tool,
		"tool_input":      input,
	})
	if err != nil {
		f.t.Fatalf("marshal payload: %v", err)
	}
	var out, errOut bytes.Buffer
	runErr := Run(Env{
		Args: []string{"guard"}, Version: "test",
		Stdout: &out, Stderr: &errOut, Stdin: bytes.NewReader(payload),
	})
	return result{stdout: out.String(), stderr: errOut.String(), err: runErr}
}

// bash runs the guard on a Bash tool call made from the main checkout.
func (f *fixture) bash(command string) result {
	f.t.Helper()
	return f.guard(f.MainDir, "Bash", map[string]any{"command": command})
}

// refused asserts the call was blocked, and returns the refusal.
func (r result) refused(t *testing.T, what string) string {
	t.Helper()
	if !errors.Is(r.err, ErrRefused) {
		t.Fatalf("%s: err = %v, want ErrRefused", what, r.err)
	}
	if r.stdout != "" {
		t.Errorf("%s: stdout = %q, want nothing — the verdict is the exit code", what, r.stdout)
	}
	return r.stderr
}

// allowed asserts the call went through in silence, which is every case the
// guard is not for.
func (r result) allowed(t *testing.T, what string) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("%s: err = %v, want the call allowed\n%s", what, r.err, r.stderr)
	}
	if r.stdout != "" || r.stderr != "" {
		t.Errorf("%s: printed %q / %q, want silence", what, r.stdout, r.stderr)
	}
}

func TestTheGuardRefusesAnEditInAnotherWorktree(t *testing.T) {
	f := guardFixture(t)

	for _, tc := range []struct {
		tool  string
		input map[string]any
	}{
		{"Edit", map[string]any{"file_path": filepath.Join(f.DirFor("alpha"), "main.go")}},
		{"Write", map[string]any{"file_path": filepath.Join(f.DirFor("alpha"), "new.go")}},
		{"NotebookEdit", map[string]any{"notebook_path": filepath.Join(f.DirFor("alpha"), "a.ipynb")}},
	} {
		f.guard(f.MainDir, tc.tool, tc.input).refused(t, tc.tool)
	}

	// A relative path is the same write spelled from where the agent stands.
	rel := f.guard(f.MainDir, "Write", map[string]any{
		"file_path": filepath.Join("..", filepath.Base(f.DirFor("alpha")), "x.go"),
	})
	rel.refused(t, "a relative path into another worktree")
}

// TestTheGuardRefusesACommandThatChangesAnotherWorktree covers the spellings
// that got past the prose: the failure it was written for used none of the
// editing tools, only `git -C` and a `cd` into the worktree.
func TestTheGuardRefusesACommandThatChangesAnotherWorktree(t *testing.T) {
	f := guardFixture(t)
	wt := f.DirFor("alpha")

	for _, command := range []string{
		"git -C " + wt + " commit -m 'fix the null user'",
		"git -C " + wt + " add -A",
		"git -C " + wt + " push -u origin HEAD",
		"git -C " + wt + " checkout -b other",
		"git -C" + wt + " commit --amend",
		"cd " + wt + " && gh pr create --fill",
		"cd " + wt + " && git commit -m done",
		"cd " + wt + "; npm install",
		"rm -rf " + wt + "/build",
		"cp notes.md " + wt + "/notes.md",
		"echo hello > " + wt + "/greeting.txt",
		"cat notes.md >> " + wt + "/notes.md",
		"cat > " + wt + "/brief.md <<'EOF'\nthe brief\nEOF",
		"sed -i s/a/b/ " + wt + "/main.go",
		"( cd " + wt + " && make build )",
		"cd " + wt + " && cat README.md && git commit -am wip",
	} {
		f.bash(command).refused(t, command)
	}
}

// TestTheGuardLetsAnotherWorktreeBeRead keeps review cheap: reading what another
// agent has written is the ordinary way to check on it, and a guard that made
// that expensive would be worked around rather than obeyed.
func TestTheGuardLetsAnotherWorktreeBeRead(t *testing.T) {
	f := guardFixture(t)
	wt := f.DirFor("alpha")

	for _, command := range []string{
		"cat " + wt + "/main.go",
		"grep -rn TODO " + wt,
		"ls -la " + wt,
		"head -20 " + wt + "/README.md",
		"wc -l " + wt + "/main.go",
		"git -C " + wt + " log --oneline -20",
		"git -C " + wt + " status --short",
		"git -C " + wt + " diff HEAD",
		"git -C " + wt + " show HEAD:main.go",
		"git -C " + wt + " rev-parse HEAD",
		"cd " + wt + " && git log --stat",
		"cd " + wt + " && gh pr view",
		"diff -u main.go " + wt + "/main.go",
		"sed -n 1,20p " + wt + "/main.go",
		// A command substitution is a command of its own, and this one reads.
		"branch=$(git -C " + wt + " rev-parse --abbrev-ref HEAD)",
		// The body of a heredoc is text being fed to a program, not a command
		// line — a path mentioned in prose there is not a write.
		"cat <<'EOF'\nreview " + wt + " and report back\nEOF",
	} {
		f.bash(command).allowed(t, command)
	}
}

// TestTheGuardNeverRefusesTreewrightsOwnCommands pins the exemption the refusal
// depends on: the two commands it tells the reader to run name the worktree it
// just refused, and a guard that blocked those would have no way out to offer.
func TestTheGuardNeverRefusesTreewrightsOwnCommands(t *testing.T) {
	f := guardFixture(t)
	wt := f.DirFor("alpha")

	for _, command := range []string{
		"treewright rm alpha",
		"treewright close alpha",
		"treewright resume alpha --prompt-file /tmp/alpha-brief.md",
		`treewright send alpha "read /tmp/alpha-brief.md in full"`,
		"treewright ls --json",
		"cd " + wt + " && treewright ls",
		"tw rm alpha",
	} {
		f.bash(command).allowed(t, command)
	}
}

// TestTheGuardLeavesTheCallersOwnWorktreeAlone is the other half of the rule:
// the agent standing in a worktree is the one the work belongs to, and
// everything it does there is what the handoff was for.
func TestTheGuardLeavesTheCallersOwnWorktreeAlone(t *testing.T) {
	f := guardFixture(t)
	own := f.DirFor("alpha")

	f.guard(own, "Write", map[string]any{"file_path": filepath.Join(own, "main.go")}).
		allowed(t, "a write in the caller's own worktree")
	f.guard(own, "Bash", map[string]any{"command": "git commit -am done"}).
		allowed(t, "a commit in the caller's own worktree")
	f.guard(own, "Bash", map[string]any{"command": "git -C " + own + " push"}).
		allowed(t, "a push named by path in the caller's own worktree")
	f.guard(own, "Bash", map[string]any{"command": "gh pr create --fill"}).
		allowed(t, "a pull request from the caller's own worktree")

	// The main checkout is not one of the worktrees the guard is about: work
	// started there is what `move` exists to relocate, not something to refuse.
	f.guard(own, "Bash", map[string]any{"command": "git -C " + f.MainDir + " commit -am x"}).
		allowed(t, "a commit in the main checkout")
}

// TestTheGuardIsSilentOutOfScope is `signal`'s discipline, and it matters more
// here because this command can block: a refusal fired where treewright has no
// business is not a nag, it is work somebody has to argue their agent past.
func TestTheGuardIsSilentOutOfScope(t *testing.T) {
	f := guardFixture(t)
	wt := f.DirFor("alpha")
	write := map[string]any{"file_path": filepath.Join(wt, "main.go")}

	t.Run("a tool the guard does not judge", func(t *testing.T) {
		f.guard(f.MainDir, "Read", write).allowed(t, "Read")
		f.guard(f.MainDir, "Glob", map[string]any{"pattern": wt + "/**"}).allowed(t, "Glob")
	})

	t.Run("a payload it cannot read", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := Run(Env{
			Args: []string{"guard"}, Stdout: &out, Stderr: &errOut,
			Stdin: strings.NewReader("not json at all"),
		})
		result{stdout: out.String(), stderr: errOut.String(), err: err}.
			allowed(t, "unparseable stdin")
	})

	t.Run("nothing piped in at all", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := Run(Env{
			Args: []string{"guard"}, Stdout: &out, Stderr: &errOut,
			Stdin: strings.NewReader(""),
		})
		result{stdout: out.String(), stderr: errOut.String(), err: err}.
			allowed(t, "empty stdin")
	})

	// Being invoked wrong is out of scope rather than a usage error, and the
	// reason is the exit code: ErrUsage is also 2, so a mis-wired hook would
	// refuse every tool call in the session and hand back help as its reason.
	t.Run("arguments it does not know", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := Run(Env{
			Args: []string{"guard", "--why-not"}, Stdout: &out, Stderr: &errOut,
			Stdin: strings.NewReader("{}"),
		})
		if err != nil {
			t.Fatalf("err = %v, want the call allowed rather than a usage error", err)
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q, want silence", errOut.String())
		}
	})

	t.Run("outside a registered repository", func(t *testing.T) {
		elsewhere := t.TempDir()
		f.guard(elsewhere, "Write", write).allowed(t, "a directory git has never heard of")
	})

	t.Run("a repository with no worktrees", func(t *testing.T) {
		bare := newFixture(t, "")
		bare.guard(bare.MainDir, "Write", map[string]any{
			"file_path": filepath.Join(bare.MainDir, "main.go"),
		}).allowed(t, "nothing in flight")
	})
}

// TestTheGuardsRefusalNamesTheWayOn is the point of the hook. A refusal that
// only refuses teaches an agent to look for another route; one that names the
// fix ends the turn the way it should have gone.
func TestTheGuardsRefusalNamesTheWayOn(t *testing.T) {
	f := guardFixture(t)
	wt := f.DirFor("alpha")

	message := f.bash("git -C "+wt+" commit -m 'fix the null user'").
		refused(t, "a commit in another worktree")
	flattened := flat(message)

	for _, want := range []string{
		"alpha is another agent's worktree",
		"finished work included",
		"treewright resume alpha --prompt-file",
		"treewright send alpha",
		wt,
	} {
		if !strings.Contains(flattened, want) {
			t.Errorf("the refusal never says %q:\n%s", want, message)
		}
	}
	if !strings.Contains(flattened, "git -C "+wt+" commit") {
		t.Errorf("the refusal never names what it refused:\n%s", message)
	}

	// The voice rule, applied here because this message is written for an agent
	// and read by whoever opens the transcript afterwards — TestMessagesShareOneVoice
	// drives commands through exec, which has no stdin to hand a payload to.
	for line := range strings.SplitSeq(strings.TrimRight(message, "\n"), "\n") {
		trimmed := strings.TrimPrefix(strings.TrimLeft(line, " "), "error: ")
		if trimmed == "" {
			continue
		}
		if first := trimmed[0]; first >= 'A' && first <= 'Z' {
			t.Errorf("line starts with a capital, unlike the rest: %q", line)
		}
		if strings.HasSuffix(line, ".") {
			t.Errorf("line ends in a period, unlike the rest: %q", line)
		}
		if strings.Contains(line, "->") {
			t.Errorf("line uses ASCII -> where the rest use an em dash: %q", line)
		}
	}
}

// TestTheGuardNamesALongCommandWithoutRepeatingIt keeps a refusal the size of a
// message rather than the size of what it refused — the same reasoning that put
// `abbreviated` in the held-open wrapper.
func TestTheGuardNamesALongCommandWithoutRepeatingIt(t *testing.T) {
	f := guardFixture(t)
	command := "git -C " + f.DirFor("alpha") + " commit -m '" + strings.Repeat("x", 4000) + "'"

	message := f.bash(command).refused(t, "an enormous commit message")
	if len(message) > 1000 {
		t.Errorf("the refusal is %d bytes — it is carrying a copy of the command", len(message))
	}
	if !strings.Contains(message, "…") {
		t.Errorf("the command was not marked as cut:\n%s", message)
	}
}

// TestTheGuardAndItsMatcherAgree holds the two halves of the wiring together.
// The plugin's matcher decides which tool calls reach the guard at all, and the
// guard's own list decides which ones it judges — a tool named in one and not
// the other is either a call nobody looks at or a hook process spawned for
// nothing.
func TestTheGuardAndItsMatcherAgree(t *testing.T) {
	agent, ok := agentinit.Lookup("claude")
	if !ok {
		t.Fatal("no claude module")
	}
	var body string
	for _, f := range agent.Plugin {
		if f.Path == "hooks/hooks.json" {
			body = f.Body
		}
	}
	if body == "" {
		t.Fatal("the claude plugin ships no hooks.json")
	}

	var wiring struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(body), &wiring); err != nil {
		t.Fatalf("hooks.json does not parse: %v", err)
	}
	pre := wiring.Hooks["PreToolUse"]
	if len(pre) != 1 || len(pre[0].Hooks) != 1 {
		t.Fatalf("PreToolUse wiring = %+v, want one matcher running one command", pre)
	}
	if got, want := pre[0].Hooks[0].Command, "treewright guard"; got != want {
		t.Errorf("PreToolUse runs %q, want %q", got, want)
	}
	if got, want := pre[0].Matcher, strings.Join(guardedTools, "|"); got != want {
		t.Errorf("matcher = %q, want %q — the guard judges a different set than fires it", got, want)
	}
}

// TestScanCommandReadsAShellLine covers the reader directly, since the cases it
// gets wrong are cases the end-to-end tests would only show as a wrong verdict.
func TestScanCommandReadsAShellLine(t *testing.T) {
	tests := []struct {
		command   string
		words     [][]string
		redirects []string
	}{
		{
			command: "git commit -m 'a message with && in it'",
			words:   [][]string{{"git", "commit", "-m", "a message with && in it"}},
		},
		{
			command: "cd /a && rm -rf /b",
			words:   [][]string{{"cd", "/a"}, {"rm", "-rf", "/b"}},
		},
		{
			command:   "echo hi > /a/out.txt",
			words:     [][]string{{"echo", "hi"}},
			redirects: []string{"/a/out.txt"},
		},
		{
			// The fd belongs to the operator, and >&2 names no file.
			command:   "make 2>/a/err.log >&2",
			words:     [][]string{{"make"}},
			redirects: []string{"/a/err.log"},
		},
		{
			command:   "cmd &> /a/all.log",
			words:     [][]string{{"cmd"}},
			redirects: []string{"/a/all.log"},
		},
		{
			command:   "cat > /a/f <<'EOF'\nrm -rf /everything\nEOF\necho done",
			words:     [][]string{{"cat"}, {"echo", "done"}},
			redirects: []string{"/a/f"},
		},
		{
			command: "out=$(ls /a)",
			words:   [][]string{{"out=$"}, {"ls", "/a"}},
		},
		{
			command: `printf "%s\n" "${HOME}/x"`,
			words:   [][]string{{"printf", "%sn", "${HOME}/x"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			var words [][]string
			var redirects []string
			for _, seg := range scanCommand(tc.command) {
				if len(seg.words) > 0 {
					words = append(words, seg.words)
				}
				redirects = append(redirects, seg.redirects...)
			}
			if got, want := joinAll(words), joinAll(tc.words); got != want {
				t.Errorf("words = %v, want %v", got, want)
			}
			if got, want := strings.Join(redirects, ","), strings.Join(tc.redirects, ","); got != want {
				t.Errorf("redirects = %q, want %q", got, want)
			}
		})
	}
}

func joinAll(segments [][]string) string {
	parts := make([]string, 0, len(segments))
	for _, s := range segments {
		parts = append(parts, strings.Join(s, " "))
	}
	return strings.Join(parts, " | ")
}

// TestTheGuardReadsTheDirectoryItIsGiven pins the payload's cwd as the answer to
// "where is this agent standing". A hook is a child process whose own working
// directory is nobody's promise, and the two come apart exactly when a subagent
// is running somewhere else.
func TestTheGuardReadsTheDirectoryItIsGiven(t *testing.T) {
	f := guardFixture(t)

	// The test binary stands in the main checkout; the payload says otherwise.
	if wd, err := os.Getwd(); err != nil || wd != f.MainDir {
		t.Fatalf("wd = %q (%v), want the main checkout", wd, err)
	}
	f.guard(f.DirFor("alpha"), "Bash", map[string]any{
		"command": "git commit -am done",
	}).allowed(t, "a commit by the agent standing in alpha")

	f.guard(f.DirFor("beta"), "Bash", map[string]any{
		"command": "git -C " + f.DirFor("alpha") + " commit -am done",
	}).refused(t, "beta's agent committing in alpha")
}
