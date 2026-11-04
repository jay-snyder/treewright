package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jay-snyder/treewright/internal/plugin/reviews"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// The reviews plugin, driven end to end. gh is never the real one: the fixture
// puts a failing stub first on PATH, and a test that wants answers puts stubGH's
// ahead of it. Nothing here asks GitHub anything.

// elsewhereOnGitHub is a repository the cache holds rows for that is not the fixture's,
// which is what every scoping assertion is about.
const elsewhereOnGitHub = "acme/checkout-api"

// fixtureRepo is the owner/name the fixture's own repository answers to, worked
// out from its origin the way the plugin works it out — the origin being a bare
// repository on disk, which has a path's last two components and no GitHub.
func fixtureRepo(t *testing.T, f *fixture) string {
	t.Helper()
	repo := reviews.RepoOf(strings.TrimSpace(f.Git(f.MainDir, "remote", "get-url", "origin")))
	if repo == "" {
		t.Fatalf("the fixture's origin has no owner/name shape")
	}
	return repo
}

// pull is a cache row, opened ageDays ago.
func pull(number int, repo string, kind reviews.Kind, ageDays int) reviews.Pull {
	return reviews.Pull{
		Number:  number,
		Repo:    repo,
		Author:  "dana",
		Title:   fmt.Sprintf("Change number %d", number),
		URL:     fmt.Sprintf("https://github.com/%s/pull/%d", repo, number),
		Created: time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour),
		Kind:    kind,
	}
}

// writeCache writes the plugin's cache as a refresh would have, fetched at the
// given time.
func writeCache(t *testing.T, fetched time.Time, pulls ...reviews.Pull) {
	t.Helper()
	if err := reviews.Save(reviews.Dir(), reviews.Cache{Fetched: fetched, Pulls: pulls}); err != nil {
		t.Fatalf("write the reviews cache: %v", err)
	}
}

// ghAnswers is what stubGH's gh prints for each question the refresh asks.
type ghAnswers struct {
	requested, again, reviewed, mine []map[string]any
	// views is each pull request's latest reviews, by number.
	views map[int][]map[string]any
}

// hit is one row of gh's search output, in the shape gh prints it.
func hit(number int, repo string, ageDays int) map[string]any {
	return map[string]any{
		"number":     number,
		"title":      fmt.Sprintf("Change number %d", number),
		"url":        fmt.Sprintf("https://github.com/%s/pull/%d", repo, number),
		"author":     map[string]any{"login": "dana"},
		"repository": map[string]any{"nameWithOwner": repo},
		"createdAt":  time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
}

// stubGH puts a gh first on PATH that answers the refresh's questions from
// files, as the real one would answer them from GitHub, and logs every call it
// was asked. It is a POSIX sh script, since it runs on both CI platforms.
func stubGH(t *testing.T, a ghAnswers) (calls string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	orEmpty := func(rows []map[string]any) []map[string]any {
		if rows == nil {
			return []map[string]any{}
		}
		return rows
	}
	write("requested.json", orEmpty(a.requested))
	write("again.json", orEmpty(a.again))
	write("reviewed.json", orEmpty(a.reviewed))
	write("mine.json", orEmpty(a.mine))
	write("user.json", map[string]any{"login": "me"})
	for number, latest := range a.views {
		write(fmt.Sprintf("view-%d.json", number), map[string]any{"latestReviews": latest})
	}

	calls = filepath.Join(dir, "calls")
	script := `#!/bin/sh
d='` + dir + `'
echo "$*" >> "$d/calls"
case "$*" in
  "auth token") echo not-a-real-token ;;
  "api user") cat "$d/user.json" ;;
  *"--review-requested=@me --reviewed-by=@me"*) cat "$d/again.json" ;;
  *"--review-requested=@me"*) cat "$d/requested.json" ;;
  *"--reviewed-by=@me"*) cat "$d/reviewed.json" ;;
  *"--author=@me"*) cat "$d/mine.json" ;;
  "pr view "*)
    n=${3##*/}
    if [ -f "$d/view-$n.json" ]; then cat "$d/view-$n.json"; else echo "no such pull request" >&2; exit 1; fi ;;
  *) echo "gh stub: nothing to say to: $*" >&2; exit 1 ;;
esac
`
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// hideGH rebuilds PATH with what the plugin needs except gh — git, sh, and tmux
// where there is one — so the missing-dependency path can be driven on a machine
// that has gh installed, which is most of them.
func hideGH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"git", "sh", "tmux", "claude"} {
		resolved, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(resolved, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("link %s: %v", tool, err)
		}
	}
	t.Setenv("PATH", dir)
}

// ---- the pill --------------------------------------------------------------

// TestThePillShowsOnlyThePanesFreshRequests: one cache holds every repository,
// and the pill is one repository's fresh requests out of it — never another
// repository's, never a stale one, and never your own pull request, which the
// picker offers and the pill does not.
func TestThePillShowsOnlyThePanesFreshRequests(t *testing.T) {
	f := newFixture(t, "")
	repo := fixtureRepo(t, f)
	writeCache(t, time.Now(),
		pull(11, repo, reviews.New, 1),
		pull(12, repo, reviews.Again, 2),
		pull(13, repo, reviews.Changes, 30),
		pull(14, repo, reviews.Yours, 1),
		pull(21, elsewhereOnGitHub, reviews.New, 1),
	)

	// From a subdirectory, which is where a pane usually is.
	sub := filepath.Join(f.MainDir, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r := f.exec("reviews", "segment", "-d", sub)
	if r.err != nil {
		t.Fatalf("segment: %v\n%s", r.err, r.both())
	}
	for _, want := range []string{"#[reverse] PR", "#[range=user|tw-pr-11]#11#[norange]", "tw-pr-12]#[underscore]#12"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("pill = %q, want it to hold %q", r.stdout, want)
		}
	}
	for _, unwanted := range []string{"tw-pr-13", "tw-pr-14", "tw-pr-21"} {
		if strings.Contains(r.stdout, unwanted) {
			t.Errorf("pill = %q, want no %s", r.stdout, unwanted)
		}
	}
	if strings.HasSuffix(r.stdout, "\n") {
		t.Errorf("pill = %q, want no newline: tmux draws the job's output as it stands", r.stdout)
	}
}

// TestThePillOfAWorktreeIsItsRepositorys: a worktree answers with its main
// checkout's repository, so every window in a repository's session draws the
// same pill.
func TestThePillOfAWorktreeIsItsRepositorys(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now(), pull(11, fixtureRepo(t, f), reviews.New, 1))
	wt := f.DirFor("eng-1")
	f.Git(f.MainDir, "worktree", "add", "-b", "john/eng-1", wt)

	r := f.exec("reviews", "segment", "--dir", wt)
	if !strings.Contains(r.stdout, "tw-pr-11") {
		t.Errorf("pill from a worktree = %q, want its repository's #11\n%s", r.stdout, r.stderr)
	}
}

// TestThePillIsEmptyOutsideARegisteredRepository is the single-config trap. With
// one config registered, ordinary resolution answers with it from any directory,
// so a pane in a scratch directory would have drawn this repository's pill.
func TestThePillIsEmptyOutsideARegisteredRepository(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now(), pull(11, fixtureRepo(t, f), reviews.New, 1))

	elsewhere := t.TempDir()
	r := f.exec("reviews", "segment", "-d", elsewhere)
	if r.err != nil || r.stdout != "" {
		t.Errorf("segment outside every repository = %q, %v — want nothing at all, and no failure", r.stdout, r.err)
	}

	// And a git repository nobody registered is outside just the same.
	gitDir := t.TempDir()
	f.Git(gitDir, "init", "-q")
	if r := f.exec("reviews", "segment", "-d", gitDir); r.stdout != "" {
		t.Errorf("segment in an unregistered repository = %q, want nothing", r.stdout)
	}
}

// TestThePillIsEmptyWhileNothingIsWaiting: an empty queue leaves the status line
// exactly as it was, frame and all.
func TestThePillIsEmptyWhileNothingIsWaiting(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now(), pull(21, elsewhereOnGitHub, reviews.New, 1), pull(14, fixtureRepo(t, f), reviews.Yours, 1))

	if r := f.exec("reviews", "segment", "-d", f.MainDir); r.stdout != "" || r.err != nil {
		t.Errorf("segment = %q, %v — want nothing while nothing is waiting here", r.stdout, r.err)
	}
}

// TestThePillTakesAStyleAndALabel: the look is the person's. A style that would
// close the pill's #[...] early is refused rather than drawn as stray text.
func TestThePillTakesAStyleAndALabel(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now(), pull(11, fixtureRepo(t, f), reviews.Changes, 1))

	r := f.exec("reviews", "segment", "-d", f.MainDir, "--style", "fg=black,bg=red,bold", "--label=GH")
	if !strings.HasPrefix(r.stdout, "#[fg=black,bg=red,bold] GH ") {
		t.Errorf("pill = %q, want the style and the label in front", r.stdout)
	}
	if !strings.Contains(r.stdout, "#[dim]#11#[norange]#[default,fg=black,bg=red,bold]") {
		t.Errorf("pill = %q, want the kind's attribute reset to the pill's own style after it", r.stdout)
	}
	if r := f.exec("reviews", "segment", "-d", f.MainDir, "--label", ""); !strings.HasPrefix(r.stdout, "#[reverse]  #[range") {
		t.Errorf("pill with an empty label = %q, want the numbers alone rather than the default label", r.stdout)
	}

	r = f.exec("reviews", "segment", "--style", "fg=red]x")
	if r.err == nil || !strings.Contains(r.stderr, "bracket") {
		t.Errorf("a style with a bracket in it = %v\n%s, want it refused", r.err, r.stderr)
	}
}

// TestAnOldCacheIsRefreshedInTheBackground: the pill is drawn from the cache it
// has, at once, and a refresh started beside it lands for the next redraw. The
// refresh is this test binary re-run as treewright — see TestMain.
func TestAnOldCacheIsRefreshedInTheBackground(t *testing.T) {
	f := newFixture(t, "")
	repo := fixtureRepo(t, f)
	writeCache(t, time.Now().Add(-time.Hour), pull(11, repo, reviews.New, 1))
	stubGH(t, ghAnswers{requested: []map[string]any{hit(31, repo, 1)}})

	r := f.exec("reviews", "segment", "-d", f.MainDir)
	if !strings.Contains(r.stdout, "tw-pr-11") {
		t.Errorf("pill = %q, want the old cache drawn while the refresh runs", r.stdout)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c := reviews.Load(reviews.Dir()); c.Fresh(time.Now()) && !reviews.Locked(reviews.Dir()) {
			if len(c.Pulls) != 1 || c.Pulls[0].Number != 31 {
				t.Errorf("refreshed cache = %+v, want #31 alone", c.Pulls)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	why, _, _ := reviews.LastFailure(reviews.Dir())
	t.Fatalf("no background refresh landed in the cache (last failure: %q)", why)
}

// TestAFreshCacheStartsNoRefresh: the status line asks GitHub nothing while the
// cache is younger than five minutes, however often it redraws.
func TestAFreshCacheStartsNoRefresh(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now(), pull(11, fixtureRepo(t, f), reviews.New, 1))
	calls := stubGH(t, ghAnswers{})

	for range 3 {
		f.exec("reviews", "segment", "-d", f.MainDir)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(calls); err == nil {
		body, _ := os.ReadFile(calls)
		t.Errorf("gh was asked %q with a fresh cache, want nothing", body)
	}
}

// ---- refreshing ------------------------------------------------------------

// TestARefreshSaysWhatEachPullRequestIsToYou: the queue, the re-requests in it,
// the ones you reviewed without approving, and your own — and an approval takes
// a pull request off the list.
func TestARefreshSaysWhatEachPullRequestIsToYou(t *testing.T) {
	f := newFixture(t, "")
	repo := fixtureRepo(t, f)
	stubGH(t, ghAnswers{
		requested: []map[string]any{hit(1, repo, 1), hit(2, repo, 2)},
		again:     []map[string]any{hit(2, repo, 2)},
		reviewed:  []map[string]any{hit(2, repo, 2), hit(3, repo, 3), hit(4, elsewhereOnGitHub, 4), hit(5, repo, 5)},
		mine:      []map[string]any{hit(6, repo, 6)},
		views: map[int][]map[string]any{
			3: {{"author": map[string]any{"login": "me"}, "state": "CHANGES_REQUESTED"}},
			4: {{"author": map[string]any{"login": "lee"}, "state": "APPROVED"}, {"author": map[string]any{"login": "me"}, "state": "COMMENTED"}},
			5: {{"author": map[string]any{"login": "me"}, "state": "APPROVED"}},
		},
	})

	r := f.exec("reviews", "refresh")
	if r.err != nil {
		t.Fatalf("refresh: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "cached 5 pull requests across 2 repositories") {
		t.Errorf("stderr = %q, want a count of what was cached", r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing", r.stdout)
	}

	kinds := map[int]reviews.Kind{}
	for _, p := range reviews.Load(reviews.Dir()).Pulls {
		kinds[p.Number] = p.Kind
	}
	want := map[int]reviews.Kind{1: reviews.New, 2: reviews.Again, 3: reviews.Changes, 4: reviews.Commented, 6: reviews.Yours}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Errorf("kinds = %v, want %v — and #5, approved, gone", kinds, want)
	}
}

// TestAFailedRefreshKeepsTheCacheItHad: a search that fails leaves the previous
// list in place rather than writing part of a new one, and says why — to the
// person who typed it, and on disk for the picker and doctor.
func TestAFailedRefreshKeepsTheCacheItHad(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now().Add(-time.Hour), pull(11, fixtureRepo(t, f), reviews.New, 1))

	r := f.exec("reviews", "refresh") // the fixture's gh, which fails
	if r.err == nil || !strings.Contains(r.err.Error(), "gh is stubbed out in this test") {
		t.Fatalf("refresh with a failing gh = %v, want gh's own sentence", r.err)
	}
	if c := reviews.Load(reviews.Dir()); len(c.Pulls) != 1 || c.Pulls[0].Number != 11 {
		t.Errorf("cache after a failed refresh = %+v, want the old one untouched", c.Pulls)
	}
	if why, _, failed := reviews.LastFailure(reviews.Dir()); !failed || !strings.Contains(why, "stubbed out") {
		t.Errorf("recorded failure = %q, %v — want gh's sentence kept", why, failed)
	}

	// And the next refresh that works forgets it.
	stubGH(t, ghAnswers{})
	f.mustRun("reviews", "refresh")
	if why, _, failed := reviews.LastFailure(reviews.Dir()); failed {
		t.Errorf("a failure %q survived a refresh that worked", why)
	}
}

// TestARefreshWithoutGHNamesThePathItSearched: the status line runs in the tmux
// server's environment, so where gh was looked for is the part of the failure
// worth reading.
func TestARefreshWithoutGHNamesThePathItSearched(t *testing.T) {
	f := newFixture(t, "")
	hideGH(t)

	r := f.exec("reviews", "refresh")
	if r.err == nil {
		t.Fatalf("refresh with no gh succeeded\n%s", r.both())
	}
	msg := r.err.Error()
	for _, want := range []string{"gh, the GitHub CLI, is not on PATH", "searched " + os.Getenv("PATH"), "https://cli.github.com"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to say %q", msg, want)
		}
	}
	if why, _, failed := reviews.LastFailure(reviews.Dir()); !failed || !strings.Contains(why, "not on PATH") {
		t.Errorf("recorded failure = %q, want it kept for the picker and doctor", why)
	}
}

// TestASecondRefreshDefersToTheOneRunning: one refresh at a time, however many
// status lines noticed the cache was old at once.
func TestASecondRefreshDefersToTheOneRunning(t *testing.T) {
	f := newFixture(t, "")
	calls := stubGH(t, ghAnswers{})
	release, err := reviews.Lock(reviews.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	r := f.exec("reviews", "refresh")
	if r.err != nil || !strings.Contains(r.stderr, "a refresh is already running") {
		t.Errorf("refresh beside a running one = %v\n%s, want it to say so and succeed", r.err, r.stderr)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Errorf("gh was asked something while another refresh held the lock")
	}
}

// ---- the click -------------------------------------------------------------

// stubOpener puts open and xdg-open first on PATH, each writing the URL it was
// handed to a file rather than launching a browser.
func stubOpener(t *testing.T) (opened string) {
	t.Helper()
	bin := t.TempDir()
	opened = filepath.Join(t.TempDir(), "opened")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> '" + opened + "'\n"
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return opened
}

// TestAClickOpensThePullRequestThePillDrew: the range carries only the number,
// so the repository comes from the clicking pane — and of two rows for one pull
// request, the one the pill drew, never your own.
func TestAClickOpensThePullRequestThePillDrew(t *testing.T) {
	f := newFixture(t, "")
	repo := fixtureRepo(t, f)
	writeCache(t, time.Now(),
		pull(21, elsewhereOnGitHub, reviews.New, 1),
		pull(21, repo, reviews.Yours, 1),
		pull(21, repo, reviews.Again, 1),
	)
	opened := stubOpener(t)

	r := f.exec("reviews", "click", "-d", f.MainDir, "tw-pr-21")
	if r.err != nil {
		t.Fatalf("click: %v\n%s", r.err, r.both())
	}
	waitForContent(t, opened, "https://github.com/"+repo+"/pull/21\n", "the click")
}

// TestAClickFromOutsideFallsBackToTheNumber covers a pane that has left the
// repository its pill was drawn for.
func TestAClickFromOutsideFallsBackToTheNumber(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now(), pull(21, elsewhereOnGitHub, reviews.New, 1))
	opened := stubOpener(t)

	f.mustRun("reviews", "click", "-d", t.TempDir(), "tw-pr-21")
	waitForContent(t, opened, "https://github.com/"+elsewhereOnGitHub+"/pull/21", "the click")
}

// TestAClickThatMissesSucceeds: tmux puts a failing binding's exit status over
// the status line, which is what a click on a number that has left the cache
// would otherwise look like.
func TestAClickThatMissesSucceeds(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now(), pull(21, fixtureRepo(t, f), reviews.New, 1))
	opened := stubOpener(t)

	for _, rangeName := range []string{"tw-pr-99", "window|3", "tw-pr-", "tw-pr-021", ""} {
		if r := f.exec("reviews", "click", "-d", f.MainDir, rangeName); r.err != nil {
			t.Errorf("click on %q = %v, want exit 0", rangeName, r.err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if body, err := os.ReadFile(opened); err == nil {
		t.Errorf("a missed click opened %q", body)
	}
}

// ---- the picker and its handoff --------------------------------------------

// TestThePickerSaysWhenNothingIsWaiting, rather than showing an empty menu.
func TestThePickerSaysWhenNothingIsWaiting(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now(), pull(21, elsewhereOnGitHub, reviews.New, 1))

	r := f.exec("reviews")
	if r.err != nil {
		t.Fatalf("reviews: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "no pull requests waiting on you in proj") {
		t.Errorf("stderr = %q, want it to say nothing is waiting here", r.stderr)
	}
}

// TestThePickerOutsideARegisteredRepositorySaysSo, and how to name one.
func TestThePickerOutsideARegisteredRepositorySaysSo(t *testing.T) {
	f := newFixture(t, "")
	r := f.exec("reviews", "--dir", t.TempDir())
	if r.err == nil {
		t.Fatalf("picker outside every repository succeeded\n%s", r.both())
	}
	msg := flat(r.err.Error())
	for _, want := range []string{"not in a repository treewright manages", "--repo"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want %q", msg, want)
		}
	}
}

// TestThePickerAsksGitHubWhenThereIsNoCacheYet: somebody who bound the key and
// never placed the pill would otherwise have nothing to pick from, ever.
func TestThePickerAsksGitHubWhenThereIsNoCacheYet(t *testing.T) {
	f := newFixture(t, "")
	stubGH(t, ghAnswers{requested: []map[string]any{hit(21, elsewhereOnGitHub, 1)}})

	r := f.exec("reviews", "--repo", "proj")
	if r.err != nil {
		t.Fatalf("reviews: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "asking GitHub") || !strings.Contains(r.stderr, "no pull requests waiting on you in proj") {
		t.Errorf("stderr = %q, want GitHub asked and the answer given", r.stderr)
	}
	if c := reviews.Load(reviews.Dir()); len(c.Pulls) != 1 {
		t.Errorf("cache = %+v, want what GitHub said written down", c.Pulls)
	}
}

// TestThePickerRefusesWithoutGH, in one sentence naming what is missing, when it
// has nothing cached to show instead.
func TestThePickerRefusesWithoutGH(t *testing.T) {
	f := newFixture(t, "")
	hideGH(t)
	r := f.exec("reviews")
	if r.err == nil || !strings.Contains(r.err.Error(), "gh, the GitHub CLI, is not on PATH") {
		t.Errorf("picker with no gh and no cache = %v, want the missing dependency named", r.err)
	}
}

// TestThePickerSaysWhenItsListIsOld: a refresh the status line started has
// nobody waiting on it, so its failure is said by the first thing somebody
// opens.
func TestThePickerSaysWhenItsListIsOld(t *testing.T) {
	f := newFixture(t, "")
	writeCache(t, time.Now().Add(-3*time.Hour), pull(21, elsewhereOnGitHub, reviews.New, 1))
	if err := reviews.RecordFailure(reviews.Dir(), "gh search: could not resolve host"); err != nil {
		t.Fatal(err)
	}
	// A lock, so the picker starts no refresh of its own to race the assertion.
	release, err := reviews.Lock(reviews.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	r := f.exec("reviews")
	got := flat(r.stderr)
	for _, want := range []string{"warning: the last refresh failed", "fetched 3 hours ago", "could not resolve host"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	}
}

// TestReviewingAPullRequestOpensAWindowOfItsOwn: review-<number>, titled
// "review: <number>", its agent started on the repository's review_prompt — the
// handoff the picker makes once a row is chosen.
func TestReviewingAPullRequestOpensAWindowOfItsOwn(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "prompt")
	f := newFixture(t, "command = \"printf %s {prompt} > "+marker+"; sleep 300\"\n")
	cfg, err := resolveConfig("")
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	env := &Env{Argv0: "tw", Stdout: &out, Stderr: &errOut}
	if err := reviewInWindow(env, cfg, "", pull(42, fixtureRepo(t, f), reviews.New, 1)); err != nil {
		t.Fatalf("review: %v\n%s", err, errOut.String())
	}
	waitForContent(t, marker, "review pull request #42", "the review window's agent")
	if got := windowsIn(t, "proj"); !slices.Contains(got, "review: 42") {
		t.Errorf("windows = %v, want one titled \"review: 42\"", got)
	}
	if _, ok := tmux.Scratch("proj")["review-42"]; !ok {
		t.Errorf("no scratch session review-42 — the review should be one, reachable by name")
	}
}

// TestAReviewPromptOfTheRepositorysOwnIsWhatIsTyped: a repository whose agent
// has a command for reviewing says so in its config.
func TestAReviewPromptOfTheRepositorysOwnIsWhatIsTyped(t *testing.T) {
	requireTmux(t)
	marker := filepath.Join(t.TempDir(), "prompt")
	f := newFixture(t, "command = \"printf %s {prompt} > "+marker+"; sleep 300\"\nreview_prompt = '/review {number} carefully'\n")
	cfg, err := resolveConfig("")
	if err != nil {
		t.Fatal(err)
	}
	env := &Env{Argv0: "tw", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := reviewInWindow(env, cfg, "proj", pull(7, fixtureRepo(t, f), reviews.New, 1)); err != nil {
		t.Fatal(err)
	}
	waitForContent(t, marker, "/review 7 carefully", "the review window's agent")
}

// TestTheReviewTableCutsTitlesToFit: a title that wrapped would break the one
// row per pull request the picker numbers.
func TestTheReviewTableCutsTitlesToFit(t *testing.T) {
	now := time.Now()
	long := pull(1, elsewhereOnGitHub, reviews.Again, 20)
	long.Title = strings.Repeat("a long title ", 10)
	table := reviewTable([]reviews.Pull{long, pull(2, elsewhereOnGitHub, reviews.New, 0)}, now, 20)
	header, rows := table.Lines(false)
	if !strings.HasPrefix(header, "PR  AGE") || !strings.Contains(header, "KIND") {
		t.Errorf("header = %q", header)
	}
	if !strings.HasSuffix(rows[0], "…") || strings.Contains(rows[0], strings.Repeat("a long title ", 3)) {
		t.Errorf("row = %q, want the title cut to twenty columns", rows[0])
	}
	for _, want := range []string{"#1", "2w ago", "dana", "re-request"} {
		if !strings.Contains(rows[0], want) {
			t.Errorf("row = %q, want %q", rows[0], want)
		}
	}
	if !strings.Contains(rows[1], "today") {
		t.Errorf("row = %q, want a pull request opened today to say so", rows[1])
	}
}

// TestThePopupIsSizedToTheQueue: tmux fixes a popup's size when it opens, so the
// size is worked out from the cache first, as the worktree picker's is from the
// worktrees — and capped, titles being cut rather than the popup grown.
func TestThePopupIsSizedToTheQueue(t *testing.T) {
	f := newFixture(t, "")
	repo := fixtureRepo(t, f)

	if w, h := reviewsPopupSize(); w != 64 || h != 4 {
		t.Errorf("empty queue popup = %dx%d, want 64x4 for its one sentence", w, h)
	}

	writeCache(t, time.Now(), pull(1, repo, reviews.New, 1), pull(2, repo, reviews.Yours, 3))
	w, h := reviewsPopupSize()
	table := reviewTable([]reviews.Pull{pull(1, repo, reviews.New, 1), pull(2, repo, reviews.Yours, 3)}, time.Now(), 0)
	_, rows := table.Lines(false)
	widest := 0
	for _, row := range rows {
		widest = max(widest, len(row))
	}
	if w < widest+pickerIndexWidth(2)+2 || w > 110 {
		t.Errorf("popup width = %d, want room for a %d-column row and its index, and no more than 110", w, widest)
	}
	if h != 1+2+1+1+2 {
		t.Errorf("popup height = %d, want a header, two rows, a gap, the prompt and the border", h)
	}

	// Through sizeFor, which is what `popup reviews` asks.
	if sw, sh := sizeFor(&Env{Argv0: "tw"}, "reviews"); sw != w || sh != h {
		t.Errorf("sizeFor(reviews) = %dx%d, want the plugin's own %dx%d", sw, sh, w, h)
	}
}

// ---- invocations -----------------------------------------------------------

// TestReviewsRefusesWhatItsSubcommandDoesNotTake: one flag list for the whole
// command, so help can document it in one place — and each sub-command says
// when it was handed one of the others'.
func TestReviewsRefusesWhatItsSubcommandDoesNotTake(t *testing.T) {
	f := newFixture(t, "")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"reviews", "--style", "bold"}, `--style is for "reviews segment"`},
		{[]string{"reviews", "--repo", "proj", "--dir", "."}, "not both"},
		{[]string{"reviews", "segment", "--repo", "proj"}, "segment takes --dir"},
		{[]string{"reviews", "click", "--label", "x", "tw-pr-1"}, "click takes only --dir"},
		{[]string{"reviews", "refresh", "-d", "."}, "refresh takes no flags"},
		{[]string{"reviews", "refresh", "now"}, `unexpected argument "now"`},
		{[]string{"reviews", "segment", "extra"}, `unexpected argument "extra"`},
		{[]string{"reviews", "pill"}, `no sub-command "pill"`},
	} {
		r := f.exec(tc.args...)
		if r.err == nil || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%v = %v\n%s\nwant a usage error saying %q", tc.args, r.err, r.stderr, tc.want)
		}
	}
}

// TestPluginsAreListedUnderTheirOwnHeading: the first screen of help is still
// the worktree tool.
func TestPluginsAreListedUnderTheirOwnHeading(t *testing.T) {
	f := newFixture(t, "")
	out := f.mustRun("help")
	heading := strings.Index(out, "\nplugins")
	reviewsAt := strings.Index(out, "\n  reviews ")
	versionAt := strings.Index(out, "\n  version ")
	if heading < 0 || reviewsAt < heading || versionAt > heading {
		t.Errorf("help = %q\nwant reviews under a plugins heading, after every core command", out)
	}
}

// TestReviewsCompletesItsSubcommands from the list cli owns, rather than three
// copies in three shims.
func TestReviewsCompletesItsSubcommands(t *testing.T) {
	f := newFixture(t, "")
	if got := f.mustRun("__complete", "reviews"); got != "segment\nclick\nrefresh\n" {
		t.Errorf("completion = %q", got)
	}
}

// ---- doctor ----------------------------------------------------------------

// TestDoctorSaysNothingAboutAPluginNobodyUsed: no section, and no finding about
// a gh nobody needed.
func TestDoctorSaysNothingAboutAPluginNobodyUsed(t *testing.T) {
	f := newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	for _, fi := range findings(t, f) {
		if strings.HasPrefix(fi.detail, "reviews:") {
			t.Errorf("doctor reported %q for a plugin nobody opted into", fi.detail)
		}
	}
}

// TestDoctorReportsThePluginOnceUsed: gh, its login, and how the last refresh
// went — read from disk, so an offline laptop gets no network call made for it.
func TestDoctorReportsThePluginOnceUsed(t *testing.T) {
	f := newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	stubGH(t, ghAnswers{})
	writeCache(t, time.Now().Add(-2*time.Minute), pull(1, elsewhereOnGitHub, reviews.New, 1), pull(2, elsewhereOnGitHub, reviews.New, 1))

	found := findings(t, f)
	for _, tc := range []struct{ substr, want string }{
		{"reviews: gh login logged in", "ok"},
		{"reviews: refresh 2 pull requests cached, 2 minutes ago", "ok"},
	} {
		if got := has(t, found, tc.substr); got != tc.want {
			t.Errorf("%q = %q, want %q\n%v", tc.substr, got, tc.want, found)
		}
	}

	if err := reviews.RecordFailure(reviews.Dir(), "gh search: HTTP 401"); err != nil {
		t.Fatal(err)
	}
	if got := has(t, findings(t, f), "reviews: refresh the last one failed"); got != "warn" {
		t.Errorf("a failed refresh = %q, want a warning", got)
	}
}

// TestDoctorWarnsWhenThePluginHasNoGH: the plugin is in use, and nothing it does
// can work.
func TestDoctorWarnsWhenThePluginHasNoGH(t *testing.T) {
	f := newFixture(t, "command = 'true'\nresume_command = 'true'\n")
	writeCache(t, time.Now(), pull(1, elsewhereOnGitHub, reviews.New, 1))
	hideGH(t)
	if got := has(t, findings(t, f), "reviews: gh not on PATH"); got != "warn" {
		t.Errorf("no gh = %q, want a warning", got)
	}
}

// TestTitlesAreCutToTheTerminalTheyAreDrawnIn: the room left for a title is the
// terminal's width less everything in front of it, so the longest row ends at
// the edge rather than wrapping onto a second, unnumbered line.
func TestTitlesAreCutToTheTerminalTheyAreDrawnIn(t *testing.T) {
	now := time.Now()
	long := pull(1, elsewhereOnGitHub, reviews.New, 1)
	long.Title = strings.Repeat("word ", 40)
	rows := []reviews.Pull{long, pull(2, elsewhereOnGitHub, reviews.Again, 1)}

	if got := titleRoom(0, rows, now); got != 0 {
		t.Errorf("titleRoom with no terminal = %d, want no limit", got)
	}
	room := titleRoom(80, rows, now)
	table := reviewTable(rows, now, room)
	_, lines := table.Lines(false)
	if got := pickerIndexWidth(len(rows)) + len([]rune(lines[0])); got != 80 {
		t.Errorf("the longest row is %d columns with its index, want exactly the 80 the terminal has", got)
	}
	if got := titleRoom(20, rows, now); got != 12 {
		t.Errorf("titleRoom in a terminal too narrow for the table = %d, want the floor of 12", got)
	}
}

// TestThePickerWaitsForARefreshAlreadyRunning: with nothing cached yet and a
// refresh the status line started still out asking, the picker waits for that
// one rather than starting a second.
func TestThePickerWaitsForARefreshAlreadyRunning(t *testing.T) {
	f := newFixture(t, "")
	calls := stubGH(t, ghAnswers{})
	release, err := reviews.Lock(reviews.Dir())
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = reviews.Save(reviews.Dir(), reviews.Cache{Fetched: time.Now(), Pulls: []reviews.Pull{pull(21, elsewhereOnGitHub, reviews.New, 1)}})
		release()
	}()

	r := f.exec("reviews")
	if r.err != nil {
		t.Fatalf("reviews: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "a refresh is already running") || !strings.Contains(r.stderr, "no pull requests waiting on you in proj") {
		t.Errorf("stderr = %q, want it to wait for the running refresh and then answer", r.stderr)
	}
	if _, err := os.Stat(calls); err == nil {
		t.Errorf("the picker asked gh itself while another refresh held the lock")
	}
}
