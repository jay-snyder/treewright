// Package tmux wraps the handful of tmux commands treewright drives.
//
// Every window treewright opens belongs to one repository, and each repository has
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
// lets treewright act on a window that turns out to be in the wrong session.
//
// Which worktree a window belongs to is recorded on the window itself, as the
// user option @treewright_worktree, rather than read off the directory its shell
// happens to be standing in. A pane's directory moves with every cd, and two
// windows can stand in one directory at once — the base window does exactly that
// after `treewright cd` — so identity has to come from something the user cannot
// change by walking around.
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// Spec describes a window to open: where it goes, what runs in it, and what
// treewright records on it.
//
// Slug and Branch are empty for the base window, which sits on a checkout rather
// than on a worktree, and so has neither.
type Spec struct {
	Session string // the session it belongs in
	Dir     string // the worktree it opens on
	Name    string // as shown in the status line, e.g. "ENG-142"
	Command string // what to run in it; blank leaves a shell

	Repo   string // the config's name
	Slug   string // names the worktree, when this is one
	Branch string // the branch that worktree is on
}

// MaxCommandLength is the longest command treewright will give a window, in
// bytes.
//
// tmux carries a command from the client to the server in a single imsg, and one
// imsg holds MAX_IMSGSIZE — 16384 bytes — of which 16 are the header and 4 the
// argument count, leaving 16364 for the arguments themselves, each packed with
// its terminating NUL. Past that the command is refused with "command too long"
// and the window is never created. Measured by bisection against tmux 3.7b, the
// boundary sits exactly there, and it is the whole argument list rather than the
// command alone: a longer session name buys a shorter command.
//
// The number here is that ceiling less a kilobyte for the rest of new-window's
// arguments — the session, the worktree's path, the window name, the format
// flags. Measuring those exactly would make the limit a property of the call
// site, and the check that matters happens before there is one: before the
// worktree exists, where a prompt too long to run is still nothing worse than a
// bad invocation. A kilobyte is far more room than those arguments take, and
// what fills this budget is a --prompt — where the difference between 15340
// bytes of prose and 16364 is not one anybody is writing to.
const MaxCommandLength = 16364 - 1024

// ErrNotFollowed reports that treewright could not move the calling client — to a
// window it opened, or to a session it was asked to attach to. Its usual cause is
// a $TMUX inherited from a client that has since detached: the variable outlives
// the client, so treewright believes there is someone to move and tmux disagrees.
//
// Where a window was opened, it is open either way, which is why that caller
// reports this rather than failing on it.
var ErrNotFollowed = errors.New("no tmux client followed")

// Window is one open tmux window: enough to name it in a prompt, to target it,
// and to tell whether it is in the session it ought to be in.
type Window struct {
	ID      string // "@3", unique across the server
	Session string // the session holding it
	Name    string // as shown in the status line, e.g. "ENG-142", minus any waiting marker
	State   string // what the agent in it last signaled, "" when nothing has

	// Worktree is the checkout treewright opened this window on, empty for a
	// window treewright did not open. It is what identifies a window, per the
	// package comment.
	//
	// Kept as the path rather than flattened to a bool because "treewright opened
	// this window" and "treewright opened this window *here*" are different
	// questions, and the window whose pane has wandered into another worktree's
	// directory is exactly the case that tells them apart.
	Worktree string

	// SessionWindows is how many windows the session holding this one has, which
	// is what LastInSession reads.
	//
	// It rides in the pane listing rather than being asked per window, because
	// the callers ask it of a whole table at once: `ls --json` over a dozen
	// worktrees would otherwise pay a dozen extra round trips to the server for
	// a number every one of those panes was already able to report.
	//
	// Only Windows fills it. A window fresh from NewWindow reports 0, and
	// nothing asks — the question is about a session as it stands, and the
	// answer for a window created a moment ago is one nobody is waiting on.
	SessionWindows int
}

// Available reports whether tmux is installed. Every operation here needs it, and
// callers fall back to telling the user what to run by hand.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// Inside reports whether treewright is running inside a tmux client. It decides
// whether there is a client to move: outside one, windows can still be created
// and selected, but nothing can be brought to the foreground.
func Inside() bool { return os.Getenv("TMUX") != "" }

// serverArgs points tmux at a particular server when TREEWRIGHT_TMUX_LABEL names
// one, for a user who runs `tmux -L work` rather than the default server.
//
// Inside a client no flag is needed: tmux reads the socket path out of $TMUX
// itself, so calls already reach the server treewright is running under.
func serverArgs() []string {
	if label := os.Getenv("TREEWRIGHT_TMUX_LABEL"); label != "" {
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

// ServerRunning reports whether a tmux server is up, without starting one.
//
// Most tmux commands start a server when none is running — list-keys does — so a
// command that only reports has to be careful which one it asks: doctor would
// otherwise answer "is anything loaded?" by first creating the empty server that
// makes the answer no. has-session is one of the few that does not start one. With
// no server it fails to connect, and with one it answers about the current
// session, so a bare call is a clean yes or no about the server itself.
func ServerRunning() bool {
	_, err := run("has-session")
	return err == nil
}

// The user options treewright records on every window it opens. Windows treewright did
// not open have none, and read as empty.
//
// Two are read back, and they ride in paneFormat below: the worktree, which is
// what identifies a window, and the agent state, which is what the agent running
// in it last said it was doing. The rest are written for the user's own
// tmux.conf, where "#{@treewright_slug}" in a status line costs nothing to render
// and the alternative is a shell-out to git on every status interval.
//
// The agent state lives on the window rather than in a file because a treewright
// window runs the agent as the window's own command: the agent dying is the
// window closing is the option evaporating, so a crashed agent can never leave a
// stale "working" behind and there is nothing to garbage-collect. It is written
// by `treewright signal`, which the agent's own hooks run.
// AgentStateOption and WaitingMarker are exported for the one consumer outside
// this package: the held-open wrapper in internal/cli, which clears both from
// shell when a window outlives the agent that was signaling in it.
const (
	worktreeOption = "@treewright_worktree"
	repoOption     = "@treewright_repo"
	slugOption     = "@treewright_slug"
	branchOption   = "@treewright_branch"

	// AgentStateOption holds what the agent in a window last signaled.
	AgentStateOption = "@treewright_agent_state"

	// WaitingMarker is prefixed to a window's name while its agent is waiting on
	// the user, so the one state that needs to reach you across the room shows in
	// any status line with nothing added to tmux.conf. Windows returns names with
	// the marker already stripped: the marker is display, not identity, and every
	// message, table cell, and JSON field wants the name underneath it.
	WaitingMarker = "!"
)

// paneFormat lists a pane as window id, session, window name, the worktree its
// window was opened on, the agent state recorded on it, how many windows its
// session holds, then path.
//
// The fields are tab-separated and the path comes last, because both a window
// name and a path may contain spaces: splitting on a space is only unambiguous
// for the id. Splitting into a fixed seven parts leaves any tab in a path — rare,
// but possible — inside the path where it belongs. The stamped worktree is a path
// too, which is why stamp declines to write one holding a tab; the agent state is
// a word from signal's closed vocabulary and the window count is a number, and
// neither can hold one.
//
// The window count is here rather than asked per window because this listing is
// gathered once for a whole table. See Window.SessionWindows.
const paneFormat = "#{window_id}\t#{session_name}\t#{window_name}\t#{" + worktreeOption + "}\t#{" + AgentStateOption + "}\t#{session_windows}\t#{pane_current_path}"

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
// several panes share a directory — the base window standing in a worktree's
// worktree after `treewright cd` is the everyday case — Window.beats decides
// between them.
func Windows(prefer string) map[string]Window {
	out, err := run("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		// No server running, or no tmux at all: nothing is open.
		return nil
	}
	return parsePanes(out, prefer)
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
	best := make(map[string]Window)
	stake := func(dir string, w Window) {
		if dir == "" {
			return
		}
		if held, taken := best[dir]; taken && !w.beats(held, dir, prefer) {
			return
		}
		best[dir] = w
	}

	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) != 7 {
			continue
		}
		// A count that does not parse is left at zero, which reads as "not the
		// last window": the only thing it feeds is a caveat about ending a
		// session, and saying nothing is the right way to be wrong about that.
		windows, _ := strconv.Atoi(fields[5])
		w := Window{
			ID:      fields[0],
			Session: fields[1],
			// The waiting marker comes off here, once, so the name every consumer
			// sees is the one underneath treewright's own punctuation.
			Name:           strings.TrimPrefix(fields[2], WaitingMarker),
			State:          fields[4],
			Worktree:       fields[3],
			SessionWindows: windows,
		}
		if w.ID == "" {
			continue
		}
		stake(fields[6], w)
		// A window treewright opened answers for its own worktree wherever its pane
		// is standing, so cd-ing a pane out of the directory no longer orphans the
		// worktree's window and has a second one opened beside it.
		stake(w.Worktree, w)
	}

	if len(best) == 0 {
		return nil
	}
	return best
}

// NewSession creates a repository's session, detached, holding one window. The
// session is created detached even when treewright is running inside tmux, because
// moving the client to it is Focus's job and a caller may not want to be moved.
func NewSession(s Spec) (Window, error) {
	args := []string{"new-session", "-d", "-s", s.Session, "-c", s.Dir, "-n", s.Name, "-P", "-F", "#{window_id}"}
	return newWindow(s, args)
}

// NewWindow opens a window in an existing session.
func NewWindow(s Spec) (Window, error) {
	args := []string{"new-window", "-t", exact(s.Session) + ":", "-c", s.Dir, "-n", s.Name, "-P", "-F", "#{window_id}"}
	return newWindow(s, args)
}

// newWindow runs a window-creating command and settles the new window's options.
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
// other window entirely. Every option below is set the same way, and for the same
// reason.
//
// All of it is best-effort. The window exists once tmux has answered with its id,
// and a window that is merely missing a stamp still works: it is matched by the
// directory its pane is standing in, which is what every window was matched by
// before stamps existed.
func newWindow(s Spec, args []string) (Window, error) {
	if strings.TrimSpace(s.Command) != "" {
		args = append(args, s.Command)
	}
	id, err := run(args...)
	if err != nil {
		return Window{}, err
	}
	// tmux switches automatic-rename off by itself when -n names a window, so this
	// only matters on versions that do not.
	_, _ = run("set-window-option", "-t", id, "automatic-rename", "off")

	// The worktree stamp is what makes a window's identity survive the user:
	// rearranging windows no longer changes which one a worktree resolves to, a
	// pane that cd's elsewhere keeps the window it belongs to, and a second pane
	// that cd's in does not take the worktree's window over.
	stamp(id, worktreeOption, s.Dir)
	stamp(id, repoOption, s.Repo)
	stamp(id, slugOption, s.Slug)
	stamp(id, branchOption, s.Branch)

	return Window{ID: id, Session: s.Session, Name: s.Name, Worktree: s.Dir}, nil
}

// Stamped reports that treewright opened this window. It gates the things
// treewright may do to its own windows but not to one the user happened to open
// on a worktree's directory, such as decorating the name.
func (w Window) Stamped() bool { return w.Worktree != "" }

// LastInSession reports that this is the only window in its session, so that
// closing it ends the session too — which moves an attached client to another
// session or detaches it altogether. Worth saying before it happens.
func (w Window) LastInSession() bool { return w.SessionWindows == 1 }

// rank scores how strong w's claim on dir is. A window treewright opened on this
// very worktree says so; a window treewright did not open says nothing; and a
// window opened on a different worktree is positive evidence against — its shell
// has wandered in here, but it is still the other worktree's window, and closing
// it or switching to it in this one's name would be wrong.
func (w Window) rank(dir string) int {
	switch w.Worktree {
	case dir:
		return 2
	case "":
		return 1
	default:
		return 0
	}
}

// beats reports whether w is a better answer for dir than held.
//
// Rank first, then the repository's own session, then the older window. Nothing
// consults the order of the listing, which is the bug this replaced:
// list-panes -a walks windows in index order, so two windows standing in one
// directory resolved to whichever the user had arranged first — a wrong name in
// `ls`, the wrong window focused by `resume`, and the wrong window offered up for
// closing by `rm`, all changing under a swap-window.
func (w Window) beats(held Window, dir, prefer string) bool {
	if a, b := w.rank(dir), held.rank(dir); a != b {
		return a > b
	}
	if a, b := w.Session == prefer, held.Session == prefer; a != b {
		return a
	}
	return olderWindow(w.ID, held.ID)
}

// SetAgentState records what the agent in a window says it is doing, or clears
// the record when state is empty.
//
// Cleared means unset rather than written as "": an option that was never set is
// the honest record for a window nothing is signaling about, and it is also what
// keeps the base case — no agent anywhere — indistinguishable from a fresh
// window, which is what it is. -q on the unset, because unsetting an option that
// is not there is the caller saying "make it so", not a mistake to report.
func SetAgentState(id, state string) error {
	if state == "" {
		_, err := run("set-window-option", "-q", "-u", "-t", id, AgentStateOption)
		return err
	}
	_, err := run("set-window-option", "-t", id, AgentStateOption, state)
	return err
}

// SetWaitingMarker puts the attention marker on a window's name, or takes it
// off. Renaming is unconditional in either direction: Windows strips the marker
// as it parses, so whether the name on the server carries one right now is the
// one thing the caller cannot know — and renaming a window to the name it
// already has is a no-op tmux does not mind.
//
// Callers gate this on Window.Stamped. The name of a window treewright opened is
// treewright's to decorate; the name of one the user opened on a worktree's
// directory is not, however good the cause.
func SetWaitingMarker(w Window, waiting bool) error {
	name := w.Name
	if waiting {
		name = WaitingMarker + name
	}
	_, err := run("rename-window", "-t", w.ID, name)
	return err
}

// stamp records one value on a window as a user option.
//
// An empty value is left unset rather than written as "": tmux answers with the
// empty string either way, and an option that was never set is the honest record
// of something treewright does not know — the base window's slug and branch.
//
// A value holding a tab goes unstamped, because the worktree comes back as one
// tab-separated field of the pane listing, where only the last field, the path,
// can carry one. The other options are not read back and so could carry a tab
// safely, but one rule for all four is easier to keep true than three.
func stamp(id, option, value string) {
	if value == "" || strings.ContainsRune(value, '\t') {
		return
	}
	_, _ = run("set-window-option", "-t", id, option, value)
}

// CurrentSession names the session the calling client is attached to, or "" when
// there is no client to ask.
//
// The caller's own pane is what gets asked, through the $TMUX_PANE every pane
// carries, rather than tmux being left to work out who is calling. Untargeted,
// display-message answers about the most recently active session — which is the
// caller's own only by coincidence, and the coincidence breaks in exactly the
// case that matters here: another session created or touched a moment ago is the
// one tmux names, so "am I already in this session?" comes back yes about a
// session the client is not in, and the move that should have happened does not.
func CurrentSession() string {
	if !Inside() {
		return ""
	}
	args := []string{"display-message", "-p", "#{session_name}"}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		args = []string{"display-message", "-p", "-t", pane, "#{session_name}"}
	}
	name, err := run(args...)
	if err != nil {
		// A $TMUX_PANE left over from a pane that has closed. Reporting no session
		// is right: the caller then tries to move a client and finds out for
		// certain, rather than acting on a name tmux guessed.
		return ""
	}
	return name
}

// CurrentWindow names the window the calling pane is in, or "" when treewright
// cannot tell — outside tmux, or in a pane tmux no longer knows about.
//
// The caller's own pane is asked through $TMUX_PANE, for the reason CurrentSession
// gives: untargeted, display-message answers about the most recently active
// window, which is the caller's own only by coincidence. A missing $TMUX_PANE is
// answered with "" rather than with that guess, because the caller is deciding
// whether a window it found is merely the one it is being typed into, and a
// guessed id would have it disown a window that is nothing to do with the caller.
func CurrentWindow() string {
	if !Inside() {
		return ""
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return ""
	}
	id, err := run("display-message", "-p", "-t", pane, "#{window_id}")
	if err != nil {
		return ""
	}
	return id
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
	return SwitchTo(w.Session)
}

// SwitchTo moves the calling client to a session, leaving whichever window is
// current there current. That is what separates attaching to a repository from
// selecting one of its windows: you arrive where you left off, not somewhere
// treewright chose.
func SwitchTo(session string) error {
	if _, err := run("switch-client", "-t", exact(session)); err != nil {
		// Both wrapped: callers match on ErrNotFollowed, and tmux's own refusal is
		// the half that says why, so neither should be flattened into text.
		return fmt.Errorf("%w: %w", ErrNotFollowed, err)
	}
	return nil
}

// AttachArgs is the argument list that attaches a terminal to a session, server
// flags included.
//
// Returned rather than run, because attaching is the one tmux command treewright
// does not want the output of: tmux takes the terminal over for as long as the
// client stays attached, so the caller has to hand it the streams it inherited
// instead of the pipes run uses.
func AttachArgs(session string) []string {
	return append(serverArgs(), "attach-session", "-t", exact(session))
}

// HasBindings reports whether any key binding runs treewright, which is how the
// tmux-side integration announces itself. Unlike the shell integration, which
// can only be inferred, a binding is a thing the server holds and can be asked
// about.
//
// The error is a server that did not answer, which is not the same as an
// integration that is not loaded — there is nothing for it to be loaded into yet.
func HasBindings() (bool, error) {
	out, err := run("list-keys")
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "treewright"), nil
}

// ServerOption reads a global user option — a name beginning with "@" — off the
// running server, or "" when nothing has set it.
//
// It takes the name rather than declaring one, because the options treewright
// asks about here are set by text treewright prints for a server to source:
// internal/tmuxinit owns the line, so it owns the spelling, and this is only the
// reader. tmux answers an unset user option with an error rather than an empty
// value, which is the same "nobody set it" this returns "" for.
//
// Only ask once a server is known to be running: like list-keys, show-options
// starts one otherwise, and a question about what a server holds would bring the
// emptiness it then reports into existence.
func ServerOption(name string) string {
	value, err := run("show-options", "-gv", name)
	if err != nil {
		return ""
	}
	return value
}

// BoundKeys reports which prefix keys currently run treewright's two popups, as
// the server holds them, with "" for either that nothing is bound to.
//
// It exists so that reloading the integration can put the bindings back on the
// keys they are on rather than on the defaults. A user who moved them with
// --resume-key did so in their tmux.conf, and a reload that silently bound T and
// N as well would hand back the keys they deliberately gave up — including,
// where those keys are now something else of theirs, by taking them.
//
// The same caveat as ServerOption: ask only once a server is up, since list-keys
// starts one.
func BoundKeys() (resume, create string) {
	out, err := run("list-keys", "-T", "prefix")
	if err != nil {
		return "", ""
	}
	// The same match popup.go uses to name a key in a hint. " new " and " resume"
	// each appear in one binding's command and in neither's surroundings: the
	// prompt in the new binding spells "new worktree:" against a quote rather
	// than a space.
	return parseBoundKey(out, "treewright", " resume"), parseBoundKey(out, "treewright", " new ")
}

// Prefix is the key that introduces a tmux binding, as tmux spells it — "C-b"
// by default, whatever the user chose otherwise. Empty outside tmux.
func Prefix() string {
	if !Inside() {
		return ""
	}
	key, err := run("show-options", "-g", "-v", "prefix")
	if err != nil {
		return ""
	}
	return key
}

// KeyBoundTo names the prefix key whose command mentions all of match, or ""
// when none does.
//
// It exists so that treewright can tell a reader which key does the thing being
// described, without knowing which key that is: the bindings are the user's to
// move, and the ones printed by tmux-init can be renamed on the way in. Asking
// the server is the only answer that stays true.
//
// Nothing is asked outside tmux, both because a hotkey is no use to someone who
// is not in it and because list-keys starts a server when none is running —
// which would be a mention of a keyboard shortcut bringing a tmux server into
// existence.
func KeyBoundTo(match ...string) string {
	if !Inside() {
		return ""
	}
	out, err := run("list-keys", "-T", "prefix")
	if err != nil {
		return ""
	}
	return parseBoundKey(out, match...)
}

// parseBoundKey picks the key out of a list-keys listing. Split out because the
// shape of that listing is the only thing here worth testing, and it can be
// tested without a server.
//
// Every line reads "bind-key [-r] -T <table> <key> <command...>", so the key is
// whatever follows the table name, and the command is the rest.
func parseBoundKey(listing string, match ...string) string {
	for line := range strings.SplitSeq(listing, "\n") {
		fields := strings.Fields(line)
		// -T is followed by the table's name, then the key, then the command.
		table := slices.Index(fields, "-T")
		if table < 0 || table+3 >= len(fields) {
			continue
		}
		command := strings.Join(fields[table+3:], " ")
		if !containsAll(command, match...) {
			continue
		}
		return unquoteKey(fields[table+2])
	}
	return ""
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// unquoteKey undoes the quoting list-keys applies to a key that would otherwise
// be ambiguous in its own output: ";" comes back as "\;", and "C-;" as the
// double-quoted "C-;". What a reader has to press is neither of those.
func unquoteKey(key string) string {
	if len(key) > 1 && strings.HasPrefix(key, `"`) && strings.HasSuffix(key, `"`) {
		key = key[1 : len(key)-1]
	}
	return strings.TrimPrefix(key, `\`)
}

// Source loads a block of tmux configuration into the running server.
//
// tmux reads configuration from a file rather than from a pipe, so this writes a
// temporary one and points source-file at it. The file is the same text
// `treewright tmux-init` prints, so what a user reads is what a server gets.
func Source(conf string) error {
	f, err := os.CreateTemp("", "treewright-*.tmux")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(conf); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = run("source-file", f.Name())
	return err
}

// PopupEnv is set on whatever a popup runs, so it can tell where it is. tmux
// offers nothing that distinguishes a popup from an ordinary pane, and the
// difference matters to anything about to print a message the popup will hold on
// screen afterwards.
const PopupEnv = "TREEWRIGHT_POPUP"

// Popup runs a command in a tmux popup of an exact size, in dir, drawn on client.
//
// The size is given in cells because that is all tmux accepts — -w and -h take a
// number or a percentage of the terminal, and a percentage is the wrong unit for
// anything whose contents do not grow with the terminal.
//
// client matters whenever this is reached from outside tmux, which is how a key
// binding gets here: run-shell spawns a process with no association to the client
// that triggered it, so tmux falls back to the most recently active one, and with
// two terminals attached the popup opens over whichever has been busier. Naming
// the client puts it where the key was pressed. An empty client leaves the choice
// to tmux, which is right when there is only one.
//
// -EE rather than -E: a single -E closes the popup however the command exited, so
// anything it reported on the way out is gone before it can be read. Doubled,
// tmux closes it only on success.
func Popup(client, dir, command string, width, height int) error {
	full := append(serverArgs(), popupArgs(client, dir, command, width, height)...)
	cmd := exec.Command("tmux", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}
	// display-popup exits with the status of whatever ran inside it. Under -EE
	// that is doubly true: the popup stays up on a failure, so by the time this
	// returns the user has read the message and dismissed it themselves. Passing
	// that status on made the caller fail too, and a key binding running through
	// run-shell then put tmux's own "returned 1" notice on screen — a second
	// overlay reporting, in the abstract, what the first had just said plainly.
	//
	// tmux's own refusals are different and worth keeping: no client to draw on,
	// a size it will not take. Those arrive with a message on stderr, where the
	// inner command's exit status arrives with nothing.
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return fmt.Errorf("tmux %s: %s (%w)", strings.Join(full, " "), msg, err)
	}
	return nil
}

// popupArgs builds that command line. Split out because opening a popup needs a
// client to draw on, which a headless test has none of — so the flags are checked
// here instead, where they can be.
func popupArgs(client, dir, command string, width, height int) []string {
	args := []string{
		"display-popup", "-EE",
		"-w", strconv.Itoa(width), "-h", strconv.Itoa(height),
		// So the command can tell it is running in a popup. It changes what is
		// worth saying: a popup that stays open because the command failed has
		// nothing on screen to say how to dismiss it, where a terminal needs no
		// such advice and would be baffled by it.
		"-e", PopupEnv + "=1",
	}
	if client != "" {
		args = append(args, "-c", client)
	}
	if dir != "" {
		args = append(args, "-d", dir)
	}
	return append(args, command)
}

// KillWindow closes a window by id.
func KillWindow(id string) error {
	_, err := run("kill-window", "-t", id)
	return err
}

// Send types one line into a window and submits it, as someone at that window's
// keyboard would. It is how a message reaches an agent whose window is already
// open, that agent being an ordinary TUI on an ordinary tty.
//
// Two calls, and both details are load-bearing in the same direction — tmux
// reads its arguments as key names unless told otherwise. Without -l a message
// beginning "Enter the amount" arrives as a keypress and three words; with -l on
// the submit, Enter arrives as five characters. Each failure is silent, and each
// puts something in somebody's session that they did not send.
//
// "--" ends the flags, so a message that starts with a dash is a message rather
// than an option tmux does not have.
//
// One line is the caller's to enforce: Enter is what submits, so a newline in
// the text posts the rest of it as further turns. Nothing here can tell whether
// that was meant, which is why cmdSend refuses it rather than this splitting it.
func Send(id, text string) error {
	if _, err := run("send-keys", "-t", id, "-l", "--", text); err != nil {
		return err
	}
	_, err := run("send-keys", "-t", id, "Enter")
	return err
}

// Capture returns the last lines of what a window's pane is showing.
//
// It is what makes "look before you type" possible: an agent sitting on a
// question with options takes the next keystrokes as the answer to it, so a
// message sent blind can pick an option nobody read. Reading the pane changes
// nothing and costs one tmux call.
//
// Only the visible pane is captured, not its scrollback, since the question is
// what the window is showing now.
func Capture(id string, lines int) (string, error) {
	out, err := run("capture-pane", "-p", "-t", id)
	if err != nil {
		return "", err
	}
	return lastLines(out, lines), nil
}

// lastLines keeps the final n lines of a capture, dropping the blank ones a
// pane's unused bottom half contributes. Split out because it is the only part
// of Capture worth testing, and it needs no server to test.
func lastLines(out string, n int) string {
	all := strings.Split(out, "\n")
	for len(all) > 0 && strings.TrimSpace(all[len(all)-1]) == "" {
		all = all[:len(all)-1]
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return strings.Join(all, "\n")
}
