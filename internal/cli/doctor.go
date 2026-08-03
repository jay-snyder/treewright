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
	"github.com/jay-snyder/treewright/internal/shellinit"
	"github.com/jay-snyder/treewright/internal/tmux"
	"github.com/jay-snyder/treewright/internal/tmuxinit"
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

// installGroup heads the install checks' section. It comes first because a
// broken installation breaks every repository, and after it comes a section per
// config — which is what took "proj: " off the front of every line that
// concerned proj. Repeated down a column it was noise; said once above the rows
// it covers, it is the thing they have in common.
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
	checkTmuxIntegration(env, &r)
	checkShellIntegration(env, &r)
	checkRelease(env, &r)
	names := checkRegistry(env, &r)
	checkCurrentRepo(&r)
	// A section per config, each holding everything about that repository —
	// which is why the session check, previously a pass of its own after every
	// config, now runs inside the loop. Only the clash it looks for spans two
	// configs, and that is reported after them all. The config is loaded once,
	// here, and handed down: checkSession used to re-run the load checkConfig
	// had just done, coupled to it only by "a failure was already reported".
	sessions := make(map[string][]string)
	for _, name := range names {
		r.in(name)
		cfg := checkConfig(env, &r, name)
		checkSession(&r, cfg, sessions)
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
		// $TMUX is set but which session this is cannot be told: a stale
		// environment inherited from a dead session, or one scrubbed of the
		// $TMUX_PANE the question is asked through. CurrentSession refuses to
		// guess in either case, and so does this finding.
		r.addf(levelWarn, "tmux session", "$TMUX is set but which session this is cannot be told\na stale environment from a dead session looks like this\nwindows may open on another server")
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
func checkTmuxIntegration(env *Env, r *report) {
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
	case bound && tmux.ServerOption(tmuxinit.VersionOption) == tmuxinit.Version():
		r.addf(levelOK, "tmux integration", "loaded")
	case bound:
		// A binding mentioning treewright and a binding this treewright wrote are
		// not the same fact, which is what "loaded" used to conflate. A tmux
		// server routinely outlives an upgrade by weeks — the keys keep working
		// the whole time, and whatever the upgrade changed about the snippet is
		// not in there. The stamp is what tells the two apart; an unstamped server
		// is one loaded by a treewright from before there was one, which is the
		// same answer.
		r.addf(levelWarn, "tmux integration", "loaded, but not by this treewright\n"+
			"the keys work — what the upgrade changed about them is not in the server\n"+
			"reload it:  %s refresh", env.Argv0)
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
// the ownership that checkSharedSessions then looks across. A nil cfg is a
// config that would not load, which checkConfig has already reported.
func checkSession(r *report, cfg *config.Config, owners map[string][]string) {
	if cfg == nil || !tmux.Available() {
		return
	}
	session := sessionFor(cfg)
	owners[session] = append(owners[session], cfg.Name)
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
//
// Which is also why the shim now exports a second variable. "A wrapper is
// loaded" was the whole of what could be asked, and it says nothing about which
// treewright emitted the one in this shell — a terminal open since two releases
// ago answers it exactly as one opened a minute ago does. The fingerprint the
// shim carries is what separates them, and any of the three shells' fingerprints
// counts: what is wanted is "one of mine", not "the one for $SHELL", which is
// the login shell rather than the running one and so answers a different
// question.
func checkShellIntegration(env *Env, r *report) {
	if env.EvalFile != "" {
		loaded := os.Getenv(shellinit.VersionVar)
		switch {
		case shellinit.Current(loaded):
			r.addf(levelOK, "shell integration", "loaded")
		default:
			// An empty variable and a stale one are one finding: both mean the
			// function in this shell came from a binary that is no longer the one
			// running, and the fix — a shell that evaluates the line again — is the
			// same. treewright cannot do it from here, a process having no way to
			// define a function in its parent.
			r.addf(levelWarn, "shell integration", "loaded, but not by this treewright\n"+
				"the wrapper in this shell is the one it started with\n"+staleShellAdvice)
		}
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

// checkRelease says whether a newer treewright has been published.
//
// The one check here that leaves the machine, and it happens because somebody
// ran doctor: an upgrade check on `new` or `ls` would be a network call in the
// middle of a command with no reason to make one. See internal/cli/release.go
// for why there is no cache and no background check either.
//
// Two of the four outcomes produce no finding at all. Being unable to reach the
// API is not a fault of the installation — an offline laptop must not come back
// from doctor with a warning about the network it is not on — and it is bounded
// by a short timeout, since doctor is what a person runs when something is
// already wrong. A build with no release version reports that rather than
// guessing: "dev" is not older than anything.
func checkRelease(env *Env, r *report) {
	switch state, latest := checkForNewerRelease(env.Version); state {
	case releaseBehind:
		r.addf(levelWarn, "release", "%s", releaseBehindNotice(latest, env.Version, releaseUpgradeAdvice()))
	case releaseCurrent:
		r.addf(levelOK, "release", "%s, the latest", latest)
	case releaseIncomparable:
		r.addf(levelOK, "release", "%s is not a released version, so nothing was compared", env.Version)
	case releaseUnreachable:
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

// checkConfig inspects one config, returning what it loaded — nil for a file
// that would not load, after reporting it — so the caller can hand the same
// parse to the session check rather than paying for a second one.
func checkConfig(env *Env, r *report, name string) *config.Config {
	cfg, err := config.Load(filepath.Join(config.Dir(), name+".toml"))
	if err != nil {
		r.addf(levelFail, "config", "%v", err)
		return nil
	}
	checkConfigVersion(env, r, cfg)

	repo := git.Repo{Dir: cfg.MainDir}
	if _, err := os.Stat(cfg.MainDir); err != nil {
		// The likeliest config fault by far: a repository that was moved or
		// renamed after being registered.
		r.addf(levelFail, "main_dir", "%s does not exist", cfg.MainDir)
		return cfg
	}
	gitMain, err := repo.MainDir()
	if err != nil {
		r.addf(levelFail, "main_dir", "%s is not a git repository", cfg.MainDir)
		return cfg
	}
	if gitMain != cfg.MainDir {
		// Worktrees are matched by path prefix against git's own spelling, so a
		// main_dir that disagrees with it makes every worktree invisible. Two
		// paths differing somewhere in the middle is a comparison, and nobody can
		// make one along a single line — so they are stacked in a column.
		r.addf(levelFail, "main_dir", "the config and git disagree about where the repo is%s",
			asFields(field("config says", cfg.MainDir), field("git says", gitMain)))
		return cfg
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
			// A setting rather than a fault: a blank command opens the window
			// on a shell, which is how a repository asks for a window with no
			// agent in it. It is still said, unlike the ordinary command that
			// merely works, because what follows from it — an AGENT column
			// that never fills and no state ever reported — is otherwise a
			// silence somebody eventually files as a bug.
			r.addf(levelOK, setting.label, "blank — the window opens a shell, and no agent reports state")
			continue
		}
		if _, err := exec.LookPath(words[0]); err != nil {
			r.addf(levelWarn, setting.label, "runs %q, which is not on PATH", words[0])
		}
	}

	checkAgentWiring(env, r, cfg)
	return cfg
}

// checkConfigVersion reports a config file written for a different revision of
// the generator than this treewright's.
//
// It is a warning and never a failure, in both directions. An old config works —
// every key it holds still means what it meant, which is the point of a format
// that has never renamed one — and what it is missing is the keys added since,
// the commentary rewritten since, and any default the generator now spells out.
// A config from a *newer* treewright works too, right up until it uses a setting
// this one has never heard of, at which point Load refuses it with the
// unknown-key error that now mentions version skew.
//
// Nothing is said when the versions agree, unlike the checks around it. A report
// is read for what is wrong with it, and "your config is the current one" is a
// line every healthy repository would carry forever to say nothing.
func checkConfigVersion(env *Env, r *report, cfg *config.Config) {
	regenerate := fmt.Sprintf("regenerate it:  %s setup --refresh %s", env.Argv0, cfg.Name)
	switch {
	case cfg.Version > config.FormatVersion:
		r.addf(levelWarn, "config version", "%d, from a newer treewright than this one\n"+
			"a setting it added would read here as a typo\n%s",
			cfg.Version, releaseUpgradeAdvice())
	case !cfg.Explicit("version"):
		// Every config in the wild predates the key, so this is the finding most
		// people see first and it has to be worth the line: what it costs is real,
		// and it is one command to answer.
		r.addf(levelWarn, "config version", "not recorded, so this file predates version %d\n"+
			"it is missing whatever setup has learned to write since\n%s",
			config.FormatVersion, regenerate)
	case cfg.Version < config.FormatVersion:
		r.addf(levelWarn, "config version", "%d, where this treewright writes %d\n"+
			"it is missing whatever setup has learned to write since\n%s",
			cfg.Version, config.FormatVersion, regenerate)
	}
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
func checkAgentWiring(env *Env, r *report, cfg *config.Config) {
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
		r.addf(levelWarn, "agent", "the hooks in %s are a pasted copy\ntreewright cannot keep them up to date\nreplace them:  %s agent-init %s",
			pasted, env.Argv0, module.Name)
	case project == pluginAbsent && user == pluginAbsent:
		r.addf(levelWarn, "agent", "%s is not wired to report state\nthe AGENT column of ls stays empty\ninstall the plugin:  %s agent-init %s",
			module.Name, env.Argv0, module.Name)
	case project == pluginStale || user == pluginStale:
		r.addf(levelWarn, "agent", "the %s plugin is not what this treewright would write\nbring it up to date:  %s agent-init %s",
			module.Name, env.Argv0, module.Name)
	case project == pluginCurrent && cfg.Agent == "" && !carriesPlugin(module, cfg):
		r.addf(levelWarn, "agent", "the %s plugin in %s reaches no worktree\nadd to the config:  agent = %q",
			module.Name, module.ProjectPlugin, module.Name)
	default:
		r.addf(levelOK, "agent", "%s reports state through its plugin", module.Name)
	}

	// Asked only of a plugin that is actually in the checkout: the default
	// placement is under the user's home directory, where no repository has an
	// opinion — nothing to ignore, and nothing to carry.
	if project != pluginAbsent {
		checkPluginIsIgnored(r, cfg, module)
		checkWorktreePlugins(env, r, cfg, module)
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

// checkPluginIsIgnored warns when the plugin in a checkout is neither ignored
// nor committed, which is the state that leaves a directory nobody created
// sitting in `git status` as untracked — in the main checkout, and, since the
// agent key carries it, in every worktree treewright makes afterwards.
//
// agent-init already says this at install time, and that is not the same thing.
// A sentence at install time is read once, by someone in the middle of
// installing; the state it warns about outlives the sentence, and doctor is
// where a half-configured repository gets asked about again. It is also where
// the question can be answered rather than assumed: agent-init says it
// unconditionally because the answer costs a git call to learn and the sentence
// is worth reading either way, while doctor is already running git and only
// reports what is actually true here.
//
// Both ways out are named because both are decisions treewright does not get to
// make. Ignoring it keeps the wiring local to whoever wants it; committing it
// hands treewright to everyone who clones, which is a real choice a team can
// make. Either way this stops warning — and treewright still writes to no
// .gitignore of anyone's, which is why the fix is a sentence rather than an
// edit.
func checkPluginIsIgnored(r *report, cfg *config.Config, module agentinit.Agent) {
	repo := git.Repo{Dir: cfg.MainDir}
	dir := module.ProjectPlugin
	if repo.Ignored(dir) || repo.Tracked(dir) {
		return
	}
	// Four lines where the findings above take three, because there are two ways
	// out rather than one: what is wrong, what it costs, the way that is a
	// decision, and the way that is a line to paste — which goes last, like
	// every other command this report names.
	r.addf(levelWarn, "agent plugin", "git neither ignores nor tracks %s\n"+
		"it reads as untracked here and in every worktree treewright makes\n"+
		"commit it to hand the wiring to everyone who clones\n"+
		"or add to .gitignore:  %s/", dir, dir)
}

// checkWorktreePlugins compares each worktree's copy of the plugin with the one
// this treewright would write.
//
// This is the gap the carry left. A worktree receives the plugin once, when
// `new` copies it in, and until now nothing ever looked at it again: doctor
// inspected the main checkout and the user-level directory and enumerated no
// worktrees at all, `agent-init` run from inside a worktree resolved the config
// and wrote to the *main* checkout, and so a worktree made the day before an
// upgrade ran its agent against the old hooks and the old skill for the rest of
// its life while the report stayed green. Rename a signal verb and every
// pre-upgrade worktree errors on every agent transition, invisibly.
//
// That is the same frozen-copy problem the plugin exists to abolish — a snapshot
// in a file treewright would never read again — reintroduced one directory
// further down, which is why it is checked here rather than left to be noticed.
//
// Two findings at most, and each carries its worktrees as a list rather than a
// finding apiece: a repository with six worktrees would otherwise turn one
// upgrade into eighteen lines of report, all of them the same sentence, and a
// report nobody reads to the end of catches nothing.
func checkWorktreePlugins(env *Env, r *report, cfg *config.Config, module agentinit.Agent) {
	managed, err := repoFor(cfg).Managed()
	if err != nil || len(managed) == 0 {
		// A repository with no worktrees is the ordinary state of a new one, and a
		// git call that failed here has already been reported by main_dir.
		return
	}

	var stale, missing []string
	for _, wt := range managed {
		switch inspectPlugin(module, filepath.Join(wt.Dir, filepath.FromSlash(module.ProjectPlugin))) {
		case pluginStale:
			stale = append(stale, wt.Slug)
		case pluginAbsent:
			missing = append(missing, wt.Slug)
		case pluginCurrent:
		}
	}

	// The subject of both findings is the plugin rather than the worktrees,
	// which is what keeps one sentence serving a repository with one worktree
	// and a repository with six: "1 worktree carries" and "2 worktrees carry"
	// would need the message to conjugate, and count() exists to stop messages
	// doing that.
	refresh := fmt.Sprintf("bring every checkout up to date:  %s refresh %s", env.Argv0, cfg.Name)
	if len(stale) > 0 {
		r.addf(levelWarn, "agent plugin", "out of date in %s%s\n"+
			"an agent there runs whatever wiring its copy was made from\n%s",
			count(len(stale), "worktree", "worktrees"), asLines(stale), refresh)
	}
	// Missing is only worth a finding where the config carries the plugin, since
	// that is what makes an empty worktree wrong rather than merely unwired: it
	// is a worktree older than the carry. Without one, "the plugin reaches no
	// worktree" is already the finding above, said once about the repository
	// instead of once per worktree.
	if len(missing) > 0 && (cfg.Agent == module.Name || carriesPlugin(module, cfg)) {
		r.addf(levelWarn, "agent plugin", "missing from %s%s\n"+
			"an agent there reports nothing and knows nothing about treewright\n%s",
			count(len(missing), "worktree", "worktrees"), asLines(missing), refresh)
	}
	if len(stale) == 0 && len(missing) == 0 {
		r.addf(levelOK, "agent plugin", "current in %s", count(len(managed), "worktree", "worktrees"))
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
