package cli

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/shellinit"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// Closing a worktree's tmux window, as a verb of treewright's own.
//
// This is the last thing driving treewright required typing raw tmux for.
// `rm` and `prune` do not close a window without being asked — something may
// still be running in it — so with nobody to prompt they printed a
// `tmux kill-window -t @16` for the caller to run, and `send` named the same
// line for a window whose agent had died. Every objection that made `send` a
// command applies: the raw line honors no TREEWRIGHT_TMUX_LABEL, goes through
// no exact(), and knows nothing of the @treewright_worktree stamp — and it
// fails silently and destructively when it is wrong, since tmux closes whatever
// window id it finds on whatever server it reaches and exits 0.
//
// The window is found by the worktree's directory, the way every other window
// is. That is what makes this work after `rm`: the directory is gone from disk,
// but @treewright_worktree still holds the path, so the window answers for a
// worktree that no longer exists — which is the case the command is mostly for.
// The path is computed from the slug rather than looked up among the worktrees
// for the same reason.
//
// A scratch window is the exception in both directions. It is found by its name,
// since the window standing for its directory is the base window — and closing
// it ends the scratch session, not only the window: the record of its
// conversation goes too, so restore will not bring it back. That is what makes
// this the way a person says a scratch session is done, and why it also takes a
// recorded session whose window is already gone, which then has nothing but its
// record to lose.

func cmdClose(env *Env, args []string) error {
	var repoName string
	positional, err := parseArgs("close", args, nil, repoValues(&repoName, nil), 1)
	if err != nil {
		return err
	}
	caller := callerOf()
	slug := at(positional, 0)
	if slug == "" {
		return closeOwnWindow(env, caller, repoName)
	}

	cfg, err := resolveConfig(repoName)
	if err != nil {
		return err
	}
	// Only the slug names a window; the branch prefix the user may have typed is
	// git's business, as it is everywhere but `new`.
	_, slug = splitPrefix(env, cfg, slug)

	if !tmux.Available() {
		return fmt.Errorf("tmux is not installed, so there is no window to close")
	}
	session := sessionFor(cfg)
	windows := tmux.Windows(session)
	target := closeTarget(env, cfg, slug, scratchSessions(cfg))
	window, ok := target.Window, target.Scratch
	if !target.Scratch {
		window, ok = windows[target.Dir]
	}
	// Decided before anything below, the ending of a recorded scratch session
	// included: whether this caller may close what it named does not depend on
	// whether there turns out to be a window to close.
	if refusal := caller.refusal(window, target.Base, false); refusal != nil {
		return refusal.closeError(env, cfg, target.name(), target.name())
	}
	// A recorded scratch session whose window is gone — a restart took it, and
	// restore has not been run — has only its record left, and ending it is
	// removing that. Said rather than refused: closing is the request, and the
	// session is exactly as closed afterwards as one whose window was open.
	if target.Scratch && window.ID == "" {
		forgetSession(cfg, target.name())
		env.progressf("ended scratch session %s, which had no window open\nrestore will not reopen it", target.name())
		return nil
	}
	if !ok || window.ID == "" {
		// Said for every name but the base checkout's: the reader may have meant a
		// scratch window, and needs to hear that none is open by that name rather
		// than only that nothing stands on a worktree directory they never meant.
		notScratch := ""
		if !target.Base {
			notScratch = "and no scratch window by that name is open"
		}
		return fmt.Errorf("no window is open on %s in %s%s%s", target.name(), cfg.Name, under(notScratch), asFields(
			field("looked for a window on", target.Dir),
			field("open in that session now", strings.Join(openWindowNames(windows, tmux.Scratch(cfg.Name)), "\n")),
		))
	}
	return shutWindow(env, cfg, target, window)
}

// closeOwnWindow is `close` with nothing named: the window the command is
// running in, found from the pane rather than from anything the caller had to
// work out.
//
// That is the whole fix for the incident this exists for. An agent closing its
// own window has to name it, and naming it is the step that went wrong: a
// scratch agent standing in the main checkout asked tmux which window it was
// in, without saying which pane was asking, was told about the window the
// attached client was looking at, and closed the base window under that name.
// An agent that never names its own window cannot name the wrong one.
//
// From the base window it is refused like `close base` — the base window is
// never closed from inside one of treewright's windows — and from anywhere that
// is not one of treewright's windows it is still the usage error it always was,
// there being no window it could mean.
//
// --repo is accepted, because the agent guide tells agents to pass it always,
// and refused when it names some other repository than the window's own: a
// bare close in another repository's name is a request this window cannot be
// the answer to.
func closeOwnWindow(env *Env, caller closer, repoName string) error {
	own := caller.window
	switch caller.kind {
	case kindNone:
		return usageErrorf("close", "a slug is required\nwith none, it closes the worktree or scratch window it is run in, and this is running in neither")
	case kindBase:
		return caller.refusal(own, true, false).closeError(env, nil, own.Name, own.Name)
	case kindWorktree, kindScratch:
		// The two kinds of window a bare close can mean, carried on below.
	}
	if repoName != "" && repoName != own.Repo {
		return fmt.Errorf("--repo names %s, and the window this is running in is %s's\nrun bare, close takes only the window it is running in%s",
			repoName, own.Repo, closeThisOne(env))
	}
	cfg, err := resolveConfig(own.Repo)
	if err != nil {
		return err
	}
	target := choice{Worktree: git.Worktree{Dir: own.Worktree}}
	if caller.kind == kindScratch {
		target = scratchChoice(cfg, own)
	}
	return shutWindow(env, cfg, target, own)
}

// shutWindow closes a window that has been found and allowed, saying first what
// that costs.
func shutWindow(env *Env, cfg *config.Config, target choice, window tmux.Window) error {
	// Everything is said before the window goes, not after, and that ordering is
	// the whole of what this function has to get right. Closing the caller's own
	// window kills the pane treewright is running in, so there is no "afterwards"
	// to report from — and closing a session's last window can detach the client
	// that would have read it. A message that arrives only when nothing important
	// happened is not a message.
	warnIfAgentWorking(env, window)
	costs := closeCosts(window)
	// Before the window goes, like everything else here, and for the stronger
	// reason: killing the window kills its agent, which reports the ending as a
	// kill — the one ending that leaves a record alone, since it is also what a
	// restart looks like.
	if target.Scratch && forgetSession(cfg, target.name()) {
		costs = append(costs, "its conversation is no longer recorded, so restore will not reopen it")
	}
	env.progressf("closing tmux window %s%s", window.Name, under(strings.Join(costs, "\n")))
	return tmux.KillWindow(window.ID)
}

// closeTarget works out which directory's window is meant, and what to call it
// in a message.
//
// A live worktree resolves the way `resume` and `rm` resolve one — an
// unambiguous prefix is enough and the expansion is reported. What is different
// here is the fallback: a slug matching no worktree at all is not an error but
// the ordinary case, since the command a reader reaches this through is `rm`,
// which has just deleted the worktree the window is still sitting in. So the
// directory is computed from the slug, which needs nothing to exist.
//
// The base checkout answers to its own names, as it does in the resume menu.
// Whether its window may be closed is closer.refusal's question, and from
// inside any of treewright's windows the answer is no; from outside them it is
// a person's call, which usually ends the repository's session and is said
// before it does. Either way it leaves the base window's record alone, closing
// the window being no reason for the next `resume base` to lose its
// conversation. A scratch session answers to its exact
// name next, open or recorded, as it does in chooseWorktree and for its reasons.
func closeTarget(env *Env, cfg *config.Config, slug string, scratch map[string]tmux.Window) choice {
	if base := baseChoice(cfg); slices.Contains(baseNames(cfg, base), slug) {
		return base
	}
	if w, ok := scratch[slug]; ok {
		return scratchChoice(cfg, w)
	}
	if managed, err := repoFor(cfg).Managed(); err == nil {
		// resolveSlug reports the expansion and errors when nothing matches; only
		// the match is wanted here, the miss being the removed-worktree case.
		if wt, err := resolveSlug(env, cfg, managed, slug); err == nil {
			return choice{Worktree: wt}
		}
	}
	return choice{Worktree: git.Worktree{Slug: slug, Dir: cfg.DirFor(slug)}}
}

// agentWorkingNote is the caveat about closing a window whose agent says it is
// working, as a format taking the window's name. A constant because it is said
// in two registers — warnIfAgentWorking prints it as a warning, and rm's prompt
// writes it to the tty above its question — and the two must stay one sentence.
const agentWorkingNote = "the agent in %s says it is working\nit is the window's command, so closing the window stops it"

// warnIfAgentWorking says so before a window closes with an agent still working
// in it.
//
// The agent is the window's command, so there is no detaching from this and
// coming back: closing the window stops the work and takes the session with it.
// That is the one thing about a window treewright knows and the caller may not,
// since the state comes from the agent's own hooks rather than from anything
// visible in the window's name.
//
// It warns rather than refuses. The caller asked to close this window, and
// treewright is in no position to judge whether what the agent is doing still
// matters — a refusal would need a --force to get past, which is a flag people
// learn to pass by reflex. What a warning buys is that the loss is on the record
// at the moment it happens rather than discovered later, and that is also why it
// is said before the window goes: afterwards there may be no session left to say
// it in.
//
// Only `working` warns. `waiting` is an agent blocked on a person and `done` is
// one with nothing in flight — those are the states an ordinary teardown closes,
// and a warning that fires on the ordinary case is one that stops being read.
func warnIfAgentWorking(env *Env, window tmux.Window) {
	if window.State != stateWorking {
		return
	}
	env.warnf(agentWorkingNote, window.Name)
}

// closeCosts lists what closing this window will take with it, one per line.
//
// Both of them change what a reader should do rather than merely describing the
// window, which is why they are said at all: a session ending moves or detaches
// whoever was attached to it, and the caller's own window ending means this is
// the last thing that happens in this session — the ordering the agent guide
// asks for, made visible at the moment it applies.
func closeCosts(window tmux.Window) []string {
	var costs []string
	if note := lastInSessionNote(window); note != "" {
		costs = append(costs, note)
	}
	if window.ID == tmux.CurrentWindow() {
		costs = append(costs, "it is the window this command is running in, so nothing after this runs")
	}
	return costs
}

// openWindowNames lists the windows treewright can see, for the error about one
// it cannot find. Sorted, since a map's order would make the same repository
// answer differently each time.
//
// The scratch windows are listed beside the rest: they claim no directory, so the
// first map never holds them, and a list of what is open that left them out
// would be wrong about the very windows a reader of this error may have meant.
func openWindowNames(windows, scratch map[string]tmux.Window) []string {
	seen := make(map[string]bool, len(windows)+len(scratch))
	var names []string
	for _, w := range slices.Concat(slices.Collect(maps.Values(windows)), slices.Collect(maps.Values(scratch))) {
		if w.Name == "" || seen[w.ID] {
			continue
		}
		seen[w.ID] = true
		names = append(names, w.Name)
	}
	if len(names) == 0 {
		return []string{"nothing — no tmux window is open"}
	}
	sort.Strings(names)
	return names
}

// closeHint is how the commands that leave a window behind say to close it: a
// treewright command rather than the `tmux kill-window` they used to print.
//
// Named once because `rm`, `prune` and `send` all reach for it, and built by
// hint so it carries the repository — a stale window is the case where naming
// only the slug is least defensible, since the worktree that would have
// disambiguated it has just been deleted.
func closeHint(env *Env, cfg *config.Config, slug string) string {
	return hint(env, cfg, "close", slug)
}

// ---- whose window it is ----------------------------------------------------

// A window is closed only by its own agent, or from outside treewright's windows.
//
// `close` used to take any window it was pointed at, and said rather than
// refused the costs of the two that mattered: the base window, whose closing
// usually ends the repository's session, and the caller's own. Both were real
// things to want, and the reasoning was warnIfAgentWorking's — the caller asked,
// and treewright is in no position to judge. That reasoning assumed the caller
// knew which window it was pointing at. On 2026-10-28 a scratch agent reviewing
// a pull request in the main checkout set out to close its own window, asked
// tmux which window that was without naming its pane, was told about the window
// the attached client was showing, concluded from where it stood that it was
// the base window, and ran `close base`. treewright closed the base window, as
// documented, and the operator lost the session they were working in.
//
// A warning could not have stopped it: the agent was confident and wrong, and a
// confident caller reads a warning as the cost it already accepted. So the rule
// moved into the binary, and it is decided by the window the command is running
// in, which the pane knows and nobody has to work out:
//
//   - from inside a worktree or scratch window, only that window can be closed;
//   - the base window is never closed from inside any of treewright's windows,
//     its own included — a person closes it from a shell outside them, or by
//     quitting its agent;
//   - from the base window, another window closes once its agent has yielded
//     it — see cmdYield — and not before;
//   - from outside treewright's windows, nothing changes.
//
// There is no --force, for the reason warnIfAgentWorking gives, and nothing here
// warns where it could refuse: the cases are the ones where the caller's own
// judgement is what failed.

// windowKind is what a window is to that rule.
type windowKind int

const (
	kindNone     windowKind = iota // no window, or one treewright did not open
	kindBase                       // treewright's window on the main checkout
	kindWorktree                   // treewright's window on a worktree
	kindScratch                    // a second agent session on the main checkout
)

// closer is the window a command that closes windows is running in.
type closer struct {
	window tmux.Window
	kind   windowKind
}

// callerOf finds and classifies the window this command is running in, from
// $TMUX_PANE. Outside tmux, without a pane to ask, or in a window treewright did
// not open, it is kindNone — the caller every rule leaves alone.
func callerOf() closer {
	w, ok := tmux.CallersWindow()
	if !ok {
		return closer{}
	}
	return closer{window: w, kind: kindOf(w)}
}

// kindOf classifies a window by what treewright stamped on it.
//
// The base window and a worktree's carry the same stamps and differ only in the
// path, so telling them apart means asking the window's own repository where
// its main checkout is — the stamp being cfg.MainDir exactly, as every lookup of
// the base window already relies on. A repository whose config cannot be read
// any more leaves the question unanswered, and the answer is kindWorktree:
// the stricter of the two everywhere but its own window, and a base window
// cannot be reached as its own window by name without being recognized as the
// base, since naming it means naming the repository whose config just loaded.
func kindOf(w tmux.Window) windowKind {
	switch {
	case !w.Stamped():
		return kindNone
	case w.Scratch != "":
		return kindScratch
	}
	if cfg, err := config.Resolve(w.Repo, ""); err == nil && w.Worktree == cfg.MainDir {
		return kindBase
	}
	return kindWorktree
}

// refusalReason is which of the rule's clauses refused.
type refusalReason int

const (
	notItsOwn    refusalReason = iota // a worktree or scratch window reaching past itself
	neverTheBase                      // the base window, from inside any of treewright's windows
	notYielded                        // the base window, reaching a window whose agent has not yielded it
)

// closeRefusal is a close the rule refused, with what the messages need.
type closeRefusal struct {
	reason refusalReason
	caller closer
	target tmux.Window
}

// refusal is the one check every path that closes a window goes through —
// `close`, rm --yes before it removes anything, and rm's and prune's question —
// and nil when the caller may close target.
//
// base says the target was named as the base checkout, which a window standing
// there that treewright did not open still answers to; a window treewright
// opened on the main checkout is the base window however it was reached, which
// matters because a base window whose pane has wandered into a worktree is what
// that worktree's directory finds once its own window has closed. A target with
// no ID is a session with no window open, which has no agent to yield it.
//
// person says a person at a terminal answers for this close, which is rm's and
// prune's y/N: that question is the person's to answer, and the yield exists
// only because an agent asking on its own behalf has nobody to ask.
func (c closer) refusal(target tmux.Window, base, person bool) *closeRefusal {
	if c.kind == kindNone {
		return nil
	}
	refuse := func(reason refusalReason) *closeRefusal {
		return &closeRefusal{reason: reason, caller: c, target: target}
	}
	if base || kindOf(target) == kindBase {
		return refuse(neverTheBase)
	}
	if c.kind != kindBase {
		if target.ID != "" && target.ID == c.window.ID {
			return nil
		}
		return refuse(notItsOwn)
	}
	if target.ID == "" || target.Yielded || person {
		return nil
	}
	return refuse(notYielded)
}

// what is the refusal's first line: the window, and what it is to this caller.
// label is what to call it.
func (r *closeRefusal) what(label string) string {
	switch {
	case r.reason == notYielded:
		return fmt.Sprintf("%s's agent has not yielded its window", label)
	case r.reason == notItsOwn:
		return fmt.Sprintf("%s is not the window this command is running in, which is %s", label, r.caller.window.Name)
	case r.target.ID != "" && r.target.ID == r.caller.window.ID:
		return fmt.Sprintf("%s is the main checkout's window, and this command is running in it", label)
	}
	return fmt.Sprintf("%s is the main checkout's window, and this command is running in %s's", label, r.caller.window.Name)
}

// rule is the clause that refused, as one line.
func (r *closeRefusal) rule() string {
	switch {
	case r.reason == notYielded:
		return "closing a window stops its agent, and only that agent knows whether anything would be lost"
	case r.caller.kind == kindBase:
		return "the base window is never closed from inside treewright's windows"
	}
	return "from inside a worktree or scratch window, only that window can be closed"
}

// closeError is the refusal as close returns it: what is wrong, what it costs,
// then what to type. label names the window and name is what reaches it.
func (r *closeRefusal) closeError(env *Env, cfg *config.Config, label, name string) error {
	return fmt.Errorf("%s\n%s%s", r.what(label), r.rule(), r.instead(env, cfg, name))
}

// rmError is the refusal as rm --yes returns it, before it has removed
// anything. A window that is not this caller's to close is the end of it —
// there is nothing to type that makes it so, and closing this one is not what
// was asked.
func (r *closeRefusal) rmError(env *Env, cfg *config.Config, label, name string) error {
	instead := r.instead(env, cfg, name)
	if r.caller.kind != kindBase {
		instead = ""
	}
	return fmt.Errorf("%s\n%s\nnothing was removed%s", r.what(label), r.rule(), instead)
}

// leftOpen is the refusal as rm's and prune's question say it, after the
// removal: the worktree is gone, and the window it would have offered to close
// stays. label names the window and name is what reaches it.
func (r *closeRefusal) leftOpen(env *Env, cfg *config.Config, label, name string) string {
	if r.reason == notYielded {
		return "it stays open, because its agent has not yielded it" +
			"\nan agent that cannot answer leaves its window to a person, outside treewright's windows" +
			asFields(
				field("ask it", askToYield(env, cfg, r.target, name)),
				field("then close it with", closeHint(env, cfg, name)),
			)
	}
	return "it stays open\n" + r.what(label) + "\n" + r.rule()
}

// instead is what the caller can do in place of what was refused.
func (r *closeRefusal) instead(env *Env, cfg *config.Config, name string) string {
	switch {
	case r.reason == notYielded:
		return "\nan agent that cannot answer leaves its window to a person, outside treewright's windows" +
			"\nonce ls --json says window_yielded, run this again" +
			asFields(field("ask it", askToYield(env, cfg, r.target, name)))
	case r.caller.kind == kindBase:
		return "\nquit its agent, or close it from a shell outside treewright's windows"
	}
	return closeThisOne(env)
}

// closeThisOne names the bare close, as a field. No --repo, unlike every other
// hint: the bare form names nothing, the pane being what it closes.
func closeThisOne(env *Env) string {
	return asFields(field("close this one", env.copyable(env.Argv0+" close")))
}

// yieldQuestion is what a window's agent is asked before the base window
// closes it.
const yieldQuestion = "your window is about to be closed: if nothing in it would be lost, run treewright yield"

// askToYield is how a refusal says to ask a window's agent for its yield, and
// the one place that route is spelled: the agent's own messaging where it has a
// name and its module a tool for it, and `send` everywhere else, as
// messageRoute decides it and for its reasons. Neither carries a reply back to
// the binary — a reply goes to the agent that asked, and `send` has none — which
// is why the answer is a mark on the window rather than a reply.
//
// w is the window being asked about, whose stamp is where the agent's name is
// read, and name is what `send` takes for it.
func askToYield(env *Env, cfg *config.Config, w tmux.Window, name string) string {
	if tool, agent, ok := nativeRoute(cfg, w); ok {
		return env.copyable(tool) + " to " + env.copyable(agent) + `: "` + yieldQuestion + `"`
	}
	return hint(env, cfg, "send", name, shellinit.Quote(yieldQuestion))
}

// ---- yield -----------------------------------------------------------------

// cmdYield is an agent saying its own window may be closed from the base
// window: nothing it is doing would be lost.
//
// treewright cannot read the answer to a question. A reply to a message goes to
// the agent that asked, never to the binary, and `send` has no reply at all —
// so the answer has to be something the binary can check, put where only the
// agent answering can put it. This is that: a mark on the window it is running
// in, found from the pane, so no agent can yield a window but its own.
//
// It lasts only until the agent takes on more work: `signal working`, which is
// every prompt the agent is handed, takes it back. That makes the question
// itself, which arrives as a prompt, clear any older yield — so a mark on the
// window is always the answer to the latest one — and an agent that dies takes
// the mark with it, the held-open wrapper clearing it as it does the state.
//
// Unlike signal, this is not a hook's command: an agent runs it on purpose, so
// being run where it means nothing is said, and is an error.
func cmdYield(env *Env, args []string) error {
	if _, err := parseArgs("yield", args, nil, nil, 0); err != nil {
		return err
	}
	caller := callerOf()
	switch caller.kind {
	case kindNone:
		return fmt.Errorf("this is not running in a treewright window, so there is nothing to yield\nyield marks the worktree or scratch window it is run in")
	case kindBase:
		return fmt.Errorf("%s is the main checkout's window, which is never closed from inside treewright's windows\nthere is nothing to yield", caller.window.Name)
	case kindWorktree, kindScratch:
		if err := tmux.SetYielded(caller.window.ID, true); err != nil {
			return err
		}
	}
	env.progressf("yielded %s\nthe base window may close it until this agent is handed more work", caller.window.Name)
	return nil
}
