package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// Color is a display attribute for one piece of text.
type Color uint8

// The attributes treewright uses. Plain is the zero value, so a Cell that names no
// color renders as the terminal's own foreground rather than as a chosen one.
const (
	Plain Color = iota
	Green
	Yellow
	Red
	Cyan
	Dim

	// Bold and Reverse are the cmdq plugin's list: Bold for who asked, and
	// Reverse for the stand-in a hidden character is shown as, which has to
	// be found on a line by somebody about to run it.
	Bold
	Reverse
)

var ansiCodes = map[Color]string{
	Green:   "\x1b[32m",
	Yellow:  "\x1b[33m",
	Red:     "\x1b[31m",
	Cyan:    "\x1b[36m",
	Dim:     "\x1b[2m",
	Bold:    "\x1b[1m",
	Reverse: "\x1b[7m",
}

const ansiReset = "\x1b[0m"

// Apply wraps s in this color's escape codes, or returns it unchanged when
// color is off. Callers decide "on" once, per output stream, via ColorEnabled.
func (c Color) Apply(s string, enabled bool) string {
	code, ok := ansiCodes[c]
	if !enabled || !ok || s == "" {
		return s
	}
	return code + s + ansiReset
}

// ColorEnabled reports whether ANSI color is appropriate for w.
//
// Three conditions must hold, and each rules out a way color goes wrong:
// NO_COLOR must be unset or empty (the no-color.org convention, which any value
// honors), TERM must not be "dumb", and w must be a terminal — so redirected or
// piped output never carries escape sequences into a file or a parser.
func ColorEnabled(w io.Writer) bool {
	if v, set := os.LookupEnv("NO_COLOR"); set && v != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Width is how many columns the terminal behind w has, or 0 when w is not a
// terminal — a pipe has no width, and nothing should be cut to fit one.
func Width(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return width
}
