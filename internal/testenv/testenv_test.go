package testenv

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestAMissingToolSkipsLocallyAndFailsUnderCI is the test that guards the guard.
//
// Every tool-dependent test in this repository routes through here, so an
// inversion of the condition, or a rename of the variable it reads, would put the
// suite straight back to where it was: tmux absent, the whole integration skipped,
// and CI green about it. That failure is invisible by construction — a run that
// checks nothing looks exactly like a run that passed — so it needs a test that
// does not depend on which tools this machine happens to have.
//
// The report is observed by running one test in a subprocess, the standard way to
// assert about a t that fails: a *testing.T cannot be faked, and a Fatalf called
// on this one would fail this test rather than be measured by it.
func TestAMissingToolSkipsLocallyAndFailsUnderCI(t *testing.T) {
	for _, tc := range []struct {
		name string
		ci   string
		want string
	}{
		// Empty rather than absent, because the parent may itself be running under
		// CI — which is exactly where this test matters most.
		{"on a machine that promised nothing", "", "--- SKIP"},
		{"under CI, which installed it on purpose", "true", "--- FAIL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestHelperWantsAToolNobodyHas", "-test.v")
			cmd.Env = append(os.Environ(), "TESTENV_HELPER=1", "CI="+tc.ci)
			out, err := cmd.CombinedOutput()

			if !strings.Contains(string(out), tc.want) {
				t.Errorf("with CI=%q the helper did not %s:\n%s", tc.ci, strings.TrimPrefix(tc.want, "--- "), out)
			}
			// The exit status has to agree with the report, since that is the half
			// the CI runner acts on.
			if wantErr := tc.want == "--- FAIL"; (err != nil) != wantErr {
				t.Errorf("with CI=%q the subprocess exited with err=%v, want err!=nil to be %v", tc.ci, err, wantErr)
			}
			// Whichever way it went, it has to say which tool was missing: a failure
			// naming no tool sends the reader to the wrong install step.
			if !strings.Contains(string(out), "definitely-not-installed") {
				t.Errorf("with CI=%q the report does not name the tool:\n%s", tc.ci, out)
			}
		})
	}
}

// TestHelperWantsAToolNobodyHas is the subprocess half of the test above, and is
// skipped when run as itself. The name is deliberately one no PATH will satisfy.
func TestHelperWantsAToolNobodyHas(t *testing.T) {
	if os.Getenv("TESTENV_HELPER") == "" {
		t.Skip("the subprocess half of TestAMissingToolSkipsLocallyAndFailsUnderCI")
	}
	RequireTool(t, "definitely-not-installed-anywhere")
}
