package tmuxinit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test that matters in this package is the one below: the snippet is loaded
// into a real tmux server, because a config file that does not parse would break
// the user's tmux startup, and tmux's quoting is where that happens.
//
// The server is private to the test — its own socket directory, killed afterwards
// — so a developer's own sessions and key bindings are never touched.

// server starts a tmux server this test owns, and returns a function that runs
// commands against it.
func server(t *testing.T) func(args ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	// Under /tmp rather than t.TempDir(), because a unix socket path is limited to
	// little over a hundred characters and a macOS temp path approaches that alone.
	dir, err := os.MkdirTemp("/tmp", "tmx")
	if err != nil {
		t.Fatalf("make a tmux socket directory: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	label := strings.ReplaceAll(t.Name(), "/", "-")

	tmux := func(args ...string) (string, error) {
		out, err := exec.Command("tmux", append([]string{"-L", label}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		_, _ = tmux("kill-server")
		_ = os.RemoveAll(dir)
	})

	// Key bindings live in a server, so there has to be one to load them into —
	// and it has to be a stock one. A server reads ~/.tmux.conf as it starts, so
	// without -f the developer's own bindings are in the table these tests then
	// count, and anyone who had loaded this very integration into their config
	// would see the suite fail. The socket is private; the configuration has to be
	// too.
	if out, err := tmux("-f", "/dev/null", "new-session", "-d", "-s", "probe", "-c", "/tmp", "sleep 300"); err != nil {
		t.Skipf("cannot start a tmux server here: %v\n%s", err, out)
	}
	return tmux
}

// TestScriptLoadsIntoARealServer is the analogue of running each shell shim
// through its own syntax checker: tmux is asked to parse and execute the snippet,
// and then asked what it ended up bound to.
func TestScriptLoadsIntoARealServer(t *testing.T) {
	tmux := server(t)

	path := filepath.Join(t.TempDir(), "treemux.tmux")
	if err := os.WriteFile(path, []byte(Script(DefaultKeys())), 0o644); err != nil {
		t.Fatalf("write the snippet: %v", err)
	}
	if out, err := tmux("source-file", path); err != nil {
		t.Fatalf("tmux rejected the emitted config: %v\n%s", err, out)
	}

	keys, err := tmux("list-keys", "-T", "prefix")
	if err != nil {
		t.Fatalf("list the key bindings: %v\n%s", err, keys)
	}
	// Both bindings have to survive tmux's own quoting, which is the thing this
	// test exists for: the N binding nests a shell command inside a tmux command
	// inside a command-prompt template, and a mistake there parses as something
	// else rather than failing outright.
	for _, want := range []string{"treemux popup", "resume", "new %1"} {
		if !strings.Contains(keys, want) {
			t.Errorf("no key binding mentions %q:\n%s", want, keys)
		}
	}

	// Both must name the client. run-shell spawns a process with no association to
	// the client that triggered it, so without this tmux picks the most recently
	// active one and the popup opens over whichever terminal has been busier —
	// which, with two attached, is not usually the one at the keyboard.
	if strings.Count(keys, "client_tty") != 2 {
		t.Errorf("a binding does not name the client to draw on:\n%s", keys)
	}

	if titles, err := tmux("show-options", "-g", "-v", "set-titles"); err != nil || titles != "on" {
		t.Errorf("set-titles = %q (%v), want it turned on", titles, err)
	}
}

// TestBindingsDoNotOverwriteTmuxsOwn guards the choice of keys. tmux binds five
// uppercase keys of its own — C, D, E, L and M — and taking one of them would
// silently cost the user a default they may rely on.
func TestBindingsDoNotOverwriteTmuxsOwn(t *testing.T) {
	tmux := server(t)

	before, err := tmux("list-keys", "-T", "prefix", "-N")
	if err != nil {
		t.Fatalf("list the default bindings: %v\n%s", err, before)
	}

	path := filepath.Join(t.TempDir(), "treemux.tmux")
	if err := os.WriteFile(path, []byte(Script(DefaultKeys())), 0o644); err != nil {
		t.Fatalf("write the snippet: %v", err)
	}
	if out, err := tmux("source-file", path); err != nil {
		t.Fatalf("source the snippet: %v\n%s", err, out)
	}

	after, err := tmux("list-keys", "-T", "prefix", "-N")
	if err != nil {
		t.Fatalf("list the bindings again: %v\n%s", err, after)
	}

	// Every key tmux described before must still be described the same way. A
	// binding treemux added is a new line, not a changed one.
	for _, line := range strings.Split(before, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(after, line) {
			t.Errorf("loading the snippet replaced a default binding:\n  %s", line)
		}
	}
}

// TestScriptDocumentsWhatItBinds keeps the snippet self-explanatory. It is
// printed for a user to read before loading, and a file of bare bind-key lines
// would not survive that reading.
func TestScriptDocumentsWhatItBinds(t *testing.T) {
	script := Script(DefaultKeys())
	for _, want := range []string{
		"tmux-init --apply", // the one-line way to load it
		"source-file",       // and the file way
		"--resume-key",      // and how to move the keys
		"@treemux_slug",     // the window options a status line can read
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the snippet never mentions %q", want)
		}
	}
}

// TestDefaultKeysSurviveAMissedShift is the reason the defaults are what they
// are. These keys get reached for in a hurry, and the first pair chosen — W for
// the picker — had a lowercase twin that a great many configurations rebind to
// kill-window, which in a treemux worktree destroys the running agent.
//
// tmux's own lowercase bindings for t and n are clock-mode and next-window. This
// pins the choice against a future edit that reintroduces the hazard by reaching
// for a nicer mnemonic.
func TestDefaultKeysSurviveAMissedShift(t *testing.T) {
	keys := DefaultKeys()
	// The keys people commonly rebind to something that destroys a window or a
	// pane, lowercased. A default whose twin is one of these is the bug.
	for _, dangerous := range []string{"w", "x", "k", "q", "&"} {
		if strings.ToLower(keys.Resume) == dangerous {
			t.Errorf("resume key %q lowercases to %q, which is commonly kill-window or kill-pane",
				keys.Resume, dangerous)
		}
		if strings.ToLower(keys.New) == dangerous {
			t.Errorf("new key %q lowercases to %q, which is commonly kill-window or kill-pane",
				keys.New, dangerous)
		}
	}
	if err := keys.Validate(); err != nil {
		t.Errorf("the defaults do not pass validation: %v", err)
	}
}

func TestScriptHonorsCustomKeys(t *testing.T) {
	script := Script(Keys{Resume: "G", New: "C-n"})

	if !strings.Contains(script, "bind-key G run-shell") {
		t.Errorf("the resume binding does not use the key asked for:\n%s", script)
	}
	if !strings.Contains(script, "bind-key C-n command-prompt") {
		t.Errorf("the new-worktree binding does not use the key asked for:\n%s", script)
	}
	// The prose names the keys too, since the file is meant to be read.
	if !strings.Contains(script, "prefix + G") || !strings.Contains(script, "prefix + C-n") {
		t.Errorf("the comments still describe the default keys:\n%s", script)
	}
	if strings.Contains(script, "bind-key T ") || strings.Contains(script, "bind-key N ") {
		t.Errorf("a default binding survived being overridden:\n%s", script)
	}
}

// TestAnEmptyKeyOmitsItsBinding covers the way to take just one of them: an
// empty key is a deliberate "bind nothing", not a mistake to fall back from.
func TestAnEmptyKeyOmitsItsBinding(t *testing.T) {
	script := Script(Keys{Resume: "T"})
	if !strings.Contains(script, `treemux popup -c "#{client_tty}" resume`) {
		t.Errorf("the kept binding is missing:\n%s", script)
	}
	if strings.Contains(script, "new %1") {
		t.Errorf("the omitted binding was emitted anyway:\n%s", script)
	}

	// With neither, the whole section goes rather than leaving a heading over
	// nothing — and the rest of the file still stands on its own.
	bare := Script(Keys{})
	if strings.Contains(bare, "bind-key") {
		t.Errorf("bindings were emitted with no keys asked for:\n%s", bare)
	}
	if !strings.Contains(bare, "set -g set-titles on") || !strings.Contains(bare, "@treemux_slug") {
		t.Errorf("dropping the bindings took the rest of the snippet with it:\n%s", bare)
	}
}

// TestValidateRejectsKeysThatWouldBreakTheConfig covers the characters that are
// punctuation in tmux's own config syntax. They would not fail loudly — they end
// the binding somewhere other than where it looks like it ends — so they are
// turned away here, with the printed-file route named as the way to have them.
func TestValidateRejectsKeysThatWouldBreakTheConfig(t *testing.T) {
	for _, key := range []string{`"`, "'", ";", "#", "a b", `\`, strings.Repeat("x", 13)} {
		if err := (Keys{Resume: key}).Validate(); err == nil {
			t.Errorf("Validate accepted resume key %q", key)
		}
	}
	// And accepts the forms a person actually reaches for.
	for _, key := range []string{"T", "G", "C-n", "M-Left", "F5", "Space", "9", ""} {
		if err := (Keys{Resume: key, New: key}).Validate(); err != nil {
			t.Errorf("Validate rejected %q: %v", key, err)
		}
	}
}

// TestCustomKeysStillLoadIntoARealServer is the important half of the above: a
// key that passes validation has to survive tmux's parser too, in both bindings
// — the second of which nests a shell command inside a tmux command inside a
// command-prompt template.
func TestCustomKeysStillLoadIntoARealServer(t *testing.T) {
	tmux := server(t)

	path := filepath.Join(t.TempDir(), "treemux.tmux")
	if err := os.WriteFile(path, []byte(Script(Keys{Resume: "C-g", New: "F5"})), 0o644); err != nil {
		t.Fatalf("write the snippet: %v", err)
	}
	if out, err := tmux("source-file", path); err != nil {
		t.Fatalf("tmux rejected a snippet with custom keys: %v\n%s", err, out)
	}

	keys, err := tmux("list-keys", "-T", "prefix")
	if err != nil {
		t.Fatalf("list the key bindings: %v\n%s", err, keys)
	}
	for _, want := range []string{"C-g", "F5"} {
		if !strings.Contains(keys, want) {
			t.Errorf("no binding on %q after loading:\n%s", want, keys)
		}
	}
}
