// Package tmux wraps the handful of tmux commands treemux drives.
package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Inside reports whether treemux is running inside a tmux session. Every
// window-opening operation is a no-op outside one, and callers fall back to
// telling the user what to run by hand.
func Inside() bool { return os.Getenv("TMUX") != "" }

// Pane is the pane treemux was invoked from, or "" when not under tmux.
func Pane() string { return os.Getenv("TMUX_PANE") }

func run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("tmux %s: %s (%w)", strings.Join(args, " "), msg, err)
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// OpenWindows maps each pane's working directory to the id of the window holding
// it. It is what stops `resume` from opening a second window on a worktree that
// already has one, and what fills the WINDOW column of the ls table.
//
// The whole mapping is fetched at once so that rendering a table of N worktrees
// costs one tmux invocation rather than N.
//
// The window id is formatted first because a pane's path may contain spaces
// while an id such as "@3" never does: splitting on the first space is only
// unambiguous in this order.
//
// Where several panes share a directory the earliest wins, so repeated calls
// return the same window for the same directory.
func OpenWindows() map[string]string {
	if !Inside() {
		return nil
	}
	out, err := run("list-panes", "-a", "-F", "#{window_id} #{pane_current_path}")
	if err != nil {
		return nil
	}
	return parsePanes(out)
}

// parsePanes turns the pane listing into a directory-to-window map. Split out so
// the space-in-path handling can be tested without a running tmux server.
func parsePanes(out string) map[string]string {
	if out == "" {
		return nil
	}
	windows := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		id, path, ok := strings.Cut(line, " ")
		if !ok || path == "" {
			continue
		}
		if _, seen := windows[path]; !seen {
			windows[path] = id
		}
	}
	return windows
}

// NewWindow opens a window whose working directory is dir, named name, running
// command. The command is handed to tmux as a single argument, so a multi-word
// value like "claude --continue" is run through the shell as written.
//
// automatic-rename is switched off immediately afterwards: left on, tmux
// renames the window after whatever process is running and the chosen name
// disappears. new-window makes the new window current, so the option applies
// to it.
func NewWindow(dir, name, command string) error {
	// The ";" is tmux's own command separator, passed as its own argument.
	_, err := run("new-window", "-c", dir, "-n", name, command,
		";", "set-window-option", "automatic-rename", "off")
	return err
}

// SelectWindow switches focus to a window id.
func SelectWindow(id string) error {
	_, err := run("select-window", "-t", id)
	return err
}

// WindowIDOf resolves a target (usually a pane id) to its window id.
func WindowIDOf(target string) (string, error) {
	return run("display-message", "-p", "-t", target, "#{window_id}")
}

// KillWindow closes the window containing target.
func KillWindow(target string) error {
	_, err := run("kill-window", "-t", target)
	return err
}
