package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/agentinit"
)

// The agent side of the integration: agent-init installing the plugin, the
// agent key carrying it into worktrees, and doctor catching the half-installed
// states. The signal protocol itself is covered in signal_test.go.

// pluginFiles is every file the claude module installs, relative to whichever
// directory the plugin was installed into.
func pluginFiles(t *testing.T) []string {
	t.Helper()
	agent, ok := agentinit.Lookup("claude")
	if !ok {
		t.Fatal("no claude module")
	}
	var out []string
	for _, f := range agent.Plugin {
		out = append(out, f.Path)
	}
	return out
}

// pluginPaths is the same files relative to a checkout, which is where --local
// puts them and where the carry takes them.
func pluginPaths(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, rel := range pluginFiles(t) {
		out = append(out, ".claude/skills/treewright/"+rel)
	}
	return out
}

// TestAgentInitCoversEveryRepositoryByDefault is the placement with no trap in
// it: one install under the user's own home, covering every checkout the agent
// is started in — worktrees included, with nothing to carry and no repository
// required to have one.
func TestAgentInitCoversEveryRepositoryByDefault(t *testing.T) {
	f := newFixture(t, "")
	home := os.Getenv("HOME")
	t.Chdir(t.TempDir()) // outside any repository at all

	r := f.exec("agent-init", "claude")
	if r.err != nil {
		t.Fatalf("agent-init claude: %v\n%s", r.err, r.both())
	}
	// stdout is the directory alone, so the path can be piped somewhere useful.
	want := filepath.Join(home, ".claude", "skills", "treewright")
	if got := strings.TrimSpace(r.stdout); got != want {
		t.Errorf("stdout = %q, want exactly %q", got, want)
	}
	for _, rel := range pluginFiles(t) {
		if _, err := os.Stat(filepath.Join(want, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not installed: %v", rel, err)
		}
	}

	// Nothing to carry at user scope, and no .gitignore to speak of: ~/.claude
	// covers every directory the agent is ever started in, and belongs to no
	// repository.
	if strings.Contains(r.stderr, `agent = "claude"`) {
		t.Errorf("stderr = %q, want no carry advice for a placement that needs none", r.stderr)
	}
	if strings.Contains(r.stderr, ".gitignore") {
		t.Errorf("stderr = %q, want no ignore advice for a directory no repository holds", r.stderr)
	}
	// Hooks do not reload themselves, so a session already open stays wired to
	// whatever it loaded at start.
	if !strings.Contains(r.stderr, "/reload-plugins") {
		t.Errorf("stderr = %q, want the reload named", r.stderr)
	}
	// And the placement it did not take is named, the way the second option
	// always is.
	if !strings.Contains(r.stderr, "agent-init claude --local") {
		t.Errorf("stderr = %q, want the per-repository placement offered", r.stderr)
	}
}

// TestAgentInitFollowsTheAgentsConfigDirectory is the default placement for a
// user who has moved the agent's configuration directory, which the XDG layout
// does as a matter of course.
//
// It is here as a whole-command test and not only as the unit test in agentinit
// because of how the bug presented: `agent-init` wrote a correct plugin, printed
// the directory it wrote, and doctor compared that same wrong directory against
// what it would write and called it current. Three reports of success and the
// agent, reading the directory it had actually been pointed at, loaded nothing.
// So the assertion that matters is the pair — installed where the variable says,
// and *not* at the default the agent has stopped reading.
func TestAgentInitFollowsTheAgentsConfigDirectory(t *testing.T) {
	f := newFixture(t, "")
	moved := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", moved)

	r := f.exec("agent-init", "claude")
	if r.err != nil {
		t.Fatalf("agent-init claude: %v\n%s", r.err, r.both())
	}
	want := filepath.Join(moved, "skills", "treewright")
	if got := strings.TrimSpace(r.stdout); got != want {
		t.Errorf("stdout = %q, want the moved directory %q", got, want)
	}
	for _, rel := range pluginFiles(t) {
		if _, err := os.Stat(filepath.Join(want, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not installed where the agent reads: %v", rel, err)
		}
	}
	// The default is where it used to go, and the agent is no longer looking
	// there — a copy left behind would be the stale wiring nobody knows about.
	stale := filepath.Join(os.Getenv("HOME"), ".claude", "skills", "treewright")
	if _, err := os.Stat(stale); err == nil {
		t.Errorf("the plugin was also written to %s, which the agent no longer reads", stale)
	}

	// And doctor agrees, which is the half that made the bug silent: it has to
	// be asking about the same directory the install used.
	if got := has(t, findings(t, f), "is not wired to report state"); got != "" {
		t.Errorf("doctor reports the agent unwired (%s) after installing where it reads", got)
	}
}

// TestAgentInitLocalInstallsThePluginIntoTheMainCheckout is the placement for a
// machine where treewright's wiring should touch one repository and no other.
// It is also the one with the carry trap in it, which is why it says so.
func TestAgentInitLocalInstallsThePluginIntoTheMainCheckout(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("agent-init", "claude", "--local")
	if r.err != nil {
		t.Fatalf("agent-init claude --local: %v\n%s", r.err, r.both())
	}
	want := filepath.Join(f.MainDir, ".claude", "skills", "treewright") + "\n"
	if r.stdout != want {
		t.Errorf("stdout = %q, want exactly %q", r.stdout, want)
	}
	for _, rel := range pluginPaths(t) {
		if _, err := os.Stat(filepath.Join(f.MainDir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not installed: %v", rel, err)
		}
	}
	// Nothing under the user's home was touched: this placement is the one that
	// was asked for.
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude")); err == nil {
		t.Error("--local wrote into the user's own directory as well")
	}

	// The instructions are narration: the carry the plugin needs to reach a
	// worktree, and the fact that nothing ignores a directory treewright
	// invented.
	if !strings.Contains(r.stderr, `agent = "claude"`) {
		t.Errorf("stderr = %q, want the carry named", r.stderr)
	}
	if !strings.Contains(r.stderr, ".gitignore") {
		t.Errorf("stderr = %q, want the reader told the plugin is not ignored for them", r.stderr)
	}
	if !strings.Contains(r.stderr, "/reload-plugins") {
		t.Errorf("stderr = %q, want the reload named", r.stderr)
	}
}

// TestAgentInitUpdatesRatherThanRepeats is the whole reason the wiring moved out
// of a settings file: run again after an upgrade it rewrites what changed and
// says so, where a pasted fragment could only ever be pasted a second time.
func TestAgentInitUpdatesRatherThanRepeats(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("agent-init", "claude")

	r := f.exec("agent-init", "claude")
	if r.err != nil {
		t.Fatalf("agent-init claude: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "already up to date") {
		t.Errorf("stderr = %q, want the second run reported as a no-op", r.stderr)
	}

	// What an upgrade looks like from here: a file left by an older treewright,
	// named on the way past rather than silently replaced.
	hooks := filepath.Join(os.Getenv("HOME"), ".claude", "skills", "treewright", "hooks", "hooks.json")
	if err := os.WriteFile(hooks, []byte(`{"hooks":{"Stop":[]}}`), 0o644); err != nil {
		t.Fatalf("write the stale hooks: %v", err)
	}
	r = f.exec("agent-init", "claude")
	if r.err != nil {
		t.Fatalf("agent-init claude: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "hooks/hooks.json") {
		t.Errorf("stderr = %q, want the file that had drifted named", r.stderr)
	}
	body, err := os.ReadFile(hooks)
	if err != nil || !strings.Contains(string(body), "treewright signal working") {
		t.Errorf("hooks.json = %q, want the current wiring", body)
	}
}

// TestAgentInitPrintWritesNothing keeps the read-before-you-run path: these are
// hooks that will run on every transition of an agent, and tmux-init prints by
// default for the same reason.
func TestAgentInitPrintWritesNothing(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("agent-init", "claude", "--print")
	if r.err != nil {
		t.Fatalf("agent-init claude --print: %v\n%s", r.err, r.both())
	}
	// Each file under the path it would be written to, which for the default
	// placement is the user's own directory — resolved rather than left as a ~,
	// because the whole question --print answers is where these are going, and
	// the agent's config directory is movable enough that the unresolved form
	// can name somewhere nothing will ever read.
	userDir := filepath.Join(os.Getenv("HOME"), ".claude", "skills", "treewright")
	for _, rel := range pluginFiles(t) {
		if !strings.Contains(r.stdout, "==> "+userDir+"/"+rel+" <==") {
			t.Errorf("stdout does not announce %s:\n%s", rel, r.stdout)
		}
		if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude", "skills", "treewright", filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was written by a command that only prints", rel)
		}
	}
	if !strings.Contains(r.stdout, "# Driving treewright") || !strings.Contains(r.stdout, "treewright signal working") {
		t.Errorf("stdout = %.200q..., want both directions of the wiring", r.stdout)
	}
	if !strings.Contains(r.stderr, "nothing was written") {
		t.Errorf("stderr = %q, want it said that nothing was installed", r.stderr)
	}

	// And with --local the headers name where that placement would put them,
	// since reading the files is also reading where they land.
	local := f.exec("agent-init", "claude", "--print", "--local")
	if local.err != nil {
		t.Fatalf("agent-init claude --print --local: %v\n%s", local.err, local.both())
	}
	for _, rel := range pluginPaths(t) {
		if !strings.Contains(local.stdout, "==> "+rel+" <==") {
			t.Errorf("stdout does not announce %s:\n%s", rel, local.stdout)
		}
		if _, err := os.Stat(filepath.Join(f.MainDir, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was written by a command that only prints", rel)
		}
	}
}

// TestAgentInitLocalOutsideARepositorySaysWhatToDo: --local is the only form
// that needs a config, and the error names the placement that does not.
func TestAgentInitLocalOutsideARepositorySaysWhatToDo(t *testing.T) {
	f := newFixture(t, "")
	registry := t.TempDir() // empty, so nothing resolves
	t.Setenv("TREEWRIGHT_CONFIG_DIR", registry)
	t.Chdir(t.TempDir())

	r := f.exec("agent-init", "claude", "--local")
	if r.err == nil {
		t.Fatalf("agent-init claude --local succeeded outside a repository:\n%s", r.both())
	}
	if !strings.Contains(flat(r.err.Error()), "cover every repository") {
		t.Errorf("err = %v, want the placement that needs no config named", r.err)
	}
}

// TestThePluginTeachesTheCLIThatExists holds the plugin's own text to the
// command table: every `treewright <command>` it shows, in a code block or
// inline code, must be a command lookup resolves, and every JSON field it
// teaches must be a tag of the ls schema. A rename that forgets the guide fails
// here rather than teaching agents a CLI that is gone.
func TestThePluginTeachesTheCLIThatExists(t *testing.T) {
	agent, ok := agentinit.Lookup("claude")
	if !ok {
		t.Fatal("no claude module")
	}
	var b strings.Builder
	for _, f := range agent.Plugin {
		b.WriteString(f.Body)
	}
	plugin := b.String()

	named := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile("`treewright ([a-z-]+)"),         // inline code
		regexp.MustCompile(`(?m)^    treewright ([a-z-]+)`), // command examples
		regexp.MustCompile(`"command": "treewright ([a-z-]+)`),
	} {
		for _, m := range re.FindAllStringSubmatch(plugin, -1) {
			named[m[1]] = true
		}
	}
	if len(named) < 5 {
		t.Fatalf("only %d commands found in the plugin — the extraction is likely broken: %v", len(named), named)
	}
	// help and version are dispatched by Run itself rather than from the command
	// table, so lookup has never heard of them. They are commands all the same,
	// and a guide pointing at "treewright help new" — which is where an agent
	// finds the detail this file has no room for — points at something real.
	runByDispatch := map[string]bool{"help": true, "version": true}
	for name := range named {
		if runByDispatch[name] {
			continue
		}
		if lookup(name) == nil {
			t.Errorf("the plugin teaches %q, which is not a command", name)
		}
	}

	// The JSON fields the guide explains, checked against the schema's actual
	// tags so `ls --json` and its documentation cannot part company.
	tags := map[string]bool{}
	for _, field := range reflect.VisibleFields(reflect.TypeFor[worktreeJSON]()) {
		tags[field.Tag.Get("json")] = true
	}
	for _, field := range []string{"base", "status", "agent_state", "ahead", "behind"} {
		if !strings.Contains(plugin, "`"+field+"`") && !strings.Contains(plugin, `"`+field+`"`) {
			t.Errorf("the plugin no longer explains the %s field", field)
		}
		if !tags[field] {
			t.Errorf("the plugin explains %q, which ls --json does not report", field)
		}
	}

	// Never `tw` as an invocation: the reader runs commands in a shell where
	// the function may not exist. Mentioning `tw` by name is fine — the guide
	// explains exactly that — but no command may start with it.
	if strings.Contains(plugin, "`tw ") || regexp.MustCompile(`(?m)^    tw `).MatchString(plugin) {
		t.Error("the plugin shows a command invoked as tw, which non-interactive shells may not have")
	}
}

func TestAgentInitRejectsAnUnknownAgent(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("agent-init", "copilot")
	if !errors.Is(r.err, ErrUsage) {
		t.Fatalf("err = %v, want a usage error", r.err)
	}
	if !strings.Contains(r.stderr, "claude") {
		t.Errorf("stderr = %q, want the modules that do exist listed", r.stderr)
	}
	if r := f.exec("agent-init"); !errors.Is(r.err, ErrUsage) {
		t.Errorf("err = %v, want a usage error for the missing agent", r.err)
	}
}

// TestAgentKeyCarriesThePluginAndTheSettings pins the key's reason to exist:
// the agent's gitignored per-project state — the plugin's hooks and skill, and
// the permission decisions beside them — reaches every new worktree with no
// carry_files line to remember. The plugin is a tree, and every file of it is
// named on the carried list rather than the directory holding them, so a
// worktree cannot arrive with the skill and no hooks.
func TestAgentKeyCarriesThePluginAndTheSettings(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")
	settings := filepath.Join(f.MainDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settings, []byte(`{"permissions":{}}`), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	f.mustRun("agent-init", "claude", "--local")

	f.mustRun("new", "alpha")
	for _, rel := range append(pluginPaths(t), ".claude/settings.local.json") {
		if _, err := os.Stat(filepath.Join(f.DirFor("alpha"), filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not carried into the worktree: %v", rel, err)
		}
	}
}

// TestAgentCarryIsSilentWhenAbsent is the one way the implicit carry differs
// from a carry_files entry: nobody asserted the file exists, so a checkout
// that has never run the agent gets no warning about it.
func TestAgentCarryIsSilentWhenAbsent(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")

	r := f.exec("new", "alpha")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "carry") {
		t.Errorf("stderr = %q, want no carry warning for a file nobody asserted", r.stderr)
	}
}

func TestConfigReportsWhatTheAgentKeySupplies(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")

	out := f.mustRun("config")

	// Every per-project artifact the module carries, one per line: the settings
	// file and all three files of the plugin, since a directory is not what the
	// carry copies. They are one row, marked in the FROM column as the module's
	// rather than the file's — a row of its own, so nothing has to say where the
	// marked run began.
	carried := configRows(t, out)["carry_files"]
	want := ".claude/settings.local.json\n" +
		".claude/skills/treewright/SKILL.md\n" +
		".claude/skills/treewright/.claude-plugin/plugin.json\n" +
		".claude/skills/treewright/hooks/hooks.json"
	if carried.from != "agent" || carried.value != want {
		t.Errorf("carry_files = %+v, want %q from the agent\n%s", carried, want, out)
	}
	if got := configRows(t, out)["command"]; got.from != "agent" || got.value != "claude {prompt}" {
		t.Errorf("command = %+v, want the agent's default marked as the agent's", got)
	}

	// An explicit command is the file's own, and says so by carrying no marker.
	f2 := newFixture(t, "agent = 'claude'\ncommand = 'nvim'\n")
	if got := configRows(t, f2.mustRun("config"))["command"]; got.from != "" || got.value != "nvim" {
		t.Errorf("command = %+v, want the explicit command unmarked", got)
	}
}

// TestSetupDetectsAnInstalledAgent relies on the fixture's stubbed claude
// being on PATH, which is exactly what detection looks for.
func TestSetupDetectsAnInstalledAgent(t *testing.T) {
	f := newFixture(t, "")

	// The fixture's repo is already registered as "proj", and setup refuses a
	// second config for the same repository — so ask for the same one, dry.
	r := f.exec("setup", "-n", "proj")
	if r.err != nil {
		t.Fatalf("setup: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stdout, "agent = \"claude\"") {
		t.Errorf("generated config = %q, want the detected agent written", r.stdout)
	}
	if !strings.Contains(flat(r.stderr), "agent claude (found on PATH") {
		t.Errorf("stderr = %q, want the detection reported like every other guess", r.stderr)
	}
}

// ---- doctor ------------------------------------------------------------------

func TestDoctorWarnsWhenTheAgentReportsNoState(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")

	found := findings(t, f)
	if got := has(t, found, "not wired to report state"); got != "warn" {
		t.Errorf("finding = %q, want a warning about the missing wiring\nall: %v", got, found)
	}
	if got := has(t, found, "agent-init claude"); got == "" {
		t.Errorf("no finding names the fix\nall: %v", found)
	}
}

func TestDoctorNamesTheCarryTrap(t *testing.T) {
	// No agent key and no carry entry — but the plugin sitting in the main
	// checkout, where it reaches the base checkout's window and no worktree at
	// all. The module is sniffed from the default command, claude, which is the
	// guess a warn-level hint is allowed to rest on.
	f := newFixture(t, "")
	f.mustRun("agent-init", "claude", "--local")

	if got := has(t, findings(t, f), "reaches no worktree"); got != "warn" {
		t.Errorf("finding = %q, want the trap warned about", got)
	}

	// The agent key is the fix, and doctor stops warning once it is taken: the
	// automatic carry leaves the trap nowhere to occur.
	f.setConfig("main_dir = '" + f.MainDir + "'\nagent = 'claude'\n")
	found := findings(t, f)
	if got := has(t, found, "reaches no worktree"); got != "" {
		t.Errorf("finding = %q, want the trap gone under the agent key\nall: %v", got, found)
	}
	if got := has(t, found, "reports state through its plugin"); got != "ok" {
		t.Errorf("finding = %q, want the wired state reported ok\nall: %v", got, found)
	}
}

// TestDoctorNamesAPluginNothingIgnores covers the state agent-init can only
// warn about once: a directory treewright invented, which nothing ignores, so
// it reads as untracked in the main checkout and — being carried — in every
// worktree made after it. The sentence at install time scrolls away; the state
// does not, which is what doctor is for.
func TestDoctorNamesAPluginNothingIgnores(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")
	f.mustRun("agent-init", "claude", "--local")

	if got := has(t, findings(t, f), "git neither ignores nor tracks .claude/skills/treewright"); got != "warn" {
		t.Errorf("finding = %q, want the unignored plugin warned about", got)
	}

	// Ignoring it is one way out, and it is the user's own edit — treewright
	// writes to no .gitignore, so what doctor offers is a line to paste.
	f.Write(f.MainDir, ".gitignore", ".env\n.claude/skills/treewright/\n")
	if got := has(t, findings(t, f), "neither ignores nor tracks"); got != "" {
		t.Errorf("finding = %q, want the warning gone once the plugin is ignored", got)
	}

	// Committing it is the other, and a real choice: a team can decide everyone
	// who clones gets the wiring. Either way there is nothing left to report.
	f.Write(f.MainDir, ".gitignore", ".env\n")
	f.Git(f.MainDir, "add", ".claude/skills/treewright")
	f.Git(f.MainDir, "commit", "--quiet", "-m", "wire treewright up for everyone")
	if got := has(t, findings(t, f), "neither ignores nor tracks"); got != "" {
		t.Errorf("finding = %q, want the warning gone once the plugin is committed", got)
	}
}

// TestDoctorNoticesAPluginAnOlderTreewrightWrote is the check the settings-file
// paste made impossible. Wiring treewright owns can be compared with the wiring
// treewright would write, so a hook mapping that has moved on is a warning
// rather than an installation that looks finished and reports the wrong words.
func TestDoctorNoticesAPluginAnOlderTreewrightWrote(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")
	f.mustRun("agent-init", "claude", "--local")
	stalePlugin(t, f.MainDir)

	if got := has(t, findings(t, f), "not what this treewright would write"); got != "warn" {
		t.Errorf("finding = %q, want the stale plugin warned about", got)
	}

	// And the fix is the command it names, which is the point of naming it.
	f.mustRun("agent-init", "claude", "--local")
	if got := has(t, findings(t, f), "not what this treewright would write"); got != "" {
		t.Errorf("finding = %q, want the warning gone after the rerun", got)
	}
}

// TestDoctorNamesHooksLeftInASettingsFile covers the upgrade path: an install
// wired by an older treewright's paste still works, and doctor says so in the
// terms that matter — it is a copy nothing can update.
func TestDoctorNamesHooksLeftInASettingsFile(t *testing.T) {
	f := newFixture(t, "agent = 'claude'\n")
	settings := filepath.Join(f.MainDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"treewright signal done"}]}]}}`
	if err := os.WriteFile(settings, []byte(body), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	found := findings(t, f)
	if got := has(t, found, "are a pasted copy"); got != "warn" {
		t.Errorf("finding = %q, want the paste warned about as the thing the plugin replaces\nall: %v", got, found)
	}
	if got := has(t, found, "cannot keep them up to date"); got != "warn" {
		t.Errorf("finding = %q, want the cost of the paste in the same warning\nall: %v", got, found)
	}

	// Once the plugin is installed the two run side by side, which is worth
	// saying: the pasted half is frozen wherever it was written.
	f.mustRun("agent-init", "claude")
	if got := has(t, findings(t, f), "run alongside the plugin's"); got != "warn" {
		t.Errorf("finding = %q, want the duplicate wiring warned about", got)
	}
}

// TestDoctorSpeaksTheNameTheUserTyped holds the typed hints to the argv0
// invariant: anything a person is told to type answers in the name they use,
// and four of doctor's hints hardcoded the canonical one. The file-destined
// lines — the tmux.conf run-shell and the startup-file eval — stay spelled
// treewright on purpose, being read by programs rather than typed.
func TestDoctorSpeaksTheNameTheUserTyped(t *testing.T) {
	newFixture(t, "agent = 'claude'\n")

	var out, errOut bytes.Buffer
	// The error is not the subject: a machine without tmux fails the tmux check
	// and the typed hints are printed either way.
	_ = Run(Env{Args: []string{"doctor"}, Argv0: "tw", Stdout: &out, Stderr: &errOut})

	report := flat(out.String())
	if !strings.Contains(report, "install the plugin: tw agent-init claude") {
		t.Errorf("doctor = %q, want the agent-init hint spelled with the name the user typed", report)
	}
	if strings.Contains(report, "treewright agent-init") {
		t.Errorf("doctor spells a typed hint with the canonical name:\n%s", out.String())
	}
}
