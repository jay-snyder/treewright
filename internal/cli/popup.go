package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/tmux"
	"github.com/jay-snyder/treewright/internal/ui"
)

// A tmux popup is given its size when it is created, and tmux offers no way to
// fit one to what appears inside it: -w and -h take cells or a percentage of the
// terminal, and nothing else. A percentage is the wrong unit for a picker, whose
// height is the number of worktrees and whose width is the widest slug — neither of
// which grows when the terminal does. On a wide terminal the stock 70% left most
// of the popup empty.
//
// So the size is worked out before the popup exists, by the one program that
// knows what will be printed into it.

// PopupHint says how to dismiss a popup, for a treewright that is running in one and
// is about to exit non-zero — which is exactly when tmux leaves the popup on
// screen, holding whatever was printed so it can be read.
//
// Written here rather than beside each message because every non-zero exit has
// the same problem and the same answer, and a hint repeated at thirty call sites
// is one that goes missing from the thirty-first. Outside a popup it says nothing:
// there is nothing to dismiss, and telling someone to press Escape at their own
// prompt would only puzzle them.
func PopupHint(w io.Writer) {
	if os.Getenv(tmux.PopupEnv) == "" {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.Dim.Apply("press Esc to close", ui.ColorEnabled(w)))
}

// cmdPopup runs another treewright command inside a tmux popup sized for its output.
//
// It is what the key bindings in `treewright tmux-init` invoke, through run-shell,
// which is the only way a binding can compute anything: display-popup would have
// to be handed a literal size.
//
// That indirection costs two things, which --client and --dir buy back. Both are
// recovered the same way: run-shell expands formats in the command it runs, so a
// binding can pass what tmux knows and the process it spawns does not.
//
// --client, because a tmux command run from outside tmux has no association with
// the client that asked for it, so tmux falls back to the most recently active
// one — and with two terminals attached to two sessions, the popup opens over
// whichever has been busier. The binding passes #{client_tty}.
//
// --dir, because run-shell does not run in the calling pane's directory. It runs
// in the tmux server's, which is wherever the server was started, so treewright
// would resolve the repository — and mark the worktree you are standing in — from
// a directory that has nothing to do with the window the key was pressed in. The
// binding passes #{pane_current_path}.
func cmdPopup(env *Env, args []string) error {
	var client, dir string
	positional, err := parseArgs("popup", args, nil,
		map[string]*string{"-c": &client, "--client": &client, "-d": &dir, "--dir": &dir}, 3)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return usageErrorf("popup", "a command to run in the popup is required")
	}
	if positional[0] == "popup" {
		// Left to itself this nests forever, each popup opening another.
		return usageErrorf("popup", "popup cannot run itself")
	}
	if !tmux.Available() {
		return fmt.Errorf("tmux is not installed, so there is no popup to open")
	}

	// Moved into before anything asks where we are. Everything below reads the
	// working directory rather than taking it as an argument — sizeFor resolves
	// the config to count the worktrees, and the popup itself starts here — so
	// the one honest way to act on another directory is to stand in it.
	//
	// Restored on the way out because Run is called in-process by the tests, and
	// a command that moved the process and left it there would decide where the
	// next one thinks it is.
	if dir != "" {
		if prev, err := os.Getwd(); err == nil {
			defer os.Chdir(prev)
		}
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("cannot open a popup for %s: %w", dir, err)
		}
	}

	width, height := sizeFor(env, positional[0])

	// The binary by its own path rather than by name: the popup runs through a
	// shell whose PATH is the tmux server's, which is inherited from whatever
	// started it and need not be the one treewright was found on.
	self, err := os.Executable()
	if err != nil {
		self = "treewright"
	}
	inner := make([]string, 0, len(positional)+3)
	// The invoked name rides along because the popup was just sized to messages
	// spelled with it, and the inner process — reached by path, so its argv[0]
	// says nothing — must print them the same width. Through env rather than a
	// VAR=value prefix, because the popup's command runs under tmux's
	// default-shell, and fish does not read that prefix as an assignment.
	inner = append(inner, "/usr/bin/env", "TREEWRIGHT_ARGV0="+shellQuote(env.Argv0))
	inner = append(inner, shellQuote(self))
	for _, a := range positional {
		inner = append(inner, shellQuote(a))
	}

	// Read back rather than reusing the flag, so the popup opens on the directory
	// this process is actually standing in whichever way it got there: --dir when
	// a binding passed one, and the caller's own directory when a person typed
	// this at a prompt.
	here, _ := os.Getwd()
	return tmux.Popup(client, here, strings.Join(inner, " "), width, height)
}

// sizeFor works out the popup a command needs.
//
// Only the pickers have a size worth deriving; everything else prints a few lines
// of progress whose length nobody can predict, and for those a small fixed popup
// beats a proportion of the terminal.
func sizeFor(env *Env, command string) (width, height int) {
	const (
		defaultWidth  = 80
		defaultHeight = 12
	)
	cmd := lookup(command)
	if cmd == nil || (cmd.name != "resume" && cmd.name != "cd") {
		return defaultWidth, defaultHeight
	}

	cfg, err := resolveConfig("")
	if err != nil {
		return defaultWidth, defaultHeight
	}
	repo := repoFor(cfg)
	managed, err := repo.Managed()
	if err != nil {
		return defaultWidth, defaultHeight
	}
	// The rows the picker will show, in the cheap form popupSize measures: the
	// base checkout at the head, as chooseWorktree puts it, and nothing inspected
	// — the columns that cost a git call are not the ones the data can stretch.
	branch, _ := git.CurrentBranch(cfg.MainDir)
	rows := make([]git.Info, 0, len(managed)+1)
	rows = append(rows, git.Info{
		Worktree: git.Worktree{Dir: cfg.MainDir, Branch: branch},
		Status:   git.StatusBase,
	})
	for _, wt := range managed {
		rows = append(rows, git.Info{Worktree: wt})
	}

	width, height = popupSize(rows, tmux.Windows(sessionFor(cfg)))
	if len(managed) == 0 {
		// Two extra lines, for the sentence resume and cd print above the menu in
		// a repository nobody has forked yet, and the blank line under it. The
		// sentence is usually the widest thing in the popup — the menu beneath it
		// is a single short row — and a message that outgrew its popup would wrap,
		// which is precisely what a hand-tuned size exists to stop.
		const border, hint = 2, 2 // the sentence, and the gap below it
		width = max(width, utf8.RuneCountInString(noWorktreesMessage(env.Argv0, repo.Name()))+border)
		height += hint
	}
	return width, height
}

// noWorktreesHint prints what treewright says about a repository nobody has started
// a worktree in yet, above the menu that follows it.
//
// The blank line is the point of having a function at all. Message and menu are
// different kinds of thing — one is prose about what to do next, the other is a
// list to answer — and run together the header reads as a second line of the
// sentence rather than as the top of a table. Both callers need the gap and both
// need it the same, and the popup is sized for it.
func noWorktreesHint(env *Env, repo string) {
	env.progressf("%s", noWorktreesMessage(env.Argv0, repo))
	fmt.Fprintln(env.Stderr)
}

// noWorktreesMessage is what treewright says about a repository nobody has started a
// worktree in yet.
//
// It lives here, next to the popup sized to hold it, so the two cannot disagree
// about how wide that sentence is — a message that outgrew its popup would wrap,
// which is precisely what a hand-tuned size stops catching.
//
// The key comes first when there is one. Someone reading this is already in tmux,
// most likely in a popup opened by that very key, and the keystroke is the nearer
// of the two answers; the command is for the shell they will drop back to —
// spelled with argv0, the name they actually type there.
func noWorktreesMessage(argv0, repo string) string {
	command := argv0 + " new <slug>"
	if keys := newWorktreeKeys(); keys != "" {
		return fmt.Sprintf("no worktrees for %s — start one with %s, or %q", repo, keys, command)
	}
	return fmt.Sprintf("no worktrees for %s — start one with %q", repo, command)
}

// newWorktreeKeys spells the keystroke that starts a worktree, or "" when no binding
// does.
//
// Asked of tmux rather than assumed, because the keys are the user's: tmux-init
// binds T and N by default, takes --resume-key and --new-key to move them, and a
// hand-written tmux.conf answers to nobody. A hint naming the wrong key is worse
// than no hint.
func newWorktreeKeys() string {
	// Both, so that neither `bind n new-window` nor treewright's own resume binding
	// is mistaken for this one.
	key := tmux.KeyBoundTo("treewright", " new ")
	if key == "" {
		return ""
	}
	if prefix := tmux.Prefix(); prefix != "" {
		return prefix + " " + key
	}
	return key
}
