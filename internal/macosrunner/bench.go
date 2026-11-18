package macosrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// A benchmark of the macOS list.
type bench struct {
	name     string
	extended bool // Extended tier only
	run      func(r *Runner, ctx context.Context, b bench) api.BenchResult
	// for the browser ones
	page, browser, version string
}

// The macOS list: the Linux tiers' benchmarks, each as macOS can run it.
// Those with no macOS build say so with no value.
var benches = []bench{
	{name: "speedometer/chrome", page: "speedometer", browser: "chrome", version: "3.1", run: (*Runner).browserBench},
	{name: "motionmark/chrome", page: "motionmark", browser: "chrome", version: "1.3", run: (*Runner).browserBench},
	{name: "7zip", run: (*Runner).sevenZip},
	{name: "glmark2", run: noEquivalent},
	{name: "vkmark", run: noEquivalent},
	{name: "boot-time", run: noEquivalent},
	{name: "speedometer/chromium", extended: true, page: "speedometer", browser: "chromium", version: "3.1", run: (*Runner).browserBench},
	{name: "motionmark/chromium", extended: true, page: "motionmark", browser: "chromium", version: "1.3", run: (*Runner).browserBench},
	{name: "jetstream/chrome", extended: true, page: "jetstream", browser: "chrome", version: "2.2", run: (*Runner).browserBench},
	{name: "jetstream/chromium", extended: true, page: "jetstream", browser: "chromium", version: "2.2", run: (*Runner).browserBench},
	{name: "geekbench6-cpu", extended: true}, // two results, see Run
	{name: "geekbench6-gpu", extended: true, run: (*Runner).geekbenchGPU},
	{name: "coremark", extended: true, run: noEquivalent},
	{name: "kernel-compile", extended: true, run: noEquivalent},
	// vkpeak would need MoltenVK, which macOS does not carry.
	{name: "vkpeak", extended: true, run: noEquivalent},
	{name: "clpeak", extended: true, run: noEquivalent},
	{name: "video-decode", extended: true, run: (*Runner).videoDecode},
	{name: "suspend-resume", extended: true, run: noEquivalent},
}

func noEquivalent(_ *Runner, _ context.Context, b bench) api.BenchResult {
	return api.BenchResult{Name: b.name, Note: api.NoMacOSEquivalent}
}

func value(v float64) *float64 { return &v }

// Run runs the run's tier, every benchmark in turn; one that fails says why
// in its Note and the run goes on.
func (r *Runner) Run(ctx context.Context, run *api.MacOSRun) ([]api.BenchResult, error) {
	r.Tools = r.Tools.withDefaults()
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return nil, err
	}
	files, err := r.fetch(ctx, run)
	if err != nil {
		return nil, err
	}
	r.files = files
	var web string
	if f, ok := files["web-benchmarks"]; ok {
		web = filepath.Join(r.Dir, "web")
		os.RemoveAll(web)
		if err := untar(f, web); err != nil {
			return nil, fmt.Errorf("web-benchmarks: %w", err)
		}
	}
	r.web = web
	var out []api.BenchResult
	for _, b := range benches {
		if b.extended && run.Tier != api.TierExtended {
			continue
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		start := time.Now()
		var rs []api.BenchResult
		if b.name == "geekbench6-cpu" {
			rs = r.geekbenchCPU(ctx)
		} else {
			rs = []api.BenchResult{b.run(r, ctx, b)}
		}
		for _, res := range rs {
			if res.Value != nil && res.Seconds == 0 {
				res.Seconds = time.Since(start).Seconds()
			}
			r.say("%s: %s", res.Name, describe(res))
			out = append(out, res)
		}
	}
	return out, nil
}

func describe(r api.BenchResult) string {
	if r.Value == nil {
		return r.Note
	}
	return fmt.Sprintf("%g %s", *r.Value, r.Unit)
}

func installed(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func notInstalled(name, path string) api.BenchResult {
	return api.BenchResult{Name: name, Note: "not installed: " + path}
}

func failed(name string, err error) api.BenchResult {
	return api.BenchResult{Name: name, Note: "failed: " + err.Error()}
}

// command runs a tool with its output; a tool not ending in time is killed
// with what it started.
func command(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd.CombinedOutput()
}

var reTot = regexp.MustCompile(`(?m)^Tot:\s+.*?(\d+)\s*$`)

// parse7zip reads the total rating (MIPS) from `7zz b`.
func parse7zip(out string) (float64, error) {
	m := reTot.FindStringSubmatch(out)
	if m == nil {
		return 0, errors.New("no Tot: line in 7zz b")
	}
	return strconv.ParseFloat(m[1], 64)
}

var re7zVersion = regexp.MustCompile(`7-Zip \(z\) ([0-9.]+)`)

func (r *Runner) sevenZip(ctx context.Context, b bench) api.BenchResult {
	if !installed(r.Tools.SevenZip) {
		return notInstalled(b.name, r.Tools.SevenZip)
	}
	out, err := command(ctx, 20*time.Minute, r.Tools.SevenZip, "b")
	if err != nil {
		return failed(b.name, err)
	}
	v, err := parse7zip(string(out))
	if err != nil {
		return failed(b.name, err)
	}
	ver := ""
	if m := re7zVersion.FindSubmatch(out); m != nil {
		ver = string(m[1])
	}
	return api.BenchResult{Name: b.name, Version: ver, Value: value(v), Unit: "MIPS", HigherIsBetter: true}
}

type geekbenchJSON struct {
	Version        string  `json:"version"`
	Score          float64 `json:"score"`
	MulticoreScore float64 `json:"multicore_score"`
}

func (r *Runner) geekbench(ctx context.Context, args ...string) (geekbenchJSON, error) {
	var g geekbenchJSON
	f := filepath.Join(r.Dir, "geekbench.json")
	os.Remove(f)
	if out, err := command(ctx, 30*time.Minute, r.Tools.Geekbench, append(args, "--export-json", f)...); err != nil {
		return g, fmt.Errorf("%v: %s", err, lastLine(string(out)))
	}
	b, err := os.ReadFile(f)
	if err == nil {
		err = json.Unmarshal(b, &g)
	}
	return g, err
}

func (r *Runner) geekbenchCPU(ctx context.Context) []api.BenchResult {
	names := []string{"geekbench6-cpu-single", "geekbench6-cpu-multi"}
	if !installed(r.Tools.Geekbench) {
		return []api.BenchResult{notInstalled(names[0], r.Tools.Geekbench), notInstalled(names[1], r.Tools.Geekbench)}
	}
	start := time.Now()
	g, err := r.geekbench(ctx, "--cpu")
	if err != nil {
		return []api.BenchResult{failed(names[0], err), failed(names[1], err)}
	}
	s := time.Since(start).Seconds()
	return []api.BenchResult{
		{Name: names[0], Version: g.Version, Value: value(g.Score), Unit: "score", HigherIsBetter: true, Seconds: s},
		{Name: names[1], Version: g.Version, Value: value(g.MulticoreScore), Unit: "score", HigherIsBetter: true, Seconds: s},
	}
}

// geekbenchGPU is Geekbench's Metal score: Linux has Vulkan or OpenCL, so
// the number is labelled with its API, not compared like for like.
func (r *Runner) geekbenchGPU(ctx context.Context, b bench) api.BenchResult {
	if !installed(r.Tools.Geekbench) {
		return notInstalled(b.name, r.Tools.Geekbench)
	}
	g, err := r.geekbench(ctx, "--gpu", "Metal")
	if err != nil {
		return failed(b.name, err)
	}
	return api.BenchResult{Name: b.name, Version: g.Version, Value: value(g.Score), Unit: "score", HigherIsBetter: true, API: "Metal"}
}

var reFrame = regexp.MustCompile(`frame=\s*(\d+)`)

func lastFrame(out string) int {
	m := reFrame.FindAllStringSubmatch(out, -1)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[len(m)-1][1])
	return n
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// videoDecode decodes the pinned sample through VideoToolbox, the macOS
// decoder (Linux decodes through the AVD driver): labelled with its API.
func (r *Runner) videoDecode(ctx context.Context, b bench) api.BenchResult {
	sample, ok := r.files["video-sample"]
	if !ok {
		return api.BenchResult{Name: b.name, Note: "no video-sample artifact in this run"}
	}
	if !installed(r.Tools.FFmpeg) {
		return notInstalled(b.name, r.Tools.FFmpeg)
	}
	start := time.Now()
	out, err := command(ctx, 10*time.Minute, r.Tools.FFmpeg, "-hide_banner", "-hwaccel", "videotoolbox", "-i", sample, "-f", "null", "-")
	secs := time.Since(start).Seconds()
	if err != nil {
		return failed(b.name, fmt.Errorf("%v: %s", err, lastLine(string(out))))
	}
	n := lastFrame(string(out))
	if n == 0 || secs <= 0 {
		return failed(b.name, errors.New("ffmpeg decoded no frames"))
	}
	return api.BenchResult{Name: b.name, Value: value(float64(n) / secs), Unit: "fps", HigherIsBetter: true, Seconds: secs, API: "VideoToolbox"}
}

// browserFlags match the Linux bench's, so the two sides' browser rows compare
// like for like: headless on the GPU at 1920 by 1080, device pixel ratio 2.
var browserFlags = []string{
	"--no-first-run", "--no-default-browser-check", "--disable-sync", "--disable-extensions",
	"--disable-background-timer-throttling", "--disable-renderer-backgrounding",
	"--headless", "--enable-gpu", "--window-size=1920,1080",
	"--screen-info={0,0 3840x2160 devicePixelRatio=2}",
}

// browserResult is what a driver page of the web bundle posts when its
// benchmark ends.
type browserResult struct {
	Value          float64 `json:"value"`
	Unit           string  `json:"unit"`
	Spread         float64 `json:"spread"`
	Version        string  `json:"version"`
	HigherIsBetter *bool   `json:"higher_is_better"`
}

// browserBench serves the pinned web bundle on loopback and opens its
// driver page for the benchmark (driver/<name>.html) in the browser, which
// posts the result to /result/<name>. The browser is stopped then.
func (r *Runner) browserBench(ctx context.Context, b bench) api.BenchResult {
	if r.web == "" {
		return api.BenchResult{Name: b.name, Note: "no web-benchmarks artifact in this run"}
	}
	path := r.Tools.Chrome
	if b.browser == "chromium" {
		path = r.Tools.Chromium
	}
	if !installed(path) {
		return notInstalled(b.name, path)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return failed(b.name, err)
	}
	got := make(chan browserResult, 1)
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(r.web)))
	mux.HandleFunc("POST /result/{name}", func(w http.ResponseWriter, req *http.Request) {
		var res browserResult
		if req.PathValue("name") != b.page || json.NewDecoder(req.Body).Decode(&res) != nil {
			http.Error(w, "bad result", 400)
			return
		}
		select {
		case got <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	timeout := r.BrowserTimeout
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	profile, _ := os.MkdirTemp(r.Dir, "profile-")
	defer os.RemoveAll(profile)
	u := fmt.Sprintf("http://%s/driver/%s.html?browser=%s", ln.Addr(), b.page, b.browser)
	args := r.asGUIUser(append(append([]string{path, "--user-data-dir=" + profile}, browserFlags...), u)...)
	if profile != "" && r.Config.GUIUser != "" && os.Geteuid() == 0 {
		if u, err := user.Lookup(r.Config.GUIUser); err == nil {
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			os.Chown(profile, uid, gid)
		}
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return failed(b.name, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop := func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited
	}
	select {
	case res := <-got:
		secs := time.Since(start).Seconds()
		stop()
		hib := true
		if res.HigherIsBetter != nil {
			hib = *res.HigherIsBetter
		}
		ver := res.Version
		if ver == "" {
			ver = b.version
		}
		return api.BenchResult{Name: b.name, Version: ver, Value: value(res.Value), Unit: res.Unit, HigherIsBetter: hib, Spread: res.Spread, Seconds: secs}
	case err := <-exited:
		return failed(b.name, fmt.Errorf("the browser ended (%v) before the benchmark reported: %s", err, strings.TrimSpace(stderr.String())))
	case <-time.After(timeout):
		stop()
		return failed(b.name, fmt.Errorf("no result in %v", timeout))
	case <-ctx.Done():
		stop()
		return failed(b.name, ctx.Err())
	}
}

// asGUIUser runs a browser in the GUI user's desktop session when the
// runner is root (the LaunchDaemon): a browser needs a window server.
func (r *Runner) asGUIUser(args ...string) []string {
	if r.Config.GUIUser == "" || os.Geteuid() != 0 {
		return args
	}
	u, err := user.Lookup(r.Config.GUIUser)
	if err != nil {
		return args
	}
	return append([]string{"/bin/launchctl", "asuser", u.Uid, "/usr/bin/sudo", "-u", r.Config.GUIUser}, args...)
}
