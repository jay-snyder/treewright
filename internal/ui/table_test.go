package ui

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTableSizesColumnsToContent is the regression for the defect a fixed-width
// format caused: one value longer than the guess shifted every column after it
// on that row only, so the table stopped lining up on real data.
func TestTableSizesColumnsToContent(t *testing.T) {
	tbl := Table{Headers: []string{"SLUG", "STATUS", "WINDOW"}}
	tbl.Add(Text("short"), Text("active"), Text("-"))
	tbl.Add(Text("a-considerably-longer-slug-than-the-header"), Text("dirty"), Text("open"))
	tbl.Add(Text("mid-length-slug"), Text("merged"), Text("-"))

	header, rows := tbl.Lines(false)
	all := append([]string{header}, rows...)

	// Every line must start its second column at the same offset.
	want := strings.Index(header, "STATUS")
	for i, line := range all {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("line %d has too few columns: %q", i, line)
		}
		if got := strings.Index(line, fields[1]); got != want {
			t.Errorf("line %d starts column 2 at offset %d, want %d\n%q", i, got, want, line)
		}
	}
}

func TestTableHasNoTrailingWhitespace(t *testing.T) {
	tbl := Table{Headers: []string{"A", "B"}}
	tbl.Add(Text("long-value"), Text("x"))
	tbl.Add(Text("y"), Text("another-long-value"))

	header, rows := tbl.Lines(false)
	for _, line := range append([]string{header}, rows...) {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("line has trailing spaces: %q", line)
		}
	}
}

func TestTableAlignsWithColorOn(t *testing.T) {
	// Escape sequences occupy no columns. Measuring the styled string instead of
	// the text would over-pad exactly when color is enabled, so the plain and
	// colored layouts must agree once the codes are stripped.
	tbl := Table{Headers: []string{"SLUG", "STATUS"}}
	tbl.Add(Text("alpha"), Colored("merged", Green))
	tbl.Add(Text("a-much-longer-slug"), Colored("dirty", Yellow))

	_, plainRows := tbl.Lines(false)
	_, colorRows := tbl.Lines(true)

	for i := range plainRows {
		stripped := strings.NewReplacer(
			ansiCodes[Green], "", ansiCodes[Yellow], "", ansiReset, "",
		).Replace(colorRows[i])
		if stripped != plainRows[i] {
			t.Errorf("row %d: colored layout %q differs from plain %q", i, stripped, plainRows[i])
		}
	}
	if !strings.Contains(colorRows[0], ansiCodes[Green]) {
		t.Error("colored row carries no escape sequence")
	}
	if strings.Contains(plainRows[0], "\x1b") {
		t.Error("plain row carries an escape sequence")
	}
}

func TestTableCountsRunesNotBytes(t *testing.T) {
	tbl := Table{Headers: []string{"SLUG", "STATUS"}}
	tbl.Add(Text("café-ünïcode"), Text("active")) // 12 runes, more bytes
	tbl.Add(Text("plain"), Text("merged"))

	header, rows := tbl.Lines(false)
	// Offsets are compared in runes, since that is what a terminal renders in;
	// a byte offset would differ between these rows even when they line up.
	want := runeOffsetOf(header, "STATUS")
	for i, line := range rows {
		fields := strings.Fields(line)
		if got := runeOffsetOf(line, fields[1]); got != want {
			t.Errorf("row %d aligned at rune %d, want %d (byte length used instead of rune count)", i, got, want)
		}
	}
}

// runeOffsetOf returns how many runes precede the first occurrence of sub.
func runeOffsetOf(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return utf8.RuneCountInString(line[:i])
}

func TestRenderWritesHeaderThenRows(t *testing.T) {
	tbl := Table{Headers: []string{"A"}}
	tbl.Add(Text("one"))
	tbl.Add(Text("two"))

	var buf bytes.Buffer
	tbl.Render(&buf, false)
	if got := buf.String(); got != "A\none\ntwo\n" {
		t.Errorf("Render = %q", got)
	}
}

func TestColorApply(t *testing.T) {
	if got := Green.Apply("ok", true); got != "\x1b[32mok\x1b[0m" {
		t.Errorf("enabled = %q", got)
	}
	if got := Green.Apply("ok", false); got != "ok" {
		t.Errorf("disabled = %q", got)
	}
	if got := Plain.Apply("ok", true); got != "ok" {
		t.Errorf("Plain should never add codes, got %q", got)
	}
	if got := Green.Apply("", true); got != "" {
		t.Errorf("empty text should stay empty, got %q", got)
	}
}

func TestColorEnabled(t *testing.T) {
	// A bytes.Buffer is not a terminal, so color must stay off regardless of the
	// environment — this is what keeps escape codes out of pipes and files.
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	if ColorEnabled(&bytes.Buffer{}) {
		t.Error("color enabled for a non-terminal writer")
	}

	t.Setenv("NO_COLOR", "1")
	if ColorEnabled(&bytes.Buffer{}) {
		t.Error("color enabled with NO_COLOR set")
	}
}
