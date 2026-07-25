// Package ui renders treemux's interactive terminal pieces.
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/term"
)

// ErrCancelled reports that the user dismissed the picker, or that there was no
// terminal to prompt on. Callers treat it as "do nothing", not as a failure.
var ErrCancelled = errors.New("cancelled")

// decision is what the picker should do after the digits typed so far.
type decision int

const (
	decideWait   decision = iota // more digits could still form a larger valid index
	decideCommit                 // the buffer is final and in range
	decideCancel                 // the buffer is final and out of range
)

// decide is the picker's selection rule, kept free of I/O so it can be tested
// directly. Given a menu of n items and the digits typed so far, it decides
// whether to keep reading.
//
// The point is to select without requiring Enter: as soon as no additional
// digit could produce a larger valid index, the buffer is final. With 3 items,
// "2" is final immediately because "2x" would exceed 3. With 12 items, "1"
// must wait, because "12" is still reachable.
//
// The returned index is 1-based, matching what the user typed.
func decide(n int, buf string) (decision, int) {
	if buf == "" {
		return decideWait, 0
	}
	v, err := strconv.Atoi(buf)
	if err != nil {
		return decideCancel, 0
	}
	// Short-circuit order matters: when v is already past n, v*10 is never
	// evaluated, so an absurdly long digit run cannot overflow.
	if v > n || v*10 > n {
		if v >= 1 && v <= n {
			return decideCommit, v
		}
		return decideCancel, 0
	}
	return decideWait, 0
}

// Key codes read in raw mode.
const (
	keyEsc       = 0x1b
	keyBackspace = 0x7f
	keyCtrlH     = 0x08
	keyCtrlC     = 0x03
)

// pick runs the menu against an arbitrary key source, which is what makes the
// whole loop — including the index conversion below — testable without a tty.
//
// It returns a ZERO-based index, because that is what indexes a Go slice, while
// everything the user sees and types is 1-based. That conversion happens in
// exactly one place, marked below.
//
// Lines end in \r\n because the caller has put the terminal in raw mode, where
// a bare \n moves down without returning to column zero.
func pick(w io.Writer, header string, items []string, nextKey func() (byte, error)) (int, error) {
	n := len(items)
	if n == 0 {
		return 0, ErrCancelled
	}

	width := len(strconv.Itoa(n)) // digit width of the largest index
	if header != "" {
		// Indent the header by the width of the "<n>) " prefix so its columns
		// line up with the rows beneath it.
		fmt.Fprintf(w, "%*s%s\r\n", width+2, "", header)
	}
	for i, item := range items {
		fmt.Fprintf(w, "%*d) %s\r\n", width, i+1, item)
	}
	fmt.Fprintf(w, "\r\nselect 1-%d (Esc to cancel): ", n)

	var typed string
	for {
		key, err := nextKey()
		if err != nil {
			// EOF or a read failure: there is no way to finish the prompt.
			fmt.Fprint(w, "\r\n")
			return 0, ErrCancelled
		}

		switch {
		case key == keyEsc || key == keyCtrlC:
			// Esc alone, or the first byte of an escape sequence such as an
			// arrow key. Either way the user is not typing a number.
			fmt.Fprint(w, "\r\n")
			return 0, ErrCancelled

		case key == '\r' || key == '\n':
			fmt.Fprint(w, "\r\n")
			// Enter accepts whatever is typed if it is in range, including the
			// still-waiting case: with 12 items, "1" then Enter selects 1 rather
			// than holding out for a possible "12". Deciding afresh here rather
			// than reusing decide is what makes that work.
			if v, err := strconv.Atoi(typed); err == nil && v >= 1 && v <= n {
				return v - 1, nil // the sole 1-based to 0-based conversion
			}
			return 0, ErrCancelled

		case key >= '0' && key <= '9':
			typed += string(key)
			w.Write([]byte{key})
			switch d, v := decide(n, typed); d {
			case decideCommit:
				fmt.Fprint(w, "\r\n")
				return v - 1, nil
			case decideCancel:
				fmt.Fprint(w, "\r\n")
				return 0, ErrCancelled
			}

		case key == keyBackspace || key == keyCtrlH:
			if typed != "" {
				typed = typed[:len(typed)-1]
				fmt.Fprint(w, "\b \b") // erase the echoed digit
			}
		}
	}
}

// Pick shows a numbered menu on w and returns the zero-based index the user
// chose, or ErrCancelled.
//
// Keys are read from /dev/tty rather than stdin so the picker still works when
// stdin has been redirected. With no controlling terminal there is nobody to
// prompt, so it cancels instead of hanging.
func Pick(w io.Writer, header string, items []string) (int, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, ErrCancelled
	}
	defer tty.Close()

	fd := int(tty.Fd())
	if !term.IsTerminal(fd) {
		return 0, ErrCancelled
	}
	// Raw mode delivers each keypress immediately instead of buffering a whole
	// line, which is what allows selection without pressing Enter.
	state, err := term.MakeRaw(fd)
	if err != nil {
		return 0, ErrCancelled
	}
	defer term.Restore(fd, state)

	one := make([]byte, 1)
	nextKey := func() (byte, error) {
		if _, err := tty.Read(one); err != nil {
			return 0, err
		}
		return one[0], nil
	}
	return pick(w, header, items, nextKey)
}
