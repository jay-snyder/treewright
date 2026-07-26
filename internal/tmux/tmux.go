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
//
// Which worktree a window belongs to is recorded on the window itself, as the
// user option @treemux_worktree, rather than read off the directory its shell
// happens to be standing in. A pane's directory moves with every cd, and two
// windows can stand in one directory at once — the base window does exactly that
// after `treemux cd` — so identity has to come from something the user cannot
// change by walking around.
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

// worktreeOption is the tmux user option carrying the worktree a window was
// opened on. Windows treemux did not open have none, and read as empty.
const worktreeOption = "@treemux_worktree"

// paneFormat lists a pane as window id, session, window name, the worktree its
// window was opened on, then path.
//
// The fields are tab-separated and the path comes last, because both a window
// name and a path may contain spaces: splitting on a space is only unambiguous
// for the id. Splitting into a fixed five parts leaves any tab in a path — rare,
// but possible — inside the path where it belongs. The stamped worktree is a path
// too, which is why stampWorktree declines to write one holding a tab.
const paneFormat = "#{window_id}\t#{session_name}\t#{window_name}\t#{" + worktreeOption + "}\t#{pane_current_path}"

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
// several panes share a directory — the base window standing in a stream's
// worktree after `treemux cd` is the everyday case — claim.beats decides between
// them.
func Windows(prefer string) map[string]Window {
	out, err := run("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		// No server running, or no tmux at all: nothing is open.
		return nil
	}
	return parsePanes(out, prefer)
}

// claim is one pane's case for being the window a directory's commands mean: the
// window it belongs to, and the worktree that window was opened on.
type claim struct {
	Window
	worktree string
}

// rank scores how strong a claim on dir is. A window treemux opened on this very
// worktree says so; a window treemux did not open says nothing; and a window
// opened on a different worktree is positive evidence against — its shell has
// wandered in here, but it is still the other stream's window, and closing it or
// switching to it in this one's name would be wrong.
func (c claim) rank(dir string) int {
	switch c.worktree {
	case dir:
		return 2
	case "":
		return 1
	default:
		return 0
	}
}

// beats reports whether c is a better answer for dir than held.
//
// Rank first, then the repository's own session, then the older window. Nothing
// consults the order of the listing, which is the bug this replaced:
// list-panes -a walks windows in index order, so two windows standing in one
// directory resolved to whichever the user had arranged first — a wrong name in
// `ls`, the wrong window focused by `resume`, and the wrong window offered up for
// closing by `rm`, all changing under a swap-window.
func (c claim) beats(held claim, dir, prefer string) bool {
	if a, b := c.rank(dir), held.rank(dir); a != b {
		return a > b
	}
	if a, b := c.Session == prefer, held.Session == prefer; a != b {
		return a
	}
	return olderWindow(c.ID, held.ID)
}

// olderWindow compares window ids by creation rather than as text, so "@9" comes
// before "@10". tmux never spells one otherwise, but an id that does not parse
// falls back to a string comparison: arbitrary, and stable, which is the property
// being bought here.
func olderWindow(a, b string) bool {
	na, errA := strconv.Atoi(strings.TrimPrefix(a, "@"))
	nb, errB := strconv.Atoi(strings.TrimPrefix(b, "@"))
	if errA == nil && errB == nil {
		return na < nb
	}
	return a < b
}

// parsePanes turns the pane listing into a directory-to-window map. Split out so
// the field handling and the preference order can be tested without a running
// tmux server.
func parsePanes(out, prefer string) map[string]Window {
	if out == "" {
		return nil
	}
	best := make(map[string]claim)
	stake := func(dir string, c claim) {
		if dir == "" {
			return
		}
		if held, taken := best[dir]; taken && !c.beats(held, dir, prefer) {
			return
		}
		best[dir] = c
	}

	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) != 5 {
			continue
		}
		c := claim{
			Window:   Window{ID: fields[0], Session: fields[1], Name: fields[2]},
			worktree: fields[3],
		}
		if c.ID == "" {
			continue
		}
		stake(fields[4], c)
		// A window treemux opened answers for its own worktree wherever its pane
		// is standing, so cd-ing a pane out of the directory no longer orphans the
		// stream's window and has a second one opened beside it.
		stake(c.worktree, c)
	}

	if len(best) == 0 {
		return nil
	}
	windows := make(map[string]Window, len(best))
	for dir, c := range best {
		windows[dir] = c.Window
	}
	return windows
}

// NewSession creates a repository's session, detached, holding one window: dir,
// named name, running command. The session is created detached even when treemux
// is running inside tmux, because moving the client to it is Focus's job and a
// caller may not want to be moved.
func NewSession(session, dir, name, command string) (Window, error) {
	args := []string{"new-session", "-d", "-s", session, "-c", dir, "-n", name, "-P", "-F", "#{window_id}"}
	return newWindow(session, dir, name, args, command)
}

// NewWindow opens a window in an existing session: dir, named name, running
// command.
func NewWindow(session, dir, name, command string) (Window, error) {
	args := []string{"new-window", "-t", exact(session) + ":", "-c", dir, "-n", name, "-P", "-F", "#{window_id}"}
	return newWindow(session, dir, name, args, command)
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
// other window entirely. The worktree stamp is written the same way, and for the
// same reason.
func newWindow(session, dir, name string, args []string, command string) (Window, error) {
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
	stampWorktree(id, dir)
	return Window{ID: id, Session: session, Name: name}, nil
}

// stampWorktree records on the window which worktree it was opened on.
//
// This is what makes the window's identity survive the user: rearranging windows
// no longer changes which one a worktree resolves to, a pane that cd's elsewhere
// keeps the window it belongs to, and a second pane that cd's in does not take
// the stream's window over.
//
// Best-effort, like automatic-rename above. A window without the stamp is matched
// by the directory its pane is standing in, which is what every window was matched
// by before — so a failure here costs accuracy, not function. A directory holding
// a tab goes unstamped for that reason too: the option comes back as one
// tab-separated field of the pane listing, where only the last field, the path,
// can carry one.
func stampWorktree(id, dir string) {
	if dir == "" || strings.ContainsRune(dir, '\t') {
		return
	}
	_, _ = run("set-window-option", "-t", id, worktreeOption, dir)
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
