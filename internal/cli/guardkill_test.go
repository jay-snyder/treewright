package cli

import (
	"os"
	"strings"
	"testing"
)

// The guard's one judgement that is not about paths: a raw tmux line that ends
// a window, pane, session or server on the server the calling agent's own
// window is on. close decides which windows may be closed from where, and a
// tmux kill decides nothing.

// TestTheGuardRefusesATmuxKillOnTheCallersServer, in every spelling tmux would
// run as one — the short aliases, an unambiguous prefix, a second command after
// a `;`, the caller's own server named outright — from a worktree's window and
// from the base window alike. The refusal names the bare close.
func TestTheGuardRefusesATmuxKillOnTheCallersServer(t *testing.T) {
	f := everyKind(t)
	label := os.Getenv("TREEWRIGHT_TMUX_LABEL")

	for _, window := range []string{"eng-1", "main"} {
		standIn(t, "proj", window)
		socket := strings.Split(os.Getenv("TMUX"), ",")[0]
		for _, command := range []string{
			"tmux kill-window -t @3",
			"tmux killw",
			"tmux kill-w -t @1",
			"tmux kill-pane",
			"tmux kill-session -t =proj",
			"tmux kill-server",
			"tmux respawn-window -k",
			"tmux unlink-window -t @2",
			`tmux new-window \; kill-window`,
			"cd /tmp && tmux kill-server",
			"env TERM=xterm tmux respawnp -k",
			"tmux -L " + label + " kill-server",
			"tmux -L" + label + " kill-server",
			"tmux -S " + socket + " kill-server",
		} {
			msg := f.bash(command).refused(t, window+": "+command)
			if !strings.Contains(msg, "would reach the tmux server this agent's window is on") ||
				!strings.Contains(flat(msg), "close this one "+canonicalName+" close") {
				t.Errorf("%s: %s: refusal = %q, want the server named and the bare close offered", window, command, msg)
			}
		}
	}
}

// TestTheGuardPassesATmuxLineThatKillsNothingOfTheCallers: a scratch server
// named with -L or -S is where experiments belong, and CLAUDE.md's own demo
// ends in killing one; and a tmux line whose command is not a kill is not
// one, whatever words appear later in it.
func TestTheGuardPassesATmuxLineThatKillsNothingOfTheCallers(t *testing.T) {
	f := everyKind(t)
	standIn(t, "proj", "eng-1")

	for _, command := range []string{
		"tmux -L other kill-server",
		"tmux -Lother kill-server",
		"tmux -S /tmp/somewhere-else/sock kill-server",
		`tmux display-message -p -t "$TMUX_PANE" '#{window_name}'`,
		"tmux rename-window kill-window",
		"tmux list-windows -F '#{window_id}'",
		"echo tmux kill-server",
	} {
		f.bash(command).allowed(t, command)
	}
}

// TestTheGuardLeavesTmuxAloneOutsideTreewrightsWindows: in a plain shell, or a
// window treewright did not open, there is no window of treewright's for the
// line to close by mistake — the guard's ordinary silence.
func TestTheGuardLeavesTmuxAloneOutsideTreewrightsWindows(t *testing.T) {
	f := everyKind(t)
	f.bash("tmux kill-server").allowed(t, "from no window at all")

	startSession(t, "mine", "hand-made", f.MainDir)
	standIn(t, "mine", "hand-made")
	f.bash("tmux kill-window").allowed(t, "from a window treewright did not open")
}

// TestTmuxInvocationFindsTheServerAndEveryCommand: tmux's own flags, stuck or
// separate and clustered, then the command words — the first, and each after a
// separating `;`.
func TestTmuxInvocationFindsTheServerAndEveryCommand(t *testing.T) {
	for _, tc := range []struct {
		args          []string
		label, socket string
		commands      []string
	}{
		{[]string{"kill-server"}, "", "", []string{"kill-server"}},
		{[]string{"-L", "work", "kill-window", "-t", "@3"}, "work", "", []string{"kill-window"}},
		{[]string{"-uLwork", "ls"}, "work", "", []string{"ls"}},
		{[]string{"-S", "/s", "-f", "/dev/null", "new"}, "", "/s", []string{"new"}},
		{[]string{"new-window", ";", "kill-pane"}, "", "", []string{"new-window", "kill-pane"}},
		{[]string{"new-window;", "killp", "-a"}, "", "", []string{"new-window", "killp"}},
		{[]string{"--", "kill-session"}, "", "", []string{"kill-session"}},
	} {
		label, socket, commands := tmuxInvocation(tc.args)
		if label != tc.label || socket != tc.socket || strings.Join(commands, " ") != strings.Join(tc.commands, " ") {
			t.Errorf("tmuxInvocation(%q) = %q, %q, %q; want %q, %q, %q",
				tc.args, label, socket, commands, tc.label, tc.socket, tc.commands)
		}
	}
}
