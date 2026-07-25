// Command treemux manages isolated git worktree + tmux + agent sessions: one
// command per stream of work, torn down together when the stream is done.
//
// Translating errors into exit codes happens only here, so that every other
// package can report failure by returning an error rather than exiting. That is
// what makes the rest of the code testable.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/jay-snyder/treemux/internal/cli"
)

// version is overwritten at build time via -ldflags "-X main.version=v1.2.3";
// GoReleaser does this on every tagged release. Plain `go build` leaves "dev".
var version = "dev"

func main() {
	err := cli.Run(cli.Env{Args: os.Args[1:], Version: version})
	switch {
	case err == nil:
		return
	case errors.Is(err, cli.ErrUsage):
		// Invoked wrongly, and already told why. 2 distinguishes that from a
		// command that ran and failed, which scripts sometimes need to tell apart.
		os.Exit(2)
	case errors.Is(err, cli.ErrSilent):
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
