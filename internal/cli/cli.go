// Package cli parses treewright's arguments and runs the requested subcommand.
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
	"slices"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
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
	command string // "" for an error about treewright itself
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
	Argv0   string    // the name the binary was invoked by: "treewright", or "tw"
	Version string    // build version, for `treewright version`
	Stdout  io.Writer // the answer
	Stderr  io.Writer // progress, warnings, prompts

	// EvalFile is a path the shell integration wants shell commands appended
	// to, so treewright can affect the calling shell. From $TREEWRIGHT_EVAL_FILE.
	EvalFile string
}

func (e *Env) defaults() {
	if e.Argv0 == "" {
		e.Argv0 = "treewright"
	}
	if e.Stdout == nil {
		e.Stdout = os.Stdout
	}
	if e.Stderr == nil {
		e.Stderr = os.Stderr
	}
	if e.EvalFile == "" {
		e.EvalFile = os.Getenv("TREEWRIGHT_EVAL_FILE")
	}
}

// progressf reports what treewright is doing. Unprefixed, on stderr.
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

// argRepo is how an optional repo name is spelled in a usage line. Named once
// because it appears in three commands' specs and in the help those render: the
// commands that take a repo all take it the same way, and a fourth added later
// should read the same as the three.
const argRepo = "[repo]"

// commands is ordered as it should read in help: first the four that get you
// into a worktree, then inspection, then teardown, then installation.
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
			args:    "[-p <text>] <slug> [window-name]",
			summary: "create a worktree and branch, and open a tmux window in it",
			long: `Creates the worktree repo-<slug> on branch <prefix><slug>, copies in the
configured carry_files, runs post_create in the background, and opens a tmux
window running the configured command.

A repo whose branch_prefixes lists several — "feature/", "bug/", "chore/" — picks
between them by naming one: "new bug/eng-1" branches bug/eng-1, while the worktree
stays repo-eng-1 and the slug stays eng-1. A bare slug gets the first in the list,
and a prefix that is not in it is refused rather than guessed at.

The window goes in this repository's own tmux session — named after its config —
which is created if it is not running yet, so one repository's windows never mix
with another's. Outside tmux the window is still created, detached, and treewright
prints the command to attach with.

The branch always forks from origin/<base_branch>; there is deliberately no flag
to base it on anything else. When origin is unreachable, the local base branch is
used instead and that is reported.

When a branch of that name already exists — your own earlier work, or a colleague's
pull request you have fetched — it is checked out rather than recreated, so this is
also how you get a worktree onto an existing branch.

The window is named after a ticket key found in the slug, or the truncated slug,
unless [window-name] overrides it.

--prompt hands the agent its first instruction, so the window opens already
working rather than waiting to be told what the ticket is. The text lands where
the command template says with {prompt} — the default is "claude {prompt}" —
shell-quoted as one argument. Without the flag the placeholder simply
disappears; with the flag and no placeholder to take it, the error says where
to write one.`,
			flags: []flagDoc{
				{"-p, --prompt", "text the agent starts working on, placed at the command's {prompt}"},
			},
			run: cmdNew,
		},
		{
			name:    "resume",
			aliases: []string{"reopen"},
			args:    "[-p <text>] [slug]",
			summary: "reopen a window on an existing worktree",
			long: `Opens a tmux window running the configured resume_command in the
worktree, or switches to the window already open there — following it into
another session if that is where it turns out to be.

With no slug a menu is shown. Naming a slug skips it, and an unambiguous prefix
of one is enough.

The base checkout heads that menu, so the window you return to between worktrees
is reachable from the same key as the rest — and after a reboot, which a checkout
on disk survives and a tmux session does not, it is reopened along with them. Name
it "base" or name the branch it is parked on. It runs resume_command like every
other row; "treewright base" is the way in that opens it fresh.

--prompt hands the resumed agent its next instruction, at resume_command's
{prompt} placeholder. It only reaches an agent the resume actually starts: a
window that was already open is switched to as usual, with a warning that the
prompt went undelivered and is worth pasting there.`,
			flags: []flagDoc{
				{"-p, --prompt", "text for the resumed agent, placed at resume_command's {prompt}"},
			},
			run: cmdResume,
		},
		{
			name:    "cd",
			args:    "[slug]",
			summary: "move your shell into a worktree",
			long: `Changes the calling shell's directory to a worktree, choosing from a
menu when no slug is given. An unambiguous prefix of a slug is enough, and "base"
moves you to the main checkout.

The path is also printed, so this works without the shell integration as
cd "$(treewright cd <slug>)" — but with the integration loaded, treewright moves your
shell for you.`,
			run: cmdCd,
		},
		{
			name:    "base",
			aliases: []string{"main", "home"},
			args:    argRepo,
			summary: "open a window on the main checkout",
			long: `Opens the persistent base window in the main checkout, for launching
worktrees and asking general questions rather than for feature work. Warns when
the checkout has drifted off the base branch.

It is the same window every time: one already sitting in the main checkout is
selected rather than a second one being opened beside it. Being the repository's
session's first window, it is also what keeps that session alive as worktrees come
and go.

"treewright resume" reaches the same window, since the base checkout is a row of its
menu. The difference is only ever visible on the first open of the day: this runs
command, for a general-purpose window, where resume runs resume_command.`,
			run: cmdBase,
		},
		{
			name:    "attach",
			args:    argRepo,
			summary: "attach this terminal to a repository's tmux session",
			long: `Puts you in the session holding a repository's windows, on whichever
window was current there when you left — which is what makes this different from
resume, that being a request for one particular worktree.

Inside tmux the client is moved instead, since attaching a second client to a
session the first one is already in is the nesting tmux warns about.

The session has to exist. "treewright base" is what opens a repository's first
window, and so what brings its session into being.`,
			run: cmdAttach,
		},
		{
			name:    "popup",
			args:    "[-c <client>] [-d <dir>] <command> [arguments]",
			summary: "run a treewright command in a tmux popup sized to its output",
			long: `Opens a tmux popup and runs "treewright <command>" inside it, having
first worked out how big that popup needs to be.

tmux sizes a popup when it is created and offers no way to fit one afterwards:
-w and -h take cells or a percentage of the terminal. A percentage is the wrong
unit for a picker, whose height is the number of worktrees and whose width is the
widest slug — neither of which grows when the terminal does, so on a wide
terminal most of the popup is empty. This is what the key bindings printed by
"treewright tmux-init" go through.

--client names the terminal to draw on. It matters because a tmux command run
from outside tmux has no association with the client that asked for it, so with
two terminals attached to two sessions the popup opens over whichever has been
busier. A binding passes #{client_tty}, which run-shell expands.

--dir names the directory the popup is for, which decides both the repository
treewright answers about and the worktree it marks as the one you are in. It is
needed because run-shell does not run in the calling pane's directory: it runs in
the tmux server's, wherever that was started. A binding passes
#{pane_current_path}, expanded the same way.`,
			flags: []flagDoc{
				{"-c, --client", "terminal to open the popup on (a tmux client, e.g. #{client_tty})"},
				{"-d, --dir", "directory the popup is for (a pane's path, e.g. #{pane_current_path})"},
			},
			run: cmdPopup,
		},
		{
			name:    "signal",
			args:    "<state>",
			summary: "record the state of the agent running in this worktree",
			long: `Stamps the tmux window belonging to the checkout you are standing in
with an agent state — working, waiting, done, or clear — as the window option
@treewright_agent_state. The state shows in the AGENT column of "treewright ls"
and as agent_state in its JSON, and "waiting" also puts a marker on the window's
name (!ENG-142), so the one window that needs a person shows in any status line
with nothing added to tmux.conf.

This is for an agent's own hooks to run rather than for typing: a hook that
fires when the agent starts work, blocks on you, or finishes runs
"treewright signal" with the matching state, and the ls table answers which of
your worktrees wants attention. The state lives on the window and dies with it,
so a closed window never leaves a stale claim behind.

Anywhere out of scope — outside tmux, outside a registered repository, in a
checkout with no window — it exits 0 and prints nothing. Hooks fire in every
session the agent runs, and most of those are none of treewright's business; a
hook that complains about that would nag from every plain terminal.`,
			run: cmdSignal,
		},
		{
			name:    "ls",
			aliases: []string{"list", "status"},
			args:    "[--json] " + argRepo,
			summary: "list worktrees with their status",
			long: `Prints one row per worktree: slug, status, divergence from
origin/<base_branch>, and the tmux window open in it, by name. A window that is
not in this repository's session is shown as session:window, since that is why
resuming it would move you somewhere unexpected. The worktree you are standing in
is marked with an asterisk.

The base checkout heads the listing, as it heads the resume menu, under the branch
it is parked on. Its status is "base" rather than one of the removable ones, and
its divergence is how far your main checkout has drifted from origin — whether
what you are reading there is stale. Nothing is printed at all until there is at
least one worktree.

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
longer exists, so treewright offers to close it — the window named after the worktree,
not whichever one you happened to run this from.`,
			flags: []flagDoc{
				{"-f, --force", "remove even when unsaved work would be lost"},
				{"-y, --yes", "close the worktree's tmux window without asking"},
			},
			run: cmdRm,
		},
		{
			name:    "prune",
			args:    "[-y] " + argRepo,
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
			args:    argRepo,
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
			long: `Verifies the parts that have to line up for treewright to work: tmux
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

    eval "$(treewright shell-init zsh)"     # or bash
    treewright shell-init fish | source

It defines a wrapper function, so that cd can move your shell and rm can move it
out of a directory it just deleted, and registers tab completion.`,
			run: cmdInit,
		},
		{
			name:    "tmux-init",
			args:    "[--apply] [--resume-key <key>] [--new-key <key>]",
			summary: "print the tmux integration: popup key bindings and titles",
			long: `Prints tmux configuration to load from your tmux.conf:

    run-shell 'treewright tmux-init --apply'

    treewright tmux-init > ~/.config/treewright/treewright.tmux
    source-file ~/.config/treewright/treewright.tmux

It binds prefix + T to switch worktrees and prefix + N to start one, both by
opening treewright in a popup — which is the only way to reach it from a window
whose pane is the agent itself and has no shell to type into. It also turns
terminal titles on, and documents the window options treewright records for a
status line to read.

Both keys are free in stock tmux, and their lowercase twins are harmless if you
miss the shift: t is clock-mode and n is next-window. Where that is not true of
your own config, move them:

    treewright tmux-init --resume-key G --new-key C-n

An empty key omits its binding, so --new-key "" binds only the picker. Anything
more than the keys — the popup size, a binding of your own — is what printing to
a file is for.

--apply loads the same text into the running server, flags included, for the
one-line form above; printing is the default, since these are key bindings and
worth reading first.`,
			flags: []flagDoc{
				{"--apply", "load it into the running tmux server instead of printing it"},
				{"--resume-key", "prefix key that switches worktrees (default T, empty to omit)"},
				{"--new-key", "prefix key that starts a worktree (default N, empty to omit)"},
			},
			run: cmdTmuxInit,
		},
		{
			name:    "agent-init",
			args:    "[--skill] <agent>",
			summary: "print the hooks that make an agent report its state",
			long: `Prints an agent's hook configuration: its own hooks wired to
"treewright signal", so the window it runs in says whether it is working,
waiting on you, or done — the AGENT column of "treewright ls", and a marker on
the window's name when it needs a person.

The fragment goes to stdout by itself, so it can be piped; where to put it is
said alongside, on stderr. For claude that is ~/.claude/settings.json, and the
hooks are safe to keep global there: outside a treewright window, signal does
nothing, quietly.

To scope the hooks to one repository instead, put them in the main checkout's
.claude/settings.local.json — the file git ignores — and set agent = "claude"
in the repo's config. The agent key is what carries that file into every new
worktree; without it, hooks in a gitignored file reach the main checkout and
no worktree at all. "treewright doctor" checks for exactly that gap.

Nothing is applied for you, deliberately: hooks live in a settings file you
own, and a merge that reordered it would be worse than asking you to paste.

--skill prints the other direction: a skill teaching the agent to drive
treewright — see what is in flight with "treewright ls --json", start parallel
work with new and a --prompt, respect the teardown guards. The instructions
name the one-line redirect that installs it.`,
			flags: []flagDoc{
				{"--skill", "print the skill that teaches the agent to drive treewright instead"},
			},
			run: cmdAgentInit,
		},
		{
			name:    "__complete",
			args:    "<slugs|targets|repos|shells|flags [command]>",
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
		if slices.Contains(c.aliases, name) {
			return c
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
	writeOverview(env.Stderr, env.Argv0)
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
		writeOverview(env.Stderr, env.Argv0)
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
			writeCommandHelp(env.Stdout, env.Argv0, target)
			return nil
		}
		writeOverview(env.Stdout, env.Argv0)
		return nil
	case "-v", "--version", "version":
		fmt.Fprintf(env.Stdout, "treewright %s\n", env.Version)
		return nil
	}

	cmd := lookup(name)
	if cmd == nil {
		return unknownCommand(&env, name)
	}

	// -h on a subcommand is a request for its help, not a flag it must accept.
	for _, a := range rest {
		if a == "-h" || a == "--help" {
			writeCommandHelp(env.Stdout, env.Argv0, cmd)
			return nil
		}
	}

	err := cmd.run(&env, rest)
	var ue *usageError
	if errors.As(err, &ue) {
		// A wrong invocation is worth showing the right one for.
		fmt.Fprintf(env.Stderr, "error: %s\n\n", ue.message)
		if target := lookup(ue.command); target != nil {
			writeCommandHelp(env.Stderr, env.Argv0, target)
		} else {
			writeOverview(env.Stderr, env.Argv0)
		}
		return ErrUsage
	}
	return err
}

// ---- help ------------------------------------------------------------------

const tagline = "treewright - give every ticket its own git worktree, tmux window, and agent session"

// The usage lines print argv0 — the name the user actually typed, "treewright"
// or its installed shorthand "tw" — so that help never scolds a user with a
// longer name than the one they are using. The same goes for every runtime hint
// that says a command to type ("attach with: tw attach proj"). What keeps the
// canonical name is help prose and anything destined for a file — tmux.conf
// lines, shell startup evals — which programs read and shell functions never
// reach.
func writeOverview(w io.Writer, argv0 string) {
	fmt.Fprintf(w, "%s\n\nusage: %s <command> [arguments]\n\ncommands:\n", tagline, argv0)

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

	fmt.Fprintf(w, "\nrun \"%s help <command>\" for detail on one command,\n", argv0)
	fmt.Fprintf(w, "or \"%s setup\" inside a repository to register it.\n", argv0)
	fmt.Fprintf(w, "\nconfig: %s/<name>.toml\n", config.Dir())
}

func writeCommandHelp(w io.Writer, argv0 string, c *command) {
	fmt.Fprintf(w, "usage: %s %s\n", argv0, strings.TrimSpace(c.name+" "+c.args))
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
