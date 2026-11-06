package cmdq

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a screen a test can read while a run is writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls the screen until it shows want, failing the test after a
// while. Both the command and the list run on goroutines the test does not
// otherwise hear from.
func waitFor(t *testing.T, screen *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(screen.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the screen never showed %q:\n%q", want, screen.String())
}

// TestACommandRunsUnderATerminalOfItsOwn is what lets sudo ask for a password:
// the command's input and output are a terminal, the one it controls.
func TestACommandRunsUnderATerminalOfItsOwn(t *testing.T) {
	var screen syncBuffer
	dir := t.TempDir()
	result, err := Run{
		Command: `[ -t 0 ] && [ -t 1 ] && echo "a terminal"; pwd; exit 3`,
		Dir:     dir,
		Screen:  &screen,
	}.Do()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 3 {
		t.Errorf("status = %d, want 3", result.Status)
	}
	if !strings.Contains(screen.String(), "a terminal") {
		t.Errorf("the command's input and output were not a terminal:\n%q", screen.String())
	}
	if !strings.Contains(result.Output, "a terminal") || !strings.HasSuffix(strings.TrimSpace(result.Output), lastPathPart(dir)) {
		t.Errorf("output kept = %q, want what it printed, ending in %s", result.Output, dir)
	}
}

func lastPathPart(p string) string { return p[strings.LastIndex(p, "/")+1:] }

// TestCtrlCGoesToTheCommand: the key reaches the command's terminal, which
// interrupts the command, and the run ends with the list still alive to say
// so.
func TestCtrlCGoesToTheCommand(t *testing.T) {
	var screen syncBuffer
	keys, typing := io.Pipe()
	defer func() { _ = typing.Close() }()
	done := make(chan Result, 1)
	go func() {
		result, err := Run{
			Command: `trap 'echo interrupted; exit 7' INT; echo ready; sleep 30 & wait`,
			Dir:     t.TempDir(),
			Keys:    NewKeyboard(keys),
			Screen:  &screen,
		}.Do()
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	waitFor(t, &screen, "ready")
	if _, err := typing.Write([]byte{0x03}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Status != 7 || !strings.Contains(screen.String(), "interrupted") {
			t.Errorf("status = %d, screen = %q, want the command's own handler to have run", result.Status, screen.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Ctrl+C did not end the command")
	}
}

// TestKeysReachTheCommand types an answer to a prompt, as a person answering
// sudo would, and the answer read without echo stays out of what is kept.
func TestKeysReachTheCommand(t *testing.T) {
	var screen syncBuffer
	keys, typing := io.Pipe()
	defer func() { _ = typing.Close() }()
	done := make(chan Result, 1)
	go func() {
		result, _ := Run{
			Command: `printf 'Password: '; read -rs pw; echo; echo "read ${#pw} characters"`,
			Dir:     t.TempDir(),
			Keys:    NewKeyboard(keys),
			Screen:  &screen,
		}.Do()
		done <- result
	}()
	waitFor(t, &screen, "Password: ")
	// The prompt is written before read turns echo off, so the answer waits
	// until the terminal has had a moment to take the change.
	time.Sleep(200 * time.Millisecond)
	if _, err := typing.Write([]byte("hunter2\r")); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if !strings.Contains(result.Output, "read 7 characters") {
		t.Errorf("output = %q", result.Output)
	}
	if strings.Contains(result.Output, "hunter2") || strings.Contains(screen.String(), "hunter2") {
		t.Errorf("the password was echoed: %q", screen.String())
	}
}

// TestARunEndsWhenItsCommandDoes, whatever the command left behind holding its
// terminal: the list comes back, and what was left is ended with the terminal.
func TestARunEndsWhenItsCommandDoes(t *testing.T) {
	var screen syncBuffer
	start := time.Now()
	result, err := Run{
		Command: `sleep 60 & echo started`,
		Dir:     t.TempDir(),
		Screen:  &screen,
	}.Do()
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > afterExit+2*time.Second {
		t.Errorf("the run took %v, held open by what its command left running", took)
	}
	if result.Status != 0 || !strings.Contains(result.Output, "started") {
		t.Errorf("result = %+v", result)
	}
}

func TestADeathBySignalIsReportedAsAShellWould(t *testing.T) {
	var screen syncBuffer
	result, err := Run{Command: `kill -TERM $$`, Dir: t.TempDir(), Screen: &screen}.Do()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 128+15 {
		t.Errorf("status = %d, want 143", result.Status)
	}
}

func TestTheKeptOutputIsBounded(t *testing.T) {
	var kept tail
	chunk := bytes.Repeat([]byte("x"), 1000)
	for range 3 * KeepBytes / len(chunk) {
		kept.add(chunk)
	}
	data, truncated := kept.bytes()
	if len(data) != KeepBytes || !truncated {
		t.Errorf("kept %d bytes, truncated %v; want %d and true", len(data), truncated, KeepBytes)
	}
}

func TestDrainDropsWhatWasTypedAndKeepsWhatComesAfter(t *testing.T) {
	keys, typing := io.Pipe()
	defer func() { _ = typing.Close() }()
	k := NewKeyboard(keys)
	go func() { _, _ = typing.Write([]byte("q")) }()
	time.Sleep(50 * time.Millisecond)
	k.Drain(drainQuiet)
	go func() { _, _ = typing.Write([]byte("x")) }()
	if got := k.waitKey(); got != "x" {
		t.Errorf("first key after the drain = %q, want x", got)
	}
}

func TestKeysAreDecoded(t *testing.T) {
	for _, c := range []struct {
		typed string
		want  []key
	}{
		{"\x1b[A\x1b[B\x1bOA\x1bOB", []key{keyUp, keyDown, keyUp, keyDown}},
		{"\x1b[5~\x1b[6~\x1b[H\x1b[F\x1b[1~\x1b[4~", []key{keyPgUp, keyPgDn, keyHome, keyEnd, keyHome, keyEnd}},
		{"\r\n", []key{keyEnter, keyEnter}},
		{"\x03\x1a\x04q", []key{keyQuit, keyNone, keyNone, "q"}},
		{"\x1bx", []key{keyNone}},
		{"é", []key{keyNone}},
	} {
		k := &Keyboard{chunks: make(chan []byte), pending: []byte(c.typed)}
		var got []key
		for len(k.pending) > 0 {
			got = append(got, k.key())
		}
		if strings.Join(keyNames(got), " ") != strings.Join(keyNames(c.want), " ") {
			t.Errorf("%q decoded as %q, want %q", c.typed, got, c.want)
		}
	}
	lone := &Keyboard{chunks: make(chan []byte), pending: []byte{0x1b}}
	if got := lone.key(); got != keyEsc {
		t.Errorf("a lone ESC = %q, want esc", got)
	}
}

func keyNames(keys []key) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, "<"+string(k)+">")
	}
	return out
}
