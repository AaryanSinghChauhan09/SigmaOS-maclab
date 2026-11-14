package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/labhttp"
	"github.com/omacom/omarchy-maclab/internal/testpki"
)

// The whole path over https under a prefix: the simulated Mac reads the
// terms and enrolls with its activation key, checks in, downloads its kernel
// through the grant check, runs the job and uploads its files, all at
// https://127.0.0.1:<port>/maclab, trusting only the test CA.
func TestAMacOverHTTPSUnderAPrefixRunsAJob(t *testing.T) {
	l := newLabWith(t, labConfig{tls: testpki.NewCA(t), prefix: "/maclab"})
	if !strings.HasPrefix(l.url, "https://127.0.0.1:") || !strings.HasSuffix(l.url, "/maclab") {
		t.Fatalf("the lab's URL: %s", l.url)
	}
	l.baseline()
	kernel := l.artifact("krel=7.2-https behavior=ok")
	j := l.run(api.JobSpec{Kernel: kernel})
	l.expect(j, api.OutcomePass)
	got := l.waitJob(j.ID, func(j *api.Job) bool { return j.State == api.JobDone })
	if len(got.Result.Logs) == 0 {
		t.Fatal("no job files arrived over https")
	}
	if _, err := os.Stat(l.s.jobDir(j.ID) + "/" + got.Result.Logs[0]); err != nil {
		t.Errorf("job file %s: %v", got.Result.Logs[0], err)
	}
	if d := l.waitDevice(func(d api.Device) bool { return d.TermsSHA256 != "" }); d.TermsSHA256 != termsSum() {
		t.Errorf("the Mac's accepted terms: %q", d.TermsSHA256)
	}
	root := strings.TrimSuffix(l.url, "/maclab")
	req, _ := http.NewRequest("GET", root+"/api/devices", nil)
	req.Header.Set("Authorization", "Bearer "+harnessToken)
	if resp, err := l.client().Do(req); err != nil || resp.StatusCode != 404 {
		t.Errorf("the API outside the prefix: %v %v", resp, err)
	}
	// A client that doesn't trust the lab's CA gets nowhere, and sends nothing.
	plain, _ := labhttp.Policy{}.Client(5 * time.Second)
	if _, err := plain.Get(l.url + "/api/auth"); err == nil {
		t.Error("an untrusted certificate was accepted")
	}
}

// labd listens on this machine only: a loopback port, and a Unix socket.
func TestListenConfigTakesLoopbackOnly(t *testing.T) {
	for _, c := range []struct {
		lc ListenConfig
		ok bool
	}{
		{ListenConfig{Addr: "127.0.0.1:7770"}, true},
		{ListenConfig{Addr: "[::1]:7770"}, true},
		{ListenConfig{Addr: "localhost:7770"}, true},
		{ListenConfig{Addr: "127.0.0.1:7770", Socket: "/var/run/labd/labd.sock"}, true},
		{ListenConfig{Addr: "0.0.0.0:7770"}, false},
		{ListenConfig{Addr: ":7770"}, false},
		{ListenConfig{Addr: "10.1.1.55:7770"}, false},
		{ListenConfig{Addr: "192.168.1.20:7770"}, false},
		{ListenConfig{Addr: "100.64.0.1:7770"}, false},
		{ListenConfig{Addr: "labd.example.com:7770"}, false},
		{ListenConfig{Addr: "7770"}, false},
		{ListenConfig{}, false},
		{ListenConfig{Addr: "127.0.0.1:7770", Socket: "labd.sock"}, false},
	} {
		if err := c.lc.Check(); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.lc, err)
		}
	}
	hs, err := ListenConfig{Addr: "127.0.0.1:0"}.Server(http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	// Headers have a deadline; nothing else does, so long polls, streams,
	// exec and downloads run as long as they need. An idle connection is
	// kept 2 minutes, longer than cloudflared keeps one in its pool (90 s).
	if hs.ReadHeaderTimeout != 10*time.Second || hs.IdleTimeout != 2*time.Minute || hs.MaxHeaderBytes != 64<<10 || hs.ReadTimeout != 0 || hs.WriteTimeout != 0 || hs.TLSConfig != nil {
		t.Errorf("server limits: %+v", hs)
	}
	if _, err := (ListenConfig{Addr: "0.0.0.0:0"}).Listen(); err == nil {
		t.Error("labd listened beyond this machine")
	}
}

// shortDir is a temporary directory with a short path: a Unix socket's path
// holds about a hundred bytes, and TMPDIR can be longer than that.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "labd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// The socket tests pass whatever TMPDIR is: a test runner's own temporary
// folder can be too long for a socket's path.
func TestTheSocketTestsTakeALongTMPDIR(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("t", 100))
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	l := listenLab(t)
	if code := l.get(t, l.tunnel, "http://"+labHost+"/healthz"); code != 200 {
		t.Fatalf("GET /healthz through the socket: %d", code)
	}
}

// listening is a labd at its Lab address, serving on both listeners.
type listening struct {
	s        *Server
	loopback string       // http://127.0.0.1:<port>
	socket   string       // the socket's path
	tunnel   *http.Client // dials the socket, as cloudflared does
}

func listenLab(t *testing.T) *listening {
	t.Helper()
	s := aliasServer(t, nil)
	lc := ListenConfig{Addr: "127.0.0.1:0", Socket: filepath.Join(shortDir(t), "labd.sock")}
	hs, err := lc.Server(s.Handler())
	if err != nil {
		t.Fatal(err)
	}
	lns, err := lc.Listen()
	if err != nil {
		t.Fatal(err)
	}
	if len(lns) != 2 {
		t.Fatalf("%d listeners, want the loopback port and the socket", len(lns))
	}
	done := make(chan error, 1)
	go func() { done <- Serve(hs, lns...) }()
	t.Cleanup(func() {
		hs.Close()
		if err := <-done; err != http.ErrServerClosed {
			t.Errorf("Serve: %v", err)
		}
	})
	return &listening{s: s, loopback: "http://" + lns[0].Addr().String(), socket: lc.Socket,
		tunnel: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", lc.Socket)
		}}}}
}

// get asks for target with hdr (key, value, ...) and returns the status.
func (l *listening) get(t *testing.T, c *http.Client, target string, hdr ...string) int {
	t.Helper()
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// The socket is for the account labd runs as, and nobody else; the loopback
// port is on loopback. A socket left by a labd that died is replaced, and
// nothing that isn't a socket is.
func TestTheSocketIsItsOwnersAndThePortIsLoopback(t *testing.T) {
	l := listenLab(t)
	st, err := os.Stat(l.socket)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Errorf("the socket's mode: %v, want a socket at 0600", st.Mode())
	}
	host, _, _ := net.SplitHostPort(strings.TrimPrefix(l.loopback, "http://"))
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Errorf("the port is at %s, not loopback", host)
	}
	if code := l.get(t, l.tunnel, "http://"+labHost+"/healthz"); code != 200 {
		t.Errorf("through the socket: %d", code)
	}
	if code := l.get(t, http.DefaultClient, l.loopback+"/healthz"); code != 200 {
		t.Errorf("at the loopback port: %d", code)
	}

	dir := shortDir(t)
	stale := filepath.Join(dir, "labd.sock")
	old, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	old.Close()
	lns, err := ListenConfig{Addr: "127.0.0.1:0", Socket: stale}.Listen()
	if err != nil {
		t.Fatalf("a socket left behind stopped labd: %v", err)
	}
	for _, ln := range lns {
		ln.Close()
	}
	file := filepath.Join(dir, "notes")
	os.WriteFile(file, []byte("keep"), 0o600)
	if _, err := (ListenConfig{Addr: "127.0.0.1:0", Socket: file}).Listen(); err == nil {
		t.Error("labd listened where a file was")
	}
	if b, _ := os.ReadFile(file); string(b) != "keep" {
		t.Error("labd replaced a file that was no socket")
	}
}

// Opening a socket leaves the process's umask alone: it is the whole
// process's, and a file made by anything else at that moment would be closed
// to its own readers too. Two sockets opened at once used to leave the
// process on the socket's umask for good.
func TestOpeningASocketLeavesTheProcessUmaskAlone(t *testing.T) {
	umask := syscall.Umask(0)
	syscall.Umask(umask)
	want := os.FileMode(0o666) &^ os.FileMode(umask)
	if want&0o066 == 0 {
		t.Skipf("under umask %04o a file is its owner's alone already: nothing to tell apart", umask)
	}
	dir := shortDir(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := filepath.Join(dir, "s"+string(rune('a'+i)))
			for {
				select {
				case <-stop:
					return
				default:
				}
				ln, err := listenSocket("--socket", path)
				if err != nil {
					t.Errorf("opening %s: %v", path, err)
					return
				}
				if st, err := os.Lstat(path); err != nil || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
					t.Errorf("the socket at %s: %v, %v, want a socket at 0600", path, st, err)
				}
				ln.Close()
			}
		}()
	}
	file := filepath.Join(dir, "a-file")
	for n := 0; n < 3000 && !t.Failed(); n++ {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
		if err != nil {
			t.Fatal(err)
		}
		st, err := f.Stat()
		f.Close()
		os.Remove(file)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != want {
			t.Errorf("a file made while a socket was opened got mode %04o, want %04o", st.Mode().Perm(), want)
		}
	}
	close(stop)
	wg.Wait()
	after := syscall.Umask(umask)
	if after != umask {
		t.Errorf("the process's umask is %04o after the sockets were opened, was %04o", after, umask)
	}
}

// The caller's address is CF-Connecting-IP on the tunnel's socket, and the
// ray id is the edge's. A caller on the mini can forge both headers at the
// loopback port: there the logged address is the connection's own and no ray
// id is taken.
func TestAForgedCallerAddressIsBelievedOnlyOnTheSocket(t *testing.T) {
	l := listenLab(t)
	forged := []string{"CF-Connecting-IP", "203.0.113.7", "CF-Ray", "8f1e2d3c4b5a6978-BNE", "X-Forwarded-For", "203.0.113.7", "X-Real-IP", "203.0.113.7"}
	if code := l.get(t, http.DefaultClient, l.loopback+"/healthz", forged...); code != 200 {
		t.Fatalf("at the loopback port: %d", code)
	}
	if code := l.get(t, l.tunnel, "http://"+labHost+"/healthz", forged...); code != 200 {
		t.Fatalf("through the socket: %d", code)
	}
	if code := l.get(t, l.tunnel, "http://"+labHost+"/healthz", "CF-Connecting-IP", "2001:db8::7"); code != 200 {
		t.Fatalf("through the socket over IPv6: %d", code)
	}
	if code := l.get(t, l.tunnel, "http://"+labHost+"/healthz", "CF-Connecting-IP", "not an address\"}", "CF-Ray", "x y"); code != 200 {
		t.Fatalf("through the socket with a broken header: %d", code)
	}
	lines := l.s.callers.linesForTest(t)
	if len(lines) != 4 {
		t.Fatalf("%d lines in the caller log, want 4: %+v", len(lines), lines)
	}
	if lines[0].Addr != "127.0.0.1" || lines[0].Ray != "" {
		t.Errorf("at the loopback port the forged headers were believed: %+v", lines[0])
	}
	if lines[1].Addr != "203.0.113.7" || lines[1].Ray != "8f1e2d3c4b5a6978-BNE" {
		t.Errorf("through the socket the edge's headers were not believed: %+v", lines[1])
	}
	if lines[2].Addr != "2001:db8::7" {
		t.Errorf("through the socket over IPv6: %+v", lines[2])
	}
	if lines[3].Addr != "" || lines[3].Ray != "" {
		t.Errorf("a broken header was logged as an address or ray id: %+v", lines[3])
	}
	// What a handler asks is the same address.
	for _, c := range []struct {
		tunnel bool
		want   string
	}{{true, "203.0.113.7"}, {false, "192.0.2.1"}} {
		r := httptest.NewRequest("GET", "http://"+labHost+"/", nil)
		r.Header.Set("CF-Connecting-IP", "203.0.113.7")
		if c.tunnel {
			r = r.WithContext(onTunnel(r.Context()))
		}
		if got := callerAddr(r); got != c.want {
			t.Errorf("callerAddr (tunnel %v): %q, want %q", c.tunnel, got, c.want)
		}
	}
}
