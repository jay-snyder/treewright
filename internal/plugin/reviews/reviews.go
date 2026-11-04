// Package reviews is what the reviews plugin knows that is not about
// treewright: which pull requests are waiting on you, asked of GitHub through
// gh, kept in a cache, and drawn as a pill for a tmux status line.
//
// Everything about treewright — which registered repository a directory is in,
// the scratch window a review is handed to, the popup the picker opens in — is
// in internal/cli, which calls what core calls. That is the plugin rule "a
// plugin asks treewright": nothing here works out a worktree, a window or a
// config, and nothing here calls git or tmux. See docs/plugins.md.
//
// The cache is repository-blind. Every query is scoped to @me, the cache holds
// every repository you have pull requests in, and each surface reads one
// repository's rows out of it — filtering at read time is what makes a second
// repository cost nothing.
package reviews

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Kind is what a pull request is to you, which both surfaces show: the pill as
// a text attribute and the picker as a word.
//
// The values are the words the cache stores. They are not promised to anybody
// — the cache's format is the plugin's to change — but they are spelled out
// rather than abbreviated so a person reading the file can tell what it says.
type Kind string

// The five kinds, in the order the refresh assigns them.
const (
	// New is a review requested of you that you have not reviewed yet.
	New Kind = "new"
	// Again is a review requested again after a review you submitted.
	Again Kind = "re-request"
	// Changes is a pull request whose latest review from you requested changes.
	Changes Kind = "changes"
	// Commented is a pull request whose latest review from you was comment-only.
	Commented Kind = "commented"
	// Yours is your own open pull request, kept for the picker alone.
	Yours Kind = "yours"
)

// StaleAfter is where a request stops being fresh. The pill shows only fresh
// ones; the picker shows every row and marks the stale ones by their age, and
// AgeLabel rolls from days to weeks at the same boundary, so the rows the
// picker colors as stale are exactly the ones the pill leaves out.
const StaleAfter = 14 * 24 * time.Hour

// Pull is one pull request in the cache.
type Pull struct {
	Number  int       `json:"number"`
	Repo    string    `json:"repo"` // owner/name, as GitHub spells it
	Author  string    `json:"author"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Created time.Time `json:"created"`
	Kind    Kind      `json:"kind"`
}

// Stale reports whether the pull request was opened longer ago than
// StaleAfter.
func (p Pull) Stale(now time.Time) bool { return now.Sub(p.Created) >= StaleAfter }

// InPill reports whether the status line shows this pull request: a request
// still fresh, and never your own — the pill is the queue of what other people
// are blocked on, and the picker is where extra work is opted into.
func (p Pull) InPill(now time.Time) bool { return p.Kind != Yours && !p.Stale(now) }

// ForRepo is the cache's rows for one repository, in the order the cache holds
// them. An empty repo has none: a pane outside a repository shows nothing,
// rather than every repository's queue at once.
func ForRepo(pulls []Pull, repo string) []Pull {
	if repo == "" {
		return nil
	}
	var mine []Pull
	for _, p := range pulls {
		if p.Repo == repo {
			mine = append(mine, p)
		}
	}
	return mine
}

// AgeLabel is how long ago a pull request was opened, in the units a person
// reads at a glance: today, days, weeks from StaleAfter on, then months.
func AgeLabel(created, now time.Time) string {
	days := int(now.Sub(created).Hours() / 24)
	switch {
	case created.IsZero():
		return "?"
	case days < 1:
		return "today"
	case time.Duration(days)*24*time.Hour < StaleAfter:
		return fmt.Sprintf("%dd ago", days)
	case days < 61:
		return fmt.Sprintf("%dw ago", days/7)
	default:
		return fmt.Sprintf("%dmo ago", days/30)
	}
}

// RepoOf turns a git remote URL into the owner/name GitHub knows the
// repository by, or "" when it has no such shape.
//
// owner/name is the last two components however the remote is spelled —
// https://host/owner/name and ssh://git@host/owner/name separate them with a
// slash, git@host:owner/name with a colon — so the host plays no part, and a
// GitHub Enterprise remote answers the same way github.com's does.
func RepoOf(remote string) string {
	url := strings.TrimSpace(remote)
	url = strings.TrimSuffix(url, "/")
	url = strings.TrimSuffix(url, ".git")
	slash := strings.LastIndex(url, "/")
	if slash < 0 {
		return ""
	}
	name, rest := url[slash+1:], url[:slash]
	owner := rest[strings.LastIndexAny(rest, ":/")+1:]
	if name == "" || owner == "" {
		return ""
	}
	return owner + "/" + name
}

// printable strips the characters a terminal would act on rather than show.
//
// A title is whatever the pull request's author typed, and the picker prints it
// into a terminal — inside an OSC 8 hyperlink, at that. An escape character in
// a title would be a way for anybody who can open a pull request against your
// repository to write control sequences to your screen, so every control
// character goes, along with the format characters that reorder text around
// them.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// pillAttribute is how the pill marks a kind: a text attribute rather than a
// color, because an attribute reads against whatever background the person
// gives the pill and a color chosen against one background vanishes on
// another. A new request is unmarked, being the ordinary case.
var pillAttribute = map[Kind]string{
	Again:     "underscore",
	Changes:   "dim",
	Commented: "italics",
}

// DefaultPillStyle and DefaultPillLabel are the pill's look when the status
// line asks for nothing else: reverse video, which stands out in any theme
// without naming a color, and a word every font has.
const (
	DefaultPillStyle = "reverse"
	DefaultPillLabel = "PR"
)

// RangePrefix begins the name of the status-line range each number in the pill
// is drawn in, which the click binding matches on.
//
// The number is all that follows it. tmux keeps a user range's name in a
// sixteen-byte field and truncates anything longer without a word, so a name
// carrying the repository came back to the click handler cut in half and
// matched nothing; the repository comes from the clicking pane instead. The
// prefix is there so the range cannot collide with one of the person's own,
// and leaves nine digits for the number.
const RangePrefix = "tw-pr-"

// Pill is the tmux format for a repository's status-line pill, or "" when
// nothing is waiting — so an empty queue leaves the status line exactly as it
// was.
//
// Each number sits in a range of its own so it can be clicked, and is followed
// by a reset to the pill's style whenever its kind set an attribute, so one
// number's marking never leaks into the next. The reset begins with "default"
// because that is the only way tmux offers to turn an attribute off without
// knowing which ones the pill's own style already set.
func Pill(pulls []Pull, repo string, now time.Time, style, label string) string {
	var b strings.Builder
	for _, p := range ForRepo(pulls, repo) {
		if !p.InPill(now) {
			continue
		}
		if b.Len() == 0 {
			fmt.Fprintf(&b, "#[%s] %s", style, label)
		}
		fmt.Fprintf(&b, " #[range=user|%s%d]", RangePrefix, p.Number)
		attribute := pillAttribute[p.Kind]
		if attribute != "" {
			fmt.Fprintf(&b, "#[%s]", attribute)
		}
		fmt.Fprintf(&b, "#%d#[norange]", p.Number)
		if attribute != "" {
			fmt.Fprintf(&b, "#[default,%s]", style)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteString(" #[default]")
	return b.String()
}

// CheckStyle refuses a pill style that would end the pill's #[...] early,
// which tmux would draw as stray text in the person's status line rather than
// refuse.
func CheckStyle(style string) error {
	if strings.ContainsAny(style, "[]\n") {
		return fmt.Errorf("--style %q has a bracket or a line break in it, which would end the pill's tmux style early\n"+
			"give the style alone, as in --style 'fg=black,bg=red,bold'", style)
	}
	return nil
}

// Clicked finds the pull request a status-line range names, among the rows the
// pill could have drawn.
//
// Your own rows are skipped, so the lookup sees exactly what the pill showed: a
// pull request that is both yours and requested of you is cached under both
// kinds, and only the request is in the pill. The pane's repository is tried
// first; the number alone is the fallback, for a click whose pane has since
// left the repository the pill was drawn for — only a number open in two
// repositories at once is ambiguous, and then the first lookup has answered.
func Clicked(pulls []Pull, rangeName, repo string) (Pull, bool) {
	digits, ok := strings.CutPrefix(rangeName, RangePrefix)
	if !ok {
		return Pull{}, false
	}
	var number int
	if _, err := fmt.Sscanf(digits, "%d", &number); err != nil || fmt.Sprint(number) != digits {
		return Pull{}, false
	}
	for _, scoped := range []bool{true, false} {
		for _, p := range pulls {
			if p.Number != number || p.Kind == Yours {
				continue
			}
			if scoped && p.Repo != repo {
				continue
			}
			return p, true
		}
	}
	return Pull{}, false
}
