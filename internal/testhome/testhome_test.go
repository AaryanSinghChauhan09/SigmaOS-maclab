package testhome_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/client"
	"github.com/omacom/omarchy-maclab/internal/testhome"
)

var realHome string

func TestMain(m *testing.M) {
	realHome, _ = os.UserHomeDir()
	os.Setenv("MACLAB_URL", "https://another.lab.example")
	os.Setenv("MACLAB_TOKEN", "someone's token")
	testhome.Main(m)
}

func under(dir, path string) bool {
	rest, err := filepath.Rel(dir, path)
	return err == nil && rest != ".." && !strings.HasPrefix(rest, ".."+string(filepath.Separator))
}

// Every folder a program finds by asking for its home is in the tests' own,
// and none is in the home of whoever runs the tests. The real folders are
// named here and never opened.
func TestNoHomeFolderIsTheRealOne(t *testing.T) {
	home := testhome.Dir()
	if home == "" || realHome == "" || under(realHome, home) {
		t.Fatalf("the tests' home is %q, inside the real one or nowhere", home)
	}
	config, _ := os.UserConfigDir()
	cache, _ := os.UserCacheDir()
	now, _ := os.UserHomeDir()
	for what, path := range map[string]string{
		"the home":               now,
		"the config folder":      config,
		"the cache folder":       cache,
		"lab's saved login":      client.LoginPath(),
		"the state folder":       os.Getenv("XDG_STATE_HOME"),
		"the data folder":        os.Getenv("XDG_DATA_HOME"),
		"XDG's config folder":    os.Getenv("XDG_CONFIG_HOME"),
		"the home, to a program": homeOfAProgram(t),
	} {
		if !under(home, path) {
			t.Errorf("%s is %s, outside the tests' home %s", what, path, home)
		}
		for _, real := range []string{
			filepath.Join(realHome, "Library", "Application Support", "maclab"),
			filepath.Join(realHome, ".config", "maclab"),
			filepath.Join(realHome, ".local", "share", "maclab"),
		} {
			if under(real, path) {
				t.Errorf("%s is %s, in the real %s", what, path, real)
			}
		}
	}
}

func homeOfAProgram(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", `printf %s "$HOME"`).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A login for another lab that the environment carries reaches no test.
func TestNoLoginComesInWithTheEnvironment(t *testing.T) {
	for _, name := range []string{"MACLAB_URL", "MACLAB_TOKEN", "MACLAB_BUILDER_TOKEN"} {
		if v, set := os.LookupEnv(name); set {
			t.Errorf("%s reached the tests as %q", name, v)
		}
	}
	if _, err := client.Load(); err != client.ErrNotLoggedIn {
		t.Fatalf("lab in the tests' home is logged in, or failed otherwise: %v", err)
	}
}

// A test that runs go still finds Go's caches where they were.
func TestGoKeepsItsFolders(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go to ask")
	}
	out, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range strings.Fields(string(out)) {
		if under(testhome.Dir(), path) {
			t.Errorf("go keeps a folder in the tests' home: %s", path)
		}
	}
}
