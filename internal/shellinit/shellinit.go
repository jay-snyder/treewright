// Package shellinit produces the shell integration treewright needs.
//
// treewright is a compiled binary, so it runs in its own process and cannot change
// the calling shell's working directory. Two small things therefore have to live
// in the shell itself: a wrapper function that lets treewright hand back a command
// to run (see the eval-file protocol in internal/cli), and tab completion.
//
// Rather than installing files per shell, the binary prints its own integration:
//
//	eval "$(treewright shell-init zsh)"     # or bash
//	treewright shell-init fish | source
//
// Each script also defines tw, the everyday short name: the same wrapper under
// fewer keystrokes, with the same completion. treewright is the name of the
// product; tw is the name of the habit.
//
// The shims are versioned with the binary that emits them, so they can never
// drift out of sync with it. This is the same approach fzf, zoxide, direnv and
// starship take, all of which are compiled binaries that still need a shell-side
// shim for exactly this reason.
package shellinit

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Shells lists the supported shell names.
func Shells() []string {
	names := make([]string, 0, len(shims))
	for name := range shims {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// versionPlaceholder is where a script writes the fingerprint of itself. Filled
// here rather than written into the file, for the obvious reason: a file cannot
// hold a digest of its own bytes.
const versionPlaceholder = "{{version}}"

// Script returns the integration snippet for a shell.
func Script(shell string) (string, error) {
	s, ok := shims[shell]
	if !ok {
		return "", fmt.Errorf("unsupported shell %q (supported: %s)", shell, strings.Join(Shells(), ", "))
	}
	return render(s.script), nil
}

// render fills a script's fingerprint in, which is all that separates the
// checked-in file from what a shell is handed.
func render(script string) string {
	return strings.ReplaceAll(script, versionPlaceholder, fingerprint(script))
}

// VersionVar is the variable each shim exports, holding the fingerprint of the
// shim itself.
//
// It exists because the shell integration is the one part of treewright that
// cannot be asked about. A binding is a thing a tmux server holds and a plugin
// is a file on disk, but a shell function lives in the user's own shell, and a
// child process cannot read its parent's function table. Until now doctor
// inferred "loaded" from TREEWRIGHT_EVAL_FILE being set — which says a wrapper
// is there and nothing whatever about which binary emitted it, in a shell that
// may have been open since two releases ago.
//
// So the shim carries its own version out to every child, and doctor compares.
// The value is exported rather than merely set because doctor is a child
// process; it is a fingerprint rather than a release number because a shim built
// from an unstamped tree still has to be distinguishable from an older one.
const VersionVar = "TREEWRIGHT_SHELL_INIT_VERSION"

// Version is the fingerprint the shim for shell exports.
func Version(shell string) (string, error) {
	s, ok := shims[shell]
	if !ok {
		return "", fmt.Errorf("unsupported shell %q (supported: %s)", shell, strings.Join(Shells(), ", "))
	}
	return fingerprint(s.script), nil
}

// Current reports whether v is the fingerprint of a shim this binary emits.
//
// Any shim, rather than a named shell's, so that nothing has to work out which
// shell the caller's wrapper came from — a question the environment answers
// badly, $SHELL being the login shell rather than the running one. A fingerprint
// identifies its script on its own, so "is this one of mine" is the whole
// question and it has an exact answer.
func Current(v string) bool {
	if v == "" {
		return false
	}
	for _, s := range shims {
		if fingerprint(s.script) == v {
			return true
		}
	}
	return false
}

// ShellVar is the variable each shim exports beside VersionVar, naming the shell
// it was written for.
//
// VersionVar cannot say that once it is stale. A fingerprint names one script,
// and a stale one names a script this binary does not carry, which is what
// makes it stale in the first place. So a shell that refresh cannot reach
// could only be told which line reloads it by guessing from $SHELL, which is
// the login shell rather than the running one: a fish started from a bash
// login would be handed bash's line. Shims emitted before this variable
// existed do not export it, and for those $SHELL is still the guess.
const ShellVar = "TREEWRIGHT_SHELL_INIT_SHELL"

// LoadLine is the line a shell's startup file loads its shim with. It is also
// how a shell holding an older shim is brought up to date by hand, since it
// asks the binary on PATH for its own text.
func LoadLine(shell string) (string, bool) {
	s, ok := shims[shell]
	return s.load, ok
}

// Reload is the text that, sourced by zsh, bash or fish, re-evaluates this
// binary's shim for that shell, and does nothing at all in the other two.
//
// It is how refresh replaces the wrapper in the shell it was run from. That
// wrapper sources the eval file after the binary exits, so a shim appended there
// redefines treewright and tw in the live shell, and the call doing the sourcing
// finishes on the body it started with. Every shell holds on to a running
// function's body when the function is redefined, and
// TestAReloadReplacesTheWrapperThatSourcesIt holds all three to that.
//
// All three shims go in, because which shell is on the other end of the eval
// file is a question the binary cannot answer. $SHELL is the login shell rather
// than the running one, ShellVar exists only in shims newer than the ones that
// need replacing, and a stale fingerprint names none of this binary's scripts.
// Each shim is guarded by a test only its own shell passes, so no guess is
// needed. That also keeps the eval-file protocol's one rule, that every line
// is one all three shells parse the same way: each shim is a quoted string to
// the two shells it is not for.
func Reload() string {
	lines := make([]string, 0, len(shims))
	for _, shell := range Shells() {
		s := shims[shell]
		lines = append(lines, s.onlyIn+" && eval "+Quote(render(s.script)))
	}
	return strings.Join(lines, "\n")
}

// Quote wraps s in single quotes so that zsh, bash, fish and POSIX sh all read
// it back as exactly s.
//
// The POSIX rule does most of it: close the quote, write an escaped quote,
// reopen. fish reads that the same way. A backslash is where they part, because
// fish takes \\ and \' inside single quotes as escapes while the POSIX shells
// take every character there literally. So a backslash is handled the way a
// quote is, written outside the quotes and escaped, where all of them agree.
// That costs four bytes where there was one, as a quote always has.
//
// It is the only quoting rule treewright has, which is why it is exported. The
// eval file is where fish is certain to be the reader, and it is not the only
// place fish can be: tmux runs a popup's command, and a window's, through its
// default-shell, which starts out as the user's login shell. A rule that is
// right in every shell is simpler to own than two rules and a judgement about
// which one a line needs. There were two once. internal/cli kept the POSIX-only
// form after this one was fixed, and a `cd` into a directory holding \' became
// a line fish could not parse. TestQuotingReadsTheSameInEveryShell holds all
// four shells to it.
//
// internal/tmuxinit keeps its own copy of fingerprint, below, because sharing
// it would take a package that exists to hold four lines. Sharing this takes
// nothing, since internal/cli imports this package already. And those are two
// digests of two different files that happen to be spelled alike, where this
// is one rule about one thing.
func Quote(s string) string {
	return "'" + quoter.Replace(s) + "'"
}

// quoter is Quote's substitution, done in one pass. Done as two, the quotes the
// first pass writes around each backslash would be escaped again by the second.
var quoter = strings.NewReplacer(`\`, `'\\'`, `'`, `'\''`)

// fingerprint digests a script's checked-in bytes, placeholder and all, which is
// what makes it stable: the value names the file rather than the rendering.
//
// Twelve hex characters, as internal/tmuxinit uses for the same purpose. The two
// are deliberately not shared — four lines of digest in each beats a package
// existing to hold four lines — but they answer the same question and so are
// spelled the same way.
func fingerprint(script string) string {
	sum := sha256.Sum256([]byte(script))
	return hex.EncodeToString(sum[:])[:12]
}

// shim is what this package knows about one shell's integration.
type shim struct {
	// script is the checked-in text, fingerprint placeholder and all.
	script string

	// load is the line a startup file loads the script with.
	load string

	// onlyIn is a test that is true in this shell and in neither of the other
	// two, written so that all three can parse it. Each shell reads every line
	// of a reload, and fish parses the whole file before it runs any of it, so
	// a line that one of them cannot parse breaks the reload for everybody.
	//
	// The obvious test is the variable each shell sets for itself, and it is
	// the one that cannot be used as it stands. Under `set -u` a POSIX shell
	// refuses to expand an unset variable, and bash abandons the whole file at
	// the first one, taking the wrapper's own cleanup with it. `${ZSH_VERSION-}`
	// is safe there, but fish cannot parse it even on a line it never runs.
	//
	// So fish is told apart from the other two by quoting, which needs no
	// variable. Inside single quotes fish reads a pair of backslashes as one,
	// where zsh and bash read it as two, and all three read the double-quoted
	// pair as one. The variable test is then safe to write, inside a quoted
	// eval that only zsh and bash ever evaluate.
	onlyIn string
}

// notFish is true in zsh and bash, and false in fish. See shim.onlyIn.
const notFish = `test '\\' != "\\"`

var shims = map[string]shim{
	"zsh": {
		script: zshScript,
		load:   `eval "$(treewright shell-init zsh)"`,
		onlyIn: notFish + ` && eval 'test -n "${ZSH_VERSION-}"'`,
	},
	"bash": {
		script: bashScript,
		load:   `eval "$(treewright shell-init bash)"`,
		onlyIn: notFish + ` && eval 'test -n "${BASH_VERSION-}"'`,
	},
	"fish": {
		script: fishScript,
		load:   "treewright shell-init fish | source",
		onlyIn: `test '\\' = "\\"`,
	},
}

// The wrapper in each shell follows the same three steps: make a temp file,
// hand its path to the binary as $TREEWRIGHT_EVAL_FILE, then source it if the
// binary wrote anything. Three commands write to it: `treewright cd`, `treewright rm`
// when the shell is standing in the directory being deleted, and `treewright
// refresh` when the wrapper doing the sourcing is an older one (see Reload).
//
// Every external program the wrappers call is invoked through `command`, for the
// same reason the binary itself is: zsh and bash expand aliases in a function
// body when the function is defined, so an alias in the user's startup file
// silently rewrites the words below. `alias rm='rm -i'` is common enough to be
// the expected case, and turns `rm -f` into `rm -i -f` — harmless only because
// BSD and GNU rm both let the later flag win. Nothing here should depend on that.
//
// The subcommand names each script lists are checked against the real command
// table by a test, so a command added to treewright cannot silently go missing from
// completion.

// Each script is checked in under scripts/ and embedded by name, so what a
// contributor reads and what a shell parses are the same bytes — shell as
// shell, in a file its own tooling understands, rather than 173 lines of it
// quoted inside Go where nothing highlights or checks it.
//
// Named one //go:embed at a time, never a pattern that walks scripts/, for the
// reason the agent plugin's files are: this text is eval'd into the user's
// interactive shell at every start, so a file must ship because somebody wrote
// its name here, not because it was sitting in a folder. The other direction is
// TestEveryScriptIsDeclared, which fails on a file in scripts/ that no shell
// claims — inert is its own kind of surprise.
var (
	//go:embed scripts/init.zsh
	zshScript string

	//go:embed scripts/init.bash
	bashScript string

	//go:embed scripts/init.fish
	fishScript string
)
