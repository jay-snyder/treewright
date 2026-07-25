package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jay-snyder/treemux/internal/config"
	"github.com/jay-snyder/treemux/internal/git"
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
	switch info.Status {
	case git.StatusDirty:
		return fmt.Sprintf("dirty (%d)", info.DirtyFiles)
	case git.StatusUnpushed:
		return fmt.Sprintf("unpushed (%d)", info.Unpushed)
	default:
		return string(info.Status)
	}
}

// worktreeTable builds the table shown by `ls` and used as the `resume` and `cd`
// menus, so a menu is a picker over the same rows the user already knows.
//
// The worktree the caller is standing in gets a leading asterisk, and the column
// holding it appears only when one of the rows is in fact the current directory:
// a marker column that is blank on every row would be a permanent indent paid
// for a case that is not occurring.
func worktreeTable(infos []git.Info, windows map[string]string) ui.Table {
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
		window := "-"
		if windows[info.Dir] != "" {
			window = "open"
		}
		cells := []ui.Cell{
			ui.Text(info.Slug),
			ui.Colored(statusText(info), statusColor(info.Status)),
			ui.Text(divergence),
			ui.Text(window),
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

// worktreeJSON is the machine-readable form of one worktree.
//
// Ahead and Behind are pointers so that an impossible comparison serializes as
// null rather than as 0, which would claim the branch is level with its base.
type worktreeJSON struct {
	Slug       string `json:"slug"`
	Dir        string `json:"dir"`
	Branch     string `json:"branch"`
	Status     string `json:"status"`
	Ahead      *int   `json:"ahead"`
	Behind     *int   `json:"behind"`
	DirtyFiles int    `json:"dirty_files"`
	Unpushed   int    `json:"unpushed"`
	Window     string `json:"window"`
}

func worktreesJSON(infos []git.Info, windows map[string]string) []worktreeJSON {
	out := make([]worktreeJSON, 0, len(infos))
	for _, info := range infos {
		row := worktreeJSON{
			Slug:       info.Slug,
			Dir:        info.Dir,
			Branch:     info.Branch,
			Status:     string(info.Status),
			DirtyFiles: info.DirtyFiles,
			Unpushed:   info.Unpushed,
			Window:     windows[info.Dir],
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

// parseArgs splits args into recognized boolean flags and positional values,
// rejecting unknown flags and more positionals than the command accepts.
//
// Flags are accepted in any position, so `treemux rm slug -f` and `treemux rm -f
// slug` both work. Go's flag package stops at the first non-flag argument and
// would read the former's -f as a positional, which is why this is hand-rolled.
func parseArgs(cmd string, args []string, flags map[string]*bool, maxPositional int) ([]string, error) {
	var positional []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			target, ok := flags[a]
			if !ok {
				return nil, usageErrorf(cmd, "unknown flag %q", a)
			}
			*target = true
			continue
		}
		positional = append(positional, a)
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
