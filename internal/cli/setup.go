package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jay-snyder/treewright/internal/agentinit"
	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/ui"
)

// ---- setup -----------------------------------------------------------------

// cmdSetup registers the repository the caller is standing in.
//
// Everything treewright needs beyond main_dir has a default or can be guessed from
// the repository itself, so the fastest path from "installed" to "working" is a
// generated file. What it writes is ordinary TOML with the guesses commented, on
// the principle that the file remains the record: setup is a way to start one,
// not a layer above it.
func cmdSetup(env *Env, args []string) error {
	var dryRun, refresh bool
	positional, err := parseArgs("setup", args, map[string]*bool{
		"-n": &dryRun, "--dry-run": &dryRun,
		"--refresh": &refresh,
	}, nil, 1)
	if err != nil {
		return err
	}
	if refresh {
		return refreshConfig(env, at(positional, 0), dryRun)
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
		return fmt.Errorf("%s already exists\nedit it, or pass a different name", path)
	}
	// A second config for the same repository would make which one applies depend
	// on registry order, so say so rather than creating the ambiguity.
	if existing, err := config.Resolve("", mainDir); err == nil && existing.Name != name {
		return fmt.Errorf("%s is already registered as %q%s", mainDir, existing.Name,
			asFields(field("its config", existing.Path())))
	}

	baseBranch := repo.DefaultBranch()
	carry := carryCandidates(repo.IgnoredFiles())
	agent := detectAgent()

	// What the repository's own branches say beats what this user's email says: a
	// team that namespaces by kind of work has already decided, and a personal
	// prefix would put new branches outside the scheme. The email is the fallback,
	// which is what an origin with no recognizable scheme leaves in place.
	detected := detectedPrefixes(repo)
	prefixes := prefixNames(detected)
	if len(prefixes) == 0 {
		prefixes = []string{branchPrefixFor(repo.UserEmail())}
	}

	// One labelled block rather than five sentences in a row. Every line here
	// answers the same question — what did setup work out, and from what — so
	// what a reader wants is to run an eye down the values and stop at the one
	// that looks wrong. As sentences, each began with a different word and the
	// value was wherever the sentence happened to reach.
	//
	// Each value says where it came from, in the same parenthesis, because every
	// one of these is a guess the user is being invited to correct.
	fields := []([2]string){
		field("main checkout", mainDir),
		field("base branch", baseBranch+"  (from origin/HEAD)"),
	}
	fields = append(fields, prefixFields(detected, prefixes)...)
	fields = append(fields,
		field("carry files", describeCarry(carry)),
		field("agent", describeAgent(agent)),
	)
	env.progressf("detected:%s", asFields(fields...))

	body := renderConfig(configSettings{
		name:             name,
		mainDir:          mainDir,
		baseBranch:       baseBranch,
		prefixes:         prefixes,
		prefixFromOrigin: len(detected) > 0,
		carry:            carry,
		agent:            agent,
	})
	if dryRun {
		fmt.Fprint(env.Stdout, body)
		env.progressf("nothing was written%s", asFields(
			field("would write", path),
			field("to do it", env.copyable(env.Argv0+" setup")),
		))
		return nil
	}

	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, path)
	// Two commands to type, so two labelled lines: run together after the em
	// dash they were one clause holding both, and the second — the one that is
	// the whole point of having registered anything — came last on the longest
	// line setup prints.
	env.progressf("registered %s as %q%s", filepath.Base(mainDir), name, asFields(
		field("config", path),
		field("check it with", env.copyable(env.Argv0+" doctor")),
		field("start work with", env.copyable(env.Argv0+" new <slug>")),
	))
	return nil
}

// refreshConfig regenerates an existing config in place, keeping every setting
// the user chose and rewriting everything around it.
//
// It exists because `setup` refuses an existing config outright — rightly, since
// overwriting one would discard edits — and the consequence was that every later
// improvement to the generated file reached new repositories only. A config
// written two releases ago names none of the keys added since, explains none of
// what the commentary now explains, and there was no command that changed that
// short of deleting the file and answering the detection again.
//
// **Nothing is re-detected.** The values are read back out of the file that is
// there, which is the whole difference between this and running `setup` again: a
// base branch someone corrected by hand, a prefix they chose over the guess, a
// command that is not the agent's default — those are decisions, and a refresh
// that re-derived them from the repository would quietly undo the ones that
// disagree with what treewright would guess today.
//
// What moves is the version, the comments, and any key the generator has since
// learned to write.
func refreshConfig(env *Env, name string, dryRun bool) error {
	// Resolved rather than derived from the directory's name, because that is
	// what --refresh is refreshing: the config in force here, whatever it is
	// called. `setup`'s own default — the checkout's base name — is a name for a
	// file that does not exist yet, and pointing it at one that does would target
	// whichever config happened to share the directory's spelling.
	cfg, err := resolveConfig(name)
	if err != nil {
		return err
	}

	body := renderConfig(settingsFrom(cfg))
	if dryRun {
		fmt.Fprint(env.Stdout, body)
		env.progressf("nothing was written%s", asFields(
			field("would rewrite", cfg.Path()),
			field("to do it", env.copyable(env.Argv0+" setup --refresh "+cfg.Name)),
		))
		return nil
	}
	if err := os.WriteFile(cfg.Path(), []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, cfg.Path())
	env.progressf("rewrote %q for config version %d%s", cfg.Name, config.FormatVersion, asFields(
		field("config", cfg.Path()),
		field("kept", "every setting the file already had"),
		field("check it with", env.copyable(env.Argv0+" config "+cfg.Name)),
	))
	return nil
}

// settingsFrom reads a loaded config back into the shape the generator writes.
//
// Explicit rather than the value throughout, because Load has already filled the
// blanks: cfg.Command holds "claude {prompt}" whether the file said so or the
// default did, and writing that back would turn a default into a setting — the
// one thing a regenerated file must not do, since a default is what follows
// treewright's own changes and a setting is what refuses to.
func settingsFrom(cfg *config.Config) configSettings {
	s := configSettings{
		name:       cfg.Name,
		mainDir:    cfg.MainDir,
		baseBranch: cfg.BaseBranch,
		prefixes:   cfg.Prefixes(),
		carry:      cfg.CarryFiles,
		agent:      cfg.Agent,
	}
	// A refreshed file is written under whichever spelling it already used, so a
	// repository that lists several prefixes keeps the list and one that names a
	// single prefix keeps the singular key. Prefixes() returns one element for
	// the singular spelling, which is exactly the shape renderConfig expects.
	s.pluralPrefixes = cfg.Explicit("branch_prefixes")
	if cfg.Explicit("command") {
		s.command = cfg.Command
	}
	if cfg.Explicit("resume_command") {
		s.resumeCommand = cfg.ResumeCommand
	}
	if cfg.Explicit("post_create") {
		s.postCreate = cfg.PostCreate
	}
	// The one setting whose empty value is a decision: ticket_pattern = "" is how
	// a repository that tracks no tickets turns the search off, so what is kept
	// is whether the key was written, not whether it holds anything.
	s.ticketPatternSet = cfg.Explicit("ticket_pattern")
	s.ticketPattern = cfg.TicketPattern
	if cfg.Explicit("tmux_session") {
		s.tmuxSession = cfg.TmuxSession
	}
	return s
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

// workKinds are the branch namespaces that name a kind of work rather than a
// person, in the order that settles ties: ordinary work first, then fixes, then
// upkeep. The first entry of a detected list is what a bare slug gets, so where
// two namespaces are used equally often, this is what picks the default.
//
// A vocabulary rather than a frequency heuristic, because the two schemes are
// otherwise the same shape: "alice/x, alice/y, bob/z" and "feature/x, feature/y,
// bug/z" are indistinguishable by counting, and proposing the first as a list of
// prefixes would write colleagues' names into your config as though they were
// kinds of work. That failure is much worse than not guessing — an unrecognized
// scheme just leaves the git-email guess and the commented example, which is
// where this started.
var workKinds = []string{
	"feature", "feat", "features", "story", "task", "epic",
	"bug", "bugs", "bugfix", "fix", "fixes", "hotfix", "patch",
	"chore", "chores", "refactor", "perf", "docs", "doc",
	"test", "tests", "ci", "build", "deps", "style",
	"release", "revert", "spike", "exp", "experiment", "wip",
	"security", "support",
}

// branchPrefix is one namespace origin's branches use, and how many use it.
type branchPrefix struct {
	name  string
	count int
}

// detectedPrefixes reads the branch prefixes a repository's own origin suggests,
// most used first.
//
// A namespace has to appear on two branches to count: one is an incident, two is
// a convention. Ordering by use means the prefix a bare slug gets is the one most
// work already goes into, which is the guess most likely to be right and the
// easiest to check against the list printed beside it.
func detectedPrefixes(repo git.Repo) []branchPrefix {
	counts := repo.RemoteBranchNamespaces("origin")
	if len(counts) == 0 {
		return nil
	}
	rank := make(map[string]int, len(workKinds))
	for i, kind := range workKinds {
		rank[kind] = i
	}

	type candidate struct {
		branchPrefix

		rank int
	}
	found := make([]candidate, 0, len(counts))
	for ns, n := range counts {
		r, known := rank[strings.ToLower(strings.TrimSuffix(ns, "/"))]
		if !known || n < 2 {
			continue
		}
		found = append(found, candidate{branchPrefix{ns, n}, r})
	}
	// Sorted rather than left in map order, which Go randomizes: the same repo has
	// to produce the same config twice, and the first entry is a setting.
	sort.Slice(found, func(i, j int) bool {
		if found[i].count != found[j].count {
			return found[i].count > found[j].count
		}
		return found[i].rank < found[j].rank
	})
	// Long enough for a real scheme, short enough that the list stays something a
	// reader checks rather than skims past.
	if len(found) > 6 {
		found = found[:6]
	}

	prefixes := make([]branchPrefix, 0, len(found))
	for _, c := range found {
		prefixes = append(prefixes, c.branchPrefix)
	}
	return prefixes
}

// prefixNames drops the counts, which are for the report rather than the file.
func prefixNames(prefixes []branchPrefix) []string {
	names := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		names = append(names, p.name)
	}
	return names
}

// branchPrefixFor derives a branch namespace from a git email, so that branches
// are attributable on a shared remote: "john.doe@example.com" gives "john/".
//
// The first dotted or plussed component is used rather than the whole local part,
// because "john.doe/eng-1" reads as a path with a surname in it while "john/" is
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

// carryCandidates picks, out of everything git is ignoring, the files the app
// is most likely to need in a new worktree.
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

// detectAgent names the first agent module whose command is on PATH, most
// often claude or nothing. A found binary is as close to consent as detection
// gets: the user installed the agent, and the key only bundles the defaults
// they would otherwise write out — still one commented line to remove.
func detectAgent() string {
	for _, name := range agentinit.Names() {
		module, _ := agentinit.Lookup(name)
		words := strings.Fields(module.Command)
		if len(words) == 0 {
			continue
		}
		if _, err := exec.LookPath(words[0]); err == nil {
			return name
		}
	}
	return ""
}

// configSettings is everything a generated config carries, and the reason it is
// a struct rather than a parameter list is `setup --refresh`.
//
// `setup` fills it from what it detected; `--refresh` fills it from the file
// that is already there. Both then go through one renderer, which is what keeps
// a regenerated config identical in shape to a fresh one — the alternative, a
// second writer for the refresh path, would have the two drifting apart at the
// first key added to either.
//
// The lower half is the keys `setup` never guesses and so only ever commented
// out. They are here because `--refresh` must not lose them: a config saying
// command = "nvim" rewritten from a struct that could not hold a command would
// come back launching claude, which is not a formatting change.
type configSettings struct {
	name       string
	mainDir    string
	baseBranch string

	// prefixes is the list in force, most preferred first — always at least one
	// element, as config.Prefixes guarantees. pluralPrefixes says which of the
	// setting's two spellings to write it under, and prefixFromOrigin whether a
	// single one is evidence or a guess, since only the guess says so.
	prefixes         []string
	pluralPrefixes   bool
	prefixFromOrigin bool

	carry []string
	agent string

	// Written only when set, commented as the default otherwise. Empty means the
	// file did not say — except for ticketPattern, where empty is a setting and
	// ticketPatternSet is what distinguishes the two.
	command          string
	resumeCommand    string
	postCreate       []string
	ticketPattern    string
	ticketPatternSet bool
	tmuxSession      string
}

// renderConfig writes the config file's text. prefixFromOrigin says which of
// the two things the branch prefix is, since only one of them is evidence.
//
// It is commented the way a config kept under review would be, because this file
// is where every later question about treewright's behavior gets answered, and
// because the values it holds are guesses that deserve to be reviewed rather than
// inherited silently.
//
// Which is why the header no longer calls the lot of it "detected". Most of it
// is: main_dir is where git says the checkout is, base_branch comes off
// origin/HEAD, carry_files off the ignore rules, agent off PATH. The branch
// prefix is the one line that can be either — read off origin's own branches,
// which is a convention observed, or derived from a git email, which is a guess
// about a person made from an address that need not be about a person at all. A
// forge-specific identity like codeberg@example.org yields "codeberg/", written
// with exactly the confidence of a value nothing guessed, and a reader with no
// reason to doubt the file leaves it there. So the guess says it is one, in the
// place a reader is already looking.
func renderConfig(s configSettings) string {
	prefixes := s.prefixes
	var b strings.Builder

	fmt.Fprintf(&b, "# treewright config for %s, generated by \"treewright setup\".\n", s.name)
	fmt.Fprintf(&b, "# Most of this was read off the repository itself; anything called a guess\n")
	fmt.Fprintf(&b, "# below was not. Worth a read either way — edit anything that is wrong.\n\n")

	// The version leads, above the settings, because it is about the file rather
	// than about the repository — and because a reader who has just been told by
	// doctor that this config predates the generator should find it on the first
	// line they look at rather than at the bottom of a page of commentary.
	fmt.Fprintf(&b, "# Which revision of this file's layout treewright wrote. \"treewright doctor\"\n")
	fmt.Fprintf(&b, "# reads it to say when a config predates the treewright you are running;\n")
	fmt.Fprintf(&b, "# \"treewright setup --refresh\" rewrites the file around your own settings.\n")
	fmt.Fprintf(&b, "version = %d\n\n", config.FormatVersion)

	fmt.Fprintf(&b, "# The repository's main checkout. Worktrees are created as its siblings,\n")
	fmt.Fprintf(&b, "# named <main_dir>-<slug>.\n")
	fmt.Fprintf(&b, "main_dir = %s\n\n", tomlString(abbreviateHome(s.mainDir)))

	fmt.Fprintf(&b, "# New branches fork from origin/%s, and every status is measured against it.\n", s.baseBranch)
	fmt.Fprintf(&b, "base_branch = %s\n\n", tomlString(s.baseBranch))

	switch {
	case s.pluralPrefixes || len(prefixes) > 1:
		// The example names the second prefix, since the first is what a bare slug
		// already gets — but a list may legitimately hold one entry, and then the
		// only prefix there is has to serve as its own example.
		example := prefixes[0]
		if len(prefixes) > 1 {
			example = prefixes[1]
		}
		fmt.Fprintf(&b, "# Branch names are <prefix><slug>, and these are the prefixes this repository\n")
		fmt.Fprintf(&b, "# uses, most preferred first. Pick one by naming it — \"treewright new\n")
		fmt.Fprintf(&b, "# %seng-1\" branches %seng-1 — or leave it off and get %s.\n",
			example, example, prefixes[0])
		fmt.Fprintf(&b, "branch_prefixes = [%s]\n\n", tomlList(prefixes))
	case len(prefixes) == 0 || prefixes[0] == "":
		fmt.Fprintf(&b, "# Prepended to a slug to form the branch name, e.g. \"alice/\" gives\n")
		fmt.Fprintf(&b, "# alice/eng-1. Left empty: git has no user.email configured for this repo.\n")
		fmt.Fprintf(&b, "# branch_prefix = \"alice/\"\n")
		writePrefixesHint(&b)
	case s.prefixFromOrigin:
		fmt.Fprintf(&b, "# Prepended to a slug to form the branch name: %seng-1. It is what\n", prefixes[0])
		fmt.Fprintf(&b, "# origin's own branches already use.\n")
		fmt.Fprintf(&b, "branch_prefix = %s\n", tomlString(prefixes[0]))
		writePrefixesHint(&b)
	default:
		fmt.Fprintf(&b, "# Prepended to a slug to form the branch name: %seng-1. A guess, from\n", prefixes[0])
		fmt.Fprintf(&b, "# your git email rather than from anything this repository does — the point\n")
		fmt.Fprintf(&b, "# being that branches stay attributable on a shared remote. An address that\n")
		fmt.Fprintf(&b, "# names a forge rather than a person makes it read as nonsense; change it,\n")
		fmt.Fprintf(&b, "# or comment the line out for no prefix at all.\n")
		fmt.Fprintf(&b, "branch_prefix = %s\n", tomlString(prefixes[0]))
		writePrefixesHint(&b)
	}

	fmt.Fprintf(&b, "# Files a new worktree starts without and the app needs anyway, like your\n")
	fmt.Fprintf(&b, "# .env, copied in from main_dir. Usually files git ignores, though being\n")
	fmt.Fprintf(&b, "# ignored is not what puts a file here — the agent key below carries its\n")
	fmt.Fprintf(&b, "# own, ignored or not. Paths are relative to main_dir.\n")
	if len(s.carry) == 0 {
		fmt.Fprintf(&b, "# Nothing was detected; add what your app needs, e.g.:\n")
		fmt.Fprintf(&b, "# carry_files = [\".env.local\", \"apps/api/.env\"]\n\n")
	} else {
		fmt.Fprintf(&b, "carry_files = [\n")
		for _, rel := range s.carry {
			fmt.Fprintf(&b, "  %s,\n", tomlString(rel))
		}
		fmt.Fprintf(&b, "]\n\n")
	}

	fmt.Fprintf(&b, "# The coding agent these windows run. Supplies command and resume_command,\n")
	fmt.Fprintf(&b, "# and copies the agent's own per-project files — its settings, and the\n")
	fmt.Fprintf(&b, "# plugin holding treewright's hooks and skill — into each new worktree.\n")
	fmt.Fprintf(&b, "# Nothing ignores that plugin until you say so, so it reads as untracked\n")
	fmt.Fprintf(&b, "# everywhere it lands — \"treewright doctor\" says so too. Remove this key\n")
	fmt.Fprintf(&b, "# for a window with no agent in it.\n")
	if s.agent == "" {
		fmt.Fprintf(&b, "# agent = \"claude\"\n\n")
	} else {
		fmt.Fprintf(&b, "agent = %s\n\n", tomlString(s.agent))
	}

	// From here down are the keys setup never guesses, so a fresh config shows
	// each as the commented default it is running on. What makes them worth
	// writing out at all is --refresh: a file that set one has it written back,
	// under the same commentary, rather than losing it to a regeneration.
	fmt.Fprintf(&b, "# What to launch in the tmux window. command is used by new and base,\n")
	fmt.Fprintf(&b, "# resume_command by resume; the two default independently, and either\n")
	fmt.Fprintf(&b, "# overrides what agent supplies. {prompt} is where --prompt's text lands,\n")
	fmt.Fprintf(&b, "# shell-quoted; without a prompt the placeholder disappears.\n")
	writeSetting(&b, "command", commandKeyWidth, s.command, config.DefaultCommand)
	writeSetting(&b, "resume_command", commandKeyWidth, s.resumeCommand, config.DefaultResumeCommand)
	fmt.Fprintln(&b)

	fmt.Fprintf(&b, "# Run in the background in each new worktree, for dependency installation.\n")
	fmt.Fprintf(&b, "# Either one command, or a list of them run in order, stopping at the first\n")
	fmt.Fprintf(&b, "# failure.\n")
	switch {
	case len(s.postCreate) == 1:
		// Written back in the shape it was set in: one command as the string it
		// is, several as a list. A single command rewritten as a one-element list
		// would mean the same thing and would still be an edit nobody made.
		fmt.Fprintf(&b, "post_create = %s\n", tomlString(s.postCreate[0]))
		fmt.Fprintf(&b, "# post_create = [\"npm install\", \"npm run codegen\"]\n\n")
	case len(s.postCreate) > 1:
		fmt.Fprintf(&b, "post_create = [\n")
		for _, c := range s.postCreate {
			fmt.Fprintf(&b, "  %s,\n", tomlString(c))
		}
		fmt.Fprintf(&b, "]\n\n")
	default:
		fmt.Fprintf(&b, "# post_create = \"npm install\"\n")
		fmt.Fprintf(&b, "# post_create = [\"npm install\", \"npm run codegen\"]\n\n")
	}

	fmt.Fprintf(&b, "# Regexp whose first submatch names the tmux window, so a slug like\n")
	fmt.Fprintf(&b, "# eng-142-white-screen opens a window called ENG-142. Pin it to your own\n")
	fmt.Fprintf(&b, "# ticket scheme to stop it matching any letters-dash-digits word, or set\n")
	fmt.Fprintf(&b, "# it to \"\" if this repository's work has no ticket behind it — then the\n")
	fmt.Fprintf(&b, "# slug always names the window, cut to ten characters if it is longer.\n")
	if s.ticketPatternSet {
		// Written on whether the key was there rather than on whether it holds
		// anything: ticket_pattern = "" is the opt-out, and a refresh that dropped
		// it for looking empty would turn every window name back into a ticket
		// hunt in the one repository that said not to.
		fmt.Fprintf(&b, "ticket_pattern = %s\n\n", tomlString(s.ticketPattern))
	} else {
		fmt.Fprintf(&b, "# ticket_pattern = %s\n", tomlString(config.DefaultTicketPattern))
		fmt.Fprintf(&b, "# ticket_pattern = \"\"\n\n")
	}

	fmt.Fprintf(&b, "# The tmux session holding this repository's windows, so that they stay\n")
	fmt.Fprintf(&b, "# separate from every other repository's. Defaults to %q.\n", s.name)
	writeSetting(&b, "tmux_session", 0, s.tmuxSession, s.name)

	return b.String()
}

// commandKeyWidth lines the two command keys up with each other where both are
// shown as commented defaults, which is every config setup writes itself.
const commandKeyWidth = len("resume_command")

// writeSetting writes one key: as a live line when the config set it, and as the
// commented default otherwise.
//
// Only the commented form is padded. Aligning the live one too would put a run
// of spaces before the "=" of a value somebody actually set, to line it up with
// a comment — and a file where every key is set would be padded to a width
// nothing in it needs.
func writeSetting(b *strings.Builder, key string, width int, value, fallback string) {
	if value == "" {
		fmt.Fprintf(b, "# %-*s = %s\n", width, key, tomlString(fallback))
		return
	}
	fmt.Fprintf(b, "%s = %s\n", key, tomlString(value))
}

// writePrefixesHint mentions the list form in a config that got a single prefix,
// because a team convention treewright could not read off origin is exactly the
// thing the user has to write in themselves — and would not know it could.
func writePrefixesHint(b *strings.Builder) {
	fmt.Fprintf(b, "# Instead of one prefix, a repo can list several and pick between them by\n")
	fmt.Fprintf(b, "# naming one: \"treewright new bug/eng-1\" branches bug/eng-1. A bare slug\n")
	fmt.Fprintf(b, "# gets the first. Set this or branch_prefix, not both.\n")
	fmt.Fprintf(b, "# branch_prefixes = [\"feature/\", \"bug/\", \"chore/\"]\n\n")
}

// tomlString quotes a value as a TOML basic string. Go's escaping rules coincide
// with TOML's for everything that can appear in a path or a branch name.
func tomlString(s string) string { return strconv.Quote(s) }

// tomlList renders values as a TOML inline array's contents. Inline rather than
// one per line, as carry_files is: prefixes are short, and the order is the
// setting — a list on one line is one glance to check.
func tomlList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, tomlString(v))
	}
	return strings.Join(quoted, ", ")
}

// prefixFields are setup's rows about the branch prefix: what a branch will be
// called, and what that was read off.
//
// Several detected prefixes go one per line with the branch counts that ordered
// them, since the order is itself a setting — the first is what a bare slug
// gets — and a comma-joined run of six is the one shape that makes an order
// hard to check. Which one is the default then needs a row of its own: as a
// last line of the same value it sat at the same indent as the prefixes above
// it and read as a fourth prefix.
//
// The names are padded so the counts line up, because the counts are what the
// order is being justified by and a ragged column of them is not a column.
//
// The parenthesis is where the two kinds of value part. A count of branches on
// origin is evidence; a git email is a guess about a person, made from an
// address that need not name one, so it says the word — the same distinction
// the generated file's header draws, in the same terms.
func prefixFields(detected []branchPrefix, prefixes []string) [][2]string {
	switch {
	case len(detected) > 1:
		width := 0
		for _, p := range detected {
			width = max(width, len(p.name))
		}
		lines := make([]string, 0, len(detected))
		for _, p := range detected {
			lines = append(lines, fmt.Sprintf("%-*s  (%s on origin)", width, p.name,
				count(p.count, "branch", "branches")))
		}
		return [][2]string{
			field("branch prefixes", strings.Join(lines, "\n")),
			field("bare slug gets", detected[0].name),
		}
	case len(detected) == 1:
		return [][2]string{field("branch prefix", fmt.Sprintf("%s  (%s on origin use it)",
			detected[0].name, count(detected[0].count, "branch", "branches")))}
	case prefixes[0] == "":
		return [][2]string{field("branch prefix", "none  (git has no user.email configured here)")}
	default:
		return [][2]string{field("branch prefix", fmt.Sprintf("%s  (guessed from your git email — branches will be %seng-1)",
			prefixes[0], prefixes[0]))}
	}
}

// describeCarry is the value of setup's carry files field, one file per line.
func describeCarry(carry []string) string {
	if len(carry) == 0 {
		return "none  (no gitignored env files found)"
	}
	return strings.Join(carry, "\n")
}

// describeAgent is the value of setup's agent field. An absent one is still a
// row, because the field being empty is what says the key exists to be set.
//
// "settings and wiring" rather than "settings": what the key carries is the
// agent's own per-project files and treewright's plugin beside them, and only
// the first of those is a file git ignores by convention.
func describeAgent(agent string) string {
	if agent == "" {
		return "none  (none found on PATH)"
	}
	return agent + "  (found on PATH — its settings and wiring ride into each worktree)"
}

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

	// Three columns, because where a value came from is a third fact about it
	// and not a suffix of it. Appended to the value, the markers ended wherever
	// each value happened to stop — a column of them that never lined up, in the
	// one command whose whole subject is which settings are the file's and which
	// treewright supplied.
	//
	// FROM sits before VALUE and not after it. After, it is the last column, so
	// every value gets padded to the width of the longest — which here is an
	// absolute path — and the markers end up fifty columns from the values they
	// mark. Before, it is bounded by the word "default", and the values stay
	// flush in the column that is allowed to be ragged because nothing follows
	// it.
	table := ui.Table{Headers: []string{"SETTING", "FROM", "VALUE"}}
	add := func(key, value, source string) {
		if value == "" {
			table.Add(ui.Text(key), ui.Colored(source, ui.Dim), ui.Colored("(none)", ui.Dim))
			return
		}
		table.Add(ui.Text(key), ui.Colored(source, ui.Dim), ui.Text(value))
	}

	// The empty source is the file's own value, which needs no marking: it is
	// the unremarkable case, and the surprise this command exists for is a
	// setting that was never set at all.
	const (
		fromFile    = ""
		fromDefault = "default"
		fromAgentIs = "agent"
	)
	addSetting := func(key, value string, explicit bool) {
		if explicit {
			add(key, value, fromFile)
			return
		}
		add(key, value, fromDefault)
	}

	// Values the agent module supplied are marked as coming from it rather
	// than as a default: the point of this command is closing the gap
	// between the file and the behavior it produces, and "agent" is the line
	// in the file these values actually trace to.
	fromAgent := func(key, value, explicitKey string) {
		switch {
		case cfg.Explicit(explicitKey):
			add(key, value, fromFile)
		case cfg.Agent != "":
			add(key, value, fromAgentIs)
		default:
			add(key, value, fromDefault)
		}
	}

	add("name", cfg.Name, fromFile)
	add("file", cfg.Path(), fromFile)
	addSetting("main_dir", cfg.MainDir, true)
	addSetting("base_branch", cfg.BaseBranch, cfg.Explicit("base_branch"))
	addPrefixes(addSetting, cfg)
	addSetting("agent", cfg.Agent, cfg.Explicit("agent"))
	carryReport(add, cfg)
	fromAgent("command", cfg.Command, "command")
	fromAgent("resume_command", cfg.ResumeCommand, "resume_command")
	// One per line rather than joined with an arrow: these run in sequence, and a
	// stack reads as a sequence where a single line reads as a set. The arrows
	// went with the joining — a column of steps is already in order.
	addSetting("post_create", strings.Join(cfg.PostCreate, "\n"), cfg.Explicit("post_create"))
	addSetting("ticket_pattern", cfg.TicketPattern, cfg.Explicit("ticket_pattern"))
	// The session name in force, not the raw setting: what a reader wants to know
	// is which session their windows land in, which is the config's name until
	// tmux_session says otherwise.
	addSetting("tmux_session", sessionFor(cfg), cfg.Explicit("tmux_session"))

	table.Render(env.Stdout, ui.ColorEnabled(env.Stdout))
	return nil
}

// carryReport adds the rows for every file a new worktree receives: the
// carry_files entries as written, and the agent module's local-state files
// marked as the agent key's doing — the carry with no other line in the file to
// trace to.
//
// One file per line, which the VALUE column renders as a cell spanning as many
// lines. Comma-joined they were a single 180-column row, since an agent module
// contributes four paths of its own and each is a path: the widest value in the
// table by a factor of five, in the one row whose whole content is a list.
//
// Two rows rather than one, when both kinds are present, because the FROM
// column is per row and the two halves of this list have different answers.
// That is also what stops a marker after the last file claiming a span the
// reader has to count backwards to find the start of.
func carryReport(add func(key, value, source string), cfg *config.Config) {
	agentCarries := cfg.AgentCarries()
	if len(cfg.CarryFiles) == 0 && len(agentCarries) == 0 {
		add("carry_files", "", "")
		return
	}
	if len(cfg.CarryFiles) > 0 {
		add("carry_files", strings.Join(cfg.CarryFiles, "\n"), "")
	}
	if len(agentCarries) > 0 {
		add("carry_files", strings.Join(agentCarries, "\n"), "agent")
	}
}

// addPrefixes reports the branch prefixes as one row, named after whichever of
// the setting's two spellings the file used — the point of this command being to
// explain a file, a row keyed to a name that is not in it would send the reader
// looking for the wrong line.
//
// A single prefix prints as the bare value it is, and an empty one as the dim
// "(none)" that every unset value gets. Several print quoted, because an empty
// prefix listed among namespaced ones is a legitimate setting and would otherwise
// be an invisible gap between two commas.
func addPrefixes(add func(key, value string, explicit bool), cfg *config.Config) {
	key, explicit := "branch_prefix", cfg.Explicit("branch_prefix")
	if cfg.Explicit("branch_prefixes") {
		key, explicit = "branch_prefixes", true
	}
	if prefixes := cfg.Prefixes(); len(prefixes) == 1 {
		add(key, prefixes[0], explicit)
		return
	}
	add(key, prefixList(cfg), explicit)
}
