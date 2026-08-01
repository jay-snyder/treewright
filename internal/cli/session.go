package cli

import (
	"errors"
	"fmt"
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
		// The two things to type get a labelled line each, because that is what
		// they are: a directory to move to and a command to run there, both long
		// enough that "cd <path> and run <command> yourself" read as one run-on
		// line with no obvious seam.
		env.progressf("tmux is not installed, so no window was opened%s", asFields(
			field("cd", env.copyable(spec.Dir)),
			field("run", env.copyable(spec.Command)),
		))
		return false, nil
	}
	spec.Session = sessionFor(cfg)
	spec.Repo = cfg.Name

	// Kept as the user wrote it for everything below that talks about it, while
	// tmux gets the wrapped form: a message naming a command has to name the
	// command, not treewright's scaffolding around it.
	command := spec.Command
	spec.Command = heldOpenOnFailure(command)

	// A window already sitting in that directory is the window being asked for, so
	// switch to it rather than opening a duplicate beside it — unless it is the
	// pane treewright is being typed into, which answers the request with the
	// place the user is already standing.
	if w, ok := tmux.Windows(spec.Session)[spec.Dir]; ok && !isTheCallersOwnShell(w, spec.Dir) {
		if w.Session != spec.Session {
			// Someone's own window, or one opened before this repo had a session
			// of its own. Switching to it is still better than opening a second
			// window on the same directory, but it is worth saying where it went.
			env.warnf("window %s is in session %s, not %s\nswitching to it there",
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
			env.progressf("created tmux session %s for %s's windows", spec.Session, cfg.Name)
		}
	}
	if err != nil {
		return false, err
	}
	focusWindow(env, cfg, w, command)
	return true, nil
}

// isTheCallersOwnShell reports that the window found on dir is the pane
// treewright is being typed into, standing there without being the window
// treewright opened on it — a shell you happened to cd into the checkout, rather
// than the window that checkout's commands mean.
//
// That is the everyday state of affairs for `base`, since standing in the main
// checkout is where you type it from. Left in, the shell was found as the base
// window and treewright "switched" to it: a switch to the session the client was
// already in, which does nothing anyone can see. The repository was left with no
// session of its own, the agent never ran, and the only sign of any of it was a
// warning that the window was in another session rather than one that did not yet
// exist.
//
// A window treewright opened on this very directory is exempt, however it is
// reached, so `base` typed in the base window — or `resume` in a worktree's own
// window — stays the no-op it has always been: you are already there. So is any
// window that is not the caller's own, wherever it sits, because switching to one
// still beats opening a second window on the same checkout.
func isTheCallersOwnShell(w tmux.Window, dir string) bool {
	if w.Worktree == dir {
		return false
	}
	return w.ID == tmux.CurrentWindow()
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
// The line that reports what exited names the command rather than repeating it,
// which is the difference between a wrapper of a fixed size and one that grows
// twice as fast as what it wraps. See abbreviated for what that cost.
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
		" " + shellQuote(abbreviated(command)) + ` "$tw_status"` + "\n" +
		"printf 'press Enter to close it\\n'\n" +
		"read -r tw_done\n" +
		`exit "$tw_status"` + "\n"
}

// maxNamedCommand caps the copy of the command a held-open window prints above
// its "press Enter", in runes.
//
// Eighty is a terminal's width, and the line already spends seventy of it saying
// what happened, so what this really buys is enough of the command to recognize
// it by. The output that explains the failure is directly above.
const maxNamedCommand = 80

// abbreviated is the command as the held-open window names it: its first line,
// cut to something a reader takes in at a glance.
//
// The wrapper used to print it whole, and that made the script grow with the
// command instead of by a fixed amount — worse than twice as fast, because this
// copy is shell-quoted a second time. fillPrompt has already quoted the prompt
// into the command, so one apostrophe of ordinary English possessive is a
// four-character escape by the time it arrives here, and quoting that again
// makes it sixteen. A few thousand words of prompt then hit tmux's own
// command-length ceiling on the strength of the copy nobody runs, and the window
// that would have reported it was the window that could not be opened.
//
// Nothing is lost by cutting it. The copy is there so a reader can tell which
// command produced the output above it, not so it can be re-run; the copy that
// does run is kept byte-exact, that one being the pane's foreground process.
func abbreviated(command string) string {
	first, rest, _ := strings.Cut(command, "\n")
	kept := []rune(first)
	dropped := strings.TrimSpace(rest) != ""
	if len(kept) > maxNamedCommand {
		kept, dropped = kept[:maxNamedCommand], true
	}
	if !dropped {
		return first
	}
	// One column rather than three periods, as a window name shortened for the
	// status line is marked.
	return string(kept) + "…"
}

// checkCommandFits refuses a window command tmux would not run, naming the
// setting it came from.
//
// tmux's own refusal arrives too late to be worth anything. By the time a window
// is asked for, `new` has made the branch and the worktree, and it deliberately
// does not fail on a window it could not open — the path is already on stdout, so
// that `cd "$(treewright new eng-1)"` cannot be broken by tmux. So the whole of
// what the user got was tmux's raw "command too long" as a warning, with the
// doubled script in it, over a worktree with no window and no agent, and `new`
// refusing the slug from then on.
//
// Checked here instead, an over-long prompt is what it actually is: this
// invocation being wrong, refused in the same breath as a prompt the template
// cannot take, and before anything exists to clean up.
//
// What is measured is the wrapped command, because that is what tmux is handed.
func checkCommandFits(command, key, prompt string) error {
	size := len(heldOpenOnFailure(command))
	if size <= tmux.MaxCommandLength {
		return nil
	}
	// The prompt is what fills this budget in practice, and a reader who has just
	// typed one needs to be told that rather than left to wonder what is wrong
	// with a config line they have not touched.
	//
	// The way out it names used to be pasting the text once the window was open,
	// which assumes a keyboard and a window — and there is no window, since
	// nothing was created. The route that works from where the reader is standing
	// is a file with the text in it and a prompt naming that file: it is what the
	// skill teaches an agent driving treewright, it needs no second command to
	// make sense of, and a person can take it too.
	subject, fix := key+" is", "shorten it"
	if prompt != "" {
		subject = "--prompt makes " + key
		fix = "shorten the prompt, or put it in a file and pass a prompt naming that file"
	}
	return fmt.Errorf("%s too long for tmux to run in a window%s\nnothing was created — %s",
		subject, asFields(
			field("size", count(size, "byte", "bytes")),
			field("limit", count(tmux.MaxCommandLength, "byte", "bytes")),
		), fix)
}

// focusWindow brings a window to the foreground, or says how to reach it when
// there is no client to move.
//
// Nothing here fails the command — it returns nothing a caller could fail on.
// The window exists by this point, created or found, so a client that could not
// be moved — or a window that has closed since — is news to report rather than
// grounds for calling the whole thing a failure.
//
// The way out comes from attachHint rather than being spelled here, because the
// session a window turned out to be in is not always this repository's.
func focusWindow(env *Env, cfg *config.Config, w tmux.Window, command string) {
	switch err := tmux.Focus(w); {
	case errors.Is(err, tmux.ErrNotFollowed):
		env.warnf("could not switch to session %s%s", w.Session,
			asFields(field("attach with", env.copyable(attachHint(env, cfg, w.Session)))))
	case err != nil:
		// The window was there a moment ago, so what changed is that it closed:
		// tmux closes a window as soon as its command exits, and a command that
		// exits at once — a typo, a wrapper script that fails — looks exactly like
		// this. Naming the command is what makes that guessable.
		env.warnf("window %s closed as soon as it opened\ndid %q exit straight away?", w.Name, command)
	case !tmux.Inside():
		env.progressf("window %s is open in tmux session %s%s", w.Name, w.Session,
			asFields(field("attach with", env.copyable(attachHint(env, cfg, w.Session)))))
	}
}

// attachHint says how to reach the session a window turned out to be in.
//
// The repository's own session is given as `treewright attach <repo>` rather than
// as the tmux command it runs, because that spelling stays correct: it names the
// session exactly, and it reaches the right server when TREEWRIGHT_TMUX_LABEL has
// aimed treewright at one a bare `tmux attach` would not find. Spelled with Argv0,
// so someone who typed tw is answered in the name they use.
//
// A window that ended up in some other session is not something `attach` can
// reach: it takes a repository, and it would send the user to this repository's
// session — which in the case that produced this message may not even be running.
// Being told to attach to a session that does not exist, in order to reach a
// window treewright has just said is somewhere else, is a dead end. So that one is
// spelled as the tmux command it is, server flags included for the same reason
// `attach` exists at all.
func attachHint(env *Env, cfg *config.Config, session string) string {
	if session == sessionFor(cfg) {
		return env.Argv0 + " attach " + cfg.Name
	}
	return "tmux " + strings.Join(tmux.AttachArgs(session), " ")
}
