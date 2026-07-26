// Package refname restates the parts of git's branch-name syntax that treewright
// checks before it builds a branch name out of a prefix and a slug.
//
// Restated rather than delegated to `git check-ref-format`, because the answer has
// to be one sentence naming the thing the user typed. git's own refusal arrives
// several steps later — by which point treewright has already announced what it
// was about to do — and describes ref syntax rather than the slug that was typed or
// the config key the bad value sits in.
//
// One package for both halves of the name, because the rules are one set and only
// the consequences differ. A slug becomes a directory as well as the tail of a
// branch, so it may not contain "/" at all; a prefix is the half that carries the
// namespaces, so what matters there are the per-component rules git applies on each
// side of a "/". Each check words its own refusals, since a bad slug leaves a stray
// directory behind while a bad prefix produces a branch git will not create.
//
// Every rule here was checked against `git check-ref-format` rather than read off
// its documentation, which is how the two surprises below turned up:
// "feature./eng-1" is legal, though it looks the worst of any of these, and
// "-x/eng-1" is legal to git yet refused here anyway — a branch named that way
// reads as a flag to every command that takes one.
package refname

import (
	"fmt"
	"strings"
)

// forbidden lists the characters git allows nowhere in a ref name: its revision
// syntax, its wildcards, and the backslash.
const forbidden = "~^:?*[\\"

// CheckSlug validates the part of a branch name that also becomes a directory.
func CheckSlug(slug string) error {
	// A slug containing "/" turns "<repo>-a/b" into a nested path: `new` silently
	// creates the intermediate "<repo>-a" directory and `rm` later removes only the
	// leaf, leaving it behind empty.
	if strings.Contains(slug, "/") {
		return fmt.Errorf("slug %q cannot contain %q — it would nest the worktree inside a stray directory", slug, "/")
	}
	if strings.ContainsAny(slug, " \t\n") {
		return fmt.Errorf("slug %q cannot contain whitespace — it becomes both a directory and a branch name", slug)
	}
	if r, bad := badChar(slug); bad {
		return fmt.Errorf("slug %q cannot contain %q — git does not allow it in a branch name", slug, r)
	}
	// The positional rules: a leading dash reads as a flag, and the others are
	// spellings git reserves.
	switch {
	case strings.HasPrefix(slug, "-"):
		return fmt.Errorf("slug %q cannot start with %q — it would read as a flag", slug, "-")
	case strings.HasPrefix(slug, "."), strings.HasSuffix(slug, "."):
		return fmt.Errorf("slug %q cannot start or end with %q — git does not allow it in a branch name", slug, ".")
	case strings.Contains(slug, ".."), strings.Contains(slug, "@{"), strings.HasSuffix(slug, ".lock"):
		return fmt.Errorf("slug %q is not usable as a branch name — git reserves %q, %q and a %q suffix", slug, "..", "@{", ".lock")
	}
	return nil
}

// CheckPrefix validates a branch prefix: the half of the name where the "/" a slug
// may not contain belongs.
//
// An empty prefix is a legitimate setting rather than a missing one — it prepends
// nothing — so it passes. A prefix need not end in "/" either: some teams write
// "feature-".
//
// The slug that will follow is checked separately, and is never empty by the time a
// branch is built, which is what makes the rules about the *end* of a ref — no
// trailing dot, no trailing ".lock" — the slug's business rather than this one's.
func CheckPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if strings.ContainsAny(prefix, " \t\n") {
		return fmt.Errorf("branch prefix %q cannot contain whitespace — git does not allow it in a branch name", prefix)
	}
	if r, bad := badChar(prefix); bad {
		return fmt.Errorf("branch prefix %q cannot contain %q — git does not allow it in a branch name", prefix, r)
	}
	switch {
	case strings.HasPrefix(prefix, "-"):
		return fmt.Errorf("branch prefix %q cannot start with %q — the branch would read as a flag", prefix, "-")
	case strings.HasPrefix(prefix, "/"):
		return fmt.Errorf("branch prefix %q cannot start with %q — so would the branch name", prefix, "/")
	case strings.Contains(prefix, "//"):
		return fmt.Errorf("branch prefix %q cannot contain %q — git does not allow an empty path component", prefix, "//")
	case strings.Contains(prefix, ".."), strings.Contains(prefix, "@{"):
		return fmt.Errorf("branch prefix %q is not usable in a branch name — git reserves %q and %q", prefix, "..", "@{")
	}
	// Per-component, because that is the scope git applies these two in: both
	// ".hidden/eng-1" and "feature.lock/eng-1" are refused, while "feature./eng-1"
	// is accepted — a component may end in a dot even though a whole ref may not.
	//
	// The trailing "" that a prefix ending in "/" splits into is skipped rather than
	// treated as an empty component: that is the ordinary shape of a prefix, and a
	// genuinely empty component means "//", which is caught above.
	for part := range strings.SplitSeq(prefix, "/") {
		switch {
		case part == "":
			continue
		case strings.HasPrefix(part, "."):
			return fmt.Errorf("branch prefix %q cannot have a path component starting with %q — git does not allow it in a branch name", prefix, ".")
		case strings.HasSuffix(part, ".lock"):
			return fmt.Errorf("branch prefix %q cannot have a path component ending in %q — git reserves that suffix", prefix, ".lock")
		}
	}
	return nil
}

// badChar reports the first character git allows nowhere in a ref name. Control
// characters and DEL are forbidden outright, as are the wildcard and revision
// characters in forbidden.
func badChar(name string) (rune, bool) {
	for _, r := range name {
		if strings.ContainsRune(forbidden, r) || r < 0x20 || r == 0x7f {
			return r, true
		}
	}
	return 0, false
}
