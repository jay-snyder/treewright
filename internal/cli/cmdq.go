package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/plugin/cmdq"
	"github.com/jay-snyder/treewright/internal/tmux"
	"github.com/jay-snyder/treewright/internal/ui"
)

// The cmdq plugin: commands an agent needs the person to run — a sudo, an
// interactive login — queued for them, with a list in a popup to run them
// from. docs/plugins.md is the design, and the rules every plugin is held to.
//
// This file is the half of the plugin that asks treewright: who is asking (the
// agent's name, off its window), which key opens the list, whether the guard
// would let the command through, the popup's size and doctor's report.
// internal/plugin/cmdq is the half that keeps the queue and runs what is in
// it, and knows nothing about worktrees or windows.

// Sub-commands of cmdq. The bare command is the list, which is what a person
// types and what the popup runs; these are what an agent runs.
const (
	cmdqCommand = "cmdq"
	cmdqAdd     = "add"
	cmdqWait    = "wait"
	cmdqStatus  = "status"
	cmdqDrop    = "drop"
	cmdqList    = "list"
	cmdqHistory = "history"
)

// cmdqSubcommands is the list completion offers after "cmdq", in the order
// help names them.
var cmdqSubcommands = []string{cmdqAdd, cmdqWait, cmdqStatus, cmdqDrop, cmdqList, cmdqHistory}

// The flags of cmdq: three for add and one for wait, in one set for the reason
// reviews gives — help documents a command's flags in one list.
const (
	whyFlag     = "--why"
	fromFlag    = "--from"
	cwdFlag     = "--cwd"
	timeoutFlag = "--timeout"
)

// waitLimit is how long wait waits unless told otherwise, and waitEvery how
// often it looks. A variable so the tests can look more often than a person
// would want a process to.
var (
	waitLimit = time.Hour
	waitEvery = 2 * time.Second
)

// maxCommand bounds what add reads from stdin. A queued command is something
// a person reads before running; one longer than this is not, and reading
// stdin without a bound is how a mistaken pipe fills the queue's disk.
const maxCommand = 1 << 20

func cmdCmdq(env *Env, args []string) error {
	args, command, hasCommand := splitCommand(args)
	var why, from, cwd, timeout string
	positional, err := parseArgs(cmdqCommand, args, nil, map[string]*string{
		whyFlag: &why, fromFlag: &from, cwdFlag: &cwd, timeoutFlag: &timeout,
	}, 2)
	if err != nil {
		return err
	}
	sub, id := at(positional, 0), at(positional, 1)
	given := func(names ...string) []string { return flagsGiven(args, names...) }
	if hasCommand && sub != cmdqAdd {
		return usageErrorf(cmdqCommand, "only add takes a command after --")
	}
	store := cmdq.Store{Dir: cmdq.Dir()}

	switch sub {
	case "":
		if extra := given(whyFlag, fromFlag, cwdFlag, timeoutFlag); len(extra) > 0 {
			return usageErrorf(cmdqCommand, "%s is for a sub-command, not the list", extra[0])
		}
		return showQueue(env, store)
	case cmdqAdd:
		if extra := given(timeoutFlag); len(extra) > 0 {
			return usageErrorf(cmdqCommand, "%s is for wait", timeoutFlag)
		}
		if id != "" {
			return usageErrorf(cmdqCommand, "the command goes after --, or on stdin — so nothing in it is read as a flag")
		}
		return queueCommand(env, store, queueRequest{
			why: why, from: from, fromGiven: len(given(fromFlag)) > 0, cwd: cwd,
			command: command, hasCommand: hasCommand,
		})
	case cmdqWait, cmdqStatus, cmdqDrop:
		allowed := []string{}
		if sub == cmdqWait {
			allowed = []string{timeoutFlag}
		}
		for _, f := range []string{whyFlag, fromFlag, cwdFlag, timeoutFlag} {
			if !slices.Contains(allowed, f) && len(given(f)) > 0 {
				return usageErrorf(cmdqCommand, "%s takes no %s", sub, f)
			}
		}
		if id == "" {
			return usageErrorf(cmdqCommand, "%s needs the ID add printed", sub)
		}
		switch sub {
		case cmdqWait:
			limit, err := waitTimeout(timeout, len(given(timeoutFlag)) > 0)
			if err != nil {
				return err
			}
			return waitForEntry(env, store, id, limit)
		case cmdqStatus:
			return entryStatus(env, store, id)
		default:
			return dropEntry(env, store, id)
		}
	case cmdqList, cmdqHistory:
		if extra := given(whyFlag, fromFlag, cwdFlag, timeoutFlag); len(extra) > 0 {
			return usageErrorf(cmdqCommand, "%s takes no flags", sub)
		}
		if id != "" {
			return usageErrorf(cmdqCommand, "unexpected argument %q", id)
		}
		return printEntries(env, store, sub == cmdqHistory)
	default:
		return usageErrorf(cmdqCommand, "no sub-command %q (have: %s)", sub, strings.Join(cmdqSubcommands, ", "))
	}
}

// splitCommand takes everything after the first "--" as the command, words
// joined by spaces as a shell would hand them on. Nothing after it is read as
// a flag of treewright's: a queued `sudo rm -f x` is the person's to the last
// character.
func splitCommand(args []string) (rest []string, command string, ok bool) {
	i := slices.Index(args, "--")
	if i < 0 {
		return args, "", false
	}
	return args[:i], strings.Join(args[i+1:], " "), true
}

// ---- add ---------------------------------------------------------------------

type queueRequest struct {
	why, from, cwd string
	fromGiven      bool
	command        string
	hasCommand     bool
}

// queueCommand puts a command in the queue and prints its ID, which is the
// answer: the agent passes it to wait. Everything said to the agent besides —
// which key the person opens the list with, how to wait — goes to stderr.
//
// Refused before anything is written: an empty command, one holding a NUL
// (which no shell can be handed, so it could not run as it would be shown), a
// missing reason, a directory that does not exist, and a command the guard
// would refuse — see docs/plugins.md for why that one is here.
func queueCommand(env *Env, store cmdq.Store, r queueRequest) error {
	command := r.command
	if !r.hasCommand {
		read, err := commandFromStdin(env)
		if err != nil {
			return err
		}
		command = read
	}
	// A heredoc ends in a newline, which is not part of the command: dropped
	// here, so the text stored is the text shown and the text run.
	command = strings.TrimRight(command, "\n")
	switch {
	case strings.TrimSpace(command) == "":
		return usageErrorf(cmdqCommand, "the command is empty")
	case strings.ContainsRune(command, 0):
		return errors.New("the command holds a NUL byte\nno shell can be handed one, so it could not run as it would be shown")
	case len(command) > maxCommand:
		return fmt.Errorf("the command is longer than %d KiB, which is too long to read before running", maxCommand>>10)
	}
	why := cmdq.OneLine(r.why)
	if why == "" {
		return usageErrorf(cmdqCommand, `%s is required: say in one line what the command is for`, whyFlag)
	}

	here := orHere("")
	dir, err := runsIn(r.cwd, here)
	if err != nil {
		return err
	}
	from := cmdq.OneLine(r.from)
	if !r.fromGiven {
		from = askedBy(here)
	} else if from == "" {
		return usageErrorf(cmdqCommand, "%s is empty", fromFlag)
	}

	if refusal, refused := guardRefusal(env, command, dir); refused {
		env.errorf("%s", refusal)
		return ErrSilent
	}

	id, err := store.Add(cmdq.Request{Command: command, From: from, Why: why, Dir: dir}, time.Now)
	if err != nil {
		return fmt.Errorf("could not queue the command: %w", err)
	}
	fmt.Fprintln(env.Stdout, id)
	env.progressf("queued for the person to run — tell them it is waiting%s", asFields(
		field("opens the list", cmdqOpener(env)),
		field("wait for it", env.copyable(env.Argv0+" cmdq wait "+id)),
	))
	return nil
}

// commandFromStdin reads a command given on stdin, which is how a quoted
// heredoc arrives. A terminal there is somebody who forgot the command, and
// reading it would sit waiting for one.
func commandFromStdin(env *Env) (string, error) {
	if f, ok := env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return "", usageErrorf(cmdqCommand, "no command: give it after --, or on stdin")
	}
	data, err := io.ReadAll(io.LimitReader(env.Stdin, maxCommand+1))
	if err != nil {
		return "", fmt.Errorf("could not read the command from stdin: %w", err)
	}
	return string(data), nil
}

// runsIn is the directory a command will run in: the one --cwd names, made
// absolute, or the one add was run from. It has to exist now. It may be gone
// by the time the person gets to it, which the list says.
func runsIn(cwd, here string) (string, error) {
	if cwd == "" {
		return here, nil
	}
	dir := cwd
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(here, dir)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("no such directory: %s\n%s is where the command runs, and it has to exist", cwd, cwdFlag)
	}
	return dir, nil
}

// askedBy is who the list says asked, when add was not told: the name of the
// agent asking, read off the window it runs in. That is the name treewright
// gave it and the one other agents message it by, so the list, ls and a
// message all call it the same thing — where the git checkout's folder name,
// which the script used, cannot tell the base window's agent from a scratch
// window's, both standing in the main checkout.
//
// Outside a window with a name, the registered repository's config name, and
// outside a registered repository the checkout's folder, or the directory's.
func askedBy(here string) string {
	if w, ok := tmux.CallersWindow(); ok && w.Agent != "" {
		return cmdq.OneLine(w.Agent)
	}
	if cfg, err := configAt(here); err == nil {
		return cfg.Name
	}
	if top, err := (git.Repo{Dir: here}).TopLevel(); err == nil {
		return filepath.Base(top)
	}
	return filepath.Base(here)
}

// guardRefusal is the guard's question asked of a queued command: would the
// guard refuse it, run as a Bash call from the directory it is queued in, by
// the agent queuing it? Without this, cmdq is the way around the guard, one
// Enter away — a command it turned back, queued for a person who is the
// reader least likely to notice whose worktree a path is in.
//
// The scope is the caller's, as the guard's is: the worktree the agent stands
// in is its own, and every other one of the repository's is somebody else's.
// The command starts in the directory it will run in, so a --cwd in another
// worktree and a `cd` into one inside the command are both found. Paths are
// compared as git spells them, which is with symlinks resolved.
func guardRefusal(env *Env, command, dir string) (string, bool) {
	scope, ok := guardScopeFor("")
	if !ok {
		return "", false
	}
	start := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		start = resolved
	}
	found, ok := scope.reachesFrom(command, start)
	if !ok {
		return "", false
	}
	return found.refusal(env, scope.cfg), true
}

// cmdqOpener is how the person opens the list: the key tmux-init bound, read
// from the server, or the command when nothing is bound.
func cmdqOpener(env *Env) string {
	if keys := cmdqKeys(); keys != "" {
		return keys
	}
	return env.copyable(env.Argv0 + " " + cmdqCommand)
}

// cmdqKeys spells the keystroke that opens the list, or "" when no binding
// does. Asked of tmux for newWorktreeKeys' reason: the key is the person's,
// and a terminal emulator's mapping is something no plugin can see.
func cmdqKeys() string {
	return keystroke(canonicalName, " "+cmdqCommand)
}

// ---- wait, status, drop -------------------------------------------------------

// waitTimeout reads --timeout: whole seconds, above zero.
func waitTimeout(value string, given bool) (time.Duration, error) {
	if !given {
		return waitLimit, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, usageErrorf(cmdqCommand, "%s takes a whole number of seconds above 0, not %q", timeoutFlag, value)
	}
	return time.Duration(n) * time.Second, nil
}

// waitForEntry waits until an entry has been seen to: it ran, a run of it
// failed after the wait began, it was dismissed or withdrawn, or the time ran
// out. Whichever it was, the entry's status is printed on stdout, so an agent
// reading the output has the outcome in words as well as in the exit code.
//
// The exit code is main's to choose, as every one is: what is returned is the
// outcome, in an OutcomeError, and main holds the table from outcome to number.
func waitForEntry(env *Env, store cmdq.Store, id string, limit time.Duration) error {
	if _, err := store.Find(id); err != nil {
		return noEntry(id, err)
	}
	start := time.Now()
	for {
		e, err := store.Find(id)
		if err != nil {
			return fmt.Errorf("%s left the history while it was being waited for", id)
		}
		if e.InHistory() || (!e.Failed.IsZero() && !e.Failed.Before(start)) {
			printStatus(env, store, e)
			switch e.Outcome {
			case cmdq.Ran:
				return nil
			case cmdq.Waiting:
				return &OutcomeError{Outcome: WaitRunFailed}
			case cmdq.Dismissed, cmdq.Withdrawn:
			}
			return &OutcomeError{Outcome: WaitDismissed}
		}
		elapsed := time.Since(start)
		if elapsed >= limit {
			printStatus(env, store, e)
			env.progressf("still waiting after %d s", int(limit/time.Second))
			return &OutcomeError{Outcome: WaitTimedOut}
		}
		time.Sleep(min(waitEvery, limit-elapsed))
	}
}

// entryStatus prints how an entry stands, and the last lines of its last run.
func entryStatus(env *Env, store cmdq.Store, id string) error {
	e, err := store.Find(id)
	if err != nil {
		return noEntry(id, err)
	}
	printStatus(env, store, e)
	return nil
}

// printStatus is status's answer, which wait prints too: one line saying how
// the entry stands, then what its last run left, each line through Visible.
func printStatus(env *Env, store cmdq.Store, e cmdq.Entry) {
	fmt.Fprintln(env.Stdout, cmdq.Describe(e, time.Now()))
	output := strings.TrimSuffix(store.Output(e), "\n")
	if output == "" {
		return
	}
	for line := range strings.SplitSeq(output, "\n") {
		fmt.Fprintln(env.Stdout, "  | "+cmdq.Visible(line))
	}
}

// dropEntry withdraws an entry nobody needs any more, which a wait on it
// reports as such.
func dropEntry(env *Env, store cmdq.Store, id string) error {
	err := store.Retire(id, cmdq.Withdrawn, time.Now())
	if errors.Is(err, cmdq.ErrNoEntry) {
		if e, found := store.Find(id); found == nil {
			return fmt.Errorf("%s has already left the list%s", id, asFields(field("it was", cmdq.Describe(e, time.Now()))))
		}
		return noEntry(id, err)
	}
	if err != nil {
		return fmt.Errorf("could not withdraw %s: %w", id, err)
	}
	env.progressf("withdrew %s", id)
	return nil
}

func noEntry(id string, err error) error {
	if errors.Is(err, cmdq.ErrNoEntry) {
		return fmt.Errorf("no entry %s\nan ID is what add printed, and the history keeps the last %d", id, cmdq.HistoryKeep)
	}
	return err
}

// ---- list, history -----------------------------------------------------------

// printEntries prints the waiting list, or the history, for a reader who is
// not at the list: an agent checking on what it queued, or a person over ssh
// with no popup. The entries are the answer, so they go to stdout; an empty
// list prints nothing there and says so on stderr.
func printEntries(env *Env, store cmdq.Store, history bool) error {
	read, empty := store.Pending, "nothing waiting"
	if history {
		read, empty = store.History, "no history yet"
	}
	entries, err := read()
	if err != nil {
		return fmt.Errorf("could not read the queue: %w", err)
	}
	if len(entries) == 0 {
		env.progressf("%s", empty)
		return nil
	}
	now := time.Now()
	for i, e := range entries {
		if i > 0 {
			fmt.Fprintln(env.Stdout)
		}
		fmt.Fprintf(env.Stdout, "%s  %s  (%s)\n", e.ID, cmdq.Visible(e.From), cmdq.Describe(e, now))
		fmt.Fprintf(env.Stdout, "  why: %s\n", cmdq.Visible(e.Why))
		fmt.Fprintf(env.Stdout, "  in:  %s\n", cmdq.Visible(e.Dir))
		for j, line := range strings.Split(cmdq.Visible(e.Command), "\n") {
			mark := ">"
			if j == 0 {
				mark = "$"
			}
			fmt.Fprintf(env.Stdout, "  %s %s\n", mark, line)
		}
	}
	return nil
}

// ---- the list ----------------------------------------------------------------

// showQueue opens the list on the terminal, which it needs: it reads keys one
// at a time and draws a screen of its own. Drawn on stderr, as every picker
// is, so stdout keeps the contract of carrying nothing but an answer, and
// there is none.
func showQueue(env *Env, store cmdq.Store) error {
	in, inOK := env.Stdin.(*os.File)
	out, outOK := env.Stderr.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return fmt.Errorf("the list needs a terminal%s", asFields(
			field("open it with", cmdqOpener(env)),
			field("or print it", env.copyable(env.Argv0+" cmdq list")),
		))
	}
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return fmt.Errorf("could not take the terminal: %w", err)
	}
	defer func() { _ = term.Restore(int(in.Fd()), state) }()

	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)

	return cmdq.Screen{
		Store:   store,
		Keys:    cmdq.NewKeyboard(in),
		Out:     out,
		Term:    out,
		Resized: resized,
		Color:   ui.ColorEnabled(out),
	}.Show()
}

// cmdqPopupSize is the popup the list opens in: most of the terminal it opens
// on. A command's output is what it ends up holding, and nobody can predict
// how wide that is — the one popup whose contents cannot be measured ahead.
func cmdqPopupSize(client string) (width, height int) {
	const (
		fallbackWidth, fallbackHeight = 120, 36
		minWidth, minHeight           = 60, 16
	)
	w, h, ok := tmux.ClientSize(client)
	if !ok {
		return fallbackWidth, fallbackHeight
	}
	return min(w, max(minWidth, w*9/10)), min(h, max(minHeight, h*9/10))
}

// ---- doctor ------------------------------------------------------------------

// checkCmdq reports on the queue, once somebody has used it — the directory
// existing, which only add and the list create, as reviews' cache does.
//
// It counts what is waiting, and the entries whose directory has gone, which
// is the one thing in the queue that goes wrong without anybody doing
// anything: `rm` took the worktree a command was queued in, and says nothing,
// since core knows nothing of the plugin. The list marks such an entry, and
// this is where somebody who has not opened the list hears of it.
func checkCmdq(r *report) {
	store := cmdq.Store{Dir: cmdq.Dir()}
	if !store.Exists() {
		return
	}
	r.in(cmdqCommand)
	pending, err := store.Pending()
	if err != nil {
		r.addf(levelWarn, "queue", "cannot be read\n%v", err)
		return
	}
	history, _ := store.History()
	r.addf(levelOK, "queue", "%s, %d in the history\n%s",
		count(len(pending), "command waiting", "commands waiting"), len(history), store.Dir)
	gone := 0
	for _, e := range pending {
		if info, err := os.Stat(e.Dir); err != nil || !info.IsDir() {
			gone++
		}
	}
	if gone > 0 {
		r.addf(levelWarn, "directories", "%s queued in a directory that has gone\nopen the list and dismiss them with d",
			count(gone, "command", "commands"))
	}
}

// ---- session-start -----------------------------------------------------------

// cmdqBriefing is what the cmdq feature tells an agent at the start of each
// session: that the queue is there, when to use it, and how. The reader is an
// agent, so it is written as instructions, with the commands spelled out as
// they are to be typed and the exit codes it will be handed.
//
// The key is read from tmux when the session starts, so the agent tells the
// person the key they actually have.
func cmdqBriefing(*config.Config) string {
	opener := `"treewright cmdq" at a prompt`
	if keys := cmdqKeys(); keys != "" {
		opener = keys + ` (or "treewright cmdq" at a prompt)`
	}
	return `treewright's command queue is on in this repository (the cmdq feature).

When a command needs the person rather than you — sudo, an interactive login,
anything that asks for a password or wants their terminal — queue it instead of
pasting it into your reply:

    treewright cmdq add --why "one line on what it is for" <<'EOF'
    sudo installer -pkg ./toolchain.pkg -target /
    EOF

Keep the heredoc's delimiter quoted as shown, so your shell expands nothing on
the way in. add prints the entry's ID. Straight after it, start the wait in the
background, so you hear as soon as the person has run it:

    treewright cmdq wait <ID>

Then tell the person what you queued and that ` + opener + ` opens the
list, where Enter runs it. wait prints how it went and the last lines of
output, and exits 0 when it ran and exited 0, 5 when a run failed (it stays
queued, and the person may try again), 3 when the person dismissed it, and 4
when it was still waiting after an hour (--timeout to change that).
"treewright cmdq status <ID>" says the same at any time, and
"treewright cmdq drop <ID>" withdraws one you no longer need.

Queue only what needs the person. Anything you can run yourself, run.`
}
