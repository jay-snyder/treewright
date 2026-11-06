// Package cmdq is what the cmdq plugin knows that is not about treewright: a
// queue of commands an agent needs a person to run, the list they are run
// from, and the pseudo-terminal each one runs under.
//
// Everything about treewright — who is asking, which key opens the list, the
// guard's verdict on a command aimed at another agent's worktree, the popup,
// doctor's report — is in internal/cli, which calls what core calls. That is
// the plugin rule "a plugin asks treewright": nothing here works out a
// worktree, a window or a config, and nothing here calls git or tmux. See
// docs/plugins.md, whose cmdq section is the design this package carries out.
//
// The properties a person trusts it for are few and each is held by a test:
// only Enter in the list runs a command; the text shown is the text run;
// hidden characters are shown and never sent to the terminal; the pick is an
// entry rather than a row; and nothing announces itself.
package cmdq

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The queue's directories, and the files an entry is made of. The layout is
// promised to nobody — the IDs are what `add` prints and every other verb
// takes — but it is a directory per entry and a file per field, as the script
// this replaced had it, so that a person looking inside can read it.
const (
	pendingDir = "pending"
	historyDir = "history"
	stagingDir = "staging"

	commandFile = "command"
	fromFile    = "from"
	whyFile     = "why"
	cwdFile     = "cwd"
	addedFile   = "added"
	lastFile    = "last"
	outcomeFile = "outcome"
	doneFile    = "done"
	outputFile  = "output"
)

// HistoryKeep is how many entries the history holds. Older ones are deleted
// as newer ones arrive: the history is for "what did I just run", and a
// queue's worth of commands nobody will scroll back to is a directory growing
// for ever.
const HistoryKeep = 50

// Dir is the queue's directory: ${XDG_STATE_HOME:-~/.local/state}/treewright/cmdq.
//
// State rather than cache, unlike the reviews plugin's: a cache is anything
// that can be fetched again, and a queued command cannot be fetched from
// anywhere. The XDG default on macOS too, for the reason reviews gives — one
// convention for everything treewright leaves behind is one place to look.
func Dir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".local", "state", "treewright", "cmdq")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "treewright", "cmdq")
}

// Store is the queue under one directory. Every verb reads it fresh: a queue
// several agents add to and several popups run from has no copy worth keeping
// in memory, and the file system's renames are what keep the readers honest.
type Store struct{ Dir string }

// Exists reports whether the queue has ever been written: whether anybody
// queued a command or opened the list. It is how doctor tells a plugin in use
// from one nobody opted into.
func (s Store) Exists() bool {
	info, err := os.Stat(s.Dir)
	return err == nil && info.IsDir()
}

// Outcome is how an entry left the list. The empty outcome is an entry still
// waiting.
type Outcome string

// The ways an entry leaves the list. The values are what the outcome file
// holds, as the script wrote them.
const (
	Waiting   Outcome = ""
	Ran       Outcome = "ran 0"
	Dismissed Outcome = "dismissed"
	Withdrawn Outcome = "withdrawn"
)

// Entry is one queued command, as it stood when it was read.
//
// Command is read once, here, and is the string Enter runs — never the file
// again. A file rewritten between the list drawing an entry and the person
// pressing Enter on it would otherwise slip a command onto the screen's
// description of another.
type Entry struct {
	ID      string
	Command string
	From    string // who asked: an agent's name, or a repository's
	Why     string
	Dir     string // where it runs
	Added   time.Time

	// Failed is when the last run failed, and Status what it exited with.
	// Zero when no run has failed since the entry was queued, which is every
	// entry in the history but one put back after a failure.
	Failed time.Time
	Status int

	// Outcome and Done say how and when an entry in the history left the
	// list. Both are zero for one still waiting.
	Outcome Outcome
	Done    time.Time

	path string // the directory it was read from
}

// InHistory reports whether the entry has left the list.
func (e Entry) InHistory() bool { return e.Outcome != Waiting }

// Request is what `add` is asked to queue, already checked by its caller.
type Request struct {
	Command string
	From    string
	Why     string
	Dir     string
}

// ErrNoEntry is an ID the queue has never held, or one the history has since
// let go of.
var ErrNoEntry = errors.New("no such entry")

// idPattern is an entry's ID: the time it was queued, to a tenth of a
// millisecond, which sorts as it reads.
var idPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9]{4}$`)

// ValidID reports whether id has an entry ID's shape. Checked before an ID is
// joined to a path, so `wait ../../x` is a malformed ID rather than a
// question about some other directory.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// newID is an ID for an entry queued at t.
func newID(t time.Time) string {
	return t.Format("20060102-150405") + fmt.Sprintf("-%04d", t.Nanosecond()/100_000)
}

// Add queues a command and returns its ID.
//
// The entry is written whole in a staging directory and then renamed into
// the queue, so a list reading the queue meanwhile sees all of it or none.
// The rename is also what keeps IDs unique under concurrent adds: renaming a
// directory onto one that exists fails rather than replacing it, so two
// agents queuing in the same tenth of a millisecond cannot take one ID, and
// the second simply tries the next.
func (s Store) Add(r Request, now func() time.Time) (string, error) {
	for _, d := range []string{pendingDir, historyDir, stagingDir} {
		if err := os.MkdirAll(filepath.Join(s.Dir, d), 0o700); err != nil {
			return "", err
		}
	}
	stage, err := os.MkdirTemp(filepath.Join(s.Dir, stagingDir), "new-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }() // nothing left once it has moved

	files := []struct{ name, body string }{
		{commandFile, r.Command}, // as given, to the byte: it is what runs
		{fromFile, r.From + "\n"},
		{whyFile, r.Why + "\n"},
		{cwdFile, r.Dir + "\n"},
		{addedFile, stamp(now())},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(stage, f.name), []byte(f.body), 0o600); err != nil {
			return "", err
		}
	}

	const attempts = 20
	for range attempts {
		id := newID(now())
		if _, taken := s.historyPath(id); !taken {
			err := os.Rename(stage, filepath.Join(s.Dir, pendingDir, id))
			if err == nil {
				return id, nil
			}
			if !errors.Is(err, fs.ErrExist) && !errors.Is(err, syscall.ENOTEMPTY) {
				return "", err
			}
		}
		// The next tenth of a millisecond is the next ID.
		time.Sleep(100 * time.Microsecond)
	}
	return "", errors.New("could not find a free ID for the entry")
}

// Pending lists the entries waiting, newest first — which is the order the
// script showed them in, and the one where a command queued while the list is
// open arrives at the top rather than somewhere in the middle.
func (s Store) Pending() ([]Entry, error) {
	return s.read(pendingDir)
}

// History lists the entries that have left the list, the most recently gone
// first.
func (s Store) History() ([]Entry, error) {
	return s.read(historyDir)
}

func (s Store) read(sub string) ([]Entry, error) {
	dirents, err := os.ReadDir(filepath.Join(s.Dir, sub))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, d := range dirents {
		if d.IsDir() {
			names = append(names, d.Name())
		}
	}
	// Names sort as the times they hold, so newest first is the names in
	// reverse — for the history too, whose names lead with when an entry left.
	slices.Sort(names)
	slices.Reverse(names)
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		e, ok := readEntry(filepath.Join(s.Dir, sub, name))
		if ok {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// Find reads one entry by ID, wherever it is now.
func (s Store) Find(id string) (Entry, error) {
	if !ValidID(id) {
		return Entry{}, ErrNoEntry
	}
	// Twice, the pending side last: an entry put back from the history moves
	// the other way between the two looks, and a rename is never in neither
	// place, so one of the three finds it.
	for _, look := range []func() (string, bool){
		func() (string, bool) { return s.pendingPath(id) },
		func() (string, bool) { return s.historyPath(id) },
		func() (string, bool) { return s.pendingPath(id) },
	} {
		if path, ok := look(); ok {
			if e, ok := readEntry(path); ok {
				return e, nil
			}
		}
	}
	return Entry{}, ErrNoEntry
}

func (s Store) pendingPath(id string) (string, bool) {
	path := filepath.Join(s.Dir, pendingDir, id)
	info, err := os.Stat(path)
	return path, err == nil && info.IsDir()
}

// historyPath finds an entry in the history, whose directories are named for
// when the entry left followed by its own ID.
func (s Store) historyPath(id string) (string, bool) {
	matches, _ := filepath.Glob(filepath.Join(s.Dir, historyDir, "*_"+id))
	if len(matches) == 0 {
		return "", false
	}
	return matches[len(matches)-1], true
}

// Retire moves a waiting entry to the history with the outcome given, and
// lets the history's oldest go. ErrNoEntry is an entry that had already left
// the list, which another popup running or dismissing it is enough to cause.
func (s Store) Retire(id string, how Outcome, now time.Time) error {
	path, ok := s.pendingPath(id)
	if !ValidID(id) || !ok {
		return ErrNoEntry
	}
	if err := os.WriteFile(filepath.Join(path, outcomeFile), []byte(string(how)+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(path, doneFile), []byte(stamp(now)), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, historyDir), 0o700); err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(s.Dir, historyDir, newID(now)+"_"+id)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNoEntry
		}
		return err
	}
	s.prune()
	return nil
}

// Restore puts an entry from the history back on the list, as it was before
// it left: its last failure and its output go with it, its outcome does not.
func (s Store) Restore(id string) error {
	path, ok := s.historyPath(id)
	if !ValidID(id) || !ok {
		return ErrNoEntry
	}
	_ = os.Remove(filepath.Join(path, outcomeFile))
	_ = os.Remove(filepath.Join(path, doneFile))
	if err := os.Rename(path, filepath.Join(s.Dir, pendingDir, id)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNoEntry
		}
		return err
	}
	return nil
}

// RecordFailure notes a run that failed, which leaves the entry waiting. A
// `wait` that began before at reads it as its answer.
func (s Store) RecordFailure(id string, status int, at time.Time) error {
	path, ok := s.pendingPath(id)
	if !ValidID(id) || !ok {
		return ErrNoEntry
	}
	return os.WriteFile(filepath.Join(path, lastFile), []byte(stampOf(at)+" "+strconv.Itoa(status)+"\n"), 0o600)
}

// KeepOutput stores what a run left for `status`, wherever the entry is now,
// replacing the last run's. Empty output removes it: a run that printed
// nothing has nothing to show, and the previous run's lines shown under it
// would be read as this one's.
func (s Store) KeepOutput(id, output string) error {
	e, err := s.Find(id)
	if err != nil {
		return err
	}
	path := filepath.Join(e.path, outputFile)
	if output == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(output), 0o600)
}

// Output is what the entry's last run left, as KeepOutput stored it.
func (s Store) Output(e Entry) string {
	data, err := os.ReadFile(filepath.Join(e.path, outputFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// prune deletes the history's oldest entries past HistoryKeep.
func (s Store) prune() {
	dirents, err := os.ReadDir(filepath.Join(s.Dir, historyDir))
	if err != nil {
		return
	}
	var names []string
	for _, d := range dirents {
		if d.IsDir() {
			names = append(names, d.Name())
		}
	}
	slices.Sort(names)
	for _, name := range names[:max(0, len(names)-HistoryKeep)] {
		_ = os.RemoveAll(filepath.Join(s.Dir, historyDir, name))
	}
}

// readEntry reads one entry's directory. ok is false for a directory with no
// command in it, which is not an entry: half of one, or something somebody
// else put there.
func readEntry(path string) (Entry, bool) {
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			return ""
		}
		return string(data)
	}
	command, err := os.ReadFile(filepath.Join(path, commandFile))
	if err != nil {
		return Entry{}, false
	}
	id := filepath.Base(path)
	if _, after, found := strings.Cut(id, "_"); found {
		id = after
	}
	e := Entry{
		ID:      id,
		Command: string(command),
		From:    strings.TrimSuffix(read(fromFile), "\n"),
		Why:     strings.TrimSuffix(read(whyFile), "\n"),
		Dir:     strings.TrimSuffix(read(cwdFile), "\n"),
		Added:   parseStamp(read(addedFile)),
		Outcome: Outcome(strings.TrimSpace(read(outcomeFile))),
		Done:    parseStamp(read(doneFile)),
		path:    path,
	}
	if when, status, found := strings.Cut(strings.TrimSpace(read(lastFile)), " "); found {
		if n, err := strconv.Atoi(status); err == nil {
			e.Failed, e.Status = parseStamp(when), n
		}
	}
	return e, true
}

// stamp writes a time as an entry's files hold one; stampOf is the same
// without the newline, for a file that holds something after it.
func stamp(t time.Time) string   { return stampOf(t) + "\n" }
func stampOf(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseStamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}
