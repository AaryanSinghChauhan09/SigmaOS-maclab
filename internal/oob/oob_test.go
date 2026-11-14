package oob

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

)

const token = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

func tokenFile(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oobd.token")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheTokenIsReadFromAFileOnlyItsAccountReads(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		got, err := ReadToken(tokenFile(t, token+"\n", mode))
		if err != nil || got != token {
			t.Errorf("mode %04o: %q, %v", mode, got, err)
		}
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o606, 0o610, 0o601} {
		if _, err := ReadToken(tokenFile(t, token+"\n", mode)); err == nil || !strings.Contains(err.Error(), "read by others") {
			t.Errorf("mode %04o: %v", mode, err)
		}
	}
	for _, text := range []string{"", "\n", "short", token[:31], token + " " + token, token + "\n" + token + "\n"} {
		if _, err := ReadToken(tokenFile(t, text, 0o600)); err == nil {
			t.Errorf("%q was read as a token", text)
		}
	}

	dir := t.TempDir()
	if _, err := ReadToken(filepath.Join(dir, "none")); err == nil || !strings.Contains(err.Error(), "set-up makes it") {
		t.Errorf("a missing file: %v", err)
	}
	if _, err := ReadToken(dir); err == nil {
		t.Error("a folder was read as a token")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(tokenFile(t, token, 0o600), link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToken(link); err == nil {
		t.Error("a link was followed to a token")
	}
}

// A file of another account's is refused: root's copy is not labd's to
// read, and labd's is not root's to trust.
func TestATokenFileOfAnotherAccountIsRefused(t *testing.T) {
	path := tokenFile(t, token, 0o600)
	if got, err := readToken(path, os.Geteuid()); err != nil || got != token {
		t.Fatalf("its own file: %v", err)
	}
	for _, uid := range []int{0, os.Geteuid() + 1} {
		if uid == os.Geteuid() {
			continue
		}
		if _, err := readToken(path, uid); err == nil || !strings.Contains(err.Error(), "does not belong") {
			t.Errorf("read as uid %d: %v", uid, err)
		}
	}
}

type asked struct{ method, path, auth, body string }

type controller struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []asked
}

func newController(t *testing.T, answer func(a asked) (int, string)) *controller {
	t.Helper()
	c := &controller{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a := asked{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), string(b)}
		c.mu.Lock()
		c.got = append(c.got, a)
		c.mu.Unlock()
		code, text := answer(a)
		w.WriteHeader(code)
		io.WriteString(w, text)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func TestEveryRequestOfLabdsNamesItsPortAndCarriesTheFilesToken(t *testing.T) {
	c := newController(t, func(a asked) (int, string) {
		if strings.HasSuffix(a.path, "/reset") {
			return 200, `{"rid":2}`
		}
		return 200, `{"rid":2,"connected":true,"name":"omarchy-m1-pro","product_id":29448,"plug_count":40}`
	})
	file := tokenFile(t, token+"\n", 0o600)
	cl, err := NewClient(c.srv.URL, file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p, err := cl.Port(ctx, 2)
	if err != nil || p != (Port{RID: 2, Connected: true, Name: "omarchy-m1-pro", ProductID: 29448, PlugCount: 40}) {
		t.Fatalf("%+v, %v", p, err)
	}
	if _, err := cl.Probe(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := cl.Reset(ctx, 2, &Seen{ProductID: 29448, PlugCount: 40}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Reset(ctx, 2, nil); err != nil {
		t.Fatal(err)
	}
	want := []asked{
		{"GET", "/v1/ports/2/status", "Bearer " + token, ""},
		{"GET", "/v1/ports/2/status?probe=1", "Bearer " + token, ""},
		{"POST", "/v1/ports/2/reset", "Bearer " + token, `{"product_id":29448,"plug_count":40}`},
		{"POST", "/v1/ports/2/reset", "Bearer " + token, ""},
	}
	if len(c.got) != len(want) {
		t.Fatalf("asked %v", c.got)
	}
	for i := range want {
		if c.got[i] != want[i] {
			t.Errorf("request %d: %+v, want %+v", i, c.got[i], want[i])
		}
	}

	// A token made again is used at once.
	other := strings.Repeat("ab", 32)
	if err := os.WriteFile(file, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Port(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if got := c.got[len(c.got)-1].auth; got != "Bearer "+other {
		t.Errorf("after the token changed labd sent %q", got)
	}

	// A file others can read stops the request before it is sent.
	n := len(c.got)
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cl.Reset(ctx, 2, nil); err == nil || len(c.got) != n {
		t.Errorf("a token others can read was sent: %v", err)
	}
}

func TestARefusalForAChangedPortIsToldApartFromAFailure(t *testing.T) {
	c := newController(t, func(a asked) (int, string) {
		switch {
		case strings.Contains(a.path, "/ports/0/"):
			return 409, "RID 0: plug count is 43, not the 42 it was seen with\n"
		case strings.Contains(a.path, "/ports/1/"):
			return 502, "the reset tool failed: No connection detected\n"
		}
		return 200, `{"rid":0,"plug_count":1}`
	})
	cl, err := NewClient(c.srv.URL, tokenFile(t, token, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	var changed *Changed
	if err := cl.Reset(context.Background(), 0, &Seen{ProductID: 1, PlugCount: 42}); !errors.As(err, &changed) || !strings.Contains(changed.Reason, "plug count is 43") {
		t.Fatalf("a changed port: %v", err)
	}
	if err := cl.Reset(context.Background(), 1, nil); err == nil || errors.As(err, &changed) || !strings.Contains(err.Error(), "No connection detected") {
		t.Fatalf("a failed tool: %v", err)
	}
	if _, err := cl.Port(context.Background(), 2); err == nil || !strings.Contains(err.Error(), "answered for RID 0") {
		t.Fatalf("an answer for another port: %v", err)
	}
	c.srv.Close()
	if err := cl.Reset(context.Background(), 2, nil); err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Fatalf("a stopped controller: %v", err)
	}
}

func TestTheControllerIsOnThisMachineOnly(t *testing.T) {
	for _, addr := range []string{"http://127.0.0.1:7780", "http://127.0.0.1:7780/", "http://[::1]:7780"} {
		if _, err := NewClient(addr, "/var/lab/secrets/oobd.token"); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{
		"http://192.168.1.20:7780", "http://100.125.212.71:7780", "http://localhost:7780", "https://127.0.0.1:7780",
		"http://127.0.0.1", "127.0.0.1:7780", "http://127.0.0.1:7780/v1", "http://user:pw@127.0.0.1:7780", "http://127.0.0.1:7780?x=1", "",
	} {
		if _, err := NewClient(addr, "/var/lab/secrets/oobd.token"); err == nil {
			t.Errorf("%q was taken for the controller's address", addr)
		}
	}
	if _, err := NewClient("http://127.0.0.1:7780", ""); err == nil {
		t.Error("a client with no token file was made")
	}
}

// The ports are the mini's three rear ones as people name them, facing its
// back: left is RID 1, middle RID 0, right RID 2.
func TestThePortsAreLeftMiddleAndRight(t *testing.T) {
	want := map[string]int{"left": 1, "middle": 0, "right": 2}
	if len(RIDs) != len(want) {
		t.Fatalf("the ports: %v", RIDs)
	}
	for i, name := range []string{"left", "middle", "right"} {
		if rid, ok := RIDOf(name); !ok || rid != want[name] || PortName(rid) != name || RIDs[i] != rid {
			t.Errorf("the %s port: RID %d %v, named %q, listed as RID %d", name, rid, ok, PortName(rid), RIDs[i])
		}
	}
	for _, name := range []string{"", "front", "Left", "0", "1", "RID 1"} {
		if rid, ok := RIDOf(name); ok {
			t.Errorf("%q is taken for the port with RID %d", name, rid)
		}
	}
	if got := PortName(7); got != "RID 7" {
		t.Errorf("a port the mini has not is named %q", got)
	}
}
