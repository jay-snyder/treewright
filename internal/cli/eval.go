package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A compiled treewright runs as its own process, so it cannot change the working
// directory of the shell that invoked it. The shell integration closes that gap:
// the wrapper function creates a temporary file, passes its path in
// $TREEWRIGHT_EVAL_FILE, and sources it after treewright exits, so anything appended
// here runs in the user's own shell.
//
// The commands written are restricted to what zsh, bash and fish all parse the
// same way, so one writer serves every shell.

// appendEval appends a shell command to the eval file for the calling shell to
// run. The caller owns the fallback: without the integration, and when the file
// cannot be written, the command never runs and something still has to say what
// to type — which is why the callers go through moveShell rather than here.
func appendEval(path, command string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, command); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// moveShell asks the calling shell to cd into dir, and says what to type when
// nothing will run the command: reason is the first line of that message,
// naming what did not happen from the caller's side.
//
// Both halves live in one function because they are one rule — an eval that may
// never run must leave a by-hand line behind — and spelled at each call site, a
// third caller can forget half of it. The half that used to be missing
// everywhere was the eval file that exists and cannot be written: a swept
// tmpdir, a full disk. emitEval swallowed that error, both callers printed
// their fallback only when no integration was loaded at all, and the result was
// a `cd` that printed a path, moved nothing, and said nothing about why — a
// failure with no report path, which nothing here is allowed to have.
func moveShell(env *Env, dir, reason string) {
	if env.EvalFile != "" {
		err := appendEval(env.EvalFile, "cd "+shellQuote(dir))
		if err == nil {
			return
		}
		env.warnf("the shell integration is loaded, but its eval file could not be written\n%v\nyour shell stays where it is%s",
			err, asFields(field("run", env.copyable("cd "+dir))))
		return
	}
	env.progressf("%s%s", reason, asFields(field("run", env.copyable("cd "+dir))))
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
