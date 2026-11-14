package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

func clientFor(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := loadWith(t, map[string]string{"MACLAB_URL": srv.URL, "MACLAB_TOKEN": "mnt_x"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// stream begins a held response as labd does: the status, the type and the
// first Keepalive.
func stream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	io.WriteString(w, "\n")
	w.(http.Flusher).Flush()
}

// drop cuts the connection under a response, as the Front door does to one
// it gives up on.
func drop(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	conn.Close()
}

// lab asks for the stream, reads past the Keepalives and takes the result
// from the last line.
func TestExecReadsItsResultFromTheLastLine(t *testing.T) {
	var asked string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path + " " + r.Header.Get("Accept")
		stream(w)
		io.WriteString(w, "\n\n"+`{"result":{"exit_code":3,"stdout":"6.17.0\n","stderr":"","seconds":1.5,"via":"agent"}}`+"\n")
	})
	res, err := c.Exec(context.Background(), "m1", api.ExecArgs{Command: "uname -r"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || res.Stdout != "6.17.0\n" || res.Via != "agent" {
		t.Fatalf("the result: %+v", res)
	}
	if !strings.HasPrefix(asked, "POST /api/devices/m1/exec ") || !strings.Contains(asked, "application/x-ndjson") {
		t.Fatalf("lab asked %q, want the stream asked for", asked)
	}
}

// An error in a stream's last line is labd turning the request down, with
// the status labd gave it.
func TestAnErrorInTheLastLineIsLabdsRefusal(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		stream(w)
		io.WriteString(w, `{"error":"m1 went away","status":502}`+"\n")
	})
	_, err := c.Exec(context.Background(), "m1", api.ExecArgs{Command: "true"})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 502 || se.Msg != "m1 went away" {
		t.Fatalf("the error: %#v", err)
	}
}

// A caller on the mini may still be given the silent form.
func TestExecReadsTheSilentForm(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"exit_code":0,"stdout":"ok\n"}`)
	})
	res, err := c.Exec(context.Background(), "m1", api.ExecArgs{Command: "true"})
	if err != nil || res.Stdout != "ok\n" {
		t.Fatalf("the silent form: %+v %v", res, err)
	}
}

// A cut lab exec is never sent again: the command may be running on the
// Mac, and a second request would run it twice.
func TestACutExecIsNeverSentAgain(t *testing.T) {
	for what, end := range map[string]func(http.ResponseWriter){
		"cut after a Keepalive": func(w http.ResponseWriter) { stream(w); drop(w) },
		"cut before a byte":     drop,
		"ended with no result":  stream,
		"cut in the middle of the line": func(w http.ResponseWriter) {
			stream(w)
			io.WriteString(w, `{"result":{"exit_code":0,"std`)
			w.(http.Flusher).Flush()
			drop(w)
		},
		"ended in the middle of a line": func(w http.ResponseWriter) { stream(w); io.WriteString(w, `{"result":{"exit_code":0}}`) },
	} {
		t.Run(what, func(t *testing.T) {
			var requests atomic.Int32
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				io.Copy(io.Discard, r.Body)
				end(w)
			})
			_, err := c.Exec(context.Background(), "m1", api.ExecArgs{Command: "make -j8"})
			if !errors.Is(err, ErrCut) || err.Error() != "connection lost; the command may still be running" {
				t.Fatalf("lab says %q", err)
			}
			if n := requests.Load(); n != 1 {
				t.Fatalf("labd saw %d requests, want 1", n)
			}
		})
	}
}

// A labd that cannot be reached was never asked: nothing may be running.
func TestAnExecThatNeverLeftIsNotACut(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c, err := loadWith(t, map[string]string{"MACLAB_URL": srv.URL, "MACLAB_TOKEN": "mnt_x"})
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if _, err := c.Exec(context.Background(), "m1", api.ExecArgs{Command: "true"}); err == nil || errors.Is(err, ErrCut) {
		t.Fatalf("lab says %q", err)
	}
}

// freeze-entry is held the same way, and a cut says what may have happened.
func TestFreezeEntryReadsItsStream(t *testing.T) {
	var requests atomic.Int32
	answer := func(w http.ResponseWriter) { io.WriteString(w, `{"result":{"device":"m1","entry_sha256":"ab"}}`+"\n") }
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		stream(w)
		answer(w)
	})
	var out map[string]any
	if err := c.Installed(context.Background(), "POST", "12.0", "entry/m1", nil, &out); err != nil || out["entry_sha256"] != "ab" {
		t.Fatalf("freeze-entry: %v %v", out, err)
	}
	answer = drop
	requests.Store(0)
	err := c.Installed(context.Background(), "POST", "12.0", "entry/m1", nil, &out)
	if !errors.Is(err, ErrCut) || requests.Load() != 1 {
		t.Fatalf("a cut freeze-entry: %v, %d requests", err, requests.Load())
	}
}
