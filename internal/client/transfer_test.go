package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestUploadNeverSendsABodyWithoutContentLength(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.ContentLength < 0 || len(r.TransferEncoding) > 0 || r.ContentLength != int64(len(body)) {
			t.Errorf("%s %s: Content-Length %d, transfer encoding %v, body of %d bytes", r.Method, r.URL.Path, r.ContentLength, r.TransferEncoding, len(body))
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s %s came without the token", r.Method, r.URL.Path)
		}
		switch {
		case r.URL.Path == "/api/artifacts":
			json.NewEncoder(w).Encode(api.StoredArtifact{SHA256: shaOf(body), Size: int64(len(body))})
		case r.URL.Path == "/api/artifacts/uploads":
			var start api.UploadStart
			json.Unmarshal(body, &start)
			json.NewEncoder(w).Encode(api.UploadState{SHA256: start.SHA256, Size: start.Size, PartBytes: 1000, AtOnce: 1})
		case strings.HasSuffix(r.URL.Path, "/close"):
			json.NewEncoder(w).Encode(api.StoredArtifact{SHA256: strings.Split(r.URL.Path, "/")[4]})
		}
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: "tok", http: srv.Client(), partBytes: 1000}
	dir := t.TempDir()
	for name, size := range map[string]int{"small": 900, "large": 2100} {
		data := bytes.Repeat([]byte(name[:1]), size)
		path := filepath.Join(dir, name)
		os.WriteFile(path, data, 0o600)
		if sha, err := c.Upload(context.Background(), path); err != nil || sha != shaOf(data) {
			t.Fatalf("%s: sha %q, %v", name, sha, err)
		}
	}
	large := "/api/artifacts/uploads/" + shaOf(bytes.Repeat([]byte("l"), 2100))
	slices.Sort(seen)
	want := []string{"POST /api/artifacts", "POST /api/artifacts/uploads", "POST " + large + "/close", "PUT " + large + "/parts/1", "PUT " + large + "/parts/2", "PUT " + large + "/parts/3"}
	if !slices.Equal(seen, want) {
		t.Fatalf("requests %v, want %v", seen, want)
	}
}

// cutServer serves data, honouring a Range, and cuts each of its first cuts
// answers after 1000 bytes of body.
func cutServer(t *testing.T, data []byte, cuts int) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		cut := len(ranges) <= cuts
		mu.Unlock()
		if !cut {
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
			return
		}
		rec := httptest.NewRecorder()
		http.ServeContent(rec, r, "", time.Time{}, bytes.NewReader(data))
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes()[:min(1000, rec.Body.Len())])
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &ranges
}

func TestADownloadCutOnTheWayGoesOnFromWhereItStopped(t *testing.T) {
	var data []byte
	for i := 0; len(data) < 3500; i++ {
		data = fmt.Appendf(data, "%08d ", i)
	}
	srv, ranges := cutServer(t, data, 2)
	c := &Client{URL: srv.URL, Token: "tok", http: srv.Client(), pause: func(context.Context, int) error { return nil }}
	dst := filepath.Join(t.TempDir(), "pkg.tar.zst")
	sha, err := c.BuildFile(context.Background(), "b1", "pkg.tar.zst", dst)
	if err != nil || sha != shaOf(data) {
		t.Fatalf("sha %q, %v", sha, err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("the downloaded file is not the one served")
	}
	if want := []string{"", "bytes=1000-", "bytes=2000-"}; !slices.Equal(*ranges, want) {
		t.Fatalf("ranges asked for: %q, want %q", *ranges, want)
	}
	if _, err := os.Stat(dst + ".part"); err == nil {
		t.Fatal("the partial file is still there")
	}
}

func TestADownloadGoesOnFromAnEarlierRunsPartialFile(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 300)
	for what, c := range map[string]struct {
		part       []byte
		wantRanges []string
	}{
		"a partial file":                  {data[:1200], []string{"bytes=1200-"}},
		"a whole file never renamed":      {data, []string{"bytes=3000-"}},
		"a longer file of something else": {bytes.Repeat([]byte("x"), 4000), []string{"bytes=4000-", ""}},
	} {
		srv, ranges := cutServer(t, data, 0)
		cl := &Client{URL: srv.URL, Token: "tok", http: srv.Client(), pause: func(context.Context, int) error { return nil }}
		dst := filepath.Join(t.TempDir(), "pkg")
		os.WriteFile(dst+".part", c.part, 0o600)
		sha, err := cl.BuildFile(context.Background(), "b1", "pkg", dst)
		if err != nil || sha != shaOf(data) {
			t.Fatalf("%s: sha %q, %v", what, sha, err)
		}
		if !slices.Equal(*ranges, c.wantRanges) {
			t.Errorf("%s: ranges asked for %q, want %q", what, *ranges, c.wantRanges)
		}
	}
}

func TestADownloadLabdRefusesIsNotAskedForAgain(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		http.Error(w, "build b1 has no file \"pkg\"", 404)
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, Token: "tok", http: srv.Client(), pause: func(context.Context, int) error { return nil }}
	dst := filepath.Join(t.TempDir(), "pkg")
	if _, err := c.BuildFile(context.Background(), "b1", "pkg", dst); err == nil || !strings.Contains(err.Error(), "has no file") || asked != 1 {
		t.Fatalf("err %v after %d requests", err, asked)
	}
}
