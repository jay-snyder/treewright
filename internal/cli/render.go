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

// worktreeTable builds the table shown by `ls` and used as the `resume` menu, so
// the menu is a picker over the same rows the user already knows.
func worktreeTable(infos []git.Info, windows map[string]string) ui.Table {
	table := ui.Table{Headers: []string{"SLUG", "STATUS", "AHEAD/BEHIND", "WINDOW"}}
	for _, info := range infos {
		divergence := "?" // an unavailable comparison is unknown, not zero
		if info.Compared {
			divergence = fmt.Sprintf("+%d/-%d", info.Ahead, info.Behind)
		}
		window := "-"
		if windows[info.Dir] != "" {
			window = "open"
		}
		table.Add(
			ui.Text(info.Slug),
			ui.Colored(string(info.Status), statusColor(info.Status)),
			ui.Text(divergence),
			ui.Text(window),
		)
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
