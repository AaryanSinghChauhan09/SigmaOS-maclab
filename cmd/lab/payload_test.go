package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
	"github.com/omacom/omarchy-maclab/internal/server"
)

func TestPayloadFlagTakesANameAndAnUploadedSha(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	got, err := payloadFiles(context.Background(), nil, []string{"mod.ko=" + sha})
	if err != nil || len(got) != 1 || got[0] != (api.PayloadFile{Name: "mod.ko", SHA256: sha}) {
		t.Fatalf("payloadFiles = %+v, %v", got, err)
	}
	for _, bad := range []string{"mod.ko", "=" + sha, "mod.ko="} {
		if _, err := payloadFiles(context.Background(), nil, []string{bad}); err == nil || !strings.Contains(err.Error(), "NAME=FILE") {
			t.Errorf("--payload %q: err = %v, want a NAME=FILE usage error", bad, err)
		}
	}
}

func TestScriptTestsTakeThePayloadButGUIScriptsDoNot(t *testing.T) {
	files := []api.PayloadFile{{Name: "mod.ko", SHA256: strings.Repeat("ab", 32)}}
	if _, err := withPayload(nil, files); err == nil || !strings.Contains(err.Error(), "--script") {
		t.Fatalf("a payload with no --script test: err = %v", err)
	}
	tests := []api.TestSpec{{Name: "a", Script: "s"}, {Name: "b", Script: "s", GUI: true}}
	got, err := withPayload(tests, files)
	if err != nil || len(got[0].Payload) != 1 || len(got[1].Payload) != 0 {
		t.Fatalf("withPayload = %+v, %v: want the payload on the root script only", got, err)
	}
}

// A payload file longer than one part goes to a real labd in 32 MiB parts,
// every body with its length; a run that lost a part leaves the others with
// labd, and the next run sends only what is missing.
func TestAPayloadFileOverOnePartGoesInPartsAndTheNextRunResumes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := t.TempDir()
	labd, err := server.New(ctx, server.Config{DataDir: data, Admins: []int64{1}}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ada, err := server.BreakGlass(data, "ada", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var refuse atomic.Bool
	var parts, bodies atomic.Int32
	refuse.Store(true)
	handler := labd.Handler()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			bodies.Add(1)
			if r.ContentLength < 0 || len(r.TransferEncoding) > 0 {
				t.Errorf("%s %s came without Content-Length", r.Method, r.URL.Path)
			}
		}
		if r.Method == "PUT" {
			parts.Add(1)
			if refuse.Load() && strings.HasSuffix(r.URL.Path, "/parts/2") {
				http.Error(w, "part 2 is lost on the way", 403)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer hs.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MACLAB_URL", hs.URL)
	t.Setenv("MACLAB_TOKEN", ada.Token)
	c, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	c.Note = func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }

	file := bytes.Repeat([]byte("0123456789abcdef"), (32<<20)/16)
	file = append(file, "the second part"...)
	sum := sha256.Sum256(file)
	sha := hex.EncodeToString(sum[:])
	path := filepath.Join(t.TempDir(), "mod.ko")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := payloadFiles(ctx, c, []string{"mod.ko=" + path}); err == nil || !strings.Contains(err.Error(), "part 2") {
		t.Fatalf("the run that lost part 2: %v", err)
	}
	if parts.Load() != 2 {
		t.Fatalf("%d parts sent, want 2: a refused part is not sent again", parts.Load())
	}
	refuse.Store(false)
	parts.Store(0)
	got, err := payloadFiles(ctx, c, []string{"mod.ko=" + path})
	if err != nil || len(got) != 1 || got[0] != (api.PayloadFile{Name: "mod.ko", SHA256: sha}) {
		t.Fatalf("the next run: %+v, %v", got, err)
	}
	if parts.Load() != 1 || len(notes) != 1 || !strings.Contains(notes[0], "1 of 2 parts") {
		t.Fatalf("the next run sent %d parts and said %q", parts.Load(), notes)
	}
	if st, err := os.Stat(filepath.Join(data, "artifacts", sha)); err != nil || st.Size() != int64(len(file)) {
		t.Fatalf("labd's stored artifact: %v", err)
	}

	// A file of one part or less is one request.
	bodies.Store(0)
	small := filepath.Join(t.TempDir(), "probe.bin")
	os.WriteFile(small, file[:32<<20], 0o600)
	if _, err := payloadFiles(ctx, c, []string{"probe.bin=" + small}); err != nil || bodies.Load() != 1 {
		t.Fatalf("a file of exactly one part: %v after %d requests", err, bodies.Load())
	}
}
