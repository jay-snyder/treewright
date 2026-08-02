package shellinit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/testenv"
)

// TestEveryScriptIsDeclared holds scripts/ to the shells that claim it, both
// ways.
//
// The shims are eval'd straight into the user's interactive shell at every
// start, which makes a file that ships because it happened to be in a folder
// the worst kind of accident: it would run on every terminal opened, on every
// machine that installed treewright, before anything else the user typed. So
// each script reaches the binary through a //go:embed naming it, and this
// closes the other direction — a file checked in under scripts/ that no shell
// claims, which would otherwise sit there emitting nothing while looking like
// part of the integration.
//
// Deliberately not shared with internal/agentinit's equivalent, which enforces
// the same shape over its plugin folder. What each one is really made of is
// the sentence it fails with — one sends you to a module's Plugin list, the
// other to the scripts map and an embed directive — and a helper taking both
// the declared set and the message it should print would be a parameterized
// wrapper around a directory walk, which is not an abstraction worth a package.
func TestEveryScriptIsDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, shell := range Shells() {
		declared["init."+shell] = true
	}

	entries, err := os.ReadDir("scripts")
	if err != nil {
		t.Fatalf("read scripts: %v", err)
	}
	checkedIn := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("scripts/%s is a directory — the scripts are one flat file per shell", e.Name())
			continue
		}
		checkedIn[e.Name()] = true
	}

	for name := range checkedIn {
		if !declared[name] {
			t.Errorf("scripts/%s is checked in but no shell emits it — add it to the scripts map with an embed directive, or delete it", name)
		}
	}
	for name := range declared {
		if !checkedIn[name] {
			t.Errorf("a shell emits scripts/%s, which is not checked in", name)
		}
	}
}

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
		// The wrapper exists to pass TREEWRIGHT_EVAL_FILE and source what comes
		// back; without either half the integration does nothing.
		for _, want := range []string{"TREEWRIGHT_EVAL_FILE", "command treewright", "__complete"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s script is missing %q", shell, want)
			}
		}
	}
}

// TestEveryScriptDefinesTheShortName checks that tw — the everyday name — is a
// wrapper with the same completion in every shell. The strings are per-shell
// because each shell has its own way of saying "and complete tw like treewright".
func TestEveryScriptDefinesTheShortName(t *testing.T) {
	// Each tw announces the typed name in TREEWRIGHT_ARGV0, exported for just this
	// call, because it runs the binary as "command treewright" — argv[0] alone
	// would have every hint answer as treewright to someone who typed tw.
	wants := map[string][]string{
		"zsh":  {`tw() { local -x TREEWRIGHT_ARGV0=tw; treewright "$@" }`, "compdef _treewright treewright tw"},
		"bash": {`tw() { local -x TREEWRIGHT_ARGV0=tw; treewright "$@"; }`, "complete -F _treewright_completions treewright tw"},
		"fish": {"function tw --wraps treewright", "set -lx TREEWRIGHT_ARGV0 tw", "treewright $argv"},
	}
	for _, shell := range Shells() {
		script, err := Script(shell)
		if err != nil {
			t.Fatalf("Script(%q): %v", shell, err)
		}
		for _, want := range wants[shell] {
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
				testenv.Unavailablef(t, "%s is not installed", shell)
			}
			script, err := Script(shell)
			if err != nil {
				t.Fatalf("Script: %v", err)
			}
			path := filepath.Join(t.TempDir(), "init."+shell)
			if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			// Concatenated rather than appended to: appending to a slice read out of
			// a map writes into that map's backing array whenever it has the capacity
			// to spare, which would leave the table holding this subtest's path.
			args := append(slices.Clone(checkers[shell]), path)
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
// an interactive prompt on every treewright call unless rm is called through
// `command`.
func TestExternalCommandsResistAliases(t *testing.T) {
	for _, shell := range Shells() {
		script, err := Script(shell)
		if err != nil {
			t.Fatalf("Script(%q): %v", shell, err)
		}
		// Matched on the invocation rather than on the bare program name: the
		// completion tables list "rm" and "treewright" as candidate data, and those
		// are not calls. "mktemp" and "rm -f" appear only where the wrapper runs
		// them, so finding one unprefixed is unambiguous.
		//
		// treewright itself is covered by the "command treewright" assertion above.
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

// TestShortNameReachesTheBinary proves TREEWRIGHT_ARGV0 travels: the tw wrapper
// runs the binary as "command treewright", so the variable is the only way the
// binary can learn which name was typed — and it must be scoped to the one call,
// or a treewright typed after a tw would answer as tw too.
func TestShortNameReachesTheBinary(t *testing.T) {
	programs := map[string]string{
		"zsh":  "source %s\ntw ls\ntreewright ls\n",
		"bash": "source %s\ntw ls\ntreewright ls\n",
		"fish": "source %s\ntw ls\ntreewright ls\n",
	}
	for _, shell := range []string{"zsh", "bash", "fish"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				testenv.Unavailablef(t, "%s is not installed", shell)
			}
			script, err := Script(shell)
			if err != nil {
				t.Fatalf("Script: %v", err)
			}

			dir := t.TempDir()
			shim := filepath.Join(dir, "shim")
			if err := os.WriteFile(shim, []byte(script), 0o644); err != nil {
				t.Fatalf("write shim: %v", err)
			}
			// A stub treewright that reports the name it was told, as main does.
			stub := filepath.Join(dir, "treewright")
			if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"argv0=${TREEWRIGHT_ARGV0:-unset}\"\n"), 0o755); err != nil {
				t.Fatalf("write stub: %v", err)
			}

			cmd := exec.Command(bin, "-c", fmt.Sprintf(programs[shell], shim))
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s could not run the wrappers: %v\n%s", shell, err, out)
			}
			if !strings.Contains(string(out), "argv0=tw") {
				t.Errorf("tw did not tell the binary its name:\n%s", out)
			}
			// The second call, as treewright, must not still be wearing tw's name.
			if !strings.Contains(string(out), "argv0=unset") {
				t.Errorf("TREEWRIGHT_ARGV0 leaked past the tw call it was set for:\n%s", out)
			}
		})
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
				testenv.Unavailablef(t, "%s is not installed", shell)
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
			// A stub treewright on PATH that writes to the eval file, so the wrapper
			// exercises the branch where it sources something and then cleans up.
			stub := filepath.Join(dir, "treewright")
			if err := os.WriteFile(stub, []byte("#!/bin/sh\necho 'export TREEWRIGHT_TEST_RAN=1' >> \"$TREEWRIGHT_EVAL_FILE\"\n"), 0o755); err != nil {
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
				"treewright ls\n" +
				"echo \"ran=$TREEWRIGHT_TEST_RAN\"\n" +
				"echo \"leftover=$(ls \"$TMPDIR\" | grep -c '^treewright-eval\\.' || true)\"\n" +
				"eval 'probe() { rm -f /probe; }'\n" +
				"case \"$(typeset -f probe)\" in\n" +
				"  *'rm -i'*) echo 'aliases-active=yes' ;;\n" +
				"  *) echo 'aliases-active=no' ;;\n" +
				"esac\n" +
				"case \"$(typeset -f treewright)\" in\n" +
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

// TestWrapperSurvivesAnEmptyTMPDIR is the regression test for the fish shim
// treating a defined-but-empty TMPDIR as usable: its temp file then aimed at
// "/treewright-eval.XXXXXX" in the root, and the failure took every invocation
// down before the binary ever ran. All three shells get the same run, since
// ${TMPDIR:-/tmp} has to mean "unset or empty" everywhere.
func TestWrapperSurvivesAnEmptyTMPDIR(t *testing.T) {
	for _, shell := range []string{"zsh", "bash", "fish"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				testenv.Unavailablef(t, "%s is not installed", shell)
			}
			script, err := Script(shell)
			if err != nil {
				t.Fatalf("Script: %v", err)
			}

			dir := t.TempDir()
			shim := filepath.Join(dir, "shim")
			if err := os.WriteFile(shim, []byte(script), 0o644); err != nil {
				t.Fatalf("write shim: %v", err)
			}
			// A stub treewright that reports whether the eval file was wired up,
			// which is the wrapper having survived far enough to run it.
			stub := filepath.Join(dir, "treewright")
			if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"evalfile=${TREEWRIGHT_EVAL_FILE:+set}\"\n"), 0o755); err != nil {
				t.Fatalf("write stub: %v", err)
			}

			cmd := exec.Command(bin, "-c", "source "+shim+"\ntreewright ls\n")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s failed with an empty TMPDIR: %v\n%s", shell, err, out)
			}
			if !strings.Contains(string(out), "evalfile=set") {
				t.Errorf("the binary never ran with an eval file wired up:\n%s", out)
			}
		})
	}
}

// TestCompletionDescriptionsAgreeAcrossShells holds the zsh and fish command
// descriptions to each other, the way the command names are already held to the
// real table. They are hand-copied prose in two dialects — bash carries none —
// and `move`'s had drifted apart by the time this test was written.
func TestCompletionDescriptionsAgreeAcrossShells(t *testing.T) {
	zsh := completionDescriptions(t, "zsh")
	fish := completionDescriptions(t, "fish")
	if len(zsh) == 0 || len(fish) == 0 {
		t.Fatalf("parsed no descriptions (zsh %d, fish %d) — the extraction no longer matches the scripts", len(zsh), len(fish))
	}

	for name, zdesc := range zsh {
		fdesc, ok := fish[name]
		if !ok {
			t.Errorf("%s is described in zsh and absent from fish's subcommand completions", name)
			continue
		}
		if zdesc != fdesc {
			t.Errorf("%s is described as %q in zsh and %q in fish — hand-copied prose has to match", name, zdesc, fdesc)
		}
	}
	for name := range fish {
		if _, ok := zsh[name]; !ok {
			t.Errorf("%s is described in fish and absent from zsh's command list", name)
		}
	}
}

// completionDescriptions reads a shell's per-command descriptions back out of
// its emitted script: zsh's 'name:description' entries, and the -d values on
// fish's subcommand completions.
func completionDescriptions(t *testing.T, shell string) map[string]string {
	t.Helper()
	script, err := Script(shell)
	if err != nil {
		t.Fatalf("Script(%q): %v", shell, err)
	}
	out := map[string]string{}
	for line := range strings.SplitSeq(script, "\n") {
		line = strings.TrimSpace(line)
		switch shell {
		case "zsh":
			if !strings.HasPrefix(line, "'") || !strings.HasSuffix(line, "'") {
				continue
			}
			if name, desc, ok := strings.Cut(strings.Trim(line, "'"), ":"); ok {
				out[name] = desc
			}
		case "fish":
			if !strings.Contains(line, "__fish_use_subcommand") {
				continue
			}
			_, rest, ok := strings.Cut(line, "-a ")
			if !ok {
				continue
			}
			name := strings.Fields(rest)[0]
			if _, desc, ok := strings.Cut(rest, "-d '"); ok {
				out[name] = strings.TrimSuffix(strings.TrimSpace(desc), "'")
			}
		}
	}
	return out
}

// TestEveryScriptExportsWhichTreewrightWroteIt covers the one integration that
// cannot be asked about any other way.
//
// A tmux binding is a thing a server holds and a plugin is a file on disk, but
// the shell wrapper lives in the user's own shell, where a child process cannot
// see it. Before this, doctor inferred "loaded" from the eval file being set —
// which a terminal opened two releases ago reports exactly as one opened a
// minute ago does. The variable is what separates them, and it has to be
// exported, since the only thing that reads it is a child.
func TestEveryScriptExportsWhichTreewrightWroteIt(t *testing.T) {
	// Each shell's own spelling of "set this in the environment", so a shim that
	// merely assigned the value — invisible to the binary — fails here.
	exports := map[string]string{
		"zsh":  `export ` + VersionVar + `="`,
		"bash": `export ` + VersionVar + `="`,
		"fish": `set -gx ` + VersionVar + ` "`,
	}
	for _, shell := range Shells() {
		script, err := Script(shell)
		if err != nil {
			t.Fatalf("Script(%q): %v", shell, err)
		}
		version, err := Version(shell)
		if err != nil {
			t.Fatalf("Version(%q): %v", shell, err)
		}
		if !strings.Contains(script, exports[shell]+version+`"`) {
			t.Errorf("the %s script does not export %s as %q", shell, VersionVar, version)
		}
		// The placeholder is filled on the way out, never shipped: a shell asked
		// to export "{{version}}" would report a version that is the same for
		// every build there has ever been.
		if strings.Contains(script, versionPlaceholder) {
			t.Errorf("the %s script still carries %s", shell, versionPlaceholder)
		}
	}
}

// TestEachShimHasItsOwnFingerprint: the value identifies a script rather than a
// release, so two shells that differ must not answer alike — otherwise a shim
// could change without the fingerprint moving.
func TestEachShimHasItsOwnFingerprint(t *testing.T) {
	seen := map[string]string{}
	for _, shell := range Shells() {
		version, err := Version(shell)
		if err != nil {
			t.Fatalf("Version(%q): %v", shell, err)
		}
		if other, clash := seen[version]; clash {
			t.Errorf("%s and %s both fingerprint as %q", shell, other, version)
		}
		seen[version] = shell
	}
}

// TestCurrentRecognizesEveryShimAndNothingElse is what doctor asks. Any shell's
// fingerprint counts, deliberately: the question is "is this wrapper one of
// mine", and working out which shell the caller is running to ask a narrower one
// would mean trusting $SHELL, which names the login shell rather than the
// running one.
func TestCurrentRecognizesEveryShimAndNothingElse(t *testing.T) {
	for _, shell := range Shells() {
		version, err := Version(shell)
		if err != nil {
			t.Fatalf("Version(%q): %v", shell, err)
		}
		if !Current(version) {
			t.Errorf("the %s shim's own fingerprint %q is not recognized", shell, version)
		}
	}
	// An older treewright's shim, and a shell that exports nothing at all.
	for _, stale := range []string{"", "000000000000", "dev"} {
		if Current(stale) {
			t.Errorf("%q is reported as a fingerprint this binary emits", stale)
		}
	}
}
