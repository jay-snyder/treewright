package reviews

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolate points the cache at a directory of this test's own, so nothing here
// can read or rewrite the review queue of whoever runs the suite.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return Dir()
}

func TestRepoOfReadsOwnerAndNameHoweverTheRemoteIsSpelled(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/acme/storefront.git":         "acme/storefront",
		"https://github.com/acme/storefront":             "acme/storefront",
		"https://github.com/acme/storefront/":            "acme/storefront",
		"git@github.com:acme/storefront.git":             "acme/storefront",
		"ssh://git@github.example.com/acme/checkout-api": "acme/checkout-api",
		"  https://github.com/acme/storefront.git\n":     "acme/storefront",
		"/srv/git/acme/storefront.git":                   "acme/storefront",
		"storefront":                                     "",
		"https://github.com/":                            "",
		"":                                               "",
		"git@github.com:storefront.git":                  "",
	} {
		if got := RepoOf(remote); got != want {
			t.Errorf("RepoOf(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestAgeLabelRollsToWeeksWhereTheRequestGoesStale(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	days := func(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }
	for _, tc := range []struct {
		created time.Time
		want    string
	}{
		{now.Add(-time.Hour), "today"},
		{days(1), "1d ago"},
		{days(13), "13d ago"},
		{days(14), "2w ago"},
		{days(60), "8w ago"},
		{days(61), "2mo ago"},
		{time.Time{}, "?"},
	} {
		if got := AgeLabel(tc.created, now); got != tc.want {
			t.Errorf("AgeLabel(%v ago) = %q, want %q", now.Sub(tc.created), got, tc.want)
		}
	}
	// The pill's cut and the picker's weeks begin at the same moment.
	if p := (Pull{Created: days(14)}); !p.Stale(now) || (Pull{Created: days(13)}).Stale(now) {
		t.Errorf("staleness disagrees with the age label's roll to weeks")
	}
}

func TestThePillIsEmptyWithNothingToShow(t *testing.T) {
	now := time.Now()
	for name, pulls := range map[string][]Pull{
		"no rows":       nil,
		"another repo":  {{Number: 1, Repo: "acme/checkout-api", Kind: New, Created: now}},
		"only your own": {{Number: 1, Repo: "acme/storefront", Kind: Yours, Created: now}},
		"only stale":    {{Number: 1, Repo: "acme/storefront", Kind: New, Created: now.Add(-StaleAfter)}},
	} {
		if got := Pill(pulls, "acme/storefront", now, DefaultPillStyle, DefaultPillLabel); got != "" {
			t.Errorf("%s: pill = %q, want nothing", name, got)
		}
	}
	// And no repository at all is nothing, rather than every repository.
	pulls := []Pull{{Number: 1, Repo: "acme/storefront", Kind: New, Created: now}}
	if got := Pill(pulls, "", now, DefaultPillStyle, DefaultPillLabel); got != "" {
		t.Errorf("pill for no repository = %q, want nothing", got)
	}
}

// TestThePillMarksEachKindAndResetsAfterIt: a kind is an attribute, never a
// color, and the pill's own style comes back after every number that set one.
func TestThePillMarksEachKindAndResetsAfterIt(t *testing.T) {
	now := time.Now()
	pulls := []Pull{
		{Number: 1, Repo: "acme/storefront", Kind: New, Created: now},
		{Number: 2, Repo: "acme/storefront", Kind: Again, Created: now},
		{Number: 3, Repo: "acme/storefront", Kind: Changes, Created: now},
		{Number: 4, Repo: "acme/storefront", Kind: Commented, Created: now},
	}
	want := "#[reverse] PR" +
		" #[range=user|tw-pr-1]#1#[norange]" +
		" #[range=user|tw-pr-2]#[underscore]#2#[norange]#[default,reverse]" +
		" #[range=user|tw-pr-3]#[dim]#3#[norange]#[default,reverse]" +
		" #[range=user|tw-pr-4]#[italics]#4#[norange]#[default,reverse]" +
		" #[default]"
	if got := Pill(pulls, "acme/storefront", now, DefaultPillStyle, DefaultPillLabel); got != want {
		t.Errorf("pill =\n  %q\nwant\n  %q", got, want)
	}
}

// TestEveryRangeNameFitsTmuxsField: tmux keeps a user range's name in sixteen
// bytes, terminator included, and cuts a longer one without a word.
func TestEveryRangeNameFitsTmuxsField(t *testing.T) {
	if name := fmt.Sprintf("%s%d", RangePrefix, 999_999_999); len(name) > 15 {
		t.Errorf("%q is %d bytes, past the fifteen tmux keeps", name, len(name))
	}
}

func TestCheckStyleRefusesWhatWouldEndThePillsStyle(t *testing.T) {
	for _, style := range []string{"fg=red]", "bg=[x", "bold\nfg=red"} {
		if CheckStyle(style) == nil {
			t.Errorf("CheckStyle(%q) accepted it", style)
		}
	}
	for _, style := range []string{"", "reverse", "fg=#292828,bg=colour203,bold"} {
		if err := CheckStyle(style); err != nil {
			t.Errorf("CheckStyle(%q) = %v", style, err)
		}
	}
}

func TestClickedFindsWhatThePillDrew(t *testing.T) {
	pulls := []Pull{
		{Number: 7, Repo: "acme/checkout-api", Kind: New, URL: "https://x/checkout-api/7"},
		{Number: 7, Repo: "acme/storefront", Kind: Yours, URL: "https://x/storefront/7-own"},
		{Number: 7, Repo: "acme/storefront", Kind: Again, URL: "https://x/storefront/7"},
	}
	for _, tc := range []struct{ rangeName, repo, want string }{
		{"tw-pr-7", "acme/storefront", "https://x/storefront/7"},
		{"tw-pr-7", "acme/checkout-api", "https://x/checkout-api/7"},
		{"tw-pr-7", "", "https://x/checkout-api/7"},
		{"tw-pr-8", "acme/storefront", ""},
		{"tw-pr-07", "acme/storefront", ""},
		{"tw-pr-7x", "acme/storefront", ""},
		{"rev-7", "acme/storefront", ""},
	} {
		p, ok := Clicked(pulls, tc.rangeName, tc.repo)
		if got := p.URL; got != tc.want || ok != (tc.want != "") {
			t.Errorf("Clicked(%q, %q) = %q, %v — want %q", tc.rangeName, tc.repo, got, ok, tc.want)
		}
	}
}

// ---- the cache -------------------------------------------------------------

func TestTheCacheGoesUnderXDGCacheHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)
	if got, want := Dir(), filepath.Join(base, "treewright", "reviews"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", home)
	if got, want := Dir(), filepath.Join(home, ".cache", "treewright", "reviews"); got != want {
		t.Errorf("Dir() with no XDG_CACHE_HOME = %q, want %q", got, want)
	}
}

func TestTheCacheRoundTrips(t *testing.T) {
	dir := isolate(t)
	if Exists(dir) || !Load(dir).Fetched.IsZero() {
		t.Fatalf("a cache nobody wrote exists, or loads as fetched")
	}
	now := time.Now().Truncate(time.Second)
	want := Cache{Fetched: now, Pulls: []Pull{{Number: 1, Repo: "acme/storefront", Title: "Retry the payment webhook", Kind: Again, Created: now}}}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got := Load(dir)
	if !got.Fetched.Equal(want.Fetched) || len(got.Pulls) != 1 || got.Pulls[0].Title != want.Pulls[0].Title || got.Pulls[0].Kind != Again {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
	if !Exists(dir) || !got.Fresh(now) || got.Fresh(now.Add(MaxAge)) {
		t.Errorf("a cache written now should exist, be fresh now, and be due a refresh after %v", MaxAge)
	}

	// Unreadable is the empty cache, due for a refresh, rather than an error the
	// status line has nowhere to show.
	if err := os.WriteFile(filepath.Join(dir, cacheFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Load(dir).Fetched.IsZero() {
		t.Errorf("an unreadable cache loaded as fetched")
	}
}

func TestAFailureIsKeptUntilARefreshWorks(t *testing.T) {
	dir := isolate(t)
	if _, _, failed := LastFailure(dir); failed {
		t.Fatalf("a failure recorded before anything ran")
	}
	if err := RecordFailure(dir, "gh search: HTTP 502\n"); err != nil {
		t.Fatal(err)
	}
	why, at, failed := LastFailure(dir)
	if !failed || why != "gh search: HTTP 502" || time.Since(at) > time.Minute {
		t.Errorf("LastFailure = %q, %v, %v", why, at, failed)
	}
	ClearFailure(dir)
	if _, _, failed := LastFailure(dir); failed {
		t.Errorf("the failure survived being cleared")
	}
}

func TestOneRefreshHoldsTheLockAndAStaleLockIsBroken(t *testing.T) {
	dir := isolate(t)
	release, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !Locked(dir) {
		t.Errorf("Locked = false while the lock is held")
	}
	if _, err := Lock(dir); !errors.Is(err, ErrLocked) {
		t.Errorf("a second Lock = %v, want ErrLocked", err)
	}
	release()
	if Locked(dir) {
		t.Errorf("Locked = true after release")
	}

	// A lock left by a refresh killed halfway.
	path := filepath.Join(dir, lockDir)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * lockStaleAfter)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if Locked(dir) {
		t.Errorf("a stale lock reads as held")
	}
	release, err = Lock(dir)
	if err != nil {
		t.Fatalf("a stale lock was not broken: %v", err)
	}
	release()
}

// ---- asking GitHub ---------------------------------------------------------

// fakeGH answers a refresh's questions from a table, keyed by the arguments a
// question ends in, and records what was asked.
type fakeGH struct {
	answers map[string]any
	fail    string // a question that fails
	asked   []string
}

func (f *fakeGH) run(args ...string) ([]byte, error) {
	q := strings.Join(args, " ")
	f.asked = append(f.asked, q)
	if f.fail != "" && strings.Contains(q, f.fail) {
		return nil, errors.New("gh: HTTP 502")
	}
	for _, key := range []string{
		"--review-requested=@me --reviewed-by=@me", "--review-requested=@me", "--reviewed-by=@me", "--author=@me",
		"api user", "auth token",
	} {
		if strings.Contains(q, key) {
			return json.Marshal(f.answers[key])
		}
	}
	if strings.HasPrefix(q, "pr view ") {
		if v, ok := f.answers[args[2]]; ok {
			return json.Marshal(v)
		}
		return nil, errors.New("no such pull request")
	}
	return nil, fmt.Errorf("unexpected question %q", q)
}

func searchRow(number int, repo, title string) map[string]any {
	return map[string]any{
		"number": number, "title": title, "url": fmt.Sprintf("https://github.com/%s/pull/%d", repo, number),
		"author": map[string]any{"login": "dana"}, "repository": map[string]any{"nameWithOwner": repo},
		"createdAt": "2026-11-01T09:00:00Z",
	}
}

func review(login, state string) map[string]any {
	return map[string]any{"author": map[string]any{"login": login}, "state": state}
}

func TestFetchSaysWhatEachPullRequestIsToYou(t *testing.T) {
	gh := &fakeGH{answers: map[string]any{
		"--review-requested=@me --reviewed-by=@me": []any{searchRow(2, "acme/storefront", "b")},
		"--review-requested=@me": []any{
			searchRow(1, "acme/storefront", "a"), searchRow(2, "acme/storefront", "b"),
			// The same number in another repository is another pull request.
			searchRow(2, "acme/checkout-api", "b elsewhere"),
		},
		"--reviewed-by=@me": []any{
			searchRow(2, "acme/storefront", "b"), searchRow(3, "acme/storefront", "c"),
			searchRow(4, "acme/storefront", "d"), searchRow(5, "acme/storefront", "e"),
			searchRow(6, "acme/storefront", "unreadable"),
		},
		"--author=@me": []any{searchRow(9, "acme/storefront", "mine")},
		"api user":     map[string]any{"login": "me"},
		"https://github.com/acme/storefront/pull/3": map[string]any{"latestReviews": []any{review("me", "CHANGES_REQUESTED")}},
		"https://github.com/acme/storefront/pull/4": map[string]any{"latestReviews": []any{review("lee", "APPROVED"), review("me", "COMMENTED")}},
		"https://github.com/acme/storefront/pull/5": map[string]any{"latestReviews": []any{review("me", "APPROVED")}},
	}}
	pulls, err := Fetch(gh.run)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pulls {
		got = append(got, fmt.Sprintf("%s#%d %s", p.Repo, p.Number, p.Kind))
	}
	want := []string{
		"acme/storefront#1 new", "acme/storefront#2 re-request", "acme/checkout-api#2 new",
		"acme/storefront#3 changes", "acme/storefront#4 commented", "acme/storefront#9 yours",
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("Fetch =\n  %v\nwant\n  %v", got, want)
	}
	for _, q := range gh.asked {
		if strings.Contains(q, "pull/2") {
			t.Errorf("asked %q about a pull request already in the queue", q)
		}
	}
}

// TestAFailedSearchFailsTheWholeRefresh, since a partial answer would flip every
// re-request back to new.
func TestAFailedSearchFailsTheWholeRefresh(t *testing.T) {
	for _, failing := range []string{"--review-requested=@me", "--reviewed-by=@me", "--author=@me", "api user"} {
		gh := &fakeGH{fail: failing, answers: map[string]any{"api user": map[string]any{"login": "me"}}}
		if _, err := Fetch(gh.run); err == nil {
			t.Errorf("Fetch with %q failing succeeded", failing)
		}
	}
}

// TestFetchStripsWhatATerminalWouldActOn: a title is whatever the pull request's
// author typed, and the picker prints it into a terminal.
func TestFetchStripsWhatATerminalWouldActOn(t *testing.T) {
	gh := &fakeGH{answers: map[string]any{
		"--review-requested=@me": []any{searchRow(1, "acme/storefront", "Fix\x1b]8;;https://evil\x1b\\ the\u202e cart\n")},
		"api user":               map[string]any{"login": "me"},
	}}
	pulls, err := Fetch(gh.run)
	if err != nil {
		t.Fatal(err)
	}
	if got := pulls[0].Title; got != "Fix]8;;https://evil\\ the cart" {
		t.Errorf("title = %q, want every control and format character gone", got)
	}
}

func TestLoggedInReadsTheLocalLogin(t *testing.T) {
	ok := &fakeGH{answers: map[string]any{"auth token": "t"}}
	if !LoggedIn(ok.run) || ok.asked[0] != "auth token" {
		t.Errorf("LoggedIn asked %v, want true from auth token", ok.asked)
	}
	if LoggedIn((&fakeGH{fail: "auth"}).run) {
		t.Errorf("LoggedIn = true with no login")
	}
}

func TestGHNamesThePathItSearched(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := GH()
	var missing *MissingGHError
	if !errors.As(err, &missing) || missing.Path != os.Getenv("PATH") || !strings.Contains(err.Error(), "searched "+missing.Path) {
		t.Errorf("GH with no gh = %v, want the PATH it searched", err)
	}
}

// TestGHCarriesGHsOwnSentence: the reason a call failed is gh's to give —
// network, login or rate limit — and the only part worth recording.
func TestGHCarriesGHsOwnSentence(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = ok ]; then echo '[]'; exit 0; fi\necho 'HTTP 401: Bad credentials' >&2\necho 'second line' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	gh, err := GH()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := gh("ok"); err != nil || string(out) != "[]\n" {
		t.Errorf("gh ok = %q, %v", out, err)
	}
	if _, err := gh("search", "prs"); err == nil || err.Error() != "gh search: HTTP 401: Bad credentials" {
		t.Errorf("gh search = %v, want gh's first line", err)
	}
}

func TestOpenRefusesWhatIsNotAWebPage(t *testing.T) {
	for _, url := range []string{"file:///etc/passwd", "/Applications/Calculator.app", "javascript:alert(1)", ""} {
		if Open(url) == nil {
			t.Errorf("Open(%q) went ahead", url)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if err := Open("https://github.com/acme/storefront/pull/1"); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("Open with no opener = %v, want it named", err)
	}
}
