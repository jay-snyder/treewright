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
//
// What the script could not do is give the popup back from a command that
// will not end. The popup lasts exactly as long as the list does, and the list
// was waiting on the command, so a command that ignored Ctrl+C held the
// person's popup for as long as it liked. A second Ctrl+C is now the list's:
// see ctrlCs and stopGroup, and "Stopping a command that will not stop" in
// docs/plugins.md.

// openPTY opens a pseudo-terminal pair: the side this process keeps, as a
// descriptor, and the side a command is given as its terminal. Both are opened
// without becoming this process's controlling terminal, which would hand a
// process that is not a session leader nothing, and one that is the wrong
// terminal.
//
// The side kept is non-blocking. Its reads wait in select already; its writes
// have to be able to come back without having written, because a command that
// has stopped reading its terminal fills it, and a write waiting for room
// there would leave every key typed after it unread — the Ctrl+C that stops
// the command among them.
func openPTY() (keep int, give *os.File, err error) {
	keep, err = unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, nil, err
	}
	name, err := ptyName(keep)
	if err != nil {
		_ = unix.Close(keep)
		return -1, nil, err
	}
	give, err = os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = unix.Close(keep)
		return -1, nil, err
	}
	return keep, give, nil
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

	// Quit is closed when the list has been told to close from outside: tmux
	// closing the popup, or a signal. A command still running is stopped as a
	// second Ctrl+C would stop it, so the list can record that before it goes
	// and nothing the command started outlives the popup unseen. Nil is a run
	// only the person can stop.
	Quit <-chan struct{}

	timing stopping // the zero value is defaultStopping; the tests' is quicker
}

// Result is how a run went.
type Result struct {
	Status int    // the exit status, or 128 plus the signal that ended it
	Output string // what Tail made of the output, for status

	// Stopped says the command did not end on its own: the person stopped it
	// with a second Ctrl+C, or the list was closed under it. Status is then
	// what it died of — or, for one that had not been seen to end when the
	// list gave up waiting, the SIGKILL it will die of.
	Stopped bool
}

// stopping is how a command the person has given up on is stopped: when a
// Ctrl+C is the person's rather than the command's, and how long the command
// is given at each step. docs/plugins.md argues the numbers.
type stopping struct {
	// again is how long the command has to have gone on running after the
	// first Ctrl+C before another stops it. One typed sooner goes to the
	// command, as the first did: it is a person pressing twice on purpose.
	again time.Duration
	// term is how long SIGTERM is given to end the command and everything in
	// its process group, and kill how long SIGKILL is given after it. When
	// kill is up the list comes back, whatever is still running.
	term, kill time.Duration
}

// defaultStopping is how a run is stopped outside the tests.
var defaultStopping = stopping{again: time.Second, term: 2 * time.Second, kill: time.Second}

// afterExit bounds how long the output is read once the command itself has
// exited. Something it left running in the background can hold its terminal
// open, and print on, for ever; what it printed in this time is shown, and
// then the list comes back, as the script's did.
const afterExit = 3 * time.Second

// afterStop is the same bound for a command that was stopped. What it printed
// on its way out arrived while it was being stopped and is on screen already,
// and the person asked for the list back: anything still holding the terminal
// gets as long as it takes to read what is waiting, and no longer.
const afterStop = 100 * time.Millisecond

// Do runs the command and returns once it has exited and its output stopped,
// or once it has been stopped.
//
// It runs under bash, in the entry's directory, as the leader of a session of
// its own whose controlling terminal is the one opened for it — the shape a
// shell gives a command at a prompt, and what makes Ctrl+C typed at the popup
// reach the command: the key is copied to the command's terminal, whose line
// discipline signals the command's own process group, and this process is in
// another session altogether. It is also what makes the command stoppable
// whole: everything it starts is in that group unless it leaves on purpose,
// and the group's number is the command's pid.
func (r Run) Do() (Result, error) {
	timing := r.timing
	if timing == (stopping{}) {
		timing = defaultStopping
	}
	keep, give, err := openPTY()
	if err != nil {
		return Result{}, err
	}
	// Closed last, and only once everything that reads or writes it has been
	// waited for: a descriptor closed under a goroutine still using it is a
	// number the next file opened can be given. Closing it hangs up whatever
	// still holds the command's terminal.
	defer func() { _ = unix.Close(keep) }()
	resize := func() {
		if r.Size == nil {
			return
		}
		if ws, ok := r.Size(); ok {
			_ = unix.IoctlSetWinsize(keep, unix.TIOCSWINSZ, ws)
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
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	var (
		kept     tail
		linger   atomic.Int64 // how long output is still read: zero while the command runs
		asked    = make(chan struct{})
		askOnce  sync.Once
		output   = make(chan struct{})
		finished = make(chan struct{})
		input    sync.WaitGroup
	)
	go func() {
		defer close(output)
		copyOutput(keep, r.Screen, &kept, &linger)
	}()
	// Keys already read off the keyboard go first: they were typed before the
	// command began, and a terminal would have kept them for it.
	var early []byte
	if r.Keys != nil {
		early, r.Keys.pending = r.Keys.pending, nil
	}
	presses := ctrlCs{again: timing.again}
	input.Go(func() {
		forwardKeys(r.Keys, keep, early, func() bool {
			if !presses.stops(time.Now()) {
				return false
			}
			askOnce.Do(func() { close(asked) })
			return true
		}, finished)
	})
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

	var (
		waitErr error
		ended   = true
		stopped bool
	)
	select {
	case waitErr = <-waited:
	case <-asked:
		stopped = true
	case <-r.Quit:
		stopped = true
	}
	if stopped {
		ended, waitErr = stopGroup(cmd.Process.Pid, waited, timing)
		linger.Store(int64(afterStop))
	} else {
		linger.Store(int64(afterExit))
	}
	// A command that exited by itself can leave something holding its
	// terminal and printing on, which is shown for afterExit — unless the
	// person asks for the list back meanwhile, which cuts it short.
	select {
	case <-output:
	case <-asked:
		linger.Store(int64(afterStop))
		<-output
	case <-r.Quit:
		linger.Store(int64(afterStop))
		<-output
	}
	// Both are waited for, so nothing still holds a key or the terminal once
	// the list has them back.
	close(finished)
	input.Wait()

	status := 128 + int(unix.SIGKILL)
	if ended {
		status = 0
		if exit, ok := errors.AsType[*exec.ExitError](waitErr); ok {
			status = exit.ExitCode()
			if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				status = 128 + int(ws.Signal())
			}
		} else if waitErr != nil {
			return Result{}, waitErr
		}
	}
	data, truncated := kept.bytes()
	return Result{Status: status, Output: Tail(data, truncated), Stopped: stopped}, nil
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

// ctrlC is the byte Ctrl+C types.
const ctrlC = 0x03

// ctrlCs decides whose each Ctrl+C typed during a run is. The first is the
// command's, always: it is the ordinary interrupt, and a command that stops
// when asked must see nothing different. A later one is the person's, and
// stops the command, once again has passed since that first one — so a person
// pressing twice on purpose, at a tool that asks again on Ctrl+C, is still
// talking to the tool.
//
// Nothing starts the count again. A command that ignored the first Ctrl+C,
// was waited on and was pressed at again has to stop, however long the wait
// was, and anything that can tell a command that answered the first press
// from one that did not — output since, a key since — is something a stuck
// command does too: a raw terminal echoes the very byte.
type ctrlCs struct {
	again time.Duration
	first time.Time
}

// stops reports whether a Ctrl+C typed at now stops the command rather than
// going to it.
func (c *ctrlCs) stops(now time.Time) bool {
	if c.first.IsZero() {
		c.first = now
		return false
	}
	return now.Sub(c.first) >= c.again
}

// groupPoll is how often a stop looks to see whether the command's process
// group has gone.
const groupPoll = 10 * time.Millisecond

// stopGroup stops the command and everything in its process group, and
// reports whether the command was seen to end and, if it was, what waiting on
// it said. SIGTERM first, which a
// command can clean up after; SIGKILL, to whatever is left, once term is up;
// and once kill is up it returns, whatever happened. What is left by then is
// in an uninterruptible sleep, or left the group on purpose, and neither is a
// reason to keep the popup. The goroutine waiting on the command goes on
// waiting, and reaps it whenever it does end.
//
// The group's number is the command's pid, which Setsid made it.
func stopGroup(pid int, waited <-chan error, s stopping) (ended bool, err error) {
	// gone waits at most grace for the command to be reaped and its group to
	// empty, taking what the command ended with as it arrives.
	gone := func(grace time.Duration) bool {
		deadline := time.NewTimer(grace)
		defer deadline.Stop()
		poll := time.NewTicker(groupPoll)
		defer poll.Stop()
		for {
			select {
			case err = <-waited:
				ended, waited = true, nil
			case <-poll.C:
			case <-deadline.C:
				return false
			}
			if ended && !groupAlive(pid) {
				return true
			}
		}
	}
	for _, step := range []struct {
		signal unix.Signal
		grace  time.Duration
	}{{unix.SIGTERM, s.term}, {unix.SIGKILL, s.kill}} {
		// Signalled only while the group is seen to exist: once the command
		// is reaped and the group empty, its number is free to be somebody
		// else's.
		if !ended || groupAlive(pid) {
			_ = unix.Kill(-pid, step.signal)
		}
		if gone(step.grace) {
			return true, err
		}
	}
	return ended, err
}

// groupAlive reports whether anything is left in a process group: EPERM is a
// member this process may not signal, which is still a member — a command
// sudo runs as root, say.
func groupAlive(pgid int) bool {
	err := unix.Kill(-pgid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

// copyOutput copies what the command prints to the screen, keeping the tail
// of it. It stops when the command's terminal ends; and once the run is over,
// which linger says by holding how much longer output is worth reading, at the
// first quiet moment or when that time is up.
//
// A wait with a timeout rather than a plain read, because a plain read cannot
// be called off: something the command left in the background, holding its
// terminal, would keep it blocked after the list had come back.
func copyOutput(fd int, screen io.Writer, kept *tail, linger *atomic.Int64) {
	buf := make([]byte, 32<<10)
	var overAt time.Time
	for {
		ready, err := readable(fd, 50*time.Millisecond)
		if err != nil {
			return
		}
		if limit := time.Duration(linger.Load()); limit > 0 {
			if overAt.IsZero() {
				overAt = time.Now()
			}
			if !ready || time.Since(overAt) > limit {
				return
			}
		}
		if !ready {
			continue
		}
		n, err := unix.Read(fd, buf)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
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

// maxUnsent bounds the keys held for a command that is not reading its
// terminal. Past it they are dropped, as a terminal drops what its input
// queue has no room for.
const maxUnsent = 64 << 10

// resendEvery is how often keys a full terminal turned away are offered to it
// again.
const resendEvery = 20 * time.Millisecond

// forwardKeys copies the person's keys to the command's terminal until the
// run is over, starting with early, which were typed before it began. Each
// Ctrl+C goes past stop first, which says whether it stops the command rather
// than going to it; once one has, nothing more is sent.
//
// Keys are read off the keyboard as they arrive whether or not the command is
// reading them. Those its terminal has no room for wait here, offered again
// every resendEvery, rather than in a write: a write that waited would leave
// the next Ctrl+C unread behind it, and a stuck command is the one most likely
// to have stopped reading.
func forwardKeys(k *Keyboard, fd int, early []byte, stop func() bool, finished <-chan struct{}) {
	if k == nil {
		return
	}
	var unsent []byte
	stopping := false
	take := func(chunk []byte) {
		for _, b := range chunk {
			if stopping {
				return
			}
			if b == ctrlC && stop() {
				stopping, unsent = true, nil
				return
			}
			if len(unsent) < maxUnsent {
				unsent = append(unsent, b)
			}
		}
	}
	take(early)
	resend := time.NewTicker(resendEvery)
	defer resend.Stop()
	for {
		unsent = send(fd, unsent)
		var again <-chan time.Time
		if len(unsent) > 0 {
			again = resend.C
		}
		select {
		case chunk, open := <-k.chunks:
			if !open {
				return
			}
			take(chunk)
		case <-again:
		case <-finished:
			return
		}
	}
}

// send writes what it can of keys to the command's terminal and returns what
// the terminal had no room for. Any other failure drops them all: the
// terminal has gone, and whoever would have read them with it.
func send(fd int, keys []byte) []byte {
	for len(keys) > 0 {
		n, err := unix.Write(fd, keys)
		switch {
		case n > 0:
			keys = keys[n:]
		case errors.Is(err, unix.EINTR):
		case errors.Is(err, unix.EAGAIN):
			return keys
		default:
			return nil
		}
	}
	return nil
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
