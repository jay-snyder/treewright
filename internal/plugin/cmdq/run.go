package cmdq

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Running a command, in the popup, under a terminal of its own.
//
// The person runs a queued command from the list, and the command has to get
// what it would get typed at a prompt: a terminal, so `sudo` asks for a
// password the way it always does and a login flow can draw what it draws.
// The popup's own terminal cannot be handed over — the list is reading it, and
// the list has to come back afterwards with what the command printed kept for
// `status`. So the command gets a pseudo-terminal of its own, its output is
// copied to the popup and kept as it goes, and the person's keys are copied
// the other way. That is what the script's python did, and docs/plugins.md
// says why it is done here rather than in a tmux pane.

// openPTY opens a pseudo-terminal pair: the side this process keeps, and the
// side a command is given as its terminal. Both are opened without becoming
// this process's controlling terminal, which would hand a process that is not
// a session leader nothing, and one that is the wrong terminal.
func openPTY() (keep, give *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	name, err := ptyName(fd)
	if err != nil {
		_ = unix.Close(fd)
		return nil, nil, err
	}
	give, err = os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = unix.Close(fd)
		return nil, nil, err
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), give, nil
}

// Keyboard is the one reader of the person's keys, for as long as the list is
// open. Between runs the list reads from it; during one the command does.
//
// One reader rather than one per owner, because a read cannot be taken back:
// a goroutine the run had left blocked on the terminal would swallow the first
// key typed at the list afterwards, and send it to a command that has ended.
type Keyboard struct {
	chunks  chan []byte
	pending []byte // read off a chunk and not yet used
	eof     bool
}

// NewKeyboard starts reading keys from r. It reads until r ends, and nothing
// stops it sooner: the program it serves exits when the list closes.
func NewKeyboard(r io.Reader) *Keyboard {
	k := &Keyboard{chunks: make(chan []byte, 64)}
	go func() {
		defer close(k.chunks)
		for {
			buf := make([]byte, 4096)
			n, err := r.Read(buf)
			if n > 0 {
				k.chunks <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	return k
}

// next is the next byte typed, waiting at most wait for it. ok is false when
// none came in time, or when the keys have ended, which eof then says.
func (k *Keyboard) next(wait time.Duration) (b byte, ok bool) {
	if len(k.pending) == 0 && !k.fill(wait) {
		return 0, false
	}
	b, k.pending = k.pending[0], k.pending[1:]
	return b, true
}

// fill waits at most wait for more keys.
func (k *Keyboard) fill(wait time.Duration) bool {
	if k.eof {
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case chunk, open := <-k.chunks:
		if !open {
			k.eof = true
			return false
		}
		k.pending = append(k.pending, chunk...)
		return true
	case <-timer.C:
		return false
	}
}

// Drain throws away every key typed until none has arrived for quiet: the
// keys somebody typed while a command ran, which were the command's to read
// and must not answer the list's next question.
func (k *Keyboard) Drain(quiet time.Duration) {
	k.pending = nil
	for k.fill(quiet) {
		k.pending = nil
	}
}

// Run is one queued command run under a terminal of its own.
type Run struct {
	Command string
	Dir     string

	Keys   *Keyboard
	Screen io.Writer // where the command's output is shown as it arrives

	// Size reads the person's terminal size, for the command's terminal to
	// match, and Resized says it changed. Either may be nil: the command's
	// terminal then keeps the size it was opened with.
	Size    func() (*unix.Winsize, bool)
	Resized <-chan os.Signal
}

// Result is how a run went.
type Result struct {
	Status int    // the exit status, or 128 plus the signal that ended it
	Output string // what Tail made of the output, for status
}

// afterExit bounds how long the output is read once the command itself has
// exited. Something it left running in the background can hold its terminal
// open, and print on, for ever; what it printed in this time is shown, and
// then the list comes back, as the script's did.
const afterExit = 3 * time.Second

// Do runs the command and returns once it has exited and its output stopped.
//
// It runs under bash, in the entry's directory, as the leader of a session of
// its own whose controlling terminal is the one opened for it — the shape a
// shell gives a command at a prompt, and what makes Ctrl+C typed at the popup
// reach the command: the key is copied to the command's terminal, whose line
// discipline signals the command's own process group, and this process is in
// another session altogether.
func (r Run) Do() (Result, error) {
	keep, give, err := openPTY()
	if err != nil {
		return Result{}, err
	}
	defer keep.Close()
	resize := func() {
		if r.Size == nil {
			return
		}
		if ws, ok := r.Size(); ok {
			_ = unix.IoctlSetWinsize(int(keep.Fd()), unix.TIOCSWINSZ, ws)
		}
	}
	resize()

	cmd := exec.Command("bash", "-c", r.Command)
	cmd.Dir = r.Dir
	cmd.Env = commandEnv()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = give, give, give
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	err = cmd.Start()
	// The command has its own copy now. Ours closed, the terminal reads as
	// ended once the command and whatever it started have let go of it.
	_ = give.Close()
	if err != nil {
		return Result{}, err
	}

	// Keys already read off the keyboard first: they were typed before the
	// command began, and a terminal would have kept them for it.
	if r.Keys != nil && len(r.Keys.pending) > 0 {
		_, _ = keep.Write(r.Keys.pending)
		r.Keys.pending = nil
	}
	var (
		kept     tail
		exited   atomic.Bool
		finished = make(chan struct{})
		output   sync.WaitGroup
		input    sync.WaitGroup
	)
	output.Go(func() { copyOutput(int(keep.Fd()), r.Screen, &kept, &exited) })
	input.Go(func() { forwardKeys(r.Keys, keep, finished) })
	input.Go(func() {
		for {
			select {
			case <-r.Resized:
				resize()
			case <-finished:
				return
			}
		}
	})

	err = cmd.Wait()
	exited.Store(true)
	output.Wait()
	// Both are waited for, so nothing still holds a key or the terminal once
	// the list has them back.
	close(finished)
	input.Wait()

	status := 0
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		status = exit.ExitCode()
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			status = 128 + int(ws.Signal())
		}
	} else if err != nil {
		return Result{}, err
	}
	data, truncated := kept.bytes()
	return Result{Status: status, Output: Tail(data, truncated)}, nil
}

// commandEnv is this process's environment, less what says it is running in
// a popup. That is true of the list and not of the command: a treewright the
// command runs would otherwise end every failure by telling the person to
// press Escape at a terminal that is not a popup.
func commandEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "TREEWRIGHT_POPUP=")
	})
}

// copyOutput copies what the command prints to the screen, keeping the tail
// of it. It stops when the command's terminal ends, or once the command has
// exited and its terminal has gone quiet, or afterExit after that at most.
//
// A wait with a timeout rather than a plain read, because a plain read cannot
// be called off: something the command left in the background, holding its
// terminal, would keep it blocked after the list had come back.
func copyOutput(fd int, screen io.Writer, kept *tail, exited *atomic.Bool) {
	buf := make([]byte, 32<<10)
	var exitedAt time.Time
	for {
		ready, err := readable(fd, 50*time.Millisecond)
		if err != nil {
			return
		}
		if exited.Load() {
			if exitedAt.IsZero() {
				exitedAt = time.Now()
			}
			if !ready || time.Since(exitedAt) > afterExit {
				return
			}
		}
		if !ready {
			continue
		}
		n, err := unix.Read(fd, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if n > 0 {
			_, _ = screen.Write(buf[:n])
			kept.add(buf[:n])
		}
		// EIO, on Linux, is the terminal with nobody left holding its other
		// side; a read of nothing is the same on macOS.
		if err != nil || n == 0 {
			return
		}
	}
}

// readable waits at most wait for fd to have something to read. select
// rather than poll: macOS's poll does not take a terminal device.
func readable(fd int, wait time.Duration) (bool, error) {
	for {
		var set unix.FdSet
		set.Set(fd)
		timeout := unix.NsecToTimeval(wait.Nanoseconds())
		n, err := unix.Select(fd+1, &set, nil, nil, &timeout)
		if errors.Is(err, unix.EINTR) {
			// A resize, most likely: the signal interrupts the wait, and
			// waiting a little less than asked is harmless.
			continue
		}
		if err != nil {
			return false, err
		}
		return n > 0 && set.IsSet(fd), nil
	}
}

// forwardKeys copies the person's keys to the command's terminal until the
// run is over.
func forwardKeys(k *Keyboard, to io.Writer, finished <-chan struct{}) {
	if k == nil {
		return
	}
	for {
		select {
		case chunk, open := <-k.chunks:
			if !open {
				return
			}
			_, _ = to.Write(chunk)
		case <-finished:
			return
		}
	}
}

// tail keeps the last KeepBytes of a run's output, and whether anything
// before that was let go.
type tail struct {
	buf       []byte
	truncated bool
}

func (t *tail) add(p []byte) {
	t.buf = append(t.buf, p...)
	// Trimmed at twice the size kept, so the copy happens once per KeepBytes
	// written rather than on every write.
	if len(t.buf) > 2*KeepBytes {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-KeepBytes:]...)
		t.truncated = true
	}
}

func (t *tail) bytes() ([]byte, bool) {
	if len(t.buf) > KeepBytes {
		return t.buf[len(t.buf)-KeepBytes:], true
	}
	return t.buf, t.truncated
}
