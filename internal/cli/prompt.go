package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The kickoff prompt: `new` and `resume` take --prompt, and the text lands
// where the command template says with {prompt}. The placeholder is the whole
// of the agent-neutral mechanism — treewright never guesses where an agent's
// CLI wants its prompt, the template says — and the defaults carry it, so
// `treewright new eng-1 --prompt "fix the rounding"` works out of the box.

// promptPlaceholder is where a command template takes the prompt's text.
const promptPlaceholder = "{prompt}"

// How the two prompt flags are spelled, parsed and documented, in one place.
//
// Three commands hand an agent its instructions — new, move and resume — and
// the flags mean the same thing on each. Spelled out at each of them, they were
// three chances to misspell a flag name and three copies of one sentence to
// drift apart. What stays per command is the half that genuinely differs: what
// the prompt is for there, which resume answers differently from the other two.
const (
	promptFlag      = "--prompt"
	promptFileFlag  = "--prompt-file"
	promptFlagNames = "-p, " + promptFlag
)

// promptFileDoc documents --prompt-file wherever it appears. It is one flag
// with one behavior, so it is one sentence.
var promptFileDoc = flagDoc{promptFileFlag, "a file holding the brief; the prompt becomes one line naming it"}

// promptValues is the flag pair as parseArgs takes it, pointed at a command's
// own two variables.
func promptValues(prompt, promptFile *string) map[string]*string {
	return map[string]*string{"-p": prompt, promptFlag: prompt, promptFileFlag: promptFile}
}

// promptPointer is the prompt --prompt-file builds: one line naming the file,
// and none of the file's own text.
//
// Spelled once, here, because it is the whole of what --prompt-file adds. The
// wording matters more than it looks: the agent receiving it has to understand
// that the file is the instruction rather than a reference to consult later,
// and "in full" is what stops a reader skimming the first heading and starting
// work. It was taught as a sentence to type by hand before it was a flag, and
// this is that sentence.
const promptPointer = "read %s in full — it is your complete brief"

// resolvePrompt settles what the agent is told, from --prompt or --prompt-file.
//
// The two are one setting with two ways to fill it, and passing both is a wrong
// invocation rather than a precedence rule to learn. The file is checked here,
// where fillPrompt's refusals are: before anything is created, so a path that
// is missing, a directory, or empty is this invocation being wrong rather than
// a half-made worktree behind an error about a flag.
//
// The path is made absolute because it travels in the agent's command line,
// which runs in the new worktree — a relative path resolved there names a file
// that is not in it. treewright neither copies the file nor deletes it: it has
// to outlive the command, and clearing it up once the work has landed stays the
// caller's.
//
// The line it builds is short whatever the file holds, so a brief of any size
// sidesteps the command-length ceiling checkCommandFits guards. That is a
// consequence rather than the point — a file can be re-read after a compaction,
// and it outlives the session that wrote it.
func resolvePrompt(cmd, prompt, promptFile string) (string, error) {
	if promptFile == "" {
		return prompt, nil
	}
	if prompt != "" {
		return "", usageErrorf(cmd, "--prompt and --prompt-file are two ways to fill one setting\npass whichever of them holds the instructions")
	}

	path, err := filepath.Abs(promptFile)
	if err != nil {
		return "", fmt.Errorf("could not resolve --prompt-file %s: %w", promptFile, err)
	}
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return "", fmt.Errorf("no file at %s\nthe prompt is one line telling the agent to read it, so nothing was created", path)
	case err != nil:
		// The file may well be there — unreadable is not missing — and "no file"
		// would send the reader to re-create one that exists.
		return "", fmt.Errorf("could not read --prompt-file %s: %w\nnothing was created", path, err)
	case info.IsDir():
		return "", fmt.Errorf("%s is a directory, not a brief\nthe prompt is one line telling the agent to read it, so nothing was created", path)
	case info.Size() == 0:
		return "", fmt.Errorf("%s is empty\nan agent sent to read it would have nothing to go on, so nothing was created", path)
	}
	return fmt.Sprintf(promptPointer, path), nil
}

// fillPrompt resolves a command template against the prompt the user gave, and
// refuses one the result is too long for tmux to run.
//
// key names the config setting the template came from — command or
// resume_command — since that is the line either error should send the reader
// to.
//
// Both refusals belong here, at the one point every caller passes through
// before it has created anything. A prompt the template cannot take and a
// prompt too long to run are the same kind of mistake — this invocation being
// wrong — and finding either out afterwards leaves a half-made worktree behind
// an error about a flag.
func fillPrompt(command, key, prompt string) (string, error) {
	filled, err := fillTemplate(command, key, prompt)
	if err != nil {
		return "", err
	}
	if err := checkCommandFits(filled, key, prompt); err != nil {
		return "", err
	}
	return filled, nil
}

// fillTemplate substitutes the prompt into the template. Three cases, each
// deliberate:
//
// With a prompt, the placeholder becomes the shell-quoted text: one literal
// argument however many spaces and quotes the prompt holds, through the same
// shellQuote the eval file trusts.
//
// Without one, the placeholder is removed entirely — never substituted as ”.
// An empty argument is not the absence of an argument: to most agents it is an
// instruction, an empty one, and `claude ”` opening every window with a blank
// prompt to answer is the bug this line avoids.
//
// A prompt aimed at a template with no placeholder is refused rather than
// appended. Appending happens to be right for claude and is still a guess
// about an arbitrary agent's CLI; the error names the setting and shows where
// the text should go, which turns the guess into the user's own line.
func fillTemplate(command, key, prompt string) (string, error) {
	if !strings.Contains(command, promptPlaceholder) {
		if prompt == "" {
			return command, nil
		}
		return "", fmt.Errorf("%s has no %s placeholder to take the prompt\nwrite one where the text belongs, e.g. %s = \"claude %s\"",
			key, promptPlaceholder, key, promptPlaceholder)
	}
	if prompt == "" {
		// The adjacent space goes with the placeholder, so "claude {prompt}"
		// becomes "claude" rather than "claude " — targeted, rather than a
		// whitespace collapse that would reach inside the template's own
		// quoted arguments.
		command = strings.ReplaceAll(command, " "+promptPlaceholder, "")
		command = strings.ReplaceAll(command, promptPlaceholder+" ", "")
		command = strings.ReplaceAll(command, promptPlaceholder, "")
		return strings.TrimSpace(command), nil
	}
	return strings.ReplaceAll(command, promptPlaceholder, shellQuote(prompt)), nil
}
