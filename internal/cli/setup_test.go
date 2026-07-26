package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/config"
)

// unregistered is a scratch repo whose config has been taken away again, which is
// the state a first-time user is in: a repository, an empty registry, and no idea
// what the file is meant to contain.
func unregistered(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, "")
	if err := os.Remove(filepath.Join(f.registry, "proj.toml")); err != nil {
		t.Fatalf("clear the registry: %v", err)
	}
	return f
}

// TestSetupProducesAUsableConfig is the whole point of the command: what it
// writes must be a config the rest of treewright can immediately act on, not a
// template needing edits first.
func TestSetupProducesAUsableConfig(t *testing.T) {
	f := unregistered(t)
	f.Write(f.MainDir, ".env", "SECRET=1\n") // gitignored by the fixture

	r := f.exec("setup")
	if r.err != nil {
		t.Fatalf("setup: %v\n%s", r.err, r.both())
	}

	path := filepath.Join(f.registry, "repo.toml")
	if got := strings.TrimSpace(r.stdout); got != path {
		t.Errorf("stdout = %q, want just the config path %q", got, path)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the generated config does not load: %v", err)
	}
	if cfg.MainDir != f.MainDir {
		t.Errorf("main_dir = %q, want %q", cfg.MainDir, f.MainDir)
	}
	if cfg.BaseBranch != "main" {
		t.Errorf("base_branch = %q, want main from origin/HEAD", cfg.BaseBranch)
	}
	// The fixture's git identity is test@example.com.
	if cfg.BranchPrefix != "test/" {
		t.Errorf("branch_prefix = %q, want test/ from the git email", cfg.BranchPrefix)
	}
	if len(cfg.CarryFiles) != 1 || cfg.CarryFiles[0] != ".env" {
		t.Errorf("carry_files = %v, want the gitignored .env", cfg.CarryFiles)
	}

	// And the repo is now registered: a command that resolves a config finds it
	// without being told where to look.
	if out := f.mustRun("ls"); !strings.Contains(out, "no worktrees for repo") {
		t.Errorf("ls after setup = %q, want it to resolve the new config", out)
	}
	// Creating work is the next thing a new user does, so it has to work too.
	if _, err := f.run("new", "eng-1"); err != nil {
		t.Errorf("new after setup: %v", err)
	}
}

func TestSetupReportsWhatItGuessed(t *testing.T) {
	f := unregistered(t)
	f.Write(f.MainDir, ".env", "SECRET=1\n")

	r := f.exec("setup")
	if r.err != nil {
		t.Fatalf("setup: %v\n%s", r.err, r.both())
	}
	// Every guess must be visible, because each one is a decision the user may
	// need to overrule and none of them is obvious from the command they ran.
	for _, want := range []string{"base branch main", "branch prefix \"test/\"", ".env"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr = %q, want it to report %q", r.stderr, want)
		}
	}
}

// pushBranches puts branches on origin under the given names, that being where
// setup reads a team's naming convention from.
func pushBranches(f *fixture, names ...string) {
	f.t.Helper()
	for _, name := range names {
		f.Git(f.MainDir, "push", "--quiet", "origin", "HEAD:refs/heads/"+name)
	}
	// The namespaces are counted off refs/remotes/origin, which a push alone does
	// not create for a branch this checkout does not track.
	f.Git(f.MainDir, "fetch", "--quiet", "origin")
}

// TestSetupProposesThePrefixesOriginUses is the guess that saves a new user from
// reading their own branch list: a repo that namespaces by kind of work gets those
// prefixes, ordered by how much it uses each.
func TestSetupProposesThePrefixesOriginUses(t *testing.T) {
	f := unregistered(t)
	pushBranches(f, "feature/a", "feature/b", "feature/c", "bug/d", "bug/e", "chore/f")

	r := f.exec("setup")
	if r.err != nil {
		t.Fatalf("setup: %v\n%s", r.err, r.both())
	}
	cfg, err := config.Load(filepath.Join(f.registry, "repo.toml"))
	if err != nil {
		t.Fatalf("the generated config does not load: %v", err)
	}
	// Ordered by use, and chore/ left out: one branch is an incident, two is a
	// convention.
	if got := strings.Join(cfg.Prefixes(), ","); got != "feature/,bug/" {
		t.Errorf("Prefixes = %q, want feature/,bug/ in that order", got)
	}
	// The counts are what chose the order, so the report has to show them.
	if !strings.Contains(r.stderr, "feature/ (3), bug/ (2)") {
		t.Errorf("stderr = %q, want the prefixes and their branch counts", r.stderr)
	}

	// And the config works both ways round: the default, and a named prefix.
	f.mustRun("new", "eng-1")
	if got := f.Git(f.DirFor("eng-1"), "branch", "--show-current"); got != "feature/eng-1" {
		t.Errorf("branch = %q, want feature/eng-1 from the detected default", got)
	}
	f.mustRun("new", "bug/eng-2")
	if got := f.Git(f.DirFor("eng-2"), "branch", "--show-current"); got != "bug/eng-2" {
		t.Errorf("branch = %q, want bug/eng-2", got)
	}
}

// TestSetupPrefersOriginsSchemeOverTheEmail: where every branch on origin is a
// feature/, new work is a feature/ too, whatever this user's git email says. The
// email guess exists to make branches attributable on a shared remote, and a repo
// that already namespaces has answered that question its own way.
func TestSetupPrefersOriginsSchemeOverTheEmail(t *testing.T) {
	f := unregistered(t)
	pushBranches(f, "feature/a", "feature/b")

	r := f.exec("setup")
	if r.err != nil {
		t.Fatalf("setup: %v\n%s", r.err, r.both())
	}
	cfg, err := config.Load(filepath.Join(f.registry, "repo.toml"))
	if err != nil {
		t.Fatalf("the generated config does not load: %v", err)
	}
	// One prefix is the singular setting, not a list of one.
	if cfg.BranchPrefix != "feature/" || cfg.Explicit("branch_prefixes") {
		t.Errorf("prefixes = %+v, want branch_prefix = feature/", cfg.Prefixes())
	}
	if !strings.Contains(r.stderr, "from the 2 branches on origin") {
		t.Errorf("stderr = %q, want where the prefix came from", r.stderr)
	}
}

// TestSetupDoesNotMistakePeopleForKindsOfWork is the failure this detection is
// built to avoid. "alice/x, alice/y, bob/z" and "feature/x, feature/y, bug/z" are
// the same shape, so counting alone would write colleagues' names into the config
// as though they were kinds of work — and send every branch this user makes into
// somebody else's namespace.
func TestSetupDoesNotMistakePeopleForKindsOfWork(t *testing.T) {
	f := unregistered(t)
	pushBranches(f, "alice/a", "alice/b", "bob/c", "bob/d")

	if r := f.exec("setup"); r.err != nil {
		t.Fatalf("setup: %v\n%s", r.err, r.both())
	}
	cfg, err := config.Load(filepath.Join(f.registry, "repo.toml"))
	if err != nil {
		t.Fatalf("the generated config does not load: %v", err)
	}
	// The fixture's git identity is test@example.com.
	if got := strings.Join(cfg.Prefixes(), ","); got != "test/" {
		t.Errorf("Prefixes = %q, want the git-email guess test/", got)
	}
}

func TestSetupDryRunWritesNothing(t *testing.T) {
	f := unregistered(t)

	r := f.exec("setup", "--dry-run")
	if r.err != nil {
		t.Fatalf("setup --dry-run: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stdout, "main_dir = ") {
		t.Errorf("stdout = %q, want the config it would write", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(f.registry, "repo.toml")); err == nil {
		t.Error("--dry-run wrote the config anyway")
	}
}

func TestSetupRefusesToOverwrite(t *testing.T) {
	f := newFixture(t, "") // already registered, as "proj"

	// Same name: the existing file is the one thing setup must never clobber,
	// since it holds edits it cannot reproduce.
	r := f.exec("setup", "proj")
	if r.err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(r.err.Error(), "already exists") {
		t.Errorf("err = %v, want it to say the file already exists", r.err)
	}

	// Different name, same repository: two configs for one repo would make which
	// applies depend on registry order.
	r = f.exec("setup", "second")
	if r.err == nil {
		t.Fatal("want an error for a repo that is already registered")
	}
	if !strings.Contains(r.err.Error(), `already registered as "proj"`) {
		t.Errorf("err = %v, want it to name the existing config", r.err)
	}
}

func TestSetupOutsideARepository(t *testing.T) {
	f := unregistered(t)
	t.Chdir(t.TempDir())

	_, err := f.run("setup")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "not in a git repository") {
		t.Errorf("err = %v, want it to say there is no repository here", err)
	}
}

// TestSetupFromInsideAWorktree covers the likely mistake of registering a repo
// while standing in one of its worktrees, which must register the repository
// rather than the worktree — the worktree is not a repo you work on, and its
// directory will be deleted.
func TestSetupFromInsideAWorktree(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "feature")
	if err := os.Remove(filepath.Join(f.registry, "proj.toml")); err != nil {
		t.Fatalf("clear the registry: %v", err)
	}
	t.Chdir(f.DirFor("feature"))

	if _, err := f.run("setup"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg, err := config.Load(filepath.Join(f.registry, "repo.toml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MainDir != f.MainDir {
		t.Errorf("main_dir = %q, want the main checkout %q", cfg.MainDir, f.MainDir)
	}
}

func TestSetupRejectsUnusableNames(t *testing.T) {
	f := unregistered(t)

	for _, name := range []string{"a/b", "..", ".hidden", "-x"} {
		t.Run(name, func(t *testing.T) {
			r := f.exec("setup", name)
			if r.err == nil {
				t.Fatalf("want an error, got %q", r.stdout)
			}
			if entries, _ := os.ReadDir(f.registry); len(entries) != 0 {
				t.Errorf("registry is no longer empty: %v", entries)
			}
		})
	}
}

func TestBranchPrefixFor(t *testing.T) {
	tests := []struct{ email, want string }{
		{"john.doe@example.com", "john/"},
		{"alice@example.com", "alice/"},
		{"alice+work@example.com", "alice/"},
		{"Alice.Smith@Example.COM", "alice/"},
		{"dev-ops@example.com", "dev-ops/"},
		{"123@example.com", "123/"},
		{"", ""},
		{"@example.com", ""},
		{"...@example.com", ""},
	}
	for _, tc := range tests {
		if got := branchPrefixFor(tc.email); got != tc.want {
			t.Errorf("branchPrefixFor(%q) = %q, want %q", tc.email, got, tc.want)
		}
	}
}

// TestCarryCandidates pins what setup will and will not propose. The cost of a
// false positive is high — a dependency tree copied into every worktree — so the
// rule is deliberately narrow.
func TestCarryCandidates(t *testing.T) {
	got := carryCandidates([]string{
		"node_modules/",            // a wholly ignored directory
		"dist/",                    //
		".env",                     // wanted
		"apps/api/.env",            // wanted, inside a tracked directory
		".env.local",               // wanted
		".envrc",                   // wanted
		".env.example",             // a committed template, no values to carry
		".env.sample",              //
		"coverage.out",             // ignored, but nothing an app needs
		"apps/web/.next/cache.bin", //
	})
	want := []string{".env", ".env.local", ".envrc", "apps/api/.env"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("carryCandidates = %v, want %v", got, want)
	}
}

func TestCarryCandidatesIsCapped(t *testing.T) {
	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, ".env."+string(rune('a'+i)))
	}
	// A repo with dozens of env files would otherwise produce a config nobody
	// reads, which is worse than one that is visibly incomplete.
	if got := carryCandidates(many); len(got) != 10 {
		t.Errorf("proposed %d files, want the list capped at 10", len(got))
	}
}

// ---- config ----------------------------------------------------------------

func TestConfigDistinguishesDefaultsFromChoices(t *testing.T) {
	f := newFixture(t, "command = 'zsh'\n")

	r := f.exec("config")
	if r.err != nil {
		t.Fatalf("config: %v\n%s", r.err, r.both())
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want nothing", r.stderr)
	}

	byKey := map[string]string{}
	for _, line := range strings.Split(r.stdout, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			byKey[fields[0]] = strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		}
	}

	// A value the file set must not be labelled a default, and one it left out
	// must be — that distinction is the reason the command exists.
	if got := byKey["command"]; got != "zsh" {
		t.Errorf("command = %q, want zsh with no default marker", got)
	}
	if got := byKey["resume_command"]; !strings.Contains(got, "(default)") {
		t.Errorf("resume_command = %q, want it marked as a default", got)
	}
	// base_branch is set explicitly by the fixture, to the same value as the
	// default: reporting it as a default would be wrong even though the value is
	// identical.
	if got := byKey["base_branch"]; strings.Contains(got, "(default)") {
		t.Errorf("base_branch = %q, want it reported as an explicit setting", got)
	}
	if got := byKey["post_create"]; got != "(none)" {
		t.Errorf("post_create = %q, want (none)", got)
	}
	if got := byKey["file"]; !strings.HasSuffix(got, "proj.toml") {
		t.Errorf("file = %q, want the config's path", got)
	}
}
