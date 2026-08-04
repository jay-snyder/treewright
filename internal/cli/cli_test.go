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

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/gittest"
	"github.com/jay-snyder/treewright/internal/testenv"
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

	// HOME is this test's own, which is the same argument as the private tmux
	// server below, one directory over: the placement `agent-init` installs into
	// by default is under the user's home, so without this a test of it would
	// rewrite the developer's own wiring — and a test of anything else would let
	// their ~/.claude decide what doctor sees.
	t.Setenv("HOME", t.TempDir())
	// And the agent's own variable for moving that directory, which HOME alone
	// does not contain: an absolute $CLAUDE_CONFIG_DIR is answered whatever HOME
	// says, so on the machine of anyone who has moved it — the XDG layout puts it
	// under ~/.local/state — these tests would install into the developer's real
	// config directory and doctor would report on their real wiring. Emptied
	// rather than pointed at the temp home, so the default placement is what the
	// suite exercises; the test that covers a moved directory sets it itself.
	t.Setenv("CLAUDE_CONFIG_DIR", "")

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
	testenv.PrivateTmuxServer(t)
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

// requireTmux stands aside from a test whose assertions need a real tmux server.
// A developer without tmux gets a skip; CI, which installs it, gets a failure —
// see internal/testenv, since a skipped tmux test in CI is an unverified one.
func requireTmux(t *testing.T) {
	t.Helper()
	testenv.RequireTool(t, "tmux")
}

// flat collapses a message's layout — its line breaks, its continuation indent
// and the padding that lines a field's values up — so an assertion can be about
// what a message says rather than about how wide its label column happened to
// come out.
//
// Worth having because the alternative is assertions that break every time a
// label is renamed one character longer, which teaches the next person to fix
// them by pasting in whatever the message currently prints. The layout is
// asserted where it is the subject: TestAMessageContinuesUnderItself, the field
// and list tests, and the table tests.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

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

// waitForContent polls for a file to contain want. A sequence of commands is
// finished only once its last one has left its mark, and the file exists from the
// first, so waitForFile cannot answer that.
func waitForContent(t *testing.T, path, want, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(path); err == nil && strings.Contains(string(body), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	body, _ := os.ReadFile(path)
	t.Errorf("%s never wrote %q to %s — it holds %q", what, want, path, body)
}

// hideTmux rebuilds PATH with everything a command needs except tmux, so a test
// can drive the no-tmux paths on a machine that has it installed. The tools a
// command shells out to — and the fixture's stubbed claude — come along as
// symlinks, resolved from the PATH being replaced.
func hideTmux(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"git", "sh", "claude"} {
		resolved, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s is not on PATH to begin with: %v", tool, err)
		}
		if err := os.Symlink(resolved, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("link %s: %v", tool, err)
		}
	}
	t.Setenv("PATH", dir)
}

// noTTY takes the terminal away, so a test can drive the nobody-to-ask path of
// a window-close offer deterministically — and so the suite can never block a
// developer's own terminal on a [y/N] no one is there to answer.
func noTTY(t *testing.T) {
	t.Helper()
	prev := openTTY
	openTTY = func() (*os.File, error) { return nil, errors.New("no tty in tests") }
	t.Cleanup(func() { openTTY = prev })
}

// setConfig writes the sole config, named "proj". base_branch and branch_prefix
// are always included so callers only supply what they are varying.
func (f *fixture) setConfig(body string) {
	f.t.Helper()
	f.writeConfig("base_branch = 'main'\nbranch_prefix = '" + gittest.BranchPrefix + "'\n" + body)
}

// writeConfig writes the sole config verbatim, for the tests whose subject is one
// of the keys setConfig supplies itself.
func (f *fixture) writeConfig(body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.registry, "proj.toml"), []byte(body), 0o644); err != nil {
		f.t.Fatalf("write config: %v", err)
	}
}

// newPrefixFixture is newFixture for a repo that namespaces branches by kind of
// work. It writes the config itself because the standard fixture always sets the
// singular branch_prefix, and no config may set both spellings.
func newPrefixFixture(t *testing.T, prefixes ...string) *fixture {
	t.Helper()
	f := newFixture(t, "")
	quoted := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		quoted = append(quoted, strconv.Quote(p))
	}
	f.writeConfig("main_dir = '" + f.MainDir + "'\nbase_branch = 'main'\n" +
		"branch_prefixes = [" + strings.Join(quoted, ", ") + "]\n")
	return f
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
func (f *fixture) runWithEvalFile(args ...string) (shellCommands string) {
	f.t.Helper()
	path := filepath.Join(f.Root, "evalfile")
	_ = os.Remove(path)

	var out bytes.Buffer
	if err := Run(Env{Args: args, Stdout: &out, Stderr: &out, EvalFile: path}); err != nil {
		f.t.Fatalf("treewright %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	written, err := os.ReadFile(path)
	if err != nil {
		return "" // nothing written is a valid outcome
	}
	return string(written)
}

// statusOf reads one slug's status out of the `ls` table.
func (f *fixture) statusOf(slug string) string {
	f.t.Helper()
	for line := range strings.SplitSeq(f.mustRun("ls"), "\n") {
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
		for line := range strings.SplitSeq(f.mustRun("ls"), "\n") {
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
		for line := range strings.SplitSeq(f.mustRun("ls"), "\n") {
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
		for line := range strings.SplitSeq(f.mustRun("ls", "proj"), "\n") {
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

// TestNewWarnsWhenTheBaseCheckoutIsAheadOfOrigin covers the gap between where a
// branch forks from and where the user has been working: commits made in the
// main checkout and not pushed are invisible to a worktree forked from origin,
// so the files they added are simply not there. Discovering that from an empty
// directory three steps into the work is what the warning is for.
func TestNewWarnsWhenTheBaseCheckoutIsAheadOfOrigin(t *testing.T) {
	f := newFixture(t, "")
	f.Commit(f.MainDir, "groundwork nobody has pushed")

	r := f.exec("new", "alpha")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(flat(r.stderr), "main has 1 commit not on origin/main") {
		t.Errorf("stderr = %q, want the unpushed base commits counted", r.stderr)
	}
	// The count alone would leave the reader to work out why it matters here,
	// and knowing why is no use without the two ways out.
	if !strings.Contains(flat(r.stderr), "so that work is not in it") {
		t.Errorf("stderr = %q, want what it means for the worktree just made", r.stderr)
	}
	if !strings.Contains(flat(r.stderr), "git cherry-pick origin/main..main") {
		t.Errorf("stderr = %q, want the command that brings the commits over", r.stderr)
	}
	// Warnings are not the answer: cd "$(treewright new alpha)" still gets a path.
	if strings.TrimSpace(r.stdout) != f.DirFor("alpha") {
		t.Errorf("stdout = %q, want the worktree path alone", r.stdout)
	}

	// Pushed, there is nothing to say — and nothing is said, since a warning
	// that appears whatever the state is one nobody reads. The error is checked
	// first: a `new beta` that failed outright would also print no warning, and
	// the absence check would pass about a worktree that was never made.
	f.Push(f.MainDir, "main")
	r = f.exec("new", "beta")
	if r.err != nil {
		t.Fatalf("new beta: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "not on origin/main") {
		t.Errorf("stderr = %q, want silence once the base branch is pushed", r.stderr)
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

// ---- new, under several branch prefixes ------------------------------------

// TestNewPicksTheBranchPrefixTheSlugNames is what branch_prefixes is for: a team
// that namespaces by kind of work chooses the namespace by typing it, and the
// worktree is still named after the work.
func TestNewPicksTheBranchPrefixTheSlugNames(t *testing.T) {
	f := newPrefixFixture(t, "feature/", "bug/")

	out := f.mustRun("new", "bug/eng-1")
	if !strings.Contains(out, "creating branch bug/eng-1 off origin/main") {
		t.Errorf("output = %q, want the named prefix in the branch", out)
	}
	if !f.Exists("eng-1") {
		t.Fatalf("worktree %s was not created\n%s", f.DirFor("eng-1"), out)
	}
	if got := f.Git(f.DirFor("eng-1"), "branch", "--show-current"); got != "bug/eng-1" {
		t.Errorf("branch = %q, want bug/eng-1", got)
	}
	// The prefix reached the branch and stopped there: the slug is what the table
	// lists and what the user types back at resume and rm.
	if got := f.statusOf("eng-1"); got == "" {
		t.Errorf("ls does not list eng-1 by its slug:\n%s", f.mustRun("ls"))
	}
}

func TestNewFallsBackToTheFirstBranchPrefix(t *testing.T) {
	f := newPrefixFixture(t, "feature/", "bug/")

	f.mustRun("new", "eng-2")
	if got := f.Git(f.DirFor("eng-2"), "branch", "--show-current"); got != "feature/eng-2" {
		t.Errorf("branch = %q, want feature/eng-2 — a bare slug takes the first prefix", got)
	}
}

// TestNewRejectsAnUnconfiguredBranchPrefix guards the case where guessing would
// cost the most: a misspelled prefix pushed as a namespace of its own is outside
// whatever the team's tooling watches, and nothing about it looks wrong locally.
func TestNewRejectsAnUnconfiguredBranchPrefix(t *testing.T) {
	f := newPrefixFixture(t, "feature/", "bug/")

	out, err := f.run("new", "feat/eng-3")
	if err == nil {
		t.Fatal("want an error for a prefix that is not configured")
	}
	if !errors.Is(err, ErrUsage) {
		t.Errorf("err = %v, want ErrUsage (exit 2)", err)
	}
	// The one that was typed, and the ones that would have worked.
	for _, want := range []string{`"feat/"`, `"feature/"`, `"bug/"`} {
		if combined := out + err.Error(); !strings.Contains(combined, want) {
			t.Errorf("error = %q, want it to name %s", combined, want)
		}
	}
	if f.Exists("eng-3") {
		t.Error("a worktree was created under a refused prefix")
	}
}

// TestTheLongestBranchPrefixWins pins that list order does not decide between two
// nested prefixes. The more specific one is listed second here, where a
// first-match loop would never reach it.
func TestTheLongestBranchPrefixWins(t *testing.T) {
	f := newPrefixFixture(t, "feature/", "feature/exp/")

	f.mustRun("new", "feature/exp/eng-4")
	if !f.Exists("eng-4") {
		t.Fatalf("worktree %s was not created", f.DirFor("eng-4"))
	}
	if got := f.Git(f.DirFor("eng-4"), "branch", "--show-current"); got != "feature/exp/eng-4" {
		t.Errorf("branch = %q, want feature/exp/eng-4", got)
	}
}

// TestAPrefixedNameStillFindsTheWorktree: worktrees are named by slug, so the
// branch name — what you read off `git branch` or a pull request — has to resolve
// to one as well.
func TestAPrefixedNameStillFindsTheWorktree(t *testing.T) {
	f := newPrefixFixture(t, "feature/", "bug/")
	f.mustRun("new", "bug/eng-5")

	out := f.mustRun("cd", "bug/eng-5")
	if !strings.Contains(out, f.DirFor("eng-5")) {
		t.Errorf("output = %q, want the path of %s", out, f.DirFor("eng-5"))
	}
	if !strings.Contains(out, `reads as branch prefix "bug/" and slug "eng-5"`) {
		t.Errorf("output = %q, want the split reported", out)
	}
}

// TestCompletionOffersTheBranchPrefixes covers the only place the configured set
// is discoverable without opening the config file.
func TestCompletionOffersTheBranchPrefixes(t *testing.T) {
	t.Run("several are a choice worth offering", func(t *testing.T) {
		f := newPrefixFixture(t, "feature/", "bug/")
		if got := f.mustRun("__complete", "prefixes"); got != "feature/\nbug/\n" {
			t.Errorf("candidates = %q, want the configured prefixes in order", got)
		}
	})

	t.Run("one is not", func(t *testing.T) {
		// Offering the single prefix would put a word in front of every new slug
		// that the user never has to type.
		f := newFixture(t, "")
		if got := f.mustRun("__complete", "prefixes"); got != "" {
			t.Errorf("candidates = %q, want nothing offered", got)
		}
	})
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

// TestNewRunsPostCreateCommandsInOrder covers the list spelling of the setting.
// Order is the whole point of it — a generate step reads what an install step
// wrote — so the commands here would produce different content if they were run
// concurrently or in the order the shell happened to reach them.
func TestNewRunsPostCreateCommandsInOrder(t *testing.T) {
	f := newFixture(t, "post_create = ['echo one >> steps', 'echo two >> steps', 'echo three >> steps']\n")

	out := f.mustRun("new", "hooked")
	if !strings.Contains(out, "3 post_create commands") {
		t.Errorf("output = %q, want the number of commands reported", out)
	}

	steps := filepath.Join(f.DirFor("hooked"), "steps")
	waitForContent(t, steps, "three", "post_create")
	body, err := os.ReadFile(steps)
	if err != nil {
		t.Fatalf("read the steps file: %v", err)
	}
	if got := string(body); got != "one\ntwo\nthree\n" {
		t.Errorf("steps = %q, want the commands run in the configured order", got)
	}
}

// TestPostCreateStopsAtTheFirstFailure keeps a broken install from being followed
// by every step that assumed it worked, which turns one failure into a log full of
// unrelated ones and buries the cause.
// The middle command exits rather than merely failing, which is the case a flat
// script gets wrong: an `exit` there ends the run with no failure reported at all.
func TestPostCreateStopsAtTheFirstFailure(t *testing.T) {
	f := newFixture(t, "post_create = ['echo first > ran', 'exit 3', 'echo third > never']\n")

	f.mustRun("new", "broken")
	log := filepath.Join(f.MainDir, ".git", "treewright", "post-create-broken.log")
	waitForContent(t, log, "post_create stopped", "post_create")

	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	// The log has to name the command that failed: the reader is looking at a
	// truncated sequence and cannot otherwise tell a stop from a run still going.
	if !strings.Contains(string(body), "post_create stopped: exit 3 failed") {
		t.Errorf("log = %q, want the failing command named", body)
	}
	// Each step announces itself, so the log reads as the terminal session it stands in for.
	if !strings.Contains(string(body), "$ echo first > ran") {
		t.Errorf("log = %q, want each command echoed as it runs", body)
	}
	if _, err := os.Stat(filepath.Join(f.DirFor("broken"), "never")); err == nil {
		t.Error("a command after the failing one ran anyway")
	}
	if _, err := os.Stat(filepath.Join(f.DirFor("broken"), "ran")); err != nil {
		t.Error("the command before the failing one did not run")
	}
}

// TestAFailedPostCreateIsReportedLater is what the marker file is for. Nothing
// waits for post_create, so treewright has exited by the time it fails and has
// nowhere to say so; without this, a half-installed worktree announces itself as a
// build breaking for reasons that look nothing like a missing install.
func TestAFailedPostCreateIsReportedLater(t *testing.T) {
	f := newFixture(t, "post_create = ['echo nope >&2', 'exit 1']\n")

	f.mustRun("new", "broken")
	_, failed := postCreatePaths(&config.Config{MainDir: f.MainDir}, "broken")
	waitForFile(t, failed, "post_create")

	// Every command that reaches a worktree says it, because which one a user runs
	// next is not knowable — and none of them may put it on stdout.
	for _, args := range [][]string{{"ls"}, {"ls", "--json"}, {"cd", "broken"}, {"resume", "broken"}} {
		// resume opens a window and so fails where tmux is missing; the warning
		// comes first either way, which is the whole assertion here.
		r := f.exec(args...)
		if !strings.Contains(flat(r.stderr), "post_create failed in broken failed step exit 1") {
			t.Errorf("%v stderr = %q, want the failed setup reported", args, r.stderr)
		}
		if strings.Contains(r.stdout, "post_create") {
			t.Errorf("%v put the warning on stdout: %q", args, r.stdout)
		}
	}
	// The listing is still machine-readable — the warning is on the other stream.
	var rows []map[string]any
	if err := json.Unmarshal([]byte(f.exec("ls", "--json").stdout), &rows); err != nil {
		t.Errorf("ls --json: %v", err)
	}

	// A slug is reusable once its worktree is gone, and the next worktree under it
	// has not failed at anything.
	f.mustRun("rm", "broken", "--force", "--yes")
	f.writeConfig("main_dir = '" + f.MainDir + "'\n")
	f.mustRun("new", "broken")
	if got := f.exec("ls").stderr; strings.Contains(got, "post_create") {
		t.Errorf("stderr = %q, want no failure carried over from the removed worktree", got)
	}
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

// TestRmInARepositoryWithNoWorktreesPointsAtNew pins the empty half of the
// not-found error. The list form ended in "have:" with silence under it —
// asLines contributes nothing for an empty slice, which is right for a list and
// exactly wrong for a line that promised one — and what the reader needs is not
// the empty set but the command that starts a first worktree.
func TestRmInARepositoryWithNoWorktreesPointsAtNew(t *testing.T) {
	f := newFixture(t, "")

	out, err := f.run("rm", "ghost")
	if err == nil {
		t.Fatal("want an error for a worktree that does not exist")
	}
	msg := out + err.Error()
	if strings.Contains(msg, "have:") {
		t.Errorf("error = %q, want no dangling have: with nothing under it", msg)
	}
	if !strings.Contains(msg, "no worktrees at all") {
		t.Errorf("error = %q, want the empty repository named as the state", msg)
	}
	if !strings.Contains(flat(msg), "new ghost") {
		t.Errorf("error = %q, want new named as the way to a first worktree", msg)
	}
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

	commands := f.runWithEvalFile("rm", "--force", "here")

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
	commands := f.runWithEvalFile("rm", "--force", "there")
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

	commands := f.runWithEvalFile("prune", "-y")

	want := "cd '" + f.MainDir + "'"
	if !strings.Contains(commands, want) {
		t.Errorf("shell commands = %q, want %q", commands, want)
	}
	if f.Exists("reaped") {
		t.Error("worktree was not pruned")
	}
}

// TestPruneLeavesTheWindowOfAWorktreeItCouldNotRemove is the regression test
// for a --yes prune offering every target's window for closing, removed or not.
// A locked worktree's removal fails, the directory still exists, and an agent
// may still be working in its window — the one window of the batch nobody
// should be invited to kill, and the one the old loop still offered.
func TestPruneLeavesTheWindowOfAWorktreeItCouldNotRemove(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	noTTY(t)

	f.mustRun("new", "reapable") // both merged and clean, so both are targets
	f.mustRun("new", "wedged")
	f.Git(f.MainDir, "worktree", "lock", f.DirFor("wedged"))

	r := f.exec("prune", "-y")
	if r.err != nil {
		t.Fatalf("prune: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "could not remove wedged") {
		t.Fatalf("stderr = %q, want the failed removal reported", r.stderr)
	}
	// The removed worktree's window is the one now pointing at nothing, and the
	// only one whose closing should be brought up.
	if !strings.Contains(r.stderr, "tmux window REAPABLE now points at a deleted directory") {
		t.Errorf("stderr = %q, want the removed worktree's window offered", r.stderr)
	}
	if strings.Contains(r.stderr, "tmux window WEDGED") {
		t.Errorf("stderr offers the locked worktree's live window for closing:\n%s", r.stderr)
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
		if !strings.Contains(out, `leaving "alpha"`) {
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

// TestResumeStartsAFreshAgentWhenThereIsNothingToContinue is the way back into a
// worktree whose first window never opened, which used to be a worktree nothing
// could open.
//
// resume_command is "carry on where I left off" — `claude --continue` and its
// like — and a worktree that has never had an agent in it has nothing to carry
// on from, so it exited on saying so and the held-open wrapper parked the window
// on the error. `new` refused the slug and named `resume` as the way in: the one
// command that could not work. Removing the worktree was the only way out.
//
// The recovery is the failure itself rather than a prediction of it, so this
// test says nothing about how the worktree got into that state: resume_command
// exits at once, and command is what ends up running.
func TestResumeStartsAFreshAgentWhenThereIsNothingToContinue(t *testing.T) {
	requireTmux(t)
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh")
	f := newFixture(t, "command = 'touch "+fresh+"'\n"+
		"resume_command = 'echo no conversation found to continue >&2; exit 1'\n")

	f.mustRun("new", "solo")
	waitForFile(t, fresh, "new's command")
	waitForNoPanes(t, f.DirFor("solo"))
	if err := os.Remove(fresh); err != nil {
		t.Fatalf("clear the marker new left: %v", err)
	}

	f.mustRun("resume", "solo")
	waitForFile(t, fresh, "command, run after a resume_command that never got going")
	// And the window is gone with it: the fresh command succeeded, so nothing is
	// held open over an error the fallback has already dealt with.
	waitForNoPanes(t, f.DirFor("solo"))
}

// TestResumeFreshSkipsTheResumeCommand covers the manual form of the same
// request: --fresh runs command whether or not there is anything to continue,
// which is what a user reaches for when the session they would be resuming is
// one they would rather leave behind.
func TestResumeFreshSkipsTheResumeCommand(t *testing.T) {
	requireTmux(t)
	dir := t.TempDir()
	fresh, resumed := filepath.Join(dir, "fresh"), filepath.Join(dir, "resumed")
	f := newFixture(t, "command = 'touch "+fresh+"'\nresume_command = 'touch "+resumed+"'\n")

	f.mustRun("new", "solo")
	waitForFile(t, fresh, "new's command")
	waitForNoPanes(t, f.DirFor("solo"))
	if err := os.Remove(fresh); err != nil {
		t.Fatalf("clear the marker new left: %v", err)
	}

	f.mustRun("resume", "--fresh", "solo")
	waitForFile(t, fresh, "command under --fresh")
	if _, err := os.Stat(resumed); err == nil {
		t.Error("resume_command ran under --fresh")
	}
}

// TestTheBaseRowGetsTheFallbackToo is the half of this a marker file could never
// reach. The base checkout is not a worktree treewright made, so there was never
// a moment at which anything could record that no agent had run in it — and it
// is a row in every picker, so a base window whose agent left no session behind
// dead-ended exactly as a worktree did.
func TestTheBaseRowGetsTheFallbackToo(t *testing.T) {
	requireTmux(t)
	fresh := filepath.Join(t.TempDir(), "fresh")
	f := newFixture(t, "command = 'touch "+fresh+"'\n"+
		"resume_command = 'echo no conversation found to continue >&2; exit 1'\n")

	f.mustRun("resume", "base")
	waitForFile(t, fresh, "command, run in the base window after a resume that found nothing")
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
		if !strings.Contains(out, "new [-p <text>] <slug>") {
			t.Errorf("output = %q, want the command overview", out)
		}
	})

	t.Run("help exits zero", func(t *testing.T) {
		out, err := f.run("--help")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "resume [-p <text>] [--fresh] [slug]") {
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

// TestLsJSONCarriesTheBaseCheckoutWithNoWorktrees is where the two modes part
// company. The table prints nothing, because "no worktrees" is the whole of
// what a person wants to hear; the JSON keeps its shape, because a schema whose
// first row appears only sometimes makes every consumer carry the special case
// — and because an empty array was read as "this repository is not registered",
// which is a state with an answer of its own.
func TestLsJSONCarriesTheBaseCheckoutWithNoWorktrees(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("ls", "--json")
	if r.err != nil {
		t.Fatalf("ls --json: %v", r.err)
	}
	var rows []struct {
		Base   bool   `json:"base"`
		Slug   string `json:"slug"`
		Dir    string `json:"dir"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%q", err, r.stdout)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the base checkout alone: %s", len(rows), r.stdout)
	}
	if row := rows[0]; !row.Base || row.Status != "base" || row.Slug != "" || row.Dir != f.MainDir {
		t.Errorf("row = %+v, want the main checkout flagged and unslugged", row)
	}

	// The table is unchanged: still nothing on stdout, still the explanation on
	// the other stream.
	if plain := f.exec("ls"); plain.stdout != "" {
		t.Errorf("ls stdout = %q, want the table to stay empty", plain.stdout)
	}

	// And the state the empty array used to be mistaken for is still entirely
	// different: an unregistered repository is an error naming what it looked in.
	t.Setenv("TREEWRIGHT_CONFIG_DIR", t.TempDir())
	if r := f.exec("ls", "--json"); r.err == nil {
		t.Errorf("an unregistered repo answered with %q, want an error", r.stdout)
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
	// Unpushed in the main checkout too, which is what makes the next new warn
	// that its worktree does not have it: a message that only appears in one
	// state is one the voice check only sees if the state is arranged for.
	f.Commit(f.MainDir, "unpushed groundwork")

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
			for line := range strings.SplitSeq(text, "\n") {
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
			for name := range strings.SplitSeq(doc.names, ",") {
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
