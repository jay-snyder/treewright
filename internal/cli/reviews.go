package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/plugin/reviews"
	"github.com/jay-snyder/treewright/internal/tmux"
	"github.com/jay-snyder/treewright/internal/ui"
)

// The reviews plugin: the pull requests waiting on your review, for the
// repository a pane is standing in. A pill in the status line, a popup picker,
// and a review run in a scratch window of its own. docs/plugins.md is the
// design, and the rules every plugin is held to.
//
// This file is the half of the plugin that asks treewright — which registered
// repository a directory is in, the scratch window a review goes to, the popup
// it is picked in, doctor's report. internal/plugin/reviews is the half that
// asks GitHub, and knows nothing about worktrees or windows.

// Sub-commands of reviews. The bare command is the picker, which is what a
// person types and what the popup runs; these are what tmux and the segment
// itself run, so a person pastes them once and never types them.
const (
	reviewsCommand = "reviews"
	reviewsSegment = "segment"
	reviewsClick   = "click"
	reviewsRefresh = "refresh"
)

// reviewsSubcommands is the list completion offers after "reviews", in the
// order help names them.
var reviewsSubcommands = []string{reviewsSegment, reviewsClick, reviewsRefresh}

// The flags of reviews, named once because which sub-command takes which is
// checked against them. One flag set for the whole command rather than one per
// sub-command: help documents a command's flags in one list, and the parser
// has to accept every flag that list names whichever sub-command follows.
const (
	dirFlag   = "--dir"
	styleFlag = "--style"
	labelFlag = "--label"
)

// reviewWindow names the scratch window a pull request's review runs in, and
// reviewTitle the title its window is given. A scratch name follows a slug's
// rules, which allow neither the colon nor the space the title has.
func reviewWindow(number int) string { return "review-" + strconv.Itoa(number) }
func reviewTitle(number int) string  { return "review: " + strconv.Itoa(number) }

func cmdReviews(env *Env, args []string) error {
	var repoName, dir, style, label string
	positional, err := parseArgs("reviews", args, nil, repoValues(&repoName, map[string]*string{
		"-d": &dir, dirFlag: &dir, styleFlag: &style, labelFlag: &label,
	}), 2)
	if err != nil {
		return err
	}
	sub := at(positional, 0)
	given := func(names ...string) []string { return flagsGiven(args, names...) }

	switch sub {
	case "":
		if extra := given(styleFlag, labelFlag); len(extra) > 0 {
			return usageErrorf("reviews", "%s is for %q, which draws the pill", extra[0], "reviews segment")
		}
		if repoName != "" && dir != "" {
			return usageErrorf("reviews", "name the repository with %s or %s, not both", repoFlag, dirFlag)
		}
		return pickReview(env, repoName, dir)
	case reviewsSegment:
		if extra := given(repoFlag); len(extra) > 0 {
			return usageErrorf("reviews", "%s takes %s, the pane's directory — a status line knows that, not a config name", sub, dirFlag)
		}
		if at(positional, 1) != "" {
			return usageErrorf("reviews", "unexpected argument %q", at(positional, 1))
		}
		// Defaulted on whether the flag was given rather than on the value, so
		// --label '' is a pill of numbers alone rather than one saying PR.
		if len(given(styleFlag)) == 0 {
			style = reviews.DefaultPillStyle
		}
		if len(given(labelFlag)) == 0 {
			label = reviews.DefaultPillLabel
		}
		if err := reviews.CheckStyle(style); err != nil {
			return usageErrorf("reviews", "%v", err)
		}
		return drawPill(env, dir, style, label)
	case reviewsClick:
		if extra := given(repoFlag, styleFlag, labelFlag); len(extra) > 0 {
			return usageErrorf("reviews", "%s takes only %s and the range that was clicked", sub, dirFlag)
		}
		return openClicked(env, dir, at(positional, 1))
	case reviewsRefresh:
		if extra := given(repoFlag, "-d", dirFlag, styleFlag, labelFlag); len(extra) > 0 {
			return usageErrorf("reviews", "%s takes no flags — it asks about every repository at once", sub)
		}
		if at(positional, 1) != "" {
			return usageErrorf("reviews", "unexpected argument %q", at(positional, 1))
		}
		return refreshReviews(env)
	default:
		return usageErrorf("reviews", "no sub-command %q (have: %s)", sub, strings.Join(reviewsSubcommands, ", "))
	}
}

// flagsGiven returns which of names appear in args, spelled either way
// parseArgs accepts — "--style x" or "--style=x".
func flagsGiven(args []string, names ...string) []string {
	var found []string
	for _, name := range names {
		if slices.Contains(args, name) || hasInline(args, name) {
			found = append(found, name)
		}
	}
	return found
}

func hasInline(args []string, name string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, name+"=") })
}

// ---- which repository ------------------------------------------------------

// errNotARegisteredRepo is a directory outside every repository treewright
// manages, which is where a pill is not drawn and a picker has nothing to
// offer.
var errNotARegisteredRepo = errors.New("not in a repository treewright manages")

// configAt is the registered config for the repository dir is in, matched
// exactly — never the single-config fallback resolveConfig gives.
//
// That fallback answers with the only registered config from any directory at
// all, which is right for a command a person types and wrong for a status
// line: a pane in /tmp would draw that repository's pill. The popup's --dir
// exists because of the same trap; see "Why the bindings pass
// #{pane_current_path}" in docs/tmux.md. guardScopeFor resolves the same way.
func configAt(dir string) (*config.Config, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		dir = wd
	}
	main, err := (git.Repo{Dir: dir}).MainDir()
	if err != nil {
		return nil, errNotARegisteredRepo
	}
	cfg, err := config.Resolve("", main)
	if err != nil {
		return nil, errNotARegisteredRepo
	}
	return cfg, nil
}

// githubRepo is the owner/name GitHub knows a repository by, read off its main
// checkout's origin, or "" when origin has no such shape.
//
// The one fact here treewright does not already know, and so the one this
// plugin works out for itself: the repository is treewright's answer, and
// what GitHub calls it is the remote's.
func githubRepo(cfg *config.Config) string {
	url, err := repoFor(cfg).RemoteURL("origin")
	if err != nil {
		return ""
	}
	return reviews.RepoOf(url)
}

// ---- the pill --------------------------------------------------------------

// drawPill prints the status-line pill for the repository dir is in.
//
// tmux runs it every status-interval for every attached client, so it reads
// the cache and asks git one question, and touches the network never: a cache
// older than reviews.MaxAge has a refresh started beside it, in the
// background, and the pill is drawn from what is there. Whatever goes wrong it
// prints nothing — a status line has nowhere to show an error — and the
// refresh records its own failures for the picker and doctor.
func drawPill(env *Env, dir, style, label string) error {
	dir = orHere(dir)
	cacheDir := reviews.Dir()
	cache := reviews.Load(cacheDir)
	// Ahead of every early return below, so whichever pane is on screen keeps
	// the cache warm — including one outside any repository, with no pill of
	// its own to draw.
	refreshIfDue(cacheDir, cache)

	// A pane outside every registered repository draws no pill, and says
	// nothing about why.
	if cfg, err := configAt(dir); err == nil {
		fmt.Fprint(env.Stdout, reviews.Pill(cache.Pulls, githubRepo(cfg), time.Now(), style, label))
	}
	return nil
}

// orHere is dir, or the working directory when none was given — which is what
// a person typing the command at a prompt means.
func orHere(dir string) string {
	if dir != "" {
		return dir
	}
	wd, _ := os.Getwd()
	return wd
}

// refreshIfDue starts a refresh in the background when the cache is older than
// reviews.MaxAge and no refresh is already running.
func refreshIfDue(cacheDir string, cache reviews.Cache) {
	if cache.Fresh(time.Now()) || reviews.Locked(cacheDir) {
		return
	}
	startRefresh()
}

// startRefresh runs `treewright reviews refresh` detached, so it outlives the
// status-line job that started it.
//
// The binary by its own path, as the popup is reached: the job runs in the
// tmux server's environment, which found treewright once already and need not
// find the same one again. A session of its own, so the job's exit — which
// tmux waits for, every few seconds — never waits on GitHub, and nothing sent
// to the job's process group reaches it. Its output goes nowhere: what it has
// to report, it records.
func startRefresh() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, reviewsCommand, reviewsRefresh)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}

// ---- the click -------------------------------------------------------------

// openClicked opens the pull request a status-line range names, which is what
// the printed MouseDown1Status binding runs for a click on the pill.
//
// It exits 0 whatever happens. tmux reports a binding's failing exit status as
// a message over the status line, which is what a click on a number that has
// since left the cache would otherwise look like; what went wrong goes to
// stderr, where run-shell drops it and a person typing this sees it.
func openClicked(env *Env, dir, rangeName string) error {
	cache := reviews.Load(reviews.Dir())
	repo := ""
	if cfg, err := configAt(orHere(dir)); err == nil {
		repo = githubRepo(cfg)
	}
	pull, ok := reviews.Clicked(cache.Pulls, rangeName, repo)
	if !ok {
		return nil
	}
	if err := reviews.Open(pull.URL); err != nil {
		env.warnf("%v", err)
	}
	return nil
}

// ---- refreshing ------------------------------------------------------------

// refreshReviews asks GitHub now and rewrites the cache, saying what it found.
//
// It is what the segment starts in the background, and what a person types to
// see why the pill is not changing. Either way a failure is recorded beside the
// cache before it is returned, so a refresh nobody waited for still has its
// failure read by somebody: the picker and doctor both report it.
func refreshReviews(env *Env) error {
	pulls, err := fetchReviews(reviews.Dir())
	if errors.Is(err, reviews.ErrLocked) {
		env.progressf("a refresh is already running\nits answer lands in the cache when it finishes")
		return nil
	}
	if err != nil {
		return err
	}
	repos := map[string]bool{}
	for _, p := range pulls {
		repos[p.Repo] = true
	}
	env.progressf("cached %s across %s", count(len(pulls), "pull request", "pull requests"),
		count(len(repos), "repository", "repositories"))
	return nil
}

// fetchReviews is the refresh itself: the lock, the queries, and the cache
// written whole or not at all. A failure leaves the previous cache as it was
// and is recorded; a success clears the record.
func fetchReviews(cacheDir string) ([]reviews.Pull, error) {
	release, err := reviews.Lock(cacheDir)
	if err != nil {
		return nil, err
	}
	defer release()

	recorded := func(err error) error {
		_ = reviews.RecordFailure(cacheDir, err.Error())
		return err
	}
	gh, err := reviews.GH()
	if err != nil {
		return nil, recorded(err)
	}
	pulls, err := reviews.Fetch(gh)
	if err != nil {
		return nil, recorded(err)
	}
	if err := reviews.Save(cacheDir, reviews.Cache{Fetched: time.Now(), Pulls: pulls}); err != nil {
		return nil, recorded(err)
	}
	reviews.ClearFailure(cacheDir)
	return pulls, nil
}

// waitForRefresh waits for a refresh another process is running, for a picker
// that has nothing to show until it lands. It gives up after the time a
// refresh's own calls are allowed, which is far longer than one takes.
func waitForRefresh(cacheDir string) {
	deadline := time.Now().Add(45 * time.Second)
	for reviews.Locked(cacheDir) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
}

// ---- the picker ------------------------------------------------------------

// pickReview lists the pull requests waiting on you in a repository, and opens
// a review of the one chosen.
//
// It reads the cache, as the pill does, and asks GitHub itself only when the
// cache has never been written — somebody who bound the key and never placed
// the pill would otherwise have nothing to pick from, ever. A cache that is
// merely old is shown as it is, a refresh started beside it: with the pill in
// place it is minutes old at worst, and a popup that waited on four searches
// every time would be a popup nobody opened twice.
func pickReview(env *Env, repoName, dir string) error {
	cfg, err := reviewsRepository(repoName, dir)
	if err != nil {
		return err
	}
	repo := githubRepo(cfg)
	if repo == "" {
		return fmt.Errorf("%s has no origin GitHub knows, so there are no pull requests to list%s",
			cfg.Name, asFields(field("main checkout", cfg.MainDir)))
	}

	cacheDir := reviews.Dir()
	cache := reviews.Load(cacheDir)
	if cache.Fetched.IsZero() {
		if reviews.Locked(cacheDir) {
			env.progressf("waiting for GitHub — a refresh is already running")
			waitForRefresh(cacheDir)
		} else {
			env.progressf("asking GitHub for the pull requests waiting on you")
			if _, err := fetchReviews(cacheDir); err != nil && !errors.Is(err, reviews.ErrLocked) {
				return err
			}
		}
		cache = reviews.Load(cacheDir)
	} else {
		refreshIfDue(cacheDir, cache)
	}
	warnIfRefreshFailed(env, cacheDir, cache)

	now := time.Now()
	rows := newestFirst(reviews.ForRepo(cache.Pulls, repo))
	if len(rows) == 0 {
		env.progressf("no pull requests waiting on you in %s", cfg.Name)
		holdThePopup(env)
		return nil
	}

	table := reviewTable(rows, now, titleRoom(ui.Width(env.Stderr), rows, now))
	header, lines := table.Lines(ui.ColorEnabled(env.Stderr))
	idx, err := ui.Pick(env.Stderr, header, lines)
	// Declining is not a failure, and in a popup a non-zero exit would leave
	// "cancelled" on screen for a second Escape to clear — see errCancelled.
	if errors.Is(err, ui.ErrCancelled) {
		env.progressf("cancelled")
		return nil
	}
	if err != nil {
		return err
	}
	return reviewInWindow(env, cfg, repoName, rows[idx])
}

// reviewsRepository is the repository the picker is about: the one named, or
// the one the directory is in, matched exactly.
func reviewsRepository(repoName, dir string) (*config.Config, error) {
	if repoName != "" {
		return resolveConfig(repoName)
	}
	cfg, err := configAt(dir)
	if err != nil {
		return nil, fmt.Errorf("%w\npull requests are listed for the repository you are standing in, or the one %s names",
			err, repoFlag)
	}
	return cfg, nil
}

// warnIfRefreshFailed says the list is stale, and why, when the last refresh
// failed. Said by the picker because somebody opened it and is reading; the
// pill, which nobody asked anything of, says nothing.
func warnIfRefreshFailed(env *Env, cacheDir string, cache reviews.Cache) {
	why, _, failed := reviews.LastFailure(cacheDir)
	if !failed {
		return
	}
	when := "never fetched"
	if !cache.Fetched.IsZero() {
		when = "fetched " + ago(time.Since(cache.Fetched)) + " ago"
	}
	env.warnf("the last refresh failed, so this list is old%s", asFields(
		field("list", when),
		field("failure", why),
	))
}

// holdThePopup keeps a popup open on a message until a key is pressed. tmux
// closes a popup whose command exited 0 at once, which is right for a picker
// that did its job and wrong for one whose whole answer is a sentence.
// Outside a popup the sentence stays on screen by itself.
func holdThePopup(env *Env) {
	if os.Getenv(tmux.PopupEnv) == "" {
		return
	}
	ui.Wait(env.Stderr, ui.Dim.Apply("press any key to close", ui.ColorEnabled(env.Stderr)))
}

// newestFirst sorts a repository's rows by when each was opened, newest at the
// top — your own interleaved with the queue rather than sunk below it, the KIND
// column being what marks them. The cache, and so the pill, keep search order.
func newestFirst(rows []reviews.Pull) []reviews.Pull {
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(a, b reviews.Pull) int { return b.Created.Compare(a.Created) })
	return sorted
}

// kindWord is how the picker names a kind, and kindColor how it shows it. A new
// request is blank, being the ordinary case; the three that are nobody else's
// ask — your own, and the two you have already reviewed — are dim.
var kindWord = map[reviews.Kind]string{
	reviews.Again:     "re-request",
	reviews.Changes:   "changes",
	reviews.Commented: "commented",
	reviews.Yours:     "yours",
}

func kindColor(k reviews.Kind) ui.Color {
	if k == reviews.Again {
		return ui.Plain
	}
	return ui.Dim
}

// reviewTable lays out the picker's rows. Titles are cut to titleMax runes
// when it is above zero, so a long one cannot wrap and break the one row per
// pull request the picker numbers; they link to the pull request where the
// terminal draws links.
func reviewTable(rows []reviews.Pull, now time.Time, titleMax int) ui.Table {
	table := ui.Table{Headers: []string{"PR", "AGE", "AUTHOR", "KIND", "TITLE"}}
	for _, p := range rows {
		age := ui.Cyan
		if p.Stale(now) {
			age = ui.Yellow
		}
		table.Add(
			ui.Text("#"+strconv.Itoa(p.Number)),
			ui.Colored(reviews.AgeLabel(p.Created, now), age),
			ui.Colored(p.Author, ui.Dim),
			ui.Colored(kindWord[p.Kind], kindColor(p.Kind)),
			ui.Linked(cut(p.Title, titleMax), p.URL),
		)
	}
	return table
}

// cut shortens s to max runes, marking the cut with an ellipsis. A max of zero
// or less is no limit.
func cut(s string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}

// titleRoom is how many columns the picker's titles can have in a terminal
// width columns wide, or 0 — no limit — when the width is unknown.
func titleRoom(width int, rows []reviews.Pull, now time.Time) int {
	if width == 0 {
		return 0
	}
	const floor = 12 // a title cut shorter than this says nothing
	return max(floor, width-pickerIndexWidth(len(rows))-titleColumn(reviewTable(rows, now, 0)))
}

// titleColumn is where the TITLE column starts in a rendered table: the width
// of every column before it, with their gutters.
func titleColumn(table ui.Table) int {
	header, _ := table.Lines(false)
	return strings.Index(header, "TITLE")
}

// pickerIndexWidth is the "n) " the picker puts in front of every row, and the
// indent it gives the header.
func pickerIndexWidth(rows int) int { return len(strconv.Itoa(rows)) + 2 }

// reviewInWindow opens a review of a pull request in its own scratch window,
// through `scratch --reuse` — called with the arguments a person would type, so
// everything scratch holds to holds here because it is the same code.
//
// --repo is passed whether or not the picker was given one. The picker found
// the repository exactly, and scratch resolving it again its own way could
// only ever disagree.
func reviewInWindow(env *Env, cfg *config.Config, named string, p reviews.Pull) error {
	name := reviewWindow(p.Number)
	// Whether --reuse will type the prompt or only report it undelivered is
	// decided by the window's agent: one with a name to be messaged at is not
	// typed at. Asked before, since afterwards the window has been switched to.
	undelivered := false
	if w, open := scratchSessions(cfg)[name]; open && w.ID != "" {
		_, _, undelivered = nativeRoute(cfg, w)
	}
	repo := cfg.Name
	if named != "" {
		repo = named
	}
	if err := cmdScratch(env, []string{
		"--reuse", "--prompt", cfg.ReviewPromptFor(p.Number), repoFlag, repo, name, reviewTitle(p.Number),
	}); err != nil {
		return err
	}
	// In a popup that closes on success, the sentence saying the prompt went
	// undelivered would be gone before it was read. Exiting non-zero is what
	// keeps it up, and PopupHint says how to dismiss it.
	if undelivered && os.Getenv(tmux.PopupEnv) != "" {
		return ErrSilent
	}
	return nil
}

// ---- the popup's size ------------------------------------------------------

// reviewsPopupSize is the popup the picker needs, worked out from the cache
// before the popup exists — the reason `popup` exists at all.
func reviewsPopupSize() (width, height int) {
	const (
		border   = 2
		maxWidth = 110 // past this, titles are cut rather than the popup grown
		// The empty picker's sentence, and the prompt beneath it.
		emptyWidth, emptyHeight = 64, 4
		// The picker's own prompt, which sets the floor on a short list.
		promptCol = len("select 1-nn (Esc to cancel): ")
		// A failed refresh's warning above the table: three lines and a gap.
		warning = 4
	)
	cfg, err := configAt("")
	if err != nil {
		return emptyWidth, emptyHeight
	}
	cacheDir := reviews.Dir()
	cache := reviews.Load(cacheDir)
	rows := reviews.ForRepo(cache.Pulls, githubRepo(cfg))
	if len(rows) == 0 {
		return emptyWidth, emptyHeight
	}
	now := time.Now()
	table := reviewTable(rows, now, 0)
	header, lines := table.Lines(false)
	widest := utf8.RuneCountInString(header)
	for _, line := range lines {
		widest = max(widest, utf8.RuneCountInString(line))
	}
	width = min(max(pickerIndexWidth(len(rows))+widest, promptCol)+border, maxWidth)
	// A header, a row each, a blank line, and the prompt.
	height = 1 + len(rows) + 1 + 1 + border
	if _, _, failed := reviews.LastFailure(cacheDir); failed {
		height += warning
	}
	return width, height
}

// ---- doctor ----------------------------------------------------------------

// checkReviews reports on the reviews plugin, once somebody has used it.
//
// "Used" is the cache directory existing, which only the plugin's own commands
// create: placing the pill, binding the key and typing the command all lead
// there, and nothing else does. Somebody who never opted in gets no section and
// no finding about a gh they never needed — a plugin nobody asked for is not a
// thing doctor nags about. It asks GitHub nothing: gh's login is read from its
// local store, and the last refresh's outcome from disk.
func checkReviews(r *report) {
	cacheDir := reviews.Dir()
	if !reviews.Exists(cacheDir) {
		return
	}
	r.in("reviews")
	gh, err := reviews.GH()
	if err != nil {
		r.addf(levelWarn, "gh", "not on PATH\nthe pill and the picker cannot ask GitHub anything without it\ninstall it from https://cli.github.com")
	} else {
		path, _ := exec.LookPath("gh")
		r.addf(levelOK, "gh", "%s", path)
		if reviews.LoggedIn(gh) {
			r.addf(levelOK, "gh login", "logged in")
		} else {
			r.addf(levelWarn, "gh login", "not logged in\nevery refresh fails until it is\nrun gh auth login")
		}
	}

	cache := reviews.Load(cacheDir)
	if why, at, failed := reviews.LastFailure(cacheDir); failed {
		r.addf(levelWarn, "refresh", "the last one failed, %s ago\n%s", ago(time.Since(at)), why)
		return
	}
	if cache.Fetched.IsZero() {
		r.addf(levelOK, "refresh", "none has finished yet")
		return
	}
	r.addf(levelOK, "refresh", "%s cached, %s ago\n%s",
		count(len(cache.Pulls), "pull request", "pull requests"), ago(time.Since(cache.Fetched)), cacheDir)
}

// ago is a duration as a person reads one in passing: the largest unit that
// is not zero, and no smaller.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return count(int(d.Seconds()), "second", "seconds")
	case d < time.Hour:
		return count(int(d.Minutes()), "minute", "minutes")
	case d < 48*time.Hour:
		return count(int(d.Hours()), "hour", "hours")
	default:
		return count(int(d.Hours()/24), "day", "days")
	}
}
