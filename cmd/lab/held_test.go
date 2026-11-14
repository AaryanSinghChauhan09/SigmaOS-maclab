package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/client"
)

func labAt(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	labd := httptest.NewServer(h)
	t.Cleanup(labd.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MACLAB_URL", labd.URL)
	t.Setenv("MACLAB_TOKEN", "tok")
	c, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// When the connection is cut under lab exec, lab says the command may still
// be running and does not send it again.
func TestExecCutSaysTheCommandMayStillBeRunning(t *testing.T) {
	var requests atomic.Int32
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-ndjson")
		io.WriteString(w, "\n")
		w.(http.Flusher).Flush()
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	})
	err := dispatch(context.Background(), c, "exec", []string{"m1", "make", "all"})
	if err == nil || err.Error() != "connection lost; the command may still be running" {
		t.Fatalf("lab exec says %q", err)
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("labd saw %d requests, want 1", n)
	}
}
