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
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

// Shells lists the supported shell names.
func Shells() []string {
	names := make([]string, 0, len(scripts))
	for name := range scripts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Script returns the integration snippet for a shell.
func Script(shell string) (string, error) {
	s, ok := scripts[shell]
	if !ok {
		return "", fmt.Errorf("unsupported shell %q (supported: %s)", shell, strings.Join(Shells(), ", "))
	}
	return s, nil
}

var scripts = map[string]string{
	"zsh":  zshScript,
	"bash": bashScript,
	"fish": fishScript,
}

// The wrapper in each shell follows the same three steps: make a temp file,
// hand its path to the binary as $TREEWRIGHT_EVAL_FILE, then source it if the
// binary wrote anything. Two commands write to it: `treewright cd`, and `treewright rm`
// when the shell is standing in the directory being deleted.
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
