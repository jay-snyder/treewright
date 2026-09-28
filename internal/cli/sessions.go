package cli

import (
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/refname"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// A record of the conversation each agent standing in the base checkout is
// running, so that resume and restore can reopen that one and not merely the
// latest one there.
//
// Why it has to exist. An agent's "carry on where I left off" — claude's
// --continue — resumes the most recent conversation in a directory, and while
// every checkout had one agent in it the directory was an exact key. Scratch
// windows ended that for the base checkout. A scratch session is a conversation
// in the same directory as the base window's, so `resume base` could pick up a
// scratch window's conversation instead of its own, and a scratch window that
// had closed, or been lost to a restart, had no key at all. What is exact is the
// conversation's own id, and the one party that knows it is the agent: its
// SessionStart hook is handed it at the moment the conversation begins. So
// `session-start` writes it down, and resume and restore read it back.
//
// Why a record is allowed when restore's snapshot was not. The snapshot was
// rejected because every command that opens or closes a window would have had to
// rewrite it, and it would have drifted from a session people rearrange by hand
// all day. This has one writer — the agent whose conversation it names — writing
// at the moments that change the answer, and it records nothing about layout:
// which windows a session should have is still read off the disk, as it always
// was. A worktree needs no record because it still has one agent in it.
//
// **It covers the base window too.** A record kept for scratch windows alone
// would be half a fix: the base window would still resume with --continue, and
// still choose between its own conversation and every scratch session that ran
// after it. So --continue stays the fallback in exactly two places — a base
// window with no record yet, and every worktree.
//
// One file per window, named for the word that reaches it — `base` for the base
// window, which a scratch window can never be called, and the name for a scratch
// window — under .git/treewright/sessions/, beside post_create's logs. Inside
// the repository's own .git, so it is never committed, goes when the repository
// goes, and is findable by anyone who wants it gone. The body is the id and
// nothing else.
//
// Records are kept only where the agent can be resumed by id, which is the
// module's to say (Config.SessionResumeCommand). A record nothing can resume is
// a file with no reader, and a scratch session reopened without one would be
// handed --continue — the base window's conversation, or another scratch
// window's, which is the hole this exists to close.
//
// What ends one:
//
//   - `close <name>`, which is how a person says a scratch session is done,
//     whether or not its window is still open.
//   - Quitting its agent, which the agent's SessionEnd hook reports with the
//     module's QuitReason. Every other ending leaves the record alone, and the
//     one that decides this is `other`: claude ends a session that way when its
//     process is killed, which is what a reboot, a dying tmux server and `close`
//     itself all do to it. Removing the record on any SessionEnd would erase
//     every scratch session at exactly the moment the record exists for.
//   - Nothing, for the base window's. Quitting the base window's agent is not
//     forgetting its conversation: the next `resume base` should carry that one
//     on, as --continue carries on a worktree whose agent was quit. It changes
//     only when the base window starts another session, which rewrites it.
//
// A record left behind by a kill, a server death or a reboot is correct: the
// agent did not finish, so its session comes back. What accumulates instead is a
// scratch session somebody stopped caring about and never quit, which restore
// reopens once and `close` ends for good.

// sessionsDir is where the records live.
func sessionsDir(cfg *config.Config) string {
	return filepath.Join(cfg.MainDir, ".git", "treewright", "sessions")
}

// sessionsKept reports whether this repository records sessions at all. See the
// note above: only where its agent can be resumed by id.
func sessionsKept(cfg *config.Config) bool { return cfg.SessionResumeCommand() != "" }

// recordedSession reads the conversation recorded for the window called name,
// reporting ok=false where there is none worth using: no record, a repository
// that no longer resumes by id, or a body that is not an id.
//
// The body is checked on the way out as well as on the way in, because what is
// read here goes into a shell command line. It is quoted there too — but a file
// anybody can edit is not a source of trusted text, and an id that fails the
// check reads as no record, which is the behavior from before records existed.
func recordedSession(cfg *config.Config, name string) (id string, ok bool) {
	if !sessionsKept(cfg) || !validRecordName(name) {
		return "", false
	}
	body, err := os.ReadFile(filepath.Join(sessionsDir(cfg), name))
	if err != nil {
		return "", false
	}
	id = strings.TrimSpace(string(body))
	if !validSessionID(id) {
		return "", false
	}
	return id, true
}

// recordSession writes down the conversation the window called name is running.
//
// Written aside and renamed into place, so a restore reading it at the wrong
// instant finds the previous id or this one and never half of either. The file
// written aside starts with a dot, which no record name may, so a crash between
// the two steps leaves nothing that reads as a session.
func recordSession(cfg *config.Config, name, id string) error {
	dir := sessionsDir(cfg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".writing-*")
	if err != nil {
		return err
	}
	// A no-op once the rename has happened, and the tidy-up when it has not.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// forgetSession removes the record for the window called name, reporting whether
// there was one to remove. Not finding it is not a failure: it is the state
// being asked for.
func forgetSession(cfg *config.Config, name string) bool {
	if !validRecordName(name) {
		return false
	}
	return os.Remove(filepath.Join(sessionsDir(cfg), name)) == nil
}

// recordedScratch names the scratch sessions this repository has a record of,
// in name order — every usable record but the base window's.
func recordedScratch(cfg *config.Config) []string {
	if !sessionsKept(cfg) {
		return nil
	}
	// ReadDir returns its entries sorted by name, which is the order a listing
	// shows scratch windows in.
	entries, err := os.ReadDir(sessionsDir(cfg))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if name == baseName || !e.Type().IsRegular() {
			continue
		}
		if _, ok := recordedSession(cfg, name); ok {
			names = append(names, name)
		}
	}
	return names
}

// validRecordName reports that name can be a record's file name: `base`, or a
// scratch name, which follows a slug's rules. Those refuse a "/", a leading "."
// and a leading "-", so a name can neither climb out of the directory nor be
// mistaken for the file recordSession writes aside.
func validRecordName(name string) bool {
	return name == baseName || (name != "" && refname.CheckSlug(name) == nil)
}

// maxSessionID caps a recorded id's length. claude's are 36-character UUIDs;
// this is room for another agent's spelling, not a format.
const maxSessionID = 128

// validSessionID accepts what can stand safely as one argument to an agent's
// resume flag: letters, digits, hyphens and underscores, never led by a hyphen —
// which the agent would read as a flag of its own — and of a sane length.
func validSessionID(id string) bool {
	if id == "" || len(id) > maxSessionID || id[0] == '-' {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// scratchSessions is every scratch session of a repository's that a name can
// reach: the windows open now, and the recorded sessions whose windows are not.
//
// A recorded session with no window is carried as a window with a name and no
// id, which is how every consumer of this map already reads "not open" — the
// WINDOW column prints "-", the JSON's window fields come out empty, and send
// says there is nothing to type into. The alternative was a second map beside
// this one, which every lookup of a scratch name would then have had to consult
// in the same order, and one of them would eventually have forgotten.
//
// The open window wins where there are both, since the window is the session.
// tmux.Scratch is still what to ask for windows alone — agentWorkingBeside, say,
// which is about agents at work and has no use for one that is not running.
func scratchSessions(cfg *config.Config) map[string]tmux.Window {
	open := tmux.Scratch(cfg.Name)
	recorded := recordedScratch(cfg)
	if len(recorded) == 0 {
		return open
	}
	all := make(map[string]tmux.Window, len(open)+len(recorded))
	maps.Copy(all, open)
	for _, name := range recorded {
		if _, ok := all[name]; !ok {
			all[name] = tmux.Window{Scratch: name, Repo: cfg.Name}
		}
	}
	return all
}

// sessionHook is the part of a SessionStart or SessionEnd payload treewright
// reads: whose conversation it is, and — at the end — why it ended. The rest
// of the payload is ignored by omission, as hookCall ignores the rest of a tool
// call's, so the shape around these can grow without breaking anything here.
type sessionHook struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
}

// recordThisSession is the part of session-start that is not optional: write
// down the conversation this agent has just begun, when it has begun it in a
// window standing in the base checkout.
//
// Each check is a silence, for the reason every other part of that command is
// silent out of scope — the hook fires in every session the agent has, most of
// them nothing to do with this:
//
//   - A repository that keeps no records has nothing to write.
//   - The window must be one treewright opened for this repository, found
//     through the pane as signal finds it. The pane is the only thing that can
//     say which window this is, the base window and a scratch window standing
//     in the same directory.
//   - It must be the base window or a scratch window. A worktree's window has
//     one agent in its directory, which --continue already resumes exactly.
//   - The agent must be standing in the base checkout. A window holding a shell
//     can have walked into a worktree, and an agent started there is running a
//     conversation in the worktree, which is not the window's to resume.
//
// A record that cannot be written is not said either. The agent reading this
// command's stdout can do nothing about treewright's bookkeeping, and what it
// costs is the behavior from before records existed.
func recordThisSession(env *Env, cfg *config.Config) {
	if !sessionsKept(cfg) || !tmux.Available() {
		return
	}
	w, ok := tmux.CallersWindow()
	if !ok || !w.Stamped() || w.Repo != cfg.Name {
		return
	}
	name := w.Scratch
	if name == "" {
		if w.Worktree != cfg.MainDir {
			return
		}
		name = baseName
	}
	if !inTheBaseCheckout(cfg) {
		return
	}
	var hook sessionHook
	if !readHookPayload(env, &hook) || !validSessionID(hook.SessionID) {
		return
	}
	_ = recordSession(cfg, name, hook.SessionID)
}

// forgetAQuitSession is `signal clear`'s part in the record. When the agent in a
// scratch window ends because a person quit it, that scratch session is over,
// and restore must not bring it back.
//
// Only the quit counts, and that is the finding the whole rule rests on — see
// the note at the top of this file. And only when the record still names the
// session that is ending: a window's record is rewritten whenever it starts
// another, so one naming something else belongs to a later session than this
// one and is not this ending's to remove.
//
// The base window's record is never removed here. Quitting the base agent is
// not forgetting its conversation.
func forgetAQuitSession(env *Env) {
	cfg, err := resolveConfig("")
	if err != nil {
		return
	}
	quit := cfg.SessionQuitReason()
	if quit == "" || !tmux.Available() {
		return
	}
	w, ok := tmux.CallersWindow()
	if !ok || w.Scratch == "" || w.Repo != cfg.Name {
		return
	}
	var hook sessionHook
	if !readHookPayload(env, &hook) || hook.Reason != quit {
		return
	}
	if id, ok := recordedSession(cfg, w.Scratch); ok && id == hook.SessionID {
		forgetSession(cfg, w.Scratch)
	}
}
