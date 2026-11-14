package server

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// macOS keeps its temporary folder behind a symlink (/var), and the code under
// test refuses paths with a symlink among their ancestors.
func TestMain(m *testing.M) {
	if dir, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		os.Setenv("TMPDIR", dir)
	}
	os.Exit(m.Run())
}

func needsLinux(t *testing.T, what string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux: " + what)
	}
}
