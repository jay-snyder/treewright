package cli

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jay-snyder/treemux/internal/git"
	"github.com/jay-snyder/treemux/internal/tmux"
)

// Opening a popup needs a client to draw on, which a headless test has none of.
// What is covered here is everything up to that: the size chosen, and the
// arguments refused before tmux is reached at all.

// TestPopupSizeCoversTheTable is the guard on an estimate.
//
// popupSize mirrors worktreeTable's layout instead of measuring it, because
// measuring would mean asking git for every worktree's status a second time
// before the popup appeared. Mirrored layout drifts, and drift here is invisible
// until someone's table wraps — so this renders a real one and checks the
// estimate still covers it.
func TestPopupSizeCoversTheTable(t *testing.T) {
	cases := []struct {
		name   string
		infos  []git.Info
		window tmux.Window
	}{
		{
			name: "the widest each column gets",
			infos: []git.Info{
				// A long slug, the longest status, and a window in another session,
				// which is the widest that column is spelled.
				{Worktree: git.Worktree{Slug: "eng-1675-cold-start-checklist-boost", Dir: "/wt/a"},
					Status: git.StatusUnpushed, Unpushed: 123, Compared: true, Ahead: 999, Behind: 999},
				{Worktree: git.Worktree{Slug: "eng-1557-migrate-api-eb-to-ecs", Dir: "/wt/b"},
					Status: git.StatusDirty, DirtyFiles: 42, Compared: true},
			},
			window: tmux.Window{ID: "@1", Session: "another-session", Name: "ENG-1675"},
		},
		{
			name:  "one short slug, where the prompt is the widest line",
			infos: []git.Info{{Worktree: git.Worktree{Slug: "x", Dir: "/wt/x"}, Status: git.StatusMerged, Compared: true}},
		},
		{
			name: "ten of them, so the index column grows a digit",
			infos: func() []git.Info {
				var out []git.Info
				for i := 0; i < 10; i++ {
					out = append(out, git.Info{Worktree: git.Worktree{Slug: "eng-100" + string(rune('0'+i)), Dir: "/wt/x"}, Status: git.StatusActive, Compared: true})
				}
				return out
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			windows := map[string]tmux.Window{}
			for _, info := range tc.infos {
				if tc.window.ID != "" {
					windows[info.Dir] = tc.window
				}
			}

			gotW, gotH := popupSize(tc.infos, windows)

			// The picker prints the header indented by the width of its "n) "
			// prefix, then one prefixed line per row, a blank, and the prompt.
			header, rows := worktreeTable(tc.infos, windows, "").Lines(false)
			indexCol := len(rows)/10 + 3 // digits in the largest index, plus "n) "
			widest := len(header) + indexCol
			for _, row := range rows {
				widest = max(widest, len(row)+indexCol)
			}
			widest = max(widest, len("select 1-99 (Esc to cancel): "))

			// Two for the border tmux draws around the popup's interior.
			if gotW < widest+2 {
				t.Errorf("popupSize width = %d, but the table needs %d — rows will wrap", gotW, widest+2)
			}
			if wantH := len(rows) + 3 + 2; gotH < wantH {
				t.Errorf("popupSize height = %d, but the picker needs %d — the list will scroll", gotH, wantH)
			}
			// Wide is fine, absurd is not: an estimate that drifts far past the
			// content is the same wasted space this replaced.
			if gotW > widest+2+20 {
				t.Errorf("popupSize width = %d for content of %d — too much slack", gotW, widest)
			}
		})
	}
}

// TestPopupSizeBeatsAPercentage is the point of the whole exercise, stated as a
// number: on a wide terminal the old 70%x60% was several times the content.
func TestPopupSizeBeatsAPercentage(t *testing.T) {
	listing := []git.Info{
		{Worktree: git.Worktree{Branch: "staging", Dir: "/wt/main"}, Status: git.StatusBase},
		{Worktree: git.Worktree{Slug: "eng-1557-migrate-api-eb-to-ecs", Dir: "/wt/a"}},
		{Worktree: git.Worktree{Slug: "eng-1646-app-landing-page-redesign", Dir: "/wt/b"}},
		{Worktree: git.Worktree{Slug: "eng-1675-cold-start-checklist-boost", Dir: "/wt/c"}},
	}
	w, h := popupSize(listing, nil)

	// The terminal this was reported on.
	const cols, rows = 237, 62
	if w >= cols*70/100 || h >= rows*60/100 {
		t.Errorf("popupSize = %dx%d, no better than the %dx%d a percentage gave",
			w, h, cols*70/100, rows*60/100)
	}
	if w < 40 || h < 6 {
		t.Errorf("popupSize = %dx%d, too small to hold the picker", w, h)
	}
}

// ---- argument handling ---------------------------------------------------------

func TestPopupRefusesWhatItCannotRun(t *testing.T) {
	f := newFixture(t, "")

	if r := f.exec("popup"); r.err == nil {
		t.Errorf("popup with no command succeeded\n%s", r.both())
	}
	// Left to itself this nests forever, each popup opening another.
	r := f.exec("popup", "popup")
	if r.err == nil {
		t.Fatalf("popup ran itself\n%s", r.both())
	}
	if !strings.Contains(r.stderr, "cannot run itself") {
		t.Errorf("stderr = %q, want it to say why", r.stderr)
	}
}

// TestNoWorktreesReadsAsAMessage covers a repository nobody has started a worktree
// in yet, which is an ordinary state and not a fault.
//
// It still exits non-zero, and that is not a contradiction: the exit status is
// what holds the popup open under -EE, and with nothing to choose from that
// sentence is the only thing on screen. What makes it a message rather than an
// error is that treemux prints it itself, unprefixed — an error returned from a
// command comes back through main wearing "error:".
func TestNoWorktreesReadsAsAMessage(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("resume")
	if strings.Contains(r.stderr, "error:") {
		t.Errorf("stderr = %q, want a plain message — an empty repository is not a fault", r.stderr)
	}
	if !strings.Contains(r.stderr, "no worktrees for repo") {
		t.Errorf("stderr = %q, want it to say there is nothing to resume", r.stderr)
	}
	// And it has to say what to do next, being the whole of what the popup shows.
	if !strings.Contains(r.stderr, "treemux new") {
		t.Errorf("stderr = %q, want it to name the way out", r.stderr)
	}
	if r.err == nil {
		t.Error("err = nil; the popup would close before the message could be read")
	}
}

// TestPopupHintOnlyAppearsInAPopup covers the other half of holding a popup open:
// nothing on screen said how to close it, because Escape is a popup's convention
// and not a terminal's.
func TestPopupHintOnlyAppearsInAPopup(t *testing.T) {
	var plain strings.Builder
	t.Setenv(tmux.PopupEnv, "")
	PopupHint(&plain)
	if plain.String() != "" {
		t.Errorf("hint outside a popup = %q, want nothing to dismiss and nothing said", plain.String())
	}

	var inPopup strings.Builder
	t.Setenv(tmux.PopupEnv, "1")
	PopupHint(&inPopup)
	if !strings.Contains(inPopup.String(), "Esc") {
		t.Errorf("hint in a popup = %q, want it to name the key", inPopup.String())
	}
}

// TestTheEmptyRepoPopupFitsItsMessage keeps the one case with no picker in it
// honest. Sizing it for a menu that will not appear is the same wasted space the
// rest of this replaced, and sizing it too tight wraps the only sentence there is.
func TestTheEmptyRepoPopupFitsItsMessage(t *testing.T) {
	f := newFixture(t, "")

	w, h := sizeFor("resume")
	want := utf8.RuneCountInString(noWorktreesMessage("proj"))
	if w < want+2 {
		t.Errorf("width = %d, but the message is %d wide — it would wrap", w, want)
	}
	if w > want+10 {
		t.Errorf("width = %d for a %d-wide message — too much slack", w, want)
	}
	// The message, a blank line, the hint, the row the cursor lands on after the
	// last newline, and a border. Sized without that cursor row the terminal
	// scrolled by one and the message — the only line that says anything — went
	// over the top, leaving a popup that showed nothing but "press Esc to close".
	if h != 6 {
		t.Errorf("height = %d, want 6: three lines, the cursor's row, and a border", h)
	}
	_ = f
}

// TestPopupSizesOnlyThePickers covers the fallback. Only resume and cd have a
// size worth deriving; everything else prints progress of a length nobody can
// predict, and a small fixed popup beats a proportion of the terminal for those.
func TestPopupSizesOnlyThePickers(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "eng-1")

	sized, _ := sizeFor("resume")
	fixed, _ := sizeFor("new")
	if sized == fixed {
		t.Errorf("resume and new both sized %d; resume should follow its worktrees", sized)
	}

	// An unknown command must not panic its way to a size.
	if w, h := sizeFor("nonesuch"); w <= 0 || h <= 0 {
		t.Errorf("sizeFor(unknown) = %dx%d, want the fallback", w, h)
	}
}
