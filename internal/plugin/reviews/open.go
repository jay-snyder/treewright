package reviews

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Open hands a pull request's URL to the desktop's own URL handler: open on
// macOS, xdg-open elsewhere, which is how the Go toolchain opens a browser too.
//
// These are what "open this link" means on each platform rather than tooling,
// and the one alternative that would have used only the dependency already
// allowed — gh browse — asks the API about the repository before it opens
// anything. See "Dependencies" in docs/plugins.md.
//
// Only an https URL is opened. The URL comes out of the cache, and open and
// xdg-open will equally launch an application or a local file if handed a path
// or a file: URL — so anything that is not a web page is refused rather than
// passed along on the strength of having come from GitHub.
func Open(url string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("not opening %q: not an https URL", url)
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	path, err := exec.LookPath(opener)
	if err != nil {
		return fmt.Errorf("%s is not on PATH, so there is nothing to open %s with", opener, url)
	}
	// Started rather than waited on: xdg-open in particular can block until the
	// browser it launched exits, and the click binding that runs this has
	// nothing to wait for.
	cmd := exec.Command(path, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
