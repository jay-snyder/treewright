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
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jay-snyder/treewright/internal/agentinit"
	"github.com/jay-snyder/treewright/internal/refname"
)

// Defaults applied when a config leaves a field out.
const (
	DefaultBaseBranch = "main"

	// {prompt} is where `new --prompt` puts its text, shell-quoted; without a
	// prompt the placeholder is removed entirely. The substitution lives in
	// internal/cli — to the config this is only a string — but the default
	// carries the placeholder so the kickoff prompt works out of the box.
	DefaultCommand = "claude {prompt}"

	// DefaultResumeCommand reattaches to the previous session in a worktree.
	// Because each worktree has its own path, `claude --continue` resumes
	// exactly the session that last ran there.
	DefaultResumeCommand = "claude --continue {prompt}"

	// DefaultTicketPattern recognizes a leading issue key such as "proj-142" or
	// "bug-7" in a slug, so the tmux window can be named after the ticket rather
	// than after a shortened slug. Submatch 1 becomes the window name.
	//
	// The key has to be a whole word — the trailing (?:-|$) — because without it
	// the digits ended wherever they liked and ordinary English slugs came back
	// as ticket keys: "fix-2fa-login" opened a window called FIX-2. A repository
	// that does track work by ticket loses nothing to the anchor, since a key is
	// followed by the description or by the end of the slug either way.
	//
	// It is still a guess, and "refactor-2-pass-parser" is a key by any pattern
	// that does not know the scheme. Two ways out, both in the config: pin the
	// pattern to your own scheme, or — for work that has no ticket behind it at
	// all — set ticket_pattern = "" and let the slug name the window.
	DefaultTicketPattern = `(?i)^([a-z]+-[0-9]+)(?:-|$)`
)

// FormatVersion is the revision of this file format, written into every config
// `setup` generates and read back by `doctor`.
//
// It counts revisions of what a generated file *says* rather than treewright
// releases, so it moves when the generator does and stays put across releases
// that change nothing here. That is the number worth having: `setup` improves
// its defaults and its commentary, `setup --refresh` is how those reach a
// registered repository, and without a version in the file nothing could tell a
// config that had them from one written two years ago.
//
// It is deliberately not a migration hook. No key has ever been renamed, and a
// rename table would be machinery built for a hypothetical — what this supports
// is one warning naming one command.
const FormatVersion = 1

// Config is one repository's settings.
type Config struct {
	// Name is the config's file name without the .toml suffix. It is what a
	// user passes to `treewright ls <name>`. Not read from the file itself.
	Name string `toml:"-"`

	// Version is the FormatVersion the file was generated for. A file without
	// one is an old config rather than a broken one: every config in the wild
	// predates the key, and refusing them would make an upgrade of treewright
	// break every repository already registered.
	Version int `toml:"version"`

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

	// Agent names a built-in agent module (see internal/agentinit), which
	// supplies the defaults for Command and ResumeCommand and has the agent's
	// own per-project state files carried into every new worktree. A defaults
	// bundle, not a second spelling: setting Command alongside it overrides
	// that one field, and the file still says which command runs.
	//
	// Never inferred from Command's first word. Two configs saying
	// command = "claude" behaving differently on an invisible guess is the kind
	// of rule this format exists to avoid; the key is one line, and writing it
	// is what asks for the bundle.
	Agent string `toml:"agent"`

	// CarryFiles are paths, relative to MainDir, copied into each new worktree:
	// .env files, local credentials, editor settings. What puts a file here is
	// that a fresh checkout does not have it and the app needs it, which is
	// usually because git ignores it — but only usually, and AgentCarries below
	// adds files git ignores not at all.
	CarryFiles []string `toml:"carry_files"`

	// Command is what to launch in the new tmux window. Defaults to
	// DefaultCommand, "claude {prompt}".
	Command string `toml:"command"`

	// ResumeCommand is what `treewright resume` launches. It is separate from
	// Command because resuming is usually a different invocation, and the two
	// default independently: setting Command alone does not change this.
	ResumeCommand string `toml:"resume_command"`

	// PostCreate is the optional setup to run in a new worktree after it is
	// created, for dependency installation. It runs in the background, and may be
	// written as one command or as a list of them to run in order.
	PostCreate Commands `toml:"post_create"`

	// TicketPattern is a regular expression whose first submatch names the tmux
	// window when it matches a slug. Defaults to DefaultTicketPattern.
	TicketPattern string `toml:"ticket_pattern"`

	// TmuxSession names the session holding this repository's windows. It
	// defaults to the config's own name, and exists for the two cases the
	// default cannot serve: a session name already taken by something else, and
	// two configs that deliberately want to share one session.
	TmuxSession string `toml:"tmux_session"`
}

// Commands is a setting written either as one shell command or as a list of them
// to run in order:
//
//	post_create = "npm install"
//	post_create = ["npm install", "npm run codegen", "npm run build"]
//
// Two shapes under one key, rather than the two keys branch_prefix and
// branch_prefixes are: the plural of this setting has no name that reads like
// anything, and a second key would let a file set both and leave a reader to
// learn which one won. One key that takes either shape has neither problem —
// what the file says is what runs, and every config written before the list
// existed still means what it meant.
//
// The list is not merely a nicety over "a && b && c" written out longhand. It is
// what lets the log name the step it is on, and the step it stopped at.
type Commands []string

// UnmarshalTOML accepts either spelling. A string becomes the single command it
// is; a list becomes its elements.
//
// An empty string stays empty rather than becoming a command that runs nothing,
// because `post_create = ""` is how a config that once had a setup step says it
// no longer does. An empty element inside a list is refused instead: a blank line
// in a list of commands is a half-finished edit, and skipping it silently would
// hide the mistake behind a run that looked fine.
func (c *Commands) UnmarshalTOML(v any) error {
	switch value := v.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			*c = nil
			return nil
		}
		*c = Commands{value}
		return nil
	case []any:
		list := make(Commands, 0, len(value))
		for i, item := range value {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("entry %d is %T, not a command", i+1, item)
			}
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("entry %d is empty — remove it, or write the command", i+1)
			}
			list = append(list, s)
		}
		*c = list
		return nil
	default:
		return fmt.Errorf("want one command or a list of them, not %T", v)
	}
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
	//
	// The second line is the reading this message could not give before. A
	// config is a file two treewrights may see — one on a laptop, a newer one on
	// a desktop, or the same machine either side of a downgrade — and the newer
	// one's settings arrive here as typos. Naming version skew costs a line and
	// saves the reader hunting for a misspelling that is not there. No command
	// is named because this package has no argv0 to name it with; `doctor` and
	// `version --check` are where the question gets answered.
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("%s: unknown setting(s): %s\nremove them, or upgrade treewright — a newer one may have added them",
			filepath.Base(path), strings.Join(keys, ", "))
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
	// Checked here rather than left to git, for the same reason a slug is: a prefix
	// is hand-written, often several at a time, and the alternative is git refusing
	// a branch three steps into a `new` that has already said what it was doing —
	// about a value the user last looked at when they wrote the file. The error names
	// the prefix, and every command loads the config, so `doctor` reports it too.
	for _, p := range c.Prefixes() {
		if err := refname.CheckPrefix(p); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	}

	// The agent module's defaults apply before the global ones, so a config
	// naming an agent launches that agent unless its own command says
	// otherwise. An unknown name is a load error listing the modules there
	// are, like an unknown branch prefix: the key's whole value is the module
	// it names, and a misspelling that silently fell back to the global
	// defaults would leave the carry — the part with no other spelling —
	// quietly not happening.
	if c.Agent != "" {
		module, ok := agentinit.Lookup(c.Agent)
		if !ok {
			return nil, fmt.Errorf("%s: unknown agent %q (built-in modules: %s)",
				filepath.Base(path), c.Agent, strings.Join(agentinit.Names(), ", "))
		}
		if c.Command == "" {
			c.Command = module.Command
		}
		if c.ResumeCommand == "" {
			c.ResumeCommand = module.ResumeCommand
		}
	}

	if c.BaseBranch == "" {
		c.BaseBranch = DefaultBaseBranch
	}
	// Defaulted off the value rather than through Explicit, so an explicit
	// command = "" is collapsed into the default — the collapse ticket_pattern
	// below avoids. tmux.Spec would honor a blank command by leaving a shell,
	// but there is no way to ask for that from a config: writing "" gets you the
	// agent. That stays this way on purpose for now — "" in a command key is far
	// more often a half-deleted line than a request for a bare shell, and
	// honoring it would turn the typo into a window that silently runs nothing.
	// A config that wants a shell is the use case that would flip these, and the
	// agent-module overrides above, to Explicit.
	if c.Command == "" {
		c.Command = DefaultCommand
	}
	if c.ResumeCommand == "" {
		c.ResumeCommand = DefaultResumeCommand
	}
	// Read through explicit rather than off the value, because here an empty
	// string is a decision and not an omission: a repository whose work is not
	// tracked by ticket writes ticket_pattern = "" and gets windows named after
	// the slug. Only a file that never mentions the key takes the default.
	if !c.Explicit("ticket_pattern") {
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
			// Only a missing file means "no config". Anything else — a
			// permissions problem, usually — is a file that is there and cannot
			// be read, and "no config" would send its owner to setup for a
			// registration they already have.
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("no config %q in %s (have: %s)", name, Dir(), strings.Join(names, ", "))
			}
			return nil, fmt.Errorf("config %q: %w", name, err)
		}
		return Load(path)
	}

	if repoMainDir != "" {
		want := canonical(repoMainDir)
		var failed []error
		for _, n := range names {
			c, err := Load(filepath.Join(Dir(), n+".toml"))
			if err != nil {
				// A broken config elsewhere in the registry must not block work
				// on a repo whose own config is fine, so it is skipped while
				// matching — but the error is kept, in case nothing matches.
				failed = append(failed, err)
				continue
			}
			if canonical(c.MainDir) == want {
				return c, nil
			}
		}
		// Nothing matched and something would not load: that is nearly always the
		// reason, and nearly always the config for this very repo, edited a moment
		// ago. Reporting only "no config matches" would send the user looking for a
		// file that is sitting right there with a typo in it. The unreadable config
		// leads, the failure to match being the consequence rather than the fault.
		if len(failed) > 1 {
			return nil, fmt.Errorf("%w (and no other config matches repo %s; %d more could not be read either)",
				failed[0], repoMainDir, len(failed)-1)
		}
		if len(failed) == 1 {
			return nil, fmt.Errorf("%w (and no other config matches repo %s)", failed[0], repoMainDir)
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

// AgentCarries returns the agent module's local-state files that carry_files
// does not already list — for claude, the plugin holding the hooks that report
// state and the skill that teaches the agent to drive treewright, plus
// .claude/settings.local.json, which holds the "always allow" permission
// decisions the agent records as it works. Carried into every new worktree so
// all of it travels with the checkout.
//
// These differ from carry_files entries in exactly one way: absent from the
// main checkout they are skipped silently rather than warned about. An
// explicit entry warns when missing because the user asserted the file exists
// and a missing one is a stale config; these were asserted by nobody, and a
// checkout that has never run the agent has nothing to carry yet.
//
// The dedupe is what makes listing the same path in carry_files harmless —
// copied once, under the explicit entry's warn-when-missing semantics, since
// writing it out is exactly such an assertion.
func (c *Config) AgentCarries() []string {
	if c.Agent == "" {
		return nil
	}
	module, ok := agentinit.Lookup(c.Agent)
	if !ok {
		return nil // Load refused the config; nothing can hold one like this
	}
	var out []string
	for _, rel := range module.LocalState() {
		if !slices.Contains(c.CarryFiles, rel) {
			out = append(out, rel)
		}
	}
	return out
}

// WindowName derives the tmux window name for a slug, uppercased.
//
// Precedence: an explicit override wins; else a ticket key matched by
// TicketPattern (so "proj-142-white-screen" becomes "PROJ-142"); else the slug,
// shortened by shorten to keep the tmux status line readable.
//
// An empty TicketPattern is the opt-out, and reaches here only from a config
// that wrote ticket_pattern = "" itself: no ticket scheme in this repository, so
// nothing is looked for and the slug always names the window.
func (c *Config) WindowName(slug, override string) string {
	if override != "" {
		return strings.ToUpper(override)
	}
	if c.TicketPattern != "" {
		// Compiled here rather than cached because this runs at most once per
		// command; Load has already proven the pattern compiles.
		if re, err := regexp.Compile(c.TicketPattern); err == nil {
			if m := re.FindStringSubmatch(slug); len(m) > 1 && m[1] != "" {
				return strings.ToUpper(m[1])
			}
		}
	}
	return strings.ToUpper(shorten(slug))
}

// maxWindowName caps a window named after its slug, in columns of the tmux
// status line those names share.
//
// Ten is a ticket key's width, and a slug-named window is held to the same one on
// purpose: the status line is the same width whichever a repository uses, and a
// name that fits is worth more than a name that is whole. A description is cut
// mid-word by that, which is a real cost and the deliberate one.
//
// Cutting at a word boundary instead was the alternative, and it loses at this
// width. It has to give back a whole word to find the boundary, so
// "flaky-payment-test" arrives as FLAKY… rather than FLAKY-PAYM…, and — because
// cutting further escapes the guard below — "rewrite-css" arrives as REWRITE…
// where the blunt cut hands it back whole. It only pays at a cap wide enough that
// the nearest boundary is usually near it.
const maxWindowName = 10

// ellipsis marks a name something was dropped from. The character rather than
// three periods, because it is one column where "..." is three and the whole
// point of shortening is columns in a status line — and because ui.Table already
// measures in runes, so the table it lands in stays aligned either way.
const ellipsis = "…"

// shorten trims a slug to maxWindowName columns plus the ellipsis.
//
// Counted in runes rather than bytes: the cap is a number of columns, and a slug
// may hold anything git will take in a ref — refname forbids control characters
// and git's own metacharacters, not the rest of Unicode. Cutting by byte would
// both misjudge the width and split a multi-byte character down the middle.
//
// The result is used only when it is narrower than what it replaces, which is
// what keeps an eleven-character slug whole: "rewrite-css" cut to ten and marked
// is eleven columns again, one character spent to say a character is missing.
func shorten(slug string) string {
	r := []rune(slug)
	if len(r) <= maxWindowName {
		return slug
	}
	// A cut landing just after a hyphen would leave one against the ellipsis,
	// where it reads as punctuation rather than as part of the name —
	// "dark-mode-toggle" as DARK-MODE-… rather than DARK-MODE…. A slug may not
	// begin with a hyphen, so this can never trim everything away.
	kept := r[:maxWindowName]
	for len(kept) > 0 && kept[len(kept)-1] == '-' {
		kept = kept[:len(kept)-1]
	}
	// The ellipsis is one rune wide, which is the whole reason it is that
	// character and not three periods.
	if len(kept)+1 >= len(r) {
		return slug
	}
	return string(kept) + ellipsis
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
