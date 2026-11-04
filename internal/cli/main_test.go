package cli

import (
	"os"
	"testing"
)

// TestMain lets the test binary stand in for treewright when it is re-run as
// one.
//
// The reviews plugin's segment starts a refresh in the background by running
// its own executable — os.Executable, which under go test is this binary. Left
// to the testing package, "reviews refresh" would be ignored as arguments and
// every test in the package would run again, in the background, once per
// status-line redraw a test simulated. Recognized here, the child is
// treewright, and the one test that waits for it sees the refresh it asked
// for. Nothing else ever runs this binary with "reviews" first.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "reviews" {
		if err := Run(Env{Args: os.Args[1:]}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
