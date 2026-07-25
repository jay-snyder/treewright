package shellinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptRejectsUnknownShells(t *testing.T) {
	if _, err := Script("nushell"); err == nil {
		t.Fatal("want an error for an unsupported shell")
	} else if !strings.Contains(err.Error(), "bash, fish, zsh") {
		// The error has to name the supported set, or the user is left guessing.
		t.Errorf("error = %q, want it to list the supported shells", err)
	}
}

func TestEveryScriptDefinesTheWrapperAndCompletion(t *testing.T) {
	for _, shell := range Shells() {
		script, err := Script(shell)
		if err != nil {
			t.Fatalf("Script(%q): %v", shell, err)
		}
		// The wrapper exists to pass TREEMUX_EVAL_FILE and source what comes
		// back; without either half the integration does nothing.
		for _, want := range []string{"TREEMUX_EVAL_FILE", "command treemux", "__complete"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s script is missing %q", shell, want)
			}
		}
	}
}

// TestScriptsParse runs each emitted script through its own shell's syntax
// checker. A shim that does not parse would break the user's shell startup, so
// this is the test that matters most in this package.
func TestScriptsParse(t *testing.T) {
	// How to ask each shell to parse a file without executing it.
	checkers := map[string][]string{
		"zsh":  {"-n"},
		"bash": {"-n"},
		"fish": {"--no-execute"},
	}

	for _, shell := range Shells() {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			script, err := Script(shell)
			if err != nil {
				t.Fatalf("Script: %v", err)
			}
			path := filepath.Join(t.TempDir(), "init."+shell)
			if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			args := append(checkers[shell], path)
			if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
				t.Errorf("%s rejected the emitted script: %v\n%s", shell, err, out)
			}
		})
	}
}
