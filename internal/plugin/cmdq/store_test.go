package cmdq

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newStore is a queue in a directory of the test's own.
func newStore(t *testing.T) Store {
	t.Helper()
	return Store{Dir: filepath.Join(t.TempDir(), "cmdq")}
}

// queue adds an entry and returns its ID, failing the test if it cannot.
func queue(t *testing.T, s Store, command string) string {
	t.Helper()
	id, err := s.Add(Request{Command: command, From: "storefront-eng-2318", Why: "install the toolchain", Dir: t.TempDir()}, time.Now)
	if err != nil {
		t.Fatalf("add %q: %v", command, err)
	}
	return id
}

func TestAnEntryIsReadBackAsItWasQueued(t *testing.T) {
	s := newStore(t)
	dir := t.TempDir()
	at := time.Date(2026, 11, 4, 10, 15, 0, 123_456_789, time.UTC)
	command := "sudo installer -pkg ./toolchain.pkg \\\n  -target /\n"
	id, err := s.Add(Request{Command: command, From: "storefront-eng-2318", Why: "install the toolchain", Dir: dir},
		func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	if id != "20261104-101500-1234" {
		t.Errorf("id = %q, want the time it was queued to a tenth of a millisecond", id)
	}
	e, err := s.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Command != command {
		t.Errorf("command = %q, want it to the byte: %q", e.Command, command)
	}
	if e.From != "storefront-eng-2318" || e.Why != "install the toolchain" || e.Dir != dir {
		t.Errorf("entry = %+v", e)
	}
	if !e.Added.Equal(at) {
		t.Errorf("added = %v, want %v", e.Added, at)
	}
	if e.InHistory() {
		t.Errorf("a new entry is in the history")
	}
}

// TestConcurrentAddsTakeDistinctIDs is the rename doing its job: agents
// queuing in the same instant each get an ID of their own, and none of their
// entries replaces another's.
func TestConcurrentAddsTakeDistinctIDs(t *testing.T) {
	s := newStore(t)
	at := time.Now()
	sameInstant := func() time.Time { return at }
	const n = 8
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			// The clock is frozen, so every add asks for the same ID first,
			// and the rename is the only thing that can keep them apart —
			// with the next ID only coming from a real clock, each retry is
			// given a moment later than the frozen one.
			calls := 0
			ids[i], errs[i] = s.Add(Request{Command: "true", From: "a", Why: "b", Dir: "/"}, func() time.Time {
				calls++
				if calls <= 2 {
					return sameInstant()
				}
				return time.Now()
			})
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, id := range ids {
		if errs[i] != nil {
			t.Fatalf("add %d: %v", i, errs[i])
		}
		if seen[id] {
			t.Errorf("two adds took %s", id)
		}
		seen[id] = true
	}
	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != n {
		t.Errorf("%d entries waiting, want %d", len(pending), n)
	}
	if left, _ := os.ReadDir(filepath.Join(s.Dir, stagingDir)); len(left) != 0 {
		t.Errorf("staging still holds %d directories", len(left))
	}
}

func TestPendingListsTheNewestFirst(t *testing.T) {
	s := newStore(t)
	first := queue(t, s, "echo first")
	time.Sleep(time.Millisecond)
	second := queue(t, s, "echo second")
	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].ID != second || pending[1].ID != first {
		t.Errorf("pending = %v, want %s then %s", ids(pending), second, first)
	}
}

func ids(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ID)
	}
	return out
}

func TestRetiringMovesAnEntryToTheHistory(t *testing.T) {
	s := newStore(t)
	id := queue(t, s, "true")
	if err := s.Retire(id, Dismissed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.Pending(); len(pending) != 0 {
		t.Errorf("still waiting: %v", ids(pending))
	}
	e, err := s.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Outcome != Dismissed || e.Done.IsZero() {
		t.Errorf("outcome = %q, done = %v", e.Outcome, e.Done)
	}
	// Retiring it again is an entry that already left, which is what another
	// popup having got there first looks like.
	if err := s.Retire(id, Ran, time.Now()); !errors.Is(err, ErrNoEntry) {
		t.Errorf("second retire = %v, want ErrNoEntry", err)
	}
}

func TestTheHistoryKeepsTheLastFifty(t *testing.T) {
	s := newStore(t)
	var all []string
	for range HistoryKeep + 3 {
		id := queue(t, s, "true")
		all = append(all, id)
		if err := s.Retire(id, Ran, time.Now()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Microsecond)
	}
	history, err := s.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != HistoryKeep {
		t.Fatalf("history holds %d, want %d", len(history), HistoryKeep)
	}
	if history[0].ID != all[len(all)-1] {
		t.Errorf("history starts with %s, want the last to leave, %s", history[0].ID, all[len(all)-1])
	}
	for _, gone := range all[:3] {
		if _, err := s.Find(gone); !errors.Is(err, ErrNoEntry) {
			t.Errorf("%s is still in the history", gone)
		}
	}
}

func TestRestorePutsAnEntryBackWithItsLastFailure(t *testing.T) {
	s := newStore(t)
	id := queue(t, s, "false")
	failedAt := time.Now().Add(-time.Hour)
	if err := s.RecordFailure(id, 3, failedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.Retire(id, Dismissed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(id); err != nil {
		t.Fatal(err)
	}
	e, err := s.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.InHistory() || !e.Done.IsZero() {
		t.Errorf("restored entry still reads as gone: %+v", e)
	}
	if e.Status != 3 || !e.Failed.Equal(failedAt) {
		t.Errorf("failure = exit %d at %v, want exit 3 at %v", e.Status, e.Failed, failedAt)
	}
}

func TestKeptOutputFollowsTheEntry(t *testing.T) {
	s := newStore(t)
	id := queue(t, s, "echo hi")
	if err := s.KeepOutput(id, "hi\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Retire(id, Ran, time.Now()); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Find(id)
	if got := s.Output(e); got != "hi\n" {
		t.Errorf("output after retiring = %q", got)
	}
	// A run that printed nothing takes the last run's lines away rather than
	// leaving them to be read as its own.
	if err := s.KeepOutput(id, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.Output(e); got != "" {
		t.Errorf("output after an empty run = %q", got)
	}
}

// TestAnIDIsNeverAPath keeps an ID from naming a directory outside the queue.
func TestAnIDIsNeverAPath(t *testing.T) {
	s := newStore(t)
	queue(t, s, "true")
	for _, id := range []string{"", "../pending", "..", "*", "20261104-101500-123", "20261104-101500-1234/../x"} {
		if _, err := s.Find(id); !errors.Is(err, ErrNoEntry) {
			t.Errorf("Find(%q) = %v, want ErrNoEntry", id, err)
		}
		if err := s.Retire(id, Dismissed, time.Now()); !errors.Is(err, ErrNoEntry) {
			t.Errorf("Retire(%q) = %v, want ErrNoEntry", id, err)
		}
	}
}

func TestADirectoryWithNoCommandIsNotAnEntry(t *testing.T) {
	s := newStore(t)
	queue(t, s, "true")
	if err := os.MkdirAll(filepath.Join(s.Dir, pendingDir, "20200101-000000-0000"), 0o700); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("pending = %v, want only the real entry", ids(pending))
	}
}

func TestDirHonorsXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got := Dir(); got != "/state/treewright/cmdq" {
		t.Errorf("Dir() = %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/someone")
	if got := Dir(); !strings.HasSuffix(got, "/.local/state/treewright/cmdq") {
		t.Errorf("Dir() = %q, want it under ~/.local/state", got)
	}
}
