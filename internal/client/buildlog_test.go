package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// A followed build log is polled from an offset until the build ends.
func TestAFollowedBuildLogIsPolledFromAnOffset(t *testing.T) {
	steps := []struct{ text, state string }{
		{"CC a.o\n", "running"},
		{"", "running"},
		{"CC b.o\nLD vmlinux\n", "running"},
		{"done\n", "done"},
	}
	var mu sync.Mutex
	var asked []string
	var log bytes.Buffer
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		if len(asked) > len(steps) {
			http.Error(w, "asked after the build ended", 500)
			return
		}
		st := steps[len(asked)-1]
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if off != log.Len() {
			http.Error(w, fmt.Sprintf("asked from %d, the log is at %d", off, log.Len()), 400)
			return
		}
		log.WriteString(st.text)
		w.Header().Set("X-Log-Offset", strconv.Itoa(log.Len()))
		w.Header().Set("X-Build-State", st.state)
		io.WriteString(w, st.text)
	})
	var out bytes.Buffer
	if err := c.BuildLog(context.Background(), "b1", true, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "CC a.o\nCC b.o\nLD vmlinux\ndone\n" {
		t.Fatalf("the log: %q", out.String())
	}
	want := []string{"/api/builds/b1/log?offset=0&wait=50", "/api/builds/b1/log?offset=7&wait=50", "/api/builds/b1/log?offset=7&wait=50", "/api/builds/b1/log?offset=25&wait=50"}
	if strings.Join(asked, " ") != strings.Join(want, " ") {
		t.Fatalf("lab asked %q, want %q", asked, want)
	}
}

// Not followed, the log is one request for all of it.
func TestABuildLogNotFollowedIsOneRequest(t *testing.T) {
	var asked []string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		io.WriteString(w, "CC a.o\n")
	})
	var out bytes.Buffer
	if err := c.BuildLog(context.Background(), "b1", false, &out); err != nil || out.String() != "CC a.o\n" || len(asked) != 1 || asked[0] != "/api/builds/b1/log" {
		t.Fatalf("%q %v, asked %q", out.String(), err, asked)
	}
}
