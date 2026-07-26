// Command treewright manages isolated git worktree + tmux + agent sessions: one
// command per worktree, torn down together when the work is done.
//
// Translating errors into exit codes happens only here, so that every other
// package can report failure by returning an error rather than exiting. That is
// what makes the rest of the code testable.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jay-snyder/treewright/internal/cli"
)

// version is overwritten at build time via -ldflags "-X main.version=v1.2.3";
// GoReleaser does this on every tagged release. Plain `go build` leaves "dev".
var version = "dev"

func main() {
	// Argv0 is passed through so help output can address the user by the name
	// they invoked — "tw", usually — rather than always the full one.
	err := cli.Run(cli.Env{Args: os.Args[1:], Argv0: filepath.Base(os.Args[0]), Version: version})
	if err == nil {
		return
	}
	if !errors.Is(err, cli.ErrUsage) && !errors.Is(err, cli.ErrSilent) {
		// The two sentinels have reported themselves already.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	// Last, because it belongs under whatever was just printed: a popup holds its
	// contents on screen when the command in it exits non-zero, which is every
	// path that reaches here, and nothing else says how to dismiss it.
	cli.PopupHint(os.Stderr)

	if errors.Is(err, cli.ErrUsage) {
		// Invoked wrongly, and already told why. 2 distinguishes that from a
		// command that ran and failed, which scripts sometimes need to tell apart.
		os.Exit(2)
	}
	os.Exit(1)
}
