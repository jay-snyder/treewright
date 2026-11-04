package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner runs gh with the given arguments and returns what it printed. Fetch
// takes one rather than calling gh itself, so the queries and the joining of
// their answers can be tested against canned output with no gh installed.
type Runner func(args ...string) ([]byte, error)

// callTimeout bounds each gh call. A refresh started from the status line has
// nobody waiting on it, but a hung one holds the lock, and nothing else
// refreshes until it is broken.
const callTimeout = 30 * time.Second

// MissingGHError reports that gh is not on PATH, with the PATH it was looked for on.
//
// The PATH is the point. A refresh the status line starts runs in the tmux
// server's environment, which is whatever the server was started with and need
// not be the shell's, so "gh is not installed" from there can be true of the
// server and false of every terminal the person has open. Carrying the PATH
// searched puts the difference in front of whoever reads the failure.
type MissingGHError struct{ Path string }

func (e *MissingGHError) Error() string {
	return "gh, the GitHub CLI, is not on PATH\nsearched " + e.Path + "\ninstall it from https://cli.github.com"
}

// GH finds gh on PATH and returns a Runner for it.
//
// PATH and nothing else: it is the PATH that found treewright, since a status
// line runs `treewright reviews segment` by name. A list of well-known
// directories tried after it would be a guess with a stale entry waiting in it,
// and would find a gh the person's own shell does not run. See "Dependencies"
// in docs/plugins.md.
func GH() (Runner, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return nil, &MissingGHError{Path: os.Getenv("PATH")}
	}
	return func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			// gh's own sentence about it, when it gave one, since that is what
			// says whether this was the network, a login or a rate limit.
			if said := strings.TrimSpace(stderr.String()); said != "" {
				return nil, fmt.Errorf("gh %s: %s", args[0], firstLine(said))
			}
			return nil, fmt.Errorf("gh %s: %w", args[0], err)
		}
		return stdout.Bytes(), nil
	}, nil
}

// LoggedIn reports whether gh holds a login, from the local credential store.
//
// `gh auth token` rather than `gh auth status`, because status checks the token
// against the API, and doctor must not make a network call on an offline
// laptop's behalf only to report that it is offline. The token itself is
// discarded unread.
func LoggedIn(gh Runner) bool {
	_, err := gh("auth", "token")
	return err == nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// searchFields is what each search asks for, and searchLimit how many of each
// kind it takes: the most recent fifty open pull requests is far past where a
// review queue stops being one.
const (
	searchFields = "number,title,url,author,repository,createdAt"
	searchLimit  = "50"
)

// searchHit is one row of gh's search output, in the parts of it Fetch reads.
//
// No struct tags: gh spells its fields in camelCase, and encoding/json matches
// a field name to a key case-insensitively when no tag says otherwise. A tag
// would have to spell gh's names, which the linter refuses for JSON this
// repository defines — and this JSON is gh's, so the field names are the
// honest place to match it.
type searchHit struct {
	Number int
	Title  string
	URL    string
	Author struct {
		Login string
	}
	Repository struct {
		NameWithOwner string
	}
	CreatedAt time.Time
}

func (h searchHit) key() string { return h.Repository.NameWithOwner + "#" + strconv.Itoa(h.Number) }

func (h searchHit) pull(kind Kind) Pull {
	return Pull{
		Number:  h.Number,
		Repo:    h.Repository.NameWithOwner,
		Author:  printable(h.Author.Login),
		Title:   printable(h.Title),
		URL:     printable(h.URL),
		Created: h.CreatedAt,
		Kind:    kind,
	}
}

func search(gh Runner, query ...string) ([]searchHit, error) {
	args := append([]string{"search", "prs"}, query...)
	args = append(args, "--state=open", "--limit", searchLimit, "--json", searchFields)
	out, err := gh(args...)
	if err != nil {
		return nil, err
	}
	var hits []searchHit
	if err := json.Unmarshal(out, &hits); err != nil {
		return nil, fmt.Errorf("gh search prs %s: unreadable answer: %w", strings.Join(query, " "), err)
	}
	return hits, nil
}

// Fetch asks GitHub for every pull request waiting on you, in every repository,
// and says what each one is to you.
//
// Four searches and a lookup of who you are, plus one request per pull request
// you have reviewed that is not already in the queue:
//
//   - requested of you, which is the queue itself;
//   - requested of you and reviewed by you, which is what tells a re-request
//     from a first one — keyed by repository and number, since numbers repeat
//     across repositories;
//   - reviewed by you, for the ones you reviewed without approving: GitHub
//     drops a request the moment any review is submitted, so they would
//     otherwise vanish while still waiting on the author. Search filters only on
//     a pull request's overall decision, which a colleague's approval satisfies,
//     so your own latest review is looked up on each;
//   - opened by you, for the picker.
//
// Any failed search fails the whole refresh, and the caller keeps the previous
// cache rather than writing part of a new one — a second search failing would
// otherwise flip every re-request back to new. A single pull request whose
// reviews cannot be read is left out instead, since that is one row and not
// the queue.
func Fetch(gh Runner) ([]Pull, error) {
	requested, err := search(gh, "--review-requested=@me")
	if err != nil {
		return nil, err
	}
	again, err := search(gh, "--review-requested=@me", "--reviewed-by=@me")
	if err != nil {
		return nil, err
	}
	login, err := whoAmI(gh)
	if err != nil {
		return nil, err
	}
	reviewed, err := search(gh, "--reviewed-by=@me")
	if err != nil {
		return nil, err
	}
	mine, err := search(gh, "--author=@me")
	if err != nil {
		return nil, err
	}

	reRequested := make(map[string]bool, len(again))
	for _, h := range again {
		reRequested[h.key()] = true
	}
	queued := make(map[string]bool, len(requested))
	pulls := make([]Pull, 0, len(requested)+len(reviewed)+len(mine))
	for _, h := range requested {
		queued[h.key()] = true
		kind := New
		if reRequested[h.key()] {
			kind = Again
		}
		pulls = append(pulls, h.pull(kind))
	}
	for _, h := range reviewed {
		if queued[h.key()] {
			continue
		}
		switch latestReview(gh, h.URL, login) {
		case "CHANGES_REQUESTED":
			pulls = append(pulls, h.pull(Changes))
		case "COMMENTED":
			pulls = append(pulls, h.pull(Commented))
		}
	}
	for _, h := range mine {
		pulls = append(pulls, h.pull(Yours))
	}
	return pulls, nil
}

func whoAmI(gh Runner) (string, error) {
	out, err := gh("api", "user")
	if err != nil {
		return "", err
	}
	var user struct {
		Login string
	}
	if err := json.Unmarshal(out, &user); err != nil || user.Login == "" {
		return "", errors.New("gh api user: no login in the answer")
	}
	return user.Login, nil
}

// latestReview is the state of your own latest review on a pull request, or ""
// when it cannot be read or you have none. An approval comes back as
// APPROVED, which Fetch does not keep: approving is what takes a pull request
// off your list.
func latestReview(gh Runner, url, login string) string {
	out, err := gh("pr", "view", url, "--json", "latestReviews")
	if err != nil {
		return ""
	}
	// Untagged for the reason searchHit is.
	var view struct {
		LatestReviews []struct {
			Author struct {
				Login string
			}
			State string
		}
	}
	if err := json.Unmarshal(out, &view); err != nil {
		return ""
	}
	for _, r := range view.LatestReviews {
		if r.Author.Login == login {
			return r.State
		}
	}
	return ""
}
