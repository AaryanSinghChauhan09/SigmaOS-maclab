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

func quiet(srv *httptest.Server) *Client {
	return &Client{URL: srv.URL, Token: "tok", http: srv.Client(), pause: func(context.Context, int) error { return nil }}
}

// noPartialFile fails if the download into dst left anything beside it.
func noPartialFile(t *testing.T, what, dst string) {
	t.Helper()
	left, _ := filepath.Glob(dst + ".*")
	if len(left) != 0 {
		t.Errorf("%s: left beside the file: %v", what, left)
	}
}

func TestADownloadCutOnTheWayGoesOnFromWhereItStopped(t *testing.T) {
	var data []byte
	for i := 0; len(data) < 3500; i++ {
		data = fmt.Appendf(data, "%08d ", i)
	}
	srv, ranges := cutServer(t, data, 2)
	dst := filepath.Join(t.TempDir(), "pkg.tar.zst")
	if err := quiet(srv).BuildFile(context.Background(), "b1", "pkg.tar.zst", shaOf(data), dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Fatal("the downloaded file is not the one served")
	}
	if want := []string{"", "bytes=1000-", "bytes=2000-"}; !slices.Equal(*ranges, want) {
		t.Fatalf("ranges asked for: %q, want %q", *ranges, want)
	}
	noPartialFile(t, "a download cut twice", dst)
}

// A partial file is gone on from only when it is a part of the same
// artifact: the SHA-256 kept beside it says which.
func TestADownloadGoesOnOnlyFromAPartialFileOfTheSameArtifact(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 300)
	other := bytes.Repeat([]byte("abcdefghij"), 500)
	for what, c := range map[string]struct {
		part       []byte
		partOf     string
		wantRanges []string
	}{
		"a partial file":                           {data[:1200], shaOf(data), []string{"bytes=1200-"}},
		"a whole file never renamed":               {data, shaOf(data), []string{"bytes=3000-"}},
		"a longer file under this SHA-256":         {bytes.Repeat([]byte("x"), 4000), shaOf(data), []string{"bytes=4000-", ""}},
		"other bytes under this SHA-256":           {other[:1200], shaOf(data), []string{"bytes=1200-", ""}},
		"another build's file of the same name":    {other[:1200], shaOf(other), []string{""}},
		"a partial file that says nothing of what": {data[:1200], "", []string{""}},
	} {
		srv, ranges := cutServer(t, data, 0)
		dst := filepath.Join(t.TempDir(), "pkg")
		os.WriteFile(dst+".part", c.part, 0o600)
		if c.partOf != "" {
			os.WriteFile(dst+".part.sha256", []byte(c.partOf+"\n"), 0o600)
		}
		if err := quiet(srv).BuildFile(context.Background(), "b1", "pkg", shaOf(data), dst); err != nil {
			t.Errorf("%s: %v", what, err)
			continue
		}
		if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
			t.Errorf("%s: the downloaded file is not the one served", what)
		}
		if !slices.Equal(*ranges, c.wantRanges) {
			t.Errorf("%s: ranges asked for %q, want %q", what, *ranges, c.wantRanges)
		}
		noPartialFile(t, what, dst)
	}
}

func TestADownloadCutLeavesItsPartialFileNamedForTheNextRun(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 300)
	srv, _ := cutServer(t, data, 1)
	ctx, cancel := context.WithCancel(context.Background())
	c := quiet(srv)
	c.pause = func(context.Context, int) error { cancel(); return ctx.Err() }
	dst := filepath.Join(t.TempDir(), "pkg")
	if err := c.BuildFile(ctx, "b1", "pkg", shaOf(data), dst); err == nil {
		t.Fatal("a download cut and given up was not an error")
	}
	if part, _ := os.ReadFile(dst + ".part"); !bytes.Equal(part, data[:1000]) {
		t.Fatalf("the partial file holds %d bytes, want the 1000 that arrived", len(part))
	}
	if of, _ := os.ReadFile(dst + ".part.sha256"); strings.TrimSpace(string(of)) != shaOf(data) {
		t.Fatalf("the partial file is said to be of %q", of)
	}
}

// Bytes that are not the SHA-256 asked for never replace the file already there.
func TestADownloadThatIsNotItsSHA256LeavesTheFileThereAlone(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 300)
	srv, ranges := cutServer(t, data, 0)
	dst := filepath.Join(t.TempDir(), "pkg")
	os.WriteFile(dst, []byte("the file of an earlier run"), 0o600)
	err := quiet(srv).BuildFile(context.Background(), "b1", "pkg", shaOf([]byte("something else")), dst)
	if err == nil || !strings.Contains(err.Error(), "pkg: downloaded sha256 "+shaOf(data)) {
		t.Fatalf("err %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "the file of an earlier run" {
		t.Fatalf("the file already there is now %q", got)
	}
	if len(*ranges) != 1 {
		t.Fatalf("asked %d times, want once", len(*ranges))
	}
	noPartialFile(t, "a wrong download", dst)
}

func TestADownloadLabdRefusesIsNotAskedForAgain(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		http.Error(w, "build b1 has no file \"pkg\"", 404)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "pkg")
	if err := quiet(srv).BuildFile(context.Background(), "b1", "pkg", shaOf(nil), dst); err == nil || !strings.Contains(err.Error(), "has no file") || asked != 1 {
		t.Fatalf("err %v after %d requests", err, asked)
	}
	noPartialFile(t, "a download labd refused", dst)
}
