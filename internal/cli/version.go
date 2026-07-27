package cli

import "runtime/debug"

// devVersion is what main.version holds when nothing stamped it: the default in
// the source. Spelled here as well because this is the only code that has to
// recognize an unstamped build, and comparing against a literal in both places
// is what would let them drift apart.
const devVersion = "dev"

// ResolveVersion works out what `treewright version` should say, given whatever
// -ldflags stamped into main.version.
//
// A release is stamped: GoReleaser passes -X main.version=v1.2.3 on every tagged
// build, and that is authoritative when it is there. But `go install
// github.com/jay-snyder/treewright@latest` — the install route the README gives
// for Linux, where the Homebrew cask is not an option — applies no ldflags at
// all, so main.version kept its "dev" default and every install that way called
// itself an unreleased build.
//
// The version was in the binary the whole time: the go command records the
// module version in the embedded build info, whether or not anyone passes
// ldflags. Reading that as a fallback costs one stdlib call and makes the
// version command tell the truth for the route most Linux users take.
func ResolveVersion(stamped string) string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(stamped, info, ok)
}

// resolveVersion is ResolveVersion with the build info handed in, because a test
// binary's build info describes the test binary rather than any build worth
// asserting about.
func resolveVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "" && stamped != devVersion {
		return stamped
	}
	if !ok {
		return devVersion
	}
	switch v := info.Main.Version; v {
	case "", "(devel)":
		// A source tree the toolchain records no module version for: Go before
		// 1.24, or a build from outside a module. "dev" is the whole truth.
		return devVersion
	default:
		// Anything else is real: a tag on a clean tree ("v0.1.0"), the same with
		// uncommitted changes ("v0.1.0+dirty"), or a pseudo-version for a commit
		// past the last tag. The +dirty and pseudo-version spellings are the
		// point — they say "not a release" more precisely than "dev" did.
		return v
	}
}
