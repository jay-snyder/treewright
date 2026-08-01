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

// finding is one line of doctor's report: how much it matters, which group it
// belongs under, the thing being checked, and what was found.
//
// The check is its own field rather than the first words of the detail because
// it is what a reader scans for. Told that the shell integration is missing,
// they come back to this report looking for the word "shell" — and used to find
// it somewhere inside the seventh sentence, at whatever column that sentence
// happened to reach.
type finding struct {
	level  level
	group  string
	check  string
	detail string // may span lines
}

// report collects findings so the whole picture is printed at once, rather than
// stopping at the first problem — the second problem is usually the interesting
// one, and a checklist is what makes the output worth re-reading after a fix.
type report struct {
	findings []finding
	group    string
	failed   bool
}

// groupHeadings are the sections, in the order they are printed. The install
// checks come first because a broken one of those breaks every repository, and
// then a section per config — which is what took "proj: " off the front of
// every line that concerned proj. Repeated down a column it was noise; said
// once above the rows it covers, it is the thing they have in common.
const installGroup = "installation"

// in opens a section. Every finding added after it belongs to that section,
// until the next one.
func (r *report) in(group string) { r.group = group }

func (r *report) addf(l level, check, format string, args ...any) {
	r.findings = append(r.findings, finding{
		level:  l,
		group:  r.group,
		check:  check,
		detail: fmt.Sprintf(format, args...),
	})
	if l == levelFail {
		r.failed = true
	}
}

// render prints the report: a heading per group, and under it the findings as
// three columns — level, check, detail.
//
// One table across every group rather than one per group, so the check column
// lands in the same place all the way down the report. A table per group would
// size each to its own longest check name and leave the reader's eye stepping
// in and out at every heading.
func (r *report) render(env *Env) {
	color := ui.ColorEnabled(env.Stdout)

	table := ui.Table{Headers: []string{"", "", ""}}
	for _, f := range r.findings {
		table.Add(ui.Colored(f.level.label(), f.level.color()), ui.Text(f.check), ui.Text(f.detail))
	}
	// Lines rather than Render, so the blank header is not printed as an empty
	// first line — and so the group headings can be threaded between the rows,
	// which is the one thing the table cannot do for itself.
	_, rows := table.Lines(color)

	group := ""
	for i, f := range r.findings {
		if f.group != group {
			if group != "" {
				fmt.Fprintln(env.Stdout)
			}
			group = f.group
			fmt.Fprintln(env.Stdout, ui.Dim.Apply(group, color))
		}
		// Indented under the heading, every line of the row: a finding that runs
		// to several lines is one row here, and half of it left at the margin
		// would read as belonging to the section rather than to the finding.
		for line := range strings.SplitSeq(rows[i], "\n") {
			fmt.Fprintln(env.Stdout, "  "+line)
		}
	}

	fmt.Fprintln(env.Stdout)
	fmt.Fprintln(env.Stdout, r.summary(color))
}

// summary counts what was found, so a report read in a hurry answers its own
// question. Ten green lines and two yellow ones in the middle is exactly the
// shape an eye slides off, and the count is what stops it.
func (r *report) summary(color bool) string {
	warnings, failures := 0, 0
	for _, f := range r.findings {
		switch f.level {
		case levelWarn:
			warnings++
		case levelFail:
			failures++
		case levelOK:
		}
	}
	switch {
	case failures > 0:
		return ui.Red.Apply(count(failures, "failure", "failures"), color) +
			", " + count(warnings, "warning", "warnings")
	case warnings > 0:
		return ui.Yellow.Apply(count(warnings, "warning", "warnings"), color) + ", nothing failed"
	default:
		return ui.Green.Apply("everything checks out", color)
	}
}

func cmdDoctor(env *Env, args []string) error {
	if _, err := parseArgs("doctor", args, nil, nil, 0); err != nil {
		return err
	}

	var r report
	r.in(installGroup)
	checkTmux(&r)
	checkTmuxIntegration(&r)
	checkShellIntegration(env, &r)
	names := checkRegistry(env, &r)
	checkCurrentRepo(&r)
	// A section per config, each holding everything about that repository —
	// which is why the session check, previously a pass of its own after every
	// config, now runs inside the loop. Only the clash it looks for spans two
	// configs, and that is reported after them all.
	sessions := make(map[string][]string)
	for _, name := range names {
		r.in(name)
		checkConfig(&r, name)
		checkSession(&r, name, sessions)
	}
	r.in(installGroup)
	checkSharedSessions(&r, sessions)

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
		r.addf(levelFail, "tmux", "not on PATH\nnew, resume and base cannot open windows without it")
		return
	}
	r.addf(levelOK, "tmux", "%s", path)
	// Worth stating whenever it is set, because it redirects every tmux command
	// treewright runs at a different server, and a window that opened "nowhere" is
	// otherwise a mystery.
	if label := os.Getenv("TREEWRIGHT_TMUX_LABEL"); label != "" {
		r.addf(levelOK, "tmux server", "%s, named by TREEWRIGHT_TMUX_LABEL", label)
	}

	switch session := tmux.CurrentSession(); {
	case session != "":
		r.addf(levelOK, "tmux session", "attached to %s", session)
	case tmux.Inside():
		// $TMUX is set but the server behind it did not answer, which is what a
		// stale environment inherited from a dead session looks like.
		r.addf(levelWarn, "tmux session", "$TMUX is set but its server does not answer\nwindows may open on another server")
	default:
		// Not a fault: treewright is often run from a plain shell, and windows are
		// still opened, in the repository's own session, to attach to afterwards.
		r.addf(levelWarn, "tmux session", "not inside tmux\nwindows are created detached, and treewright says how to attach")
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
		r.addf(levelOK, "tmux integration", "loaded")
	default:
		// Three lines because there are three things here: what is wrong, what it
		// costs, and the line that fixes it. The line to paste goes last, where a
		// reader who has decided to act can find it without reading past it
		// twice — it used to sit in the middle of the sentence.
		r.addf(levelWarn, "tmux integration", "not loaded\n"+
			"no key reaches treewright from a window running an agent\n"+
			"add to tmux.conf:  run-shell 'treewright tmux-init --apply'")
	}
}

// checkSession reports which session a repository's windows go to, recording
// the ownership that checkSharedSessions then looks across.
func checkSession(r *report, name string, owners map[string][]string) {
	if !tmux.Available() {
		return
	}
	cfg, err := config.Load(filepath.Join(config.Dir(), name+".toml"))
	if err != nil {
		return // already reported by checkConfig
	}
	session := sessionFor(cfg)
	owners[session] = append(owners[session], name)
	if tmux.HasSession(session) {
		r.addf(levelOK, "tmux session", "%s, running", session)
		return
	}
	// "created by the first new" read as a sentence about newness rather than
	// about the command, which is what naming the commands as a list fixes.
	r.addf(levelOK, "tmux session", "%s — not running yet\nthe first new, resume or base creates it", session)
}

// checkSharedSessions catches the one way a session can be got wrong: two
// configs naming one, which puts two repositories' windows back in the same
// status line — the thing a session per repository exists to prevent.
func checkSharedSessions(r *report, owners map[string][]string) {
	// Sorted, so that a report read twice reads the same way.
	shared := make([]string, 0, len(owners))
	for session, configs := range owners {
		if len(configs) > 1 {
			shared = append(shared, session)
		}
	}
	sort.Strings(shared)
	for _, session := range shared {
		r.addf(levelWarn, "tmux session", "%s is shared, so its windows will mix%s",
			session, asLines(owners[session]))
	}
}

// checkShellIntegration infers whether the wrapper function is loaded from the
// eval file it sets. Nothing else can tell: the binary cannot see its parent's
// function table, so the presence of the variable the wrapper exports is the only
// evidence available.
func checkShellIntegration(env *Env, r *report) {
	if env.EvalFile != "" {
		r.addf(levelOK, "shell integration", "loaded")
		return
	}
	// The same three lines the tmux check uses, and in the same order: what is
	// wrong, what it costs, then the line to paste.
	const notLoaded = "not loaded\ncd and rm cannot move your shell\n"

	shell := filepath.Base(os.Getenv("SHELL"))
	switch shell {
	case "zsh", "bash":
		r.addf(levelWarn, "shell integration", notLoaded+"add to your startup file:  eval \"$(treewright shell-init %s)\"", shell)
	case "fish":
		r.addf(levelWarn, "shell integration", notLoaded+"add to your config:  treewright shell-init fish | source")
	default:
		r.addf(levelWarn, "shell integration", notLoaded+"see \"%s help shell-init\" for the line your shell wants", env.Argv0)
	}
}

// checkRegistry reports on the config directory and returns the names to inspect.
func checkRegistry(env *Env, r *report) []string {
	dir := config.Dir()
	names, err := config.Names()
	if err != nil {
		r.addf(levelFail, "registry", "cannot read %s\n%v", dir, err)
		return nil
	}
	if len(names) == 0 {
		r.addf(levelFail, "registry", "no configs in %s\nrun \"%s setup\" inside a repository", dir, env.Argv0)
		return nil
	}
	r.addf(levelOK, "registry", "%s in %s%s", count(len(names), "config", "configs"), dir, asLines(names))

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
		r.addf(levelWarn, "registry", "ignored, not a .toml file:%s", asLines(strays))
	}
	return names
}

func checkConfig(r *report, name string) {
	cfg, err := config.Load(filepath.Join(config.Dir(), name+".toml"))
	if err != nil {
		r.addf(levelFail, "config", "%v", err)
		return
	}

	repo := git.Repo{Dir: cfg.MainDir}
	if _, err := os.Stat(cfg.MainDir); err != nil {
		// The likeliest config fault by far: a repository that was moved or
		// renamed after being registered.
		r.addf(levelFail, "main_dir", "%s does not exist", cfg.MainDir)
		return
	}
	gitMain, err := repo.MainDir()
	if err != nil {
		r.addf(levelFail, "main_dir", "%s is not a git repository", cfg.MainDir)
		return
	}
	if gitMain != cfg.MainDir {
		// Worktrees are matched by path prefix against git's own spelling, so a
		// main_dir that disagrees with it makes every worktree invisible. Two
		// paths differing somewhere in the middle is a comparison, and nobody can
		// make one along a single line — so they are stacked in a column.
		r.addf(levelFail, "main_dir", "the config and git disagree about where the repo is%s",
			asFields(field("config says", cfg.MainDir), field("git says", gitMain)))
		return
	}
	r.addf(levelOK, "main_dir", "%s", cfg.MainDir)

	switch {
	case !repo.HasRemote("origin"):
		r.addf(levelWarn, "origin", "no origin remote\nnew forks from the local %s instead", cfg.BaseBranch)
	case !repo.RefExists("origin/" + cfg.BaseBranch):
		r.addf(levelWarn, "origin", "origin/%s does not resolve\nfetch, or fix base_branch", cfg.BaseBranch)
	default:
		r.addf(levelOK, "origin", "forks from origin/%s", cfg.BaseBranch)
	}

	for _, rel := range cfg.CarryFiles {
		src := filepath.Join(cfg.MainDir, rel)
		info, err := os.Stat(src)
		switch {
		case err != nil:
			r.addf(levelWarn, "carry_files", "%s is missing from %s", rel, cfg.MainDir)
		case info.IsDir():
			// carryFiles copies one file at a time, so a directory is silently
			// skipped at the point where it would matter.
			r.addf(levelWarn, "carry_files", "%s is a directory, which is not copied", rel)
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
			r.addf(levelWarn, setting.label, "blank — the window would open running nothing")
			continue
		}
		if _, err := exec.LookPath(words[0]); err != nil {
			r.addf(levelWarn, setting.label, "runs %q, which is not on PATH", words[0])
		}
	}

	checkAgentWiring(r, cfg)
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
func checkAgentWiring(r *report, cfg *config.Config) {
	module, ok := agentModuleFor(cfg)
	if !ok || len(module.Plugin) == 0 {
		return
	}
	projectDir := filepath.Join(cfg.MainDir, filepath.FromSlash(module.ProjectPlugin))
	project, user := inspectPlugin(module, projectDir), inspectPlugin(module, expandHome(module.UserPlugin))
	pasted := settingsWithPastedHooks(module, cfg)

	// Each of these is a finding, what it costs, and the command that answers
	// it — one per line, in that order. Run together they were the longest lines
	// doctor printed, and the command, the only part anyone types back, was
	// buried in the middle of every one of them.
	switch {
	case project == pluginAbsent && user == pluginAbsent && pasted != "":
		// The fragment older versions printed for pasting still works — Claude
		// Code runs hooks from every scope it loads — so this is not "no
		// wiring". It is wiring treewright cannot keep current, which is the
		// whole reason the plugin exists.
		r.addf(levelWarn, "agent", "the hooks in %s are a pasted copy\ntreewright cannot keep them up to date\nreplace them:  treewright agent-init %s",
			pasted, module.Name)
	case project == pluginAbsent && user == pluginAbsent:
		r.addf(levelWarn, "agent", "%s is not wired to report state\nthe AGENT column of ls stays empty\ninstall the plugin:  treewright agent-init %s",
			module.Name, module.Name)
	case project == pluginStale || user == pluginStale:
		r.addf(levelWarn, "agent", "the %s plugin is not what this treewright would write\nbring it up to date:  treewright agent-init %s",
			module.Name, module.Name)
	case project == pluginCurrent && cfg.Agent == "" && !carriesPlugin(module, cfg):
		r.addf(levelWarn, "agent", "the %s plugin in %s reaches no worktree\nadd to the config:  agent = %q",
			module.Name, module.ProjectPlugin, module.Name)
	default:
		r.addf(levelOK, "agent", "%s reports state through its plugin", module.Name)
	}

	// Said as well as, not instead of, whatever the plugin's own state was: the
	// two sets of hooks both fire, and the pasted ones are the copy frozen at
	// whichever treewright printed them — a verb renamed since is an error
	// message on every transition, from a file the user has long stopped
	// thinking of as treewright's.
	if pasted != "" && (project != pluginAbsent || user != pluginAbsent) {
		r.addf(levelWarn, "agent", "the hooks pasted in %s run alongside the plugin's\ndelete them — the plugin is the copy treewright keeps current", pasted)
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
		r.addf(levelWarn, "here", "not inside a git repository\ncommands here need a [repo] argument")
		return
	}
	cfg, err := config.Resolve("", mainDir)
	if err != nil {
		r.addf(levelWarn, "here", "%v", err)
		return
	}
	r.addf(levelOK, "here", "commands use %q", cfg.Name)
}
