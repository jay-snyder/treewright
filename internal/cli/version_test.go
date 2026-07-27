package cli

import (
	"runtime/debug"
	"testing"
)

// buildInfo is the one field of debug.BuildInfo these tests turn on.
func buildInfo(moduleVersion string) *debug.BuildInfo {
	return &debug.BuildInfo{Main: debug.Module{Version: moduleVersion}}
}

func TestAStampedReleaseWinsOverTheModuleVersion(t *testing.T) {
	// GoReleaser's ldflags are the release's own account of itself, and the
	// module version recorded beside them can disagree: a build off a tag that
	// moved, or a release built from a commit the tag was applied to after.
	if got := resolveVersion("v1.2.3", buildInfo("v0.1.0"), true); got != "v1.2.3" {
		t.Errorf("resolveVersion = %q, want the stamped v1.2.3", got)
	}
}

// The bug this file exists for: `go install ...@latest` applies no ldflags, so
// an install of a tagged release used to report itself as "dev".
func TestAnUnstampedBuildReportsTheModuleVersion(t *testing.T) {
	if got := resolveVersion("dev", buildInfo("v0.1.0"), true); got != "v0.1.0" {
		t.Errorf("resolveVersion = %q, want v0.1.0 from the build info", got)
	}
}

func TestABuildFromAModifiedTreeKeepsItsDirtyMarker(t *testing.T) {
	// Worth asserting rather than assuming: it is what stops a local build with
	// uncommitted changes from claiming to be the release it was built beside.
	if got := resolveVersion("dev", buildInfo("v0.1.0+dirty"), true); got != "v0.1.0+dirty" {
		t.Errorf("resolveVersion = %q, want the +dirty version intact", got)
	}
}

func TestAVersionlessBuildStaysDev(t *testing.T) {
	// "(devel)" is what a toolchain that stamps no module version reports, and
	// an absent build info is what a binary built outside the go command has.
	// Neither is more informative than "dev", so neither should replace it.
	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
	}{
		{"devel", buildInfo("(devel)"), true},
		{"empty", buildInfo(""), true},
		{"no build info", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion("dev", tc.info, tc.ok); got != "dev" {
				t.Errorf("resolveVersion = %q, want dev", got)
			}
		})
	}
}

// An empty stamp means the same thing as "dev": nobody said. It is reachable
// through -X main.version= with nothing after it.
func TestAnEmptyStampIsTreatedAsUnstamped(t *testing.T) {
	if got := resolveVersion("", buildInfo("v0.1.0"), true); got != "v0.1.0" {
		t.Errorf("resolveVersion = %q, want v0.1.0", got)
	}
	if got := resolveVersion("", buildInfo("(devel)"), true); got != "dev" {
		t.Errorf("resolveVersion = %q, want dev", got)
	}
}

// ResolveVersion reads the running binary's build info, which under `go test` is
// the test binary's. The contract that survives that is the stamp taking
// precedence, which is the path a release takes.
func TestResolveVersionPrefersItsArgument(t *testing.T) {
	if got := ResolveVersion("v9.9.9"); got != "v9.9.9" {
		t.Errorf("ResolveVersion = %q, want v9.9.9", got)
	}
}
