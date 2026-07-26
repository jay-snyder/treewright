package refname

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// gitAccepts asks git itself whether a branch of this name could exist. The whole
// package is a restatement of git's rules, so git is the only authority worth
// testing it against — reading the answers off the documentation is how the
// surprises in the package comment were missed the first time.
func gitAccepts(t *testing.T, branch string) bool {
	t.Helper()
	return exec.Command("git", "check-ref-format", "refs/heads/"+branch).Run() == nil
}

// TestCheckPrefixAgreesWithGit is what keeps the restatement honest: every prefix
// accepted here has to produce a branch name git accepts, and every one refused has
// to be one git refuses too — unless treewright is deliberately stricter, which
// each case has to declare.
func TestCheckPrefixAgreesWithGit(t *testing.T) {
	tests := []struct {
		prefix string
		valid  bool
		// stricter marks a prefix git would accept and treewright refuses anyway.
		// Asserted rather than tolerated: if git ever tightens up, the marker is
		// wrong and this test says so.
		stricter bool
	}{
		{prefix: "feature/", valid: true},
		{prefix: "feature/exp/", valid: true},
		{prefix: "feature-", valid: true},  // "/" is not required
		{prefix: "feature./", valid: true}, // legal, unlikely as it looks
		{prefix: "feat.ure/", valid: true}, // a dot inside a component is fine
		{prefix: "", valid: true},          // no prefix at all is a setting
		{prefix: "-x/", valid: false, stricter: true},
		{prefix: "/feature/", valid: false},
		{prefix: "feature//", valid: false},
		{prefix: ".hidden/", valid: false},
		{prefix: "feature.lock/", valid: false},
		{prefix: "a..b/", valid: false},
		{prefix: "a@{/", valid: false},
		{prefix: "fea ture/", valid: false},
		{prefix: "fea~ture/", valid: false},
		{prefix: "back\\slash/", valid: false},
		{prefix: "wild*card/", valid: false},
		{prefix: "colon:/", valid: false},
		{prefix: "tab\t/", valid: false},
		{prefix: "ctrl\x01/", valid: false},
		{prefix: "x/.hidden/", valid: false}, // the rule is per component, not per prefix
	}

	for _, tc := range tests {
		name := tc.prefix
		if name == "" {
			name = "(empty)"
		}
		t.Run(name, func(t *testing.T) {
			err := CheckPrefix(tc.prefix)
			if tc.valid && err != nil {
				t.Fatalf("CheckPrefix(%q) = %v, want no error", tc.prefix, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("CheckPrefix(%q) = nil, want an error", tc.prefix)
			}
			// The refusal has to name the value, since a config may list several.
			// Quoted, which is how the messages render it — and the only readable way
			// to show a prefix with a tab or a control character in it.
			if err != nil && !strings.Contains(err.Error(), strconv.Quote(tc.prefix)) {
				t.Errorf("error %q does not name the prefix %q", err, tc.prefix)
			}

			// And now git's own verdict on the branch this prefix would produce.
			branch := tc.prefix + "eng-1"
			switch accepted := gitAccepts(t, branch); {
			case tc.stricter && !accepted:
				t.Errorf("%q is marked stricter than git, but git rejects %q too — drop the marker", tc.prefix, branch)
			case !tc.stricter && accepted != tc.valid:
				t.Errorf("CheckPrefix(%q) says valid=%v, but git says %v about %q", tc.prefix, tc.valid, accepted, branch)
			}
		})
	}
}

// TestCheckSlugAgreesWithGit is the same check for the other half of the name, with
// one deliberate difference: a slug becomes a directory too, so "/" is refused here
// while git is perfectly happy with it.
func TestCheckSlugAgreesWithGit(t *testing.T) {
	tests := []struct {
		slug     string
		valid    bool
		stricter bool
	}{
		{slug: "eng-1", valid: true},
		{slug: "eng-1-white-screen", valid: true},
		{slug: "v1.2.3", valid: true},
		{slug: "a/b", valid: false, stricter: true},   // a stray parent directory
		{slug: "-flag", valid: false, stricter: true}, // reads as a flag
		{slug: ".hidden", valid: false},
		{slug: "trailing.", valid: false},
		{slug: "a..b", valid: false},
		{slug: "a@{b", valid: false},
		{slug: "eng-1.lock", valid: false},
		{slug: "with space", valid: false},
		{slug: "til~de", valid: false},
		{slug: "car^et", valid: false},
		{slug: "quest?ion", valid: false},
	}

	for _, tc := range tests {
		t.Run(tc.slug, func(t *testing.T) {
			err := CheckSlug(tc.slug)
			if tc.valid && err != nil {
				t.Fatalf("CheckSlug(%q) = %v, want no error", tc.slug, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("CheckSlug(%q) = nil, want an error", tc.slug)
			}
			if err != nil && !strings.Contains(err.Error(), strconv.Quote(tc.slug)) {
				t.Errorf("error %q does not name the slug %q", err, tc.slug)
			}

			switch accepted := gitAccepts(t, "x/"+tc.slug); {
			case tc.stricter && !accepted:
				t.Errorf("%q is marked stricter than git, but git rejects it too — drop the marker", tc.slug)
			case !tc.stricter && accepted != tc.valid:
				t.Errorf("CheckSlug(%q) says valid=%v, but git says %v", tc.slug, tc.valid, accepted)
			}
		})
	}
}
