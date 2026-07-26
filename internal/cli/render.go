package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jay-snyder/treemux/internal/config"
	"github.com/jay-snyder/treemux/internal/git"
	"github.com/jay-snyder/treemux/internal/tmux"
	"github.com/jay-snyder/treemux/internal/ui"
)

// resolveConfig picks the config for this invocation. An explicit name wins;
// otherwise the repository the caller is standing in decides.
func resolveConfig(name string) (*config.Config, error) {
	repoMain := ""
	if wd, err := os.Getwd(); err == nil {
		// Asking git, rather than walking up for a .git entry, works identically
		// from a worktree, where .git is a file rather than a directory.
		if main, err := (git.Repo{Dir: wd}).MainDir(); err == nil {
			repoMain = main
		}
	}
	return config.Resolve(name, repoMain)
}

// repoFor returns the repository a config describes.
func repoFor(cfg *config.Config) git.Repo { return git.Repo{Dir: cfg.MainDir} }

// statusColor maps a status to how urgently it wants attention: green is safe to
// throw away, red is work that exists nowhere else, yellow needs a decision, and
// cyan is in flight and fine.
func statusColor(s git.Status) ui.Color {
	switch s {
	case git.StatusMerged:
		return ui.Green
	case git.StatusUnpushed:
		return ui.Red
	case git.StatusDirty:
		return ui.Yellow
	case git.StatusBase:
		// Deliberately outside that scale. The base checkout is not a candidate
		// for removal, so it gets the one color that is not urging anything.
		return ui.Dim
	default:
		return ui.Cyan
	}
}

// statusText spells out a status, carrying the count of what is at stake when
// there is one.
//
// The counts are already gathered, and they are the numbers the removal guards
// refuse over: "dirty (3)" says how much a --force would discard, where "dirty"
// alone leaves the reader to go and look.
func statusText(info git.Info) string {
	switch {
	case info.Status == git.StatusDirty:
		return fmt.Sprintf("dirty (%d)", info.DirtyFiles)
	case info.Status == git.StatusUnpushed:
		return fmt.Sprintf("unpushed (%d)", info.Unpushed)
	case info.Status == git.StatusBase && info.DirtyFiles > 0:
		// The base checkout keeps its own status — it is never removable, so
		// "dirty" would be answering a question nobody asks of it — but the count
		// still carries, because half-finished investigation left lying in the
		// window you launch everything from is worth seeing before you fork
		// another branch off it.
		return fmt.Sprintf("base (%d)", info.DirtyFiles)
	default:
		return string(info.Status)
	}
}

// slugCell names a row in the SLUG column.
//
// A worktree is named by its slug, which is also what you type at `resume` and
// `cd`. The base checkout has no slug — it is a checkout rather than something
// treemux created — so the column carries the branch it is parked on instead.
// That is the more useful of the two things it could say: the row is always the
// first one, so "base" would only repeat the position, while the branch is what
// tells you whether your general-purpose window is sitting on staging, on main,
// or somewhere you left it three days ago.
func slugCell(info git.Info) string {
	if info.Status != git.StatusBase {
		return info.Slug
	}
	if info.Branch == "" {
		return "detached"
	}
	return info.Branch
}

// windowCell says where a worktree's window is, in the width of a table column.
//
// The window's own name is more use than a bare "open" — it is what the status
// line shows — and a window that is not in this repository's session is named
// with the session it is in instead, since that is the surprising case and the
// one that explains why switching to it moves you somewhere unexpected.
func windowCell(w tmux.Window, session string) string {
	switch {
	case w.ID == "":
		return "-"
	case w.Session != session:
		return w.Session + ":" + w.Name
	default:
		return w.Name
	}
}

// worktreeTable builds the table shown by `ls` and used as the `resume` and `cd`
// menus, so a menu is a picker over the same rows the user already knows.
//
// Callers put the base checkout at the head of infos. It belongs in the list on
// both of the list's own terms: it is somewhere you return to between worktrees,
// and — since a tmux session does not survive a reboot while a checkout on disk
// does — it is something you reopen. Leaving it out made the one window that is
// always there, and that keeps the session alive, the one window the menu could
// not reach.
//
// The worktree the caller is standing in gets a leading asterisk, and the column
// holding it appears only when one of the rows is in fact the current directory:
// a marker column that is blank on every row would be a permanent indent paid
// for a case that is not occurring.
func worktreeTable(infos []git.Info, windows map[string]tmux.Window, session string) ui.Table {
	cwd, err := os.Getwd()
	here := -1
	if err == nil {
		for i, info := range infos {
			if insideDir(cwd, info.Dir) {
				here = i
				break
			}
		}
	}

	headers := []string{"SLUG", "STATUS", "AHEAD/BEHIND", "WINDOW"}
	if here >= 0 {
		headers = append([]string{""}, headers...)
	}

	table := ui.Table{Headers: headers}
	for i, info := range infos {
		divergence := "?" // an unavailable comparison is unknown, not zero
		if info.Compared {
			divergence = fmt.Sprintf("+%d/-%d", info.Ahead, info.Behind)
		}
		cells := []ui.Cell{
			ui.Text(slugCell(info)),
			ui.Colored(statusText(info), statusColor(info.Status)),
			ui.Text(divergence),
			ui.Text(windowCell(windows[info.Dir], session)),
		}
		if here >= 0 {
			marker := " "
			if i == here {
				marker = "*"
			}
			cells = append([]ui.Cell{ui.Text(marker)}, cells...)
		}
		table.Add(cells...)
	}
	return table
}

// popupSize estimates the popup a picker over these worktrees needs, in the
// cells display-popup takes.
//
// Estimated rather than measured. Measuring means building the table, and
// building the table means asking git for every worktree's status — the better
// part of a second on a repository with a few worktrees, and it would be paid twice
// before the popup so much as appeared. The inputs here are the two cheap
// listings the caller already has.
//
// It errs wide on purpose. A popup a few columns larger than its contents is
// invisible; one a few columns smaller wraps every row of the table, and one too
// short scrolls the head of the list away before it can be read.
//
// The rows are the table's, but filled in only as far as the width needs: the
// status and divergence columns are measured from their widest possible content
// rather than from these, so nothing here has to have been inspected. What the
// caller must get right is how many rows there are and what goes in the two
// columns the data can stretch, which is why the cells are measured through the
// very functions that render them.
//
// The layout mirrored here is worktreeTable's, and TestPopupSizeCoversTheTable
// renders a real one to check this still covers it.
func popupSize(rows []git.Info, windows map[string]tmux.Window) (width, height int) {
	const (
		// The two columns whose width the data cannot push past: the longest
		// status is "unpushed (nnn)", and divergence never outgrows its header.
		statusCol     = len("unpushed (nnn)")
		divergenceCol = len("AHEAD/BEHIND")
		gap           = 2
		// The current-worktree marker column, which appears only sometimes, and
		// is cheaper to always allow for than to predict.
		markerCol = 1 + gap
		// display-popup spends a row and a column on each side drawing a border,
		// so the interior is two smaller than what -w and -h ask for.
		border = 2
		// The picker's own prompt line, which is what sets the floor on a
		// repository with one short slug in it.
		promptCol = len("select 1-nn (Esc to cancel): ")
	)

	slugCol, windowCol := len("SLUG"), len("WINDOW")
	for _, info := range rows {
		slugCol = max(slugCol, len(slugCell(info)))
		windowCol = max(windowCol, len(windowCell(windows[info.Dir], "")))
	}
	// The "n) " the picker puts in front of every row, and the same indent it
	// gives the header.
	indexCol := len(strconv.Itoa(len(rows))) + 2

	width = indexCol + markerCol + slugCol + gap + statusCol + gap + divergenceCol + gap + windowCol
	width = max(width, promptCol) + border

	// A header, a row each, a blank line, and the prompt.
	height = 1 + len(rows) + 1 + 1 + border
	return width, height
}

// worktreeJSON is the machine-readable form of one worktree.
//
// Ahead and Behind are pointers so that an impossible comparison serializes as
// null rather than as 0, which would claim the branch is level with its base.
type worktreeJSON struct {
	Slug string `json:"slug"`

	// Base marks the main checkout, which is in this listing for the same reason
	// it is in the table but is not one of the worktrees: it has no slug, and
	// `rm` and `prune` cannot name it. A consumer deciding what to tear down —
	// an agent reading this to work out where a ticket should go — needs the
	// distinction spelled out rather than inferred from an empty slug.
	Base bool `json:"base"`

	Dir        string `json:"dir"`
	Branch     string `json:"branch"`
	Status     string `json:"status"`
	Ahead      *int   `json:"ahead"`
	Behind     *int   `json:"behind"`
	DirtyFiles int    `json:"dirty_files"`
	Unpushed   int    `json:"unpushed"`

	// The open window, in three fields rather than one: the name is what a human
	// reads, the id is what `tmux kill-window -t` takes, and the session is what
	// `tmux attach -t` takes. All three are empty when no window is open.
	Window        string `json:"window"`
	WindowID      string `json:"window_id"`
	WindowSession string `json:"window_session"`
}

func worktreesJSON(infos []git.Info, windows map[string]tmux.Window) []worktreeJSON {
	out := make([]worktreeJSON, 0, len(infos))
	for _, info := range infos {
		w := windows[info.Dir]
		row := worktreeJSON{
			Slug:          info.Slug,
			Base:          info.Status == git.StatusBase,
			Dir:           info.Dir,
			Branch:        info.Branch,
			Status:        string(info.Status),
			DirtyFiles:    info.DirtyFiles,
			Unpushed:      info.Unpushed,
			Window:        w.Name,
			WindowID:      w.ID,
			WindowSession: w.Session,
		}
		if info.Compared {
			ahead, behind := info.Ahead, info.Behind
			row.Ahead, row.Behind = &ahead, &behind
		}
		out = append(out, row)
	}
	return out
}

// writeJSON emits indented JSON with a trailing newline, so output reads well in
// a terminal and still parses.
func writeJSON(env *Env, v any) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, string(encoded))
	return nil
}

// parseArgs splits args into recognized flags and positional values, rejecting
// unknown flags and more positionals than the command accepts.
//
// Flags are accepted in any position, so `treemux rm slug -f` and `treemux rm -f
// slug` both work. Go's flag package stops at the first non-flag argument and
// would read the former's -f as a positional, which is why this is hand-rolled.
//
// A flag in flags is a switch and takes no value; one in values takes the next
// argument, or the text after an "=". Both spellings are accepted because both
// are what people type: --resume-key T and --resume-key=T.
func parseArgs(cmd string, args []string, flags map[string]*bool, values map[string]*string, maxPositional int) ([]string, error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}

		// "--key=value" is split here so the two spellings meet at the same place.
		name, inline, joined := strings.Cut(a, "=")
		if target, ok := flags[name]; ok {
			if joined {
				return nil, usageErrorf(cmd, "flag %q takes no value", name)
			}
			*target = true
			continue
		}
		target, ok := values[name]
		if !ok {
			return nil, usageErrorf(cmd, "unknown flag %q", name)
		}
		if joined {
			*target = inline
			continue
		}
		// A flag consuming the next argument must not swallow another flag: that
		// turns a forgotten value into a silently mis-parsed command line rather
		// than into a message about the value being missing.
		if i+1 >= len(args) || (strings.HasPrefix(args[i+1], "-") && args[i+1] != "-") {
			return nil, usageErrorf(cmd, "flag %q needs a value", name)
		}
		i++
		*target = args[i]
	}
	if len(positional) > maxPositional {
		// Silently dropping an argument hides a typo: a slug with a stray space
		// in it would look as though it had simply been accepted.
		return nil, usageErrorf(cmd, "unexpected argument %q", positional[maxPositional])
	}
	return positional, nil
}

// at returns the nth positional argument, or "" when absent.
func at(positional []string, n int) string {
	if n < len(positional) {
		return positional[n]
	}
	return ""
}

// stripPrefix removes a branch prefix the user typed into the slug, reporting the
// correction so it is visible rather than silent.
func stripPrefix(env *Env, cfg *config.Config, slug string) string {
	stripped, changed := cfg.StripPrefix(slug)
	if changed {
		env.progressf("stripped the %q prefix from the slug — the branch gets it automatically (using %q)",
			cfg.BranchPrefix, stripped)
	}
	return stripped
}

// slugsOf lists worktree slugs, for naming the alternatives in an error.
func slugsOf(worktrees []git.Worktree) string {
	slugs := make([]string, 0, len(worktrees))
	for _, wt := range worktrees {
		slugs = append(slugs, wt.Slug)
	}
	return strings.Join(slugs, ", ")
}

// resolveSlug turns what the user typed into the worktree they meant.
//
// An exact slug always wins. Failing that an unambiguous prefix is accepted,
// because a slug carries both a ticket key and a description —
// "eng-1646-app-landing-page-redesign" — while people refer to that work as
// "eng-1646". The expansion is reported rather than applied silently, since the
// caller may be about to delete what it resolved to.
//
// An ambiguous prefix is an error listing the candidates: guessing among them
// would eventually guess wrong on a command that destroys something.
func resolveSlug(env *Env, repo git.Repo, managed []git.Worktree, want string) (git.Worktree, error) {
	var prefixed []git.Worktree
	for _, wt := range managed {
		if wt.Slug == want {
			return wt, nil
		}
		if strings.HasPrefix(wt.Slug, want) {
			prefixed = append(prefixed, wt)
		}
	}

	switch len(prefixed) {
	case 1:
		env.progressf("%s matches worktree %s", want, prefixed[0].Slug)
		return prefixed[0], nil
	case 0:
		return git.Worktree{}, fmt.Errorf("no worktree %q for %s (have: %s)",
			want, repo.Name(), slugsOf(managed))
	default:
		return git.Worktree{}, fmt.Errorf("%q matches %d worktrees (%s) — name one exactly",
			want, len(prefixed), slugsOf(prefixed))
	}
}
