package cmdq

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

// ---- stopping a run ---------------------------------------------------------

// fastStop is how the tests stop a run: the rule the list keeps, in less time
// than a test should spend waiting on it.
var fastStop = stopping{again: 100 * time.Millisecond, term: 500 * time.Millisecond, kill: 500 * time.Millisecond}

// running is a run the test types at and watches, under timing.
type running struct {
	screen *syncBuffer
	typing *io.PipeWriter
	done   chan Result
}

func startRun(t *testing.T, command string, timing stopping, quit <-chan struct{}) *running {
	t.Helper()
	keys, typing := io.Pipe()
	t.Cleanup(func() { _ = typing.Close() })
	r := &running{screen: &syncBuffer{}, typing: typing, done: make(chan Result, 1)}
	run := Run{Command: command, Dir: t.TempDir(), Keys: NewKeyboard(keys), Screen: r.screen, Quit: quit, timing: timing}
	go func() {
		result, err := run.Do()
		if err != nil {
			t.Error(err)
		}
		r.done <- result
	}()
	return r
}

func (r *running) press(t *testing.T, keys string) {
	t.Helper()
	if _, err := r.typing.Write([]byte(keys)); err != nil {
		t.Fatal(err)
	}
}

// result waits at most within for the run to end.
func (r *running) result(t *testing.T, within time.Duration) Result {
	t.Helper()
	select {
	case result := <-r.done:
		return result
	case <-time.After(within):
		t.Fatalf("the run was still going after %v:\n%q", within, r.screen.String())
		return Result{}
	}
}

// stillRunning fails the test if the run ends within d.
func (r *running) stillRunning(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case result := <-r.done:
		t.Fatalf("the run ended as %+v, want it still going:\n%q", result, r.screen.String())
	case <-time.After(d):
	}
}

// pressTwice is a person who pressed Ctrl+C, watched the command go on, and
// pressed it again.
func (r *running) pressTwice(t *testing.T) {
	t.Helper()
	r.press(t, "\x03")
	r.stillRunning(t, 2*fastStop.again)
	r.press(t, "\x03")
}

// waitForNumber waits for the screen to show label followed by a number, and
// returns the number: a pid the command printed, for the test to look for
// afterwards.
func waitForNumber(t *testing.T, screen *syncBuffer, label string) int {
	t.Helper()
	pattern := regexp.MustCompile(regexp.QuoteMeta(label) + `([0-9]+)`)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := pattern.FindStringSubmatch(screen.String()); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the screen never showed %s and a number:\n%q", label, screen.String())
	return 0
}

// waitGroupGone fails the test unless the process group is soon empty. Soon
// rather than at once, because whatever was killed in it is reaped by
// whoever adopted it, which takes a moment.
func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := unix.Kill(-pgid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("process group %d is still there after the stop", pgid)
}

// TestOneCtrlCIsStillTheCommandsInterrupt: a command that stops when asked
// ends as it always did, an ordinary interrupt, whatever a second press now
// means.
func TestOneCtrlCIsStillTheCommandsInterrupt(t *testing.T) {
	r := startRun(t, `echo ready; sleep 60`, defaultStopping, nil)
	waitFor(t, r.screen, "ready")
	r.press(t, "\x03")
	if got := r.result(t, 10*time.Second); got.Status != 128+2 || got.Stopped {
		t.Errorf("result = %+v, want exit 130 and not stopped", got)
	}
}

// TestASecondCtrlCStopsACommandThatIgnoresTheFirst is the trap the list used
// to be: a command that ignores SIGINT, and a popup that lasted exactly as
// long as it did.
func TestASecondCtrlCStopsACommandThatIgnoresTheFirst(t *testing.T) {
	r := startRun(t, `trap '' INT; echo ready; sleep 60`, fastStop, nil)
	waitFor(t, r.screen, "ready")
	r.pressTwice(t)
	if got := r.result(t, fastStop.term+fastStop.kill+5*time.Second); !got.Stopped || got.Status != 128+15 {
		t.Errorf("result = %+v, want stopped by SIGTERM", got)
	}
}

// TestASecondCtrlCStopsACommandWhoseTerminalIsRaw: a command that put its
// terminal in raw mode, where Ctrl+C is a byte it reads rather than a signal
// — a login flow drawing its own screen, or anything under stty raw.
func TestASecondCtrlCStopsACommandWhoseTerminalIsRaw(t *testing.T) {
	r := startRun(t, `stty raw -echo; echo ready; cat > /dev/null`, fastStop, nil)
	waitFor(t, r.screen, "ready")
	r.pressTwice(t)
	if got := r.result(t, fastStop.term+fastStop.kill+5*time.Second); !got.Stopped || got.Status != 128+15 {
		t.Errorf("result = %+v, want stopped by SIGTERM", got)
	}
}

// TestACommandThatSurvivesOneCtrlCIsLeftToRun: one press is the command's
// however long the command goes on after it. Nothing but a second press
// stops it.
func TestACommandThatSurvivesOneCtrlCIsLeftToRun(t *testing.T) {
	r := startRun(t, `trap '' INT; echo ready; read -r line; echo "got $line"`, fastStop, nil)
	waitFor(t, r.screen, "ready")
	r.press(t, "\x03")
	r.stillRunning(t, fastStop.again+fastStop.term+fastStop.kill)
	r.press(t, "go\r")
	if got := r.result(t, 10*time.Second); got.Stopped || got.Status != 0 || !strings.Contains(got.Output, "got go") {
		t.Errorf("result = %+v, want the command to have read its answer and exited 0", got)
	}
}

// TestADoublePressIsTheCommands: two presses in quick succession are a person
// pressing twice on purpose — at a tool that asks again on Ctrl+C, say — and
// both go to the command.
func TestADoublePressIsTheCommands(t *testing.T) {
	patient := fastStop
	patient.again = time.Hour
	r := startRun(t, `trap '' INT; echo ready; read -r line; echo "got $line"`, patient, nil)
	waitFor(t, r.screen, "ready")
	r.press(t, "\x03")
	time.Sleep(50 * time.Millisecond)
	r.press(t, "\x03")
	r.stillRunning(t, fastStop.term+fastStop.kill)
	r.press(t, "go\r")
	if got := r.result(t, 10*time.Second); got.Stopped || got.Status != 0 || !strings.Contains(got.Output, "got go") {
		t.Errorf("result = %+v, want the command to have read its answer and exited 0", got)
	}
}

func TestASecondCtrlCStopsOnlyOnceTheCommandHasHadAMoment(t *testing.T) {
	c := ctrlCs{again: time.Second}
	start := time.Now()
	for _, press := range []struct {
		at    time.Duration
		stops bool
	}{
		{0, false},                      // the first is the command's
		{300 * time.Millisecond, false}, // a double press is too
		{999 * time.Millisecond, false},
		{time.Second, true}, // one after the command has gone on is the person's
		{time.Hour, true},   // however long they waited to press it
	} {
		if got := c.stops(start.Add(press.at)); got != press.stops {
			t.Errorf("a Ctrl+C %v after the first: stops = %v, want %v", press.at, got, press.stops)
		}
	}
}

// TestClosingTheListStopsTheRun: the popup closed from outside while a
// command runs that would outlive the hangup of its terminal.
func TestClosingTheListStopsTheRun(t *testing.T) {
	quit := make(chan struct{})
	r := startRun(t, `trap '' INT HUP; echo ready; sleep 60`, fastStop, quit)
	waitFor(t, r.screen, "ready")
	close(quit)
	if got := r.result(t, fastStop.term+fastStop.kill+5*time.Second); !got.Stopped || got.Status != 128+15 {
		t.Errorf("result = %+v, want stopped by SIGTERM", got)
	}
}

// TestAStopReachesWhatTheCommandStarted: a grandchild that ignores SIGTERM,
// holding the terminal, outlives the command and is killed with the rest of
// its process group when its grace is up.
func TestAStopReachesWhatTheCommandStarted(t *testing.T) {
	r := startRun(t, `(trap '' INT TERM; exec sleep 60) & trap '' INT; echo "pgid:$$"; wait`, fastStop, nil)
	pgid := waitForNumber(t, r.screen, "pgid:")
	r.pressTwice(t)
	if got := r.result(t, fastStop.term+fastStop.kill+5*time.Second); !got.Stopped {
		t.Errorf("result = %+v, want stopped", got)
	}
	waitGroupGone(t, pgid)
}

// TestAStopComesBackWhateverHoldsTheTerminal: the command ignores SIGTERM,
// and something it started has left its process group — out of reach of
// the stop — ignoring every signal that would end it, with the terminal
// held open. The list is back once SIGKILL has had its grace, and what is
// left gets the hangup of the terminal closing.
func TestAStopComesBackWhateverHoldsTheTerminal(t *testing.T) {
	r := startRun(t, `set -m; (trap '' INT TERM HUP; exec sleep 60) & echo "holder:$!"; trap '' INT TERM; echo ready; wait`, fastStop, nil)
	holder := waitForNumber(t, r.screen, "holder:")
	t.Cleanup(func() { _ = unix.Kill(-holder, unix.SIGKILL) })
	waitFor(t, r.screen, "ready")
	r.press(t, "\x03")
	r.stillRunning(t, 2*fastStop.again)
	asked := time.Now()
	r.press(t, "\x03")
	got := r.result(t, fastStop.term+fastStop.kill+5*time.Second)
	if !got.Stopped || got.Status != 128+9 {
		t.Errorf("result = %+v, want stopped by SIGKILL", got)
	}
	if took, bound := time.Since(asked), fastStop.term+fastStop.kill+time.Second; took > bound {
		t.Errorf("the list came back %v after the stop was asked for, want under %v", took, bound)
	}
}

// TestKeysTheCommandIsNotReadingDoNotHoldUpTheStop: a command whose terminal
// is raw and that reads nothing is typed at far past what its terminal will
// hold. The keys it has no room for wait in the list rather than in a write
// to the terminal, so a Ctrl+C typed after them is still read.
func TestKeysTheCommandIsNotReadingDoNotHoldUpTheStop(t *testing.T) {
	r := startRun(t, `stty raw -echo; echo ready; sleep 60`, fastStop, nil)
	waitFor(t, r.screen, "ready")
	// Typed from a goroutine, so that a list stuck behind the flood fails the
	// test by its deadline rather than hanging it on the write.
	go func() {
		_, _ = r.typing.Write(bytes.Repeat([]byte("x"), 1<<20))
		_, _ = r.typing.Write([]byte{ctrlC})
		time.Sleep(2 * fastStop.again)
		_, _ = r.typing.Write([]byte{ctrlC})
	}()
	if got := r.result(t, fastStop.term+fastStop.kill+10*time.Second); !got.Stopped {
		t.Errorf("result = %+v, want stopped", got)
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
	if got := k.waitKey(nil); got != "x" {
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
