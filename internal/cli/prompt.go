package cli

import (
	"fmt"
	"strings"
)

// The kickoff prompt: `new` and `resume` take --prompt, and the text lands
// where the command template says with {prompt}. The placeholder is the whole
// of the agent-neutral mechanism — treewright never guesses where an agent's
// CLI wants its prompt, the template says — and the defaults carry it, so
// `treewright new eng-1 --prompt "fix the rounding"` works out of the box.

// promptPlaceholder is where a command template takes the prompt's text.
const promptPlaceholder = "{prompt}"

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
