package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/gittest"
	"github.com/jay-snyder/treewright/internal/tmux"
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

// TestPopupAnswersAboutTheDirectoryItIsGiven covers the reason --dir exists.
//
// run-shell does not run in the calling pane's directory. It runs in the tmux
// server's, wherever that was started — so a popup left to work out where it is
// answers about whichever repository the server happens to have been launched
// from, from every window on it. The symptom that surfaced it was the ls table
// marking the same row as current no matter which worktree the key was pressed
// in; the part that mattered was that a second repository's windows offered the
// first one's worktrees.
//
// Two repositories are registered so that the caller's own directory decides
// something: with one config, treewright falls back to it and the bug hides.
// Their pickers are different widths, which is what makes the popup's size the
// evidence — it is derived from the worktrees of whichever repo was resolved.
//
// The assertion rides on a failure because opening a popup needs a client to
// draw on and a headless test has none. tmux reports the command line it was
// given, which is exactly what is under test.
func TestPopupAnswersAboutTheDirectoryItIsGiven(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")
	f.mustRun("new", "short")

	// A second repository, registered beside the first, whose worktree is named
	// far more widely than anything in it.
	other := gittest.New(t)
	other.Worktree("a-slug-long-enough-to-change-the-popups-width")
	config := "main_dir = '" + other.MainDir + "'\nbase_branch = 'main'\nbranch_prefix = '" + gittest.BranchPrefix + "'\n"
	if err := os.WriteFile(filepath.Join(f.registry, "other.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write the second config: %v", err)
	}

	// Standing in the first repository, as a popup spawned by run-shell stands in
	// the server's directory rather than in the worktree the key was pressed in.
	here := f.exec("popup", "resume")
	there := f.exec("popup", "-d", other.MainDir, "resume")

	for _, r := range []result{here, there} {
		if r.err == nil {
			t.Fatalf("popup succeeded with no client to draw on\n%s", r.both())
		}
	}
	if !strings.Contains(there.err.Error(), "-d "+other.MainDir) {
		t.Errorf("the popup did not open on the directory it was given:\n%v", there.err)
	}
	if w := popupWidth(t, here.err.Error()); w == popupWidth(t, there.err.Error()) {
		t.Errorf("both popups came out %s wide, so --dir did not decide the repository:\n%v\n%v",
			w, here.err, there.err)
	}

	// And the process is left where it was found, since Run is called in-process
	// here and the next command would otherwise inherit somewhere it never went.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if wd != f.MainDir {
		t.Errorf("working directory is %s, want it restored to %s", wd, f.MainDir)
	}
}

// popupWidth reads the -w tmux was asked for out of its complaint.
func popupWidth(t *testing.T, message string) string {
	t.Helper()
	m := regexp.MustCompile(`-w (\d+)`).FindStringSubmatch(message)
	if m == nil {
		t.Fatalf("no popup width in: %s", message)
	}
	return m[1]
}

// TestNoWorktreesReadsAsAMessage covers a repository nobody has started a worktree
// in yet, which is an ordinary state and not a fault.
//
// What makes it a message rather than an error is that treewright prints it itself,
// unprefixed — an error returned from a command comes back through main wearing
// "error:".
//
// It no longer needs a non-zero exit to hold the popup open, and that is the
// point of the change: the sentence used to be the only thing on screen, so the
// popup had to be kept up to be read. Now the menu is under it, holding the base
// checkout, and the popup stays up because the picker is waiting rather than
// because the command failed.
func TestNoWorktreesReadsAsAMessage(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("resume")
	if strings.Contains(r.stderr, "error:") {
		t.Errorf("stderr = %q, want a plain message — an empty repository is not a fault", r.stderr)
	}
	if !strings.Contains(r.stderr, "no worktrees for repo") {
		t.Errorf("stderr = %q, want it to say there is nothing to resume", r.stderr)
	}
	// And it has to say what to do next, that being why it is printed at all.
	if !strings.Contains(r.stderr, "treewright new") {
		t.Errorf("stderr = %q, want it to name the way out", r.stderr)
	}
}

// TestHintsSpeakTheInvokedName pins the rule that a hint naming a command to
// type is spelled with the name the user typed. Someone who reached treewright as
// tw — through the shell shim, which reports the typed name in TREEWRIGHT_ARGV0 —
// should never be told to run a longer command than the one they know.
func TestHintsSpeakTheInvokedName(t *testing.T) {
	f := newFixture(t, "")
	_ = f

	var out, errOut bytes.Buffer
	Run(Env{Args: []string{"resume"}, Argv0: "tw", Stdout: &out, Stderr: &errOut})
	if !strings.Contains(errOut.String(), `"tw new <slug>"`) {
		t.Errorf("stderr = %q, want the way out spelled as tw, the name that was typed", errOut.String())
	}
	if strings.Contains(errOut.String(), "treewright new") {
		t.Errorf("stderr = %q, scolds with the long name the user did not type", errOut.String())
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

// TestTheEmptyRepoPopupFitsItsMessage keeps the empty repository honest. Its
// popup holds two things of quite different widths — a long sentence over a menu
// of one short row — and sizing it to either alone gets it wrong: to the menu and
// the sentence wraps, to the sentence and the menu is fine but only by luck.
func TestTheEmptyRepoPopupFitsItsMessage(t *testing.T) {
	f := newFixture(t, "")

	env := &Env{Argv0: "treewright"}
	w, h := sizeFor(env, "resume")
	want := utf8.RuneCountInString(noWorktreesMessage(env.Argv0, "proj"))
	if w < want+2 {
		t.Errorf("width = %d, but the message is %d wide — it would wrap", w, want)
	}
	if w > want+10 {
		t.Errorf("width = %d for a %d-wide message — too much slack", w, want)
	}
	// The message and the blank line under it, then the picker's own four lines —
	// header, the base row, a blank, and the prompt the cursor sits on — and a
	// border. The picker needs no allowance for the cursor, its last line being
	// one the cursor rests on rather than passes.
	if h != 8 {
		t.Errorf("height = %d, want 8: the message, a gap, a four-line picker, and a border", h)
	}
	_ = f
}

// TestPopupSizesOnlyThePickers covers the fallback. Only resume and cd have a
// size worth deriving; everything else prints progress of a length nobody can
// predict, and a small fixed popup beats a proportion of the terminal for those.
func TestPopupSizesOnlyThePickers(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "eng-1")

	env := &Env{Argv0: "treewright"}
	sized, _ := sizeFor(env, "resume")
	fixed, _ := sizeFor(env, "new")
	if sized == fixed {
		t.Errorf("resume and new both sized %d; resume should follow its worktrees", sized)
	}

	// An unknown command must not panic its way to a size.
	if w, h := sizeFor(env, "nonesuch"); w <= 0 || h <= 0 {
		t.Errorf("sizeFor(unknown) = %dx%d, want the fallback", w, h)
	}
}
