package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/jay-snyder/treewright/internal/agentinit"
	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/tmux"
	"github.com/jay-snyder/treewright/internal/ui"
)

// A working treewright is four things agreeing: the binary, a tmux server, a shell
// integration loaded into the current shell, and a config whose paths still point
// at something. Any one of them can be wrong in a way that shows up much later as
// behavior nobody asked for — a command that silently does nothing, a worktree
// missing the .env file it needs. doctor checks all four in one pass.

// level is how much a finding matters.
type level int

const (
	levelOK   level = iota // working
	levelWarn              // works, but something is degraded or unused
	levelFail              // does not work
)

func (l level) label() string {
	switch l {
	case levelFail:
		return "fail"
	case levelWarn:
		return "warn"
	default:
		return "ok"
	}
}

func (l level) color() ui.Color {
	switch l {
	case levelFail:
		return ui.Red
	case levelWarn:
		return ui.Yellow
	default:
		return ui.Green
	}
}

// report collects findings so the whole picture is printed at once, rather than
// stopping at the first problem — the second problem is usually the interesting
// one, and a checklist is what makes the output worth re-reading after a fix.
type report struct {
	rows   [][2]string // label, detail, in the order found
	levels []level
	failed bool
}

func (r *report) addf(l level, format string, args ...any) {
	r.rows = append(r.rows, [2]string{l.label(), fmt.Sprintf(format, args...)})
	r.levels = append(r.levels, l)
	if l == levelFail {
		r.failed = true
	}
}

func (r *report) render(env *Env) {
	table := ui.Table{Headers: []string{"", ""}}
	for i, row := range r.rows {
		table.Add(ui.Colored(row[0], r.levels[i].color()), ui.Text(row[1]))
	}
	// Headers are empty because the columns need no naming: a status word and a
	// sentence. Lines is used rather than Render so the blank header is not
	// printed as an empty first line.
	_, rows := table.Lines(ui.ColorEnabled(env.Stdout))
	for _, line := range rows {
		fmt.Fprintln(env.Stdout, line)
	}
}

func cmdDoctor(env *Env, args []string) error {
	if _, err := parseArgs("doctor", args, nil, nil, 0); err != nil {
		return err
	}

	var r report
	checkTmux(&r)
	checkTmuxIntegration(&r)
	checkShellIntegration(env, &r)
	names := checkRegistry(env, &r)
	for _, name := range names {
		checkConfig(&r, name)
	}
	checkSessions(&r, names)
	checkCurrentRepo(&r)

	r.render(env)
	if r.failed {
		// Reported in full already; the exit code is for a setup script gating on
		// it, and repeating the failures as an error message would only duplicate
		// what was just printed.
		return ErrSilent
	}
	return nil
}

func checkTmux(r *report) {
	path, err := exec.LookPath("tmux")
	if err != nil {
		r.addf(levelFail, "tmux is not on PATH — new, resume and base cannot open windows")
		return
	}
	r.addf(levelOK, "tmux at %s", path)
	// Worth stating whenever it is set, because it redirects every tmux command
	// treewright runs at a different server, and a window that opened "nowhere" is
	// otherwise a mystery.
	if label := os.Getenv("TREEWRIGHT_TMUX_LABEL"); label != "" {
		r.addf(levelOK, "driving the tmux server %q, from TREEWRIGHT_TMUX_LABEL", label)
	}

	switch session := tmux.CurrentSession(); {
	case session != "":
		r.addf(levelOK, "attached to tmux session %s", session)
	case tmux.Inside():
		// $TMUX is set but the server behind it did not answer, which is what a
		// stale environment inherited from a dead session looks like.
		r.addf(levelWarn, "$TMUX is set but its server does not answer — windows may open on another server")
	default:
		// Not a fault: treewright is often run from a plain shell, and windows are
		// still opened, in the repository's own session, to attach to afterwards.
		r.addf(levelWarn, "not inside tmux — windows are created detached, and treewright says how to attach")
	}
}

// checkTmuxIntegration reports whether the tmux-side integration is loaded.
//
// Unlike the shell one, which can only be inferred from the variable its wrapper
// exports, this can simply be asked: a key binding is a thing the server holds.
// It matters most in the case it is hardest to notice — a window running an agent
// has no shell in it, so without a binding there is no way to reach treewright from
// inside a worktree at all.
func checkTmuxIntegration(r *report) {
	if !tmux.Available() {
		return // already reported as a failure by checkTmux
	}
	// Asked only once a server is known to be up, and in that order deliberately.
	// Key bindings live in a server, so with none running there is nothing loaded
	// and nothing to load into — and the command that would ask, list-keys, starts
	// one, which would have doctor create the very emptiness it then reported.
	if !tmux.ServerRunning() {
		return
	}
	bound, err := tmux.HasBindings()
	switch {
	case err != nil:
		// The server stopped between the two questions. Nothing worth saying.
		return
	case bound:
		r.addf(levelOK, "tmux integration loaded")
	default:
		r.addf(levelWarn, "tmux integration not loaded — add run-shell 'treewright tmux-init --apply' to your tmux.conf, or no key reaches treewright from a window running an agent")
	}
}

// checkSessions reports which session each repository's windows go to, and
// catches the one way that can be got wrong: two configs naming one session,
// which puts two repositories' windows back in the same status line — the thing
// a session per repository exists to prevent.
func checkSessions(r *report, names []string) {
	if !tmux.Available() {
		return
	}
	owners := make(map[string][]string)
	for _, name := range names {
		cfg, err := config.Load(filepath.Join(config.Dir(), name+".toml"))
		if err != nil {
			continue // already reported by checkConfig
		}
		session := sessionFor(cfg)
		owners[session] = append(owners[session], name)
		if tmux.HasSession(session) {
			r.addf(levelOK, "%s: tmux session %s is running", name, session)
			continue
		}
		r.addf(levelOK, "%s: tmux session %s, created by the first new, resume or base", name, session)
	}

	// Sorted, so that a report read twice reads the same way.
	shared := make([]string, 0, len(owners))
	for session, configs := range owners {
		if len(configs) > 1 {
			shared = append(shared, session)
		}
	}
	sort.Strings(shared)
	for _, session := range shared {
		r.addf(levelWarn, "configs %s share tmux session %s — their windows will mix",
			strings.Join(owners[session], ", "), session)
	}
}

// checkShellIntegration infers whether the wrapper function is loaded from the
// eval file it sets. Nothing else can tell: the binary cannot see its parent's
// function table, so the presence of the variable the wrapper exports is the only
// evidence available.
func checkShellIntegration(env *Env, r *report) {
	if env.EvalFile != "" {
		r.addf(levelOK, "shell integration loaded")
		return
	}
	shell := filepath.Base(os.Getenv("SHELL"))
	switch shell {
	case "zsh", "bash":
		r.addf(levelWarn, "shell integration not loaded — add eval \"$(treewright shell-init %s)\" to your startup file, or cd and rm cannot move your shell", shell)
	case "fish":
		r.addf(levelWarn, "shell integration not loaded — add treewright shell-init fish | source to your config, or cd and rm cannot move your shell")
	default:
		r.addf(levelWarn, "shell integration not loaded — see \"%s help shell-init\"; without it cd and rm cannot move your shell", env.Argv0)
	}
}

// checkRegistry reports on the config directory and returns the names to inspect.
func checkRegistry(env *Env, r *report) []string {
	dir := config.Dir()
	names, err := config.Names()
	if err != nil {
		r.addf(levelFail, "cannot read %s: %v", dir, err)
		return nil
	}
	if len(names) == 0 {
		r.addf(levelFail, "no configs in %s — run \"%s setup\" inside a repository", dir, env.Argv0)
		return nil
	}
	r.addf(levelOK, "%d config(s) in %s: %s", len(names), dir, strings.Join(names, ", "))

	// A file left in the registry that treewright does not read is nearly always a
	// config the user believes is in force — a rename half-done, or a config from
	// an older version in another format.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return names
	}
	var strays []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".toml") {
			strays = append(strays, e.Name())
		}
	}
	if len(strays) > 0 {
		r.addf(levelWarn, "ignored, not a .toml file: %s", strings.Join(strays, ", "))
	}
	return names
}

func checkConfig(r *report, name string) {
	cfg, err := config.Load(filepath.Join(config.Dir(), name+".toml"))
	if err != nil {
		r.addf(levelFail, "%v", err)
		return
	}

	repo := git.Repo{Dir: cfg.MainDir}
	if _, err := os.Stat(cfg.MainDir); err != nil {
		// The likeliest config fault by far: a repository that was moved or
		// renamed after being registered.
		r.addf(levelFail, "%s: main_dir %s does not exist", name, cfg.MainDir)
		return
	}
	gitMain, err := repo.MainDir()
	if err != nil {
		r.addf(levelFail, "%s: main_dir %s is not a git repository", name, cfg.MainDir)
		return
	}
	if gitMain != cfg.MainDir {
		// Worktrees are matched by path prefix against git's own spelling, so a
		// main_dir that disagrees with it makes every worktree invisible.
		r.addf(levelFail, "%s: main_dir is %s but git calls the same repo %s", name, cfg.MainDir, gitMain)
		return
	}
	r.addf(levelOK, "%s: main_dir %s", name, cfg.MainDir)

	switch {
	case !repo.HasRemote("origin"):
		r.addf(levelWarn, "%s: no origin remote — new forks from the local %s instead", name, cfg.BaseBranch)
	case !repo.RefExists("origin/" + cfg.BaseBranch):
		r.addf(levelWarn, "%s: origin/%s does not resolve — fetch, or fix base_branch", name, cfg.BaseBranch)
	default:
		r.addf(levelOK, "%s: forks from origin/%s", name, cfg.BaseBranch)
	}

	for _, rel := range cfg.CarryFiles {
		src := filepath.Join(cfg.MainDir, rel)
		info, err := os.Stat(src)
		switch {
		case err != nil:
			r.addf(levelWarn, "%s: carry_files %s is missing from %s", name, rel, cfg.MainDir)
		case info.IsDir():
			// carryFiles copies one file at a time, so a directory is silently
			// skipped at the point where it would matter.
			r.addf(levelWarn, "%s: carry_files %s is a directory, which is not copied", name, rel)
		}
	}

	// Only the first word can be checked: the value is a shell command line, and
	// anything further would mean parsing the shell. Listed as a slice rather than
	// a map so the output does not reorder itself between runs.
	for _, setting := range []struct{ label, command string }{
		{"command", cfg.Command},
		{"resume_command", cfg.ResumeCommand},
	} {
		words := strings.Fields(setting.command)
		if len(words) == 0 {
			r.addf(levelWarn, "%s: %s is blank — the window would open running nothing", name, setting.label)
			continue
		}
		if _, err := exec.LookPath(words[0]); err != nil {
			r.addf(levelWarn, "%s: %s runs %q, which is not on PATH", name, setting.label, words[0])
		}
	}

	checkAgentWiring(r, name, cfg)
}

// pluginState is how much of a module's plugin is present at a directory.
type pluginState int

const (
	pluginAbsent  pluginState = iota // nothing of it is there
	pluginStale                      // some of it is, and it is not what treewright would write
	pluginCurrent                    // every file, byte for byte
)

// checkAgentWiring reports whether the agent this config runs is wired to
// `signal` — the integration whose absence is otherwise invisible: everything
// works, and the AGENT column simply never appears.
//
// The module is the config's `agent` key when it sets one, else the one whose
// command matches the config's first word — a guess, but a warn-level hint may
// rest on a guess in a way behavior never would. The plugin counts wherever it
// is: the agent's user-level skills directory covers every repository, and the
// main checkout's covers this one — provided it reaches the worktrees, which is
// the second check. A plugin in a gitignored directory with nothing carrying it
// fires in the MAIN window and in no worktree at all: the half-configured state
// that looks finished, which is what doctor is for.
//
// The staleness check is the one this command could not make before. When the
// wiring was a fragment the user pasted into their own settings file, all
// doctor could ask was whether some hook mentioned `signal` — so a copy pasted
// two releases ago, wired to a verb that has since been renamed, read exactly
// like a current one. The plugin is treewright's own to write, so the question
// becomes whether the files are what agent-init would write today, which is a
// byte comparison and admits no false positives.
func checkAgentWiring(r *report, name string, cfg *config.Config) {
	module, ok := agentModuleFor(cfg)
	if !ok || len(module.Plugin) == 0 {
		return
	}
	projectDir := filepath.Join(cfg.MainDir, filepath.FromSlash(module.ProjectPlugin))
	project, user := inspectPlugin(module, projectDir), inspectPlugin(module, expandHome(module.UserPlugin))
	pasted := settingsWithPastedHooks(module, cfg)

	switch {
	case project == pluginAbsent && user == pluginAbsent && pasted != "":
		// The fragment older versions printed for pasting still works — Claude
		// Code runs hooks from every scope it loads — so this is not "no
		// wiring". It is wiring treewright cannot keep current, which is the
		// whole reason the plugin exists.
		r.addf(levelWarn, "%s: the hooks in %s are a pasted copy treewright cannot update — \"treewright agent-init %s\" installs the plugin that replaces them",
			name, pasted, module.Name)
	case project == pluginAbsent && user == pluginAbsent:
		r.addf(levelWarn, "%s: %s does not report state — \"treewright agent-init %s\" installs the plugin that fills the AGENT column",
			name, module.Name, module.Name)
	case project == pluginStale || user == pluginStale:
		r.addf(levelWarn, "%s: the %s plugin is not what this treewright would write — rerun \"treewright agent-init %s\" to bring the wiring up to date",
			name, module.Name, module.Name)
	case project == pluginCurrent && cfg.Agent == "" && !carriesPlugin(module, cfg):
		r.addf(levelWarn, "%s: the %s plugin in %s reaches no worktree — set agent = %q so every new one gets a copy",
			name, module.Name, module.ProjectPlugin, module.Name)
	default:
		r.addf(levelOK, "%s: %s reports state through its plugin", name, module.Name)
	}

	// Said as well as, not instead of, whatever the plugin's own state was: the
	// two sets of hooks both fire, and the pasted ones are the copy frozen at
	// whichever treewright printed them — a verb renamed since is an error
	// message on every transition, from a file the user has long stopped
	// thinking of as treewright's.
	if pasted != "" && (project != pluginAbsent || user != pluginAbsent) {
		r.addf(levelWarn, "%s: the hooks pasted in %s run alongside the plugin's — delete them, the plugin is the copy treewright keeps current", name, pasted)
	}
}

// inspectPlugin reads what is installed at dir and compares it with what
// agent-init would put there.
//
// A partial install is stale rather than absent: a plugin missing its hooks is
// the state an interrupted write or a hand-copied directory leaves, and it is
// the one worth naming — the skill loads, so everything looks installed, and
// the AGENT column never appears.
func inspectPlugin(module agentinit.Agent, dir string) pluginState {
	found, current := 0, 0
	for _, f := range module.Plugin {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			continue
		}
		found++
		if string(body) == f.Body {
			current++
		}
	}
	switch {
	case found == 0:
		return pluginAbsent
	case current == len(module.Plugin):
		return pluginCurrent
	default:
		return pluginStale
	}
}

// carriesPlugin reports whether carry_files names every file of the plugin,
// which is the long-hand alternative to the agent key. Every file, because a
// worktree that got the skill and not the hooks is the half-installed state
// this check exists to catch.
func carriesPlugin(module agentinit.Agent, cfg *config.Config) bool {
	for _, rel := range module.LocalState() {
		if rel != module.ProjectSettings && !slices.Contains(cfg.CarryFiles, rel) {
			return false
		}
	}
	return true
}

// settingsWithPastedHooks names the settings file still carrying hooks an older
// treewright printed for pasting, or "" when neither does.
func settingsWithPastedHooks(module agentinit.Agent, cfg *config.Config) string {
	for _, path := range []string{
		filepath.Join(cfg.MainDir, filepath.FromSlash(module.ProjectSettings)),
		expandHome(module.UserSettings),
	} {
		if mentionsSignal(path) {
			return path
		}
	}
	return ""
}

// agentModuleFor resolves which agent module a config runs: the `agent` key
// when set — Load has already proven it resolves — else the module whose
// command shares the config's first word.
func agentModuleFor(cfg *config.Config) (agentinit.Agent, bool) {
	if cfg.Agent != "" {
		return agentinit.Lookup(cfg.Agent)
	}
	words := strings.Fields(cfg.Command)
	if len(words) == 0 {
		return agentinit.Agent{}, false
	}
	for _, name := range agentinit.Names() {
		module, _ := agentinit.Lookup(name)
		if moduleWords := strings.Fields(module.Command); len(moduleWords) > 0 && moduleWords[0] == words[0] {
			return module, true
		}
	}
	return agentinit.Agent{}, false
}

// mentionsSignal reports whether a settings file wires anything to
// `treewright signal`. A substring test rather than a parse, deliberately: the
// file is another program's, its schema is not treewright's to validate, and
// the question is only whether the wiring exists at all. A missing file is a
// plain no.
func mentionsSignal(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(body), "treewright signal")
}

// expandHome resolves the leading ~ the agent modules spell their user-level
// paths with, being paths shown to users as often as read.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
		}
	}
	return path
}

// checkCurrentRepo reports which config the caller's location selects, since that
// is the question every command answers implicitly and none of them shows.
func checkCurrentRepo(r *report) {
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	mainDir, err := git.Repo{Dir: wd}.MainDir()
	if err != nil {
		r.addf(levelWarn, "not inside a git repository — commands here need a [repo] argument")
		return
	}
	cfg, err := config.Resolve("", mainDir)
	if err != nil {
		r.addf(levelWarn, "%v", err)
		return
	}
	r.addf(levelOK, "here, commands use %q", cfg.Name)
}
