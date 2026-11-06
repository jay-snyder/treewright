package cmdq

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestHiddenCharactersAreShownAsStandIns is the rule that nothing somebody
// else wrote reaches the terminal as an instruction: each hidden character
// comes out as a character that is merely drawn.
func TestHiddenCharactersAreShownAsStandIns(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"\x1b[2Jrm -rf ~", "␛[2Jrm -rf ~"},
		{"a\tb", "a␉b"},
		{"echo safe\rrm -rf ~", "echo safe␍rm -rf ~"},
		{"x\x08y", "x␈y"},
		{"\x00", "␀"},
		{"\x7f", "␡"},
		{"\u009b31m", "�31m"},       // C1 CSI, an escape on terminals that read 8-bit controls
		{"a\xffb", "a�b"},           // a byte that is not UTF-8
		{"sudo\u202erm", "sudo�rm"}, // right-to-left override
		{"\u200bx\u200f", "�x�"},
		{"\u2060\u2064\u2066\u2069\ufeff", "�����"},
		{"one\ntwo", "one\ntwo"},   // a newline stays one
		{"café 日本 ✓", "café 日本 ✓"}, // and so does everything visible
	} {
		if got := Visible(c.in); got != c.want {
			t.Errorf("Visible(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEveryHiddenCharacterIsCaught walks the ranges the rule names, so a
// boundary off by one fails here rather than in somebody's terminal.
func TestEveryHiddenCharacterIsCaught(t *testing.T) {
	var hidden []rune
	for r := range rune(0x20) {
		if r != '\n' {
			hidden = append(hidden, r)
		}
	}
	for _, span := range [][2]rune{{0x7f, 0x9f}, {0x200b, 0x200f}, {0x202a, 0x202e}, {0x2060, 0x2064}, {0x2066, 0x2069}, {0xfeff, 0xfeff}} {
		for r := span[0]; r <= span[1]; r++ {
			hidden = append(hidden, r)
		}
	}
	for _, r := range hidden {
		got := Visible(string(r))
		if got == string(r) || !isStandIn([]rune(got)[0]) {
			t.Errorf("U+%04X came out as %q", r, got)
		}
	}
	for _, r := range []rune{0x20, 0x7e, 0xa0, 0x200a, 0x2010, 0x2029, 0x202f, 0x2065, 0x206a, 0xfefe} {
		if got := Visible(string(r)); got != string(r) {
			t.Errorf("U+%04X, which hides nothing, came out as %q", r, got)
		}
	}
}

func TestStandInsAreMarkedAndNothingElseIs(t *testing.T) {
	mark := func(s string) string { return "[" + s + "]" }
	if got := markStandIns(Visible("a\x1bb\u202ec"), mark); got != "a[␛]b[�]c" {
		t.Errorf("marked = %q", got)
	}
	if got := markStandIns("plain", mark); got != "plain" {
		t.Errorf("marked = %q", got)
	}
}

func TestOneLineFlattensControlsAndTrims(t *testing.T) {
	if got := OneLine("  log in,\nthen\trefresh \x1b "); got != "log in, then refresh" {
		t.Errorf("OneLine = %q", got)
	}
	if got := OneLine("x\u202ey"); got != "x\u202ey" {
		t.Errorf("OneLine took a format character out rather than leaving it to be shown: %q", got)
	}
}

func TestTailStripsEscapesAndKeepsTheLastLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "\x1b[1;32mline %d\x1b[0m\r\n", i)
	}
	b.WriteString("\x1b]0;a title\x07\x1b(B\r\n\r\n")
	got := Tail([]byte(b.String()), false)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != KeepLines {
		t.Fatalf("kept %d lines, want %d:\n%s", len(lines), KeepLines, got)
	}
	if lines[0] != "line 11" || lines[len(lines)-1] != "line 30" {
		t.Errorf("kept %q … %q, want line 11 … line 30", lines[0], lines[len(lines)-1])
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("an escape survived: %q", got)
	}
}

// TestTailShowsALineAsTheTerminalLeftIt is a progress bar: each update returns
// to the start of the line and writes over it, and what was on screen at the
// end is the last of them.
func TestTailShowsALineAsTheTerminalLeftIt(t *testing.T) {
	got := Tail([]byte("downloading 10%\rdownloading 50%\rdownloading 100%\r\ndone\r\n"), false)
	if got != "downloading 100%\ndone\n" {
		t.Errorf("Tail = %q", got)
	}
}

func TestTailDropsTheFragmentAtTheStartOfWhatWasKept(t *testing.T) {
	if got := Tail([]byte("of a line\nwhole\n"), true); got != "whole\n" {
		t.Errorf("Tail = %q", got)
	}
	if got := Tail(nil, false); got != "" {
		t.Errorf("Tail(nil) = %q", got)
	}
}

func TestAgo(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{4 * time.Minute, "4m ago"},
		{3 * time.Hour, "3h ago"},
		{50 * time.Hour, "2d ago"},
	} {
		if got := Ago(now.Add(-c.d), now); got != c.want {
			t.Errorf("Ago(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestDescribeSaysHowAnEntryStands(t *testing.T) {
	now := time.Now()
	done := time.Date(2026, 11, 4, 10, 15, 0, 0, time.Local)
	for _, c := range []struct {
		e    Entry
		want string
	}{
		{Entry{Added: now.Add(-4 * time.Minute)}, "waiting — queued 4m ago"},
		{Entry{Added: now, Failed: now.Add(-time.Hour), Status: 3}, "waiting — last run failed: exit 3, 1h ago"},
		{Entry{Outcome: Ran, Done: done}, "ran, exit 0, 2026-11-04 10:15"},
		{Entry{Outcome: Dismissed, Done: done}, "dismissed by the person, 2026-11-04 10:15"},
		{Entry{Outcome: Withdrawn}, "withdrawn"},
	} {
		if got := Describe(c.e, now); got != c.want {
			t.Errorf("Describe = %q, want %q", got, c.want)
		}
	}
}
