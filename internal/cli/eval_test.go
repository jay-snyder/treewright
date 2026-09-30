package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/shellinit"
	"github.com/jay-snyder/treewright/internal/testenv"
)

// TestACdLineLandsEveryShellInTheDirectoryItNames holds the eval file's one rule
// where the file is read. moveShell writes the line, and zsh, bash and fish each
// source it as their wrapper does and say where they ended up. The directories
// are real ones rather than a quoted word printed back, because what a misread
// costs is the cd.
//
// The names are the ones the shells disagree about. Inside single quotes fish
// takes \\ and \' as escapes, where zsh and bash take every character
// literally. A lone backslash, a doubled one, one against a quote and one at
// the very end are each a separate way for the two readings to part. The
// POSIX-only quoting this replaced sent fish looking for a directory with one
// backslash in its name on the second, and wrote it a line it could not parse
// on the last two.
func TestACdLineLandsEveryShellInTheDirectoryItNames(t *testing.T) {
	names := []string{
		"with spaces",
		"with'quote",
		`with\backslash`,
		`with\\two`,
		`with\'both`,
		`ends\`,
		"with$dollar and `tick`",
	}
	for _, shell := range []string{"zsh", "bash", "fish"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				testenv.Unavailablef(t, "%s is not installed", shell)
			}
			for _, name := range names {
				// Resolved, so the directory the shell reports is the one made
				// here rather than the same one through macOS's /var symlink.
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatalf("resolve temp dir: %v", err)
				}
				dir := filepath.Join(root, name)
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatalf("mkdir %q: %v", name, err)
				}
				var stderr strings.Builder
				evalFile := filepath.Join(root, "evalfile")
				moveShell(&Env{EvalFile: evalFile, Stderr: &stderr}, dir, "your shell did not move")
				if stderr.Len() != 0 {
					t.Fatalf("moveShell reported instead of writing the line:\n%s", stderr.String())
				}

				cmd := exec.Command(bin, "-c", "source "+evalFile+"\npwd")
				cmd.Env = testenv.ShellEnv(root)
				var out, errOut strings.Builder
				cmd.Stdout, cmd.Stderr = &out, &errOut
				// Stderr rather than the exit status alone, because a cd that
				// fails is followed by a pwd that does not.
				if err := cmd.Run(); err != nil {
					fmt.Fprintln(&errOut, err)
				}
				if errOut.Len() != 0 {
					line, _ := os.ReadFile(evalFile)
					t.Errorf("%s could not follow %q:\n%s", shell, line, errOut.String())
					continue
				}
				if got := strings.TrimSuffix(out.String(), "\n"); got != dir {
					t.Errorf("%s ended up in %q, want %q", shell, got, dir)
				}
			}
		})
	}
}

func TestAppendEvalAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evalfile")

	if err := appendEval(path, "cd '/one'"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := appendEval(path, "cd '/two'"); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "cd '/one'\ncd '/two'\n" {
		t.Errorf("eval file = %q, want both commands on their own lines", got)
	}
}

// TestMoveShellWithoutIntegrationSaysWhatToRun covers the invariant every
// caller depends on: run straight from a shell with no wrapper loaded, nothing
// is written anywhere and the by-hand line is printed instead.
//
// "Nothing" is asserted rather than assumed, in an empty directory the test then
// reads back. An earlier version of this test called the emitter and checked
// nothing at all, so it passed whatever happened — including the case it was named
// for, a stray file left behind for someone to wonder about later.
func TestMoveShellWithoutIntegrationSaysWhatToRun(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var stderr strings.Builder
	env := &Env{EvalFile: "", Stderr: &stderr}

	moveShell(env, "/somewhere", "your shell did not move")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read back the working directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("with no eval file configured, moveShell wrote %v", names)
	}
	if got := stderr.String(); !strings.Contains(got, "cd /somewhere") {
		t.Errorf("stderr = %q, want the by-hand cd line", got)
	}
}

// TestMoveShellReportsAnUnwritableEvalFile pins the failure that used to have
// no report path: the integration is loaded — $TREEWRIGHT_EVAL_FILE is set —
// but the file cannot be written, as when a tmpdir has been swept. The shell
// will not move, so the caller has to hear why and see the line to type.
func TestMoveShellReportsAnUnwritableEvalFile(t *testing.T) {
	var stderr strings.Builder
	env := &Env{
		EvalFile: filepath.Join(t.TempDir(), "swept", "gone", "evalfile"),
		Stderr:   &stderr,
	}

	moveShell(env, "/somewhere", "your shell did not move")

	got := stderr.String()
	if !strings.Contains(got, "warning:") || !strings.Contains(got, "could not be written") {
		t.Errorf("stderr = %q, want a warning that the eval file could not be written", got)
	}
	if !strings.Contains(got, "cd /somewhere") {
		t.Errorf("stderr = %q, want the by-hand cd line", got)
	}
}

// TestReloadShellWithoutIntegrationSaysWhatToRun is moveShell's invariant held
// for reloadShell. refresh never calls it without an eval file, but the
// by-hand line lives beside the emit so that no caller has to remember it.
func TestReloadShellWithoutIntegrationSaysWhatToRun(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(shellinit.ShellVar, "")
	t.Setenv("SHELL", "/bin/zsh")
	var stderr strings.Builder
	env := &Env{EvalFile: "", Stderr: &stderr, Argv0: "tw"}

	reloadShell(env)

	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("with no eval file configured, reloadShell wrote %v (%v)", entries, err)
	}
	got := flat(stderr.String())
	if !strings.Contains(got, "the shell wrapper was not reloaded") ||
		!strings.Contains(got, `run eval "$(treewright shell-init zsh)"`) {
		t.Errorf("stderr = %q, want what did not happen and the by-hand line", got)
	}
}

func TestInsideDir(t *testing.T) {
	tests := []struct {
		path, dir string
		want      bool
	}{
		{"/a/b", "/a/b", true},        // the directory itself counts
		{"/a/b/c", "/a/b", true},      // below it
		{"/a/b/c/d", "/a/b", true},    // further below
		{"/a/c", "/a/b", false},       // sibling
		{"/a", "/a/b", false},         // parent
		{"/a/bb", "/a/b", false},      // prefix match that is not a child
		{"/other/a/b", "/a/b", false}, // unrelated

		// A name that merely begins with ".." is an ordinary child. Testing for
		// a ".." prefix rather than a whole ".." element read these as escaping
		// the directory, so a shell sitting in one would not be rescued.
		{"/a/b/..config", "/a/b", true},
		{"/a/b/..", "/a/b", false},
		{"/a/b/../c", "/a/b", false},
	}
	for _, tc := range tests {
		if got := insideDir(tc.path, tc.dir); got != tc.want {
			t.Errorf("insideDir(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}
