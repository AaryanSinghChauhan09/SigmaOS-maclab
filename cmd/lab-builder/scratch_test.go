package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// buildingContainer plays Apple's container CLI for one kernel build: the
// VM reads its input in the folder shared as /in and leaves its outputs in
// the one shared as /io.
const buildingContainer = `#!/bin/sh
echo "$*" >> "$(dirname "$0")/calls"
in= io=
for a; do
  case "$a" in
    *:/in) in=${a%:/in} ;;
    *:/io) io=${a%:/io} ;;
  esac
done
[ -n "$in" ] && [ -n "$io" ] || { echo "the VM was given no /in and /io" >&2; exit 64; }
[ ! -e "$io/config" ] || { echo "the input is in the folder the VM writes" >&2; exit 65; }
grep -q CONFIG_X=y "$in/config" || { echo "no config in /in" >&2; exit 66; }
mkdir "$io/out" && echo kernel > "$io/out/kernel.tar.zst" && echo kernel.tar.zst > "$io/kernel" && echo 7.2.0-lab-x > "$io/release"
`

// builtIn runs one kernel build, b1, with scratch as the scratch folder and
// script as container, against a labd that hands out the build's input and
// takes its result. asked runs when the input is asked for, which is after
// the build's folder was made and before the input is written.
func builtIn(t *testing.T, scratch, script string, asked func()) (api.BuildResult, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "container"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var result api.BuildResult
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/builder/artifacts/"):
			if asked != nil {
				asked()
			}
			io.WriteString(w, "CONFIG_X=y\n")
		case strings.HasSuffix(r.URL.Path, "/progress"):
			io.WriteString(w, "{}")
		case strings.HasSuffix(r.URL.Path, "/done"):
			mu.Lock()
			json.NewDecoder(r.Body).Decode(&result)
			mu.Unlock()
			close(done)
			w.WriteHeader(204)
		default:
			body, _ := io.ReadAll(r.Body)
			sum := sha256.Sum256(body)
			json.NewEncoder(w).Encode(map[string]any{"sha256": hex.EncodeToString(sum[:]), "size": len(body)})
		}
	}))
	defer srv.Close()
	b, err := configure([]string{"--server", srv.URL, "--token-file", tokenFile(t, testToken, 0o600), "--io", scratch}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	b.containerBin = filepath.Join(bin, "container")
	b.run(context.Background(), api.BuildAssignment{LabVer: "-labx", Build: api.Build{ID: "b1", ConfigSHA: strings.Repeat("c", 64),
		Source: api.Source{Repo: "https://github.com/o/r", SHA: strings.Repeat("a", 40)}}})
	<-done
	mu.Lock()
	defer mu.Unlock()
	calls, _ := os.ReadFile(filepath.Join(bin, "calls"))
	return result, string(calls)
}

func holds(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// A build's input is in a folder of its own, which the VM is given beside
// the folder its outputs come back in, and nothing of the build is left
// when it ends.
func TestABuildReadsItsInputAndLeavesItsOutputs(t *testing.T) {
	scratch := t.TempDir()
	res, calls := builtIn(t, scratch, buildingContainer, nil)
	if res.Error != "" || res.Release != "7.2.0-lab-x" || res.Artifact == "" || len(res.Files) != 1 {
		t.Fatalf("the build ended %+v\ncontainer: %s", res, calls)
	}
	real, _ := filepath.EvalSymlinks(scratch)
	if !strings.Contains(calls, "-v "+real+"/b1/in:/in -v "+real+"/b1/io:/io ") {
		t.Errorf("the VM was started as: %s", calls)
	}
	if left := holds(t, scratch); len(left) != 0 {
		t.Errorf("the scratch folder still holds %v", left)
	}
}

// lab-builder runs as root on the mini, so it writes nothing through a link:
// not where the scratch folder's name is one, not where a build's folder
// became one, and not where the input's own name is one. Each such build is
// refused, and nothing outside the scratch folder is written or changed.
func TestTheBuilderWritesThroughNoLink(t *testing.T) {
	refused := func(t *testing.T, res api.BuildResult, calls, elsewhere string) {
		t.Helper()
		if res.Error == "" {
			t.Errorf("the build was not refused: %+v", res)
		}
		if strings.Contains(calls, "run ") {
			t.Errorf("a VM was started: %s", calls)
		}
		if got := holds(t, elsewhere); len(got) != 1 || got[0] != "kept" {
			t.Errorf("the folder the link names now holds %v", got)
		}
		if kept, _ := os.ReadFile(filepath.Join(elsewhere, "kept")); string(kept) != "KEEP" {
			t.Errorf("the file the link names now reads %q", kept)
		}
	}
	elsewhereOf := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "kept"), []byte("KEEP"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("the scratch folder", func(t *testing.T) {
		elsewhere, scratch := elsewhereOf(t), filepath.Join(t.TempDir(), "io")
		if err := os.Symlink(elsewhere, scratch); err != nil {
			t.Fatal(err)
		}
		res, calls := builtIn(t, scratch, buildingContainer, nil)
		refused(t, res, calls, elsewhere)
		if !strings.Contains(res.Error, "symbolic link") {
			t.Errorf("the refusal does not say why: %s", res.Error)
		}
	})
	t.Run("a build's folder", func(t *testing.T) {
		elsewhere, scratch := elsewhereOf(t), t.TempDir()
		res, calls := builtIn(t, scratch, buildingContainer, func() {
			if err := os.Rename(filepath.Join(scratch, "b1"), filepath.Join(scratch, "b1.was")); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(elsewhere, filepath.Join(scratch, "b1")); err != nil {
				t.Error(err)
			}
		})
		refused(t, res, calls, elsewhere)
	})
	t.Run("the input", func(t *testing.T) {
		elsewhere, scratch := elsewhereOf(t), t.TempDir()
		res, calls := builtIn(t, scratch, buildingContainer, func() {
			if err := os.Symlink(filepath.Join(elsewhere, "kept"), filepath.Join(scratch, "b1", inputAt)); err != nil {
				t.Error(err)
			}
		})
		refused(t, res, calls, elsewhere)
	})
}

// inputAt is where a kernel build's input is written, below the build's
// folder.
const inputAt = "in/config"
