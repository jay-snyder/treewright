package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// The name an agent answers to.
//
// `send` reaches the agent in another window by typing at its pane, and every
// rule it enforces — look at the pane first, one line only, never a window held
// open on a dead agent's output — is a rule about keystrokes landing in a
// terminal whose state the sender cannot see. An agent with messaging of its own
// has none of those problems: a message arrives between its tool calls rather
// than through the keyboard, marked as coming from another session, so it cannot
// answer a question or a permission prompt, and a slash command in it is text.
// What it needs is an address, and the one an agent picks for itself when not
// given one is its directory plus a couple of random characters — which nobody
// can predict, so nobody can be handed it.
//
// So treewright names every agent it launches, through {name} in the command
// template, the way it hands one its prompt through {prompt}. The value is
// core's and the flag is the agent module's: `--name` is a fact about claude,
// and a template without {name} is an agent launched with no name, whatever it
// is. The name is recorded on the window as it opens, and that record is what
// everything else reads — `ls --json`'s agent_name, and the messages below that
// say how to reach an agent — because the config only says what the *next*
// agent will be called, and a window opened under an older treewright, or an
// older command, is running one that was called something else.
//
// The scheme is the repository's config name, then a hyphen and the slug or the
// scratch name — `cibo-eng-142`, `cibo-review-42` — and the config name alone for
// the base window. Names are kept exactly as given, so treewright can predict
// the address to the character; an exact name wins over a longer one it is a
// prefix of, so bare `cibo` is safe beside `cibo-eng-142`; and it is the shape
// the agent would have derived itself from a worktree's directory, minus the
// random tail. Within one repository two names never collide, since slugs and
// scratch names already share one namespace there. Across two they can, and
// refuseAgentNameTaken is why that is refused rather than lived with.

// agentName is the name treewright gives the agent in a worktree's window or a
// scratch window: the repository's config name, a hyphen, and the slug or the
// scratch name.
func agentName(cfg *config.Config, name string) string { return cfg.Name + "-" + name }

// baseAgentName is the name of the agent in the base window, which is the
// repository's own: the config name, with nothing after it.
//
// A function of its own rather than agentName given "base", because a worktree
// may be called base — it loses the short spelling to the checkout and keeps its
// own — and its agent is `cibo-base`, which is not the base window's.
func baseAgentName(cfg *config.Config) string { return cfg.Name }

// agentName is the name the agent for this choice is launched under.
func (c choice) agentName(cfg *config.Config) string {
	if c.Base {
		return baseAgentName(cfg)
	}
	return agentName(cfg, c.name())
}

// namedAs is the name an agent launched from these templates answers to: name,
// when every template that may run in the window names its agent, and "" when
// any of them does not.
//
// Every, because a window may run either of two — resume's command, and the
// fresh one behind it — and which of them ends up running is decided inside the
// window, after treewright has gone. Recording a name that only one of them gives
// would be an address that is right on some days, and a message sent to an
// address that names nobody is a message lost without a word.
func namedAs(name string, templates ...string) string {
	for _, t := range templates {
		if !strings.Contains(t, namePlaceholder) {
			return ""
		}
	}
	return name
}

// namesItsAgents reports that a repository's command templates give its agents
// names at all — either of them, since either is how one of its windows may
// come to answer to a name.
func namesItsAgents(cfg *config.Config) bool {
	return strings.Contains(cfg.Command, namePlaceholder) || strings.Contains(cfg.ResumeCommand, namePlaceholder)
}

// windowOf is the window open for a choice, or a window with no ID when none is:
// a scratch session's own, which it carries, and anything else's by the
// directory it stands on.
func windowOf(cfg *config.Config, target choice) tmux.Window {
	if target.Scratch {
		return target.Window
	}
	return tmux.Windows(sessionFor(cfg))[target.Dir]
}

// nativeRoute is the agent's own way to a window: the tool its module names for
// messaging another session, and the name the agent in that window answers to.
// ok is false where either is missing — an agent module with no such tool, no
// module at all, or an agent launched with no name — and then the way to it is
// `send`, typing at its window.
//
// Both halves are needed, and they come from different places for a reason. The
// tool is a fact about the agent, so it comes from the module the config names.
// The name is a fact about the window, so it comes from the stamp: a config that
// names its agents today may have windows open from before it did.
func nativeRoute(cfg *config.Config, w tmux.Window) (tool, agent string, ok bool) {
	tool, agent = cfg.MessageTool(), w.Agent
	return tool, agent, tool != "" && agent != ""
}

// messageRoute says how a message gets to the agent in w, for a message whose
// first line has said why it has to: the agent's own messaging and the name it
// answers to where there is both, and `send` everywhere else. name is what
// `send` takes for it — a slug, a scratch name, or base.
//
// The native route is put first because it is the safer of the two, not merely
// the newer. A message delivered by the agent's own messaging cannot be taken
// as the answer to whatever question the pane happens to be showing, which is
// the one hazard `send` can only reduce by printing the pane first. `send`
// stays, for a person and for an agent with no messaging of its own — and for
// an agent that was simply never given a name, since typing at its window
// reaches it all the same.
func messageRoute(env *Env, cfg *config.Config, w tmux.Window, name string) string {
	if tool, agent, ok := nativeRoute(cfg, w); ok {
		return "send it to the agent there with " + tool + asFields(field("to", env.copyable(agent)))
	}
	return "type it at the agent there" + asFields(field("with", hint(env, cfg, "send", name)+` "…"`))
}

// refuseAgentNameTaken refuses to give an agent a name an agent of another
// registered repository already answers to.
//
// Names are hyphen-joined, and a hyphen is a character both halves may hold, so
// two repositories can arrive at one name between them whenever one config is
// named after the other plus a hyphen: `api` with a worktree `v2-fix` and
// `api-v2` with a worktree `fix` are both `api-v2-fix`, and `api`'s worktree `v2`
// is `api-v2`, the name of that repository's base window. Two agents under one
// name do not fail closed — a message sent to it is delivered to one of them,
// chosen by the agent's own messaging and not by treewright, with at most a note
// saying so. So the collision is refused when the name is given out — at `new`,
// `move` and `scratch` — which is the rule that keeps a slug from colliding with
// a scratch name too: refused once, while choosing another name is still free,
// rather than detected at every send.
//
// What counts as taken is what the other repository will name an agent: its
// base window, which it always has, and each worktree and scratch session it
// has now, open or not, since a closed window's agent gets its name back when it
// is resumed. A repository whose templates give no names takes none, and one
// whose name and a hyphen does not begin this one cannot be colliding with it,
// so the registry is read once and nothing past it is asked of most configs.
//
// The separators that could never collide were the alternative. `/` is the one
// character barred from both a config name and a slug, and `cibo/eng-142` reads
// as a file mention in the agent's prompt — and is the `<repo>/<slug>` shape
// treewright has already ruled out of its own vocabulary. `:` is barred from
// slugs only, and `server:resource` is what MCP resources look like.
//
// The base window's own name is never refused, since there is no other name for
// it to take; a repository registered under `api-v2` beside an `api` that already
// has a `v2` worktree is the one collision this cannot stop, and `new` refuses
// every one after it.
func refuseAgentNameTaken(cfg *config.Config, name, noun string) error {
	if !namesItsAgents(cfg) {
		return nil
	}
	agent := agentName(cfg, name)
	// A registry that cannot be listed has nothing in it to collide with that
	// this could name, and the command's own config was read from it a moment
	// ago — so an error here is no reason to refuse, and none to report.
	names, _ := config.Names()
	for _, other := range names {
		if other == cfg.Name || (agent != other && !strings.HasPrefix(agent, other+"-")) {
			continue
		}
		// A config that will not load is a broken registry entry, which every
		// command naming it says so about. It is not this command's to report,
		// and it names no agents while it cannot be read.
		theirs, err := config.Load(filepath.Join(config.Dir(), other+".toml"))
		if err != nil || !namesItsAgents(theirs) {
			continue
		}
		if whose := agentAnswering(theirs, agent); whose != "" {
			return fmt.Errorf("the agent for %s would answer to %s\n"+
				"that is already the name of the agent in %s\n"+
				"a message sent to it could reach either of them, and nothing would say which\n"+
				"pick another %s", name, agent, whose, noun)
		}
	}
	return nil
}

// agentAnswering names what in cfg's repository has an agent answering to name
// — its base window, a worktree, or a scratch session — or "" for nothing.
func agentAnswering(cfg *config.Config, name string) string {
	if name == baseAgentName(cfg) {
		return cfg.Name + "'s base window"
	}
	rest, ok := strings.CutPrefix(name, cfg.Name+"-")
	if !ok {
		return ""
	}
	if managed, err := repoFor(cfg).Managed(); err == nil && slices.ContainsFunc(managed, func(wt git.Worktree) bool {
		return wt.Slug == rest
	}) {
		return cfg.Name + "'s worktree " + rest
	}
	if _, ok := scratchSessions(cfg)[rest]; ok {
		return cfg.Name + "'s scratch session " + rest
	}
	return ""
}
