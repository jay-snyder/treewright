package tmux

import "testing"

func TestParsePanes(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want map[string]string
	}{
		{
			name: "one pane",
			out:  "@3 /Users/x/code/repo",
			want: map[string]string{"/Users/x/code/repo": "@3"},
		},
		{
			// The regression this ordering exists for: with the path formatted
			// first, splitting on the first space truncated it to "/Users/x/my"
			// and no worktree in a path containing a space was ever recognized
			// as already open.
			name: "path containing spaces",
			out:  "@7 /Users/x/my code/repo-foo",
			want: map[string]string{"/Users/x/my code/repo-foo": "@7"},
		},
		{
			name: "several panes",
			out:  "@1 /a\n@2 /b\n@3 /c",
			want: map[string]string{"/a": "@1", "/b": "@2", "/c": "@3"},
		},
		{
			// Two panes in one directory must resolve to a stable answer, so
			// repeated calls do not switch between windows.
			name: "duplicate directories keep the first window",
			out:  "@1 /shared\n@2 /shared",
			want: map[string]string{"/shared": "@1"},
		},
		{
			name: "malformed lines are skipped",
			out:  "@1 /a\nnospace\n@2 \n@3 /b",
			want: map[string]string{"/a": "@1", "/b": "@3"},
		},
		{
			name: "empty output",
			out:  "",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePanes(tc.out)
			if len(got) != len(tc.want) {
				t.Fatalf("parsePanes(%q) = %v, want %v", tc.out, got, tc.want)
			}
			for dir, window := range tc.want {
				if got[dir] != window {
					t.Errorf("window for %q = %q, want %q", dir, got[dir], window)
				}
			}
		})
	}
}
