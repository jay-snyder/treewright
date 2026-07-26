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
type Table struct {
	Headers []string
	Rows    [][]Cell
}

// Add appends a row.
func (t *Table) Add(cells ...Cell) { t.Rows = append(t.Rows, cells) }

const gutter = "  "

// Lines renders the table as a header line plus one line per row.
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
			widths[i] = max(widths[i], utf8.RuneCountInString(cell.Text))
		}
	}
	return widths
}

func renderRow(cells []Cell, widths []int, color bool) string {
	var b strings.Builder
	for i, cell := range cells {
		if i > 0 {
			b.WriteString(gutter)
		}
		styled := cell.Color.Apply(cell.Text, color)
		b.WriteString(styled)
		// The final column is never padded, so no line carries trailing spaces.
		if i < len(cells)-1 && i < len(widths) {
			if pad := widths[i] - utf8.RuneCountInString(cell.Text); pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
	}
	return b.String()
}
