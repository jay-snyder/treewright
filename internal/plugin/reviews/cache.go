package reviews

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxAge is how old the cache may get before a reader starts a refresh. It is
// the floor on how often GitHub is asked, whatever number of status lines are
// drawing the pill: tmux runs the segment every status-interval for every
// attached client, and a query per redraw would be a rate limit within the hour.
const MaxAge = 5 * time.Minute

// lockStaleAfter is how long a refresh may hold the lock before the next one
// breaks it. A refresh is a handful of gh calls, each given far less than this,
// so a lock older than it belongs to one that was killed halfway — and left in
// place it would wedge every refresh after it for good.
const lockStaleAfter = 2 * time.Minute

// File names inside the cache directory. The format of each is the plugin's
// own business and promised to nobody; they are named here once so the reader
// and the writer cannot disagree.
const (
	cacheFile   = "pulls.json"
	lockDir     = "refresh.lock"
	failureFile = "refresh-failed"
)

// Dir is the cache directory: ${XDG_CACHE_HOME:-~/.cache}/treewright/reviews.
//
// The XDG default on macOS too, where the platform's own answer would be
// ~/Library/Caches. Somebody removing treewright looks for what it left beside
// ~/.config/treewright, where the registry is, and a second convention for one
// plugin's cache is a second place to forget. See docs/plugins.md.
func Dir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".cache", "treewright", "reviews")
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "treewright", "reviews")
}

// Cache is what the last successful refresh found.
type Cache struct {
	Fetched time.Time `json:"fetched"`
	Pulls   []Pull    `json:"pulls"`
}

// Fresh reports whether the cache is young enough that no refresh is due.
func (c Cache) Fresh(now time.Time) bool { return !c.Fetched.IsZero() && now.Sub(c.Fetched) < MaxAge }

// Load reads the cache in dir. A cache that is missing or unreadable is the
// empty one — never fetched, and so due for a refresh — because every reader
// would otherwise have to treat the two the same way by hand, and the status
// line has nowhere to report either.
func Load(dir string) Cache {
	data, err := os.ReadFile(filepath.Join(dir, cacheFile))
	if err != nil {
		return Cache{}
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}
	}
	return c
}

// Exists reports whether the plugin has ever written its directory: whether
// anybody placed the pill, bound the key or typed the command. It is how
// doctor tells a plugin in use from one nobody opted into, without asking tmux
// or GitHub anything.
func Exists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// Save writes the cache, whole or not at all: to a temporary file in the same
// directory, renamed over the old one. A reader in the middle of a status-line
// redraw sees the old list or the new one, never half of either.
func Save(dir string, c Cache) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, cacheFile+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, cacheFile))
}

// RecordFailure keeps what stopped the last refresh, for the picker and doctor
// to report: nothing waits for a refresh the status line started, so without
// this its failure would be invisible — a pill that simply stops changing.
func RecordFailure(dir, why string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, failureFile), []byte(strings.TrimSpace(why)+"\n"), 0o644)
}

// ClearFailure forgets the last failure, once a refresh has succeeded.
func ClearFailure(dir string) {
	_ = os.Remove(filepath.Join(dir, failureFile))
}

// LastFailure is what stopped the last refresh, and when, or ok false when the
// last one succeeded.
func LastFailure(dir string) (why string, at time.Time, ok bool) {
	path := filepath.Join(dir, failureFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, false
	}
	info, err := os.Stat(path)
	if err == nil {
		at = info.ModTime()
	}
	return strings.TrimSpace(string(data)), at, true
}

// ErrLocked reports that another refresh is already running.
var ErrLocked = errors.New("another refresh is already running")

// Lock takes the refresh lock, returning the function that releases it.
//
// A directory rather than a file, because creating one either succeeds or
// fails as a single step on every filesystem, where a lock file needs an
// O_EXCL that some network filesystems do not honor. A lock older than
// lockStaleAfter belongs to a refresh that was killed, and is broken.
func Lock(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, lockDir)
	if err := os.Mkdir(path, 0o755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) < lockStaleAfter {
			return nil, ErrLocked
		}
		// Removed and taken again rather than taken over in place, so that two
		// refreshes breaking the same stale lock at once cannot both win: the
		// second Mkdir fails exactly as an ordinary contended one does.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, ErrLocked
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			return nil, ErrLocked
		}
	}
	return func() { _ = os.Remove(path) }, nil
}

// Locked reports whether a refresh is running now, so a reader does not start
// a second one only for it to find the lock taken.
func Locked(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, lockDir))
	return err == nil && time.Since(info.ModTime()) < lockStaleAfter
}
