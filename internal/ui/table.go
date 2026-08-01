package ui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Cell is one table entry: its text, and how to display it.
type Cell struct {
	Text  string
	Color Color
}

// Text makes an unstyled cell.
func Text(s string) Cell { return Cell{Text: s} }

// Colored makes a styled cell.
func Colored(s string, c Color) Cell { return Cell{Text: s, Color: c} }

// Table lays out rows in columns sized to their contents.
//
// Sizing from content is the point: a fixed column width silently misaligns
// every subsequent column as soon as one value is longer than the guess, and
// worktree slugs have no bounded length.
//
// A cell whose text holds newlines spans several lines of the rendered table:
// its first line sits in the row, and each later one on a line of its own in
// the same column. That is what lets a doctor finding say what to do about
// itself underneath itself, and a carry_files list stand as a list, rather than
// either becoming a 180-column row no terminal shows whole. The alternative was
// to let those two commands lay their own text out beside the table, which is
// how a column stops lining up with the column above it.
//
// Only Render and the plain half of Lines lay a spanning cell out. The picker
// numbers each row it is handed, so a row arriving as two lines would leave the
// second unnumbered and hanging off the left margin — the tables it draws hold
// no spanning cell, and this is the reason they must not start.
type Table struct {
	Headers []string
	Rows    [][]Cell
}

// Add appends a row.
func (t *Table) Add(cells ...Cell) { t.Rows = append(t.Rows, cells) }

const gutter = "  "

// Lines renders the table as a header line plus one entry per row — one line
// each, unless some cell spans more, in which case that row's entry carries the
// newlines between them.
//
// Returning lines rather than writing them lets the same table feed both plain
// output and the interactive picker, which needs each row as a separate label.
//
// Padding is computed from cell text only, never from the styled string: escape
// sequences occupy no columns, so measuring them would break alignment exactly
// when color is on.
func (t *Table) Lines(color bool) (header string, rows []string) {
	widths := t.widths()

	headerCells := make([]Cell, len(t.Headers))
	for i, h := range t.Headers {
		headerCells[i] = Text(h)
	}
	header = renderRow(headerCells, widths, color)

	rows = make([]string, 0, len(t.Rows))
	for _, row := range t.Rows {
		rows = append(rows, renderRow(row, widths, color))
	}
	return header, rows
}

// Render writes the table, header first.
func (t *Table) Render(w io.Writer, color bool) {
	header, rows := t.Lines(color)
	fmt.Fprintln(w, header)
	for _, row := range rows {
		fmt.Fprintln(w, row)
	}
}

// widths returns the display width of each column: the widest of its header and
// its cells, counted in runes rather than bytes so multi-byte text still aligns.
//
// The column count is settled before any width is measured, because a row may
// carry more cells than there are headers — the picker's index column is one — and
// growing the slice as such a row is met means appending past entries that are
// already set, which reads as the bug it would be if the order ever changed.
func (t *Table) widths() []int {
	columns := len(t.Headers)
	for _, row := range t.Rows {
		columns = max(columns, len(row))
	}

	widths := make([]int, columns)
	for i, h := range t.Headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			widths[i] = max(widths[i], cellWidth(cell.Text))
		}
	}
	return widths
}

// cellWidth is how many columns a cell occupies: its widest line, since a cell
// may span several. Measuring the whole text instead would count the newlines
// and every line after the first into one column's width, which is the width of
// nothing that is ever printed.
func cellWidth(text string) int {
	width := 0
	for line := range strings.SplitSeq(text, "\n") {
		width = max(width, utf8.RuneCountInString(line))
	}
	return width
}

// renderRow lays out one row, which is one line unless some cell spans more.
//
// Each line of the row is assembled from the matching line of every cell, so a
// cell with nothing left to say contributes the empty string and its column
// stays a column. Trailing spaces are trimmed per line rather than by refusing
// to pad the last cell: with a spanning cell anywhere but the end, the padding
// that lines the next column up is itself what would be left dangling.
func renderRow(cells []Cell, widths []int, color bool) string {
	height := 1
	for _, cell := range cells {
		height = max(height, strings.Count(cell.Text, "\n")+1)
	}

	lines := make([]string, 0, height)
	for line := range height {
		var b strings.Builder
		for i, cell := range cells {
			if i > 0 {
				b.WriteString(gutter)
			}
			text := cellLine(cell.Text, line)
			b.WriteString(cell.Color.Apply(text, color))
			if i < len(cells)-1 && i < len(widths) {
				if pad := widths[i] - utf8.RuneCountInString(text); pad > 0 {
					b.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(lines, "\n")
}

// cellLine returns the nth line of a cell's text, or "" once it has run out.
func cellLine(text string, n int) string {
	for i, line := range strings.Split(text, "\n") {
		if i == n {
			return line
		}
	}
	return ""
}
