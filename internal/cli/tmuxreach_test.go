package cli

import (
	"strings"
	"testing"
)

// The two commands here are about reaching treewright and reaching a session, which
// is the part of the tool that lives outside any worktree: `attach` puts a
// terminal in a repository's session, and `tmux-init` puts a key binding in the
// server so that a window running an agent — which has no shell in it to type
// into — can still be got out of.
//
// Moving a client needs a client, which a headless test has none of. What is
// covered here is everything up to that point: which session is chosen, what
// happens when there is none, and what a server holds once the integration has
// been loaded into it.

// ---- attach ------------------------------------------------------------------

// TestAttachWithoutASession covers the deliberate gap between attach and base.
// Attaching does not create a session, because base is the command that opens a
// repository's first window — so the error has to hand the reader that.
func TestAttachWithoutASession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")

	r := f.exec("attach")
	if r.err == nil {
		t.Fatalf("attach with no session succeeded, want an error\n%s", r.both())
	}
	if !strings.Contains(r.err.Error(), "treewright base proj") {
		t.Errorf("error = %q, want it to name the command that opens the session", r.err)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing — attach has no answer to print", r.stdout)
	}
}

// insideSession makes treewright believe it is running in a pane of a session, the
// way a real one would be.
//
// $TMUX is the flag that says there is a client to move rather than a terminal to
// take over; its value is never parsed. $TMUX_PANE is the part that matters, and
// it is a real pane id, because that is what tmux is asked about.
func insideSession(t *testing.T, session string) {
	t.Helper()
	pane, err := tmuxctl(t, "list-panes", "-t", "="+session, "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("find a pane in %s: %v\n%s", session, err, pane)
	}
	t.Setenv("TMUX", "/dev/null,0,0")
	t.Setenv("TMUX_PANE", strings.Split(pane, "\n")[0])
}

// TestAttachSaysWhenYouAreAlreadyThere covers the case where attaching is a
// no-op: nothing visible would happen, so something has to be said, or the
// command reads as having silently failed.
func TestAttachSaysWhenYouAreAlreadyThere(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "proj", "MAIN", f.MainDir)
	insideSession(t, "proj")

	r := f.exec("attach")
	if r.err != nil {
		t.Fatalf("attach: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stderr, "already attached to proj") {
		t.Errorf("stderr = %q, want it to say the client is already there", r.stderr)
	}
}

// TestAttachDoesNotMistakeAnotherSessionForThisOne is the regression test for
// asking tmux which session is current without saying which pane is asking.
// Untargeted, display-message names the most recently active session — so with a
// second session just created, attach decided it was already there and moved
// nobody. The pane is now named, so the answer is about the caller.
//
// Completing the move needs a client, which a headless test has none of. What is
// covered is the decision: attach must conclude it has somewhere to go.
func TestAttachDoesNotMistakeAnotherSessionForThisOne(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "proj", "MAIN", f.MainDir)
	// Created after proj, so it is the one an untargeted question names.
	startSession(t, "other", "DECOY", f.MainDir)
	addConfig(t, f.registry, "second",
		"main_dir = '"+f.MainDir+"'\nbase_branch = 'main'\ntmux_session = 'other'\n")

	insideSession(t, "proj")

	r := f.exec("attach", "second")
	if strings.Contains(r.stderr, "already attached") {
		t.Errorf("stderr = %q, want attach to know the client is in proj, not other", r.stderr)
	}
	// The move itself then fails, there being no client to move — which is the
	// proof that it was attempted.
	if r.err == nil || !strings.Contains(r.err.Error(), "no tmux client followed") {
		t.Errorf("err = %v, want the failure to come from trying to move a client", r.err)
	}
}

// TestAttachPrefersTheConfiguredSession keeps attach and the windows agreeing:
// tmux_session moves a repository's windows, and attach has to follow them.
func TestAttachPrefersTheConfiguredSession(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\ntmux_session = 'work'\n")
	// The session named after the config exists, and is the wrong one.
	startSession(t, "proj", "DECOY", f.MainDir)

	r := f.exec("attach")
	if r.err == nil {
		t.Fatalf("attach found a session, want it to look for %q\n%s", "work", r.both())
	}
	if !strings.Contains(r.err.Error(), "no tmux session work") {
		t.Errorf("error = %q, want it to name the configured session", r.err)
	}
}

// ---- cancelling a picker -------------------------------------------------------

// TestCancellingResumeSucceedsAndCancellingCdDoesNot pins an asymmetry that looks
// like an inconsistency and is not.
//
// resume runs in a tmux popup, which closes on success and stays up on failure so
// that an error can be read. Treating a dismissed menu as a failure therefore cost
// two Escapes — one to dismiss the picker, one to clear the popup holding
// "cancelled" on screen. Nothing failed, so it exits 0.
//
// cd cannot follow: its answer is the path on stdout, and `cd "$(treewright cd)"`
// succeeding with nothing to print would send the shell to the home directory.
//
// The picker reads keys from /dev/tty, so with no terminal it reports cancellation
// on its own — which is exactly the path under test.
func TestCancellingResumeSucceedsAndCancellingCdDoesNot(t *testing.T) {
	f := newFixture(t, "")
	// Two worktrees, so a menu is shown rather than the sole one chosen for us.
	f.mustRun("new", "eng-1")
	f.mustRun("new", "eng-2")

	resume := f.exec("resume")
	if resume.err != nil {
		t.Errorf("resume err = %v, want nil so the popup closes on one Escape", resume.err)
	}
	if !strings.Contains(resume.stderr, "cancelled") {
		t.Errorf("stderr = %q, want the cancellation reported", resume.stderr)
	}
	if resume.stdout != "" {
		t.Errorf("stdout = %q, want nothing — nothing was chosen", resume.stdout)
	}

	cd := f.exec("cd")
	if cd.err == nil {
		t.Error("cd err = nil; an empty answer would send `cd \"$(treewright cd)\"` home")
	}
	if cd.stdout != "" {
		t.Errorf("stdout = %q, want no path when nothing was chosen", cd.stdout)
	}
}

// ---- tmux-init ---------------------------------------------------------------

// TestTmuxInitPrintsTheIntegration holds tmux-init to the output contract: the
// snippet is the answer, so it goes to stdout alone and can be redirected into a
// file to source.
func TestTmuxInitPrintsTheIntegration(t *testing.T) {
	f := newFixture(t, "")

	r := f.exec("tmux-init")
	if r.err != nil {
		t.Fatalf("tmux-init: %v\n%s", r.err, r.both())
	}
	if !strings.Contains(r.stdout, "bind-key") {
		t.Errorf("stdout = %q, want the key bindings", r.stdout)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want nothing — the snippet is the answer", r.stderr)
	}
}

// TestTmuxInitTakesTheKeysAsFlags covers customization through the route that
// works for both ways of loading the snippet. Editing the printed file cannot
// help someone whose tmux.conf says `run-shell 'treewright tmux-init --apply'`,
// which is the form most people will use.
func TestTmuxInitTakesTheKeysAsFlags(t *testing.T) {
	f := newFixture(t, "")

	for _, spelling := range [][]string{
		{"tmux-init", "--resume-key", "G", "--new-key", "C-n"},
		{"tmux-init", "--resume-key=G", "--new-key=C-n"},
	} {
		r := f.exec(spelling...)
		if r.err != nil {
			t.Fatalf("%v: %v\n%s", spelling, r.err, r.both())
		}
		if !strings.Contains(r.stdout, "bind-key G run-shell") {
			t.Errorf("%v did not bind the key asked for:\n%s", spelling, r.stdout)
		}
		if !strings.Contains(r.stdout, "bind-key C-n command-prompt") {
			t.Errorf("%v did not bind the key asked for:\n%s", spelling, r.stdout)
		}
	}
}

// TestTmuxInitRejectsAnUnusableKey covers the two ways of getting the flag wrong:
// a key that would break the config it is written into, and one left off the end
// of the command line. Both have to be caught before anything is printed.
func TestTmuxInitRejectsAnUnusableKey(t *testing.T) {
	f := newFixture(t, "")

	bad := f.exec("tmux-init", "--resume-key", ";")
	if bad.err == nil {
		t.Fatalf("a key of %q was accepted\n%s", ";", bad.both())
	}
	if bad.stdout != "" {
		t.Errorf("stdout = %q, want nothing printed for a rejected key", bad.stdout)
	}

	// The value missing entirely must not be read as the flag after it, which
	// would silently drop --apply and print instead of loading.
	missing := f.exec("tmux-init", "--resume-key", "--apply")
	if missing.err == nil {
		t.Fatalf("--resume-key swallowed the flag after it\n%s", missing.both())
	}
	if !strings.Contains(missing.stderr, "needs a value") {
		t.Errorf("stderr = %q, want it to say the value is missing", missing.stderr)
	}
}

// TestTmuxInitApplyHonorsCustomKeys is the half that matters most: the flags have
// to reach the server, not just the printed text.
func TestTmuxInitApplyHonorsCustomKeys(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "proj", "MAIN", f.MainDir)

	if r := f.exec("tmux-init", "--apply", "--resume-key", "G", "--new-key", ""); r.err != nil {
		t.Fatalf("tmux-init --apply: %v\n%s", r.err, r.both())
	}

	keys, err := tmuxctl(t, "list-keys", "-T", "prefix")
	if err != nil {
		t.Fatalf("list the key bindings: %v\n%s", err, keys)
	}
	if !strings.Contains(keys, "bind-key    -T prefix G") {
		t.Errorf("G is not bound after --apply:\n%s", keys)
	}
	if strings.Contains(keys, "treewright new") {
		t.Errorf("an empty --new-key still bound something:\n%s", keys)
	}
}

// TestTmuxInitApplyLoadsTheBindings covers the one-line form: what the server
// ends up holding has to be what printing produces, or the two ways of loading
// the integration are two integrations.
func TestTmuxInitApplyLoadsTheBindings(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	// Key bindings live in a server, so there has to be one.
	startSession(t, "proj", "MAIN", f.MainDir)

	r := f.exec("tmux-init", "--apply")
	if r.err != nil {
		t.Fatalf("tmux-init --apply: %v\n%s", r.err, r.both())
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing — applying prints no snippet", r.stdout)
	}

	keys, err := tmuxctl(t, "list-keys", "-T", "prefix")
	if err != nil {
		t.Fatalf("list the key bindings: %v\n%s", err, keys)
	}
	if !strings.Contains(keys, "treewright popup") {
		t.Errorf("no binding reaches treewright after --apply:\n%s", keys)
	}
}

// TestTmuxInitApplyWithoutAServer names the one thing that can go wrong with it.
// Run from tmux.conf this cannot happen — the server is what is reading the file
// — so the error is for someone trying it by hand, and has to say why there is
// nowhere for a binding to go.
func TestTmuxInitApplyWithoutAServer(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")

	r := f.exec("tmux-init", "--apply")
	if r.err == nil {
		t.Fatalf("--apply succeeded with no server running\n%s", r.both())
	}
	if !strings.Contains(r.err.Error(), "server") {
		t.Errorf("error = %q, want it to explain that bindings live in a server", r.err)
	}
}

// TestDoctorDoesNotStartATmuxServer guards the side effect that asking about key
// bindings invites. list-keys starts a server when none is running, so a doctor
// that asked unconditionally would create an empty one and then report it as
// having no integration loaded — a finding it had just manufactured, on a server
// nobody wanted.
func TestDoctorDoesNotStartATmuxServer(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "")

	r := f.exec("doctor")

	if _, err := tmuxctl(t, "has-session"); err == nil {
		t.Error("doctor started a tmux server, which it only meant to ask about")
	}
	if strings.Contains(r.stdout, "tmux integration") {
		t.Errorf("doctor = %q, want no finding about bindings when no server holds any", r.stdout)
	}
}

// TestDoctorReportsTheTmuxIntegration covers the check that exists because this
// integration, unlike the shell one, can actually be asked about: a binding is a
// thing the server holds.
func TestDoctorReportsTheTmuxIntegration(t *testing.T) {
	requireTmux(t)
	f := newFixture(t, "command = 'sleep 300'\n")
	startSession(t, "proj", "MAIN", f.MainDir)

	// Read through findings rather than off stdout: the check name and its
	// detail are separate columns now, so the two are only one string once the
	// report has been parsed back into findings.
	if got := has(t, findings(t, f), "tmux integration not loaded"); got != "warn" {
		t.Errorf("finding = %q, want a warning that the integration is missing", got)
	}

	if r := f.exec("tmux-init", "--apply"); r.err != nil {
		t.Fatalf("tmux-init --apply: %v\n%s", r.err, r.both())
	}

	after := findings(t, f)
	if got := has(t, after, "tmux integration loaded"); got != "ok" {
		t.Errorf("finding = %q, want the integration reported as loaded\nall: %v", got, after)
	}
	if has(t, after, "tmux integration not loaded") != "" {
		t.Errorf("doctor still reports the tmux integration missing:\n%v", after)
	}
}
