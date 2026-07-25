package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/jay-snyder/treemux/internal/shellinit"
)

// ---- cd --------------------------------------------------------------------

func TestCdMovesTheCallingShell(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "feature")

	commands, _ := f.runWithEvalFile("cd", "feature")
	want := "cd '" + f.DirFor("feature") + "'"
	if strings.TrimSpace(commands) != want {
		t.Errorf("shell was asked to run %q, want %q", commands, want)
	}
}

// TestCdWorksWithoutTheIntegration covers the plain-binary case: the path is the
// command's answer, so `cd "$(treemux cd x)"` substitutes correctly, and the user
// is told why nothing moved on its own.
func TestCdWorksWithoutTheIntegration(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "feature")

	r := f.exec("cd", "feature")
	if r.err != nil {
		t.Fatalf("cd: %v\n%s", r.err, r.both())
	}
	if got, want := r.stdout, f.DirFor("feature")+"\n"; got != want {
		t.Errorf("stdout = %q, want exactly %q", got, want)
	}
	if !strings.Contains(r.stderr, "no shell integration loaded") {
		t.Errorf("stderr = %q, want it to say why the shell did not move", r.stderr)
	}
}

func TestCdWithNoWorktrees(t *testing.T) {
	f := newFixture(t, "")

	_, err := f.run("cd")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "treemux new") {
		t.Errorf("err = %v, want it to point at the command that makes one", err)
	}
}

// TestCdTakesTheOnlyWorktree checks that a single candidate is not put to a menu:
// with one worktree there is nothing to choose, and a prompt there would be a
// keystroke with one possible answer.
func TestCdTakesTheOnlyWorktree(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "solo")

	r := f.exec("cd")
	if r.err != nil {
		t.Fatalf("cd: %v\n%s", r.err, r.both())
	}
	if got, want := strings.TrimSpace(r.stdout), f.DirFor("solo"); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// ---- slug resolution -------------------------------------------------------

// TestAnUnambiguousPrefixNamesAWorktree covers how people actually refer to their
// work: slugs carry a ticket key and a description, and the key is what gets
// typed.
func TestAnUnambiguousPrefixNamesAWorktree(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "eng-1646-app-landing-page-redesign")

	for _, cmd := range []string{"cd", "resume", "rm"} {
		t.Run(cmd, func(t *testing.T) {
			r := f.exec(cmd, "eng-1646")
			if r.err != nil {
				t.Fatalf("%s: %v\n%s", cmd, r.err, r.both())
			}
			// The expansion is reported rather than applied silently: rm is on
			// this list, and it deletes what it resolved to.
			if !strings.Contains(r.stderr, "eng-1646 matches worktree eng-1646-app-landing-page-redesign") {
				t.Errorf("stderr = %q, want the expansion reported", r.stderr)
			}
			// cd and rm answer with a path, so the path they resolved to is part
			// of the answer. resume answers with a window, and names it instead.
			if cmd != "resume" && !strings.Contains(r.both(), f.DirFor("eng-1646-app-landing-page-redesign")) {
				t.Errorf("output = %q, want the resolved worktree", r.both())
			}
		})
		if cmd == "rm" {
			break // it removed the worktree the other cases depend on
		}
	}
}

func TestAnExactSlugBeatsAPrefix(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-10")

	// "eng-1" is both an exact slug and a prefix of another. Preferring the exact
	// match is what keeps a slug from becoming unreachable by its own name.
	r := f.exec("cd", "eng-1")
	if r.err != nil {
		t.Fatalf("cd: %v\n%s", r.err, r.both())
	}
	if got, want := strings.TrimSpace(r.stdout), f.DirFor("eng-1"); got != want {
		t.Errorf("stdout = %q, want the exact match %q", got, want)
	}
	if strings.Contains(r.stderr, "matches worktree") {
		t.Errorf("stderr = %q, want no expansion reported for an exact match", r.stderr)
	}
}

func TestAnAmbiguousPrefixIsRefused(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "eng-18-a")
	f.mustRun("new", "eng-18-b")

	_, err := f.run("rm", "eng-18")
	if err == nil {
		t.Fatal("want an error rather than a guess")
	}
	// Both candidates must be named: the user has to know what to type instead.
	for _, want := range []string{"eng-18-a", "eng-18-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to list %q", err, want)
		}
	}
	if !f.Exists("eng-18-a") || !f.Exists("eng-18-b") {
		t.Error("an ambiguous prefix removed something")
	}
}

// TestAnUnknownSlugNamesTheAlternatives keeps git's own complaint about a path the
// user never typed from surfacing.
func TestAnUnknownSlugNamesTheAlternatives(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "alpha")

	for _, cmd := range []string{"rm", "resume", "cd"} {
		t.Run(cmd, func(t *testing.T) {
			_, err := f.run(cmd, "nope")
			if err == nil {
				t.Fatal("want an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, `no worktree "nope"`) {
				t.Errorf("err = %v, want it to name what was not found", err)
			}
			if !strings.Contains(msg, "alpha") {
				t.Errorf("err = %v, want it to list what does exist", err)
			}
			if strings.Contains(msg, "is not a working tree") {
				t.Errorf("err = %v, git's own message leaked", err)
			}
		})
	}
}

// ---- new's guards ----------------------------------------------------------

// TestNewOnAnExistingWorktreePointsAtResume replaces a git failure that arrived
// after treemux had already announced what it was doing.
func TestNewOnAnExistingWorktreePointsAtResume(t *testing.T) {
	f := newFixture(t, "")
	f.mustRun("new", "feature")

	r := f.exec("new", "feature")
	if r.err == nil {
		t.Fatal("want an error")
	}
	msg := r.err.Error()
	if !strings.Contains(msg, "already exists") || !strings.Contains(msg, "treemux resume feature") {
		t.Errorf("err = %v, want it to name the command that opens the existing worktree", r.err)
	}
	// The failure has to precede the narration, or the output reads as though the
	// work was done and then undone.
	if strings.Contains(r.stderr, "reusing existing branch") {
		t.Errorf("stderr = %q, want no progress reported before the refusal", r.stderr)
	}
}

func TestNewRejectsSlugsGitWouldRefuse(t *testing.T) {
	f := newFixture(t, "")

	// Each of these reaches git as part of a branch name or a directory name, and
	// each produced several lines of git ref-format advice before this check.
	// A leading dash is not here: the flag parser rejects it before this check,
	// and reaches it only by the route below.
	for _, slug := range []string{
		"feature thing", "a\tb", "wip^2", "a..b", "x~1", "q?", "s*", "l[1]",
		"back\\slash", ".hidden", "trailing.", "v1.lock", "at@{1}", "a/b",
	} {
		t.Run(slug, func(t *testing.T) {
			r := f.exec("new", slug)
			if r.err == nil {
				t.Fatalf("slug %q was accepted", slug)
			}
			if !errors.Is(r.err, ErrUsage) {
				t.Errorf("err = %v, want ErrUsage (exit 2)", r.err)
			}
			if !strings.Contains(r.stderr, "error: slug ") {
				t.Errorf("stderr = %q, want a message naming the slug", r.stderr)
			}
			if strings.Contains(r.stderr, "check-ref-format") {
				t.Errorf("stderr = %q, git's advice leaked", r.stderr)
			}
		})
	}

	// Stripping the branch prefix can uncover a leading dash that the flag parser
	// never saw, which is the one way this rule is reached.
	t.Run("dash after the prefix is stripped", func(t *testing.T) {
		r := f.exec("new", "x/-dash")
		if r.err == nil {
			t.Fatal("want an error")
		}
		if !strings.Contains(r.stderr, "would read as a flag") {
			t.Errorf("stderr = %q, want the leading dash explained", r.stderr)
		}
	})

	// And the ordinary shapes still pass, so the rules above have not quietly
	// outlawed a normal slug.
	for _, slug := range []string{"eng-1646", "eng-1646-white-screen", "v1.2", "under_score", "UPPER"} {
		t.Run("allows "+slug, func(t *testing.T) {
			if _, err := f.run("new", slug); err != nil {
				t.Errorf("slug %q was rejected: %v", slug, err)
			}
		})
	}
}

// ---- names -----------------------------------------------------------------

func TestAliasesReachTheirCommand(t *testing.T) {
	f := newFixture(t, "")

	for alias, want := range map[string]string{
		"list":   "ls",
		"status": "ls",
		"remove": "rm",
		"delete": "rm",
		"create": "new",
		"main":   "base",
		"home":   "base",
		"reopen": "resume",
	} {
		t.Run(alias, func(t *testing.T) {
			if got := lookup(alias); got == nil || got.name != want {
				t.Fatalf("lookup(%q) = %v, want the %s command", alias, got, want)
			}
			// Help for an alias is help for the real command, so the reader learns
			// the name the documentation uses.
			r := f.exec("help", alias)
			if r.err != nil {
				t.Fatalf("help %s: %v", alias, r.err)
			}
			if !strings.Contains(r.stdout, "usage: treemux "+want) {
				t.Errorf("help %s = %q, want the usage for %s", alias, r.stdout, want)
			}
		})
	}
}

// TestAliasesStayOutOfHelp keeps one name canonical: listing both spellings would
// leave the reader deciding which is real.
func TestAliasesStayOutOfHelp(t *testing.T) {
	f := newFixture(t, "")
	overview := f.exec("help").stdout

	for _, c := range commands {
		for _, alias := range c.aliases {
			for _, line := range strings.Split(overview, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), alias+" ") {
					t.Errorf("help lists the alias %q as though it were a command", alias)
				}
			}
		}
	}
}

// TestShellInitIsSilentOnStdout pins the property a startup file depends on: the
// script is the whole of stdout, and nothing is written to stderr, since anything
// there would print in every new terminal.
func TestShellInitIsSilentOnStdout(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("shell-init", "zsh")
	if r.err != nil {
		t.Fatalf("shell-init zsh: %v\n%s", r.err, r.both())
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want silence in a shell startup file", r.stderr)
	}
	if !strings.HasPrefix(r.stdout, "# treemux shell integration for zsh") {
		t.Errorf("stdout = %q, want the zsh script", r.stdout)
	}

	// "init" is not accepted: it is reserved for reading as "initialize this
	// repository", so it must fail rather than quietly meaning something else.
	if old := f.exec("init", "zsh"); old.err == nil {
		t.Error("init still runs, want it rejected as an unknown command")
	}
}

func TestUnknownCommandSuggestsTheNearest(t *testing.T) {
	f := newFixture(t, "")

	for typo, want := range map[string]string{
		"nwe":    "new",
		"remov":  "rm", // via the alias, but named canonically
		"lst":    "ls",
		"resmue": "resume",
		"doctro": "doctor",
		"setpu":  "setup",
	} {
		t.Run(typo, func(t *testing.T) {
			r := f.exec(typo)
			if !errors.Is(r.err, ErrUsage) {
				t.Fatalf("err = %v, want ErrUsage", r.err)
			}
			if !strings.Contains(r.stderr, `did you mean "`+want+`"`) {
				t.Errorf("stderr = %q, want a suggestion of %q", r.stderr, want)
			}
		})
	}

	// Nothing close enough must not produce a wild guess: a wrong suggestion sends
	// the reader off to try it.
	r := f.exec("zzzzzz")
	if strings.Contains(r.stderr, "did you mean") {
		t.Errorf("stderr = %q, want no suggestion for an unrecognizable word", r.stderr)
	}
}

// TestShellScriptsKnowEveryCommand catches the drift the shims invite: each one
// hardcodes a list of subcommand names, so a command added to the table can go
// missing from completion in three places at once.
func TestShellScriptsKnowEveryCommand(t *testing.T) {
	for _, shell := range shellinit.Shells() {
		script, err := shellinit.Script(shell)
		if err != nil {
			t.Fatalf("script %s: %v", shell, err)
		}
		for _, c := range commands {
			if c.hidden {
				continue
			}
			if !strings.Contains(script, c.name) {
				t.Errorf("the %s integration never mentions %q", shell, c.name)
			}
		}
	}
}
