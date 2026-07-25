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

// TestExternalCommandsResistAliases guards a hazard specific to shipping shell
// code into someone else's shell: zsh and bash expand aliases in a function body
// at definition time, so an alias in the user's startup file rewrites the words
// the shim emits. `alias rm='rm -i'` is common, and turns the cleanup below into
// an interactive prompt on every treemux call unless rm is called through
// `command`.
func TestExternalCommandsResistAliases(t *testing.T) {
	for _, shell := range Shells() {
		script, err := Script(shell)
		if err != nil {
			t.Fatalf("Script(%q): %v", shell, err)
		}
		// Matched on the invocation rather than on the bare program name: the
		// completion tables list "rm" and "treemux" as candidate data, and those
		// are not calls. "mktemp" and "rm -f" appear only where the wrapper runs
		// them, so finding one unprefixed is unambiguous.
		//
		// treemux itself is covered by the "command treemux" assertion above.
		for _, invocation := range []string{"mktemp ", "rm -f"} {
			rest := script
			for {
				idx := strings.Index(rest, invocation)
				if idx < 0 {
					break
				}
				if !strings.HasSuffix(rest[:idx], "command ") {
					line := rest[:idx]
					if nl := strings.LastIndex(line, "\n"); nl >= 0 {
						line = line[nl+1:]
					}
					t.Errorf("%s runs %q without \"command\", so an alias can rewrite it: %q",
						shell, strings.TrimSpace(invocation), line+invocation)
				}
				rest = rest[idx+len(invocation):]
			}
		}
	}
}

// TestWrapperSurvivesAnRmAlias runs the emitted shim in a real shell that has the
// alias set, and checks the temp file is cleaned up without a prompt.
//
// The shim is written to a file and sourced, rather than pasted into the program,
// because that is the only shape in which the hazard occurs — and the shape a
// startup file uses. Both shells expand aliases as they parse a function body, so
// the alias has to already be in effect when the body is read: sourced after the
// alias, `rm -f` becomes `rm -i -f`, while the same text inlined into one -c
// argument is parsed before the alias statement ever runs and expands to nothing.
// An earlier version of this test inlined it, and so proved nothing.
func TestWrapperSurvivesAnRmAlias(t *testing.T) {
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			script, err := Script(shell)
			if err != nil {
				t.Fatalf("Script: %v", err)
			}

			dir := t.TempDir()
			shim := filepath.Join(dir, "shim.sh")
			if err := os.WriteFile(shim, []byte(script), 0o644); err != nil {
				t.Fatalf("write shim: %v", err)
			}
			// A stub treemux on PATH that writes to the eval file, so the wrapper
			// exercises the branch where it sources something and then cleans up.
			stub := filepath.Join(dir, "treemux")
			if err := os.WriteFile(stub, []byte("#!/bin/sh\necho 'export TREEMUX_TEST_RAN=1' >> \"$TREEMUX_EVAL_FILE\"\n"), 0o755); err != nil {
				t.Fatalf("write stub: %v", err)
			}

			// bash disables alias expansion entirely when not interactive, so
			// without this the bash case would silently test nothing.
			//
			// The probe is what keeps this test honest. It is a function defined
			// the same way, through a fresh parse with the alias already in force,
			// and it is expected to come out rewritten — proving aliases really
			// are active here. The wrapper, defined identically but guarded with
			// `command`, is expected to come out untouched. Asserting only the
			// second would pass on a shell that expands nothing at all.
			program := "shopt -s expand_aliases 2>/dev/null\n" +
				"alias rm='rm -i'\nalias mktemp='mktemp -q'\n" +
				"source " + shim + "\n" +
				"treemux ls\n" +
				"echo \"ran=$TREEMUX_TEST_RAN\"\n" +
				"echo \"leftover=$(ls \"$TMPDIR\" | grep -c '^treemux-eval\\.' || true)\"\n" +
				"eval 'probe() { rm -f /probe; }'\n" +
				"case \"$(typeset -f probe)\" in\n" +
				"  *'rm -i'*) echo 'aliases-active=yes' ;;\n" +
				"  *) echo 'aliases-active=no' ;;\n" +
				"esac\n" +
				"case \"$(typeset -f treemux)\" in\n" +
				"  *'rm -i'*) echo 'wrapper-rewritten=yes' ;;\n" +
				"  *) echo 'wrapper-rewritten=no' ;;\n" +
				"esac\n"

			cmd := exec.Command(bin, "-c", program)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s could not run the wrapper: %v\n%s", shell, err, out)
			}
			if !strings.Contains(string(out), "ran=1") {
				t.Errorf("the wrapper never sourced the eval file:\n%s", out)
			}
			if !strings.Contains(string(out), "leftover=0") {
				t.Errorf("the eval file was left behind, so cleanup did not run:\n%s", out)
			}
			// The guard against this test quietly ceasing to test anything: without
			// an active alias, the run proves only that the wrapper works, not that
			// it is immune to one.
			if !strings.Contains(string(out), "aliases-active=yes") {
				t.Errorf("%s expanded no alias into the probe, so this run proves nothing about the wrapper:\n%s", shell, out)
			}
			if !strings.Contains(string(out), "wrapper-rewritten=no") {
				t.Errorf("the rm alias was baked into the wrapper body:\n%s", out)
			}
		})
	}
}
