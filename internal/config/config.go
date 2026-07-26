// Package config loads treewright's per-repo configuration.
//
// One TOML file per repository lives in the registry directory:
//
//	${TREEWRIGHT_CONFIG_DIR:-${XDG_CONFIG_HOME:-~/.config}/treewright/repos}/<name>.toml
//
// The format is TOML rather than a sourced shell script so that reading a config
// cannot execute code: configs are meant to be shared, linted, and generated,
// none of which should require trusting their author.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Defaults applied when a config leaves a field out.
const (
	DefaultBaseBranch = "main"
	DefaultCommand    = "claude"

	// DefaultResumeCommand reattaches to the previous session in a worktree.
	// Because each worktree has its own path, `claude --continue` resumes
	// exactly the session that last ran there.
	DefaultResumeCommand = "claude --continue"

	// DefaultTicketPattern recognizes a leading issue key such as "proj-142" or
	// "bug-7" in a slug, so the tmux window can be named after the ticket rather
	// than after a truncated slug. Submatch 1 becomes the window name.
	DefaultTicketPattern = `(?i)^([a-z]+-[0-9]+)`
)

// Config is one repository's settings.
type Config struct {
	// Name is the config's file name without the .toml suffix. It is what a
	// user passes to `treewright ls <name>`. Not read from the file itself.
	Name string `toml:"-"`

	// explicit holds the keys the file actually set, so that reporting a
	// configuration can distinguish a value that was chosen from one that was
	// merely defaulted — including when the two happen to coincide.
	explicit map[string]bool `toml:"-"`

	// MainDir is the repository's main checkout. Required. Managed worktrees
	// are its siblings, named "<MainDir>-<slug>".
	MainDir string `toml:"main_dir"`

	// BaseBranch is the branch new work forks from and is compared against.
	BaseBranch string `toml:"base_branch"`

	// BranchPrefix is prepended to a slug to form the branch name, e.g. "alice/".
	// The singular spelling of BranchPrefixes, and the one every config written
	// before a repo could have several says.
	BranchPrefix string `toml:"branch_prefix"`

	// BranchPrefixes are the prefixes this repository's branches may carry, most
	// preferred first, for teams that namespace by kind of work rather than by
	// person: ["feature/", "bug/", "chore/"]. Which one a branch gets is chosen
	// by naming it at `new`; a bare slug gets the first.
	//
	// Read through Prefixes rather than directly, which folds the singular
	// spelling into the same list.
	BranchPrefixes []string `toml:"branch_prefixes"`

	// CarryFiles are paths, relative to MainDir, copied into each new worktree.
	// Git ignores these files, so a new worktree starts without them, and the
	// app needs them: .env files, local credentials, editor settings.
	CarryFiles []string `toml:"carry_files"`

	// Command is what to launch in the new tmux window. Defaults to "claude".
	Command string `toml:"command"`

	// ResumeCommand is what `treewright resume` launches. It is separate from
	// Command because resuming is usually a different invocation, and the two
	// default independently: setting Command alone does not change this.
	ResumeCommand string `toml:"resume_command"`

	// PostCreate is an optional shell command run in a new worktree after it is
	// created, for dependency installation. It runs in the background.
	PostCreate string `toml:"post_create"`

	// TicketPattern is a regular expression whose first submatch names the tmux
	// window when it matches a slug. Defaults to DefaultTicketPattern.
	TicketPattern string `toml:"ticket_pattern"`

	// TmuxSession names the session holding this repository's windows. It
	// defaults to the config's own name, and exists for the two cases the
	// default cannot serve: a session name already taken by something else, and
	// two configs that deliberately want to share one session.
	TmuxSession string `toml:"tmux_session"`
}

// Dir returns the registry directory holding the per-repo config files.
func Dir() string {
	if d := os.Getenv("TREEWRIGHT_CONFIG_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// Without a home directory there is nowhere conventional to look;
			// a relative path at least produces a comprehensible error later.
			return filepath.Join(".config", "treewright", "repos")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "treewright", "repos")
}

// Names lists the configs in the registry, sorted, without the .toml suffix.
func Names() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(Dir(), "*.toml"))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".toml"))
	}
	sort.Strings(names)
	return names, nil
}

// Load reads and validates a single config file.
func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	// Reject unknown keys: a typo like "base-branch" would otherwise be silently
	// ignored, leaving the user to wonder why their base branch is still "main".
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("%s: unknown setting(s): %s", filepath.Base(path), strings.Join(keys, ", "))
	}

	c.explicit = make(map[string]bool, len(md.Keys()))
	for _, k := range md.Keys() {
		c.explicit[k.String()] = true
	}

	c.Name = strings.TrimSuffix(filepath.Base(path), ".toml")
	if strings.TrimSpace(c.MainDir) == "" {
		return nil, fmt.Errorf("%s: main_dir is required", filepath.Base(path))
	}
	// Resolve symlinks, not just the path text. git reports fully resolved paths
	// for every worktree, so a main_dir that reaches the repo through a symlink
	// would never match what git says and its worktrees would be invisible.
	c.MainDir = canonical(expandPath(c.MainDir))

	// branch_prefix and branch_prefixes are two spellings of one setting, and
	// Prefixes reads either. Setting both is refused rather than resolved:
	// whichever precedence we picked would be a rule to learn, and the file itself
	// would no longer say which value was in force.
	if c.Explicit("branch_prefix") && c.Explicit("branch_prefixes") {
		return nil, fmt.Errorf("%s: set branch_prefix or branch_prefixes, not both", filepath.Base(path))
	}
	if c.Explicit("branch_prefixes") {
		if len(c.BranchPrefixes) == 0 {
			return nil, fmt.Errorf("%s: branch_prefixes is empty — list at least one prefix, or remove the key", filepath.Base(path))
		}
		// A duplicate changes nothing about which prefix a slug resolves to, and is
		// always a mistake — usually half of a rename. Cheap to catch here, and
		// invisible otherwise.
		seen := make(map[string]bool, len(c.BranchPrefixes))
		for _, p := range c.BranchPrefixes {
			if seen[p] {
				return nil, fmt.Errorf("%s: branch_prefixes lists %q twice", filepath.Base(path), p)
			}
			seen[p] = true
		}
	}

	if c.BaseBranch == "" {
		c.BaseBranch = DefaultBaseBranch
	}
	if c.Command == "" {
		c.Command = DefaultCommand
	}
	if c.ResumeCommand == "" {
		c.ResumeCommand = DefaultResumeCommand
	}
	if c.TicketPattern == "" {
		c.TicketPattern = DefaultTicketPattern
	}
	if _, err := regexp.Compile(c.TicketPattern); err != nil {
		return nil, fmt.Errorf("%s: ticket_pattern is not a valid regexp: %w", filepath.Base(path), err)
	}
	return &c, nil
}

// Resolve picks which config to use, in order:
//  1. an explicit config name, when the user passed one;
//  2. the config whose main_dir is the repository the caller is standing in;
//  3. the only config, when the registry holds exactly one;
//  4. otherwise an error listing the available names.
//
// repoMainDir is the main checkout of the caller's current repository, or ""
// when the caller is not inside one. Taking it as an argument keeps this
// package free of any dependency on git and makes every branch above testable.
func Resolve(name, repoMainDir string) (*Config, error) {
	names, err := Names()
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no repo configs in %s", Dir())
	}

	if name != "" {
		path := filepath.Join(Dir(), name+".toml")
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("no config %q in %s (have: %s)", name, Dir(), strings.Join(names, ", "))
		}
		return Load(path)
	}

	if repoMainDir != "" {
		want := canonical(repoMainDir)
		for _, n := range names {
			c, err := Load(filepath.Join(Dir(), n+".toml"))
			if err != nil {
				// A broken config elsewhere in the registry must not block work
				// on a repo whose own config is fine; skip it while matching.
				continue
			}
			if canonical(c.MainDir) == want {
				return c, nil
			}
		}
		return nil, fmt.Errorf("no registered config matches repo %s (have: %s)", repoMainDir, strings.Join(names, ", "))
	}

	if len(names) == 1 {
		return Load(filepath.Join(Dir(), names[0]+".toml"))
	}
	return nil, fmt.Errorf("not in a repo — pass a repo name (have: %s)", strings.Join(names, ", "))
}

// ---- derived values --------------------------------------------------------

// Path returns the file this config was read from.
func (c *Config) Path() string { return filepath.Join(Dir(), c.Name+".toml") }

// Explicit reports whether the file set a key itself, as opposed to leaving it
// to its default. Keys are the TOML spellings, e.g. "base_branch".
func (c *Config) Explicit(key string) bool { return c.explicit[key] }

// DirFor returns the worktree directory a slug maps to.
func (c *Config) DirFor(slug string) string { return c.MainDir + "-" + slug }

// Prefixes returns the branch prefixes in force, most preferred first.
//
// Always at least one element, so nothing downstream has to special-case a repo
// that namespaces nothing: with neither key set, the one prefix is the empty
// string, which prepends nothing. The singular branch_prefix is exactly the
// one-element case, which is why Load never rewrites one spelling into the other.
func (c *Config) Prefixes() []string {
	if len(c.BranchPrefixes) > 0 {
		return c.BranchPrefixes
	}
	return []string{c.BranchPrefix}
}

// SplitPrefix splits what the user typed into the branch prefix it names and the
// slug that remains, reporting whether it found a prefix at all.
//
// This is the whole of how a repo with several prefixes picks between them:
// "feature/eng-1" makes branch feature/eng-1, and the worktree is still named
// after the slug alone. The longest match wins, so nested prefixes ("feature/"
// and "feature/exp/") resolve to the more specific one rather than to whichever
// happens to come first in the list.
//
// A leading word naming no configured prefix stays in the slug, where the caller
// rejects it: guessing that "feat/" meant "feature/" would create a branch nobody
// asked for, and accepting it as written would namespace one outside the scheme
// the repo agreed on.
//
// Only one copy is taken. If a user really does want a slug named "alice/foo"
// under prefix "alice/", stripping repeatedly would make that unreachable.
func (c *Config) SplitPrefix(typed string) (prefix, slug string, matched bool) {
	best := ""
	for _, p := range c.Prefixes() {
		// The empty prefix matches every word, and reporting that as a match would
		// make each bare slug arrive as a prefix having been found.
		if p == "" || !strings.HasPrefix(typed, p) {
			continue
		}
		if len(p) > len(best) {
			best = p
		}
	}
	if best == "" {
		return c.Prefixes()[0], typed, false
	}
	return best, strings.TrimPrefix(typed, best), true
}

// WindowName derives the tmux window name for a slug, uppercased.
//
// Precedence: an explicit override wins; else a ticket key matched by
// TicketPattern (so "proj-142-white-screen" becomes "PROJ-142"); else the slug,
// truncated with an ellipsis past 10 characters to keep the tmux status line
// readable.
func (c *Config) WindowName(slug, override string) string {
	if override != "" {
		return strings.ToUpper(override)
	}
	// Compiled here rather than cached because this runs at most once per
	// command; Load has already proven the pattern compiles.
	if re, err := regexp.Compile(c.TicketPattern); err == nil {
		if m := re.FindStringSubmatch(slug); len(m) > 1 && m[1] != "" {
			return strings.ToUpper(m[1])
		}
	}
	if len(slug) > 10 {
		return strings.ToUpper(slug[:10]) + "..."
	}
	return strings.ToUpper(slug)
}

// ---- path helpers ----------------------------------------------------------

// expandPath resolves a leading ~ and any $VAR references, then cleans the
// result. TOML has no notion of either, so a config saying "~/code/repo" or
// "$HOME/code/repo" needs this to become a real path.
//
// A literal dollar sign in a path is not supported: "$" always introduces a
// variable reference, and an undefined one expands to nothing.
func expandPath(p string) string {
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return filepath.Clean(p)
}

// canonical resolves symlinks so that two spellings of the same directory
// compare equal. Git reports fully resolved paths, so a config pointing at a
// path that traverses a symlink would otherwise never match its own repo.
// Paths that do not exist are compared as-is.
func canonical(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}
