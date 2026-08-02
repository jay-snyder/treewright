// Command treewright gives every piece of work its own git worktree, tmux
// window, and agent session: one command to make all three, one to take them
// away when the work is done. A ticket key names that work where there is one,
// and the slug you typed names it where there is not.
//
// Translating errors into exit codes happens only here, so that every other
// package can report failure by returning an error rather than exiting. That is
// what makes the rest of the code testable.
package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/jay-snyder/treewright/internal/cli"
)

// version is overwritten at build time via -ldflags "-X main.version=v1.2.3";
// GoReleaser does this on every tagged release. Nothing else stamps it — notably
// not `go install ...@latest` — so when it is still "dev",
// cli.ResolveVersion falls back to the module version the go command embedded.
var version = "dev"

func main() {
	// Argv0 is passed through so help and hints can address the user by the name
	// they invoked — "tw", usually — rather than always the full one. The shell
	// integration masks that name: its tw function runs `command treewright`, so
	// argv[0] says "treewright" whichever name was typed. The wrapper therefore
	// reports the typed name in TREEWRIGHT_ARGV0, and argv[0] is the fallback for
	// a binary invoked directly.
	argv0 := os.Getenv("TREEWRIGHT_ARGV0")
	if argv0 == "" {
		argv0 = filepath.Base(os.Args[0])
	}
	err := cli.Run(cli.Env{Args: os.Args[1:], Argv0: argv0, Version: cli.ResolveVersion(version)})
	if err == nil {
		return
	}
	if !errors.Is(err, cli.ErrUsage) && !errors.Is(err, cli.ErrSilent) && !errors.Is(err, cli.ErrRefused) {
		// The three sentinels have reported themselves already. Printed through
		// cli rather than here, so that an error saying what to do next on a
		// second line is laid out the way every other message is.
		cli.WriteError(os.Stderr, err)
	}
	// Last, because it belongs under whatever was just printed: a popup holds its
	// contents on screen when the command in it exits non-zero, which is every
	// path that reaches here, and nothing else says how to dismiss it.
	cli.PopupHint(os.Stderr)

	if errors.Is(err, cli.ErrUsage) || errors.Is(err, cli.ErrRefused) {
		// Invoked wrongly, and already told why. 2 distinguishes that from a
		// command that ran and failed, which scripts sometimes need to tell apart.
		//
		// `guard` shares the code deliberately: an agent's PreToolUse hook blocks
		// on 2 and on nothing else, so a refusal has to arrive as this number.
		// Nothing reads the two apart — the caller of one is a shell and of the
		// other an agent hook, and neither ever sees the other's exit.
		os.Exit(2)
	}
	os.Exit(1)
}
