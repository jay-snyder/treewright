package ui

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// TestDecide covers the selection rule directly, mirroring the cases the zsh
// suite asserted on _treemux_pick_decide.
func TestDecide(t *testing.T) {
	tests := []struct {
		name  string
		n     int
		buf   string
		want  decision
		index int
	}{
		{"single digit is final", 3, "2", decideCommit, 2},
		{"first of three is final", 3, "1", decideCommit, 1},
		{"out of range cancels", 3, "9", decideCancel, 0},
		{"empty waits", 3, "", decideWait, 0},
		{"growable waits", 12, "1", decideWait, 0},
		{"two digits commit", 12, "12", decideCommit, 12},
		{"unambiguous digit commits", 12, "2", decideCommit, 2},
		{"upper boundary commits", 25, "25", decideCommit, 25},
		{"past boundary cancels", 25, "26", decideCancel, 0},
		{"zero alone waits", 3, "0", decideWait, 0},
		{"leading zero resolves", 3, "02", decideCommit, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, i := decide(tc.n, tc.buf)
			if d != tc.want || i != tc.index {
				t.Errorf("decide(%d, %q) = (%v, %d), want (%v, %d)",
					tc.n, tc.buf, d, i, tc.want, tc.index)
			}
		})
	}
}

// keys turns a string into a key source, so the whole picker loop can be driven
// without a terminal. Running out of keys behaves like EOF.
func keys(s string) func() (byte, error) {
	i := 0
	return func() (byte, error) {
		if i >= len(s) {
			return 0, io.EOF
		}
		b := s[i]
		i++
		return b, nil
	}
}

// TestPickReturnsZeroBasedIndex is the test that matters most in this package.
// Everything the user sees is 1-based and everything Go indexes is 0-based, and
// getting that boundary wrong would silently open the wrong worktree — the
// exact hazard of porting 1-indexed zsh arrays.
func TestPickReturnsZeroBasedIndex(t *testing.T) {
	items := []string{"alpha", "beta", "gamma"}
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"typing 1 selects the first item", "1", 0},
		{"typing 2 selects the second item", "2", 1},
		{"typing 3 selects the last item", "3", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pick(io.Discard, "", items, keys(tc.input))
			if err != nil {
				t.Fatalf("pick: %v", err)
			}
			if got != tc.want {
				t.Errorf("index = %d (%q), want %d (%q)", got, items[got], tc.want, items[tc.want])
			}
		})
	}
}

func TestPickAutoCommitsAndWaits(t *testing.T) {
	twelve := make([]string, 12)
	for i := range twelve {
		twelve[i] = "item"
	}

	// With 12 items, "1" alone must not commit: the picker waits to see whether
	// a "2" follows. Feeding "12" therefore selects the twelfth item.
	got, err := pick(io.Discard, "", twelve, keys("12"))
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got != 11 {
		t.Errorf("index = %d, want 11", got)
	}

	// A digit that cannot grow into a valid index commits immediately, with no
	// Enter needed: "2" is unambiguous because "2x" would exceed 12.
	got, err = pick(io.Discard, "", twelve, keys("2"))
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got != 1 {
		t.Errorf("index = %d, want 1", got)
	}
}

func TestPickEnterCommitsBufferedDigit(t *testing.T) {
	twelve := make([]string, 12)

	// "1" is in the waiting state with 12 items; Enter must accept it rather
	// than discard it waiting for a second digit.
	got, err := pick(io.Discard, "", twelve, keys("1\r"))
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got != 0 {
		t.Errorf("index = %d, want 0", got)
	}
}

func TestPickCancels(t *testing.T) {
	items := []string{"alpha", "beta", "gamma"}
	tests := []struct {
		name  string
		input string
	}{
		{"escape", "\x1b"},
		{"ctrl-c", "\x03"},
		{"enter with nothing typed", "\r"},
		{"out-of-range digit", "9"},
		{"end of input", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pick(io.Discard, "", items, keys(tc.input))
			if !errors.Is(err, ErrCancelled) {
				t.Errorf("err = %v, want ErrCancelled", err)
			}
		})
	}

	// Erasing the only digit leaves nothing to commit, so Enter cancels. This
	// needs 12 items: with 3, a digit commits before a backspace can arrive.
	t.Run("digit erased then enter", func(t *testing.T) {
		_, err := pick(io.Discard, "", make([]string, 12), keys("1\x7f\r"))
		if !errors.Is(err, ErrCancelled) {
			t.Errorf("err = %v, want ErrCancelled", err)
		}
	})
}

func TestPickBackspaceEditsTheBuffer(t *testing.T) {
	twelve := make([]string, 12)

	// Type "1", backspace it away, then type "3": with 12 items "3" cannot grow
	// into a valid index, so it commits on its own.
	got, err := pick(io.Discard, "", twelve, keys("1\x7f3"))
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if got != 2 {
		t.Errorf("index = %d, want 2", got)
	}
}

func TestPickRendersAlignedMenu(t *testing.T) {
	items := make([]string, 10)
	for i := range items {
		items[i] = "row"
	}
	var out strings.Builder
	if _, err := pick(&out, "HEADER", items, keys("\x1b")); !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
	got := out.String()

	// With 10 items the index column is two wide, so single digits are padded
	// and the header is indented past the "nn) " prefix.
	for _, want := range []string{"    HEADER", " 1) row", "10) row", "select 1-10 (Esc to cancel): "} {
		if !strings.Contains(got, want) {
			t.Errorf("menu missing %q\n---\n%s", want, got)
		}
	}
}

func TestPickWithNoItemsCancels(t *testing.T) {
	if _, err := pick(io.Discard, "", nil, keys("1")); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
}
