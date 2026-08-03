package cli

import (
	"errors"
	"fmt"
	"strconv"
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
// The caller describes the worktree: its directory, window name, the slug and
// branch behind it, and — separately, in run — what the window should be
// running. What every window of a repository shares — the session it goes in
// and the repository's name — is filled in here from the config, so no caller
// has to remember it.
//
// The session is created when it does not exist yet, so the first `new` of the
// day is also what establishes the repository's session. Nothing here needs a
// client: outside tmux the window is still created and left current, and the
// caller is told how to attach.
//
// Whether the window was created or merely found is reported back, because the
// two differ in the one thing a caller cannot see: only a created window runs
// the command. A caller that folded something into that command — a kickoff
// prompt — needs to know when it never ran.
func openWindow(env *Env, cfg *config.Config, spec tmux.Spec, run windowCommand) (created bool, err error) {
	if !tmux.Available() {
		// The two things to type get a labelled line each, because that is what
		// they are: a directory to move to and a command to run there, both long
		// enough that "cd <path> and run <command> yourself" read as one run-on
		// line with no obvious seam.
		env.progressf("tmux is not installed, so no window was opened%s", asFields(
			field("cd", env.copyable(spec.Dir)),
			field("run", env.copyable(run.Command)),
		))
		return false, nil
	}
	spec.Session = sessionFor(cfg)
	spec.Repo = cfg.Name

	// The command stays as the user wrote it for everything below that talks
	// about it, while tmux gets the script around it: a message naming a command
	// has to name the command, not treewright's scaffolding around it.
	command := run.Command
	spec.Command = run.script()

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

// windowCommand is what a window is asked to run: the command itself, and —
// where the caller has one — what to run instead when that command fails
// without ever getting going.
//
// Only `resume` has the second. It runs resume_command, which is "carry on
// where I left off", and a checkout with nothing to carry on from meets that
// with an error and a window parked on it. command is what such a checkout is
// owed, so the recovery is to run it in the same window: the failure triggers
// it, rather than treewright predicting from somewhere outside the agent
// whether a conversation exists. Everything else — `new`, `move`, `base` —
// runs command already and has nothing to fall back to.
type windowCommand struct {
	Command string // what the window runs
	Fresh   string // what runs instead when Command fails at once, "" for none
}

// script renders what tmux is handed for this window.
func (c windowCommand) script() string {
	// A blank command is the setting that opens a window on a plain shell. There
	// is nothing there that can fail, so there is nothing to wrap and nothing to
	// fall back to — and a script around nothing would run the shell, exit 0, and
	// close the window that was meant to stay.
	if strings.TrimSpace(c.Command) == "" {
		return c.Command
	}
	// A fallback to the command that has just failed is not a fallback: it is the
	// same failure again, in the one shape that could be read as a retry loop.
	// The rule is one recovery and then the held-open window.
	if strings.TrimSpace(c.Fresh) == "" || c.Fresh == c.Command {
		return heldOpenOnFailure(c.Command)
	}
	return freshOnFastFailure(c.Command, c.Fresh) + heldOpenTail()
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
// Which of those two happens is heldOpenTail's decision, taken from the status
// of whatever ran last — this being the shape of a window that runs one command,
// and freshOnFastFailure the shape of one that may run two.
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
func heldOpenOnFailure(command string) string {
	return runStep(command) + heldOpenTail()
}

// runStep renders one command as a step of a window's script: the name the
// reports below give it, the command itself, and the status it exited with.
//
// The name goes in a variable rather than into each message, because a script
// can hold two commands and the line that reports a failure has to name
// whichever of them failed. A variable is how the shell knows which that was.
func runStep(command string) string {
	return "tw_command=" + shellQuote(abbreviated(command)) + "\n" +
		"( " + command + "\n)\n" +
		"tw_status=$?\n"
}

// heldOpenTail is what a window's script ends with: nothing at all when the last
// command succeeded or was interrupted, and otherwise the window held open on
// its output until the user has read it and pressed Enter.
//
// Anything above 128 is let through untouched. That range is a command killed by
// a signal — usually the user's own Ctrl-C — and holding a window open to report
// a stop the user asked for would turn every deliberate quit into a keypress.
//
// This path also takes the agent state off the window. That state normally dies
// with the window — the agent is the window's command — and holding the window
// open past the command is the one place that stops being true: left alone, a
// dead agent's "working" would sit in the ls table for as long as the window sat
// unread, and its waiting marker would keep flagging a window whose agent is
// gone. Both are cleared best-effort, straight through tmux rather than through
// `treewright signal`, so the script stays runnable when the binary that wrote it
// has since moved off PATH. Inside the pane, $TMUX names the right server —
// including under TREEWRIGHT_TMUX_LABEL — and $TMUX_PANE targets the pane's own
// window, which display-message untargeted would not.
func heldOpenTail() string {
	return `if [ "$tw_status" -eq 0 ] || [ "$tw_status" -gt 128 ]; then exit "$tw_status"; fi` + "\n" +
		`if [ -n "$TMUX_PANE" ]; then` + "\n" +
		`  tmux set-window-option -q -u -t "$TMUX_PANE" ` + tmux.AgentStateOption + " 2>/dev/null || true\n" +
		`  tw_name=$(tmux display-message -p -t "$TMUX_PANE" '#{window_name}' 2>/dev/null) || tw_name=''` + "\n" +
		`  case "$tw_name" in ` + shellQuote(tmux.WaitingMarker) + `*) tmux rename-window -t "$TMUX_PANE" "${tw_name#?}" 2>/dev/null || true ;; esac` + "\n" +
		"fi\n" +
		`printf '\n"%s" exited %s — this window is kept so the output above stays readable\n' "$tw_command" "$tw_status"` + "\n" +
		"printf '" + heldOpenNotice + "\\n'\n" +
		"read -r tw_done\n" +
		`exit "$tw_status"` + "\n"
}

// neverGotGoing is how long a command may run and still be read as one that
// never got going, in seconds.
//
// The question the fallback has to answer is "did the agent ever start", and
// treewright cannot ask it: whether a conversation exists is the agent's own
// fact, and every proxy treewright has kept for it drifted from it. How long the
// command ran is the one honest signal that belongs to nobody in particular. Any
// agent's *nothing to resume* is a message and an exit, over in well under a
// second; any agent a person actually used ran for minutes.
//
// Five seconds sits in the gap. It has to clear the slow end of a start that
// fails — a launcher script, a runtime coming off a cold page cache, a config
// read over a network home directory — and it has to stay far below anything a
// person worked in. What it must never do is fire on a session that ran and then
// died, because starting a fresh agent over that takes the alternate screen and
// erases the stack trace the user needs, which is the whole reason the held-open
// window exists.
//
// The alternative was matching the agent's own "no conversation found" on
// stderr. It reads as more precise and is worth less: it is an undocumented
// string from another program, in one language, for one agent — the coupling
// this mechanism exists to remove — and it fails silently on the day that
// program rewords it.
const neverGotGoing = 5

// freshOnFastFailure renders resume's two commands as one script: the resume
// command, and behind it the fresh one, run only when the first failed without
// ever getting going.
//
// The clock is `date +%s`, twice, because it is the one clock every shell tmux
// might run this through agrees on: $SECONDS is bash and zsh but not dash, and
// timing the command from Go is not available to a script treewright is no
// longer around to watch. It is read defensively — a missing or unusable date
// leaves the elapsed time unknown, and unknown means no fallback, which is
// exactly today's behavior. Second granularity is ample for a threshold of
// seconds.
//
// Nothing inside the `if` is indented, because a command reaching here can carry
// a prompt with newlines in it: indenting the block would put spaces inside the
// user's own quoted text.
func freshOnFastFailure(resume, fresh string) string {
	return "tw_started=$(date +%s 2>/dev/null)\n" +
		runStep(resume) +
		"tw_ended=$(date +%s 2>/dev/null)\n" +
		"tw_brief=0\n" +
		`case "$tw_started,$tw_ended" in *[!0-9,]*|,*|*,) ;; *) [ "$((tw_ended - tw_started))" -lt ` +
		strconv.Itoa(neverGotGoing) + ` ] && tw_brief=1 ;; esac` + "\n" +
		`if [ "$tw_brief" -eq 1 ] && [ "$tw_status" -gt 0 ] && [ "$tw_status" -le 128 ]; then` + "\n" +
		`printf '\n"%s" exited %s straight away — starting a fresh agent instead\n' "$tw_command" "$tw_status"` + "\n" +
		runStep(fresh) +
		"fi\n"
}

// heldOpenNotice is the last thing a held-open window prints before it blocks
// on Enter, and so the last line such a window shows for as long as it stands.
//
// It is a constant because it is read as well as written. `send` captures a
// pane before typing into it, and a window in this state has a shell in it
// rather than an agent — where the message would be text nobody reads and the
// Enter after it would close the window, taking with it the output the wrapper
// exists to preserve. Recognizing it costs nothing, the capture having been
// taken anyway.
const heldOpenNotice = "press Enter to close it"

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
// What is measured is the whole script, rendered by the caller, because that is
// what tmux is handed — and for `resume` that is two commands and a prompt in
// each of them rather than one. A check that measured the setting alone would
// pass an invocation tmux then refuses, which arrives as tmux's raw "command too
// long" over a worktree that already exists.
func checkCommandFits(script, key, prompt string) error {
	size := len(script)
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
