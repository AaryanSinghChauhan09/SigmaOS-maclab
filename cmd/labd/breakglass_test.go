package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/server"
)

// labd break-glass, run beside a labd on its data directory, prints a Lab
// token for the named person that works at the loopback listener.
func TestBreakGlassPrintsATokenThatWorksAtTheLoopbackListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := t.TempDir()
	labd, err := server.New(ctx, server.Config{DataDir: data, Admins: []int64{7}}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	var out, said bytes.Buffer
	if code := breakGlass([]string{"--data", data, "ada"}, &out, &said); code != 1 || out.Len() != 0 || !strings.Contains(said.String(), "--account-id") {
		t.Fatalf("for a handle the lab does not know: exit %d, %q, %q", code, out.String(), said.String())
	}
	if code := breakGlass([]string{"--data", data}, &out, &said); code != 2 || out.Len() != 0 {
		t.Fatalf("for nobody: exit %d, %q", code, out.String())
	}
	if code := breakGlass([]string{"--data", t.TempDir(), "--account-id", "7", "ada"}, &out, &said); code != 1 || out.Len() != 0 {
		t.Fatalf("on a folder that is not labd's: exit %d, %q", code, out.String())
	}
	said.Reset()
	if code := breakGlass([]string{"--data", data, "--account-id", "7", "@ada"}, &out, &said); code != 0 {
		t.Fatalf("exit %d: %s", code, said.String())
	}
	token := strings.TrimSuffix(out.String(), "\n")
	if !regexp.MustCompile(`^lab_[0-9a-f]{40}$`).MatchString(token) {
		t.Fatalf("printed %q, not a Lab token alone on its line", out.String())
	}
	if strings.Contains(said.String(), token) || !strings.Contains(said.String(), "@ada") || !strings.Contains(said.String(), "expires on") {
		t.Errorf("said %q", said.String())
	}
	ask := func(token string) int {
		r := httptest.NewRequest("GET", "http://127.0.0.1:7770/api/devices", nil)
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		labd.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if code := ask(token); code != 200 {
		t.Errorf("the token at the loopback listener, for an Admin the configuration names: %d", code)
	}
	if code := ask(token + "0"); code != 401 {
		t.Errorf("another token: %d", code)
	}
}
