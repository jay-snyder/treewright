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
)

var ansiCodes = map[Color]string{
	Green:  "\x1b[32m",
	Yellow: "\x1b[33m",
	Red:    "\x1b[31m",
	Cyan:   "\x1b[36m",
	Dim:    "\x1b[2m",
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
