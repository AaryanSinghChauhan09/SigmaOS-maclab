// Package testhome gives a test binary a home of its own, so that no test,
// and nothing a test starts, reads or writes the files of whoever runs the
// tests: lab's saved login, labd's default data, a CLI's state.
package testhome

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

var dir string

// Dir is the home Set made, empty before it.
func Dir() string { return dir }

// Main runs a package's tests in a home of their own. A package's TestMain
// is this call alone, unless it has more to set up (see Set).
func Main(m interface{ Run() int }) {
	Set()(m.Run())
}

// Set points HOME and the folders named after it at a new, empty folder,
// takes a login for a lab out of the environment, and returns what ends the
// test binary: it removes the folder and exits with code. Go's own folders
// stay where they were, so a test that runs go finds its caches.
func Set() (exit func(code int)) {
	keepGo()
	d, err := os.MkdirTemp("", "lab-tests-home")
	if err != nil {
		panic(err)
	}
	if real, err := filepath.EvalSymlinks(d); err == nil {
		d = real
	}
	for name, folder := range map[string]string{
		"HOME":            "",
		"XDG_CONFIG_HOME": ".config",
		"XDG_STATE_HOME":  ".local/state",
		"XDG_DATA_HOME":   ".local/share",
		"XDG_CACHE_HOME":  ".cache",
	} {
		os.Setenv(name, filepath.Join(d, folder))
	}
	for _, name := range []string{"MACLAB_URL", "MACLAB_TOKEN", "MACLAB_BUILDER_TOKEN"} {
		os.Unsetenv(name)
	}
	dir = d
	return func(code int) {
		os.RemoveAll(d)
		os.Exit(code)
	}
}

// keepGo names Go's folders in the environment while HOME is still the real
// one: left to their defaults they would follow HOME into the empty folder.
func keepGo() {
	names := []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"}
	if !slices.ContainsFunc(names, func(name string) bool { return os.Getenv(name) == "" }) {
		return
	}
	out, err := exec.Command("go", append([]string{"env"}, names...)...).Output()
	if err != nil {
		return
	}
	values := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(values) != len(names) {
		return
	}
	for i, name := range names {
		if values[i] != "" {
			os.Setenv(name, values[i])
		}
	}
}
