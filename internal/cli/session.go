package cli

import (
	"errors"
	"strings"

	"github.com/jay-snyder/treemux/internal/config"
	"github.com/jay-snyder/treemux/internal/tmux"
)

// Each repository gets a tmux session of its own, named after its config, and
// every window treemux opens goes into that session.
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

// openWindow puts a window on dir in the repository's session, running command
// under the name name — or focuses the window already sitting in dir, wherever it
// turns out to be.
//
// The session is created when it does not exist yet, so the first `new` of the
// day is also what establishes the repository's session. Nothing here needs a
// client: outside tmux the window is still created and left current, and the
// caller is told how to attach.
func openWindow(env *Env, cfg *config.Config, dir, name, command string) error {
	if !tmux.Available() {
		env.progressf("tmux is not installed — cd %s and run %s yourself", dir, command)
		return nil
	}
	session := sessionFor(cfg)

	// A window already sitting in that directory is the session being asked for,
	// so switch to it rather than opening a duplicate beside it.
	if w, ok := tmux.Windows(session)[dir]; ok {
		if w.Session != session {
			// Someone's own window, or one opened before this repo had a session
			// of its own. Switching to it is still better than opening a second
			// window on the same directory, but it is worth saying where it went.
			env.warnf("window %s is in session %s rather than %s — switching to it there",
				w.Name, w.Session, session)
		}
		return focusWindow(env, w, command)
	}

	var (
		w   tmux.Window
		err error
	)
	if tmux.HasSession(session) {
		w, err = tmux.NewWindow(session, dir, name, command)
	} else {
		w, err = tmux.NewSession(session, dir, name, command)
		if err == nil {
			env.progressf("created tmux session %s, which holds %s's windows", session, cfg.Name)
		}
	}
	if err != nil {
		return err
	}
	return focusWindow(env, w, command)
}

// focusWindow brings a window to the foreground, or says how to reach it when
// there is no client to move.
//
// Nothing here fails the command. The window exists by this point, created or
// found, so a client that could not be moved — or a window that has closed since
// — is news to report rather than grounds for calling the whole thing a failure.
func focusWindow(env *Env, w tmux.Window, command string) error {
	switch err := tmux.Focus(w); {
	case errors.Is(err, tmux.ErrNotFollowed):
		env.warnf("could not switch to session %s — attach with: tmux attach -t %s", w.Session, w.Session)
	case err != nil:
		// The window was there a moment ago, so what changed is that it closed:
		// tmux closes a window as soon as its command exits, and a command that
		// exits at once — a typo, a wrapper script that fails — looks exactly like
		// this. Naming the command is what makes that guessable.
		env.warnf("window %s closed as soon as it opened — did %q exit straight away?", w.Name, command)
	case !tmux.Inside():
		env.progressf("window %s is open in tmux session %s — attach with: tmux attach -t %s",
			w.Name, w.Session, w.Session)
	}
	return nil
}
