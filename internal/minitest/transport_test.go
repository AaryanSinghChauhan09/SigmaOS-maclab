package minitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// labd is told how many parts of an upload go at once in its LaunchDaemon
// file: 1 until the Front door's test 5 measures 4 at least 20% faster.
func TestLabdsLaunchDaemonSaysHowManyPartsGoAtOnce(t *testing.T) {
	_, arrays := plist(t, filepath.Join(repo(t), "deploy", labdLabel+".plist"))
	args := arrays["ProgramArguments"]
	said := ""
	for i, a := range args {
		if a == "--parts-at-once" && i+1 < len(args) {
			if said != "" {
				t.Fatal("--parts-at-once is given twice")
			}
			said = args[i+1]
		}
	}
	if said != "1" && said != "4" {
		t.Errorf("--parts-at-once is %q, want 1 or 4", said)
	}
}

// sysctl keeps the one limit the set-up looks at. A made-up mini starts
// with it high enough; a test writes down macOS's own 8 MiB.
const sysctlStub = `#!/bin/bash
echo "sysctl $*" >> "$STUB_LOG"
s="$STUB_STATE/machine"; mkdir -p "$s"
case "$1" in
  -n) [[ $2 == kern.ipc.maxsockbuf ]] || exit 64; cat "$s/maxsockbuf" 2>/dev/null || echo 16777216 ;;
  -w) [[ $2 == kern.ipc.maxsockbuf=* ]] || exit 64; echo "${2#*=}" > "$s/maxsockbuf" ;;
  *) exit 64 ;;
esac
`

// tunnelUp is a mini whose set-up has installed the credential and started
// the tunnel.
func tunnelUp(t *testing.T) *mini {
	t.Helper()
	m, k := withTunnel(t)
	m.ok("setup", "--release-key", k.signers, "--tunnel-credential", credentialFile(t, madeUpCredential+"\n", 0o600))
	os.Remove(m.log)
	return m
}

// transport writes the Deployed build's route file with a transport in it.
func (m *mini) transport(protocol string) {
	m.t.Helper()
	write(m.t, m.path("usr/local/lab/current/deploy/cloudflared.yml"), "protocol: "+protocol+"\ningress:\n  - service: http_status:404\n", 0o644)
}

// Under QUIC cloudflared wants a UDP buffer macOS's own limit does not
// allow: the set-up raises the limit, now and for the next start, and
// restarts the tunnel so that it gets the buffer. Under HTTP/2, and on a
// machine whose limit is already high enough, it sets nothing.
func TestSetupRaisesTheUDPBufferLimitOnlyUnderQUIC(t *testing.T) {
	const line = "kern.ipc.maxsockbuf=8441037"
	m := tunnelUp(t)
	conf := m.path("etc/sysctl.conf")

	m.transport("http2")
	m.set("maxsockbuf", "8388608")
	m.ok("setup")
	if strings.Contains(m.calls(), "sysctl") || exists(conf) {
		t.Fatalf("under HTTP/2 the set-up looked at or set the limit:\n%s", m.calls())
	}

	m.transport("quic")
	m.unset("maxsockbuf")
	out := m.ok("setup")
	if strings.Contains(m.calls(), "sysctl -w") || exists(conf) || strings.Contains(out, "UDP") {
		t.Fatalf("a limit already high enough was set:\n%s\n%s", out, m.calls())
	}

	m.set("maxsockbuf", "8388608")
	write(t, conf, "# the owner's own\nkern.maxfiles=65536\nkern.ipc.maxsockbuf=1000\n", 0o644)
	os.Remove(m.log)
	out = m.ok("setup", "--dry-run")
	if !strings.Contains(out, "would raise the UDP buffer limit") || !strings.Contains(out, "from 8388608 to 8441037") || strings.Contains(m.calls(), "sysctl -w") || strings.Contains(read(t, conf), line) {
		t.Fatalf("the dry run:\n%s\n%s", out, m.calls())
	}

	os.Remove(m.log)
	out = m.ok("setup")
	if m.is("maxsockbuf") != "8441037" || !strings.Contains(m.calls(), "sysctl -w "+line) {
		t.Fatalf("the limit was not raised:\n%s\n%s", out, m.calls())
	}
	if got := read(t, conf); got != "# the owner's own\nkern.maxfiles=65536\n"+line+"\n" {
		t.Errorf("sysctl.conf is %q", got)
	}
	if mode(t, conf) != 0o644 {
		t.Errorf("sysctl.conf is %v", mode(t, conf))
	}
	daemon := m.path("Library/LaunchDaemons/" + cloudflaredLabel + ".plist")
	if !strings.Contains(m.calls(), "launchctl bootout system/"+cloudflaredLabel) || !strings.Contains(m.calls(), "launchctl bootstrap system "+daemon) {
		t.Errorf("the tunnel was not restarted to take the larger buffer:\n%s", m.calls())
	}

	os.Remove(m.log)
	out = m.ok("setup")
	if !strings.Contains(out, "nothing to change") || strings.Contains(m.calls(), "sysctl -w") || strings.Contains(m.calls(), "bootstrap") {
		t.Errorf("a second run changed something:\n%s\n%s", out, m.calls())
	}

	// A restart put macOS's own limit back and the file was not read: the
	// set-up raises it again and leaves the file as it is.
	m.set("maxsockbuf", "8388608")
	out = m.ok("setup")
	if m.is("maxsockbuf") != "8441037" || strings.Count(read(t, conf), "maxsockbuf") != 1 {
		t.Errorf("after a restart:\n%s", out)
	}
}

// ready plays cloudflared's own answer to whether it holds a connection.
func (m *mini) ready(up bool) {
	m.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up || !m.tunnelLoaded() {
			http.Error(w, "no", http.StatusServiceUnavailable)
		}
	}))
	m.t.Cleanup(srv.Close)
	m.env = append(m.env, "LAB_TUNNEL_READY="+srv.URL+"/ready", "LAB_READY_TRIES=2")
}

// For the Front door's test 15 the running tunnel is put under one
// transport and then the other without a Deploy: transport writes the
// installed LaunchDaemon file with --protocol in it and restarts the
// tunnel; "deployed" and the next set-up put the Deployed file back.
func TestTransportPutsTheRunningTunnelUnderOneTransportAndBack(t *testing.T) {
	m, k := withTunnel(t)
	m.transport("quic")
	daemon := m.path("Library/LaunchDaemons/" + cloudflaredLabel + ".plist")
	deployed := read(t, filepath.Join(repo(t), "deploy", cloudflaredLabel+".plist"))

	// No tunnel set up yet: nothing to switch.
	out := m.refused("transport", "http2")
	if !strings.Contains(out, "not set up") || exists(daemon) || strings.Contains(m.calls(), "bootstrap") {
		t.Fatalf("with no tunnel:\n%s", out)
	}

	m.ok("setup", "--release-key", k.signers, "--tunnel-credential", credentialFile(t, madeUpCredential+"\n", 0o600))
	m.transport("quic")
	m.ready(true)
	if out := m.ok("transport"); !strings.Contains(out, "quic") || !strings.Contains(out, "Deployed") {
		t.Errorf("asked which transport, it said:\n%s", out)
	}
	for _, bad := range [][]string{{"http3"}, {"auto"}, {"http2", "quic"}, {"--protocol"}} {
		os.Remove(m.log)
		m.refused("transport", bad...)
		if read(t, daemon) != deployed || strings.Contains(m.calls(), "launchctl boot") {
			t.Fatalf("transport %v changed something", bad)
		}
	}

	os.Remove(m.log)
	out = m.ok("transport", "http2")
	_, arrays := plist(t, daemon)
	args := strings.Join(arrays["ProgramArguments"], " ")
	_, want := plist(t, filepath.Join(repo(t), "deploy", cloudflaredLabel+".plist"))
	if args != strings.Replace(strings.Join(want["ProgramArguments"], " "), " run ", " --protocol http2 run ", 1) {
		t.Fatalf("the tunnel's arguments are %q", args)
	}
	if mode(t, daemon) != 0o644 || !m.tunnelLoaded() || !strings.Contains(m.calls(), "launchctl bootout system/"+cloudflaredLabel) || !strings.Contains(m.calls(), "launchctl bootstrap system "+daemon) {
		t.Errorf("the tunnel was not restarted under the file:\n%s", m.calls())
	}
	if !strings.Contains(out, "http2") || !strings.Contains(out, "transport deployed") {
		t.Errorf("it does not say how to go back:\n%s", out)
	}
	if out := m.ok("transport"); !strings.Contains(out, "http2") || !strings.Contains(out, "for a measurement") {
		t.Errorf("asked which transport, it said:\n%s", out)
	}

	m.ok("transport", "quic")
	_, arrays = plist(t, daemon)
	if got := strings.Join(arrays["ProgramArguments"], " "); !strings.Contains(got, " --protocol quic run ") || strings.Contains(got, "http2") {
		t.Fatalf("the tunnel's arguments are %q", got)
	}

	os.Remove(m.log)
	m.ok("transport", "deployed")
	if read(t, daemon) != deployed || !m.tunnelLoaded() || !strings.Contains(m.calls(), "launchctl bootstrap system "+daemon) {
		t.Errorf("the Deployed file was not put back:\n%s", m.calls())
	}

	// The next set-up puts the Deployed file back by itself.
	m.ok("transport", "http2")
	m.ok("setup")
	if read(t, daemon) != deployed || !m.tunnelLoaded() {
		t.Error("the set-up left the measuring transport in place")
	}
}

// A tunnel that does not connect under the transport asked for is put back
// under the Deployed file, and the run fails.
func TestTransportGoesBackWhenTheTunnelDoesNotConnect(t *testing.T) {
	m := tunnelUp(t)
	m.transport("quic")
	m.ready(false)
	daemon := m.path("Library/LaunchDaemons/" + cloudflaredLabel + ".plist")
	out := m.refused("transport", "http2")
	if !strings.Contains(out, "did not connect") || read(t, daemon) != read(t, filepath.Join(repo(t), "deploy", cloudflaredLabel+".plist")) || !m.tunnelLoaded() {
		t.Errorf("want the Deployed file back and the tunnel loaded:\n%s", out)
	}
}

// page-times reads labd's own times for the pages it asked for, and for the
// day's pages, and prints numbers only: no path, no address, no line of
// the caller log.
func TestPageTimesReadsLabdsOwnTimesAndPrintsNumbersOnly(t *testing.T) {
	m := newMini(t)
	log := m.path("var/lab/data/callers/" + time.Now().UTC().Format("2006-01-02") + ".log")
	// The day so far: a slow page someone signed in loaded, and what is no page.
	write(t, log, `{"time":"t","addr":"203.0.113.9","who":"person:grace","method":"GET","path":"/people","status":200,"ms":900,"agent":"Mozilla/5.0"}
{"time":"t","addr":"203.0.113.9","method":"GET","path":"/api/devices","status":200,"ms":5000,"agent":"lab/0.2.0"}
{"time":"t","addr":"203.0.113.9","method":"GET","path":"/assets/app-0123456789abcdef.css","status":200,"ms":7000,"agent":"Mozilla/5.0"}
{"time":"t","addr":"203.0.113.9","method":"POST","path":"/sign-out","status":200,"ms":8000,"agent":"Mozilla/5.0"}
{"time":"t","addr":"203.0.113.9","method":"GET","path":"/nowhere","status":404,"ms":9000,"agent":"Mozilla/5.0"}
`, 0o600)
	var mu sync.Mutex
	asked := 0
	labd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		status, ms := 200, 7
		switch r.URL.Path {
		case "/status":
			status = 404
		case "/matrix":
			ms = 40
		}
		asked++
		if asked == 3 {
			ms = 333
		}
		// The fields in the order labd's caller log writes them.
		agent, _ := json.Marshal(r.UserAgent())
		line := fmt.Sprintf(`{"time":"t","addr":"127.0.0.1","method":%q,"path":%q,"status":%d,"ms":%d,"agent":%s}`, r.Method, r.URL.Path, status, ms, agent)
		f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Fprintf(f, "%s\n", line)
		f.Close()
		w.WriteHeader(status)
	}))
	t.Cleanup(labd.Close)
	m.env = append(m.env, "LAB_LOOPBACK="+strings.TrimPrefix(labd.URL, "http://"))

	out := m.ok("page-times")
	// Twelve pages answer, twenty requests each: 240, of which twenty take
	// 40 ms and one 333, so the 95th percentile is the 228th: 40.
	want := "probe_pages=12\nprobe_requests=240\nprobe_p95_ms=40\nprobe_max_ms=333\nday_requests=241\nday_p95_ms=40\n"
	if out != want {
		t.Fatalf("page-times printed:\n%s\nwant:\n%s", out, want)
	}
	if strings.Contains(m.calls(), "launchctl") {
		t.Errorf("page-times changed something:\n%s", m.calls())
	}

	if out := m.refused("page-times", "--all"); !strings.Contains(out, "page-times") {
		t.Errorf("an argument was taken:\n%s", out)
	}
	os.RemoveAll(m.path("var/lab/data/callers"))
	if out := m.refused("page-times"); !strings.Contains(out, "run it as the lab account") {
		t.Errorf("with no caller log to read:\n%s", out)
	}
}
