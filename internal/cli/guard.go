package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/jay-snyder/treewright/internal/config"
	"github.com/jay-snyder/treewright/internal/git"
	"github.com/jay-snyder/treewright/internal/tmux"
)

// `guard` is the handoff rule made mechanical.
//
// The rule is that a worktree's work belongs to the agent in that worktree's
// window: you hand it over with --prompt, you do not carry it out from the
// window you were already in. It is the most-revised paragraph in the claude
// module's skill and it has failed every time, not through ignorance but
// through rationalization — an agent reads it, finds a reading of its own case
// that the prose does not rule on, and proceeds. Prose is interpreted, and
// interpretation is where the leak is. So the plugin runs this from a
// PreToolUse hook, which fires whether or not the agent has talked itself into
// something.
//
// What it refuses is one sentence: a tool call that **mutates a treewright
// worktree other than the one the caller is standing in**. Reads of any
// worktree go through untouched — reading another agent's work to review it is
// legitimate and has to stay cheap — and so does everything treewright's own
// commands do, since `close`, `rm`, `resume` and `send` name worktree paths and
// slugs constantly and refusing those would be refusing the way out the message
// names.
//
// It inherits `signal`'s discipline, for the same reason and one more.
// **Silent and permissive out of scope**: outside tmux, outside a registered
// repository, outside git, on a payload it cannot parse, or on a tool it does
// not know — exit 0, print nothing, allow the call. Agent hooks fire in every
// session the agent has, and a guard that failed closed on an unregistered
// repository would turn every unrelated session into a fight. The extra reason
// is that this one can *block*: an over-eager refusal is not a nag, it is work
// the person has to argue their agent past.
//
// The protocol is the exit code. A PreToolUse hook blocks on 2 and on nothing
// else, and hands that run's stderr back to the agent as the reason; every
// other non-zero exit is a note in the transcript with the call allowed
// through. So the refusal is stderr plus ErrRefused, and the ordinary treewright
// message writers do the rest — the reader of this message is an agent, but the
// person reading the transcript afterwards is the maintainer, and it should
// read like every other thing treewright says.
//
// One consequence of that protocol is deliberately outside the "Adding a
// subcommand" checklist: **`guard` never returns a usage error**. ErrUsage is
// also exit 2, so a mis-wired hook would refuse every tool call in the session
// and hand back this command's help text as its reason. Being invoked wrong is
// therefore one more way of being out of scope.

// guardedTools are the tool names the guard judges, and the whole of what the
// plugin's PreToolUse matcher fires on.
//
// A closed list rather than "anything with a file_path", because the matcher
// and this have to mean the same thing in both directions: widened to `*`, a
// name-blind rule would refuse Read on a path it is perfectly fine to read.
// TestTheGuardAndItsMatcherAgree holds the two together.
//
// NotebookEdit is here for the same reason Edit and Write are, and it carries
// its path under a different key — see hookCall.
var guardedTools = []string{"Bash", "Edit", "Write", "NotebookEdit"}

// hookCall is the part of a PreToolUse payload the guard reads: which tool, the
// directory the agent is standing in, and enough of the tool's own input to say
// what it would touch.
//
// Everything else in the payload is ignored by omission, which is what keeps
// this from breaking when the shape around it grows — including tool_input's
// own bulk, since a Write's `content` can be megabytes and none of it is a
// path.
//
// hook_event_name is deliberately not checked. Requiring it would mean a
// renamed event value silently switches the guard off, and silently not
// guarding is the failure this exists to end; the tool name and the plugin's
// matcher are the scope instead.
type hookCall struct {
	ToolName string `json:"tool_name"`

	// Cwd is where the agent is standing, as the payload reports it. It is
	// preferred over the process's own working directory because a hook is a
	// child process whose directory is nobody's promise, and because a test can
	// then drive the decision without moving the whole test binary.
	Cwd string `json:"cwd"`

	Input struct {
		Command      string `json:"command"`       // Bash
		FilePath     string `json:"file_path"`     // Edit, Write
		NotebookPath string `json:"notebook_path"` // NotebookEdit
	} `json:"tool_input"`
}

// cmdGuard refuses a tool call that would mutate another worktree.
func cmdGuard(env *Env, args []string) error {
	// Not parseArgs: see the note on usage errors above. An argument this
	// command does not understand is a hook wired wrong, and the safe answer to
	// that is the same silence every other out-of-scope case gets.
	if len(args) > 0 {
		return nil
	}
	call, ok := readHookCall(env)
	if !ok {
		return nil
	}
	if !slices.Contains(guardedTools, call.ToolName) {
		return nil
	}
	scope, ok := guardScopeFor(call.Cwd)
	if !ok {
		return nil
	}
	found, ok := scope.reaches(call)
	if !ok {
		return nil
	}
	env.errorf("%s", found.refusal(env))
	return ErrRefused
}

// readHookCall decodes the payload, reporting ok=false for every way there is
// not one to read.
//
// The terminal check is the one that matters to a person: typed at a prompt
// rather than run by a hook, a decoder on os.Stdin would sit there waiting for
// input nobody intends to give, and a command that hangs is worse than one that
// refuses. Decoding rather than reading first is what keeps a large tool_input
// from being buffered whole for the sake of three fields.
func readHookCall(env *Env) (hookCall, bool) {
	if f, ok := env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return hookCall{}, false
	}
	var call hookCall
	if err := json.NewDecoder(env.Stdin).Decode(&call); err != nil {
		return hookCall{}, false
	}
	return call, true
}

// ---- scope -------------------------------------------------------------------

// guardScope is what the guard needs to know about where it is: the checkout
// the caller stands in, and the worktrees of that repository which are not it.
type guardScope struct {
	here    string            // git's spelling of the caller's own checkout
	foreign map[string]string // worktree directory → slug, the caller's own excluded
}

// guardScopeFor works out where the calling agent is standing, reporting
// ok=false for each way the question has no answer.
//
// tmux is required because a treewright worktree's work belongs to the agent in
// its *window*: with no tmux there are no windows, no agents to hand anything
// to, and so no rule to enforce. The registered-repository and git checks are
// the other two halves of "this is treewright's business".
//
// Paths are compared as git spells them. Both sides come from git — the
// caller's checkout from rev-parse, the worktrees from worktree list — and git
// always reports fully resolved paths, so nothing here has to resolve symlinks
// to make the two comparable.
func guardScopeFor(cwd string) (guardScope, bool) {
	if !tmux.Available() {
		return guardScope{}, false
	}
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return guardScope{}, false
		}
		cwd = wd
	}
	repo := git.Repo{Dir: cwd}
	here, err := repo.TopLevel()
	if err != nil {
		return guardScope{}, false
	}
	main, err := repo.MainDir()
	if err != nil {
		return guardScope{}, false
	}
	cfg, err := config.Resolve("", main)
	if err != nil {
		return guardScope{}, false
	}
	managed, err := repoFor(cfg).Managed()
	if err != nil {
		return guardScope{}, false
	}
	scope := guardScope{here: here, foreign: map[string]string{}}
	for _, wt := range managed {
		if wt.Dir == here {
			continue
		}
		scope.foreign[wt.Dir] = wt.Slug
	}
	if len(scope.foreign) == 0 {
		return guardScope{}, false
	}
	return scope, true
}

// owns reports the foreign worktree a path lies in.
//
// Compared a component at a time rather than as a string prefix, because
// treewright's worktrees are siblings of the main checkout named after it:
// "/src/proj-eng-1" has "/src/proj" as a string prefix and is not inside it,
// and neither is "/src/proj-eng-12" inside "/src/proj-eng-1".
func (s guardScope) owns(path string) (slug, dir string, ok bool) {
	for wt, slug := range s.foreign {
		if path == wt || strings.HasPrefix(path, wt+string(filepath.Separator)) {
			return slug, wt, true
		}
	}
	return "", "", false
}

// resolve turns a path as the command spelled it into one comparable with a
// worktree directory, relative to wherever the command would run.
func resolve(path, base string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

// ---- the decision ------------------------------------------------------------

// guardFinding is a foreign worktree a call reaches into, and how the call
// spelled it.
type guardFinding struct {
	slug string
	dir  string
	what string // the tool call as the message names it back
}

// reaches decides a tool call.
func (s guardScope) reaches(call hookCall) (guardFinding, bool) {
	if call.ToolName == "Bash" {
		return s.reachesInCommand(call.Input.Command)
	}
	path := call.Input.FilePath
	if path == "" {
		path = call.Input.NotebookPath
	}
	if path == "" {
		return guardFinding{}, false
	}
	slug, dir, ok := s.owns(resolve(path, s.here))
	if !ok {
		return guardFinding{}, false
	}
	return guardFinding{slug: slug, dir: dir, what: call.ToolName + " " + path}, true
}

// reachesInCommand walks a shell line looking for a mutation aimed at a foreign
// worktree.
//
// A segment mutates one of two ways: it runs something that is not on the
// read-only list, or it redirects output into the worktree whatever it runs —
// `cat notes > <other>/x` is cat, and it is not a read. It reaches the worktree
// one of three: it runs there (a `cd` earlier in the line, or git's own -C), it
// names a path there, or it writes to a path there.
//
// The directory a `cd` establishes carries to every later segment and is not
// undone at a subshell's closing paren. Tracking subshell scope would be the
// exact version; over-reaching by a segment or two is the safe direction, and
// the message says how to proceed either way.
func (s guardScope) reachesInCommand(command string) (guardFinding, bool) {
	found := func(slug, dir string) (guardFinding, bool) {
		return guardFinding{slug: slug, dir: dir, what: abbreviatedCall(command)}, true
	}

	at := s.here
	for _, seg := range scanCommand(command) {
		program, args := programOf(seg.words)
		if program == "" {
			continue
		}
		// treewright's own commands are never the thing being refused. They name
		// worktree paths and slugs constantly — and the two commands this
		// message tells the reader to run are among them.
		if program == canonicalName || program == "tw" {
			continue
		}
		if program == "cd" || program == "pushd" {
			if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
				at = resolve(args[0], at)
			}
			continue
		}

		// git's -C moves that one command without moving the line.
		where := at
		if program == "git" {
			if c := valueOfFlag(args, "-C"); c != "" {
				where = resolve(c, at)
			}
		}

		mutates := !readsOnly(program, args)
		for _, target := range seg.redirects {
			if _, _, ok := s.owns(resolve(target, at)); ok {
				mutates = true
			}
		}
		if !mutates {
			continue
		}

		if slug, dir, ok := s.owns(where); ok {
			return found(slug, dir)
		}
		for _, word := range seg.words {
			for _, candidate := range pathsIn(word) {
				if slug, dir, ok := s.owns(resolve(candidate, at)); ok {
					return found(slug, dir)
				}
			}
		}
		for _, target := range seg.redirects {
			if slug, dir, ok := s.owns(resolve(target, at)); ok {
				return found(slug, dir)
			}
		}
	}
	return guardFinding{}, false
}

// maxRefusedCall caps the copy of a tool call the refusal names, in runes.
//
// Wider than the held-open wrapper's `abbreviated`, because the two lines pay
// for different things. That one is shell-quoted into a script tmux measures,
// so its budget is bytes on the wire and eighty of them is generous; this one
// is only ever read, so what it costs is a line and a half of terminal, next to
// a `worktree` field already free to run as long as the path does.
const maxRefusedCall = 160

// abbreviatedCall names a tool call in one field, cutting the middle out of one
// too long to print.
//
// The middle rather than the tail, and that is the whole of the decision. A
// tail cut spends its budget front-first, which on `git -C <path> commit` is
// the directory — the same directory the message names again on the very next
// line — so a deep enough checkout printed the path twice and the verb never.
// macOS found it before a person did: a temp directory there is a hundred and
// fifteen characters before the command has said anything. Keeping both ends
// keeps the program and what it was going to do, at any path length.
func abbreviatedCall(call string) string {
	first, rest, _ := strings.Cut(call, "\n")
	kept := []rune(first)
	switch {
	case len(kept) <= maxRefusedCall && strings.TrimSpace(rest) == "":
		return first
	case len(kept) <= maxRefusedCall:
		// One column rather than three periods, as a window name shortened for
		// the status line is marked.
		return first + " …"
	}
	// Two thirds from the front: the program and its flags are what identify the
	// call, where the tail only has to reach back past the last argument.
	head := maxRefusedCall * 2 / 3
	return string(kept[:head]) + "…" + string(kept[len(kept)-(maxRefusedCall-head):])
}

// pathsIn pulls the path-shaped parts out of one word: the word itself, and
// whatever follows the first "=" — so that --output=<path> is seen as well as
// --output <path>.
func pathsIn(word string) []string {
	if _, value, ok := strings.Cut(word, "="); ok && value != "" {
		return []string{word, value}
	}
	return []string{word}
}

// valueOfFlag finds the argument a flag carries, in either spelling git accepts.
func valueOfFlag(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag) && len(a) > len(flag) {
			return strings.TrimPrefix(a, flag)
		}
	}
	return ""
}

// ---- what counts as reading ---------------------------------------------------

// The read-only lists are closed, and everything absent from them mutates. That
// is the conservative direction, and it costs little: nothing here is consulted
// until a command has already been found reaching into somebody else's
// worktree, where the question is not "is this safe" but "is this a review".
//
// The lists are deliberately short of the commands that read in one form and
// write in another. `git branch` lists branches and also deletes them; `git
// remote` prints them and also removes them. Splitting those on their flags
// would put a second, subtler parser here for a question already answered
// elsewhere: `treewright ls --json` reports every worktree's branch, status and
// divergence without touching the worktree at all, so a review that wants those
// facts has a command that is never refused.

// words turns a space-separated list into a set. The lists below are lists of
// command names and nothing else, and written out as one string each they can
// be read down in a glance — where forty `"name": true,` pairs is a shape to
// look past before the names arrive.
func words(list string) map[string]bool {
	set := map[string]bool{}
	for w := range strings.FieldsSeq(list) {
		set[w] = true
	}
	return set
}

// readingPrograms only ever read what they are pointed at.
var readingPrograms = words(`
	cat head tail less more bat nl tac wc strings od xxd hexdump
	grep egrep fgrep rg ag ack
	ls tree find fd stat file du df realpath readlink basename dirname pwd
	echo printf test [ true false
	diff cmp comm sort uniq cut tr column jq yq awk
	md5sum sha1sum sha256sum shasum which type date wait
`)

// readingGit are git's read-only subcommands.
var readingGit = words(`
	log status diff show blame annotate
	ls-files ls-tree cat-file rev-parse rev-list describe shortlog
	for-each-ref merge-base name-rev cherry whatchanged grep count-objects
	range-diff diff-tree diff-files diff-index
	check-ignore check-ref-format verify-commit verify-tag var version help
`)

// readingGh are the gh verbs that only look: `gh pr view`, `gh run list`. The
// verb is the second word, gh's own shape being <group> <verb>.
var readingGh = words(`view list diff status checks browse search`)

// gitValueFlags are git's global flags that swallow the next argument, so the
// subcommand can be found past them.
var gitValueFlags = words(`-C -c --git-dir --work-tree --namespace --exec-path`)

// commandWrappers run another program, and it is that program being judged.
// Their own flags are skipped past on the way — along with a bare number, which
// is how `timeout` and `nice` take theirs.
var commandWrappers = words(`
	sudo env command exec builtin nohup nice time timeout stdbuf xargs
`)

// readsOnly reports a command that cannot change anything it is pointed at.
func readsOnly(program string, args []string) bool {
	switch program {
	case "git":
		return readingGit[gitSubcommand(args)]
	case "gh":
		return readingGh[ghVerb(args)]
	case "sed":
		// The one filter on the reading list that can be told to write, and
		// -i is how: bare, with a backup suffix stuck to it, or spelled out.
		for _, a := range args {
			if a == "--in-place" || strings.HasPrefix(a, "--in-place=") ||
				(strings.HasPrefix(a, "-i") && !strings.HasPrefix(a, "--")) {
				return false
			}
		}
		return true
	}
	return readingPrograms[program]
}

// programOf finds the program a segment runs and the arguments it runs with,
// looking past the environment assignments and wrappers in front of it.
func programOf(words []string) (string, []string) {
	for i := 0; i < len(words); i++ {
		word := words[i]
		if word == "" || isAssignment(word) {
			continue
		}
		name := filepath.Base(word)
		if commandWrappers[name] {
			// The wrapper's own arguments: flags, and the bare number a
			// timeout takes. Anything else is the program it runs.
			for i+1 < len(words) && (strings.HasPrefix(words[i+1], "-") || isNumber(words[i+1])) {
				i++
			}
			continue
		}
		return name, words[i+1:]
	}
	return "", nil
}

// gitSubcommand is the first word of a git command line that is not one of
// git's own global flags.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a
		}
		if gitValueFlags[a] {
			i++
		}
	}
	return ""
}

// ghVerb is the second non-flag word of a gh command line: the verb, gh's own
// shape being `gh <group> <verb>`.
func ghVerb(args []string) string {
	seen := 0
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		seen++
		if seen == 2 {
			return a
		}
	}
	// `gh` with nothing to do prints its help, which reads nothing and changes
	// nothing.
	return "view"
}

// isAssignment reports the NAME=value form that puts a variable in a command's
// environment rather than naming the command.
func isAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		digit := i > 0 && r >= '0' && r <= '9'
		if !letter && !digit {
			return false
		}
	}
	return true
}

func isNumber(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---- reading a shell line ------------------------------------------------------

// shellSegment is one simple command out of a shell line: the words it is made
// of, quotes removed, and the paths it sends output into.
//
// This is a reader rather than a shell, and the difference is the whole design.
// It has to answer two questions — what is being run, and what does it name —
// closely enough that a review is not refused and a commit is. It does not have
// to expand a variable, resolve a glob, or run anything, and everything it
// cannot answer it answers in the safe direction: an unrecognized program
// mutates.
type shellSegment struct {
	words     []string
	redirects []string
}

// scanCommand splits a shell line into segments at the operators that separate
// commands, and pulls out where each one redirects its output.
//
// Command substitution is split on rather than parsed: `$(git -C x log)` breaks
// at the paren, leaving the inner command as a segment of its own, which is
// exactly the one worth judging. Heredoc bodies are skipped whole, so prose
// inside one is never read as a command line.
func scanCommand(command string) []shellSegment {
	var (
		segments []shellSegment
		current  shellSegment
		word     strings.Builder
		inWord   bool

		// Set when the last operator was a ">": the next word is a file being
		// written rather than an argument.
		pendingRedirect bool
		// Set between a "<<" and its delimiter word, and then until the body
		// has been skipped.
		wantDelimiter bool
		delimiter     string
		inHeredoc     bool
	)

	flushWord := func() {
		if !inWord {
			return
		}
		w := word.String()
		word.Reset()
		inWord = false
		switch {
		case wantDelimiter:
			delimiter, wantDelimiter, inHeredoc = w, false, true
		case pendingRedirect:
			current.redirects = append(current.redirects, w)
			pendingRedirect = false
		default:
			current.words = append(current.words, w)
		}
	}
	flushSegment := func() {
		flushWord()
		if len(current.words) > 0 || len(current.redirects) > 0 {
			segments = append(segments, current)
		}
		current = shellSegment{}
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\\' && i+1 < len(runes):
			i++
			word.WriteRune(runes[i])
			inWord = true
		case r == '\'' || r == '"':
			i = copyQuoted(runes, i, &word)
			inWord = true
		case r == '\n':
			flushSegment()
			if inHeredoc {
				i = skipHeredoc(runes, i, delimiter)
				inHeredoc, delimiter = false, ""
			}
		case r == '&' && i+1 < len(runes) && runes[i+1] == '>':
			// `&>file` and `&>>file` send both streams to one place. Caught
			// before the separator below, which would otherwise cut the segment
			// away from its own redirect.
			flushWord()
			i++
			for i+1 < len(runes) && runes[i+1] == '>' {
				i++
			}
			pendingRedirect = true
		case strings.ContainsRune(";&|`()", r):
			flushSegment()
		case r == ' ' || r == '\t' || r == '\r':
			flushWord()
		case r == '<' || r == '>':
			// A leading file descriptor belongs to the operator rather than to
			// the word before it: the 2 of `2>` is not an argument.
			if inWord && isNumber(word.String()) {
				word.Reset()
				inWord = false
			}
			flushWord()
			var operator string
			operator, i = readRedirect(runes, i)
			switch {
			case strings.HasPrefix(operator, "<<") && !strings.HasPrefix(operator, "<<<"):
				wantDelimiter = true
			case strings.HasPrefix(operator, ">") && !strings.Contains(operator, "&"):
				// A ">&" duplicates a descriptor and names no file.
				pendingRedirect = true
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	flushSegment()
	return segments
}

// copyQuoted copies a quoted run into word and returns the index of its closing
// quote — or of the end of the line, an unterminated quote being one more thing
// to read rather than an error to report. Single quotes take everything
// literally; double quotes let a backslash through to the character after it.
func copyQuoted(runes []rune, start int, word *strings.Builder) int {
	quote := runes[start]
	for i := start + 1; i < len(runes); i++ {
		if quote == '"' && runes[i] == '\\' && i+1 < len(runes) {
			i++
			word.WriteRune(runes[i])
			continue
		}
		if runes[i] == quote {
			return i
		}
		word.WriteRune(runes[i])
	}
	return len(runes) - 1
}

// readRedirect consumes a redirection operator whole — the run of angle
// brackets, and the descriptor a ">&2" duplicates — and returns it with the
// index of its last character.
func readRedirect(runes []rune, start int) (string, int) {
	i := start
	var b strings.Builder
	for i < len(runes) && (runes[i] == '<' || runes[i] == '>') {
		b.WriteRune(runes[i])
		i++
	}
	if i < len(runes) && runes[i] == '&' {
		b.WriteRune(runes[i])
		i++
		for i < len(runes) && (isNumber(string(runes[i])) || runes[i] == '-') {
			b.WriteRune(runes[i])
			i++
		}
	}
	return b.String(), i - 1
}

// skipHeredoc runs past a heredoc's body from the newline that opens it,
// returning the index to carry on from. The body is text a program is being fed,
// not a command line, and reading it as one would find commands nobody ran.
func skipHeredoc(runes []rune, newline int, delimiter string) int {
	if delimiter == "" {
		return newline
	}
	i := newline + 1
	for i <= len(runes) {
		end := i
		for end < len(runes) && runes[end] != '\n' {
			end++
		}
		if strings.TrimSpace(string(runes[i:end])) == delimiter {
			return end
		}
		if end >= len(runes) {
			return len(runes)
		}
		i = end + 1
	}
	return len(runes)
}

// ---- the refusal ----------------------------------------------------------------

// refusal is what the blocked agent is handed back, and it is most of the point
// of the hook: a refusal that only refuses teaches an agent to look for another
// way through, where one that names the fix ends the turn the way it should have
// gone.
//
// Three things in order — what is wrong, what was refused, what to type. The
// second line rules on the case the prose kept losing: work that is *finished*
// goes to that agent too, so "it only needed committing" is not an exception to
// find. The brief file is named rather than described because the instructions
// this session has are the whole reason a fresh agent cannot pick the work up,
// and a prompt short enough to type usually is not them.
func (f guardFinding) refusal(env *Env) string {
	brief := "/tmp/" + f.slug + "-brief.md"
	return "" +
		f.slug + " is another agent's worktree, and this is not its window" +
		asFields(
			field("refused", f.what),
			field("worktree", f.dir),
		) +
		"\nhand the work over instead, finished work included" +
		asFields(
			field("with a brief", env.copyable("treewright resume "+f.slug+" --prompt-file "+brief)),
			field("or one line", env.copyable(`treewright send `+f.slug+` "read `+brief+` in full"`)),
		) +
		"\nwrite that file first — a fresh agent has none of this session"
}
