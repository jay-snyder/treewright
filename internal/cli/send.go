package cli

import (
	"fmt"
	"strings"

	"github.com/jay-snyder/treewright/internal/tmux"
)

// Typing at the agent in a worktree's window.
//
// The window `resume` finds already open is a window whose agent cannot be
// handed a prompt: the command carrying it only runs in a window that was
// created, so a prompt aimed at one already there is warned as undelivered. What
// is left is the agent itself, which is an ordinary TUI on an ordinary tty — so
// tmux can type at it, and this is that.
//
// It was taught as raw tmux before it was a command, and every reason it should
// not have been is in the four rules the prose had to carry: capture the pane
// first, -l for literal, Enter as its own call, one line only. Each of them
// fails silently and differently, and none of them is a judgment — they are the
// mechanics of typing at a terminal, which is what a binary is for. The raw form
// also missed everything internal/tmux already knows: TREEWRIGHT_TMUX_LABEL,
// exact() session targets, and the @treewright_worktree stamp that says which
// window a worktree actually owns.

// capturedLines is how much of the receiving pane is shown before a message is
// sent. Twenty is what the guide's `tail -20` showed and is about a third of a
// terminal: enough to hold a question with its options, which is the thing a
// blind keystroke answers by accident.
const capturedLines = 20

func cmdSend(env *Env, args []string) error {
	var dry bool
	positional, err := parseArgs("send", args, map[string]*bool{
		"-n": &dry, "--dry-run": &dry,
	}, nil, 2)
	if err != nil {
		return err
	}
	slug, message := at(positional, 0), at(positional, 1)
	if slug == "" {
		return usageErrorf("send", "a slug is required")
	}
	if message == "" && !dry {
		return usageErrorf("send", "a message is required\n%s reads the pane and sends nothing, if looking is all you wanted",
			env.copyable("--dry-run"))
	}
	// Refused rather than split, and refused here, before anything has been
	// captured or resolved. Enter is what submits in these TUIs, so the second
	// line and everything after it would post as further turns — an accident
	// treewright cannot tell from an intention, and one that has already
	// happened by the time anyone sees it.
	if strings.ContainsAny(message, "\n\r") {
		return fmt.Errorf("the message has a line break in it, and Enter is what submits\n" +
			"everything after the first line would post as further turns\n" +
			"put the text in a file and send one line naming it, as --prompt-file does")
	}

	cfg, err := resolveConfig("")
	if err != nil {
		return err
	}
	// Only the slug is wanted: what names a window is the worktree, and the
	// branch prefix the user may have typed is git's business.
	_, slug = splitPrefix(env, cfg, slug)

	repo := repoFor(cfg)
	managed, err := repo.Managed()
	if err != nil {
		return err
	}
	// Resolved the way `resume` resolves it, base checkout included: an
	// unambiguous prefix is enough, the expansion is reported, and the window
	// you launch work from runs an agent like any other.
	target, err := chooseWorktree(env, cfg, repo, managed, slug)
	if err != nil {
		return err
	}
	name := target.Slug
	if target.Base {
		name = baseName
	}

	if !tmux.Available() {
		return fmt.Errorf("tmux is not installed, so there is no window to type into")
	}
	window := tmux.Windows(sessionFor(cfg))[target.Dir]
	if window.ID == "" {
		return fmt.Errorf("no window is open on %s, so there is no agent to reach%s",
			name, asFields(field("open one with", env.copyable(env.Argv0+" resume "+name))))
	}
	// An agent typing at itself is a real footgun and a hard one to notice from
	// the inside: the message arrives in this very session, ahead of whatever is
	// being answered, and reads afterwards as an instruction from somewhere else.
	if window.ID == tmux.CurrentWindow() {
		return fmt.Errorf("%s is the window this command is running in%s\n"+
			"the work is already here — do it rather than sending it on",
			window.Name, asFields(field("window", window.ID)))
	}

	// Before the message rather than after it, and unconditionally. What it
	// prevents is silent: an agent sitting on a question with options takes the
	// next keystrokes as the answer to it, so a message sent blind can choose an
	// option nobody read. A flag to skip it would be a flag to turn the check
	// off, and the cost of keeping it is a few lines of stderr.
	pane, err := tmux.Capture(window.ID, capturedLines)
	if err != nil {
		return fmt.Errorf("could not read %s before typing into it: %w\nnothing was sent", window.Name, err)
	}
	// The pane's text is an argument rather than part of the format, so a % in
	// what the agent happened to print stays a %.
	env.progressf("%s shows:\n%s", window.Name, pane)

	if err := refuseHeldOpen(env, window, pane, name); err != nil {
		return err
	}
	if dry {
		if message == "" {
			return nil
		}
		env.progressf("nothing was sent%s", asFields(field("would have sent", env.copyable(message))))
		return nil
	}

	if err := tmux.Send(window.ID, message); err != nil {
		return err
	}
	// Nothing is written to @treewright_agent_state here. The receiving agent's
	// own UserPromptSubmit hook fires `signal working` when the message lands,
	// which is the protocol working as designed; a sender that stamped the
	// window would be guessing at a state only the agent can report.
	env.progressf("sent to %s%s", window.Name, asFields(
		field("worktree", name),
		field("message", message),
	))
	return nil
}

// refuseHeldOpen stops a message going to a window whose command has already
// exited.
//
// The held-open wrapper is the one place a treewright window outlives the agent
// that was running in it: the command failed, its output is being kept on
// screen, and what is actually reading the keyboard is a shell blocked on
// `read`. Typing there does two wrong things at once — the message reaches
// nobody, and the Enter that follows it satisfies the `read` and closes the
// window, erasing the output the wrapper exists to preserve. So this is a
// refusal rather than a warning: the failure it prevents is the loss of the one
// thing that explains why there is no agent.
//
// It is recognized from the capture, which has been taken anyway, rather than
// from the window's agent state — that state is cleared by the wrapper and also
// absent from every window whose agent never signaled, so it cannot tell the two
// apart. The notice is the last line such a window shows, and the match is
// against the last line rather than the whole capture, so an agent that happens
// to print those words mid-screen is not mistaken for a dead one.
func refuseHeldOpen(env *Env, window tmux.Window, pane, name string) error {
	lines := strings.Split(pane, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) != heldOpenNotice {
		return nil
	}
	return fmt.Errorf("%s has no agent in it — its command exited and the window is being held open%s\n"+
		"a message would reach the shell holding it, and the Enter after it would close the window\n"+
		"close it and start again:  %s",
		window.Name, asFields(field("window", window.ID)),
		env.copyable(env.Argv0+" close "+name+" && "+env.Argv0+" resume "+name))
}
