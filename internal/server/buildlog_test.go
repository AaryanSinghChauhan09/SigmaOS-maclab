package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// A followed build log is asked for from an offset: each answer is what the
// log gained since, where it now ends and the build's state.
func TestABuildLogIsReadFromAnOffset(t *testing.T) {
	s, clock := heldServer(t)
	b := &api.Build{ID: "b0001-000001-abcd", State: api.BuildRunning, Created: s.now()}
	s.saveBuild(b)
	os.MkdirAll(s.buildDir(b.ID), 0o755)
	logFile := filepath.Join(s.buildDir(b.ID), "build.log")
	os.WriteFile(logFile, []byte("CC a.o\n"), 0o644)
	add := func(text string) {
		f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(text)
		f.Close()
	}
	check := func(rec *httptest.ResponseRecorder, what, body string, offset int, state api.BuildState) {
		t.Helper()
		if rec == nil {
			t.Fatalf("%s: no answer", what)
		}
		if got := rec.Body.String(); rec.Code != 200 || got != body || rec.Header().Get("X-Log-Offset") != strconv.Itoa(offset) || rec.Header().Get("X-Build-State") != string(state) {
			t.Fatalf("%s: %d %q, offset %q, state %q", what, rec.Code, got, rec.Header().Get("X-Log-Offset"), rec.Header().Get("X-Build-State"))
		}
	}
	path := "/api/builds/" + b.ID + "/log"
	check(<-waiting(s, path+"?offset=0"), "from the start", "CC a.o\n", 7, api.BuildRunning)
	add("CC b.o\n")
	check(<-waiting(s, path+"?offset=7"), "from 7", "CC b.o\n", 14, api.BuildRunning)
	check(<-waiting(s, path+"?offset=14"), "nothing new, not waiting", "", 14, api.BuildRunning)
	check(<-waiting(s, path+"?offset=900"), "past the end", "", 14, api.BuildRunning)

	// Asked to wait, labd holds the answer until the log grows.
	got := waiting(s, path+"?offset=14&wait=3600")
	if moved, rec := advanceUntil(clock, 10*time.Second, got); rec != nil {
		t.Fatalf("nothing new, yet the wait ended after %s: %q", moved, rec.Body)
	}
	add("LD vmlinux\n")
	_, rec := advanceUntil(clock, 10*time.Second, got)
	check(rec, "what the log gained", "LD vmlinux\n", 25, api.BuildRunning)

	// It holds for 50 s at most.
	got = waiting(s, path+"?offset=25&wait=3600")
	if moved, rec := advanceUntil(clock, 49*time.Second, got); rec != nil {
		t.Fatalf("the wait ended after %s", moved)
	}
	_, rec = advanceUntil(clock, 30*time.Second, got)
	check(rec, "at 50 s", "", 25, api.BuildRunning)

	// And until the build ends.
	got = waiting(s, path+"?offset=25&wait=50")
	if _, rec := advanceUntil(clock, 5*time.Second, got); rec != nil {
		t.Fatal("the wait ended while the build ran")
	}
	b.State = api.BuildDone
	s.saveBuild(b)
	_, rec = advanceUntil(clock, 10*time.Second, got)
	check(rec, "the build's end", "", 25, api.BuildDone)

	// The whole log is still there for the asking.
	if rec := <-waiting(s, path); rec.Code != 200 || rec.Body.String() != "CC a.o\nCC b.o\nLD vmlinux\n" {
		t.Fatalf("the whole log: %d %q", rec.Code, rec.Body)
	}
}
