package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/installed"
)

// heldServer is a labd on a clock the test moves.
func heldServer(t *testing.T) (*Server, *agent.FakeClock) {
	t.Helper()
	clock := agent.NewFakeClock(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := New(ctx, Config{DataDir: t.TempDir(), FirstMaintainer: "ada", FirstMaintainerToken: "adm_secret", Clock: clock}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return s, clock
}

// throughTunnel serves h as labd's socket does: every request came in
// through the tunnel.
func throughTunnel(t *testing.T, h http.Handler) string {
	t.Helper()
	hs := httptest.NewUnstartedServer(h)
	hs.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context { return onTunnel(ctx) }
	hs.Start()
	t.Cleanup(hs.Close)
	return hs.URL
}

// lines reads a stream's lines as they arrive; the channel closes when the
// stream ends.
func lines(body io.Reader) <-chan string {
	out := make(chan string, 100)
	go func() {
		defer close(out)
		br := bufio.NewReader(body)
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			out <- strings.TrimSuffix(l, "\n")
		}
	}()
	return out
}

func nextLine(t *testing.T, ch <-chan string, what string) string {
	t.Helper()
	select {
	case l, ok := <-ch:
		if !ok {
			t.Fatalf("%s: the stream ended", what)
		}
		return l
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: nothing came", what)
	}
	return ""
}

func noLine(t *testing.T, ch <-chan string, what string) {
	t.Helper()
	select {
	case l, ok := <-ch:
		t.Fatalf("%s: got %q (open %v)", what, l, ok)
	case <-time.After(150 * time.Millisecond):
	}
}

// A held response sends a Keepalive before anything else, then one every
// 30 s, and its result as the last line.
func TestAHeldResponseSendsAKeepaliveAtOnceThenEvery30s(t *testing.T) {
	s, clock := heldServer(t)
	release := make(chan struct{})
	url := throughTunnel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.held(w, r, func() (any, error) {
			<-release
			return map[string]int{"n": 1}, nil
		})
	}))
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || ct != "application/x-ndjson" {
		t.Fatalf("answered %d %q, want 200 application/x-ndjson", resp.StatusCode, ct)
	}
	ch := lines(resp.Body)
	if l := nextLine(t, ch, "the first Keepalive, with the clock standing still"); l != "" {
		t.Fatalf("the first line is %q, want a Keepalive", l)
	}
	noLine(t, ch, "before 30 s")
	clock.Advance(29 * time.Second)
	noLine(t, ch, "at 29 s")
	clock.Advance(time.Second)
	if l := nextLine(t, ch, "the Keepalive at 30 s"); l != "" {
		t.Fatalf("at 30 s: %q", l)
	}
	noLine(t, ch, "just after 30 s")
	clock.Advance(30 * time.Second)
	if l := nextLine(t, ch, "the Keepalive at 60 s"); l != "" {
		t.Fatalf("at 60 s: %q", l)
	}
	clock.Advance(29 * time.Second)
	noLine(t, ch, "at 89 s")
	close(release)
	if l := nextLine(t, ch, "the result"); l != `{"result":{"n":1}}` {
		t.Fatalf("the last line is %q", l)
	}
	if l, open := <-ch; open {
		t.Fatalf("a line after the result: %q", l)
	}
}

// What goes wrong once a held response has begun is said in its last line:
// the status is already 200.
func TestAHeldResponsePutsItsErrorInTheBody(t *testing.T) {
	s, _ := heldServer(t)
	url := throughTunnel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.held(w, r, func() (any, error) { return nil, refuse(502, "m1 went away") })
	}))
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "\n"+`{"error":"m1 went away","status":502}`+"\n" {
		t.Fatalf("answered %d %q", resp.StatusCode, b)
	}
}

// A held response a client stops reading ends, and its work is told.
func TestACutHeldResponseEndsItsWork(t *testing.T) {
	s, _ := heldServer(t)
	ended := make(chan struct{})
	url := throughTunnel(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.held(w, r, func() (any, error) {
			<-r.Context().Done()
			close(ended)
			return nil, r.Context().Err()
		})
	}))
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	nextLine(t, lines(resp.Body), "the first Keepalive")
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the work ran on after its caller was cut")
	}
}

// The event stream is never silent either: a line at once, then its own
// Keepalive every 15 s.
func TestTheEventStreamKeepsIts15s(t *testing.T) {
	s, clock := heldServer(t)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	req, _ := http.NewRequest("GET", hs.URL+"/api/stream", nil)
	req.Header.Set("Authorization", "Bearer adm_secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ch := lines(resp.Body)
	if l := nextLine(t, ch, "the first line, with the clock standing still"); resp.StatusCode != 200 || l != ": maclab stream" {
		t.Fatalf("the stream began %d %q", resp.StatusCode, l)
	}
	nextLine(t, ch, "the end of the first event")
	clock.Advance(14 * time.Second)
	noLine(t, ch, "at 14 s")
	clock.Advance(time.Second)
	if l := nextLine(t, ch, "the Keepalive at 15 s"); l != ": keepalive" {
		t.Fatalf("at 15 s: %q", l)
	}
}

// heldExec asks for an exec on l's Mac as a Maintainer.
func heldExec(l *lab, tunnel bool, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"command":"uname -r"}`))
	req.SetPathValue("name", l.name)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if tunnel {
		req = req.WithContext(onTunnel(req.Context()))
	}
	rec := httptest.NewRecorder()
	l.s.hExec(rec, byMaintainer(req))
	return rec
}

// heldLast is a stream's last line, after its Keepalives.
func heldLast(t *testing.T, body string) api.HeldLine {
	t.Helper()
	if !strings.HasPrefix(body, "\n") || !strings.HasSuffix(body, "\n") {
		t.Fatalf("a stream starts with a Keepalive and ends its last line: %q", body)
	}
	ls := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	for _, l := range ls[:len(ls)-1] {
		if l != "" {
			t.Fatalf("a line before the last is not a Keepalive: %q", l)
		}
	}
	var hl api.HeldLine
	if err := json.Unmarshal([]byte(ls[len(ls)-1]), &hl); err != nil {
		t.Fatalf("the last line %q: %v", ls[len(ls)-1], err)
	}
	return hl
}

// Through the tunnel lab exec always answers as a stream. The silent form,
// one JSON answer when the command ends, is for callers on the mini, and
// they get the stream too when they ask.
func TestTheSilentFormAnswersOnlyOnTheLoopbackPort(t *testing.T) {
	l := newLab(t, false)
	l.waitDevice(func(d api.Device) bool { return d.BootID != "" })

	rec := heldExec(l, false, "")
	var res api.ExecResult
	if ct := rec.Header().Get("Content-Type"); rec.Code != 200 || ct != "application/json" || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.Stdout != "sim ran: uname -r\n" {
		t.Fatalf("at the loopback port: %d %q %q", rec.Code, ct, rec.Body)
	}
	for _, c := range []struct {
		what   string
		tunnel bool
		accept string
	}{
		{"through the tunnel", true, ""},
		{"through the tunnel, asking for the silent form", true, "application/json"},
		{"at the loopback port, asking for the stream", false, "application/x-ndjson, application/json"},
	} {
		rec := heldExec(l, c.tunnel, c.accept)
		if ct := rec.Header().Get("Content-Type"); rec.Code != 200 || ct != "application/x-ndjson" {
			t.Fatalf("%s: %d %q %q", c.what, rec.Code, ct, rec.Body)
		}
		hl := heldLast(t, rec.Body.String())
		var res api.ExecResult
		if hl.Error != "" || json.Unmarshal(hl.Result, &res) != nil || res.Stdout != "sim ran: uname -r\n" || res.Via != "agent" {
			t.Fatalf("%s: the last line is %+v", c.what, hl)
		}
	}

	// A refusal labd can give before it holds anything keeps its status.
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	req.SetPathValue("name", l.name)
	rec = httptest.NewRecorder()
	l.s.hExec(rec, byMaintainer(req.WithContext(onTunnel(req.Context()))))
	if rec.Code != 400 {
		t.Fatalf("an exec with no command through the tunnel: %d %q", rec.Code, rec.Body)
	}
}

// freeze-entry answers as a stream through the tunnel, and what goes wrong
// on the Mac is in its last line.
func TestFreezeEntryAnswersAsAStream(t *testing.T) {
	l, _, _ := installedLab(t, func(_ string, o map[string]any) {
		e := o["installer_entry"].(installed.Entry)
		e.UKISections = maps.Clone(e.UKISections)
		e.UKISections[".cmdline"] = installed.Section{SHA256: "cc", Text: e.Cmdline}
		o["installer_entry"] = e
	})
	dir := l.s.frozenDir("9.9")
	for _, f := range []string{"installer-entry/sim-mac.json", "PINS.installer-entry.sim-mac"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	freeze := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", nil)
		req.SetPathValue("release", "9.9")
		req.SetPathValue("device", "sim-mac")
		rec := httptest.NewRecorder()
		l.s.hFreezeEntry(rec, byMaintainer(req.WithContext(onTunnel(req.Context()))))
		return rec
	}

	l.sim.OnExec = func(string) (string, bool) { return "not what the collector writes", true }
	rec := freeze()
	if ct := rec.Header().Get("Content-Type"); rec.Code != 200 || ct != "application/x-ndjson" {
		t.Fatalf("a failed freeze: %d %q %q", rec.Code, ct, rec.Body)
	}
	if hl := heldLast(t, rec.Body.String()); hl.Status != 502 || !strings.Contains(hl.Error, "collector output") || hl.Result != nil {
		t.Fatalf("a failed freeze's last line: %+v", hl)
	}

	l.sim.OnExec = func(cmd string) (string, bool) {
		if !strings.Contains(cmd, "MACLAB_FREEZE_EOF") {
			return "", false
		}
		d := l.s.dev("sim-mac").snapshot()
		return string(l.sim.InstalledObs("freeze", "", d.BootID, d.Kernel, "")), true
	}
	rec = freeze()
	var out struct {
		Device string `json:"device"`
		SHA    string `json:"entry_sha256"`
	}
	if hl := heldLast(t, rec.Body.String()); hl.Error != "" || json.Unmarshal(hl.Result, &out) != nil || out.Device != "sim-mac" || out.SHA == "" {
		t.Fatalf("the freeze's last line: %+v", hl)
	}
	if _, err := os.Stat(filepath.Join(dir, "PINS.installer-entry.sim-mac")); err != nil {
		t.Fatalf("the entry was not frozen: %v", err)
	}
	// Frozen once: labd says so before it holds anything, with its status.
	if rec := freeze(); rec.Code != 409 {
		t.Fatalf("a second freeze: %d %q", rec.Code, rec.Body)
	}
}
