package tmux

import (
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/testenv"
)

// pane renders one line of the listing parsePanes reads, so the tests below say
// what they mean rather than spelling out tab positions. The window carries no
// worktree stamp: it is one treewright did not open, or one open since before
// treewright stamped them.
func pane(id, session, name, dir string) string {
	return signaled(id, session, name, "", "", dir)
}

// stamped renders a pane whose window treewright opened on worktree — which is not
// necessarily where the pane is standing now, since a shell can walk anywhere.
func stamped(id, session, name, worktree, dir string) string {
	return signaled(id, session, name, worktree, "", dir)
}

// signaled renders a pane whose window also carries an agent state, the way one
// looks after `treewright signal` has run in its worktree.
//
// Its session holds two windows, which is the ordinary case and the one that
// matters least: nothing about resolving a directory to a window depends on the
// count, and the one thing that does — whether closing it ends the session —
// has a test of its own below.
func signaled(id, session, name, worktree, state, dir string) string {
	return inSession(2, id, session, name, worktree, state, dir)
}

// inSession renders a pane whose session holds a given number of windows.
func inSession(windows int, id, session, name, worktree, state, dir string) string {
	return strings.Join([]string{id, session, name, worktree, state, strconv.Itoa(windows), dir}, "\t")
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
				"/Users/x/code/repo": {ID: "@3", Session: "myrepo", Name: "ENG-1", SessionWindows: 2},
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
				"/Users/x/my code/repo-foo": {ID: "@7", Session: "my repo", Name: "WIN NAME", SessionWindows: 2},
			},
		},
		{
			// A tab inside a path stays in the path: the split takes four fields
			// at most, and the path is the last of them.
			name:   "tab inside a path",
			out:    pane("@1", "s", "W", "/a\tb"),
			prefer: "s",
			want: map[string]Window{
				"/a\tb": {ID: "@1", Session: "s", Name: "W", SessionWindows: 2},
			},
		},
		{
			name:   "several panes",
			out:    pane("@1", "s", "A", "/a") + "\n" + pane("@2", "s", "B", "/b"),
			prefer: "s",
			want: map[string]Window{
				"/a": {ID: "@1", Session: "s", Name: "A", SessionWindows: 2},
				"/b": {ID: "@2", Session: "s", Name: "B", SessionWindows: 2},
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
				"/shared": {ID: "@1", Session: "s", Name: "FIRST", SessionWindows: 2},
			},
		},
		{
			// And ids are compared as numbers: a session that has opened ten windows
			// would otherwise call "@10" older than "@9".
			name:   "window ids order by creation, not as text",
			out:    pane("@9", "s", "NINTH", "/shared") + "\n" + pane("@10", "s", "TENTH", "/shared"),
			prefer: "s",
			want: map[string]Window{
				"/shared": {ID: "@9", Session: "s", Name: "NINTH", SessionWindows: 2},
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
				"/shared": {ID: "@2", Session: "myrepo", Name: "MINE", SessionWindows: 2},
			},
		},
		{
			// The everyday collision: `treewright cd eng-1` leaves the base window's
			// shell standing in the worktree, so MAIN and ENG-1 both report
			// the same directory. The window treewright opened there is the worktree's,
			// whichever of them the listing reaches first.
			name: "the window opened on the worktree wins over one standing in it",
			out: pane("@1", "myrepo", "MAIN", "/wt/eng-1") + "\n" +
				stamped("@2", "myrepo", "ENG-1", "/wt/eng-1", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@2", Session: "myrepo", Name: "ENG-1", Worktree: "/wt/eng-1", SessionWindows: 2},
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
				"/wt/eng-1": {ID: "@2", Session: "elsewhere", Name: "ENG-1", Worktree: "/wt/eng-1", SessionWindows: 2},
			},
		},
		{
			// A worktree's window is still its own after its shell walks off, so
			// nothing opens a second window on a worktree that already has one.
			name:   "a window opened on a worktree answers for it after its pane moves",
			out:    stamped("@2", "myrepo", "ENG-1", "/wt/eng-1", "/somewhere/else"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1":       {ID: "@2", Session: "myrepo", Name: "ENG-1", Worktree: "/wt/eng-1", SessionWindows: 2},
				"/somewhere/else": {ID: "@2", Session: "myrepo", Name: "ENG-1", Worktree: "/wt/eng-1", SessionWindows: 2},
			},
		},
		{
			// The other half of that: a window known to belong to another worktree
			// loses to one that claims nothing, since closing or switching to the
			// other worktree's window in this one's name is the damaging mistake.
			name: "a window opened on another worktree loses to an unstamped one",
			out: stamped("@1", "myrepo", "ENG-2", "/wt/eng-2", "/wt/eng-1") + "\n" +
				pane("@5", "myrepo", "VISITOR", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@5", Session: "myrepo", Name: "VISITOR", SessionWindows: 2},
				"/wt/eng-2": {ID: "@1", Session: "myrepo", Name: "ENG-2", Worktree: "/wt/eng-2", SessionWindows: 2},
			},
		},
		{
			// A window in no session treewright knows about is still reported: it is
			// what stops a second window being opened on the same directory.
			name:   "a window outside the preferred session is kept",
			out:    pane("@9", "someone-else", "HAND-MADE", "/shared"),
			prefer: "myrepo",
			want: map[string]Window{
				"/shared": {ID: "@9", Session: "someone-else", Name: "HAND-MADE", SessionWindows: 2},
			},
		},
		{
			name:   "malformed lines are skipped",
			out:    pane("@1", "s", "A", "/a") + "\nnotabs\n" + pane("@2", "s", "B", "") + "\n" + pane("@3", "s", "C", "/b"),
			prefer: "s",
			want: map[string]Window{
				"/a": {ID: "@1", Session: "s", Name: "A", SessionWindows: 2},
				"/b": {ID: "@3", Session: "s", Name: "C", SessionWindows: 2},
			},
		},
		{
			// The agent state rides the same listing the worktree stamp does, so
			// one tmux invocation still answers for every window.
			name:   "an agent state is read back",
			out:    signaled("@2", "myrepo", "ENG-1", "/wt/eng-1", "waiting", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@2", Session: "myrepo", Name: "ENG-1", State: "waiting", Worktree: "/wt/eng-1", SessionWindows: 2},
			},
		},
		{
			// The waiting marker is display, not identity: it comes off here, once,
			// so every message, table cell, and JSON field carries the name
			// underneath treewright's own punctuation.
			name:   "the waiting marker is stripped from the name",
			out:    signaled("@2", "myrepo", "!ENG-1", "/wt/eng-1", "waiting", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@2", Session: "myrepo", Name: "ENG-1", State: "waiting", Worktree: "/wt/eng-1", SessionWindows: 2},
			},
		},
		{
			// The window count rides the same listing, so a table of a dozen
			// worktrees can say which of their windows would take a session
			// down with it without a round trip per row.
			name:   "a lone window reports the session it would end",
			out:    inSession(1, "@2", "myrepo", "ENG-1", "/wt/eng-1", "", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@2", Session: "myrepo", Name: "ENG-1", Worktree: "/wt/eng-1", SessionWindows: 1},
			},
		},
		{
			// tmux always answers with a number; a listing that somehow does not
			// leaves the count at zero, which reads as "not the last window".
			// Saying nothing is the right way to be wrong about a caveat.
			name:   "an unreadable count is not a claim",
			out:    inSession(0, "@2", "myrepo", "ENG-1", "/wt/eng-1", "", "/wt/eng-1"),
			prefer: "myrepo",
			want: map[string]Window{
				"/wt/eng-1": {ID: "@2", Session: "myrepo", Name: "ENG-1", Worktree: "/wt/eng-1"},
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
		pane("@1", "proj", "MAIN", "/wt/eng-1"), // the base window, after `treewright cd eng-1`
		stamped("@2", "proj", "ENG-1", "/wt/eng-1", "/wt/eng-1"),
		stamped("@3", "proj", "ENG-2", "/wt/eng-2", "/wt/eng-2"),
		pane("@4", "elsewhere", "HAND-MADE", "/wt/eng-2"),
	}

	want := parsePanes(strings.Join(lines, "\n"), "proj")
	if got := want["/wt/eng-1"].Name; got != "ENG-1" {
		t.Fatalf("window for /wt/eng-1 = %q, want the worktree's own window ENG-1", got)
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

// TestParseBoundKey covers reading a key back out of list-keys.
//
// It is parsed from text because list-keys has no format variable for the key
// itself — #{key} renders empty and #{command} says "list-keys" — so the shape of
// that output is load-bearing, and this is where it is pinned. The lines below
// are verbatim from tmux 3.7, quirks included.
func TestParseBoundKey(t *testing.T) {
	const listing = `bind-key    -T prefix Space   next-layout
bind-key -r -T prefix \;      select-pane -D
bind-key -r -T prefix \'      select-pane -R
bind-key    -T prefix n       new-window -c "#{pane_current_path}"
bind-key    -T prefix b       command-prompt -p "new worktree:" "run-shell -b \"treewright popup -c #{client_tty} new %1\""
bind-key    -T prefix g       run-shell -b "treewright popup -c \"#{client_tty}\" resume"
bind-key -r -T prefix "C-;"   resize-pane -D 5`

	tests := []struct {
		name  string
		match []string
		want  string
	}{
		{
			// Both terms, because either alone catches the wrong line: "treewright"
			// also matches the resume binding, and " new " would match any
			// treewright-ish command that mentioned it.
			name: "the binding that starts a worktree", match: []string{"treewright", " new "}, want: "b",
		},
		{name: "the binding that switches", match: []string{"treewright", "resume"}, want: "g"},
		{
			// The trap this spacing avoids: new-window is not starting a worktree.
			name: "new-window is not a match", match: []string{"treewright", " new "}, want: "b",
		},
		{name: "nothing bound to it", match: []string{"treewright", "prune"}, want: ""},
		{
			// list-keys escapes a key that would be ambiguous in its own output,
			// and a reader has to press the key, not the escaping.
			name: "an escaped key is unescaped", match: []string{"select-pane -D"}, want: ";",
		},
		{name: "a quoted key is unquoted", match: []string{"resize-pane -D 5"}, want: "C-;"},
		{name: "a repeatable binding, whose -r shifts the fields", match: []string{"select-pane -R"}, want: "'"},
		{name: "empty listing", match: []string{"treewright"}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := listing
			if tc.name == "empty listing" {
				in = ""
			}
			if got := parseBoundKey(in, tc.match...); got != tc.want {
				t.Errorf("parseBoundKey(%v) = %q, want %q", tc.match, got, tc.want)
			}
		})
	}
}

// TestPopupArgs pins the flags a popup is opened with. Opening one needs a client
// to draw on, which a headless test has none of, so this is where they get
// checked.
func TestPopupArgs(t *testing.T) {
	got := strings.Join(popupArgs("/dev/ttys005", "/code/repo", "treewright resume", 78, 9), " ")
	want := "display-popup -EE -w 78 -h 9 -e TREEWRIGHT_POPUP=1 -c /dev/ttys005 -d /code/repo treewright resume"
	if got != want {
		t.Errorf("popupArgs = %q, want %q", got, want)
	}

	// The command inside has no other way to know where it is running, and tmux
	// marks a popup with nothing of its own. Without this a failure holds the
	// popup on screen with nothing saying how to dismiss it.
	if !strings.Contains(got, "-e "+PopupEnv+"=1") {
		t.Errorf("popupArgs = %q, want the popup marked for what runs in it", got)
	}

	// -EE, doubled, is what leaves a failed command on screen: with a single -E
	// tmux closes the popup however it exited, and "no worktrees to resume"
	// vanished before it could be read.
	if !strings.Contains(got, " -EE ") {
		t.Errorf("popupArgs = %q, want -EE so a failure stays readable", got)
	}

	// Both are optional, and an empty one must not become a flag with no value —
	// tmux would read the command that follows as the flag's argument.
	bare := strings.Join(popupArgs("", "", "treewright ls", 80, 12), " ")
	if strings.Contains(bare, "-c") || strings.Contains(bare, "-d") {
		t.Errorf("popupArgs with no client or directory = %q, want neither flag", bare)
	}
}

// TestPopupIgnoresTheInnerCommandsExitStatus is the regression test for a second
// overlay appearing on Escape.
//
// display-popup exits with the status of whatever ran inside it, and under -EE
// the popup has already stayed up, shown the message and been dismissed by the
// time that status comes back. Returning it made the caller fail too, and a key
// binding running through run-shell then had tmux announce "returned 1" over the
// top — reporting in the abstract what the popup had just said plainly.
//
// Driven against a real server with no client attached, which is the closest a
// headless test gets: tmux refuses for its own reason and says so on stderr, so
// this also pins the other half — that tmux's own refusals are still reported.
func TestPopupIgnoresTheInnerCommandsExitStatus(t *testing.T) {
	testenv.RequireTool(t, "tmux")
	//nolint:usetesting // t.TempDir gives a path too long for a unix socket on macOS
	dir, err := os.MkdirTemp("/tmp", "tmx")
	if err != nil {
		t.Fatalf("make a tmux socket directory: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	label := strings.ReplaceAll(t.Name(), "/", "-")
	t.Setenv("TREEWRIGHT_TMUX_LABEL", label)
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", label, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	if out, err := exec.Command("tmux", "-L", label, "-f", "/dev/null",
		"new-session", "-d", "-s", "probe", "-c", "/tmp", "sleep 300").CombinedOutput(); err != nil {
		testenv.Unavailablef(t, "cannot start a tmux server here: %v\n%s", err, out)
	}

	// No client is attached, so tmux declines to draw — and says why, which is
	// exactly the case that must still surface.
	err = Popup("", "/tmp", "true", 40, 10)
	if err == nil {
		t.Fatal("Popup returned nil when tmux itself refused; its message would be lost")
	}
	if !strings.Contains(err.Error(), "no current client") {
		t.Errorf("err = %v, want tmux's own complaint passed through", err)
	}
}

// TestKillWindowArgs covers the line treewright prints for someone to run when
// there is nobody to ask, and the one thing it has to carry: the server flag.
//
// The line is run from a shell holding none of treewright's environment, so
// under a label a bare `tmux kill-window -t @3` goes to the default server —
// where @3 is some other window entirely. tmux closes it and exits 0, so the
// window that was meant to close stays open, one nobody asked about is gone,
// and nothing anywhere says so. A wrong session name fails loudly; a wrong
// server does not, which is why this is the more dangerous of the two.
func TestKillWindowArgs(t *testing.T) {
	t.Setenv("TREEWRIGHT_TMUX_LABEL", "")
	if got, want := strings.Join(KillWindowArgs("@3"), " "), "kill-window -t @3"; got != want {
		t.Errorf("KillWindowArgs = %q, want %q", got, want)
	}

	t.Setenv("TREEWRIGHT_TMUX_LABEL", "work")
	if got, want := strings.Join(KillWindowArgs("@3"), " "), "-L work kill-window -t @3"; got != want {
		t.Errorf("KillWindowArgs under a label = %q, want %q", got, want)
	}

	// And it is the command KillWindow itself runs, not a second spelling of it
	// that can drift: what gets printed has to be what treewright would have done.
	if got, want := strings.Join(killWindow("@3"), " "), "kill-window -t @3"; got != want {
		t.Errorf("killWindow = %q, want the printed line to name the same command", got)
	}
}

// TestAttachArgs pins the two things `treewright attach` exists to get right, and
// that a person copying "tmux attach -t myrepo" out of a progress line cannot:
// the session is named exactly, and the server flag survives.
func TestAttachArgs(t *testing.T) {
	t.Setenv("TREEWRIGHT_TMUX_LABEL", "")
	if got, want := strings.Join(AttachArgs("api"), " "), "attach-session -t =api"; got != want {
		t.Errorf("AttachArgs(%q) = %q, want %q", "api", got, want)
	}

	// Without this, attaching would reach the default server while every window
	// treewright opened went to another one — and report no session at all.
	t.Setenv("TREEWRIGHT_TMUX_LABEL", "work")
	if got, want := strings.Join(AttachArgs("api"), " "), "-L work attach-session -t =api"; got != want {
		t.Errorf("AttachArgs under a label = %q, want %q", got, want)
	}
}

// TestLastLines covers what Capture does to a pane's contents before anyone
// reads them: a pane is mostly empty below whatever is running in it, and a
// tail that counted those blank rows would report twenty lines of nothing about
// a window that is asking a question.
func TestLastLines(t *testing.T) {
	tests := []struct {
		name string
		out  string
		n    int
		want string
	}{
		{"shorter than the tail", "one\ntwo", 20, "one\ntwo"},
		{"cut to the last lines", "one\ntwo\nthree\nfour", 2, "three\nfour"},
		{
			// The case it exists for: a question at the top of an otherwise
			// empty pane still reaches the reader.
			name: "the empty bottom of a pane does not count",
			out:  "overwrite the file? y/n\n\n   \n\n",
			n:    2,
			want: "overwrite the file? y/n",
		},
		{"blank lines between content are kept", "one\n\ntwo", 3, "one\n\ntwo"},
		{"nothing at all", "", 20, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastLines(tc.out, tc.n); got != tc.want {
				t.Errorf("lastLines(%q, %d) = %q, want %q", tc.out, tc.n, got, tc.want)
			}
		})
	}
}
