package minitest

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/server"
)

const agentsGroup = "YWdlbnRzLWdyb3VwLWlkLWFnZW50cy1ncm91cC1pZC0="

// agents stands in for signal-cli: it keeps what labd's sender would post
// to "Agents".
type agents struct {
	mu    sync.Mutex
	lines []string
}

func (a *agents) Send(_ context.Context, group, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if group == agentsGroup {
		a.lines = append(a.lines, text)
	}
	return nil
}

// posted waits until "Agents" has n lines, and a moment more for one too many.
func (a *agents) posted(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.mu.Lock()
		got := append([]string{}, a.lines...)
		a.mu.Unlock()
		if len(got) >= n {
			if len(got) == n {
				time.Sleep(150 * time.Millisecond)
				a.mu.Lock()
				got = append([]string{}, a.lines...)
				a.mu.Unlock()
			}
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("\"Agents\" got %d lines, want %d: %q", len(got), n, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// withLabd gives the mini a real labd's ops socket where the scripts look
// for it, a curl that writes down its arguments, and "Agents".
func (m *mini) withLabd() (*agents, net.Listener) {
	m.t.Helper()
	a := &agents{}
	groups := m.t.TempDir()
	write(m.t, filepath.Join(groups, "signal-agents-group"), agentsGroup+"\n", 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	s, err := server.New(ctx, server.Config{DataDir: m.t.TempDir(), Signal: a, SignalDir: groups}, log.New(io.Discard, "", 0))
	if err != nil {
		cancel()
		m.t.Fatal(err)
	}
	for _, d := range []string{"var/lab/run", "var/lab/log"} {
		if err := os.MkdirAll(m.path(d), 0o700); err != nil {
			m.t.Fatal(err)
		}
	}
	ln, err := server.ListenOps(m.path("var/lab/run/ops.sock"))
	if err != nil {
		cancel()
		m.t.Fatal(err)
	}
	ops := s.OpsServer()
	go ops.Serve(ln)
	m.t.Cleanup(func() { ops.Close(); cancel() })
	m.wrapCurl()
	return a, ln
}

func (m *mini) wrapCurl() {
	m.t.Helper()
	curl, err := exec.LookPath("curl")
	if err != nil {
		m.t.Skip("needs curl")
	}
	write(m.t, m.bin+"/curl", "#!/bin/bash\necho \"curl $*\" >> \"$STUB_LOG\"\nexec "+curl+" \"$@\"\n", 0o755)
}

func (m *mini) notified() string { return read(m.t, m.path("var/lab/log/notify.log")) }

func TestNotifyReachesAgentsThroughLabdsOpsSocket(t *testing.T) {
	m := newMini(t)
	a, _ := m.withLabd()
	const line = "container's server on the mini was found stopped; starting it again"

	out := m.ok("notify", "agents", line)

	if got := a.posted(t, 1); len(got) != 1 || got[0] != line {
		t.Fatalf("\"Agents\" got %q\n%s", got, out)
	}
	if got := m.notified(); !strings.Contains(got, " agents "+line) || strings.Count(got, "\n") != 1 {
		t.Errorf("notify.log holds %q", got)
	}
	// The line is the request's body: curl's arguments name the socket and
	// the route, and carry neither the line nor anything that opens a door.
	var asked int
	for _, call := range strings.Split(m.calls(), "\n") {
		if !strings.HasPrefix(call, "curl ") {
			continue
		}
		asked++
		if !strings.Contains(call, "--unix-socket "+m.path("var/lab/run/ops.sock")) || !strings.HasSuffix(call, " http://labd/agents/line") {
			t.Errorf("curl was run as: %s", call)
		}
		for _, never := range []string{"found stopped", "Authorization", "Bearer", "token", "-u ", "--user"} {
			if strings.Contains(call, never) {
				t.Errorf("curl's arguments hold %q: %s", never, call)
			}
		}
	}
	if asked != 1 {
		t.Errorf("curl ran %d times", asked)
	}
	// The socket is the lab account's alone.
	if st, err := os.Lstat(m.path("var/lab/run/ops.sock")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the ops socket: %v %v", st, err)
	}
}

func TestATroubleIsToldOnceAndItsRecoveryOnce(t *testing.T) {
	m := newMini(t)
	a, _ := m.withLabd()

	m.ok("notify", "recovered", "restore-drill", "The Restore drill passed again.")
	if got := m.notified(); got != "" {
		t.Errorf("a recovery from no trouble was written down: %q", got)
	}
	m.ok("notify", "trouble", "restore-drill", "The Restore drill failed: the house copy has no snapshot.")
	m.ok("notify", "trouble", "restore-drill", "The Restore drill failed: the house copy has no snapshot.")
	m.ok("notify", "recovered", "restore-drill", "The Restore drill passed again.")
	m.ok("notify", "recovered", "restore-drill", "The Restore drill passed again.")

	got := a.posted(t, 2)
	if len(got) != 2 || got[0] != "The Restore drill failed: the house copy has no snapshot." || got[1] != "The Restore drill passed again." {
		t.Fatalf("\"Agents\" got %q", got)
	}
	log := m.notified()
	if strings.Count(log, " trouble:restore-drill The Restore drill failed") != 2 || strings.Count(log, " recovered:restore-drill The Restore drill passed again.") != 1 || strings.Count(log, "\n") != 3 {
		t.Errorf("notify.log holds:\n%s", log)
	}
}

func TestWithNoLabdTheLineIsWrittenDownAndSaidNotSent(t *testing.T) {
	m := newMini(t)
	for _, d := range []string{"var/lab/run", "var/lab/log"} {
		os.MkdirAll(m.path(d), 0o700)
	}
	out := m.refused("notify", "agents", "The nightly backup failed: the house copy.")
	if !strings.Contains(out, "the line was not sent") || !strings.Contains(m.notified(), " agents The nightly backup failed: the house copy.") {
		t.Errorf("with no socket:\n%s\nnotify.log: %q", out, m.notified())
	}

	// A labd that stopped leaves its socket behind.
	a, ln := m.withLabd()
	ln.Close()
	if l, err := net.Listen("unix", m.path("var/lab/run/ops.sock")); err == nil {
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		l.Close()
	}
	out = m.refused("notify", "agents", "Another line.")
	if !strings.Contains(out, "it was not sent") || !strings.Contains(m.notified(), " agents Another line.") {
		t.Errorf("with a dead socket:\n%s\nnotify.log: %q", out, m.notified())
	}
	if got := a.posted(t, 0); len(got) != 0 {
		t.Errorf("\"Agents\" got %q", got)
	}
}

func TestNotifyTakesOnlyItsThreeForms(t *testing.T) {
	m := newMini(t)
	a, _ := m.withLabd()
	for _, args := range [][]string{
		{},
		{"agents"},
		{"agents", ""},
		{"agents", "a", "b"},
		{"lab", "a line for the Lab group"},
		{"trouble", "disk"},
		{"trouble", "Not A Key", "text"},
		{"trouble", "../line", "text"},
		{"trouble", "disk", ""},
		{"recovered", "", "text"},
		{"recovered", strings.Repeat("k", 41), "text"},
	} {
		if out, code := m.run("notify", args...); code != 2 {
			t.Errorf("notify %q: exit %d\n%s", args, code, out)
		}
	}
	if got := a.posted(t, 0); len(got) != 0 || m.notified() != "" {
		t.Errorf("\"Agents\" got %q, notify.log %q", got, m.notified())
	}
}

// A failed Restore drill is told once however many drills fail, and the
// first that passes says so once.
func TestTheRestoreDrillTellsAgentsOnChangeAndOnRecovery(t *testing.T) {
	d := newDrilled(t)
	a, _ := d.withLabd()
	live := read(t, d.data+"/artifacts/"+d.artifact)

	write(t, d.data+"/artifacts/"+d.artifact, "another kernel image\n", 0o600)
	d.refused("restore-drill")
	d.refused("restore-drill")
	write(t, d.data+"/artifacts/"+d.artifact, live, 0o600)
	d.ok("restore-drill")
	d.ok("restore-drill")

	got := a.posted(t, 2)
	if len(got) != 2 || !strings.HasPrefix(got[0], "The Restore drill failed: the artifact "+d.artifact+" from the house copy differs from the live file") || got[1] != "The Restore drill passed again." {
		t.Fatalf("\"Agents\" got %q", got)
	}
}

// loadedAs has launchd hold a daemon in the given state, started so often.
func (m *mini) loadedAs(label, state string, starts int) {
	m.t.Helper()
	file := filepath.Join(filepath.Dir(m.root), "state", "loaded."+label)
	write(m.t, file, "<plist/>\n", 0o644)
	write(m.t, file+".state", state+"\n", 0o644)
	write(m.t, file+".runs", fmt.Sprint(starts)+"\n", 0o644)
}

func TestPartsSaysWhatLabdCannotSee(t *testing.T) {
	m := newMini(t)
	m.wrapCurl()
	var mu sync.Mutex
	ready := http.StatusOK
	cloudflared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(ready)
		fmt.Fprintf(w, `{"status":%d,"readyConnections":%d}`, ready, map[bool]int{true: 4}[ready == 200])
	}))
	t.Cleanup(cloudflared.Close)
	m.env = append(m.env, "LAB_BOOT=1763092800", "LAB_TUNNEL_READY="+cloudflared.URL+"/ready")

	// A mini with none of the lab's daemons yet says only when it started.
	if out := m.ok("parts"); out != "boot 1763092800\n" {
		t.Fatalf("with nothing installed:\n%s", out)
	}

	m.loadedAs(cloudflaredLabel, "running", 3)
	m.loadedAs(litestreamLabel, "running", 1)
	m.loadedAs(signalLabel, "running", 2)
	m.loadedAs(oobdLabel, "running", 1)
	m.loadedAs(builderLabel, "running", 7)
	write(t, m.path("var/lab/log/litestream.log"), "time=2026-11-14T14:00:00+10:00 level=INFO msg=\"replicating\"\n"+
		"time=2026-11-14T14:00:01+10:00 level=ERROR msg=\"sync error\" error=\"dial tcp: no route to host\"\n"+
		"time=2026-11-14T14:00:02+10:00 level=ERROR msg=\"sync error\" error=\"dial tcp: no route to host\"\n", 0o600)
	out := m.ok("parts")
	want := "boot 1763092800\ntunnel up\nservice cloudflared 3\nlitestream running 2\nservice litestream 1\nservice signal-cli 2\nservice oobd 1\nservice lab-builder 7\n"
	if out != want {
		t.Fatalf("parts says:\n%s\nwant:\n%s", out, want)
	}
	// It is what labd reads.
	got, err := server.ParseParts(out)
	if err != nil || got.Tunnel != "up" || got.Litestream != "running" || got.LitestreamErrors != 2 || got.Starts["lab-builder"] != 7 || len(got.Starts) != 5 || got.Boot.Unix() != 1763092800 {
		t.Fatalf("labd reads it as %+v, %v", got, err)
	}

	mu.Lock()
	ready = http.StatusServiceUnavailable
	mu.Unlock()
	m.loadedAs(litestreamLabel, "not running", 4)
	out = m.ok("parts")
	if !strings.Contains(out, "tunnel down\n") || !strings.Contains(out, "litestream stopped 2\nservice litestream 4\n") {
		t.Errorf("with the tunnel disconnected and Litestream stopped:\n%s", out)
	}
	if _, err := server.ParseParts(out); err != nil {
		t.Error(err)
	}
	cloudflared.Close()
	if out = m.ok("parts"); !strings.Contains(out, "tunnel down\n") {
		t.Errorf("with cloudflared not answering:\n%s", out)
	}

	// It only looks: launchd is asked to print, and nothing else of it.
	for _, call := range strings.Split(strings.TrimSpace(m.calls()), "\n") {
		if strings.HasPrefix(call, "launchctl ") && !strings.HasPrefix(call, "launchctl print system/") {
			t.Errorf("parts ran: %s", call)
		}
		if strings.HasPrefix(call, "as-lab ") || strings.HasPrefix(call, "brew ") {
			t.Errorf("parts ran: %s", call)
		}
	}
	if out, code := m.run("parts", "now"); code != 2 {
		t.Errorf("parts with an argument: exit %d\n%s", code, out)
	}
}

// A daemon whose LaunchDaemon file is installed and which launchd does not
// have is down, not absent: booted out by hand, or by a set-up or a Deploy
// that stopped between the bootout and the start. labd reads it as trouble.
func TestPartsSaysADaemonLaunchdLostIsDown(t *testing.T) {
	m := newMini(t)
	m.wrapCurl()
	m.env = append(m.env, "LAB_BOOT=1763092800", "LAB_TUNNEL_READY=http://127.0.0.1:1/ready")
	for _, label := range []string{cloudflaredLabel, litestreamLabel, signalLabel} {
		write(t, m.path("Library/LaunchDaemons/"+label+".plist"), "<plist/>\n", 0o644)
		m.loadedAs(label, "running", 1)
	}
	write(t, m.path("var/lab/log/litestream.log"), "time=2026-11-14T14:00:01+10:00 level=ERROR msg=\"sync error\"\n", 0o600)
	for _, label := range []string{cloudflaredLabel, litestreamLabel} {
		for _, ext := range []string{"", ".state", ".runs"} {
			if err := os.Remove(filepath.Join(filepath.Dir(m.root), "state", "loaded."+label+ext)); err != nil {
				t.Fatal(err)
			}
		}
	}
	out := m.ok("parts")
	if want := "boot 1763092800\ntunnel down\nlitestream stopped 1\nservice signal-cli 1\n"; out != want {
		t.Fatalf("parts says:\n%s\nwant:\n%s", out, want)
	}
	if got, err := server.ParseParts(out); err != nil || got.Tunnel != "down" || got.Litestream != "stopped" || got.LitestreamErrors != 1 {
		t.Errorf("labd reads it as %+v, %v", got, err)
	}
	if strings.Contains(m.calls(), "curl ") {
		t.Errorf("parts asked a tunnel launchd does not have:\n%s", m.calls())
	}
}

// The tunnel's answer is asked where its LaunchDaemon file has it listen.
func TestPartsAsksTheTunnelWhereItListens(t *testing.T) {
	_, arrays := plist(t, filepath.Join(repo(t), "deploy", cloudflaredLabel+".plist"))
	var metrics string
	for i, a := range arrays["ProgramArguments"] {
		if a == "--metrics" && i+1 < len(arrays["ProgramArguments"]) {
			metrics = arrays["ProgramArguments"][i+1]
		}
	}
	if want := "http://" + metrics + "/ready"; metrics == "" || shell(t, "TUNNEL_READY") != want {
		t.Errorf("TUNNEL_READY is %q, and cloudflared is started with --metrics %q", shell(t, "TUNNEL_READY"), metrics)
	}
	if !strings.HasPrefix(metrics, "127.0.0.1:") {
		t.Errorf("cloudflared's own port is %q, not loopback", metrics)
	}
}

const softwareUpdateFound = `Software Update Tool

Finding available software
Software Update found the following new or updated software:
* Label: macOS Tahoe 26.1.1-25B80
	Title: macOS Tahoe 26.1.1, Version: 26.1.1, Size: 3458440KiB, Recommended: YES, Action: restart, 
* Label: Command Line Tools for Xcode-26.1
	Title: Command Line Tools for Xcode, Version: 26.1, Size: 812345KiB, Recommended: YES, 
`

// homebrew stands in for the list Homebrew publishes and for a tap's file.
type homebrew struct {
	mu     sync.Mutex
	stable map[string]string
	rev    map[string]int
	tap    string // litestream's version in its tap's file; "" has no file
	down   bool
	asked  []string
	srv    *httptest.Server
}

func (m *mini) withHomebrew() *homebrew {
	m.t.Helper()
	need(m.t, "plutil")
	h := &homebrew{stable: map[string]string{}, rev: map[string]int{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.asked = append(h.asked, r.URL.Path)
		if h.down {
			http.Error(w, "no", http.StatusBadGateway)
			return
		}
		if name, ok := strings.CutPrefix(r.URL.Path, "/api/formula/"); ok {
			name = strings.TrimSuffix(name, ".json")
			if v := h.stable[name]; v != "" {
				fmt.Fprintf(w, `{"name":%q,"versions":{"stable":%q,"head":"HEAD","bottle":true},"revision":%d,"desc":"a tool"}`, name, v, h.rev[name])
				return
			}
		}
		if r.URL.Path == "/benbjohnson/homebrew-litestream/HEAD/litestream.rb" && h.tap != "" {
			fmt.Fprintf(w, "class Litestream < Formula\n  desc \"Streaming replication\"\n  version %q\n  license \"Apache-2.0\"\nend\n", h.tap)
			return
		}
		http.NotFound(w, r)
	}))
	m.t.Cleanup(h.srv.Close)
	m.env = append(m.env, "LAB_FORMULA_LIST="+h.srv.URL+"/api/formula", "LAB_TAP_FILES="+h.srv.URL)
	write(m.t, m.tree+"/deploy/homebrew-formulae", read(m.t, filepath.Join(repo(m.t), "deploy", "homebrew-formulae")), 0o644)
	write(m.t, m.path("usr/local/lab/tools/current/VERSIONS"), "ca-certificates 2026-09-09\ncloudflared 2026.10.1\nlitestream 0.5.18\nrestic 0.19.1_1\nsignal-cli 0.14.10\n", 0o644)
	h.stable = map[string]string{"cloudflared": "2026.10.1", "restic": "0.19.1", "signal-cli": "0.14.10"}
	h.rev["restic"] = 1
	h.tap = "0.5.18"
	return h
}

func (m *mini) softwareUpdate(script string) {
	m.t.Helper()
	write(m.t, m.bin+"/softwareupdate", "#!/bin/bash\necho \"softwareupdate $*\" >> \"$STUB_LOG\"\n"+script, 0o755)
}

func TestPendingUpdatesListsMacOSAndTheHomebrewToolsWithANewerVersion(t *testing.T) {
	m := newMini(t)
	h := m.withHomebrew()

	// Nothing waits: nothing is said.
	m.softwareUpdate("echo 'Software Update Tool'; echo; echo 'Finding available software'; echo 'No new software available.' >&2\n")
	if out := m.ok("pending-updates"); out != "" {
		t.Fatalf("with nothing waiting:\n%s", out)
	}

	m.softwareUpdate("cat <<'END'\n" + softwareUpdateFound + "END\n")
	h.mu.Lock()
	h.stable["cloudflared"] = "2026.11.0"
	h.rev["restic"] = 2
	h.tap = "0.5.19"
	h.mu.Unlock()
	out := m.ok("pending-updates")
	want := "macOS: macOS Tahoe 26.1.1-25B80\nmacOS: Command Line Tools for Xcode-26.1\ncloudflared 2026.10.1 -> 2026.11.0\nlitestream 0.5.18 -> 0.5.19\nrestic 0.19.1_1 -> 0.19.1_2\n"
	if out != want {
		t.Fatalf("pending-updates says:\n%s\nwant:\n%s", out, want)
	}

	// It asks Software Update to list and nothing else, and no brew runs.
	calls := m.calls()
	for _, call := range strings.Split(strings.TrimSpace(calls), "\n") {
		if strings.HasPrefix(call, "softwareupdate ") && call != "softwareupdate --list" {
			t.Errorf("Software Update was asked: %s", call)
		}
		if strings.HasPrefix(call, "brew ") || strings.HasPrefix(call, "installer ") || strings.HasPrefix(call, "launchctl ") {
			t.Errorf("pending-updates ran: %s", call)
		}
	}
	// Only the lab's own list is asked about: nothing a tool merely needs.
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, path := range h.asked {
		if strings.Contains(path, "ca-certificates") {
			t.Errorf("Homebrew's list was asked for %s", path)
		}
	}
}

func TestPendingUpdatesSaysWhatItCouldNotAsk(t *testing.T) {
	m := newMini(t)
	h := m.withHomebrew()
	m.softwareUpdate("echo 'the server is not reachable' >&2; exit 1\n")
	h.mu.Lock()
	h.down = true
	h.mu.Unlock()
	out := m.ok("pending-updates")
	want := "macOS: Software Update did not answer\ncloudflared: its newest version could not be read\nsignal-cli: its newest version could not be read\nlitestream: its newest version could not be read\nrestic: its newest version could not be read\n"
	if out != want {
		t.Fatalf("pending-updates says:\n%s\nwant:\n%s", out, want)
	}

	// An answer that is no version is not passed on.
	h.mu.Lock()
	h.down = false
	h.stable["cloudflared"] = "1.0; rm -rf /"
	h.tap = "$(reboot)"
	h.mu.Unlock()
	m.softwareUpdate("echo 'No new software available.'\n")
	out = m.ok("pending-updates")
	if want := "cloudflared: its newest version could not be read\nlitestream: its newest version could not be read\n"; out != want {
		t.Fatalf("with answers that are no version:\n%s\nwant:\n%s", out, want)
	}
}

func TestPendingUpdatesDoesNotWaitForASoftwareUpdateThatHangs(t *testing.T) {
	m := newMini(t)
	m.withHomebrew()
	m.softwareUpdate("exec sleep 300\n")
	m.env = append(m.env, "LAB_UPDATE_LIMIT=1")
	started := time.Now()
	out := m.ok("pending-updates")
	if !strings.Contains(out, "macOS: Software Update did not answer\n") || time.Since(started) > 30*time.Second {
		t.Errorf("after %s:\n%s", time.Since(started).Round(time.Second), out)
	}
}

func TestAMiniWithNoToolsYetHasNoHomebrewUpdate(t *testing.T) {
	m := newMini(t)
	m.softwareUpdate("echo 'No new software available.'\n")
	if out := m.ok("pending-updates"); out != "" {
		t.Errorf("before the set-up:\n%s", out)
	}
	// A tool on the list that is not copied yet is the set-up's to bring.
	h := m.withHomebrew()
	write(t, m.path("usr/local/lab/tools/current/VERSIONS"), "cloudflared 2026.10.1\n", 0o644)
	if out := m.ok("pending-updates"); out != "" {
		t.Errorf("with one tool copied, and that one current:\n%s", out)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.asked) != 1 {
		t.Errorf("Homebrew's list was asked for %v", h.asked)
	}
}

// labd is started with the socket the scripts leave lines on, the folder
// the backup and the drill leave their marker files in, and the scripts it
// runs, which are in the Deployed build.
func TestLabdIsStartedWithWhatItWatches(t *testing.T) {
	_, arrays := plist(t, filepath.Join(repo(t), "deploy", labdLabel+".plist"))
	flags := map[string]string{}
	args := arrays["ProgramArguments"]
	for i := 1; i+1 < len(args); i++ {
		if strings.HasPrefix(args[i], "--ops-") {
			flags[args[i]] = args[i+1]
		}
	}
	onMini := func(name string) string { return strings.Replace(shell(t, name), "$LAB_HOME", "/var/lab", 1) }
	for flag, want := range map[string]string{
		"--ops-socket":  onMini("OPS_SOCKET"),
		"--ops-marks":   filepath.Dir(onMini("BACKUP_OK")),
		"--ops-scripts": "/usr/local/lab/current/scripts/mini",
	} {
		if flags[flag] != want {
			t.Errorf("%s is %q, want %q", flag, flags[flag], want)
		}
	}
	if filepath.Dir(onMini("DRILL_OK")) != flags["--ops-marks"] {
		t.Error("the Restore drill leaves its marker file somewhere else")
	}
	if flags["--ops-socket"] == "/var/lab/run/labd.sock" || !regexp.MustCompile(`^/var/lab/run/[a-z.]+$`).MatchString(flags["--ops-socket"]) {
		t.Errorf("the ops socket is %q", flags["--ops-socket"])
	}
	for _, script := range []string{"parts", "pending-updates", "notify"} {
		if st, err := os.Stat(filepath.Join(repo(t), "scripts", "mini", script)); err != nil || st.Mode().Perm()&0o111 == 0 {
			t.Errorf("scripts/mini/%s: %v", script, err)
		}
	}
}
