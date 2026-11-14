// Package repotest checks the repository itself: its layout and the names
// that must not change.
package repotest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func root(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}

// sources returns the text of every file that can carry a kept name, by path
// relative to the root. Documents that only talk about the names are left out.
func sources(t *testing.T) map[string]string {
	t.Helper()
	top := root(t)
	out := map[string]string{}
	for _, dir := range []string{"cmd", "internal", "builder", "scripts", "deploy", "tools", "specs", "volunteer"} {
		err := filepath.WalkDir(filepath.Join(top, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(top, path)
			if d.IsDir() {
				if rel == filepath.Join("internal", "repotest") || d.Name() == "__pycache__" || d.Name() == ".tools" {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = string(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func exists(top, rel string) bool {
	_, err := os.Lstat(filepath.Join(top, filepath.FromSlash(rel)))
	return err == nil
}

func read(t *testing.T, top, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(top, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
