package cutovertest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two modes on one command line are refused before anything is done: the
// last one used to win, so "--dry-run --lab" ran for real.
func TestTwoModesAreRefused(t *testing.T) {
	env, log := stubs(t)
	env = append(env, "LAB_ADDRESS=http://127.0.0.1:9")
	found, _ := filepath.Glob(filepath.Join(frontDoor(t, ""), "fd[0-9][0-9]-*"))
	var scripts []string
	for _, s := range found {
		if !strings.HasSuffix(s, ".sh") && !strings.HasSuffix(s, ".md") {
			scripts = append(scripts, s)
		}
	}
	for _, s := range []string{"alias-rehearsal", "dark-recordings", "builder-gate"} {
		scripts = append(scripts, filepath.Join(repo(t), "scripts", "cutover", s))
	}
	scripts = append(scripts, frontDoor(t, "run"))
	for _, s := range scripts {
		for _, modes := range [][]string{{"--dry-run", "--lab"}, {"--local", "--lab"}, {"--lab", "--dry-run"}} {
			args := append(append([]string{}, modes...), "--mini", "owner@mini", "--bin", t.TempDir(), "--records", t.TempDir())
			out, code := run(t, env, "", s, args...)
			if code == 0 || !strings.Contains(out, "two modes") {
				t.Errorf("%s %v: exit %d\n%s", filepath.Base(s), modes, code, out)
			}
		}
	}
	for _, opts := range []string{"--lab", "--mac m1-pro --local", "--dry-run"} {
		out, code := run(t, env, "", frontDoor(t, "run"), "--for", "12", opts)
		if code == 0 || !strings.Contains(out, "names a mode") {
			t.Errorf("run --for 12 %q: exit %d\n%s", opts, code, out)
		}
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("a refused run ran something:\n%s", b)
	}
	if out, code := run(t, env, "", frontDoor(t, "fd12-limiter"), "--dry-run", "--dry-run"); code != 0 {
		t.Errorf("the same mode twice: exit %d\n%s", code, out)
	}
}
