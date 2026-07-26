package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellQuoteSurvivesEveryShell(t *testing.T) {
	// The quoted string is sourced by the user's shell, so a path containing a
	// quote or a space must come back out byte-identical. Each case is checked
	// against real shells rather than against an expected spelling.
	paths := []string{
		"/simple/path",
		"/path with spaces/repo",
		"/path/with'quote/repo",
		"/path/with''double-quotes/repo",
		`/path/with\backslash/repo`,
		"/path/with$dollar/repo",
		"/path/with`backtick/repo",
		"/path/with;semicolon/repo",
		"/path/with*glob/repo",
	}

	shells := []string{"sh", "bash", "zsh"}
	for _, shell := range shells {
		bin, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("skipping %s: not installed", shell)
			continue
		}
		for _, path := range paths {
			t.Run(shell+" "+path, func(t *testing.T) {
				// printf %s of the quoted value must reproduce the input.
				script := "printf %s " + shellQuote(path)
				out, err := exec.Command(bin, "-c", script).Output()
				if err != nil {
					t.Fatalf("%s: %v", shell, err)
				}
				if string(out) != path {
					t.Errorf("%s round-trip = %q, want %q", shell, out, path)
				}
			})
		}
	}
}

func TestEmitEvalAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "evalfile")
	env := &Env{EvalFile: path}

	emitEval(env, "cd '/one'")
	emitEval(env, "cd '/two'")

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "cd '/one'\ncd '/two'\n" {
		t.Errorf("eval file = %q, want both commands on their own lines", got)
	}
}

// TestEmitEvalWithoutIntegrationIsANoOp covers the invariant every caller depends
// on: run straight from a shell with no wrapper loaded, emitEval does nothing.
//
// "Nothing" is asserted rather than assumed, in an empty directory the test then
// reads back. The previous version of this test called emitEval and checked
// nothing at all, so it passed whatever happened — including the case it was named
// for, a stray file left behind for someone to wonder about later.
func TestEmitEvalWithoutIntegrationIsANoOp(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	emitEval(&Env{EvalFile: ""}, "cd '/nowhere'")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read back the working directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("with no eval file configured, emitEval wrote %v", names)
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

func TestShellQuoteIsSingleQuoted(t *testing.T) {
	// A sanity check on the shape, independent of any shell being installed.
	got := shellQuote("/plain")
	if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
		t.Errorf("shellQuote(%q) = %q, want it wrapped in single quotes", "/plain", got)
	}
}
