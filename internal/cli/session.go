package cli

import (
	"errors"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// Each repository gets a tmux session of its own, named after its config, and
// every window treewright opens goes into that session.
//
// The alternative — opening windows in whatever session the caller happens to be
// attached to — mixes repositories together in one status line, where two windows
// named MAIN belong to different repositories and a ticket key says nothing about
// which checkout it is in. Worse, it made `resume` a silent no-op across
// sessions: selecting a window in a session your client is not attached to
// succeeds and changes nothing you can see.
//
// So one function does the work for `new`, `resume` and `base` alike: find the
// window already sitting in a directory, or make one in the right session, and
// then bring it to the foreground.

// sessionFor names the tmux session holding a repository's windows: the config's
// own name, unless the config chose one with tmux_session.
func sessionFor(cfg *config.Config) string {
	if name := strings.TrimSpace(cfg.TmuxSession); name != "" {
		return tmux.SessionName(name)
	}
	return tmux.SessionName(cfg.Name)
}

// openWindow puts a window on spec.Dir in the repository's session — or focuses
// the window already sitting there, wherever it turns out to be.
//
// The caller describes the worktree: its directory, window name, command, and the
// slug and branch behind it. What every window of a repository shares — the
// session it goes in and the repository's name — is filled in here from the
// config, so no caller has to remember it.
//
// The session is created when it does not exist yet, so the first `new` of the
// day is also what establishes the repository's session. Nothing here needs a
// client: outside tmux the window is still created and left current, and the
// caller is told how to attach.
//
// Whether the window was created or merely found is reported back, because the
// two differ in the one thing a caller cannot see: only a created window runs
// spec.Command. A caller that folded something into that command — a kickoff
// prompt — needs to know when it never ran.
func openWindow(env *Env, cfg *config.Config, spec tmux.Spec) (created bool, err error) {
	if !tmux.Available() {
		env.progressf("tmux is not installed — cd %s and run %s yourself", spec.Dir, spec.Command)
		return false, nil
	}
	spec.Session = sessionFor(cfg)
	spec.Repo = cfg.Name

	// Kept as the user wrote it for everything below that talks about it, while
	// tmux gets the wrapped form: a message naming a command has to name the
	// command, not treewright's scaffolding around it.
	command := spec.Command
	spec.Command = heldOpenOnFailure(command)

	// A window already sitting in that directory is the session being asked for,
	// so switch to it rather than opening a duplicate beside it.
	if w, ok := tmux.Windows(spec.Session)[spec.Dir]; ok {
		if w.Session != spec.Session {
			// Someone's own window, or one opened before this repo had a session
			// of its own. Switching to it is still better than opening a second
			// window on the same directory, but it is worth saying where it went.
			env.warnf("window %s is in session %s rather than %s — switching to it there",
				w.Name, w.Session, spec.Session)
		}
		focusWindow(env, cfg, w, command)
		return false, nil
	}

	var w tmux.Window
	if tmux.HasSession(spec.Session) {
		w, err = tmux.NewWindow(spec)
	} else {
		w, err = tmux.NewSession(spec)
		if err == nil {
			env.progressf("created tmux session %s, which holds %s's windows", spec.Session, cfg.Name)
		}
	}
	if err != nil {
		return false, err
	}
	focusWindow(env, cfg, w, command)
	return true, nil
}

// heldOpenOnFailure wraps a window's command so that one which fails leaves its
// output on screen instead of taking the window down with it.
//
// tmux closes a window the moment its command exits, which means the whole of what
// went wrong — the `command not found`, the config error, the stack trace — is
// erased at exactly the speed it appeared. treewright can say afterwards that the
// window "closed as soon as it opened", and does, but that is a guess arriving in
// another terminal, without the one thing the user needs, which is the message.
//
// So a failing command's window stays up until the user has read it and pressed
// Enter. A successful one closes as before: the shell exits with the same status,
// so nothing about finishing normally changes.
//
// Anything above 128 is let through untouched. That range is a command killed by a
// signal — usually the user's own Ctrl-C — and holding a window open to report a
// stop the user asked for would turn every deliberate quit into a keypress.
//
// The wrapper is a shell script because tmux already runs the command through a
// shell, so this adds no layer that was not there: `command` is still one shell
// line, run as written, and still the thing the pane's foreground process is.
//
// It runs inside a subshell for the same reason post_create's steps do. A command
// that calls `exit` itself, or sources something that does — a wrapper script, a
// shell function, an activate — would otherwise end the whole script at that line
// and close the window with its output erased, which is the case this exists for.
//
// The held-open path also takes the agent state off the window. That state
// normally dies with the window — the agent is the window's command — and holding
// the window open past the command is the one place that stops being true: left
// alone, a dead agent's "working" would sit in the ls table for as long as the
// window sat unread, and its waiting marker would keep flagging a window whose
// agent is gone. Both are cleared best-effort, straight through tmux rather than
// through `treewright signal`, so the wrapper stays runnable when the binary that
// wrote it has since moved off PATH. Inside the pane, $TMUX names the right
// server — including under TREEWRIGHT_TMUX_LABEL — and $TMUX_PANE targets the
// pane's own window, which display-message untargeted would not.
func heldOpenOnFailure(command string) string {
	if strings.TrimSpace(command) == "" {
		return command
	}
	return "( " + command + "\n)\n" +
		"tw_status=$?\n" +
		`if [ "$tw_status" -eq 0 ] || [ "$tw_status" -gt 128 ]; then exit "$tw_status"; fi` + "\n" +
		`if [ -n "$TMUX_PANE" ]; then` + "\n" +
		`  tmux set-window-option -q -u -t "$TMUX_PANE" ` + tmux.AgentStateOption + " 2>/dev/null || true\n" +
		`  tw_name=$(tmux display-message -p -t "$TMUX_PANE" '#{window_name}' 2>/dev/null) || tw_name=''` + "\n" +
		`  case "$tw_name" in ` + shellQuote(tmux.WaitingMarker) + `*) tmux rename-window -t "$TMUX_PANE" "${tw_name#?}" 2>/dev/null || true ;; esac` + "\n" +
		"fi\n" +
		"printf '\\n\"%s\" exited %s — this window is kept so the output above stays readable\\n'" +
		" " + shellQuote(command) + ` "$tw_status"` + "\n" +
		"printf 'press Enter to close it\\n'\n" +
		"read -r tw_done\n" +
		`exit "$tw_status"` + "\n"
}

// focusWindow brings a window to the foreground, or says how to reach it when
// there is no client to move.
//
// Nothing here fails the command — it returns nothing a caller could fail on.
// The window exists by this point, created or found, so a client that could not
// be moved — or a window that has closed since — is news to report rather than
// grounds for calling the whole thing a failure.
//
// The way out is given as `treewright attach <repo>` rather than as the tmux command
// it runs, because that spelling stays correct: it names the session exactly, and
// it reaches the right server when TREEWRIGHT_TMUX_LABEL has aimed treewright at one a
// bare `tmux attach` would not find. Spelled with Argv0, so someone who typed tw
// is answered in the name they use.
func focusWindow(env *Env, cfg *config.Config, w tmux.Window, command string) {
	switch err := tmux.Focus(w); {
	case errors.Is(err, tmux.ErrNotFollowed):
		env.warnf("could not switch to session %s — attach with: %s attach %s", w.Session, env.Argv0, cfg.Name)
	case err != nil:
		// The window was there a moment ago, so what changed is that it closed:
		// tmux closes a window as soon as its command exits, and a command that
		// exits at once — a typo, a wrapper script that fails — looks exactly like
		// this. Naming the command is what makes that guessable.
		env.warnf("window %s closed as soon as it opened — did %q exit straight away?", w.Name, command)
	case !tmux.Inside():
		env.progressf("window %s is open in tmux session %s — attach with: %s attach %s",
			w.Name, w.Session, env.Argv0, cfg.Name)
	}
}
