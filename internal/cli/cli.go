// Package cli parses treemux's arguments and runs the requested subcommand.
//
// Output follows one rule throughout: stdout carries the answer — the ls table,
// completion candidates, the shell integration script — and stderr carries
// everything else, so any command's output can be piped without progress lines
// or warnings contaminating it. Problems are prefixed "error: " or "warning: ",
// following git's convention; progress is unprefixed.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jay-snyder/treemux/internal/config"
)

// Sentinels main uses to choose an exit code. Both mean "already reported".
var (
	// ErrSilent exits 1 without printing: the failure has been reported, or
	// needs no message, as when the user cancels a picker.
	ErrSilent = errors.New("silent failure")

	// ErrUsage exits 2, the conventional code for being invoked wrongly, as
	// distinct from a command that ran and failed.
	ErrUsage = errors.New("usage error")
)

// usageError is a command invoked wrongly: a bad flag, a missing or extra
// argument. Run turns it into a message plus that command's help.
type usageError struct {
	command string // "" for an error about treemux itself
	message string
}

func (e *usageError) Error() string { return e.message }

func usageErrorf(command, format string, args ...any) error {
	return &usageError{command: command, message: fmt.Sprintf(format, args...)}
}

// Env is everything a subcommand needs from the outside world. Passing this in
// rather than reading globals is what makes the subcommands testable: a test can
// point the streams at buffers and assert on exactly what each one received.
type Env struct {
	Args    []string  // arguments after the program name
	Version string    // build version, for `treemux version`
	Stdout  io.Writer // the answer
	Stderr  io.Writer // progress, warnings, prompts

	// EvalFile is a path the shell integration wants shell commands appended
	// to, so treemux can affect the calling shell. From $TREEMUX_EVAL_FILE.
	EvalFile string
}

func (e *Env) defaults() {
	if e.Stdout == nil {
		e.Stdout = os.Stdout
	}
	if e.Stderr == nil {
		e.Stderr = os.Stderr
	}
	if e.EvalFile == "" {
		e.EvalFile = os.Getenv("TREEMUX_EVAL_FILE")
	}
}

// progressf reports what treemux is doing. Unprefixed, on stderr.
func (e *Env) progressf(format string, args ...any) {
	fmt.Fprintf(e.Stderr, format+"\n", args...)
}

// warnf reports something the user should know but that does not stop the
// command — a stale config entry, a branch that could not be deleted.
func (e *Env) warnf(format string, args ...any) {
	fmt.Fprintf(e.Stderr, "warning: "+format+"\n", args...)
}

// ---- command table ---------------------------------------------------------

type flagDoc struct {
	names string // "-f, --force"
	desc  string
}

type command struct {
	name    string
	aliases []string // accepted spellings, kept out of help so one name is canonical
	args    string   // argument spec shown in usage, e.g. "<slug> [window-name]"
	summary string   // one line, for the command list
	long    string   // optional detail paragraphs
	flags   []flagDoc
	hidden  bool // kept out of help and completion
	run     func(*Env, []string) error
}

// commands is ordered as it should read in help: first the four that get you
// into a stream of work, then inspection, then teardown, then installation.
//
// Several carry aliases for the name a newcomer is likelier to reach for.
// They are deliberately absent from help and completion: accepting a second
// spelling costs nothing, but listing it would present two names for one thing
// and leave the reader deciding which is real.
//
// Populated in init rather than as a var literal because the table names
// cmdComplete, which reads the table back to list a command's flags — written as
// one literal, Go rejects that as an initialization cycle.
var commands []*command

func init() {
	commands = []*command{
		{
			name:    "new",
			aliases: []string{"create"},
			args:    "<slug> [window-name]",
			summary: "create a worktree and branch, and open a tmux window in it",
			long: `Creates the worktree repo-<slug> on branch <prefix><slug>, copies in the
configured carry_files, runs post_create in the background, and opens a tmux
window running the configured command.

The branch always forks from origin/<base_branch>; there is deliberately no flag
to base it on anything else. When origin is unreachable, the local base branch is
used instead and that is reported.

When a branch of that name already exists — your own earlier work, or a colleague's
pull request you have fetched — it is checked out rather than recreated, so this is
also how you get a worktree onto an existing branch.

The window is named after a ticket key found in the slug, or the truncated slug,
unless [window-name] overrides it.`,
			run: cmdNew,
		},
		{
			name:    "resume",
			aliases: []string{"reopen"},
			args:    "[slug]",
			summary: "reopen a window on an existing worktree",
			long: `Opens a tmux window running the configured resume_command in the
worktree, or switches to the window already open there.

With no slug, a lone worktree is chosen automatically and otherwise a menu is
shown. Naming a slug skips the menu, and an unambiguous prefix of one is enough.`,
			run: cmdResume,
		},
		{
			name:    "cd",
			args:    "[slug]",
			summary: "move your shell into a worktree",
			long: `Changes the calling shell's directory to a worktree, choosing from a
menu when no slug is given. An unambiguous prefix of a slug is enough.

The path is also printed, so this works without the shell integration as
cd "$(treemux cd <slug>)" — but with the integration loaded, treemux moves your
shell for you.`,
			run: cmdCd,
		},
		{
			name:    "base",
			aliases: []string{"main", "home"},
			args:    "[repo]",
			summary: "open a window on the main checkout",
			long: `Opens the persistent base window in the main checkout, for launching
worktrees and asking general questions rather than for feature work. Warns when
the checkout has drifted off the base branch.`,
			run: cmdBase,
		},
		{
			name:    "ls",
			aliases: []string{"list", "status"},
			args:    "[--json] [repo]",
			summary: "list worktrees with their status",
			long: `Prints one row per worktree: slug, status, divergence from
origin/<base_branch>, and whether a tmux window is open in it. The worktree you
are standing in is marked with an asterisk.

Changes no working tree, branch, or ref, though detecting a squash merge writes a
dangling object to the object database. It also does not fetch, so a branch that
landed since your last fetch still reads as active; rm and prune fetch before they
judge, and so can disagree with a stale listing.`,
			flags: []flagDoc{
				{"--json", "print machine-readable output instead of a table"},
			},
			run: cmdLs,
		},
		{
			name:    "rm",
			aliases: []string{"remove", "delete"},
			args:    "[-f] [-y] <slug>",
			summary: "tear down a worktree and its branch",
			long: `Removes the worktree, deletes the local branch, and prunes the stale
remote-tracking ref.

Refuses when the branch holds work that exists nowhere else — uncommitted changes
or commits on no origin ref — unless --force is given. Work that reached origin
only as a squash merge is recognized as landed and does not trigger the refusal.

An unambiguous prefix of a slug is enough to name it.

The tmux window open on that worktree is now pointing at a directory that no
longer exists, so treemux offers to close it — the window named after the stream,
not whichever one you happened to run this from.`,
			flags: []flagDoc{
				{"-f, --force", "remove even when unsaved work would be lost"},
				{"-y, --yes", "close the worktree's tmux window without asking"},
			},
			run: cmdRm,
		},
		{
			name:    "prune",
			args:    "[-y] [repo]",
			summary: "remove every merged, clean worktree",
			long: `Lists the worktrees whose branch has landed in origin/<base_branch> and
whose tree is clean. Nothing is removed until --yes is given.

A pushed but unmerged branch is never a target: prune reaps landed work, not open
pull requests.

Closing the tmux window left open on each removed worktree is asked about
separately: --yes answers for the worktrees, not for windows that may still have
something running in them.`,
			flags: []flagDoc{
				{"-y, --yes", "actually remove them, instead of listing"},
			},
			run: cmdPrune,
		},
		{
			name:    "setup",
			args:    "[-n] [name]",
			summary: "write a config for the repository you are standing in",
			long: `Registers the current repository by writing a config file for it,
filling in what can be detected: the main checkout, the base branch from
origin/HEAD, a branch prefix from your git email, and any gitignored env files
worth carrying into new worktrees.

Everything it guesses is reported and written as editable TOML — the file is the
record, not this command. It refuses to overwrite an existing config.

The name defaults to the repository's directory name, and is what you pass to
commands that take a [repo].`,
			flags: []flagDoc{
				{"-n, --dry-run", "print the config instead of writing it"},
			},
			run: cmdSetup,
		},
		{
			name:    "config",
			args:    "[repo]",
			summary: "print the settings in force, defaults included",
			long: `Prints the configuration this repository is actually running under:
every setting with its value, defaults filled in, paths expanded, and the file it
was read from.

What a config file leaves out is where confusion lives — this is how you find out
which base branch a command would really fork from.`,
			run: cmdConfig,
		},
		{
			name:    "doctor",
			summary: "check the installation and every registered config",
			long: `Verifies the parts that have to line up for treemux to work: tmux
installed, the shell integration loaded, the registry readable, and for each
config its main checkout, origin remote, base branch, carry_files and command.

Exits non-zero when a check fails, so it can gate a setup script. Warnings — a
missing carry file, no tmux server running yet — do not fail the run.`,
			run: cmdDoctor,
		},
		{
			// Named for what it does to a shell, leaving "init" to read as
			// "initialize this repository" — which is what a newcomer expects it to
			// mean, and is what setup does.
			name:    "shell-init",
			args:    "<shell>",
			summary: "print the shell integration for zsh, bash, or fish",
			long: `Prints a snippet to load from your shell's startup file:

    eval "$(treemux shell-init zsh)"     # or bash
    treemux shell-init fish | source

It defines a wrapper function, so that cd can move your shell and rm can move it
out of a directory it just deleted, and registers tab completion.`,
			run: cmdInit,
		},
		{
			name:    "__complete",
			args:    "<slugs|repos|shells|flags [command]>",
			summary: "list completion candidates",
			hidden:  true,
			run:     cmdComplete,
		},
	}
}

// lookup finds a command by its canonical name or any of its aliases.
func lookup(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
		for _, alias := range c.aliases {
			if alias == name {
				return c
			}
		}
	}
	return nil
}

// suggest names the command a mistyped word most likely meant, or "" when
// nothing is close enough to be worth guessing at.
//
// Aliases are searched alongside canonical names but resolve to the canonical
// one, so a user who types "remov" is told about rm rather than about a name
// help does not list.
func suggest(name string) string {
	best, bestDist := "", 0
	for _, c := range commands {
		if c.hidden {
			continue
		}
		for _, candidate := range append([]string{c.name}, c.aliases...) {
			// A prefix of a command is nearly always that command — "wor" for
			// worktree — and beats any edit-distance reading of it.
			if len(name) >= 2 && strings.HasPrefix(candidate, name) {
				return c.name
			}
			// Otherwise allow one edit per three characters, so short names are
			// not matched by anything vaguely similar: "ls" tolerates no typo,
			// "resume" tolerates two.
			budget := 1 + len(candidate)/3
			if d := editDistance(name, candidate); d <= budget && (best == "" || d < bestDist) {
				best, bestDist = c.name, d
			}
		}
	}
	return best
}

// editDistance is the Levenshtein distance between two words: how many single
// character insertions, deletions or substitutions separate them.
//
// Only one row of the matrix is kept, since each cell depends solely on the row
// above and the cell to its left.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// unknownCommand reports a name that is not a command, pointing at the nearest
// one when there is a plausible candidate.
func unknownCommand(env *Env, name string) error {
	if did := suggest(name); did != "" {
		fmt.Fprintf(env.Stderr, "error: unknown command %q — did you mean %q?\n\n", name, did)
	} else {
		fmt.Fprintf(env.Stderr, "error: unknown command %q\n\n", name)
	}
	writeOverview(env.Stderr)
	return ErrUsage
}

// ---- dispatch --------------------------------------------------------------

// Run dispatches to a subcommand, returning an error rather than exiting so that
// main owns the exit code and tests can call it directly.
func Run(env Env) error {
	env.defaults()

	if len(env.Args) == 0 {
		// Invoked with nothing to do: help belongs on stderr here, because this
		// is an error, and exit 2 says so.
		writeOverview(env.Stderr)
		return ErrUsage
	}

	name, rest := env.Args[0], env.Args[1:]

	switch name {
	case "-h", "--help", "help":
		// Asked for deliberately, so it is the answer: stdout, exit 0.
		if len(rest) > 0 {
			target := lookup(rest[0])
			if target == nil || target.hidden {
				return unknownCommand(&env, rest[0])
			}
			writeCommandHelp(env.Stdout, target)
			return nil
		}
		writeOverview(env.Stdout)
		return nil
	case "-v", "--version", "version":
		fmt.Fprintf(env.Stdout, "treemux %s\n", env.Version)
		return nil
	}

	cmd := lookup(name)
	if cmd == nil {
		return unknownCommand(&env, name)
	}

	// -h on a subcommand is a request for its help, not a flag it must accept.
	for _, a := range rest {
		if a == "-h" || a == "--help" {
			writeCommandHelp(env.Stdout, cmd)
			return nil
		}
	}

	err := cmd.run(&env, rest)
	var ue *usageError
	if errors.As(err, &ue) {
		// A wrong invocation is worth showing the right one for.
		fmt.Fprintf(env.Stderr, "error: %s\n\n", ue.message)
		if target := lookup(ue.command); target != nil {
			writeCommandHelp(env.Stderr, target)
		} else {
			writeOverview(env.Stderr)
		}
		return ErrUsage
	}
	return err
}

// ---- help ------------------------------------------------------------------

const tagline = "treemux - isolated git worktree, tmux, and agent sessions for parallel work"

func writeOverview(w io.Writer) {
	fmt.Fprintf(w, "%s\n\nusage: treemux <command> [arguments]\n\ncommands:\n", tagline)

	width := 0
	for _, c := range commands {
		if c.hidden {
			continue
		}
		if n := len(c.name + " " + c.args); n > width {
			width = n
		}
	}
	for _, c := range commands {
		if c.hidden {
			continue
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, strings.TrimSpace(c.name+" "+c.args), c.summary)
	}

	fmt.Fprintf(w, "\nrun \"treemux help <command>\" for detail on one command,\n")
	fmt.Fprintf(w, "or \"treemux setup\" inside a repository to register it.\n")
	fmt.Fprintf(w, "\nconfig: %s/<name>.toml\n", config.Dir())
}

func writeCommandHelp(w io.Writer, c *command) {
	fmt.Fprintf(w, "usage: treemux %s\n", strings.TrimSpace(c.name+" "+c.args))
	fmt.Fprintf(w, "\n%s\n", c.summary)
	if c.long != "" {
		fmt.Fprintf(w, "\n%s\n", c.long)
	}
	if len(c.flags) > 0 {
		width := 0
		for _, f := range c.flags {
			if len(f.names) > width {
				width = len(f.names)
			}
		}
		fmt.Fprint(w, "\nflags:\n")
		for _, f := range c.flags {
			fmt.Fprintf(w, "  %-*s  %s\n", width, f.names, f.desc)
		}
	}
}
