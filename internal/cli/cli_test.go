package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jay-snyder/treewright/internal/gittest"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// fixture is a scratch repository plus a config registry pointing at it, so the
// subcommands can be driven end to end.
type fixture struct {
	*gittest.Repo
	t        *testing.T
	registry string
}

func newFixture(t *testing.T, extraConfig string) *fixture {
	t.Helper()

	repo := gittest.New(t)
	f := &fixture{Repo: repo, t: t, registry: filepath.Join(repo.Root, "conf")}
	if err := os.MkdirAll(f.registry, 0o755); err != nil {
		t.Fatalf("mkdir registry: %v", err)
	}
	f.setConfig("main_dir = '" + repo.MainDir + "'\n" + extraConfig)

	t.Setenv("TREEWRIGHT_CONFIG_DIR", f.registry)
	// Unset so every command runs as it does from a plain shell — no client to
	// switch, no inherited eval file to write to.
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TREEWRIGHT_EVAL_FILE", "")
	// Windows are still opened without a client, so every tmux command is aimed at
	// a server private to this test. Nothing here can then open a window on, or
	// kill a window in, the developer's own tmux — and a test that wants to assert
	// about windows has a server it fully owns.
	privateTmuxServer(t)
	stubDefaultCommand(t)
	t.Chdir(repo.MainDir)
	return f
}

// stubDefaultCommand puts an inert "claude" first on PATH, that being what a new
// window runs unless a test configures something else.
//
// Windows are real now, so without this a test that does not care what runs in
// one would spawn a coding agent to find out — and only on a machine that has one
// installed, which is the worst of both worlds. With it, every machine behaves the
// same: the window opens, the command exits, the window closes.
func stubDefaultCommand(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write the claude stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// requireTmux skips a test whose assertions need a real tmux server. CI installs
// tmux, so a skip here means a developer's machine lacks it rather than that the
// behavior is unverified.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
}

// waitForFile polls for a file a background process is expected to create, since
// neither post_create nor a command running in a tmux window is waited on.
func waitForFile(t *testing.T, path, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("%s never produced %s", what, path)
}

// privateTmuxServer points treewright at a tmux server of this test's own, in a
// socket directory of its own, and takes both away afterwards. The server is not
// started here: no test pays for one unless it runs a command that opens a window.
//
// The directory is under /tmp rather than the test's own temp directory because a
// unix socket path is limited to little over a hundred characters, and a macOS
// temp directory path is long enough to approach that by itself.
//
// Removing the directory is what cleans up, rather than asking tmux where its
// socket is: kill-server does not unlink the socket, and a server whose last
// window has already closed is not there to answer the question.
func privateTmuxServer(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "tmx")
	if err != nil {
		t.Fatalf("make a tmux socket directory: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", dir)

	// Sanitized for the same reason a session name is: a subtest's name contains
	// a "/", which tmux would read as a path.
	label := tmux.SessionName(strings.ReplaceAll(t.Name(), "/", "-"))
	t.Setenv("TREEWRIGHT_TMUX_LABEL", label)

	// Registered after both Setenvs, so it runs before either is undone and the
	// kill still finds the server.
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", label, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
}

// setConfig writes the sole config, named "proj". base_branch and branch_prefix
// are always included so callers only supply what they are varying.
func (f *fixture) setConfig(body string) {
	f.t.Helper()
	full := "base_branch = 'main'\nbranch_prefix = '" + gittest.BranchPrefix + "'\n" + body
	if err := os.WriteFile(filepath.Join(f.registry, "proj.toml"), []byte(full), 0o644); err != nil {
		f.t.Fatalf("write config: %v", err)
	}
}

// result is one invocation's output, kept per stream so tests can assert on the
// stdout/stderr split rather than only on the merged text.
type result struct {
	stdout string
	stderr string
	err    error
}

// both is the two streams concatenated, for assertions that do not care which
// one carried the message.
func (r result) both() string { return r.stdout + r.stderr }

// exec invokes treewright with separate stdout and stderr.
func (f *fixture) exec(args ...string) result {
	f.t.Helper()
	var out, errOut bytes.Buffer
	err := Run(Env{Args: args, Version: "test", Stdout: &out, Stderr: &errOut})
	return result{stdout: out.String(), stderr: errOut.String(), err: err}
}

// run invokes treewright and returns its combined output plus the error.
func (f *fixture) run(args ...string) (string, error) {
	f.t.Helper()
	r := f.exec(args...)
	return r.both(), r.err
}

func (f *fixture) mustRun(args ...string) string {
	f.t.Helper()
	out, err := f.run(args...)
	if err != nil {
		f.t.Fatalf("treewright %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// runWithEvalFile invokes treewright with the shell integration's eval file wired
// up, and returns whatever treewright asked the calling shell to run.
func (f *fixture) runWithEvalFile(args ...string) (shellCommands string, output string) {
	f.t.Helper()
	path := filepath.Join(f.Root, "evalfile")
	_ = os.Remove(path)

	var out bytes.Buffer
	if err := Run(Env{Args: args, Stdout: &out, Stderr: &out, EvalFile: path}); err != nil {
		f.t.Fatalf("treewright %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	written, err := os.ReadFile(path)
	if err != nil {
		return "", out.String() // nothing written is a valid outcome
	}
	return string(written), out.String()
}

// statusOf reads one slug's status out of the `ls` table.
func (f *fixture) statusOf(slug string) string {
	f.t.Helper()
	for _, line := range strings.Split(f.mustRun("ls"), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == slug {
			return fields[1]
		}
	}
	return ""
}

// ---- ls --------------------------------------------------------------------

func TestLsWithNoWorktrees(t *testing.T) {
	f := newFixture(t, "")

	out := f.mustRun("ls")
	if !strings.Contains(out, "no worktrees for repo") {
		t.Errorf("output = %q, want a no-worktrees message", out)
	}
	if strings.Contains(out, "SLUG") {
		t.Errorf("a table header was printed with no rows:\n%s", out)
	}
}

func TestLsStatuses(t *testing.T) {
	f := newFixture(t, "")

	f.mustRun("new", "atbase")

	f.mustRun("new", "localonly")
	f.Commit(f.DirFor("localonly"), "local")

	f.mustRun("new", "act")
	f.Commit(f.DirFor("act"), "pushed")
	f.Push(f.DirFor("act"), f.BranchFor("act"))

	f.mustRun("new", "dirty")
	f.Write(f.DirFor("dirty"), "a.txt", "seed\nuncommitted\n")

	tests := []struct{ slug, want string }{
		{"atbase", "merged"},
		{"localonly", "unpushed"},
		{"act", "active"},
		{"dirty", "dirty"},
	}
	for _, tc := range tests {
		if got := f.statusOf(tc.slug); got != tc.want {
			t.Errorf("status of %s = %q, want %q", tc.slug, got, tc.want)
		}
	}
}

// TestLsMarksTheCurrentWorktree covers orientation: standing in one of several
// checkouts of the same repo, nothing else in the listing says which one is yours.
func TestLsMarksTheCurrentWorktree(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "alpha")
	f.mustRun("new", "beta")

	t.Run("marked from inside a worktree", func(t *testing.T) {
		t.Chdir(f.DirFor("beta"))

		var marked, unmarked int
		for _, line := range strings.Split(f.mustRun("ls"), "\n") {
			switch {
			case strings.HasPrefix(line, "*") && strings.Contains(line, "beta"):
				marked++
			case strings.Contains(line, "alpha"):
				unmarked++
				if strings.HasPrefix(line, "*") {
					t.Errorf("alpha is marked as current: %q", line)
				}
			}
		}
		if marked != 1 || unmarked != 1 {
			t.Errorf("marked %d rows and left %d unmarked, want one of each:\n%s", marked, unmarked, f.mustRun("ls"))
		}
	})

	t.Run("marked from the main checkout, which is a row of its own", func(t *testing.T) {
		// The base checkout heads the listing, so standing in it is standing in a
		// row — and the marker belongs on that row rather than on nothing.
		var marked string
		for _, line := range strings.Split(f.mustRun("ls"), "\n") {
			if strings.HasPrefix(line, "*") {
				marked = line
			}
		}
		if marked == "" {
			t.Fatalf("nothing marked from the main checkout:\n%s", f.mustRun("ls"))
		}
		if !strings.Contains(marked, "base") {
			t.Errorf("marked row = %q, want the base checkout's", marked)
		}
	})

	t.Run("no marker column from outside the repo", func(t *testing.T) {
		// A column that is blank on every row would be a permanent indent paid for
		// a case that is not occurring — which is now only the case when the
		// caller is standing in none of the checkouts at all.
		t.Chdir(t.TempDir())
		for _, line := range strings.Split(f.mustRun("ls", "proj"), "\n") {
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "*") {
				t.Errorf("line is indented for an unused marker column: %q", line)
			}
		}
	})
}

// TestLsShowsWhatIsAtStake pins the counts onto the two statuses that block a
// removal, since those are the numbers rm refuses over.
func TestLsShowsWhatIsAtStake(t *testing.T) {
	f := newFixture(t, "")

	f.mustRun("new", "messy")
	f.Write(f.DirFor("messy"), "a.txt", "seed\nedited\n")
	f.Write(f.DirFor("messy"), "extra.txt", "new file\n")

	f.mustRun("new", "local")
	f.Commit(f.DirFor("local"), "one")
	f.Commit(f.DirFor("local"), "two")

	out := f.mustRun("ls")
	if !strings.Contains(out, "dirty (2)") {
		t.Errorf("output = %q, want the count of uncommitted files", out)
	}
	if !strings.Contains(out, "unpushed (2)") {
		t.Errorf("output = %q, want the count of unpushed commits", out)
	}
}

// ---- new -------------------------------------------------------------------

func TestNewCreatesWorktreeAndBranch(t *testing.T) {
	f := newFixture(t, "")

	out := f.mustRun("new", "feature")
	if !strings.Contains(out, "creating branch x/feature off origin/main") {
		t.Errorf("output = %q, want the fork point reported", out)
	}
	if !f.Exists("feature") {
		t.Fatalf("worktree %s was not created", f.DirFor("feature"))
	}
	if got := f.Git(f.DirFor("feature"), "branch", "--show-current"); got != "x/feature" {
		t.Errorf("branch = %q, want x/feature", got)
	}
}

func TestNewStripsAnAlreadyPrefixedSlug(t *testing.T) {
	f := newFixture(t, "")

	// `new x/pfx` under prefix "x/" must not produce branch x/x/pfx or a
	// worktree at repo-x/pfx.
	out := f.mustRun("new", "x/pfx")
	if !strings.Contains(out, `stripped the "x/" prefix`) {
		t.Errorf("output = %q, want the correction reported", out)
	}
	if !f.Exists("pfx") {
		t.Error("worktree repo-pfx was not created")
	}
	if got := f.Git(f.DirFor("pfx"), "branch", "--show-current"); got != "x/pfx" {
		t.Errorf("branch = %q, want x/pfx", got)
	}
}

func TestNewRejectsSlugWithPathSeparator(t *testing.T) {
	f := newFixture(t, "")

	// repo-a/b would nest the worktree inside a "repo-a" directory that rm
	// leaves behind empty, so the slug is refused outright.
	out, err := f.run("new", "a/b")
	if err == nil {
		t.Fatal("want an error for a slug containing a separator")
	}
	if !errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage (exit 2)", err)
	}
	if !strings.Contains(out, "a/b") {
		t.Errorf("output = %q, want the slug named", out)
	}
	if _, statErr := os.Stat(f.MainDir + "-a"); statErr == nil {
		t.Errorf("a stray %s-a directory was created\n%s", f.MainDir, out)
	}
}

func TestNewCarriesConfiguredFiles(t *testing.T) {
	f := newFixture(t, "carry_files = ['.env', 'nested/creds.txt']\n")
	f.Write(f.MainDir, ".env", "SECRET=1\n")
	f.Write(f.MainDir, "nested/creds.txt", "token\n")

	f.mustRun("new", "carried")

	if got, err := os.ReadFile(filepath.Join(f.DirFor("carried"), ".env")); err != nil || string(got) != "SECRET=1\n" {
		t.Errorf(".env not carried: %v %q", err, got)
	}
	// A carried file in a subdirectory needs that directory created first.
	if got, err := os.ReadFile(filepath.Join(f.DirFor("carried"), "nested", "creds.txt")); err != nil || string(got) != "token\n" {
		t.Errorf("nested/creds.txt not carried: %v %q", err, got)
	}
}

func TestNewWarnsAboutMissingCarryFiles(t *testing.T) {
	f := newFixture(t, "carry_files = ['.env']\n")

	// Nothing created .env, so the config is stale and the user should hear it
	// rather than find out when the app fails to boot.
	out := f.mustRun("new", "nofiles")
	if !strings.Contains(out, "carry_files") || !strings.Contains(out, "not found") {
		t.Errorf("output = %q, want a warning about the missing carry file", out)
	}
}

func TestNewRequiresASlug(t *testing.T) {
	f := newFixture(t, "")
	if _, err := f.run("new"); err == nil {
		t.Error("want an error when no slug is given")
	}
}

func TestNewRunsPostCreate(t *testing.T) {
	f := newFixture(t, "post_create = 'echo ran > post-create-marker'\n")

	out := f.mustRun("new", "hooked")
	if !strings.Contains(out, "post_create") {
		t.Errorf("output = %q, want the hook reported", out)
	}
	// The hook is deliberately not waited on, so poll for its effect rather than
	// assuming it has already finished.
	waitForFile(t, filepath.Join(f.DirFor("hooked"), "post-create-marker"), "post_create")
}

// ---- rm --------------------------------------------------------------------

func TestRmGuards(t *testing.T) {
	f := newFixture(t, "")

	f.mustRun("new", "localonly")
	f.Commit(f.DirFor("localonly"), "local")

	f.mustRun("new", "dirty")
	f.Write(f.DirFor("dirty"), "a.txt", "seed\nuncommitted\n")

	f.mustRun("new", "pushed")
	f.Commit(f.DirFor("pushed"), "pushed")
	f.Push(f.DirFor("pushed"), f.BranchFor("pushed"))

	t.Run("blocks a local-only commit", func(t *testing.T) {
		out, err := f.run("rm", "localonly")
		if err == nil {
			t.Error("want a refusal")
		}
		if !strings.Contains(out, "not on origin") {
			t.Errorf("output = %q, want the reason named", out)
		}
		if !f.Exists("localonly") {
			t.Error("worktree was removed despite the refusal")
		}
	})

	t.Run("blocks uncommitted changes", func(t *testing.T) {
		out, err := f.run("rm", "dirty")
		if err == nil {
			t.Error("want a refusal")
		}
		if !strings.Contains(out, "uncommitted file") {
			t.Errorf("output = %q, want the reason named", out)
		}
		if !f.Exists("dirty") {
			t.Error("worktree was removed despite the refusal")
		}
	})

	t.Run("allows pushed and clean", func(t *testing.T) {
		if _, err := f.run("rm", "pushed"); err != nil {
			t.Errorf("unexpected refusal: %v", err)
		}
		if f.Exists("pushed") {
			t.Error("worktree was not removed")
		}
	})

	t.Run("force overrides a refusal", func(t *testing.T) {
		if _, err := f.run("rm", "--force", "localonly"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if f.Exists("localonly") {
			t.Error("worktree was not removed")
		}
	})

	t.Run("flags are accepted after the slug", func(t *testing.T) {
		// Go's flag package would read this -f as a positional argument.
		if _, err := f.run("rm", "dirty", "-f"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if f.Exists("dirty") {
			t.Error("worktree was not removed")
		}
	})
}

func TestRmAllowsSquashMergedWithoutForce(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "squashed")
	dir, branch := f.DirFor("squashed"), f.BranchFor("squashed")

	f.Write(dir, "a.txt", "seed\nchange 1\n")
	f.Git(dir, "commit", "--quiet", "-am", "work 1")
	f.Write(dir, "a.txt", "seed\nchange 1\nchange 2\n")
	f.Git(dir, "commit", "--quiet", "-am", "work 2")
	f.Push(dir, branch)
	f.SquashMerge(branch, "squashed work (#1)")

	if got := f.statusOf("squashed"); got != "merged" {
		t.Errorf("status = %q, want merged", got)
	}
	if _, err := f.run("rm", "squashed"); err != nil {
		t.Errorf("squash-merged worktree needed --force: %v", err)
	}
	if f.Exists("squashed") {
		t.Error("worktree was not removed")
	}
}

func TestRmEmitsCdWhenStandingInTheRemovedWorktree(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "here")
	t.Chdir(f.DirFor("here")) // the shell is inside the directory about to go

	commands, _ := f.runWithEvalFile("rm", "--force", "here")

	want := "cd '" + f.MainDir + "'"
	if !strings.Contains(commands, want) {
		t.Errorf("shell commands = %q, want %q", commands, want)
	}
}

func TestRmDoesNotEmitCdFromElsewhere(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "there")

	// cwd is the main checkout, so the caller's directory stays valid and must
	// be left alone.
	commands, _ := f.runWithEvalFile("rm", "--force", "there")
	if commands != "" {
		t.Errorf("shell commands = %q, want none", commands)
	}
}

// ---- prune -----------------------------------------------------------------

func TestPrune(t *testing.T) {
	f := newFixture(t, "")

	f.mustRun("new", "merged1") // tip at origin/main
	f.mustRun("new", "act")
	f.Commit(f.DirFor("act"), "pushed")
	f.Push(f.DirFor("act"), f.BranchFor("act"))
	f.mustRun("new", "dirty")
	f.Write(f.DirFor("dirty"), "a.txt", "seed\nuncommitted\n")

	t.Run("dry run lists but removes nothing", func(t *testing.T) {
		r := f.exec("prune")
		if !strings.Contains(r.stdout, f.DirFor("merged1")) {
			t.Errorf("stdout = %q, want the merged worktree path", r.stdout)
		}
		// Open and dirty work must not be offered up for deletion.
		if strings.Contains(r.stdout, f.DirFor("act")) || strings.Contains(r.stdout, f.DirFor("dirty")) {
			t.Errorf("stdout = %q, want open and dirty work left out", r.stdout)
		}
		if !f.Exists("merged1") {
			t.Error("dry run removed a worktree")
		}
	})

	t.Run("-y removes only merged and clean", func(t *testing.T) {
		f.mustRun("prune", "-y")
		if f.Exists("merged1") {
			t.Error("merged worktree was not removed")
		}
		if !f.Exists("act") {
			t.Error("an open pull request's worktree was removed")
		}
		if !f.Exists("dirty") {
			t.Error("a dirty worktree was removed")
		}
	})
}

// TestPruneEmitsCdWhenStandingInAPrunedWorktree covers the gap that rm handled
// and prune did not: prune can delete the directory the caller is standing in.
func TestPruneEmitsCdWhenStandingInAPrunedWorktree(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "reaped") // merged and clean, so prune will take it
	t.Chdir(f.DirFor("reaped"))

	commands, _ := f.runWithEvalFile("prune", "-y")

	want := "cd '" + f.MainDir + "'"
	if !strings.Contains(commands, want) {
		t.Errorf("shell commands = %q, want %q", commands, want)
	}
	if f.Exists("reaped") {
		t.Error("worktree was not pruned")
	}
}

func TestPruneWithNothingToDo(t *testing.T) {
	f := newFixture(t, "")
	out := f.mustRun("prune")
	if !strings.Contains(out, "nothing to prune") {
		t.Errorf("output = %q, want a nothing-to-prune message", out)
	}
}

// ---- resume ----------------------------------------------------------------

func TestResume(t *testing.T) {
	// The resume command outlives the command, so that the window it opens is
	// still there to be reported on rather than having closed with it.
	f := newFixture(t, "resume_command = 'sleep 300'\n")
	f.mustRun("new", "alpha")
	f.mustRun("new", "beta")

	t.Run("explicit slug resolves without a prompt", func(t *testing.T) {
		requireTmux(t)
		out := f.mustRun("resume", "alpha")
		// The window alpha's slug names, in the repository's own session.
		want := "window ALPHA is open in tmux session proj"
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	})

	t.Run("prefixed slug is stripped", func(t *testing.T) {
		out := f.mustRun("resume", "x/alpha")
		if !strings.Contains(out, `using "alpha"`) {
			t.Errorf("output = %q, want the stripped prefix reported", out)
		}
	})

	t.Run("unknown slug errors and names it", func(t *testing.T) {
		out, err := f.run("resume", "nope")
		if err == nil {
			t.Fatal("want an error")
		}
		if combined := out + err.Error(); !strings.Contains(combined, `"nope"`) {
			t.Errorf("error = %q, want the bad slug named", combined)
		}
	})
}

// TestResumeUsesConfiguredCommand pins that the window runs what the config says
// — nothing hardcodes any particular agent — by having resume_command leave a
// file behind. The file appearing is the window having actually run it, which is
// worth more than treewright reporting that it would.
func TestResumeUsesConfiguredCommand(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "resumed")
	f := newFixture(t, "command = 'true'\nresume_command = 'touch "+marker+"'\n")
	f.mustRun("new", "solo")

	// Named rather than picked: the menu always has the base checkout in it, so
	// even a lone worktree is now one of two rows and there is no tty to choose on.
	f.mustRun("resume", "solo")
	waitForFile(t, marker, "resume_command")
}

// TestResumeWithNoWorktrees covers the empty repository. The menu still appears,
// holding the one row there is, with the sentence that says how to start work
// above it — so resume can no longer fail here, only be dismissed.
func TestResumeWithNoWorktrees(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("resume")
	if !strings.Contains(r.stderr, "treewright new") {
		t.Errorf("stderr = %q, want it to name the way to start work", r.stderr)
	}
	// Dismissed, not failed: with no tty the picker cancels, and cancelling
	// resume is not an error — that is what closes the popup on one Escape.
	if r.err != nil {
		t.Errorf("err = %v, want nil so the popup closes on the Escape that dismissed the menu", r.err)
	}
	// An empty target path must never reach a command line.
	if strings.Contains(r.both(), "cd  ") {
		t.Errorf("output = %q, want no empty-path command", r.both())
	}
}

// ---- base and configuration ------------------------------------------------

func TestBaseWarnsWhenOffTheBaseBranch(t *testing.T) {
	f := newFixture(t, "command = 'true'\n")
	f.Git(f.MainDir, "checkout", "--quiet", "-b", "wandered")

	out := f.mustRun("base")
	if !strings.Contains(out, "not main") {
		t.Errorf("output = %q, want a drift warning", out)
	}

	// The warning is the base window's, not the `base` command's, so it has to
	// survive the other way in — which is the whole reason both routes go through
	// one function.
	f.Git(f.MainDir, "checkout", "--quiet", "-b", "wandered-again")
	if out := f.mustRun("resume", "base"); !strings.Contains(out, "not main") {
		t.Errorf("resume base = %q, want the same drift warning", out)
	}
}

// TestBaseWindowCommand pins the one place the two ways into that window
// disagree, and why each is right.
//
// `base` opens a general-purpose window and runs command. Picking the base row
// out of the resume menu is resuming, and runs resume_command — after a reboot
// the triage you were doing in the base window is exactly what you want back,
// not a blank one. It shows once and never again: every later call finds the
// window by its directory and switches to it, whatever started it.
//
// The names here are curt because they end up in a socket path: tmux is given a
// server of this test's own, named after it, and a long one overflows the length
// a unix socket may have.
func TestBaseWindowCommand(t *testing.T) {
	requireTmux(t)

	t.Run("base", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "fresh")
		f := newFixture(t, "command = 'touch "+marker+"'\nresume_command = 'true'\n")
		f.mustRun("base")
		waitForFile(t, marker, "command")
	})

	t.Run("resume", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "resumed")
		f := newFixture(t, "command = 'true'\nresume_command = 'touch "+marker+"'\n")
		f.mustRun("resume", "base")
		waitForFile(t, marker, "resume_command")
	})
}

func TestConfigSelection(t *testing.T) {
	f := newFixture(t, "")

	t.Run("explicit repo name works from outside any repo", func(t *testing.T) {
		t.Chdir(f.Root) // not a git repo
		if _, err := f.run("ls", "proj"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("unknown repo name errors", func(t *testing.T) {
		if _, err := f.run("ls", "nope"); err == nil {
			t.Error("want an error for an unknown config name")
		}
	})
}

// TestWorktreesAreVisibleThroughASymlinkedMainDir is the end-to-end regression
// for a config whose main_dir reaches the repo through a symlink: `new` used to
// succeed while `ls` reported nothing, because git reports resolved paths.
func TestWorktreesAreVisibleThroughASymlinkedMainDir(t *testing.T) {
	f := newFixture(t, "")
	viaLink := f.Symlink()
	if viaLink == f.MainDir {
		t.Fatal("symlinked path is identical to the real one; test proves nothing")
	}
	f.setConfig("main_dir = '" + viaLink + "'\n")

	f.mustRun("new", "viasym")

	out := f.mustRun("ls")
	if !strings.Contains(out, "viasym") {
		t.Errorf("ls output = %q, want the worktree listed", out)
	}
	if got := f.mustRun("__complete", "slugs"); !strings.Contains(got, "viasym") {
		t.Errorf("completion = %q, want the worktree listed", got)
	}
}

// ---- dispatch --------------------------------------------------------------

func TestDispatch(t *testing.T) {
	f := newFixture(t, "")

	t.Run("bare treewright is a usage error", func(t *testing.T) {
		out, err := f.run()
		if err == nil {
			t.Error("want a non-zero exit")
		}
		if !strings.Contains(out, "new <slug>") {
			t.Errorf("output = %q, want the command overview", out)
		}
	})

	t.Run("help exits zero", func(t *testing.T) {
		out, err := f.run("--help")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "resume [slug]") {
			t.Errorf("output = %q, want the command overview", out)
		}
	})

	t.Run("unknown command errors", func(t *testing.T) {
		if _, err := f.run("bogus"); err == nil {
			t.Error("want an error")
		}
	})

	t.Run("unknown flag errors", func(t *testing.T) {
		if _, err := f.run("ls", "--nope"); err == nil {
			t.Error("want an error")
		}
	})
}

// TestParseArgs covers the shared parser directly, because every subcommand
// routes through it and the value-taking flags added for tmux-init changed how
// it walks the arguments — from one pass over each word to one that can consume
// the word after it.
func TestParseArgs(t *testing.T) {
	// untouched is what the value holds when the flag never appears, standing in
	// for the caller's own default — which has to survive a run that does not
	// mention it. Every case says which it expects, so nothing is implied.
	const untouched = "«untouched»"

	tests := []struct {
		name       string
		args       []string
		wantFlag   bool
		wantValue  string
		wantPos    []string
		wantErr    string
		maxAllowed int
	}{
		{name: "a switch on its own", args: []string{"-f"}, wantFlag: true, wantValue: untouched},
		{name: "a value, spaced", args: []string{"--key", "G"}, wantValue: "G"},
		{name: "a value, joined", args: []string{"--key=G"}, wantValue: "G"},
		{
			// The spelling that lets someone drop a binding rather than move it.
			name: "an empty value is a value", args: []string{"--key="}, wantValue: "",
		},
		{
			name: "flags mix with positionals in any order",
			args: []string{"one", "-f", "--key", "G"}, maxAllowed: 1,
			wantFlag: true, wantValue: "G", wantPos: []string{"one"},
		},
		{
			// The failure this ordering is written to avoid: reading the next flag
			// as the value, which drops it silently.
			name: "a value flag does not swallow the next flag",
			args: []string{"--key", "-f"}, wantErr: "needs a value",
		},
		{name: "a value flag at the end", args: []string{"--key"}, wantErr: "needs a value"},
		{name: "a switch given a value", args: []string{"-f=yes"}, wantErr: "takes no value"},
		{name: "an unknown flag", args: []string{"--nope"}, wantErr: "unknown flag"},
		{
			name: "a lone dash is a positional, not a flag",
			args: []string{"-"}, maxAllowed: 1, wantPos: []string{"-"}, wantValue: untouched,
		},
		{name: "too many positionals", args: []string{"a"}, wantErr: "unexpected argument"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var flag bool
			value := untouched
			pos, err := parseArgs("test", tc.args,
				map[string]*bool{"-f": &flag},
				map[string]*string{"--key": &value},
				tc.maxAllowed)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want an error mentioning %q, got positionals %v", tc.wantErr, pos)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			if flag != tc.wantFlag {
				t.Errorf("flag = %v, want %v", flag, tc.wantFlag)
			}
			if value != tc.wantValue {
				t.Errorf("value = %q, want %q", value, tc.wantValue)
			}
			if strings.Join(pos, ",") != strings.Join(tc.wantPos, ",") {
				t.Errorf("positionals = %v, want %v", pos, tc.wantPos)
			}
		})
	}
}

// TestRejectsExtraArguments guards against silently discarding an argument,
// which would make a typo — a slug with a stray space, say — look accepted.
func TestRejectsExtraArguments(t *testing.T) {
	f := newFixture(t, "")

	for _, args := range [][]string{
		{"ls", "proj", "extra"},
		{"prune", "proj", "extra"},
		{"rm", "a", "b"},
		{"resume", "a", "b"},
		{"base", "proj", "extra"},
		{"new", "a", "b", "c"},
		{"shell-init", "zsh", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := f.exec(args...)
			if r.err == nil {
				t.Fatalf("want an error, got output %q", r.stdout)
			}
			if !errors.Is(r.err, ErrUsage) {
				t.Errorf("err = %v, want ErrUsage (exit 2)", r.err)
			}
			if !strings.Contains(r.stderr, "unexpected argument") {
				t.Errorf("stderr = %q, want it to name the unexpected argument", r.stderr)
			}
		})
	}
}

func TestCompleteIsQuietAndUseful(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "alpha")
	f.mustRun("new", "beta")

	if got := f.mustRun("__complete", "slugs"); got != "alpha\nbeta\n" {
		t.Errorf("slugs = %q, want alpha and beta", got)
	}
	if got := f.mustRun("__complete", "repos"); got != "proj\n" {
		t.Errorf("repos = %q, want proj", got)
	}
	if got := f.mustRun("__complete", "shells"); got != "bash\nfish\nzsh\n" {
		t.Errorf("shells = %q", got)
	}

	// Completion runs while the user is mid-keystroke: an unresolvable request
	// must print nothing rather than a diagnostic into their prompt.
	t.Chdir(f.Root)
	t.Setenv("TREEWRIGHT_CONFIG_DIR", filepath.Join(f.Root, "empty"))
	out, err := f.run("__complete", "slugs")
	if err != nil || out != "" {
		t.Errorf("completion with no config = (%q, %v), want silence", out, err)
	}
}

// ---- output conventions ----------------------------------------------------

// TestStdoutCarriesOnlyTheAnswer pins the rule the whole CLI is organized
// around: stdout is pipeable data, stderr is narration. Progress and warnings
// mixed into stdout would corrupt any consumer.
func TestStdoutCarriesOnlyTheAnswer(t *testing.T) {
	f := newFixture(t, "carry_files = ['.env']\n") // missing, so `new` also warns

	t.Run("new emits the worktree path and nothing else", func(t *testing.T) {
		r := f.exec("new", "feature")
		if r.err != nil {
			t.Fatalf("new: %v\n%s", r.err, r.both())
		}
		if got, want := r.stdout, f.DirFor("feature")+"\n"; got != want {
			t.Errorf("stdout = %q, want exactly %q", got, want)
		}
		// The narration and the warning both belong on the other worktree.
		if !strings.Contains(r.stderr, "creating branch") {
			t.Errorf("stderr = %q, want the progress line", r.stderr)
		}
		if !strings.Contains(r.stderr, "warning: carry_files") {
			t.Errorf("stderr = %q, want the carry_files warning", r.stderr)
		}
	})

	t.Run("ls emits the table and nothing else", func(t *testing.T) {
		r := f.exec("ls")
		if r.err != nil {
			t.Fatalf("ls: %v", r.err)
		}
		if !strings.Contains(r.stdout, "SLUG") || !strings.Contains(r.stdout, "feature") {
			t.Errorf("stdout = %q, want the table", r.stdout)
		}
		if r.stderr != "" {
			t.Errorf("stderr = %q, want nothing", r.stderr)
		}
	})

	t.Run("an empty listing leaves stdout empty", func(t *testing.T) {
		f2 := newFixture(t, "")
		r := f2.exec("ls")
		if r.stdout != "" {
			t.Errorf("stdout = %q, want nothing for a consumer to read", r.stdout)
		}
		if !strings.Contains(r.stderr, "no worktrees") {
			t.Errorf("stderr = %q, want the explanation", r.stderr)
		}
	})
}

func TestLsJSON(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "alpha")
	f.Commit(f.DirFor("alpha"), "local work")

	r := f.exec("ls", "--json")
	if r.err != nil {
		t.Fatalf("ls --json: %v\n%s", r.err, r.both())
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want nothing alongside machine output", r.stderr)
	}

	var rows []struct {
		Slug     string `json:"slug"`
		Base     bool   `json:"base"`
		Dir      string `json:"dir"`
		Branch   string `json:"branch"`
		Status   string `json:"status"`
		Ahead    *int   `json:"ahead"`
		Behind   *int   `json:"behind"`
		Unpushed int    `json:"unpushed"`
		Window   string `json:"window"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, r.stdout)
	}
	// The base checkout heads the listing as it heads the menu, and says so, so
	// that a consumer working out where to put a ticket can tell the one row it
	// can never remove from the ones it can.
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want the base checkout and alpha: %s", len(rows), r.stdout)
	}
	base := rows[0]
	if !base.Base || base.Slug != "" || base.Status != "base" || base.Dir != f.MainDir {
		t.Errorf("base row = %+v, want the main checkout flagged and unslugged", base)
	}
	got := rows[1]
	if got.Base {
		t.Errorf("alpha is flagged as the base checkout: %+v", got)
	}
	if got.Slug != "alpha" || got.Branch != "x/alpha" || got.Status != "unpushed" || got.Unpushed != 1 {
		t.Errorf("row = %+v", got)
	}
	if got.Dir != f.DirFor("alpha") {
		t.Errorf("dir = %q, want %q", got.Dir, f.DirFor("alpha"))
	}
	if got.Ahead == nil || *got.Ahead != 1 {
		t.Errorf("ahead = %v, want 1", got.Ahead)
	}
	// The window fields are asserted by TestLsReportsTheOpenWindow, which owns a
	// tmux server: whether a window is open here depends on how quickly the
	// stubbed command exited, which is not this test's subject.
}

func TestLsJSONIsAnEmptyArrayWithNoWorktrees(t *testing.T) {
	f := newFixture(t, "")

	// A caller parsing this needs valid JSON whether or not there is anything to
	// report, so the empty case cannot be a prose message.
	r := f.exec("ls", "--json")
	if r.err != nil {
		t.Fatalf("ls --json: %v", r.err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%q", err, r.stdout)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows, want none", len(rows))
	}
}

func TestLsOutputIsPlainWhenNotATerminal(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "alpha")

	// Buffers are not terminals, so no escape sequence may reach them — this is
	// what keeps color out of pipes, files, and this test suite's assertions.
	r := f.exec("ls")
	if strings.Contains(r.stdout, "\x1b") {
		t.Errorf("stdout carries escape sequences: %q", r.stdout)
	}
}

func TestHelpGoesToStdoutAndUsageErrorsToStderr(t *testing.T) {
	f := newFixture(t, "")

	t.Run("help asked for is the answer", func(t *testing.T) {
		r := f.exec("help")
		if r.err != nil {
			t.Errorf("err = %v, want nil (exit 0)", r.err)
		}
		if !strings.Contains(r.stdout, "usage: treewright") {
			t.Errorf("stdout = %q, want the overview", r.stdout)
		}
		if r.stderr != "" {
			t.Errorf("stderr = %q, want nothing", r.stderr)
		}
	})

	t.Run("per-command help exists for every command", func(t *testing.T) {
		for _, c := range commands {
			if c.hidden {
				continue
			}
			r := f.exec("help", c.name)
			if r.err != nil {
				t.Errorf("help %s: %v", c.name, r.err)
			}
			if !strings.Contains(r.stdout, "usage: treewright "+c.name) {
				t.Errorf("help %s: stdout = %q", c.name, r.stdout)
			}
			// -h and --help must reach the same place as `help <command>`.
			for _, flag := range []string{"-h", "--help"} {
				if got := f.exec(c.name, flag); got.stdout != r.stdout {
					t.Errorf("%s %s printed something different from help %s", c.name, flag, c.name)
				}
			}
		}
	})

	t.Run("being invoked wrongly is not the answer", func(t *testing.T) {
		r := f.exec()
		if !errors.Is(r.err, ErrUsage) {
			t.Errorf("err = %v, want ErrUsage (exit 2)", r.err)
		}
		if r.stdout != "" {
			t.Errorf("stdout = %q, want nothing", r.stdout)
		}
		if !strings.Contains(r.stderr, "usage: treewright") {
			t.Errorf("stderr = %q, want the overview", r.stderr)
		}
	})
}

// TestMessagesShareOneVoice guards the conventions from drifting back apart: a
// new message that invents its own prefix, or ends in a period, will fail here.
func TestMessagesShareOneVoice(t *testing.T) {
	f := newFixture(t, "carry_files = ['.env']\n")
	f.mustRun("new", "alpha")
	f.Commit(f.DirFor("alpha"), "local work")

	var lines []string
	for _, args := range [][]string{
		{"new", "beta"}, {"ls"}, {"prune"}, {"rm", "alpha"}, {"resume", "nope"},
	} {
		r := f.exec(args...)
		lines = append(lines, strings.Split(strings.TrimRight(r.stderr, "\n"), "\n")...)
	}

	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "treewright:") || strings.HasPrefix(line, "note:") {
			t.Errorf("message uses a retired prefix: %q", line)
		}
		if strings.HasSuffix(line, ".") {
			t.Errorf("message ends in a period, unlike the rest: %q", line)
		}
		if strings.Contains(line, "->") {
			t.Errorf("message uses ASCII -> where the rest use an em dash: %q", line)
		}
		if trimmed := strings.TrimLeft(line, " "); trimmed != "" {
			if first := trimmed[0]; first >= 'A' && first <= 'Z' {
				t.Errorf("message starts with a capital, unlike the rest: %q", line)
			}
		}
	}
}

// TestHelpTextIsCleanlyIndented guards a hazard of keeping help prose in Go raw
// string literals: the text is column-sensitive, and any reindentation of the
// surrounding code silently becomes stray whitespace in what the user reads.
func TestHelpTextIsCleanlyIndented(t *testing.T) {
	for _, c := range commands {
		for label, text := range map[string]string{"summary": c.summary, "long": c.long} {
			if strings.Contains(text, "\t") {
				t.Errorf("%s %s contains a tab", c.name, label)
			}
			for _, line := range strings.Split(text, "\n") {
				if line != strings.TrimRight(line, " \t") {
					t.Errorf("%s %s has a line with trailing whitespace: %q", c.name, label, line)
				}
			}
		}
		if strings.HasSuffix(c.summary, ".") {
			t.Errorf("%s summary ends in a period, unlike the others: %q", c.name, c.summary)
		}
	}
}

// TestEveryFlagIsDocumented keeps help and behavior from drifting: a flag the
// parser accepts but help omits is undiscoverable, and one help lists but the
// parser rejects is a lie.
func TestEveryFlagIsDocumented(t *testing.T) {
	f := newFixture(t, "")

	for _, c := range commands {
		if c.hidden {
			continue
		}
		for _, doc := range c.flags {
			for _, name := range strings.Split(doc.names, ",") {
				name = strings.TrimSpace(name)

				// The parser must accept it: an unknown flag is a usage error,
				// and anything else means it is wired up.
				r := f.exec(c.name, name, "--definitely-not-a-flag")
				if strings.Contains(r.stderr, "unknown flag "+strconv.Quote(name)) {
					t.Errorf("%s: help documents %s but the parser rejects it", c.name, name)
				}

				// And completion must offer it.
				candidates := f.mustRun("__complete", "flags", c.name)
				if !strings.Contains(candidates, name+"\n") {
					t.Errorf("%s: completion omits %s (offered: %q)", c.name, name, candidates)
				}
			}
		}
	}
}
