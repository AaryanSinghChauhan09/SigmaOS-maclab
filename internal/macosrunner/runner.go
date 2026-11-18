// Package macosrunner is lab-macos-runner: in macOS on a lab Mac, at boot,
// it asks labd whether a macOS run is queued for this Mac and, if one is,
// runs the tier's benchmarks, uploads the results and reboots. The Mac was
// booted into macOS for one boot only (lab-agent boot-macos-once, asahi-bless
// --next), so the reboot returns it to Omarchy.
package macosrunner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// Version is the runner's own number.
var Version = "0.1.0"

// ConfigPath is where enroll keeps the runner's secret, root's only.
const ConfigPath = "/Library/Application Support/Omarchy Mac Lab/macos-runner.json"

// WorkDir is where a run keeps its artifacts and scratch files.
const WorkDir = "/Library/Application Support/Omarchy Mac Lab/work"

// Config is the enrolled runner.
type Config struct {
	Server  string `json:"server"`
	Mac     string `json:"mac"`
	Secret  string `json:"secret"`
	GUIUser string `json:"gui_user,omitempty"` // whose desktop the browsers run in
}

func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Server == "" || c.Secret == "" {
		return c, fmt.Errorf("%s: not enrolled", path)
	}
	return c, nil
}

// Enroll spends the one-time token from `lab macos enroll` for this Mac's
// runner secret and keeps it at path, mode 0600.
func Enroll(ctx context.Context, server, token, guiUser, path string) error {
	server = strings.TrimRight(server, "/")
	var e api.MacOSEnrolled
	if err := call(ctx, http.DefaultClient, "POST", server+"/api/macos/runner/enroll", token, nil, &e); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(Config{Server: server, Mac: e.Mac, Secret: e.Secret, GUIUser: guiUser}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Tools are the programs the benchmarks run; "" is the usual place.
type Tools struct {
	SevenZip, Geekbench, FFmpeg, Chrome, Chromium string
}

func (t Tools) withDefaults() Tools {
	or := func(s, d string) string {
		if s == "" {
			return d
		}
		return s
	}
	t.SevenZip = or(t.SevenZip, "/opt/homebrew/bin/7zz")
	t.Geekbench = or(t.Geekbench, "/Applications/Geekbench 6.app/Contents/Resources/geekbench6")
	t.FFmpeg = or(t.FFmpeg, "/opt/homebrew/bin/ffmpeg")
	t.Chrome = or(t.Chrome, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	t.Chromium = or(t.Chromium, "/Applications/Chromium.app/Contents/MacOS/Chromium")
	return t
}

// Runner is one boot's run.
type Runner struct {
	Config Config
	Dir    string
	Tools  Tools
	HTTP   *http.Client
	// Reboot returns the Mac to Omarchy; nil is shutdown -r now.
	Reboot func() error
	// BrowserTimeout bounds one browser benchmark; 0 is 15 minutes.
	BrowserTimeout time.Duration
	// NetworkWait is how long the first poll retries while the network
	// comes up after boot; 0 is 5 minutes.
	NetworkWait time.Duration
	Log         func(string, ...any)

	files map[string]string // the run's artifacts by name
	web   string            // the web bundle, unpacked
}

func (r *Runner) say(f string, a ...any) {
	if r.Log != nil {
		r.Log(f, a...)
	}
}

func (r *Runner) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (r *Runner) url(p string) string { return strings.TrimRight(r.Config.Server, "/") + p }

// Boot is the LaunchDaemon's run at boot. With no run queued it does
// nothing: the Mac stays in macOS, booted by a person. With one, whatever
// happens, it reports and reboots.
func (r *Runner) Boot(ctx context.Context) error {
	run, err := r.poll(ctx)
	if err != nil || run == nil {
		if run == nil && err == nil {
			r.say("no macOS run queued for %s", r.Config.Mac)
		}
		return err
	}
	r.say("macOS run %s (%s) on %s", run.ID, run.Tier, r.Config.Mac)
	results, runErr := r.Run(ctx, run)
	up := api.MacOSResults{RunID: run.ID, Results: results}
	if runErr != nil {
		up.Error = runErr.Error()
	}
	var upErr error
	for i := 0; i < 5; i++ {
		if upErr = call(ctx, r.client(), "POST", r.url("/api/macos/runner/results"), r.Config.Secret, up, nil); upErr == nil {
			break
		}
		r.say("upload: %v", upErr)
		sleep(ctx, 10*time.Second)
	}
	reboot := r.Reboot
	if reboot == nil {
		reboot = func() error { return exec.Command("/sbin/shutdown", "-r", "now").Run() }
	}
	r.say("rebooting to Omarchy")
	return errors.Join(runErr, upErr, reboot())
}

func (r *Runner) poll(ctx context.Context) (*api.MacOSRun, error) {
	wait := r.NetworkWait
	if wait == 0 {
		wait = 5 * time.Minute
	}
	end := time.Now().Add(wait)
	for {
		var p api.MacOSPoll
		err := call(ctx, r.client(), "GET", r.url("/api/macos/runner/poll"), r.Config.Secret, nil, &p)
		var se *statusError
		if err == nil || errors.As(err, &se) || time.Now().After(end) {
			return p.Run, err
		}
		r.say("labd not reached yet: %v", err)
		sleep(ctx, 10*time.Second)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// fetch gets the run's artifacts into the work directory by name, each
// checked against its sha256.
func (r *Runner) fetch(ctx context.Context, run *api.MacOSRun) (map[string]string, error) {
	dir := filepath.Join(r.Dir, "artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	got := map[string]string{}
	for _, a := range run.Artifacts {
		req, _ := http.NewRequestWithContext(ctx, "GET", r.url("/api/macos/runner/artifacts/"+a.SHA256), nil)
		body, err := do(r.client(), req, r.Config.Secret)
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", a.Name, err)
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			return nil, fmt.Errorf("artifact %s: its sha256 is not the pinned %s", a.Name, a.SHA256)
		}
		p := filepath.Join(dir, a.SHA256)
		if err := os.WriteFile(p, body, 0o600); err != nil {
			return nil, err
		}
		got[a.Name] = p
	}
	return got, nil
}

// untar unpacks a .tar.gz into dir, refusing any path out of it.
func untar(file, dir string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("%s: %s is outside the bundle", file, h.Name)
		}
		p := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(h.Mode)&0o755|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}

type statusError struct {
	Code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

func call(ctx context.Context, c *http.Client, method, u, secret string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, u, body)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	b, err := do(c, req, secret)
	if err == nil && out != nil {
		err = json.Unmarshal(b, out)
	}
	return err
}

func do(c *http.Client, req *http.Request, secret string) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("User-Agent", "lab-macos-runner/"+Version)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, &statusError{Code: resp.StatusCode, msg: fmt.Sprintf("labd: %s: %s", resp.Status, strings.TrimSpace(string(b)))}
	}
	return b, nil
}
