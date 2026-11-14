package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

func TestPlainNeverShowsAPage(t *testing.T) {
	for in, want := range map[string]string{
		"connection lost; the command may still be running\nsecond line": "connection lost; the command may still be running",
		"<!DOCTYPE html><html><body>error code: 1015</body></html>":      "an error page, not shown",
		"  <HTML><head>":                    "an error page, not shown",
		"a tag <b>here</b> and a tab\there": "a tag ?b?here?/b? and a tab?here",
		"":                                  "",
	} {
		if got := plain([]byte(in)); got != want {
			t.Errorf("plain(%q) = %q, want %q", in, got, want)
		}
	}
	if got := plain(bytes.Repeat([]byte("x"), 5000)); len(got) > 300 {
		t.Errorf("a long line is shown whole: %d characters", len(got))
	}
}

// origin is a made-up labd on a Unix socket: it says what it was asked by
// and from where, holds /silent without a byte, and streams /held with a
// Keepalive first.
func origin(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fdp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "labd.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/who", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, r.Host+" "+r.Header.Get("CF-Connecting-IP")+" "+r.Header.Get("X-Forwarded-For")+" ray:"+r.Header.Get("CF-Ray"))
	})
	mux.HandleFunc("/never", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	mux.HandleFunc("/held", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		io.WriteString(w, "\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(time.Duration(len(r.URL.Query().Get("for"))) * 100 * time.Millisecond):
			io.WriteString(w, "{\"result\":1}\n")
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return path
}

func standInFor(t *testing.T, socket string, silence time.Duration, faults ...string) *httptest.Server {
	t.Helper()
	var d net.Dialer
	s := &standIn{host: "lab.front-door.test", client: "198.51.100.7", silence: silence, faults: map[string]bool{}}
	for _, f := range faults {
		s.faults[f] = true
	}
	s.up = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "unix", socket) }}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, url string, headers ...string) (*http.Response, string, error) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, string(b), err
}

func TestTheStandInNamesTheCallerAsTheEdgeDoes(t *testing.T) {
	socket := origin(t)
	ts := standInFor(t, socket, time.Minute)
	resp, body, err := get(t, ts.URL+"/who", "CF-Connecting-IP", "203.0.113.77", "X-Forwarded-For", "203.0.113.77", "CF-Ray", "mine")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "lab.front-door.test 198.51.100.7  ray:") || strings.Contains(body, "mine") || strings.Contains(body, "203.0.113.77") {
		t.Errorf("labd was told: %q", body)
	}
	if resp.Header.Get("CF-Ray") == "" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("the answer's headers: %v", resp.Header)
	}

	if _, body, _ := get(t, standInFor(t, socket, time.Minute, "pass-client-address").URL+"/who", "CF-Connecting-IP", "203.0.113.77"); !strings.Contains(body, " 203.0.113.77 ") {
		t.Errorf("with the fault it did not believe the caller: %q", body)
	}
	roaming := standInFor(t, socket, time.Minute, "roaming-address")
	_, one, _ := get(t, roaming.URL+"/who")
	_, two, _ := get(t, roaming.URL+"/who")
	if strings.Fields(one)[1] == strings.Fields(two)[1] {
		t.Errorf("with the fault two requests came from one address: %q, %q", one, two)
	}
	if resp, _, _ := get(t, standInFor(t, socket, time.Minute, "public-cache").URL+"/who"); resp.Header.Get("Cache-Control") != "public, max-age=60" {
		t.Errorf("with the fault the policy is %q", resp.Header.Get("Cache-Control"))
	}
	resp, body, _ = get(t, standInFor(t, socket, time.Minute, "challenge").URL+"/who", "User-Agent", "omarchy-m-test/0.2.0")
	if resp.StatusCode != 403 || resp.Header.Get("CF-Mitigated") != "challenge" || !strings.Contains(body, "<html>") {
		t.Errorf("with the fault the Python tool got %d %q", resp.StatusCode, body)
	}
}

func TestTheStandInCutsASilentAnswerAndKeepsAHeldOne(t *testing.T) {
	socket := origin(t)
	ts := standInFor(t, socket, 300*time.Millisecond)
	// No answer at all: the edge's own page, as a 524.
	resp, body, err := get(t, ts.URL+"/never")
	if err != nil || resp.StatusCode != 524 || !strings.Contains(body, "error code: 524") {
		t.Errorf("an origin that never answers: %v %v %q", err, resp, body)
	}
	// A stream that goes silent after its first Keepalive is cut, not ended.
	if _, body, err := get(t, ts.URL+"/held?for=xxxxxxxxxx"); err == nil {
		t.Errorf("a stream silent for a second came through whole: %q", body)
	}
	// One that ends inside the silence comes through.
	if _, body, err := get(t, ts.URL+"/held?for=x"); err != nil || body != "\n{\"result\":1}\n" {
		t.Errorf("a held stream: %v %q", err, body)
	}
	// With the fault the Keepalive never reaches the caller.
	if _, body, err := get(t, standInFor(t, socket, time.Minute, "no-keepalives").URL+"/held?for=x"); err != nil || body != "{\"result\":1}\n" {
		t.Errorf("with the fault a held stream: %v %q", err, body)
	}
}

func TestAnswersCountsWhatTakesAConnection(t *testing.T) {
	open, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := closed.Addr().String()
	closed.Close()
	var b bytes.Buffer
	out = &b
	defer func() { out = os.Stdout }()
	if err := answersCommand(flag.NewFlagSet("answers", flag.ContinueOnError), []string{"--wait", "1s", open.Addr().String(), gone}); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(open.Addr().String())
	if want := "asked=2\nopen=1\nclosed=1\nopen_ports=" + port + "\n"; b.String() != want {
		t.Errorf("answers said:\n%swant:\n%s", b.String(), want)
	}
	if strings.Contains(b.String(), "127.0.0.1") {
		t.Error("answers named an address")
	}
}
