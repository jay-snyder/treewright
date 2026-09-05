package cli

import (
	"maps"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
)

// Naming the repository a command acts on, rather than standing in it.
//
// Most commands used to resolve their repository from the working directory and
// offer no way to say otherwise. That is right for a person, who is nearly
// always standing in the checkout they mean, and wrong for an agent, which
// stands in exactly one repository and may have business in another. The
// failure it produced was silent: `new` run for another repository made a
// worktree with the standing repository's branch prefix, base branch and path,
// and nothing in the output said which repository had been acted on.
//
// The repository is named by --repo rather than by qualifying the target as
// <repo>/<slug>, and the reason is that a slash is already spoken for. `new`
// reads a leading "feature/" as a branch prefix, and `rm`, `close`, `send`,
// `resume` and `cd` all strip one; a qualified target would collide with that
// on every one of them. Worse, the collision would be silent and would move
// under people: registering a config named after somebody's branch prefix would
// change what `tw rm feature/eng-1` means in a repository whose config nobody
// touched — from "the eng-1 worktree here" to "the eng-1 worktree over there",
// which is a deletion. A flag cannot be confused with a prefix, a slug or a
// window name, and it leaves the prefix mechanism working on the slug:
// `tw new --repo cibo bug/eng-1` still branches bug/eng-1 in cibo.
//
// See "Naming the repository a command acts on" in docs/design-notes.md.

// repoFlag names the repository to act on. Spelled once because a dozen
// commands take it and every one of them must spell it the same: a flag that
// worked on some commands and not others would be one an agent learns not to
// trust.
const repoFlag = "--repo"

// repoFlagDoc documents it wherever it appears. One flag, one behavior, one
// sentence — as promptFileDoc is, and for the same reason.
var repoFlagDoc = flagDoc{repoFlag, "the repository to act on, by config name, rather than the one you are standing in"}

// repoValues is the flag as parseArgs takes it, merged with whatever other
// value flags the command has. Taking the others rather than being merged by
// each caller is what keeps a command from wiring up its prompt flags and
// quietly dropping this one.
func repoValues(repo *string, others map[string]*string) map[string]*string {
	values := map[string]*string{repoFlag: repo}
	maps.Copy(values, others)
	return values
}

// namedRepo settles which repository was named, from the flag and — on the
// commands that have one to spare — the [repo] positional.
//
// Both spellings stay, and they are not two ways of saying one thing so much as
// one general form and one shorthand. `ls`, `prune`, `base`, `attach`,
// `restore`, `config` and `refresh` take a repository as their only argument,
// where a positional reads better than a flag and has been the spelling since
// those commands existed; the flag is what the rest can take, their positionals
// being slugs. Accepting the flag everywhere is what makes it learnable as
// universal, which is the whole point: an agent that finds --repo rejected by
// one command has no reason to believe it works on the next.
//
// Naming it twice is refused rather than resolved by precedence, following
// --prompt and --prompt-file, and config's branch_prefix pair before them. A
// precedence rule here would be one more thing to know about a command line
// whose author has already said the same thing twice and may have meant two
// different things by it.
func namedRepo(env *Env, cmd, flag, positional string) (string, error) {
	if flag != "" && positional != "" {
		return "", usageErrorf(cmd, "the repository is named twice, as %s and as %s\nname it once",
			env.copyable(repoFlag+" "+flag), env.copyable(positional))
	}
	if flag != "" {
		return flag, nil
	}
	return positional, nil
}

// hint spells out a treewright command for a reader to type, with the
// repository named in it.
//
// Every hint names the repository, in every repository, and that is a decision
// rather than an oversight. A slug does not identify anything on its own: the
// same slug exists in two repositories the moment two pieces of work are called
// the same thing, which is what "eng-1" and "fix" are for. The reader of a
// message is not reliably standing where it was printed either — an agent has
// its own working directory, a message scrolls back into a transcript read from
// somewhere else, and `rm`'s hint about a stale window is read after the
// worktree it names has gone. In the incident that produced this, an operator
// with a hint that named only the slug had nothing to type and fell back to
// `tmux kill-window` on a window index.
//
// So the hint is longer everywhere and correct everywhere, which is the right
// trade for a line whose whole purpose is to be copied. Spelled with Argv0, so
// someone who typed `tw` is answered in the name they use.
func hint(env *Env, cfg *config.Config, command string, args ...string) string {
	parts := append([]string{env.Argv0, command, repoFlag, cfg.Name}, args...)
	return env.copyable(strings.Join(parts, " "))
}

// standingIn reports whether the caller is standing in the repository cfg
// describes.
//
// Asked by resolving the working directory the way every command resolves it,
// rather than by comparing paths here, so that "the repository you are standing
// in" means exactly what it means everywhere else — symlinks, worktrees and the
// single-config fallback included. A working directory in no registered
// repository is not standing in this one either, which is the answer that
// matters: a fresh terminal tab attached to somebody else's session is the case
// arrivalFor exists for.
func standingIn(cfg *config.Config) bool {
	here, err := resolveConfig("")
	return err == nil && here.Name == cfg.Name
}

// arrivalFor decides what a command that opens one window should do about the
// caller's tmux client.
//
// treewright keeps one session per repository, so a window opened in another
// repository is always in another session — and tmux.Focus, which follows a
// window across sessions, therefore always switches. From the seat of whoever
// is attached, that is their session being replaced by another repository's
// under their hands, with no way back but detach and reattach. It happened
// twice in one sitting to an operator watching one repository while an agent
// spawned work in another.
//
// So the client moves only when the repository acted on is the one the caller
// is standing in. Naming another repository is good evidence you did not mean
// to leave the one you are in, and a person who did mean to has `attach` and
// `restore`, whose whole answer is a session rather than a window. Naming your
// own repository is not the same request and does not suppress anything, which
// matters because the agent guide tells agents to pass --repo always: a flag
// that changed where you land depending on whether you bothered to name the
// repository you were already in would be a flag nobody could pass by default.
//
// Outside tmux none of this arises — there is no client to move, Focus skips
// the switch itself, and both paths print the way in — so this is only ever
// deciding between arriving somewhere and being told where the window is.
//
// It is not a flag. A flag would put the operator's client at the mercy of
// every caller remembering to pass it, and the callers likeliest to forget are
// the agents this exists to contain. See "Whose client is it" in docs/tmux.md.
func arrivalFor(cfg *config.Config, named string) arrival {
	if named == "" || standingIn(cfg) {
		return bringToFront
	}
	return stayHere
}

// warnIfWindowNameIsARepo says so when a window would be named after some other
// registered repository.
//
// Nothing breaks: treewright targets sessions by =name and windows by id, so a
// name collision is never a correctness problem. What it costs is the reading
// of the window list, which is the one place a person checks which repository
// they are looking at — a session is per-repository, so a window called "cibo"
// sitting in another repository's session says, to the only person who will
// ever read it, that cibo's work is here. That is what happened.
//
// A warning rather than a refusal, on warnIfAgentWorking's argument: the caller
// may have meant it, a refusal would need a --force to get past, and a --force
// is a flag people learn to pass by reflex. The repository's own name is exempt
// — a window named after the repository it is in misleads nobody.
func warnIfWindowNameIsARepo(env *Env, cfg *config.Config, name string) {
	if name == "" || name == cfg.Name {
		return
	}
	names, err := config.Names()
	if err != nil {
		return
	}
	for _, registered := range names {
		if registered != name {
			continue
		}
		env.warnf("the window would be called %s, which is a registered repository\n"+
			"in %s's session that reads as %s's window, and window names are all anyone has to go on\n"+
			"give it a name of its own with the window-name argument",
			name, cfg.Name, name)
		return
	}
}
