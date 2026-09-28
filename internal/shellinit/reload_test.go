package shellinit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/testenv"
)

// What Reload is held to: sourced from inside the wrapper it replaces, in each
// of the three shells, it has to leave that shell running this binary's shim
// and nothing else changed — no error, no stray file, no exit status lost, and
// nothing run by the two shims that were not for it.

// olderWrapper is a wrapper of the kind an older treewright emitted, cut down to
// what a reload has to survive: a function that sources the eval file from
// inside itself and then carries on, and a tw that calls it. It says which
// body it finished on, which is the fact under test.
//
// Written out rather than taken from the current shim, because the wrappers
// in people's shells are older ones, and the protocol they share is all the
// reload may rely on. The current shim reloading itself is the other case in
// the table below.
var olderWrapper = map[string]string{
	"zsh":  posixOlderWrapper,
	"bash": posixOlderWrapper,
	"fish": `set -gx TREEWRIGHT_SHELL_INIT_VERSION 000000000000
function treewright
    set -l evalfile (command mktemp $TMPDIR/treewright-eval.XXXXXX)
    or return 1
    set -lx TREEWRIGHT_EVAL_FILE $evalfile
    command treewright $argv
    set -l saved $status
    if test -s $evalfile
        source $evalfile
    end
    command rm -f $evalfile
    echo "finished on the older wrapper"
    return $saved
end
function tw --wraps treewright
    set -lx TREEWRIGHT_ARGV0 tw
    treewright $argv
end
`,
}

const posixOlderWrapper = `export TREEWRIGHT_SHELL_INIT_VERSION="000000000000"
treewright() {
  local evalfile rc
  evalfile="$(command mktemp "${TMPDIR:-/tmp}/treewright-eval.XXXXXX")" || return 1
  TREEWRIGHT_EVAL_FILE="$evalfile" command treewright "$@"
  rc=$?
  [[ -s "$evalfile" ]] && source "$evalfile"
  command rm -f "$evalfile"
  echo "finished on the older wrapper"
  return $rc
}
tw() { local -x TREEWRIGHT_ARGV0=tw; treewright "$@"; }
`

// TestAReloadReplacesTheWrapperThatSourcesIt drives the one mechanic that could
// have gone wrong: a function sourcing a redefinition of itself while it runs.
// The call doing it has to finish on the body it started with — its own
// cleanup, its own return — and the next call has to get the new one.
//
// The stub binary exits 3 so that a lost status shows, and writes the reload
// on every call, so the second call is also the current shim sourcing a
// reload of itself. Under `set -u` a guard that named another shell's
// variable would stop bash halfway through the file and skip the cleanup, and
// under zsh's nounset it would print an error, so both options get a run.
func TestAReloadReplacesTheWrapperThatSourcesIt(t *testing.T) {
	cases := []struct {
		shell, options string
		older          bool
	}{
		{"zsh", "", true},
		{"zsh", "setopt nounset", true},
		{"bash", "", true},
		{"bash", "set -u", true},
		{"fish", "", true},
		{"zsh", "", false},
		{"bash", "", false},
		{"fish", "", false},
	}
	for _, tc := range cases {
		name := tc.shell
		if tc.options != "" {
			name += " with " + tc.options
		}
		if !tc.older {
			name += " reloading itself"
		}
		t.Run(name, func(t *testing.T) {
			bin := requireShell(t, tc.shell)
			dir := t.TempDir()

			wrapper := olderWrapper[tc.shell]
			if !tc.older {
				script, err := Script(tc.shell)
				if err != nil {
					t.Fatalf("Script: %v", err)
				}
				wrapper = script
			}
			loaded := writeFile(t, dir, "wrapper", wrapper, 0o644)
			reload := writeFile(t, dir, "reload", Reload()+"\n", 0o644)
			stubTreewright(t, dir, "cat "+reload+` >> "$TREEWRIGHT_EVAL_FILE"`+"\nexit 3\n")

			status := "$?"
			if tc.shell == "fish" {
				status = "$status"
			}
			program := tc.options + "\n" +
				"source " + loaded + "\n" +
				"tw refresh; echo rc=" + status + "\n" +
				"tw refresh; echo rc=" + status + "\n" +
				"echo version=$TREEWRIGHT_SHELL_INIT_VERSION\n" +
				"echo shell=$TREEWRIGHT_SHELL_INIT_SHELL\n"
			stdout, stderr := runShell(t, bin, dir, program)

			version, err := Version(tc.shell)
			if err != nil {
				t.Fatalf("Version: %v", err)
			}
			want := fmt.Sprintf("rc=3\nrc=3\nversion=%s\nshell=%s\n", version, tc.shell)
			if tc.older {
				// Once: the first call finishes on the older body, and the second
				// is the new wrapper's, which says nothing.
				want = "finished on the older wrapper\n" + want
			}
			if stdout != want {
				t.Errorf("%s after a reload:\n%s\nwant:\n%s", tc.shell, stdout, want)
			}
			// The whole of what the other two shims could do to this shell, if
			// either one's guard let it through, is fail to parse or run here.
			if stderr != "" {
				t.Errorf("%s printed while reloading:\n%s", tc.shell, stderr)
			}
			if left := leftoverEvalFiles(t, dir); len(left) > 0 {
				t.Errorf("the wrapper's cleanup did not run, leaving %v", left)
			}
		})
	}
}

// TestQuotingReadsTheSameInEveryShell is what lets Reload hand three shells one
// file. Each reads the other two shims as quoted strings, so the quoting has
// to tokenize the same in all of them, and each has to get its own back byte
// for byte. The string is built from what the three disagree about, not from
// what today's shims contain.
func TestQuotingReadsTheSameInEveryShell(t *testing.T) {
	const tricky = `plain 'single' "double" \ \\ \' \\' $HOME ${HOME-} ` + "`tick` * ? {a,b} ! #\nsecond line\n"
	for _, shell := range []string{"zsh", "bash", "fish"} {
		t.Run(shell, func(t *testing.T) {
			bin := requireShell(t, shell)
			stdout, stderr := runShell(t, bin, t.TempDir(), "printf '%s' "+quote(tricky))
			if stdout != tricky || stderr != "" {
				t.Errorf("%s read the quoted text back as %q (stderr %q), want %q", shell, stdout, stderr, tricky)
			}
		})
	}
}

// TestEveryShimIsInTheReloadBehindItsOwnGuard holds Reload to the list of
// shells without needing any of them installed. A shell added to the shims
// table with no guard would go into the file unguarded, and every shell would
// run it.
func TestEveryShimIsInTheReloadBehindItsOwnGuard(t *testing.T) {
	reload := Reload()
	for _, shell := range Shells() {
		s := shims[shell]
		if s.onlyIn == "" {
			t.Errorf("%s has no onlyIn test, so Reload cannot keep its shim out of the other shells", shell)
			continue
		}
		if !strings.Contains(reload, s.onlyIn+" && eval "+quote(render(s.script))) {
			t.Errorf("the reload does not carry the %s shim behind its guard", shell)
		}
	}
	if lines := strings.Count(reload, " && eval '"); lines < len(Shells()) {
		t.Errorf("the reload has %d guarded evals, want one per shell", lines)
	}
}

// TestEveryScriptExportsWhichShellItIsFor: a stale fingerprint cannot say
// which shell it came from, and $SHELL answers for the login shell, so this is
// how refresh knows which line to print for the shells it cannot reach.
func TestEveryScriptExportsWhichShellItIsFor(t *testing.T) {
	exports := map[string]string{
		"zsh":  "\nexport " + ShellVar + "=zsh\n",
		"bash": "\nexport " + ShellVar + "=bash\n",
		"fish": "\nset -gx " + ShellVar + " fish\n",
	}
	for _, shell := range Shells() {
		script, err := Script(shell)
		if err != nil {
			t.Fatalf("Script(%q): %v", shell, err)
		}
		if !strings.Contains(script, exports[shell]) {
			t.Errorf("the %s script does not export %s as %q", shell, ShellVar, shell)
		}
	}
}

// TestEveryShellHasTheLineItsScriptSaysItLoadsWith ties LoadLine to the shims'
// own first line, which is where a reader learns it. The two are how a person
// and refresh each spell one line.
func TestEveryShellHasTheLineItsScriptSaysItLoadsWith(t *testing.T) {
	for _, shell := range Shells() {
		line, ok := LoadLine(shell)
		if !ok {
			t.Errorf("LoadLine knows no line for %s", shell)
			continue
		}
		script, err := Script(shell)
		if err != nil {
			t.Fatalf("Script(%q): %v", shell, err)
		}
		first, _, _ := strings.Cut(script, "\n")
		if !strings.HasSuffix(first, "Load with: "+line) {
			t.Errorf("the %s script opens with %q, which does not name LoadLine's %q", shell, first, line)
		}
	}
	if _, ok := LoadLine("nushell"); ok {
		t.Error("LoadLine offers a line for a shell treewright has no shim for")
	}
}

// TestSourcingTheFishShimAgainAddsNoCompletions: fish keeps every completion it
// is given rather than replacing one, so a shim sourced a second time — which a
// reload is — would double every entry, running each __complete once per copy.
func TestSourcingTheFishShimAgainAddsNoCompletions(t *testing.T) {
	bin := requireShell(t, "fish")
	script, err := Script("fish")
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	dir := t.TempDir()
	shim := writeFile(t, dir, "shim", script, 0o644)
	stdout, stderr := runShell(t, bin, dir,
		"source "+shim+"\nset -l once (complete -c treewright | count)\n"+
			"source "+shim+"\nset -l twice (complete -c treewright | count)\n"+
			"echo $once $twice\n")

	counts := strings.Fields(stdout)
	if len(counts) != 2 || stderr != "" {
		t.Fatalf("could not count fish's completions: stdout %q, stderr %q", stdout, stderr)
	}
	once, _ := strconv.Atoi(counts[0])
	if once == 0 || counts[0] != counts[1] {
		t.Errorf("fish holds %s completions after one load and %s after two, want the same nonzero count", counts[0], counts[1])
	}
}

// requireShell finds a shell or stands the test aside — a skip locally, a
// failure under CI, as everywhere else in the suite.
func requireShell(t *testing.T, shell string) string {
	t.Helper()
	bin, err := exec.LookPath(shell)
	if err != nil {
		testenv.Unavailablef(t, "%s is not installed", shell)
	}
	return bin
}

// stubTreewright puts a treewright on the front of PATH that runs body, which
// is how a test stands in for the binary the wrapper calls.
func stubTreewright(t *testing.T, dir, body string) {
	t.Helper()
	writeFile(t, dir, "treewright", "#!/bin/sh\n"+body, 0o755)
}

// runShell runs program in shell with the test's own HOME and TMPDIR, and none
// of the wrapper's variables.
//
// The environment is scrubbed rather than inherited because this suite is
// usually run from inside a tw window, which exports all of them, and because
// zsh reads the developer's .zshenv even for -c. A reload asserted on top of
// the developer's own wrapper would prove nothing.
func runShell(t *testing.T, bin, dir, program string) (stdout, stderr string) {
	t.Helper()
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command(bin, "-c", program)
	cmd.Env = append(scrubbedEnv(),
		"HOME="+dir,
		"TMPDIR="+tmp,
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\nstdout:\n%s\nstderr:\n%s", filepath.Base(bin), err, out.String(), errOut.String())
	}
	return out.String(), errOut.String()
}

// scrubbedEnv is this process's environment less what a wrapper exports and
// what points a shell at somebody's startup files.
func scrubbedEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(name, "TREEWRIGHT_"),
			name == "HOME", name == "TMPDIR", name == "PATH",
			name == "ZDOTDIR", name == "BASH_ENV", name == "ENV", name == "XDG_CONFIG_HOME":
			continue
		}
		env = append(env, kv)
	}
	return env
}

// leftoverEvalFiles lists the eval files a wrapper made under dir's TMPDIR and
// did not remove.
func leftoverEvalFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "tmp", "treewright-eval.*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

func writeFile(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}
