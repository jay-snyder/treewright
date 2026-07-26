package tmux

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// pane renders one line of the listing parsePanes reads, so the tests below say
// what they mean rather than spelling out tab positions. The window carries no
// worktree stamp: it is one treemux did not open, or one open since before
// treemux stamped them.
func pane(id, session, name, dir string) string {
	return stamped(id, session, name, "", dir)
}

// stamped renders a pane whose window treemux opened on worktree — which is not
// necessarily where the pane is standing now, since a shell can walk anywhere.
func stamped(id, session, name, worktree, dir string) string {
	return strings.Join([]string{id, session, name, worktree, dir}, "\t")
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
			// Two unstamped panes in one directory resolve to the older window,
			// listed second here on purpose: the listing's own order follows window
			// position, which the user rearranges, so it cannot be what decides.
			name:   "duplicate directories keep the older window",
			out:    pane("@2", "s", "SECOND", "/shared") + "\n" + pane("@1", "s", "FIRST", "/shared"),
			prefer: "s",
			want: map[string]Window{
				"/shared": {ID: "@1", Session: "s", Name: "FIRST"},
			},
		},
		{
			// And ids are compared as numbers: a session that has opened ten windows
			// would otherwise call "@10" older than "@9".
			name:   "window ids order by creation, not as text",
			out:    pane("@9", "s", "NINTH", "/shared") + "\n" + pane("@10", "s", "TENTH", "/shared"),
			prefer: "s",
			want: map[string]Window{
				"/shared": {ID: "@9", Session: "s", Name: "NINTH"},
			},
		},
		{
			// Unless one of them is in the repository's own session, which is the
			// window the repository's commands mean — wherever it appears in the
			// listing.
			name:   "the preferred session wins over an older window",
			out:    pane("@1", "elsewhere", "STRAY", "/shared") + "\n" + pane("@2", "myrepo", "MINE", "/shared"),
			prefer: "myrepo",
			want: map[string]Window{
				"/shared": {ID: "@2", Session: "myrepo", Name: "MINE"},
			},
		},
		{
			// The everyday collision: `treemux cd eng-1` leaves the base window's
			// shell standing in the stream's worktree, so MAIN and ENG-1 both report
			// the same directory. The window treemux opened there is the stream's,
			// whichever of them the listing reaches first.
			name: "the window opened on the worktree wins over one standing in it",
			out: pane("@1", "myrepo", "MAIN", "/wt/eng-1") + "\n" +
				stamped("@2", "myrepo", "ENG-1", "/wt/eng-1", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@2", Session: "myrepo", Name: "ENG-1"},
			},
		},
		{
			// Even from another session, which is the case `resume` already reports
			// and switches to rather than duplicating.
			name: "the window opened on the worktree wins from another session",
			out: pane("@1", "myrepo", "MAIN", "/wt/eng-1") + "\n" +
				stamped("@2", "elsewhere", "ENG-1", "/wt/eng-1", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@2", Session: "elsewhere", Name: "ENG-1"},
			},
		},
		{
			// A stream's window is still its own after its shell walks off, so
			// nothing opens a second window on a worktree that already has one.
			name:   "a window opened on a worktree answers for it after its pane moves",
			out:    stamped("@2", "myrepo", "ENG-1", "/wt/eng-1", "/somewhere/else"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1":       {ID: "@2", Session: "myrepo", Name: "ENG-1"},
				"/somewhere/else": {ID: "@2", Session: "myrepo", Name: "ENG-1"},
			},
		},
		{
			// The other half of that: a window known to belong to another worktree
			// loses to one that claims nothing, since closing or switching to the
			// other stream's window in this one's name is the damaging mistake.
			name: "a window opened on another worktree loses to an unstamped one",
			out: stamped("@1", "myrepo", "ENG-2", "/wt/eng-2", "/wt/eng-1") + "\n" +
				pane("@5", "myrepo", "VISITOR", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@5", Session: "myrepo", Name: "VISITOR"},
				"/wt/eng-2": {ID: "@1", Session: "myrepo", Name: "ENG-2"},
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

// TestParsePanesIgnoresWindowOrder is the regression test for the bug this
// preference order was written for: list-panes -a walks windows in index order,
// so a directory two windows stood in used to resolve to whichever of them was
// further left in the status line. Rearranging windows — swap-window, or a drag —
// silently changed which window `ls` named, `resume` switched to, and `rm`
// offered to close.
func TestParsePanesIgnoresWindowOrder(t *testing.T) {
	lines := []string{
		pane("@1", "proj", "MAIN", "/wt/eng-1"), // the base window, after `treemux cd eng-1`
		stamped("@2", "proj", "ENG-1", "/wt/eng-1", "/wt/eng-1"),
		stamped("@3", "proj", "ENG-2", "/wt/eng-2", "/wt/eng-2"),
		pane("@4", "elsewhere", "HAND-MADE", "/wt/eng-2"),
	}

	want := parsePanes(strings.Join(lines, "\n"), "proj")
	if got := want["/wt/eng-1"].Name; got != "ENG-1" {
		t.Fatalf("window for /wt/eng-1 = %q, want the stream's own window ENG-1", got)
	}

	// Every arrangement of the same windows has to answer the same way, so this
	// walks the permutations rather than trusting one reversal.
	for _, order := range slices.Collect(permutations(lines)) {
		got := parsePanes(strings.Join(order, "\n"), "proj")
		if !maps.Equal(got, want) {
			t.Errorf("rearranged to %v\n got %v\nwant %v", order, got, want)
		}
	}
}

// permutations yields every ordering of a listing, standing in for every way the
// windows could be arranged.
func permutations(lines []string) func(func([]string) bool) {
	return func(yield func([]string) bool) {
		var walk func(prefix, rest []string) bool
		walk = func(prefix, rest []string) bool {
			if len(rest) == 0 {
				return yield(slices.Clone(prefix))
			}
			for i := range rest {
				remaining := slices.Concat(rest[:i:i], rest[i+1:])
				if !walk(append(prefix, rest[i]), remaining) {
					return false
				}
			}
			return true
		}
		walk(nil, lines)
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
