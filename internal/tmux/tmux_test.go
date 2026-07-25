package tmux

import "testing"

// pane renders one line of the listing parsePanes reads, so the tests below say
// what they mean rather than spelling out tab positions.
func pane(id, session, name, dir string) string {
	return id + "\t" + session + "\t" + name + "\t" + dir
}

func TestParsePanes(t *testing.T) {
	tests := []struct {
		name   string
		out    string
		prefer string
		want   map[string]Window
	}{
		{
			name:   "one pane",
			out:    pane("@3", "myrepo", "ENG-1", "/Users/x/code/repo"),
			prefer: "myrepo",
			want: map[string]Window{
				"/Users/x/code/repo": {ID: "@3", Session: "myrepo", Name: "ENG-1"},
			},
		},
		{
			// The fields are tab-separated because a window name, a session name
			// and a path may all contain spaces: an earlier space-separated
			// listing truncated "/Users/x/my code/repo-foo" at the space and no
			// worktree in such a path was ever recognized as already open.
			name:   "spaces in every field",
			out:    pane("@7", "my repo", "WIN NAME", "/Users/x/my code/repo-foo"),
			prefer: "my repo",
			want: map[string]Window{
				"/Users/x/my code/repo-foo": {ID: "@7", Session: "my repo", Name: "WIN NAME"},
			},
		},
		{
			// A tab inside a path stays in the path: the split takes four fields
			// at most, and the path is the last of them.
			name:   "tab inside a path",
			out:    pane("@1", "s", "W", "/a\tb"),
			prefer: "s",
			want: map[string]Window{
				"/a\tb": {ID: "@1", Session: "s", Name: "W"},
			},
		},
		{
			name:   "several panes",
			out:    pane("@1", "s", "A", "/a") + "\n" + pane("@2", "s", "B", "/b"),
			prefer: "s",
			want: map[string]Window{
				"/a": {ID: "@1", Session: "s", Name: "A"},
				"/b": {ID: "@2", Session: "s", Name: "B"},
			},
		},
		{
			// Two panes in one directory must resolve to a stable answer, so
			// repeated calls do not switch between windows.
			name:   "duplicate directories keep the first window",
			out:    pane("@1", "s", "FIRST", "/shared") + "\n" + pane("@2", "s", "SECOND", "/shared"),
			prefer: "s",
			want: map[string]Window{
				"/shared": {ID: "@1", Session: "s", Name: "FIRST"},
			},
		},
		{
			// Unless one of them is in the repository's own session, which is the
			// window the repository's commands mean — wherever it appears in the
			// listing.
			name:   "the preferred session wins over an earlier window",
			out:    pane("@1", "elsewhere", "STRAY", "/shared") + "\n" + pane("@2", "myrepo", "MINE", "/shared"),
			prefer: "myrepo",
			want: map[string]Window{
				"/shared": {ID: "@2", Session: "myrepo", Name: "MINE"},
			},
		},
		{
			name:   "two windows in the preferred session keep the first",
			out:    pane("@1", "myrepo", "FIRST", "/shared") + "\n" + pane("@2", "myrepo", "SECOND", "/shared"),
			prefer: "myrepo",
			want: map[string]Window{
				"/shared": {ID: "@1", Session: "myrepo", Name: "FIRST"},
			},
		},
		{
			// A window in no session treemux knows about is still reported: it is
			// what stops a second window being opened on the same directory.
			name:   "a window outside the preferred session is kept",
			out:    pane("@9", "someone-else", "HAND-MADE", "/shared"),
			prefer: "myrepo",
			want: map[string]Window{
				"/shared": {ID: "@9", Session: "someone-else", Name: "HAND-MADE"},
			},
		},
		{
			name:   "malformed lines are skipped",
			out:    pane("@1", "s", "A", "/a") + "\nnotabs\n" + pane("@2", "s", "B", "") + "\n" + pane("@3", "s", "C", "/b"),
			prefer: "s",
			want: map[string]Window{
				"/a": {ID: "@1", Session: "s", Name: "A"},
				"/b": {ID: "@3", Session: "s", Name: "C"},
			},
		},
		{
			name: "empty output",
			out:  "",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePanes(tc.out, tc.prefer)
			if len(got) != len(tc.want) {
				t.Fatalf("parsePanes(%q) = %v, want %v", tc.out, got, tc.want)
			}
			for dir, want := range tc.want {
				if got[dir] != want {
					t.Errorf("window for %q = %+v, want %+v", dir, got[dir], want)
				}
			}
		})
	}
}

// TestSessionName covers the characters that make a session unaddressable rather
// than merely ugly: tmux creates a session called "my.repo" happily and then
// reads the period as the pane separator in every target that names it.
func TestSessionName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"myrepo", "myrepo"},
		{"my.repo", "my-repo"},
		{"my:repo", "my-repo"},
		{"api.v2:old", "api-v2-old"},
		{"already-fine_123", "already-fine_123"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := SessionName(tc.in); got != tc.want {
			t.Errorf("SessionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestExactTargets guards the form every session target is written in. tmux
// matches a bare session name as a prefix, so "api" finds "api-gateway" — and a
// window meant for one repository lands in another's session.
func TestExactTargets(t *testing.T) {
	if got := exact("api"); got != "=api" {
		t.Errorf("exact(%q) = %q, want %q", "api", got, "=api")
	}
}
