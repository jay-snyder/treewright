package shellinit

import (
	"os"
	"path/filepath"
	"testing"
)

// inherited is what TestMain sets each of the wrapper's variables to, so a
// test that reports one of them seeing it has been handed this process's
// environment.
const inherited = "inherited-from-TestMain"

// TestMain runs every test in this package from the worst place a shell test
// can be run from: inside a tw window, on a machine where every startup file a
// shell could be pointed at exists and refuses to let the shell go on.
//
// A test that hands its shell os.Environ() passes in CI, where nothing loads
// the wrapper and HOME holds no dotfiles. On a developer's machine it fails on
// the environment rather than the code, or quietly tests something else. Three
// tests here were in that state at once, and it showed only on the one laptop
// that ran the suite from a tw window. This makes every machine that laptop:
// the wrapper's variables hold a value no test expects, and each variable a
// shell finds startup files through names one that says so and ends the
// shell. runShell hands a shell none of them, through testenv.ShellEnv, which
// is the whole of what a test here has to do.
//
// Syntax checks are unaffected. zsh -n, bash -n and fish --no-execute read no
// startup file's commands.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "shellinit-")
	if err != nil {
		panic(err)
	}
	if err := poisonTheEnvironment(dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// poisonTheEnvironment sets the environment TestMain describes, with every
// file it names under dir.
func poisonTheEnvironment(dir string) error {
	wrapper := map[string]string{
		"TREEWRIGHT_ARGV0": inherited,
		// A path in a directory that does not exist, so that a stub writing to
		// an inherited eval file fails rather than leaving one in the source tree.
		"TREEWRIGHT_EVAL_FILE": filepath.Join(dir, inherited, "eval"),
		VersionVar:             inherited,
		ShellVar:               inherited,
	}
	for name, value := range wrapper {
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}

	// Each variable a shell finds a startup file through, and the file under it
	// that zsh or fish reads for -c. HOME's are where those shells look once the
	// others are gone, so they catch a test that takes those away and keeps HOME.
	under := map[string][]string{
		"HOME":            {".zshenv", ".config/fish/config.fish"},
		"ZDOTDIR":         {".zshenv"},
		"XDG_CONFIG_HOME": {"fish/config.fish"},
		"XDG_DATA_HOME":   {"fish/vendor_conf.d/inherited.fish"},
	}
	for name, files := range under {
		root := filepath.Join(dir, name)
		for _, file := range files {
			if err := writeStartupFile(filepath.Join(root, file), name); err != nil {
				return err
			}
		}
		if err := os.Setenv(name, root); err != nil {
			return err
		}
	}
	// And the two that name the file itself, which bash reads for -c and sh
	// only when interactive.
	for _, name := range []string{"BASH_ENV", "ENV"} {
		path := filepath.Join(dir, name)
		if err := writeStartupFile(path, name); err != nil {
			return err
		}
		if err := os.Setenv(name, path); err != nil {
			return err
		}
	}
	return nil
}

// writeStartupFile writes a startup file that says which variable led the shell
// to it and then ends the shell, in words every one of the three shells reads
// alike.
//
// It ends the shell with exec rather than exit, because fish reads an exit in
// config.fish as the end of that file and goes on to run the -c program.
func writeStartupFile(path, variable string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := "echo 'this shell read a startup file through the " + variable +
		" it inherited from the test process: start it through runShell, or with testenv.ShellEnv, never os.Environ()' >&2\n" +
		"exec false\n"
	return os.WriteFile(path, []byte(body), 0o644)
}
