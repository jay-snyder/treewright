// Package feature holds treewright's optional behaviors: the things it does on
// its own initiative, in a repository whose config asked for them by name.
//
// Everything else treewright does is asked for at the moment it happens. A
// command is typed, or an agent hook reports a transition that has just
// occurred and treewright writes it down. A feature is the other shape — work
// treewright starts because a config named it once, at a moment nobody was
// thinking about it — and that difference is the whole reason the list is
// opt-in. A tool that fetches and moves somebody's checkout is welcome exactly
// when they asked for it and alarming otherwise, so nothing here is on by
// default and nothing here ever becomes so.
//
// What lives in this package is only the vocabulary: the name a config writes,
// the moment it runs at, and the prose that explains it in the generated config
// and in `treewright config`. The behavior lives in internal/cli beside the
// command that runs it, because a feature is made of git calls and this package
// has to stay importable from internal/config — which validates the names, and
// would otherwise need a second copy of the list to validate them against.
//
// That is agentinit's split exactly: facts here, behavior there. What holds the
// two together is a test rather than a shared type, for the same reason the
// guard and its matcher are held together by one — the coupling is a list on
// each side, and a test that names the missing half is a better failure than a
// signature nobody can implement wrong.
package feature

import "slices"

// Moment is when treewright runs a feature.
//
// There is one, and what makes a second cheap is that this is a field rather
// than an assumption. A moment is a `treewright <verb>` an agent's hooks call,
// so adding one means a command here, a dispatcher in internal/cli, and one
// line in each agent module's hook wiring — three places a reader has to find,
// which they can only do if the feature says which moment it belongs to.
type Moment string

// AtSessionStart is the start of an agent session in a checkout, and the moment
// `treewright session-start` runs for.
//
// It is the safe moment for a feature that moves a checkout, and the only one
// currently on offer for that reason: the agent has done nothing yet, so there
// is no work in flight for the move to happen underneath. What guards that is
// the module's matcher rather than anything here — see plugins/claude/hooks.
const AtSessionStart Moment = "session-start"

// FreshBase is the one feature there is. Named as a constant because two
// packages spell it — this registry and the implementation in internal/cli —
// and a string literal in each is a rename waiting to half-happen.
const FreshBase = "fresh-base"

// Feature is one optional behavior.
type Feature struct {
	// Name is what a config's `features` list writes. Spelled like a command
	// and not like a setting — hyphens rather than underscores — because it is
	// a name drawn from a closed vocabulary the way an agent module's is, and
	// not a key of its own.
	Name string

	// Moment is when it runs.
	Moment Moment

	// Summary is the one line `treewright config` and `doctor` name it by, in
	// the message voice: lowercase, unpunctuated, and short enough for a table
	// cell, since both print it straight through.
	Summary string

	// Doc is what `setup` writes above the features key, one element per line,
	// already wrapped to the width the generated config uses.
	//
	// The generated config is where somebody decides whether to switch this on,
	// which makes it the one place the prose has to be complete: what it does,
	// what it will not do, and where it does nothing at all. A feature whose
	// documentation lives only in `help` is one nobody reads before enabling.
	Doc []string
}

// features is the registry, in the order the generated config explains them.
//
// A slice rather than a map, so that order is the author's rather than the
// runtime's: this list is printed into a file people read top to bottom, and a
// registry that reordered itself between runs would make every `setup
// --refresh` a diff.
var features = []Feature{
	{
		Name:    FreshBase,
		Moment:  AtSessionStart,
		Summary: "fast-forward the base checkout when a session starts in it",
		Doc: []string{
			"When an agent session starts in the base checkout with base_branch",
			"checked out, fetch origin and fast-forward. Fast-forward only: it",
			"never merges, never rebases, and a branch that has diverged is",
			"reported and left exactly as it is. A session in a worktree does",
			"not touch the base checkout, and a base checkout already current",
			"is a silence.",
		},
	},
}

// All lists the features in the registry's own order.
func All() []Feature { return slices.Clone(features) }

// At lists the features that run at one moment, in registry order — which is
// the order they run in, since nothing here declares a dependency on anything
// else and a stable order is what makes the output reproducible.
func At(m Moment) []Feature {
	var out []Feature
	for _, f := range features {
		if f.Moment == m {
			out = append(out, f)
		}
	}
	return out
}

// Lookup finds a feature by name.
func Lookup(name string) (Feature, bool) {
	for _, f := range features {
		if f.Name == name {
			return f, true
		}
	}
	return Feature{}, false
}

// Names lists the feature names, for errors and for the commented example a
// generated config carries. Registry order, not sorted: the error naming the
// built-ins and the file explaining them should list them the same way round.
func Names() []string {
	out := make([]string, 0, len(features))
	for _, f := range features {
		out = append(out, f.Name)
	}
	return out
}
