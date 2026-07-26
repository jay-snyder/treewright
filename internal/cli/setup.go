package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jay-snyder/treemux/internal/config"
	"github.com/jay-snyder/treemux/internal/git"
	"github.com/jay-snyder/treemux/internal/ui"
)

// ---- setup -----------------------------------------------------------------

// cmdSetup registers the repository the caller is standing in.
//
// Everything treemux needs beyond main_dir has a default or can be guessed from
// the repository itself, so the fastest path from "installed" to "working" is a
// generated file. What it writes is ordinary TOML with the guesses commented, on
// the principle that the file remains the record: setup is a way to start one,
// not a layer above it.
func cmdSetup(env *Env, args []string) error {
	var dryRun bool
	positional, err := parseArgs("setup", args, map[string]*bool{
		"-n": &dryRun, "--dry-run": &dryRun,
	}, nil, 1)
	if err != nil {
		return err
	}

	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	repo := git.Repo{Dir: wd}
	mainDir, err := repo.MainDir()
	if err != nil {
		return fmt.Errorf("not in a git repository — run this from the repo you want to register")
	}
	// Queries run against the main checkout, not the caller's directory, so
	// running setup from inside a worktree registers the same repository.
	repo = git.Repo{Dir: mainDir}

	name := at(positional, 0)
	if name == "" {
		name = filepath.Base(mainDir)
	}
	if err := validateConfigName(name); err != nil {
		return err
	}

	path := filepath.Join(config.Dir(), name+".toml")
	if _, err := os.Stat(path); err == nil && !dryRun {
		return fmt.Errorf("%s already exists — edit it, or pass a different name", path)
	}
	// A second config for the same repository would make which one applies depend
	// on registry order, so say so rather than creating the ambiguity.
	if existing, err := config.Resolve("", mainDir); err == nil && existing.Name != name {
		return fmt.Errorf("%s is already registered as %q in %s", mainDir, existing.Name, existing.Path())
	}

	baseBranch := repo.DefaultBranch()
	prefix := branchPrefixFor(repo.UserEmail())
	carry := carryCandidates(repo.IgnoredFiles())

	env.progressf("main checkout %s", mainDir)
	env.progressf("base branch %s, from origin/HEAD", baseBranch)
	if prefix == "" {
		env.progressf("no branch prefix — git has no user.email configured here")
	} else {
		env.progressf("branch prefix %q, from your git email — branches will be %seng-1", prefix, prefix)
	}
	switch len(carry) {
	case 0:
		env.progressf("no gitignored env files found to carry into new worktrees")
	default:
		env.progressf("carrying %d gitignored file(s): %s", len(carry), strings.Join(carry, ", "))
	}

	body := renderConfig(name, mainDir, baseBranch, prefix, carry)
	if dryRun {
		fmt.Fprint(env.Stdout, body)
		env.progressf("nothing written — remove --dry-run to save this to %s", path)
		return nil
	}

	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, path)
	env.progressf("registered %s as %q — check it with \"treemux doctor\", then start work with \"treemux new <slug>\"",
		filepath.Base(mainDir), name)
	return nil
}

// validateConfigName rejects a name that would not survive being turned into a
// file name in the registry, where "../x" would write outside it entirely.
func validateConfigName(name string) error {
	switch {
	case strings.ContainsAny(name, `/\`), name == "." || name == "..":
		return usageErrorf("setup", "config name %q cannot be a path — it becomes a file in %s", name, config.Dir())
	case strings.HasPrefix(name, "-"):
		return usageErrorf("setup", "config name %q cannot start with %q — it would read as a flag", name, "-")
	case strings.HasPrefix(name, "."):
		return usageErrorf("setup", "config name %q cannot start with %q — it would be a hidden file", name, ".")
	}
	return nil
}

// branchPrefixFor derives a branch namespace from a git email, so that branches
// are attributable on a shared remote: "jay.snyder@example.com" gives "jay/".
//
// The first dotted or plussed component is used rather than the whole local part,
// because "jay.snyder/eng-1" reads as a path with a surname in it while "jay/" is
// what people actually name their branches. It is a guess in a file the user can
// edit, and setup says what it chose.
func branchPrefixFor(email string) string {
	local, _, _ := strings.Cut(email, "@")
	local, _, _ = strings.Cut(local, "+")
	first, _, _ := strings.Cut(local, ".")

	var b strings.Builder
	for _, r := range strings.ToLower(first) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "/"
}

// carryCandidates picks, out of everything git is ignoring, the files a fresh
// worktree most likely cannot run without.
//
// Only env files are proposed. They are the case where a missing file breaks the
// app immediately and confusingly, and they are recognizable by name — unlike
// local credentials or editor settings, which vary too much between projects to
// guess at without proposing junk.
func carryCandidates(ignored []string) []string {
	const limit = 10
	var found []string
	for _, rel := range ignored {
		// A trailing slash marks a wholly ignored directory: build output, a
		// dependency tree. Copying one into every worktree is the opposite of
		// what the worktree is for.
		if strings.HasSuffix(rel, "/") {
			continue
		}
		base := filepath.Base(rel)
		if base != ".envrc" && !strings.HasPrefix(base, ".env") {
			continue
		}
		// Templates are committed alongside the real file and carry no values, so
		// a fresh checkout already has them.
		if strings.HasSuffix(base, ".example") || strings.HasSuffix(base, ".sample") || strings.HasSuffix(base, ".template") {
			continue
		}
		found = append(found, rel)
	}
	sort.Strings(found)
	if len(found) > limit {
		found = found[:limit]
	}
	return found
}

// renderConfig writes the config file's text.
//
// It is commented the way a config kept under review would be, because this file
// is where every later question about treemux's behavior gets answered, and
// because the values it holds are guesses that deserve to be reviewed rather than
// inherited silently.
func renderConfig(name, mainDir, baseBranch, prefix string, carry []string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# treemux config for %s, generated by \"treemux setup\".\n", name)
	fmt.Fprintf(&b, "# Every value below was detected — edit anything that is wrong.\n\n")

	fmt.Fprintf(&b, "# The repository's main checkout. Worktrees are created as its siblings,\n")
	fmt.Fprintf(&b, "# named <main_dir>-<slug>.\n")
	fmt.Fprintf(&b, "main_dir = %s\n\n", tomlString(abbreviateHome(mainDir)))

	fmt.Fprintf(&b, "# New branches fork from origin/%s, and every status is measured against it.\n", baseBranch)
	fmt.Fprintf(&b, "base_branch = %s\n\n", tomlString(baseBranch))

	if prefix == "" {
		fmt.Fprintf(&b, "# Prepended to a slug to form the branch name, e.g. \"alice/\" gives\n")
		fmt.Fprintf(&b, "# alice/eng-1. Left empty: git has no user.email configured for this repo.\n")
		fmt.Fprintf(&b, "# branch_prefix = \"alice/\"\n\n")
	} else {
		fmt.Fprintf(&b, "# Prepended to a slug to form the branch name: %seng-1.\n", prefix)
		fmt.Fprintf(&b, "branch_prefix = %s\n\n", tomlString(prefix))
	}

	fmt.Fprintf(&b, "# Gitignored files a fresh checkout lacks but the app needs. Paths are\n")
	fmt.Fprintf(&b, "# relative to main_dir, and are copied into every new worktree.\n")
	if len(carry) == 0 {
		fmt.Fprintf(&b, "# Nothing was detected; add what your app needs, e.g.:\n")
		fmt.Fprintf(&b, "# carry_files = [\".env.local\", \"apps/api/.env\"]\n\n")
	} else {
		fmt.Fprintf(&b, "carry_files = [\n")
		for _, rel := range carry {
			fmt.Fprintf(&b, "  %s,\n", tomlString(rel))
		}
		fmt.Fprintf(&b, "]\n\n")
	}

	fmt.Fprintf(&b, "# What to launch in the tmux window. command is used by new and base,\n")
	fmt.Fprintf(&b, "# resume_command by resume; the two default independently.\n")
	fmt.Fprintf(&b, "# command        = %s\n", tomlString(config.DefaultCommand))
	fmt.Fprintf(&b, "# resume_command = %s\n\n", tomlString(config.DefaultResumeCommand))

	fmt.Fprintf(&b, "# Run in the background in each new worktree, for dependency installation.\n")
	fmt.Fprintf(&b, "# post_create = \"npm install\"\n\n")

	fmt.Fprintf(&b, "# Regexp whose first submatch names the tmux window, so a slug like\n")
	fmt.Fprintf(&b, "# eng-142-white-screen opens a window called ENG-142. Pin it to your own\n")
	fmt.Fprintf(&b, "# ticket scheme to stop it matching any letters-dash-digits prefix.\n")
	fmt.Fprintf(&b, "# ticket_pattern = %s\n\n", tomlString(config.DefaultTicketPattern))

	fmt.Fprintf(&b, "# The tmux session holding this repository's windows, so that they stay\n")
	fmt.Fprintf(&b, "# separate from every other repository's. Defaults to %q.\n", name)
	fmt.Fprintf(&b, "# tmux_session = %s\n", tomlString(name))

	return b.String()
}

// tomlString quotes a value as a TOML basic string. Go's escaping rules coincide
// with TOML's for everything that can appear in a path or a branch name.
func tomlString(s string) string { return strconv.Quote(s) }

// abbreviateHome writes a path under the home directory back as "~/...", which
// config expands again on the way in. A generated file then reads the way a
// hand-written one would, and survives being copied to another machine.
func abbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	if rel == "." {
		return "~"
	}
	return "~/" + rel
}

// ---- config ----------------------------------------------------------------

// cmdConfig prints the settings in force.
//
// Its reason for existing is the gap between a config file and the behavior it
// produces: defaults are invisible in the file, paths are written unexpanded, and
// which of several configs applies depends on where you are standing. This
// answers all three at once.
func cmdConfig(env *Env, args []string) error {
	positional, err := parseArgs("config", args, nil, nil, 1)
	if err != nil {
		return err
	}
	cfg, err := resolveConfig(at(positional, 0))
	if err != nil {
		return err
	}

	table := ui.Table{Headers: []string{"SETTING", "VALUE"}}
	add := func(key, value string, explicit bool) {
		if value == "" {
			table.Add(ui.Text(key), ui.Colored("(none)", ui.Dim))
			return
		}
		if explicit {
			table.Add(ui.Text(key), ui.Text(value))
			return
		}
		// Marked rather than hidden: a default is still the value in force, and
		// the surprise is usually that a setting was never set at all.
		table.Add(ui.Text(key), ui.Text(value+"  (default)"))
	}

	table.Add(ui.Text("name"), ui.Text(cfg.Name))
	table.Add(ui.Text("file"), ui.Text(cfg.Path()))
	add("main_dir", cfg.MainDir, true)
	add("base_branch", cfg.BaseBranch, cfg.Explicit("base_branch"))
	add("branch_prefix", cfg.BranchPrefix, cfg.Explicit("branch_prefix"))
	add("carry_files", strings.Join(cfg.CarryFiles, ", "), cfg.Explicit("carry_files"))
	add("command", cfg.Command, cfg.Explicit("command"))
	add("resume_command", cfg.ResumeCommand, cfg.Explicit("resume_command"))
	add("post_create", cfg.PostCreate, cfg.Explicit("post_create"))
	add("ticket_pattern", cfg.TicketPattern, cfg.Explicit("ticket_pattern"))
	// The session name in force, not the raw setting: what a reader wants to know
	// is which session their windows land in, which is the config's name until
	// tmux_session says otherwise.
	add("tmux_session", sessionFor(cfg), cfg.Explicit("tmux_session"))

	table.Render(env.Stdout, ui.ColorEnabled(env.Stdout))
	return nil
}
