package repotest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The mini's bundle is built from the tree it is in, and in the hub's
// working tree that holds the owner's deploy/mini.conf: the mini's address
// and the old lab's account, which no release carries.
func TestTheMinisBundleLeavesOutTheOwnersMiniConf(t *testing.T) {
	top := root(t)
	tree := t.TempDir()
	for _, f := range []string{"builder/kbuild", "deploy/mini.conf.example", "deploy/mini.conf", "deploy/pins/x", "scripts/mini/setup"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(tree, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", `. "$1/release/lib.sh" && bundled_sources`, "bundled", top)
	cmd.Dir = tree
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bundled_sources: %v\n%s", err, out)
	}
	if got := strings.Fields(string(out)); strings.Join(got, " ") != "builder/kbuild deploy/mini.conf.example deploy/pins/x scripts/mini/setup" {
		t.Errorf("the bundle takes %q", got)
	}
	for _, rel := range []string{"release/build.sh", "release/check.sh"} {
		if !strings.Contains(read(t, top, rel), "bundled_sources") {
			t.Errorf("%s does not take the bundle's files from bundled_sources", rel)
		}
	}
	if !strings.Contains(read(t, top, "release/check.sh"), `[[ ! -e $s/deploy/mini.conf ]] || fail`) {
		t.Error("release/check.sh does not refuse a bundle that holds deploy/mini.conf")
	}
}
