package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/testenv"
)

// registry points config lookups at a temp directory and writes the given
// files into it. The map key is the config name, the value its TOML body.
func registry(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TREEWRIGHT_CONFIG_DIR", dir)
	for name, body := range files {
		path := filepath.Join(dir, name+".toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return dir
}

// ---- loading ---------------------------------------------------------------

func TestLoadAppliesDefaults(t *testing.T) {
	dir := registry(t, map[string]string{
		"minimal": `main_dir = "/tmp/repo"`,
	})

	c, err := Load(filepath.Join(dir, "minimal.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Name != "minimal" {
		t.Errorf("Name = %q, want %q", c.Name, "minimal")
	}
	if c.BaseBranch != DefaultBaseBranch {
		t.Errorf("BaseBranch = %q, want %q", c.BaseBranch, DefaultBaseBranch)
	}
	if c.Command != DefaultCommand {
		t.Errorf("Command = %q, want %q", c.Command, DefaultCommand)
	}
	if c.ResumeCommand != DefaultResumeCommand {
		t.Errorf("ResumeCommand = %q, want %q", c.ResumeCommand, DefaultResumeCommand)
	}
	if c.TicketPattern != DefaultTicketPattern {
		t.Errorf("TicketPattern = %q, want the default", c.TicketPattern)
	}
	// An empty branch prefix is a legitimate setting, not a missing one.
	if c.BranchPrefix != "" {
		t.Errorf("BranchPrefix = %q, want empty", c.BranchPrefix)
	}
}

func TestLoadReadsEveryField(t *testing.T) {
	dir := registry(t, map[string]string{
		"full": `
main_dir       = "/tmp/repo"
base_branch    = "staging"
branch_prefix  = "alice/"
carry_files    = ["apps/api/.env", "aws/config"]
command        = "codex"
resume_command = "codex resume"
post_create    = "npm install"
ticket_pattern = '(?i)^(proj-[0-9]+)'
tmux_session   = "work"
`,
	})

	c, err := Load(filepath.Join(dir, "full.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BaseBranch != "staging" || c.BranchPrefix != "alice/" || c.Command != "codex" {
		t.Errorf("scalar fields wrong: %+v", c)
	}
	if got := strings.Join(c.PostCreate, ","); got != "npm install" {
		t.Errorf("PostCreate = %q, want the one command the file names", got)
	}
	if c.TmuxSession != "work" {
		t.Errorf("TmuxSession = %q, want %q", c.TmuxSession, "work")
	}
	if c.ResumeCommand != "codex resume" {
		t.Errorf("ResumeCommand = %q, want %q", c.ResumeCommand, "codex resume")
	}
	if got := strings.Join(c.CarryFiles, ","); got != "apps/api/.env,aws/config" {
		t.Errorf("CarryFiles = %q", got)
	}
}

// TestLoadReadsBranchPrefixes covers the other spelling of the prefix setting,
// which TestLoadReadsEveryField cannot: a config may set one or the other.
func TestLoadReadsBranchPrefixes(t *testing.T) {
	dir := registry(t, map[string]string{
		"kinds": "main_dir = \"/tmp/repo\"\nbranch_prefixes = [\"feature/\", \"bug/\", \"chore/\"]\n",
	})

	c, err := Load(filepath.Join(dir, "kinds.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := strings.Join(c.Prefixes(), ","); got != "feature/,bug/,chore/" {
		t.Errorf("Prefixes = %q, want the file's order preserved", got)
	}
	// Order is the setting, not an accident of it: the first entry is what a bare
	// slug gets, so sorting the list would change which branch `new eng-1` creates.
	if !c.Explicit("branch_prefixes") || c.Explicit("branch_prefix") {
		t.Errorf("Explicit disagrees with the file: %+v", c)
	}
}

// TestLoadReadsPostCreateEitherWay covers the second shape of the setting, which
// TestLoadReadsEveryField cannot: a file writes one command or a list, not both.
func TestLoadReadsPostCreateEitherWay(t *testing.T) {
	dir := registry(t, map[string]string{
		"list":  "main_dir = \"/tmp/repo\"\npost_create = [\"npm install\", \"npm run build\"]\n",
		"blank": "main_dir = \"/tmp/repo\"\npost_create = \"\"\n",
	})

	c, err := Load(filepath.Join(dir, "list.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Order is the setting: the build runs against what install put there.
	if got := strings.Join(c.PostCreate, " | "); got != "npm install | npm run build" {
		t.Errorf("PostCreate = %q, want the file's order preserved", got)
	}

	// A config that once had a setup step and no longer does keeps loading, and
	// keeps meaning nothing runs.
	c, err = Load(filepath.Join(dir, "blank.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.PostCreate) != 0 {
		t.Errorf("PostCreate = %q, want nothing to run", c.PostCreate)
	}
	// Still a key the file set, so `config` reports it as chosen rather than defaulted.
	if !c.Explicit("post_create") {
		t.Error("Explicit(post_create) = false, want the key the file set to be seen")
	}
}

func TestLoadRejectsBadConfigs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			// A misspelled key that silently does nothing is the worst outcome
			// here: the setting appears to have no effect, with nothing to see.
			name:    "unknown key",
			body:    "main_dir = \"/tmp/repo\"\nbase-branch = \"staging\"\n",
			wantErr: "unknown setting",
		},
		{
			// The key's whole value is the module it names, and a misspelling
			// that fell back to the global defaults would leave the carry — the
			// part with no other spelling — quietly not happening.
			name:    "unknown agent",
			body:    "main_dir = \"/tmp/repo\"\nagent = \"claud\"\n",
			wantErr: "unknown agent",
		},
		{
			name:    "missing main_dir",
			body:    `base_branch = "main"`,
			wantErr: "main_dir is required",
		},
		{
			name:    "blank main_dir",
			body:    `main_dir = "   "`,
			wantErr: "main_dir is required",
		},
		{
			name:    "invalid ticket pattern",
			body:    "main_dir = \"/tmp/repo\"\nticket_pattern = \"([unclosed\"\n",
			wantErr: "not a valid regexp",
		},
		{
			// Two spellings of one setting. Silently preferring either would leave
			// the file no longer saying which prefix a branch actually gets.
			name:    "both branch prefix spellings",
			body:    "main_dir = \"/tmp/repo\"\nbranch_prefix = \"alice/\"\nbranch_prefixes = [\"feature/\"]\n",
			wantErr: "not both",
		},
		{
			name:    "empty branch_prefixes",
			body:    "main_dir = \"/tmp/repo\"\nbranch_prefixes = []\n",
			wantErr: "branch_prefixes is empty",
		},
		{
			name:    "duplicate branch prefix",
			body:    "main_dir = \"/tmp/repo\"\nbranch_prefixes = [\"feature/\", \"bug/\", \"feature/\"]\n",
			wantErr: `lists "feature/" twice`,
		},
		{
			// A prefix is hand-written, often several at a time, and git would
			// otherwise refuse the branch several steps into a `new` that has
			// already said what it was doing.
			name:    "unusable branch prefix",
			body:    "main_dir = \"/tmp/repo\"\nbranch_prefix = \"fea ture/\"\n",
			wantErr: "cannot contain whitespace",
		},
		{
			// One bad entry in a list has to name itself, not just the key.
			name:    "unusable prefix in a list",
			body:    "main_dir = \"/tmp/repo\"\nbranch_prefixes = [\"feature/\", \".hidden/\"]\n",
			wantErr: `".hidden/"`,
		},
		{
			name:    "branch prefix that reads as a flag",
			body:    "main_dir = \"/tmp/repo\"\nbranch_prefixes = [\"-x/\", \"bug/\"]\n",
			wantErr: "would read as a flag",
		},
		{
			// A list of commands is hand-written, and a stray value in one would
			// otherwise reach `sh` as whatever %v made of it.
			name:    "non-command in post_create",
			body:    "main_dir = \"/tmp/repo\"\npost_create = [\"npm install\", 7]\n",
			wantErr: "entry 2 is int64, not a command",
		},
		{
			// Unlike post_create = "", which is a setting turned off, a blank entry
			// among real ones is a half-finished edit.
			name:    "empty entry in post_create",
			body:    "main_dir = \"/tmp/repo\"\npost_create = [\"npm install\", \"  \"]\n",
			wantErr: "entry 2 is empty",
		},
		{
			name:    "post_create that is neither shape",
			body:    "main_dir = \"/tmp/repo\"\npost_create = true\n",
			wantErr: "want one command or a list of them",
		},
		{
			name:    "malformed toml",
			body:    "main_dir = = \"/tmp/repo\"",
			wantErr: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := registry(t, map[string]string{"c": tc.body})
			_, err := Load(filepath.Join(dir, "c.toml"))
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestAgentKeyIsADefaultsBundle pins the key's three effects and the two rules
// they follow: explicit keys win field by field, and the carry is deduped
// against carry_files rather than doubled.
func TestAgentKeyIsADefaultsBundle(t *testing.T) {
	dir := registry(t, map[string]string{
		"bare":     "main_dir = \"/tmp/repo\"\nagent = \"claude\"\n",
		"override": "main_dir = \"/tmp/repo\"\nagent = \"claude\"\ncommand = \"nvim\"\n",
		"listed":   "main_dir = \"/tmp/repo\"\nagent = \"claude\"\ncarry_files = [\".claude/settings.local.json\", \".env\"]\n",
		"none":     "main_dir = \"/tmp/repo\"\n",
	})
	load := func(name string) *Config {
		t.Helper()
		c, err := Load(filepath.Join(dir, name+".toml"))
		if err != nil {
			t.Fatalf("Load(%s): %v", name, err)
		}
		return c
	}

	// Against the config constants, so the module and the global defaults are
	// held to agreement: setting the key must not change which command runs.
	bare := load("bare")
	if bare.Command != DefaultCommand || bare.ResumeCommand != DefaultResumeCommand {
		t.Errorf("module defaults = %q / %q, want the global defaults %q / %q",
			bare.Command, bare.ResumeCommand, DefaultCommand, DefaultResumeCommand)
	}
	// Every per-project artifact the module has, not just its settings: the
	// plugin placed in the main checkout and not carried would reach the MAIN
	// window and no worktree, which is the trap the carry closes. A tree is
	// carried file by file, since a directory is not what carry_files copies —
	// so all three of the plugin's files are named here, and a worktree cannot
	// arrive with the skill and no hooks.
	want := []string{
		".claude/settings.local.json",
		".claude/skills/treewright/SKILL.md",
		".claude/skills/treewright/.claude-plugin/plugin.json",
		".claude/skills/treewright/hooks/hooks.json",
	}
	if got := bare.AgentCarries(); !slices.Equal(got, want) {
		t.Errorf("AgentCarries() = %v, want %v", got, want)
	}

	// agent-plus-command is override, not a load error: the file still says
	// which command runs, because the command key is sitting right there in it.
	override := load("override")
	if override.Command != "nvim" {
		t.Errorf("explicit command = %q, want it to beat the module's", override.Command)
	}
	if override.ResumeCommand != DefaultResumeCommand {
		t.Errorf("resume_command = %q, want the module's default for the field left unset", override.ResumeCommand)
	}

	// Writing the same path in carry_files changes nothing but the semantics —
	// the explicit entry warns when missing, so it must own the copy. The
	// module's other artifacts are unaffected and still carried implicitly.
	if got := load("listed").AgentCarries(); slices.Contains(got, ".claude/settings.local.json") {
		t.Errorf("AgentCarries() = %v, want the explicitly listed entry deduped out", got)
	}

	if got := load("none").AgentCarries(); len(got) != 0 {
		t.Errorf("AgentCarries() with no agent = %v, want nothing", got)
	}
}

func TestLoadExpandsHomeAndEnv(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		testenv.Unavailablef(t, "no home directory: %v", err)
	}
	dir := registry(t, map[string]string{
		"tilde":  `main_dir = "~/code/repo"`,
		"envvar": `main_dir = "$HOME/code/repo"`,
	})
	// canonical is applied to every loaded main_dir, so the expectation goes
	// through it too rather than assuming the home directory has no symlinks.
	want := canonical(filepath.Join(home, "code", "repo"))

	for _, name := range []string{"tilde", "envvar"} {
		c, err := Load(filepath.Join(dir, name+".toml"))
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		if c.MainDir != want {
			t.Errorf("%s: MainDir = %q, want %q", name, c.MainDir, want)
		}
	}
}

// TestLoadResolvesSymlinkedMainDir is a regression test: git reports resolved
// paths for every worktree, so a main_dir left unresolved matched nothing and
// the repo's worktrees became invisible to ls, prune, resume, and completion.
func TestLoadResolvesSymlinkedMainDir(t *testing.T) {
	root := t.TempDir()
	onDisk := filepath.Join(root, "real", "repo")
	if err := os.MkdirAll(onDisk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(root, "link", "repo")

	dir := registry(t, map[string]string{"sym": `main_dir = "` + viaLink + `"`})
	c, err := Load(filepath.Join(dir, "sym.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, err := filepath.EvalSymlinks(onDisk)
	if err != nil {
		t.Fatal(err)
	}
	if c.MainDir != want {
		t.Errorf("MainDir = %q, want the resolved %q", c.MainDir, want)
	}
}

// ---- resolution ------------------------------------------------------------

func TestResolveSelectionOrder(t *testing.T) {
	// Two repos so that "match the repo I am standing in" is a real choice and
	// the not-in-a-repo case is genuinely ambiguous.
	registry(t, map[string]string{
		"alpha": `main_dir = "/tmp/alpha"`,
		"beta":  `main_dir = "/tmp/beta"`,
	})

	t.Run("explicit name wins over the current repo", func(t *testing.T) {
		c, err := Resolve("beta", "/tmp/alpha")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if c.Name != "beta" {
			t.Errorf("Name = %q, want beta", c.Name)
		}
	})

	t.Run("matches the current repo", func(t *testing.T) {
		c, err := Resolve("", "/tmp/beta")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if c.Name != "beta" {
			t.Errorf("Name = %q, want beta", c.Name)
		}
	})

	t.Run("unregistered repo errors", func(t *testing.T) {
		_, err := Resolve("", "/tmp/unknown")
		if err == nil {
			t.Fatal("want an error for an unregistered repo")
		}
		// The message must list the choices, or the user has no next move.
		if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
			t.Errorf("error = %q, want it to list the available configs", err)
		}
	})

	t.Run("unknown name errors", func(t *testing.T) {
		if _, err := Resolve("nope", ""); err == nil {
			t.Fatal("want an error for an unknown config name")
		}
	})

	t.Run("ambiguous outside a repo errors", func(t *testing.T) {
		if _, err := Resolve("", ""); err == nil {
			t.Fatal("want an error when outside a repo with several configs")
		}
	})
}

func TestResolveFallsBackToSoleConfig(t *testing.T) {
	registry(t, map[string]string{"only": `main_dir = "/tmp/only"`})

	// Outside any repo, a single registered config is unambiguous.
	c, err := Resolve("", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if c.Name != "only" {
		t.Errorf("Name = %q, want only", c.Name)
	}
}

func TestResolveIgnoresBrokenSiblingConfigs(t *testing.T) {
	registry(t, map[string]string{
		"good":   `main_dir = "/tmp/good"`,
		"broken": `this is not toml`,
	})

	// A broken config for some other repo must not prevent work on this one.
	c, err := Resolve("", "/tmp/good")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if c.Name != "good" {
		t.Errorf("Name = %q, want good", c.Name)
	}
}

// TestResolveReportsAnUnreadableConfigWhenNothingMatches covers the other side of
// skipping a broken config: when the broken one is this repo's own, silence about it
// leaves "no config matches" pointing at a file that is right there.
func TestResolveReportsAnUnreadableConfigWhenNothingMatches(t *testing.T) {
	registry(t, map[string]string{
		"mine": "main_dir = \"/tmp/mine\"\nbranch_prefix = \"fea ture/\"\n",
	})

	_, err := Resolve("", "/tmp/mine")
	if err == nil {
		t.Fatal("want an error")
	}
	// The mistake, named, and the reason the command could not proceed.
	for _, want := range []string{"mine.toml", `"fea ture/"`, "no other config matches"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestResolveErrorsOnEmptyRegistry(t *testing.T) {
	registry(t, nil)
	if _, err := Resolve("", ""); err == nil {
		t.Fatal("want an error when the registry is empty")
	}
}

func TestNamesIsSorted(t *testing.T) {
	registry(t, map[string]string{
		"zeta":  `main_dir = "/tmp/z"`,
		"alpha": `main_dir = "/tmp/a"`,
		"mid":   `main_dir = "/tmp/m"`,
	})
	names, err := Names()
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if got := strings.Join(names, ","); got != "alpha,mid,zeta" {
		t.Errorf("Names = %q, want sorted", got)
	}
}

// ---- derived values --------------------------------------------------------

// TestSplitPrefixUnderOnePrefix guards the recurring "alice/alice/foo" doubling:
// a slug that already carries the configured prefix must have exactly one leading
// copy removed.
func TestSplitPrefixUnderOnePrefix(t *testing.T) {
	tests := []struct {
		name        string
		prefix      string
		typed       string
		want        string
		wantMatched bool
	}{
		{"strips leading prefix", "x/", "x/foo", "foo", true},
		{"no prefix present", "x/", "foo", "foo", false},
		{"empty prefix is a no-op", "", "x/foo", "x/foo", false},
		{"strips only one copy", "x/", "x/x/foo", "x/foo", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{BranchPrefix: tc.prefix}
			prefix, slug, matched := c.SplitPrefix(tc.typed)
			if slug != tc.want || matched != tc.wantMatched {
				t.Errorf("SplitPrefix(%q) = (%q, %q, %v), want slug %q, matched %v",
					tc.typed, prefix, slug, matched, tc.want, tc.wantMatched)
			}
			// The branch is the two halves rejoined, whichever way they split.
			if prefix+slug != tc.prefix+tc.want {
				t.Errorf("prefix+slug = %q, want %q", prefix+slug, tc.prefix+tc.want)
			}
		})
	}
}

// TestSplitPrefixChoosesAmongSeveral covers the setting teams use when they
// namespace by kind of work rather than by person: the prefix typed at `new` picks
// which one the branch gets, and the slug that comes back is what names the
// worktree.
func TestSplitPrefixChoosesAmongSeveral(t *testing.T) {
	tests := []struct {
		name        string
		prefixes    []string
		typed       string
		wantPrefix  string
		wantSlug    string
		wantMatched bool
	}{
		{"named prefix wins", []string{"feature/", "bug/"}, "bug/eng-1", "bug/", "eng-1", true},
		{"bare slug gets the first", []string{"feature/", "bug/"}, "eng-1", "feature/", "eng-1", false},
		// A prefix that is not configured stays in the slug, where `new` rejects it
		// by name rather than inventing a namespace.
		{"unknown prefix is left alone", []string{"feature/", "bug/"}, "feat/eng-1", "feature/", "feat/eng-1", false},
		// List order must not decide this, so the more specific prefix is listed
		// second — where a first-match loop would miss it.
		{"longest match wins", []string{"feature/", "feature/exp/"}, "feature/exp/eng-1", "feature/exp/", "eng-1", true},
		// Some branches namespaced and some not: the empty prefix is the default,
		// and never reads as a match of its own.
		{"empty prefix can be the default", []string{"", "feature/"}, "eng-1", "", "eng-1", false},
		{"empty default does not shadow the rest", []string{"", "feature/"}, "feature/eng-1", "feature/", "eng-1", true},
		// Rejected a step later, as "the slug is empty once the branch prefix is
		// removed" — but the split itself has to survive it.
		{"prefix with nothing after it", []string{"feature/", "bug/"}, "bug/", "bug/", "", true},
		// A prefix need not end in "/": some teams use "feature-".
		{"dashed prefixes split too", []string{"feature-", "bug-"}, "bug-eng-1", "bug-", "eng-1", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{BranchPrefixes: tc.prefixes}
			prefix, slug, matched := c.SplitPrefix(tc.typed)
			if prefix != tc.wantPrefix || slug != tc.wantSlug || matched != tc.wantMatched {
				t.Errorf("SplitPrefix(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.typed, prefix, slug, matched, tc.wantPrefix, tc.wantSlug, tc.wantMatched)
			}
		})
	}
}

// TestPrefixesAlwaysHoldsOne is what lets every caller treat "no prefix" and "one
// prefix" alike: the create path prepends Prefixes()[0] without asking whether
// this repo namespaces anything.
func TestPrefixesAlwaysHoldsOne(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"nothing configured", &Config{}, `[""]`},
		{"singular spelling", &Config{BranchPrefix: "alice/"}, `["alice/"]`},
		{"list spelling", &Config{BranchPrefixes: []string{"feature/", "bug/"}}, `["feature/" "bug/"]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.Prefixes()
			if len(got) == 0 {
				t.Fatal("Prefixes is empty, so the create path has no prefix to prepend")
			}
			if fmt.Sprintf("%q", got) != tc.want {
				t.Errorf("Prefixes = %q, want %s", got, tc.want)
			}
		})
	}
}

func TestWindowName(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		slug     string
		override string
		want     string
	}{
		{"ticket key", DefaultTicketPattern, "proj-142-fix", "", "PROJ-142"},
		{"ticket key alone", DefaultTicketPattern, "bug-7", "", "BUG-7"},
		{"short slug as-is", DefaultTicketPattern, "hotfix", "", "HOTFIX"},
		{"override wins", DefaultTicketPattern, "proj-1", "billing", "BILLING"},
		{"exactly the cap is not shortened", DefaultTicketPattern, "abcdefghij", "", "ABCDEFGHIJ"},
		// The default pattern accepts any issue-key scheme, which means any
		// whole letters-dash-digits word reads as a ticket. A repo that wants
		// stricter matching pins its own pattern, and one that tracks no tickets
		// at all turns the pattern off — both below.
		{"generalized default matches any key", DefaultTicketPattern, "fix-2-bugs", "", "FIX-2"},
		{"pinned pattern ignores other keys", `(?i)^(proj-[0-9]+)`, "fix-2-bugs", "", "FIX-2-BUGS"},
		{"pinned pattern matches its own", `(?i)^(proj-[0-9]+)`, "proj-142-fix", "", "PROJ-142"},

		// A digit run that does not end the word is part of a description, not a
		// key: "fix-2fa-login" is work on two-factor login, and naming its window
		// FIX-2 named nothing. It is shortened like any other description.
		{"digits mid-word are not a key", DefaultTicketPattern, "fix-2fa-login", "", "FIX-2FA-LO…"},

		// Work with no ticket behind it: the slug is the whole name, and the same
		// cap applies to it, because the status line is the same width either way.
		{"no pattern leaves the slug alone", "", "proj-1-fix", "", "PROJ-1-FIX"},
		{"long slug is cut at the cap", "", "flaky-payment-test", "", "FLAKY-PAYM…"},

		// A cut landing just after a hyphen would leave one against the mark,
		// where it reads as punctuation rather than as part of the name.
		{"no hyphen is left against the mark", "", "dark-mode-toggle", "", "DARK-MODE…"},

		// One over the cap is where the guard against shortening to the same width
		// bites: ten runes and a mark is eleven columns, which is what it started
		// at.
		{"shortening never lengthens", "", "rewrite-css", "", "REWRITE-CSS"},
		{"one over the cap is left whole", "", "abcdefghijk", "", "ABCDEFGHIJK"},

		// Counted in runes, not bytes. refname forbids control characters and
		// git's metacharacters, not the rest of Unicode, so a byte-wise cut here
		// would both misjudge the width and split the "é" in half.
		{"multi-byte slug is cut by rune", "", "café-refactor-plan", "", "CAFÉ-REFAC…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{TicketPattern: tc.pattern}
			if got := c.WindowName(tc.slug, tc.override); got != tc.want {
				t.Errorf("WindowName(%q, %q) = %q, want %q", tc.slug, tc.override, got, tc.want)
			}
		})
	}
}

// An empty ticket_pattern has to survive Load, which is where every other empty
// string in the file is replaced by a default. It is how a repository that
// tracks no tickets says so, and defaulting it would leave that repository with
// no way to turn ticket matching off at all.
func TestLoadKeepsAnEmptyTicketPatternEmpty(t *testing.T) {
	dir := registry(t, map[string]string{
		"off":    "main_dir = '/tmp/repo'\nticket_pattern = ''\n",
		"unset":  "main_dir = '/tmp/repo'\n",
		"pinned": "main_dir = '/tmp/repo'\nticket_pattern = '(?i)^(proj-[0-9]+)'\n",
	})

	off, err := Load(filepath.Join(dir, "off.toml"))
	if err != nil {
		t.Fatalf("load off: %v", err)
	}
	if off.TicketPattern != "" {
		t.Errorf("ticket_pattern = '' loaded as %q, want it left empty", off.TicketPattern)
	}
	// A slug the default pattern would have read as key PROJ-1, kept short enough
	// that the cap is not what this test ends up measuring.
	if got, want := off.WindowName("proj-1-fix", ""), "PROJ-1-FIX"; got != want {
		t.Errorf("WindowName with matching off = %q, want %q", got, want)
	}

	unset, err := Load(filepath.Join(dir, "unset.toml"))
	if err != nil {
		t.Fatalf("load unset: %v", err)
	}
	if unset.TicketPattern != DefaultTicketPattern {
		t.Errorf("unset ticket_pattern = %q, want the default", unset.TicketPattern)
	}

	pinned, err := Load(filepath.Join(dir, "pinned.toml"))
	if err != nil {
		t.Fatalf("load pinned: %v", err)
	}
	if pinned.TicketPattern != `(?i)^(proj-[0-9]+)` {
		t.Errorf("pinned ticket_pattern = %q, want what the file set", pinned.TicketPattern)
	}
}

func TestDirFor(t *testing.T) {
	c := &Config{MainDir: "/home/u/code/myrepo", BranchPrefix: "alice/"}
	if got, want := c.DirFor("proj-1"), "/home/u/code/myrepo-proj-1"; got != want {
		t.Errorf("DirFor = %q, want %q", got, want)
	}
}

// ---- reporting -------------------------------------------------------------

// TestExplicitDistinguishesSettingFromDefaulting is what lets `treewright config`
// say which values were chosen. A setting that happens to match its default is
// the case that makes comparing values insufficient.
func TestExplicitDistinguishesSettingFromDefaulting(t *testing.T) {
	dir := registry(t, map[string]string{
		"proj": "main_dir = \"/tmp/repo\"\nbase_branch = \"" + DefaultBaseBranch + "\"\n",
	})

	c, err := Load(filepath.Join(dir, "proj.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Explicit("base_branch") {
		t.Error("base_branch was set to the default value, and reads as never set")
	}
	if c.Explicit("command") {
		t.Error("command was never set, and reads as explicit")
	}
	if got, want := c.Path(), filepath.Join(dir, "proj.toml"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}
