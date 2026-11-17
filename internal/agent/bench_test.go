package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent/tests/bench"
	"github.com/omacom/omarchy-maclab/internal/api"
)

// benchFixture stands a fake runner and a fake labd in for the real ones: the
// runner names the items its tier needs and records how it was called.
type benchFixture struct {
	l       *Linux
	cache   string
	calls   string
	fetched []string
	blobs   map[string][]byte
}

func newBenchFixture(t *testing.T, needs ...string) *benchFixture {
	t.Helper()
	root := t.TempDir()
	f := &benchFixture{l: &Linux{WorkDir: filepath.Join(root, "agent"), GUIUser: "gui"}, cache: filepath.Join(root, "cache"),
		calls: filepath.Join(root, "calls"), blobs: map[string][]byte{}}
	py := filepath.Join(root, "python3")
	script := "#!/bin/sh\necho \"$@\" >> " + f.calls + "\ncase \"$*\" in *--needs*) printf '" + strings.Join(needs, `\n`) + "\\n'; exit 0;; esac\n" +
		"while [ $# -gt 0 ]; do [ \"$1\" = --out ] && echo '[]' > \"$2\"; shift; done\necho ran\n"
	if err := os.WriteFile(py, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	items, _ := bench.Manifest()
	man := map[string]bench.Item{}
	for _, it := range items {
		man[it.Name] = it
	}
	var fake []bench.Item
	for _, n := range needs {
		b := []byte("pinned " + n)
		s := sha256.Sum256(b)
		it := man[n]
		it.SHA256, it.Size = hex.EncodeToString(s[:]), int64(len(b))
		f.blobs[it.SHA256] = b
		fake = append(fake, it)
	}
	restore := setBench(t, py, f.cache, root, fake)
	t.Cleanup(restore)
	return f
}

func setBench(t *testing.T, py, cache, work string, items []bench.Item) func() {
	op, oc, ow, om, og := benchPython, benchCache, benchWorkParent, benchManifest, benchGUIEnv
	benchPython, benchCache, benchWorkParent = py, cache, work
	benchManifest = func() ([]bench.Item, error) { return items, nil }
	benchGUIEnv = func(name string, wait time.Duration) (*user.User, []string, error) {
		return &user.User{Username: name, Uid: "1000", Gid: "1000"}, []string{"WAYLAND_DISPLAY=wayland-1"}, nil
	}
	return func() { benchPython, benchCache, benchWorkParent, benchManifest, benchGUIEnv = op, oc, ow, om, og }
}

func (f *benchFixture) fetch(ctx context.Context, sha, dst string) error {
	f.fetched = append(f.fetched, sha)
	b, ok := f.blobs[sha]
	if !ok {
		return errors.New("labd has no artifact " + sha)
	}
	return os.WriteFile(dst, b, 0o600)
}

func noQuiet(context.Context, time.Duration) error { return nil }

func TestBenchIsABuiltinInTwoTiers(t *testing.T) {
	for _, n := range []string{"bench", "bench-extended"} {
		if !slices.Contains(Builtins(), n) {
			t.Errorf("Builtins() = %v, no %s", Builtins(), n)
		}
	}
	if d := testTimeout(api.TestSpec{Builtin: "bench"}); d < 20*time.Minute {
		t.Errorf("default tier timeout %s", d)
	}
	if d := testTimeout(api.TestSpec{Builtin: "bench-extended"}); d < 60*time.Minute {
		t.Errorf("extended tier timeout %s", d)
	}
}

func TestBenchFetchesItsTiersItemsFromLabdBySha(t *testing.T) {
	f := newBenchFixture(t, "7zip", "glmark2")
	out := t.TempDir()
	r := f.l.RunTest(context.Background(), "j1", api.TestSpec{Name: "b", Builtin: "bench"}, f.fetch, noQuiet, out)
	if !r.Passed {
		t.Fatalf("not passed: %+v", r)
	}
	if len(f.fetched) != 2 {
		t.Fatalf("fetched %v", f.fetched)
	}
	for sha, b := range f.blobs {
		got, err := os.ReadFile(filepath.Join(f.cache, sha))
		if err != nil || string(got) != string(b) {
			t.Errorf("cache %s = %q, %v", sha, got, err)
		}
		if st, _ := os.Stat(filepath.Join(f.cache, sha)); st.Mode().Perm() != 0o644 {
			t.Errorf("cache file mode %v", st.Mode())
		}
	}
	calls, _ := os.ReadFile(f.calls)
	run := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(run) != 2 || !strings.Contains(run[0], "--tier default --needs") {
		t.Fatalf("runner calls %q", run)
	}
	for _, w := range []string{"--tier default", "--cache " + f.cache, "--out " + filepath.Join(out, "bench.json"), "--gui-user gui", "--gui-env-file "} {
		if !strings.Contains(run[1], w) {
			t.Errorf("runner call %q lacks %q", run[1], w)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "bench.json")); err != nil {
		t.Error("no bench.json in the test's output")
	}

	// A second run takes the cache and asks labd for nothing.
	f.fetched = nil
	if r := f.l.RunTest(context.Background(), "j2", api.TestSpec{Name: "b", Builtin: "bench-extended"}, f.fetch, noQuiet, t.TempDir()); !r.Passed {
		t.Fatalf("second run: %+v", r)
	}
	if len(f.fetched) != 0 {
		t.Errorf("fetched again: %v", f.fetched)
	}
	calls, _ = os.ReadFile(f.calls)
	if !strings.Contains(string(calls), "--tier extended") {
		t.Errorf("extended tier not asked for: %s", calls)
	}
}

func TestBenchRefusesAFetchThatIsNotThePinnedFile(t *testing.T) {
	f := newBenchFixture(t, "7zip")
	for sha := range f.blobs {
		f.blobs[sha] = []byte("something else")
	}
	r := f.l.RunTest(context.Background(), "j1", api.TestSpec{Name: "b", Builtin: "bench"}, f.fetch, noQuiet, t.TempDir())
	if r.Passed || !strings.Contains(r.Error, "sha256") {
		t.Fatalf("result %+v", r)
	}
	if ents, _ := os.ReadDir(f.cache); len(ents) != 0 {
		t.Errorf("cache kept %v", ents)
	}
}

func TestBenchNeedsTheDesktopSession(t *testing.T) {
	f := newBenchFixture(t)
	f.l.GUIUser = ""
	r := f.l.RunTest(context.Background(), "j1", api.TestSpec{Name: "b", Builtin: "bench"}, f.fetch, noQuiet, t.TempDir())
	if r.Passed || !strings.Contains(r.Error, "gui") {
		t.Fatalf("result %+v", r)
	}
}
