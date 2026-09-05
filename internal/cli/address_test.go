package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jay-snyder/treewright/internal/gittest"
)

// Naming the repository a command acts on, and what that does to the caller's
// tmux client.
//
// Both halves come from one incident. An agent standing in one repository ran
// `new` for another, got a worktree in the repository it was standing in — its
// branch prefix, its base branch, its path — and read a progress line that said
// nothing to contradict that. Then, once the repository could be named, the
// window opened in the other repository's session and tmux switched the
// operator's client into it, twice in one sitting.

// otherRepo registers a second repository in the same registry, with its own
// checkout, its own origin and its own branch prefix.
//
// The prefix is what makes the addressing tests conclusive rather than
// suggestive: a worktree in the right directory could be a coincidence of two
// fixtures sharing a naming scheme, while a branch carrying the other
// repository's prefix could only have come from the other repository's config.
func otherRepo(t *testing.T, registry string) *gittest.Repo {
	t.Helper()
	repo := gittest.New(t)
	addConfig(t, registry, "other", "main_dir = '"+repo.MainDir+"'\n"+
		"base_branch = 'main'\nbranch_prefix = 'y/'\ncommand = 'sleep 300'\n")
	return repo
}

// ---- naming a repository ---------------------------------------------------

// TestNewActsOnTheRepositoryItWasGiven is the incident, made a test. Every
// value that used to come from the working directory has to come from the
// named config instead — the branch prefix, the base branch, and the directory
// the worktree lands in.
func TestNewActsOnTheRepositoryItWasGiven(t *testing.T) {
	f := newFixture(t, "")
	other := otherRepo(t, f.registry)

	f.mustRun("new", "--repo", "other", "eng-1")

	if !other.Exists("eng-1") {
		t.Errorf("no worktree at %s — new acted on the repository it was standing in", other.DirFor("eng-1"))
	}
	if f.Exists("eng-1") {
		t.Errorf("a worktree was made in %s, which was named nowhere", f.DirFor("eng-1"))
	}
	if got := other.Git(other.DirFor("eng-1"), "branch", "--show-current"); got != "y/eng-1" {
		t.Errorf("branch = %q, want the named repository's prefix", got)
	}
	// stdout stays the worktree path and nothing else, so `cd "$(tw new --repo
	// x y)"` works exactly as it does without the flag.
	if r := f.exec("new", "--repo", "other", "eng-2"); strings.TrimSpace(r.stdout) != other.DirFor("eng-2") {
		t.Errorf("stdout = %q, want the new worktree's path alone", r.stdout)
	}
}

// TestNewSaysWhichRepositoryItActedOn covers the half of the incident that had
// no output at all. The branch name and the base branch identify a repository
// only to a reader who already knows whose base branch that is, which the agent
// that got this wrong did not.
func TestNewSaysWhichRepositoryItActedOn(t *testing.T) {
	f := newFixture(t, "")
	otherRepo(t, f.registry)

	r := f.exec("new", "--repo", "other", "eng-1")
	if !strings.Contains(r.stderr, "off other's origin/main") {
		t.Errorf("stderr = %q, want the repository named beside the fork point", r.stderr)
	}
	// On stderr, not stdout: the answer is still the path and nothing else.
	if strings.Contains(r.stdout, "origin/main") {
		t.Errorf("stdout = %q, want the narration kept off it", r.stdout)
	}
}

// TestTheBranchPrefixStillOwnsTheSlash is why the repository is named by a flag
// and not by qualifying the target as <repo>/<slug>. A leading "bug/" means a
// branch prefix, in a repository named by --repo exactly as in one named by
// standing in it, and the two mechanisms never have to be told apart.
func TestTheBranchPrefixStillOwnsTheSlash(t *testing.T) {
	f := newFixture(t, "")
	other := gittest.New(t)
	addConfig(t, f.registry, "other", "main_dir = '"+other.MainDir+"'\n"+
		"base_branch = 'main'\nbranch_prefixes = ['feature/', 'bug/']\ncommand = 'sleep 300'\n")

	r := f.exec("new", "--repo", "other", "bug/eng-1")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if got := other.Git(other.DirFor("eng-1"), "branch", "--show-current"); got != "bug/eng-1" {
		t.Errorf("branch = %q, want the prefix read as a prefix", got)
	}
	// The slug alone still names the directory, prefix or no prefix, repository
	// named or not.
	if !other.Exists("eng-1") {
		t.Errorf("no worktree at %s — the prefix reached the directory", other.DirFor("eng-1"))
	}
}

// TestEveryWorktreeCommandTakesTheRepository is the consistency the flag is
// for. An agent that finds --repo rejected by one command has no reason to
// trust it on the next, so the test is that none of them rejects it.
func TestEveryWorktreeCommandTakesTheRepository(t *testing.T) {
	f := newFixture(t, "")
	other := otherRepo(t, f.registry)
	other.Worktree("eng-1")

	// Read-only or refusing invocations, chosen so the assertion is only ever
	// about the flag being understood: what must not come back is the parser
	// saying it does not know the flag.
	for _, args := range [][]string{
		{"ls", "--repo", "other"},
		{"config", "--repo", "other"},
		{"prune", "--repo", "other"},
		{"cd", "--repo", "other", "eng-1"},
		{"resume", "--repo", "other", "nosuchslug"},
		{"rm", "--repo", "other", "nosuchslug"},
		{"close", "--repo", "other", "nosuchslug"},
		{"send", "--repo", "other", "nosuchslug", "hello"},
		{"new", "--repo", "other", "eng-1"},
		{"attach", "--repo", "other"},
		{"restore", "--repo", "other", "-d"},
		{"refresh", "--repo", "other"},
	} {
		r := f.exec(args...)
		if r.err != nil && strings.Contains(r.err.Error(), "unknown flag") {
			t.Errorf("%s: %v", strings.Join(args, " "), r.err)
		}
	}
}

// TestNamingTheRepositoryTwiceIsRefused follows --prompt and --prompt-file:
// somebody who has said the same thing twice may have meant two different
// things by it, and a precedence rule is one more thing to know.
func TestNamingTheRepositoryTwiceIsRefused(t *testing.T) {
	f := newFixture(t, "")
	otherRepo(t, f.registry)

	for _, args := range [][]string{
		{"ls", "--repo", "other", "proj"},
		{"base", "--repo", "other", "proj"},
		{"config", "--repo", "proj", "proj"},
	} {
		r := f.exec(args...)
		if r.err == nil {
			t.Errorf("%s succeeded, want a usage error\n%s", strings.Join(args, " "), r.both())
			continue
		}
		// A usage error carries its message on stderr, the error itself being
		// only the exit code's carrier.
		if msg := flat(r.stderr); !strings.Contains(msg, "named twice") {
			t.Errorf("%s: stderr = %q, want it to say the repository was named twice",
				strings.Join(args, " "), msg)
		}
	}
}

// TestTheRepoPositionalStillWorks is the other half of that: the shorthand
// those commands have always taken is not removed by the flag arriving beside
// it. A config written for `tw ls proj` keeps working.
func TestTheRepoPositionalStillWorks(t *testing.T) {
	f := newFixture(t, "")
	other := otherRepo(t, f.registry)
	other.Worktree("eng-1")

	out := f.mustRun("ls", "other")
	if !strings.Contains(out, "eng-1") {
		t.Errorf("ls other = %q, want the other repository's worktree listed", out)
	}
}

// ---- the cleanup hints -----------------------------------------------------

// TestCleanupHintsNameTheRepository is what the operator in the incident did
// not have. A slug identifies nothing on its own once the same one exists in
// two repositories, and with the worktree already deleted there is nothing left
// to disambiguate it — which is how a `tmux kill-window` on a window index came
// to be the way out.
func TestCleanupHintsNameTheRepository(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	f.mustRun("new", "eng-1")

	// No tty in tests, so rm takes the nobody-to-ask path and prints the command
	// rather than asking — which is the path an agent takes too.
	r := f.exec("rm", "eng-1")
	if r.err != nil {
		t.Fatalf("rm: %v\n%s", r.err, r.both())
	}
	if want := "treewright close --repo proj eng-1"; !strings.Contains(flat(r.stderr), want) {
		t.Errorf("stderr = %q, want the hint to name %q", r.stderr, want)
	}
}

// ---- the window name -------------------------------------------------------

// TestNewWarnsWhenAWindowIsNamedAfterAnotherRepository is hygiene rather than a
// bug: treewright's own targeting is exact, so nothing misbehaves. The reader
// of a window list is what breaks — a session is one per repository, so a
// window called "other" sitting in proj's session says, to the only person who
// will ever read it, that this is other's work.
func TestNewWarnsWhenAWindowIsNamedAfterAnotherRepository(t *testing.T) {
	f := newFixture(t, "")
	otherRepo(t, f.registry)

	r := f.exec("new", "eng-1", "other")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "which is a registered repository") {
		t.Errorf("stderr = %q, want the collision named", r.stderr)
	}
	// A warning, not a refusal: the caller may have meant it, and a refusal would
	// need a --force, which is a flag people learn to pass by reflex.
	if !f.Exists("eng-1") {
		t.Error("the worktree was not created — the collision must warn, not refuse")
	}
}

// TestAWindowNamedAfterItsOwnRepositoryIsFine keeps the warning off the
// ordinary case. A warning that fires when nothing is wrong is one that stops
// being read, and a window named after the repository it is in misleads nobody.
func TestAWindowNamedAfterItsOwnRepositoryIsFine(t *testing.T) {
	f := newFixture(t, "")
	otherRepo(t, f.registry)

	for _, name := range []string{"proj", "eng-1"} {
		r := f.exec("new", "slug-"+name, name)
		if strings.Contains(r.stderr, "which is a registered repository") {
			t.Errorf("window name %q warned: %q", name, r.stderr)
		}
	}
}

// ---- whose client is it ----------------------------------------------------

// TestNamingAnotherRepositoryLeavesTheClientWhereItIs is the addendum's item,
// and the highest-value behavior here.
//
// treewright keeps one session per repository, so a window opened in another
// repository is always in another session — and Focus, which follows a window
// across sessions, therefore always switched. The operator watching one
// repository had their client moved into another's, under their hands.
//
// A headless test has no client to move, which is what makes the decision
// visible: an attempted switch fails and says "could not switch to session".
// Its absence is the proof that none was attempted.
func TestNamingAnotherRepositoryLeavesTheClientWhereItIs(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	otherRepo(t, f.registry)
	startSession(t, "proj", "main", f.MainDir)
	insideSession(t, "proj")

	r := f.exec("new", "--repo", "other", "eng-1")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "could not switch to session") {
		t.Errorf("stderr = %q, want no attempt to move the client out of proj", r.stderr)
	}
	if !strings.Contains(flat(r.stderr), "your client stayed where it is") {
		t.Errorf("stderr = %q, want it said that the client stayed", r.stderr)
	}
	// And the way over is named, because the reader may well have wanted to go.
	if !strings.Contains(flat(r.stderr), "treewright attach other") {
		t.Errorf("stderr = %q, want the way into the other session named", r.stderr)
	}
}

// TestTheWindowIsStillCurrentInItsOwnSession is the half that makes staying put
// harmless. tmux makes a new window current by itself, but a window merely
// found is not — so without the select, "it is ready over there" would land
// whoever attaches somewhere else entirely.
func TestTheWindowIsStillCurrentInItsOwnSession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	otherRepo(t, f.registry)
	startSession(t, "proj", "main", f.MainDir)

	// The other repository's session gains a base window first, so the worktree
	// window is not the only one there and being current means something.
	f.mustRun("base", "--repo", "other")
	insideSession(t, "proj")
	f.mustRun("new", "--repo", "other", "eng-1")

	current, err := tmuxctl(t, "list-windows", "-t", "=other",
		"-F", "#{window_name}", "-f", "#{window_active}")
	if err != nil {
		t.Fatalf("ask which window is current in other: %v\n%s", err, current)
	}
	if current != "eng-1" {
		t.Errorf("current window in other = %q, want the one just opened", current)
	}
}

// TestYourOwnRepositoryStillBringsYouToTheWindow is the behavior that must not
// regress. A person spawning work in the repository they are standing in asked
// for a window and wants to land in it, and treewright following a window into
// another session is the whole reason Focus exists.
//
// Standing in proj while attached to some other session is what makes the
// switch observable: the window goes to proj's session, which is not the
// caller's, so the client is moved — and fails to move, there being none.
func TestYourOwnRepositoryStillBringsYouToTheWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "elsewhere", "decoy", f.MainDir)
	insideSession(t, "elsewhere")

	r := f.exec("new", "eng-1")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(flat(r.stderr), "could not switch to session proj") {
		t.Errorf("stderr = %q, want the client moved into proj's session", r.stderr)
	}
}

// TestNamingYourOwnRepositoryDoesNotSuppressTheSwitch matters because the agent
// guide tells agents to pass --repo always. A flag that changed where you land
// depending on whether you bothered to name the repository you were already in
// would be one nobody could pass by default.
func TestNamingYourOwnRepositoryDoesNotSuppressTheSwitch(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "elsewhere", "decoy", f.MainDir)
	insideSession(t, "elsewhere")

	r := f.exec("new", "--repo", "proj", "eng-1")
	if r.err != nil {
		t.Fatalf("new: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(flat(r.stderr), "could not switch to session proj") {
		t.Errorf("stderr = %q, want naming your own repository to behave as not naming one", r.stderr)
	}
}

// TestResumeAcrossRepositoriesLeavesTheClientToo covers the addendum's "and
// anything else the addressing work makes reachable": the rule is the
// repository acted on, not the command that acted.
func TestResumeAcrossRepositoriesLeavesTheClientToo(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	other := otherRepo(t, f.registry)
	other.Worktree("eng-1")
	startSession(t, "proj", "main", f.MainDir)
	insideSession(t, "proj")

	r := f.exec("resume", "--repo", "other", "eng-1")
	if r.err != nil {
		t.Fatalf("resume: %v\n%s", r.err, r.both())
	}
	if strings.Contains(r.stderr, "could not switch to session") {
		t.Errorf("stderr = %q, want no attempt to move the client out of proj", r.stderr)
	}
}

// ---- reaching another repository's agent -----------------------------------

// TestSendReachesAnAgentInAnotherRepository is the cross-session handoff the
// addressing exists to enable: one agent typing at another that is running in a
// repository it cannot see.
func TestSendReachesAnAgentInAnotherRepository(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")
	marker := filepath.Join(t.TempDir(), "received")
	other := gittest.New(t)
	addConfig(t, f.registry, "other", "main_dir = '"+other.MainDir+"'\n"+
		"base_branch = 'main'\nbranch_prefix = 'y/'\ncommand = \"cat > "+marker+"\"\n")
	f.mustRun("new", "--repo", "other", "eng-1")

	const message = "the review is ready, take it from here"
	r := f.exec("send", "--repo", "other", "eng-1", message)
	if r.err != nil {
		t.Fatalf("send: %v\n%s", r.err, r.both())
	}
	waitForContent(t, marker, message+"\n", "the message")
	if !strings.Contains(flat(r.stderr), "repository other") {
		t.Errorf("stderr = %q, want the repository it reached named", r.stderr)
	}
}

// TestSendReachesAnotherRepositorysBaseWindow is the target that did not exist
// before: a repository's base checkout, addressed from outside it. The base
// window answers to "base" inside its own repository already; what is new is
// being able to say which repository's base is meant.
func TestSendReachesAnotherRepositorysBaseWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")
	marker := filepath.Join(t.TempDir(), "received")
	other := gittest.New(t)
	addConfig(t, f.registry, "other", "main_dir = '"+other.MainDir+"'\n"+
		"base_branch = 'main'\nbranch_prefix = 'y/'\ncommand = \"cat > "+marker+"\"\n")
	f.mustRun("base", "--repo", "other")

	const message = "start on the fetch timeouts when you are free"
	r := f.exec("send", "--repo", "other", "base", message)
	if r.err != nil {
		t.Fatalf("send: %v\n%s", r.err, r.both())
	}
	waitForContent(t, marker, message+"\n", "the message")
}

// TestSendShowsThePaneOfAnotherRepositoryBeforeTyping is the safety property
// that matters most across repositories and is easiest to lose there. An agent
// sitting on a question takes the next keystrokes as the answer to it, and the
// sender in another session cannot see the question at all — so the capture is
// not a convenience here, it is the only look anybody gets.
func TestSendShowsThePaneOfAnotherRepositoryBeforeTyping(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")
	other := gittest.New(t)
	addConfig(t, f.registry, "other", "main_dir = '"+other.MainDir+"'\n"+
		"base_branch = 'main'\nbranch_prefix = 'y/'\n"+
		"command = \"sh -c 'echo overwrite the config file? y/n; sleep 300'\"\n")
	f.mustRun("new", "--repo", "other", "eng-1")

	r := f.exec("send", "--repo", "other", "-n", "eng-1")
	if r.err != nil {
		t.Fatalf("send --dry-run: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "overwrite the config file?") {
		t.Errorf("stderr = %q, want the receiving pane shown", r.stderr)
	}
	if !strings.Contains(r.stderr, "in other shows") {
		t.Errorf("stderr = %q, want the repository named with the pane", r.stderr)
	}
}

// TestSendStillRefusesALineBreakAcrossRepositories keeps the refusals from
// being something the cross-repository path skipped. Enter is what submits, so
// everything after the first line posts as further turns — and across sessions
// nobody sees it happen.
func TestSendStillRefusesALineBreakAcrossRepositories(t *testing.T) {
	f := newFixture(t, "")
	otherRepo(t, f.registry)

	r := f.exec("send", "--repo", "other", "eng-1", "first line\nsecond line")
	if r.err == nil {
		t.Fatalf("send with a line break succeeded\n%s", r.both())
	}
	if !strings.Contains(r.err.Error(), "line break") {
		t.Errorf("error = %q, want the line break refused", r.err)
	}
}

// TestSendAcrossRepositoriesRefusesAHeldOpenWindow keeps the other refusal
// too. What reads the keyboard in a held-open window is a shell blocked on
// `read`: the message reaches nobody, and the Enter after it closes the window
// and erases the output that explains why there is no agent.
func TestSendAcrossRepositoriesRefusesAHeldOpenWindow(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")
	other := gittest.New(t)
	addConfig(t, f.registry, "other", "main_dir = '"+other.MainDir+"'\n"+
		"base_branch = 'main'\nbranch_prefix = 'y/'\n"+
		"command = 'echo no such model >&2; exit 12'\n")
	f.mustRun("new", "--repo", "other", "eng-1")
	waitForPane(t, windowIDNamed(t, "other", "eng-1"), heldOpenNotice)

	r := f.exec("send", "--repo", "other", "eng-1", "carry on")
	if r.err == nil {
		t.Fatalf("send to a held-open window succeeded\n%s", r.both())
	}
	if want := "treewright close --repo other eng-1"; !strings.Contains(r.err.Error(), want) {
		t.Errorf("error = %q, want the way out to name %q", r.err, want)
	}
}

// TestCloseFindsAWindowInAnotherRepository is close's own reason for existing,
// carried across repositories: the window outlives the worktree, and the record
// that finds it is the path stamped on the window rather than anything on disk.
func TestCloseFindsAWindowInAnotherRepository(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	other := otherRepo(t, f.registry)
	f.mustRun("new", "--repo", "other", "eng-1")

	// Removed first, so what close is asked to find is a window whose directory
	// has gone — the case the command is mostly for.
	f.mustRun("rm", "--repo", "other", "-y", "eng-1")
	if _, err := os.Stat(other.DirFor("eng-1")); err == nil {
		t.Fatalf("worktree %s still exists", other.DirFor("eng-1"))
	}
	if out, err := tmuxctl(t, "list-windows", "-t", "=other", "-F", "#{window_name}"); err == nil {
		if strings.Contains(out, "eng-1") {
			t.Errorf("windows in other = %q, want the removed worktree's window closed", out)
		}
	}
}

// ---- the discriminator itself ----------------------------------------------

// TestArrivalDependsOnWhoseRepositoryItIs pins the rule down without tmux in
// the way: the client moves when the repository acted on is the one the caller
// stands in, and stays when it is not.
func TestArrivalDependsOnWhoseRepositoryItIs(t *testing.T) {
	f := newFixture(t, "")
	otherRepo(t, f.registry)

	here, err := resolveConfig("proj")
	if err != nil {
		t.Fatalf("resolve proj: %v", err)
	}
	there, err := resolveConfig("other")
	if err != nil {
		t.Fatalf("resolve other: %v", err)
	}

	// The fixture leaves the caller standing in proj's main checkout.
	for _, c := range []struct {
		what  string
		got   arrival
		want  arrival
		named string
	}{
		{"no repository named", arrivalFor(here, ""), bringToFront, ""},
		{"your own repository named", arrivalFor(here, "proj"), bringToFront, "proj"},
		{"another repository named", arrivalFor(there, "other"), stayHere, "other"},
	} {
		if c.got != c.want {
			t.Errorf("%s: arrival = %v, want %v", c.what, c.got, c.want)
		}
	}
}

// TestStandingNowhereStillLeavesTheClient covers the terminal tab that launched
// in no repository at all. It is standing in nothing, so it is not standing in
// the repository being acted on either — and whatever session its client is
// watching is one nobody asked to leave.
func TestStandingNowhereStillLeavesTheClient(t *testing.T) {
	f := newFixture(t, "")
	otherRepo(t, f.registry)
	t.Chdir(t.TempDir())

	there, err := resolveConfig("other")
	if err != nil {
		t.Fatalf("resolve other: %v", err)
	}
	if got := arrivalFor(there, "other"); got != stayHere {
		t.Errorf("arrival = %v, want the client left where it is", got)
	}
}
