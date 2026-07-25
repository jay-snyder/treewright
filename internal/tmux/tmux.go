// Package tmux wraps the handful of tmux commands treemux drives.
//
// Every window treemux opens belongs to one repository, and each repository has
// a session of its own — named after its config — so that windows for different
// repositories never share a session. Two of tmux's own rules shape this API:
//
// Session names are matched as prefixes. With only "api-gateway" running, a
// target of "api" resolves to it, so a repo named "api" would silently drop its
// windows into the other repository's session. Every session target here is
// therefore written in tmux's exact form, "=api". The one command that does not
// understand that form is set-option, which is why nothing here sets session
// options.
//
// Window ids ("@3") are unique across the whole server, so a window can be
// selected or killed by id without naming the session holding it — which is what
// lets treemux act on a window that turns out to be in the wrong session.
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ErrNotFollowed reports that a window was opened and selected but the calling
// client could not be moved to it. Its usual cause is a $TMUX inherited from a
// client that has since detached: the variable outlives the client, so treemux
// believes there is someone to move and tmux disagrees. The window is open
// either way, which is why callers report this rather than failing.
var ErrNotFollowed = errors.New("no tmux client followed the window")

// Window is one open tmux window: enough to name it in a prompt, to target it,
// and to tell whether it is in the session it ought to be in.
type Window struct {
	ID      string // "@3", unique across the server
	Session string // the session holding it
	Name    string // as shown in the status line, e.g. "ENG-142"
}

// Available reports whether tmux is installed. Every operation here needs it, and
// callers fall back to telling the user what to run by hand.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// Inside reports whether treemux is running inside a tmux client. It decides
// whether there is a client to move: outside one, windows can still be created
// and selected, but nothing can be brought to the foreground.
func Inside() bool { return os.Getenv("TMUX") != "" }

// serverArgs points tmux at a particular server when TREEMUX_TMUX_LABEL names
// one, for a user who runs `tmux -L work` rather than the default server.
//
// Inside a client no flag is needed: tmux reads the socket path out of $TMUX
// itself, so calls already reach the server treemux is running under.
func serverArgs() []string {
	if label := os.Getenv("TREEMUX_TMUX_LABEL"); label != "" {
		return []string{"-L", label}
	}
	return nil
}

func run(args ...string) (string, error) {
	full := append(serverArgs(), args...)
	cmd := exec.Command("tmux", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("tmux %s: %s (%w)", strings.Join(full, " "), msg, err)
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(full, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// exact renders a session name as a target tmux will match whole rather than as
// a prefix. See the package comment: without it, "api" finds "api-gateway".
//
// It only means "this session" where a session is what the target is for. Given a
// window or pane target it is read as a name to match, and matches nothing —
// silently, with no error — which is why window targets here are either a window
// id or a session followed by a colon.
func exact(session string) string { return "=" + session }

// SessionName makes a name usable as a tmux session name.
//
// Periods and colons are the separators in a target spec — "session:window.pane"
// — so a session whose name contains one cannot be addressed unambiguously even
// though tmux allows it to be created. They become dashes.
func SessionName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == ':' {
			return '-'
		}
		return r
	}, name)
}

// HasSession reports whether a session of exactly this name exists.
func HasSession(session string) bool {
	_, err := run("has-session", "-t", exact(session))
	return err == nil
}

// paneFormat lists a pane as window id, session, window name, then path.
//
// The fields are tab-separated and the path comes last, because both a window
// name and a path may contain spaces: splitting on a space is only unambiguous
// for the id. Splitting into a fixed four parts leaves any tab in a path — rare,
// but possible — inside the path where it belongs.
const paneFormat = "#{window_id}\t#{session_name}\t#{window_name}\t#{pane_current_path}"

// Windows maps each pane's working directory to the window holding it, across
// every session on the server. It is what stops `resume` from opening a second
// window on a worktree that already has one, what fills the WINDOW column of the
// ls table, and what `rm` uses to find the window left pointing at a directory it
// deleted.
//
// The whole mapping is fetched at once so that rendering a table of N worktrees
// costs one tmux invocation rather than N.
//
// Panes outside prefer are included rather than hidden: a window on this repo's
// worktree that ended up in another session is a thing to report and to switch
// to, and pretending it does not exist would open a duplicate beside it. Where
// several panes share a directory, one in prefer wins, and otherwise the earliest
// does, so repeated calls return the same window for the same directory.
func Windows(prefer string) map[string]Window {
	out, err := run("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		// No server running, or no tmux at all: nothing is open.
		return nil
	}
	return parsePanes(out, prefer)
}

// parsePanes turns the pane listing into a directory-to-window map. Split out so
// the field handling and the session preference can be tested without a running
// tmux server.
func parsePanes(out, prefer string) map[string]Window {
	if out == "" {
		return nil
	}
	windows := make(map[string]Window)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) != 4 {
			continue
		}
		w := Window{ID: fields[0], Session: fields[1], Name: fields[2]}
		dir := fields[3]
		if w.ID == "" || dir == "" {
			continue
		}
		if seen, ok := windows[dir]; ok && (seen.Session == prefer || w.Session != prefer) {
			continue
		}
		windows[dir] = w
	}
	return windows
}

// NewSession creates a repository's session, detached, holding one window: dir,
// named name, running command. The session is created detached even when treemux
// is running inside tmux, because moving the client to it is Focus's job and a
// caller may not want to be moved.
func NewSession(session, dir, name, command string) (Window, error) {
	args := []string{"new-session", "-d", "-s", session, "-c", dir, "-n", name, "-P", "-F", "#{window_id}"}
	return newWindow(session, name, args, command)
}

// NewWindow opens a window in an existing session: dir, named name, running
// command.
func NewWindow(session, dir, name, command string) (Window, error) {
	args := []string{"new-window", "-t", exact(session) + ":", "-c", dir, "-n", name, "-P", "-F", "#{window_id}"}
	return newWindow(session, name, args, command)
}

// newWindow runs a window-creating command and settles the new window's name.
//
// The command is handed to tmux as a single argument, so a multi-word value like
// "claude --continue" is run through the shell as written. A blank one is omitted
// entirely rather than passed as an empty string, which would have tmux run
// nothing and close the window immediately.
//
// automatic-rename is then switched off, or tmux renames the window after
// whatever process is running and the chosen name disappears. It is set on the
// new window by id: the window is not necessarily current — it is in a session
// this client may not be attached to — so an untargeted option would land on some
// other window entirely.
func newWindow(session, name string, args []string, command string) (Window, error) {
	if strings.TrimSpace(command) != "" {
		args = append(args, command)
	}
	id, err := run(args...)
	if err != nil {
		return Window{}, err
	}
	// Best-effort: tmux switches automatic-rename off by itself when -n names a
	// window, so this only matters on versions that do not.
	_, _ = run("set-window-option", "-t", id, "automatic-rename", "off")
	return Window{ID: id, Session: session, Name: name}, nil
}

// CurrentSession names the session the calling client is attached to, or "" when
// there is no client to ask.
func CurrentSession() string {
	if !Inside() {
		return ""
	}
	name, err := run("display-message", "-p", "#{session_name}")
	if err != nil {
		return ""
	}
	return name
}

// Focus brings a window to the foreground, following it across sessions.
//
// select-window works with no client attached, so this is still worth doing
// outside tmux: it leaves the window current in its session, and whoever attaches
// later arrives on it. switch-client is the part that needs a client, and is
// skipped when the window is already in the session this one is in.
func Focus(w Window) error {
	if _, err := run("select-window", "-t", w.ID); err != nil {
		return err
	}
	if !Inside() || w.Session == "" || w.Session == CurrentSession() {
		return nil
	}
	if _, err := run("switch-client", "-t", exact(w.Session)); err != nil {
		return fmt.Errorf("%w: %v", ErrNotFollowed, err)
	}
	return nil
}

// KillWindow closes a window by id.
func KillWindow(id string) error {
	_, err := run("kill-window", "-t", id)
	return err
}

// LastInSession reports whether a window is the only one in its session, so that
// closing it ends the session too — which moves an attached client to another
// session or detaches it altogether. Worth saying before it happens.
func LastInSession(id string) bool {
	out, err := run("display-message", "-p", "-t", id, "#{session_windows}")
	return err == nil && out == "1"
}
