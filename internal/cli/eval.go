package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A compiled treemux runs as its own process, so it cannot change the working
// directory of the shell that invoked it. The shell integration closes that gap:
// the wrapper function creates a temporary file, passes its path in
// $TREEMUX_EVAL_FILE, and sources it after treemux exits, so anything appended
// here runs in the user's own shell.
//
// The commands written are restricted to what zsh, bash and fish all parse the
// same way, so one writer serves every shell.

// emitEval appends a shell command for the calling shell to run. It is a no-op
// without the shell integration, so every caller must still behave correctly
// when the command never runs.
func emitEval(env *Env, command string) {
	if env.EvalFile == "" {
		return
	}
	f, err := os.OpenFile(env.EvalFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, command)
}

// shellQuote wraps s in single quotes so a shell reads it as one literal word.
// An embedded quote is replaced by a sequence that closes the quoted run, emits
// a backslash-escaped quote, and reopens it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// insideDir reports whether path is dir itself or somewhere beneath it. Used to
// tell whether the caller is standing in the worktree being deleted.
func insideDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	// Only a ".." that is a whole path element means "outside". Testing for a
	// ".." prefix alone would misread a directory named ".." + something, such
	// as "..config", as escaping the parent.
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
