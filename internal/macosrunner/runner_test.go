package macosrunner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// fakeLab is labd as the runner sees it: one run, its artifacts, the upload.
type fakeLab struct {
	t       *testing.T
	mu      sync.Mutex
	run     *api.MacOSRun
	files   map[string][]byte
	got     *api.MacOSResults
	enrols  int
	fetched []string
	srv     *httptest.Server
}

func newFakeLab(t *testing.T, run *api.MacOSRun, files map[string][]byte) *fakeLab {
	f := &fakeLab{t: t, run: run, files: map[string][]byte{}}
	if run != nil {
		for name, b := range files {
			sum := sha256.Sum256(b)
			sha := hex.EncodeToString(sum[:])
			f.files[sha] = b
			run.Artifacts = append(run.Artifacts, api.MacOSArtifact{Name: name, SHA256: sha})
		}
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLab) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := r.Header.Get("Authorization")
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/macos/runner/enroll":
		if auth != "Bearer mos_enr_once" || f.enrols > 0 {
			http.Error(w, "used", 401)
			return
		}
		f.enrols++
		json.NewEncoder(w).Encode(api.MacOSEnrolled{Mac: "m2-max", Secret: "mos_secret"})
		return
	case auth != "Bearer mos_secret":
		http.Error(w, "unknown macOS runner secret", 401)
	case r.URL.Path == "/api/macos/runner/poll":
		json.NewEncoder(w).Encode(api.MacOSPoll{Run: f.run})
	case strings.HasPrefix(r.URL.Path, "/api/macos/runner/artifacts/"):
		sha := strings.TrimPrefix(r.URL.Path, "/api/macos/runner/artifacts/")
		f.fetched = append(f.fetched, sha)
		b, ok := f.files[sha]
		if !ok {
			http.Error(w, "no", 404)
			return
		}
		w.Write(b)
	case r.URL.Path == "/api/macos/runner/results":
		var up api.MacOSResults
		json.NewDecoder(r.Body).Decode(&up)
		f.got = &up
		w.Write([]byte(`{}`))
	default:
		f.t.Errorf("asked %s %s", r.Method, r.URL.Path)
		http.Error(w, "no", 404)
	}
}

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// webBundle is the pinned web benchmarks: each driver page posts its
// result to the local server; the fake browser does that itself.
func webBundle(t *testing.T) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"driver/speedometer.html", "driver/motionmark.html", "driver/jetstream.html", "speedometer/index.html"} {
		b := []byte("<html></html>")
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg})
		tw.Write(b)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeTools are 7zz, Geekbench, FFmpeg and two browsers that print what the
// real ones do.
func fakeTools(t *testing.T) Tools {
	dir := t.TempDir()
	browser := `url=""; for a in "$@"; do url="$a"; done
base="${url%%/driver/*}"; name="${url##*/driver/}"; name="${name%%.html*}"
curl -fsS -d '{"value":'"$VALUE"',"unit":"score","spread":1.5,"version":"9.9"}' "$base/result/$name" >/dev/null || { echo "url=$url" >&2; exit 1; }
sleep 30
`
	return Tools{
		SevenZip: script(t, dir, "7zz", `[ "$1" = b ] || exit 2
echo "7-Zip (z) 24.08 (arm64)"
echo "Avr:             796   4545  36167               1158   4563  52863"
echo "Tot:             977   4554  44515"
`),
		Geekbench: script(t, dir, "geekbench6", `out=""; while [ $# -gt 0 ]; do case "$1" in --export-json) out="$2"; shift;; --gpu) gpu="$2"; shift;; esac; shift; done
if [ -n "$gpu" ]; then echo "{\"version\":\"6.4.0\",\"score\":150000}" > "$out"; else echo "{\"version\":\"6.4.0\",\"score\":2900,\"multicore_score\":15000}" > "$out"; fi
`),
		FFmpeg: script(t, dir, "ffmpeg", `echo "$@" | grep -q -- "-hwaccel videotoolbox" || exit 3
echo "frame=  100 fps=0.0 q=-0.0 size=N/A time=00:00:01.00" >&2
echo "frame=  600 fps=300 q=-0.0 Lsize=N/A time=00:00:10.00" >&2
`),
		Chrome:   script(t, dir, "chrome", "VALUE=400\n"+browser),
		Chromium: script(t, dir, "chromium", "VALUE=380\n"+browser),
	}
}

func newRunner(t *testing.T, lab *fakeLab, tools Tools) (*Runner, *int) {
	rebooted := new(int)
	return &Runner{
		Config:         Config{Server: lab.srv.URL, Mac: "m2-max", Secret: "mos_secret"},
		Dir:            t.TempDir(),
		Tools:          tools,
		Reboot:         func() error { *rebooted++; return nil },
		BrowserTimeout: 20 * time.Second,
		Log:            t.Logf,
	}, rebooted
}

func byName(rs []api.BenchResult) map[string]api.BenchResult {
	m := map[string]api.BenchResult{}
	for _, r := range rs {
		m[r.Name] = r
	}
	return m
}

func TestNoRunQueuedLeavesTheMacInMacOS(t *testing.T) {
	lab := newFakeLab(t, nil, nil)
	r, rebooted := newRunner(t, lab, fakeTools(t))
	if err := r.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *rebooted != 0 || lab.got != nil {
		t.Errorf("rebooted %d, uploaded %+v", *rebooted, lab.got)
	}
}

func TestADefaultRunRunsItsListUploadsAndReboots(t *testing.T) {
	lab := newFakeLab(t, &api.MacOSRun{ID: "mos-1", Mac: "m2-max", Tier: api.TierDefault, State: api.MacOSRunning},
		map[string][]byte{"web-benchmarks": webBundle(t)})
	r, rebooted := newRunner(t, lab, fakeTools(t))
	start := time.Now()
	if err := r.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 15*time.Second {
		t.Errorf("the browser was not stopped once it reported: %v", time.Since(start))
	}
	if *rebooted != 1 {
		t.Errorf("rebooted %d times", *rebooted)
	}
	if lab.got == nil || lab.got.RunID != "mos-1" {
		t.Fatalf("uploaded %+v", lab.got)
	}
	got := byName(lab.got.Results)
	want := []string{"speedometer/chrome", "motionmark/chrome", "7zip", "glmark2", "vkmark", "boot-time"}
	if len(got) != len(want) {
		t.Errorf("results %v, want %v", lab.got.Results, want)
	}
	for _, n := range want {
		if _, ok := got[n]; !ok {
			t.Errorf("no %s", n)
		}
	}
	if s := got["speedometer/chrome"]; s.Value == nil || *s.Value != 400 || s.Spread != 1.5 || !s.HigherIsBetter || s.Version != "9.9" || s.Unit != "score" {
		t.Errorf("speedometer: %+v", s)
	}
	if z := got["7zip"]; z.Value == nil || *z.Value != 44515 || z.Unit != "MIPS" || !z.HigherIsBetter {
		t.Errorf("7zip: %+v", z)
	}
	for _, n := range []string{"glmark2", "vkmark", "boot-time"} {
		if g := got[n]; g.Value != nil || g.Note != api.NoMacOSEquivalent {
			t.Errorf("%s: %+v", n, g)
		}
	}
	b, _ := json.Marshal(lab.got.Results[0])
	for _, k := range []string{`"name"`, `"version"`, `"value"`, `"unit"`, `"higher_is_better"`, `"spread"`, `"seconds"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("a result lacks %s: %s", k, b)
		}
	}
}

func TestAnExtendedRunAddsTheRestLabellingOtherAPIs(t *testing.T) {
	lab := newFakeLab(t, &api.MacOSRun{ID: "mos-2", Mac: "m2-max", Tier: api.TierExtended, State: api.MacOSRunning},
		map[string][]byte{"web-benchmarks": webBundle(t), "video-sample": []byte("not really a video")})
	r, _ := newRunner(t, lab, fakeTools(t))
	if err := r.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := byName(lab.got.Results)
	for _, n := range []string{"speedometer/chromium", "motionmark/chromium", "jetstream/chrome", "jetstream/chromium"} {
		if v := got[n]; v.Value == nil {
			t.Errorf("%s: %+v", n, v)
		}
	}
	if v := got["speedometer/chromium"]; *v.Value != 380 {
		t.Errorf("chromium ran as chrome: %+v", v)
	}
	if g := got["geekbench6-cpu-single"]; g.Value == nil || *g.Value != 2900 || g.Version != "6.4.0" {
		t.Errorf("geekbench single: %+v", g)
	}
	if g := got["geekbench6-cpu-multi"]; g.Value == nil || *g.Value != 15000 {
		t.Errorf("geekbench multi: %+v", g)
	}
	if g := got["geekbench6-gpu"]; g.Value == nil || *g.Value != 150000 || g.API != "Metal" {
		t.Errorf("geekbench gpu: %+v", g)
	}
	if v := got["video-decode"]; v.Value == nil || v.API != "VideoToolbox" || v.Unit != "fps" || !v.HigherIsBetter || *v.Value <= 0 {
		t.Errorf("video decode: %+v", v)
	}
	for _, n := range []string{"kernel-compile", "vkpeak", "clpeak", "suspend-resume", "glmark2", "vkmark"} {
		if g := got[n]; g.Value != nil || g.Note != api.NoMacOSEquivalent {
			t.Errorf("%s: %+v", n, g)
		}
	}
}

func TestAMissingToolIsSaidAndTheRunGoesOn(t *testing.T) {
	lab := newFakeLab(t, &api.MacOSRun{ID: "mos-3", Mac: "m2-max", Tier: api.TierDefault, State: api.MacOSRunning},
		map[string][]byte{"web-benchmarks": webBundle(t)})
	tools := fakeTools(t)
	tools.SevenZip = filepath.Join(t.TempDir(), "no-7zz")
	tools.Chrome = filepath.Join(t.TempDir(), "no-chrome")
	r, rebooted := newRunner(t, lab, tools)
	if err := r.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := byName(lab.got.Results)
	if z := got["7zip"]; z.Value != nil || !strings.Contains(z.Note, "not installed") {
		t.Errorf("7zip: %+v", z)
	}
	if s := got["speedometer/chrome"]; s.Value != nil || s.Note == "" {
		t.Errorf("speedometer: %+v", s)
	}
	if *rebooted != 1 {
		t.Errorf("rebooted %d", *rebooted)
	}
}

// An artifact that is not what labd pinned is not used, and the Mac still
// goes back to Omarchy.
func TestAnArtifactNotMatchingItsSHA256IsRefused(t *testing.T) {
	run := &api.MacOSRun{ID: "mos-4", Mac: "m2-max", Tier: api.TierDefault, State: api.MacOSRunning}
	lab := newFakeLab(t, run, map[string][]byte{"web-benchmarks": webBundle(t)})
	lab.files[run.Artifacts[0].SHA256] = []byte("changed")
	r, rebooted := newRunner(t, lab, fakeTools(t))
	if err := r.Boot(context.Background()); err == nil {
		t.Error("ran with a changed artifact")
	}
	if lab.got == nil || !strings.Contains(lab.got.Error, "sha256") {
		t.Errorf("uploaded %+v", lab.got)
	}
	if *rebooted != 1 {
		t.Errorf("rebooted %d", *rebooted)
	}
}

func TestEnrollKeepsTheSecretPrivate(t *testing.T) {
	lab := newFakeLab(t, nil, nil)
	path := filepath.Join(t.TempDir(), "etc", "macos-runner.json")
	if err := Enroll(context.Background(), lab.srv.URL, "mos_enr_once", "maralc", path); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	c, err := LoadConfig(path)
	if err != nil || c.Secret != "mos_secret" || c.Mac != "m2-max" || c.GUIUser != "maralc" || c.Server != lab.srv.URL {
		t.Errorf("%+v %v", c, err)
	}
	if err := Enroll(context.Background(), lab.srv.URL, "mos_enr_once", "", path); err == nil {
		t.Error("enrolled twice with one token")
	}
}

func TestSevenZipAndFFmpegOutputIsRead(t *testing.T) {
	if v, err := parse7zip("junk\nTot:             977   4554  44515\n"); err != nil || v != 44515 {
		t.Errorf("%v %v", v, err)
	}
	if _, err := parse7zip("nothing"); err == nil {
		t.Error("read a number from nothing")
	}
	if n := lastFrame("frame=  100 fps=0\nframe=  600 fps=300 q=-0.0\n"); n != 600 {
		t.Errorf("frames %d", n)
	}
}
