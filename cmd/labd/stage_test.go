package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/server"
)

// labd stage reads the opening from a data directory, with labd running on
// it or not, and never makes or changes a file.
func TestStageSaysWhetherTheLabOpened(t *testing.T) {
	said := func(args ...string) (string, int) {
		var out, errw bytes.Buffer
		code := stage(args, &out, &errw)
		return strings.TrimSpace(out.String() + errw.String()), code
	}
	dir := t.TempDir()
	if got, code := said("--data", dir); code != 0 || got != "not opened" {
		t.Errorf("a directory no labd has opened: %d %q", code, got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("labd stage made %d file(s) in an empty directory", len(entries))
	}
	for _, args := range [][]string{nil, {"--data"}, {"--data", dir, "more"}, {"--nope"}} {
		if _, code := said(args...); code != 2 {
			t.Errorf("labd stage %q: status %d, want 2", args, code)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := bytes.Repeat([]byte{7}, 64)
	s, err := server.New(ctx, server.Config{DataDir: dir, Preview: true, MachineIDKey: key,
		GitHub: &server.GitHubApp{ClientID: "id", ClientSecret: "secret", WebURL: "http://127.0.0.1:1", APIURL: "http://127.0.0.1:1"}}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got, code := said("--data", dir); code != 0 || got != "not opened" {
		t.Errorf("a lab in Preview, labd running: %d %q", code, got)
	}
	w := httptest.NewRecorder()
	s.OpsHandler().ServeHTTP(w, httptest.NewRequest("POST", "http://labd/open?m-team=0", nil))
	if w.Code != 200 {
		t.Fatalf("POST /open: %d %s", w.Code, w.Body)
	}
	want := strings.TrimSpace(w.Body.String())
	if got, code := said("--data", dir); code != 0 || got != want || !strings.HasPrefix(got, "live 20") {
		t.Errorf("an opened lab, labd running: %d %q, want %q", code, got, want)
	}
	before, _ := os.Stat(filepath.Join(dir, "labd.db"))
	if got, _ := said("--data", dir); got != want {
		t.Errorf("asked again: %q", got)
	}
	if after, _ := os.Stat(filepath.Join(dir, "labd.db")); !after.ModTime().Equal(before.ModTime()) {
		t.Error("labd stage wrote to labd.db")
	}
}
