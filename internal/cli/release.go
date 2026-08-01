package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Whether a newer treewright has been released.
//
// Nothing here runs unless it is asked for: `doctor` asks, and `version
// --check` asks, and no other command does. An upgrade check that fires on
// `new` or `ls` is a network call in the middle of a command that had no reason
// to make one — slow on a bad connection, a privacy question on any connection,
// and a warning nobody asked for at the moment they were doing something else.
// There is no cache file for the same reason there is no background check: both
// exist to make an automatic check cheap, and this one is not automatic.
//
// Two properties matter more than the answer. It must not hang, since doctor is
// what a person runs when something is already wrong; and it must be silent
// when it cannot reach anything, since an offline laptop is not a fault to
// report. Both are why the failure path here produces a state rather than an
// error: there is nothing for a caller to handle.

// releaseAPI is where the check asks. A variable so that a test can point it at
// a server of its own — this is the one thing in treewright that talks to
// something it did not start, and a test that could not stand in for that would
// leave the parsing and the comparison unexercised or the suite on the network.
var releaseAPI = "https://api.github.com/repos/jay-snyder/treewright/releases/latest"

// releaseTimeout bounds the whole request. Short on purpose: this is a line of
// a report, and a report that sits for thirty seconds on a captive-portal
// network has failed at being a report whatever it eventually says.
const releaseTimeout = 3 * time.Second

// releaseState is what a check found.
type releaseState int

const (
	// releaseIncomparable: this build reports no version a release can be
	// compared against — "dev", or anything else that is not vX.Y.Z. No request
	// is made, which is also what keeps the test suite off the network.
	releaseIncomparable releaseState = iota

	// releaseUnreachable: the question could not be asked. Offline, blocked,
	// rate-limited, or GitHub having a bad day — none of which is a fault of the
	// installation, and doctor says nothing about any of them.
	releaseUnreachable

	releaseCurrent // this is the newest release
	releaseBehind  // a newer one is published
)

// checkForNewerRelease compares the running version against the newest
// published release, returning what it found and the release's tag.
func checkForNewerRelease(current string) (releaseState, string) {
	if _, _, ok := parseVersion(current); !ok {
		return releaseIncomparable, ""
	}
	latest, err := latestRelease()
	if err != nil {
		return releaseUnreachable, ""
	}
	if compareVersions(current, latest) < 0 {
		return releaseBehind, latest
	}
	return releaseCurrent, latest
}

// latestRelease asks GitHub for the tag of the newest published release.
//
// net/http and encoding/json rather than a client library: the whole build is
// two modules and it stays two, and what is wanted here is one field of one
// document. GitHub's own releases API is the endpoint that knows about
// prereleases and drafts and excludes them, which is the reason to ask it
// rather than to read the tag list.
func latestRelease() (string, error) {
	client := &http.Client{Timeout: releaseTimeout}
	req, err := http.NewRequest(http.MethodGet, releaseAPI, nil)
	if err != nil {
		return "", err
	}
	// Both headers are GitHub's own asks of an API client. Without a user agent
	// the request is refused outright.
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "treewright")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Rate-limited, or a repository with no release yet. Neither is worth
		// distinguishing: the caller's whole vocabulary for this is "could not
		// ask", and it says nothing either way.
		return "", errUnreachable
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if _, _, ok := parseVersion(payload.TagName); !ok {
		return "", errUnreachable
	}
	return payload.TagName, nil
}

// errUnreachable stands for every way the question fails to be answered, since
// the caller treats them all alike and none of them is ever printed.
var errUnreachable = errors.New("no usable answer from the release API")

// parseVersion reads the three numbers out of a version string, reporting
// whether it carries a prerelease suffix and whether it is a version at all.
//
// It handles the four spellings treewright reports about itself: a release tag
// (v1.2.3), the same from a modified tree (v1.2.3+dirty), a pseudo-version for a
// commit past the last tag (v1.2.4-0.20250101000000-abcdef123456), and "dev",
// which is not a version and is the case the caller has to say something about
// rather than guess at.
//
// Build metadata is dropped, as semver says: "+dirty" describes the tree the
// binary was built from and not which release it is. A prerelease suffix is kept
// only as a flag, because that is the whole of what ordering needs from it — a
// pseudo-version sorts before the release it names, which is exactly right for a
// commit that is on the way to v1.2.4 and is not v1.2.4.
func parseVersion(s string) (nums [3]int, prerelease bool, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if base, _, found := strings.Cut(s, "+"); found {
		s = base
	}
	if base, _, found := strings.Cut(s, "-"); found {
		s, prerelease = base, true
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return nums, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, false, false
		}
		nums[i] = n
	}
	return nums, prerelease, true
}

// compareVersions orders two versions: -1 when a is older, +1 when it is newer,
// 0 when they are the same release. Anything unparseable compares equal, so a
// caller that failed to check first reports nothing rather than an upgrade to a
// version that does not exist.
func compareVersions(a, b string) int {
	an, apre, aok := parseVersion(a)
	bn, bpre, bok := parseVersion(b)
	if !aok || !bok {
		return 0
	}
	for i := range an {
		switch {
		case an[i] < bn[i]:
			return -1
		case an[i] > bn[i]:
			return 1
		}
	}
	// Same numbers: a prerelease of them is older than the release itself.
	switch {
	case apre && !bpre:
		return -1
	case !apre && bpre:
		return 1
	default:
		return 0
	}
}

// upgradeCommand names how to upgrade, for the install route it can tell, and
// "" for one it cannot.
//
// Naming the wrong command is worse than naming none: `brew upgrade` told to
// someone who used `go install` fails in a way that reads as treewright being
// broken. So this only answers where the answer is written on the path the
// binary is running from, and the message has a sentence for the rest.
func upgradeCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	// Homebrew puts a symlink in bin and the real file under Cellar or Caskroom,
	// so the informative path is the resolved one.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return upgradeCommandFor(exe)
}

// upgradeCommandFor is upgradeCommand with the path handed in, because the
// interesting cases are other people's machines.
func upgradeCommandFor(exe string) string {
	sep := string(filepath.Separator)
	for _, marker := range []string{"Cellar", "Caskroom", "homebrew", "Homebrew"} {
		if strings.Contains(exe, sep+marker+sep) {
			return "brew upgrade --cask treewright"
		}
	}
	if dir := filepath.Dir(exe); dir != "" && dir == goBin() {
		return "go install github.com/jay-snyder/treewright@latest"
	}
	return ""
}

// goBin is where `go install` puts a binary, worked out the way the go command
// does — GOBIN, else GOPATH/bin, else ~/go/bin.
//
// Read out of the environment rather than by running `go env`, because a machine
// that installed treewright with `go install` need not still have a toolchain on
// it, and shelling out to one during a version check would be a fresh way for
// this to be slow.
func goBin() string {
	if bin := os.Getenv("GOBIN"); bin != "" {
		return bin
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		// GOPATH is a list; the first entry is where installs land.
		first, _, _ := strings.Cut(gopath, string(os.PathListSeparator))
		return filepath.Join(first, "bin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "go", "bin")
}

// upgradeFallback is what the advice says where the install route is not
// written on the path the binary is running from.
const upgradeFallback = "upgrade it the way you installed it — a cask, a release tarball, or go install"

// releaseUpgradeAdvice is the last line of a report about a newer release: the
// command to run, or the sentence for a route nothing can name.
//
// Plain, because doctor renders a finding as a table cell and ui.Table measures
// its columns in runes — escape codes inside one are counted as width and the
// row after it stops lining up. Color in that report marks the level column and
// nothing else, deliberately.
func releaseUpgradeAdvice() string {
	if cmd := upgradeCommand(); cmd != "" {
		return "upgrade with:  " + cmd
	}
	return upgradeFallback
}

// copyableUpgradeAdvice is the same line for a message rather than a table, with
// the command marked as the part meant to be typed.
func copyableUpgradeAdvice(env *Env) string {
	if cmd := upgradeCommand(); cmd != "" {
		return "upgrade with:  " + env.copyable(cmd)
	}
	return upgradeFallback
}
