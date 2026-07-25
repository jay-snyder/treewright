package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jay-snyder/treemux/internal/config"
	"github.com/jay-snyder/treemux/internal/git"
	"github.com/jay-snyder/treemux/internal/tmux"
	"github.com/jay-snyder/treemux/internal/ui"
)

// A working treemux is four things agreeing: the binary, a tmux server, a shell
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

func (r *report) add(l level, format string, args ...any) {
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
	if _, err := parseArgs("doctor", args, nil, 0); err != nil {
		return err
	}

	var r report
	checkTmux(&r)
	checkShellIntegration(env, &r)
	names := checkRegistry(&r)
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
		r.add(levelFail, "tmux is not on PATH — new, resume and base cannot open windows")
		return
	}
	r.add(levelOK, "tmux at %s", path)
	// Worth stating whenever it is set, because it redirects every tmux command
	// treemux runs at a different server, and a window that opened "nowhere" is
	// otherwise a mystery.
	if label := os.Getenv("TREEMUX_TMUX_LABEL"); label != "" {
		r.add(levelOK, "driving the tmux server %q, from TREEMUX_TMUX_LABEL", label)
	}

	switch session := tmux.CurrentSession(); {
	case session != "":
		r.add(levelOK, "attached to tmux session %s", session)
	case tmux.Inside():
		// $TMUX is set but the server behind it did not answer, which is what a
		// stale environment inherited from a dead session looks like.
		r.add(levelWarn, "$TMUX is set but its server does not answer — windows may open on another server")
	default:
		// Not a fault: treemux is often run from a plain shell, and windows are
		// still opened, in the repository's own session, to attach to afterwards.
		r.add(levelWarn, "not inside tmux — windows are created detached, and treemux says how to attach")
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
			r.add(levelOK, "%s: tmux session %s is running", name, session)
			continue
		}
		r.add(levelOK, "%s: tmux session %s, created by the first new, resume or base", name, session)
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
		r.add(levelWarn, "configs %s share tmux session %s — their windows will mix",
			strings.Join(owners[session], ", "), session)
	}
}

// checkShellIntegration infers whether the wrapper function is loaded from the
// eval file it sets. Nothing else can tell: the binary cannot see its parent's
// function table, so the presence of the variable the wrapper exports is the only
// evidence available.
func checkShellIntegration(env *Env, r *report) {
	if env.EvalFile != "" {
		r.add(levelOK, "shell integration loaded")
		return
	}
	shell := filepath.Base(os.Getenv("SHELL"))
	switch shell {
	case "zsh", "bash":
		r.add(levelWarn, "shell integration not loaded — add eval \"$(treemux shell-init %s)\" to your startup file, or cd and rm cannot move your shell", shell)
	case "fish":
		r.add(levelWarn, "shell integration not loaded — add treemux shell-init fish | source to your config, or cd and rm cannot move your shell")
	default:
		r.add(levelWarn, "shell integration not loaded — see \"treemux help shell-init\"; without it cd and rm cannot move your shell")
	}
}

// checkRegistry reports on the config directory and returns the names to inspect.
func checkRegistry(r *report) []string {
	dir := config.Dir()
	names, err := config.Names()
	if err != nil {
		r.add(levelFail, "cannot read %s: %v", dir, err)
		return nil
	}
	if len(names) == 0 {
		r.add(levelFail, "no configs in %s — run \"treemux setup\" inside a repository", dir)
		return nil
	}
	r.add(levelOK, "%d config(s) in %s: %s", len(names), dir, strings.Join(names, ", "))

	// A file left in the registry that treemux does not read is nearly always a
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
		r.add(levelWarn, "ignored, not a .toml file: %s", strings.Join(strays, ", "))
	}
	return names
}

func checkConfig(r *report, name string) {
	cfg, err := config.Load(filepath.Join(config.Dir(), name+".toml"))
	if err != nil {
		r.add(levelFail, "%v", err)
		return
	}

	repo := git.Repo{Dir: cfg.MainDir}
	if _, err := os.Stat(cfg.MainDir); err != nil {
		// The likeliest config fault by far: a repository that was moved or
		// renamed after being registered.
		r.add(levelFail, "%s: main_dir %s does not exist", name, cfg.MainDir)
		return
	}
	gitMain, err := repo.MainDir()
	if err != nil {
		r.add(levelFail, "%s: main_dir %s is not a git repository", name, cfg.MainDir)
		return
	}
	if gitMain != cfg.MainDir {
		// Worktrees are matched by path prefix against git's own spelling, so a
		// main_dir that disagrees with it makes every worktree invisible.
		r.add(levelFail, "%s: main_dir is %s but git calls the same repo %s", name, cfg.MainDir, gitMain)
		return
	}
	r.add(levelOK, "%s: main_dir %s", name, cfg.MainDir)

	switch {
	case !repo.HasRemote("origin"):
		r.add(levelWarn, "%s: no origin remote — new forks from the local %s instead", name, cfg.BaseBranch)
	case !repo.RefExists("origin/" + cfg.BaseBranch):
		r.add(levelWarn, "%s: origin/%s does not resolve — fetch, or fix base_branch", name, cfg.BaseBranch)
	default:
		r.add(levelOK, "%s: forks from origin/%s", name, cfg.BaseBranch)
	}

	for _, rel := range cfg.CarryFiles {
		src := filepath.Join(cfg.MainDir, rel)
		info, err := os.Stat(src)
		switch {
		case err != nil:
			r.add(levelWarn, "%s: carry_files %s is missing from %s", name, rel, cfg.MainDir)
		case info.IsDir():
			// carryFiles copies one file at a time, so a directory is silently
			// skipped at the point where it would matter.
			r.add(levelWarn, "%s: carry_files %s is a directory, which is not copied", name, rel)
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
			r.add(levelWarn, "%s: %s is blank — the window would open running nothing", name, setting.label)
			continue
		}
		if _, err := exec.LookPath(words[0]); err != nil {
			r.add(levelWarn, "%s: %s runs %q, which is not on PATH", name, setting.label, words[0])
		}
	}
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
		r.add(levelWarn, "not inside a git repository — commands here need a [repo] argument")
		return
	}
	cfg, err := config.Resolve("", mainDir)
	if err != nil {
		r.add(levelWarn, "%v", err)
		return
	}
	r.add(levelOK, "here, commands use %q", cfg.Name)
}
