package minitest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// edge-facts is what the Front door's tests ask the mini: where the tunnel
// leads, who listens, and what the caller log holds for a path. It prints
// counts and words, never an address.

func sha(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// edgeMini is a mini with a Deployed route file and labd's socket.
func edgeMini(t *testing.T) *mini {
	t.Helper()
	m := newMini(t)
	write(t, m.path("usr/local/lab/current/deploy/cloudflared.yml"), read(t, filepath.Join(repo(t), "deploy", "cloudflared.yml")), 0o644)
	if err := os.MkdirAll(m.path("var/lab/run"), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", m.path("var/lab/run/labd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	if err := os.Chmod(m.path("var/lab/run/labd.sock"), 0o600); err != nil {
		t.Fatal(err)
	}
	return m
}

const listening = "labd 10 lab 7u IPv4 0x1 0t0 TCP 127.0.0.1:7770 (LISTEN)\n" +
	"cloudflar 11 lab 9u IPv4 0x2 0t0 TCP 127.0.0.1:20241 (LISTEN)\n" +
	"oobd 12 root 5u IPv4 0x3 0t0 TCP 127.0.0.1:7780 (LISTEN)\n" +
	"sshd 13 root 5u IPv4 0x4 0t0 TCP *:22 (LISTEN)\n" +
	"tailscale 14 root 5u IPv4 0x5 0t0 TCP 100.64.0.7:40000 (LISTEN)\n"

func TestEdgeFactsSaysWhereTheTunnelLeadsAndWhoListens(t *testing.T) {
	m := edgeMini(t)
	m.set("lsof.-sTCP:LISTEN", listening)
	want := "tunnel_origin=socket\nsocket=ok\nlab_listeners=3\nbeyond_loopback=0\nbeyond_loopback_ports=\n"
	if out := m.ok("edge-facts", "origin"); out != want {
		t.Errorf("origin said:\n%swant:\n%s", out, want)
	}

	// A daemon of the lab's that listens beyond this machine is named by
	// its port, whatever account it runs as.
	m.set("lsof.-sTCP:LISTEN", listening+"labd 10 lab 8u IPv6 0x6 0t0 TCP *:7770 (LISTEN)\noobd 12 root 6u IPv4 0x7 0t0 TCP 192.168.1.20:7780 (LISTEN)\n")
	out := m.ok("edge-facts", "origin")
	if !strings.Contains(out, "lab_listeners=5\nbeyond_loopback=2\nbeyond_loopback_ports=labd:7770,oobd:7780\n") || strings.Contains(out, "192.168") {
		t.Errorf("with two ports open beyond loopback it said:\n%s", out)
	}

	// A route that leads anywhere but the socket, and a socket others open.
	routes := m.path("usr/local/lab/current/deploy/cloudflared.yml")
	write(t, routes, strings.Replace(read(t, routes), "unix:/var/lab/run/labd.sock", "http://127.0.0.1:7770", 1), 0o644)
	if err := os.Chmod(m.path("var/lab/run/labd.sock"), 0o666); err != nil {
		t.Fatal(err)
	}
	if out := m.ok("edge-facts", "origin"); !strings.Contains(out, "tunnel_origin=other\nsocket=open\n") {
		t.Errorf("with a route to a port and an open socket it said:\n%s", out)
	}
	os.Remove(m.path("var/lab/run/labd.sock"))
	if out := m.ok("edge-facts", "origin"); !strings.Contains(out, "socket=missing\n") {
		t.Errorf("with no socket it said:\n%s", out)
	}
}

func TestEdgeFactsCountsTheCallerLogAndNamesNoAddress(t *testing.T) {
	m := edgeMini(t)
	line := func(addr, ray, path string) string {
		if ray != "" {
			ray = `"ray":"` + ray + `",`
		}
		return fmt.Sprintf(`{"time":"2026-11-15T00:00:00Z","addr":"%s",%s"method":"GET","path":"%s","status":404,"ms":1,"agent":"curl/8"}`+"\n", addr, ray, path)
	}
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	write(t, m.path("var/lab/data/callers/"+yesterday+".log"), line("192.0.2.10", "aaaa-BNE", "/fd08-x-plain"), 0o600)
	write(t, m.path("var/lab/data/callers/"+today+".log"),
		line("192.0.2.10", "bbbb-BNE", "/fd08-x-plain")+
			line("192.0.2.10", "cccc-BNE", "/fd08-x-forged")+
			line("203.0.113.77", "dddd-BNE", "/fd08-x-believed")+
			line("127.0.0.1", "", "/fd08-x-loopback")+
			line("192.0.2.99", "eeee-BNE", "/fd08-x-plain-not-this"), 0o600)

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"/fd08-x-plain", "203.0.113.77", sha("192.0.2.10")}, "lines=2\nwith_ray=2\nforged=0\naddresses=1\nas_expected=2\n"},
		{[]string{"/fd08-x-forged", "203.0.113.77", sha("192.0.2.10")}, "lines=1\nwith_ray=1\nforged=0\naddresses=1\nas_expected=1\n"},
		{[]string{"/fd08-x-believed", "203.0.113.77", sha("192.0.2.10")}, "lines=1\nwith_ray=1\nforged=1\naddresses=1\nas_expected=0\n"},
		{[]string{"/fd08-x-loopback", "203.0.113.77", sha("127.0.0.1")}, "lines=1\nwith_ray=0\nforged=0\naddresses=1\nas_expected=1\n"},
		{[]string{"/fd08-x-loopback"}, "lines=1\nwith_ray=0\nforged=0\naddresses=1\nas_expected=0\n"},
		{[]string{"/fd08-nobody-asked"}, "lines=0\nwith_ray=0\nforged=0\naddresses=0\nas_expected=0\n"},
	} {
		out := m.ok("edge-facts", append([]string{"callers"}, c.args...)...)
		if out != c.want {
			t.Errorf("callers %v said:\n%swant:\n%s", c.args[:1], out, c.want)
		}
		if strings.Contains(out, "192.0.2") || strings.Contains(out, "BNE") {
			t.Errorf("callers named an address or a ray id:\n%s", out)
		}
	}
	// The log is read as the lab account.
	if calls := m.calls(); !strings.Contains(calls, "as-lab cat ") {
		t.Errorf("the caller log was not read as the lab account:\n%s", calls)
	}

	for _, bad := range [][]string{{}, {"callers"}, {"callers", "no-slash"}, {"callers", "/a b"}, {"callers", "/x", "not an address"}, {"callers", "/x", "203.0.113.77", "short"},
		{"callers", "/x", "203.0.113.77", sha("x"), "more"}, {"origin", "more"}, {"everything"}} {
		if out, code := m.run("edge-facts", bad...); code != 2 || !strings.Contains(out, "edge-facts origin") {
			t.Errorf("edge-facts %v: exit %d\n%s", bad, code, out)
		}
	}
}

// A link planted where the caller log is kept is refused, and nothing is
// read through it.
func TestEdgeFactsRefusesAPlantedCallerLog(t *testing.T) {
	m := edgeMini(t)
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, time.Now().UTC().Format("2006-01-02")+".log"), `{"addr":"192.0.2.10","path":"/x","status":200}`+"\n", 0o600)
	if err := os.MkdirAll(m.path("var/lab/data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, m.path("var/lab/data/callers")); err != nil {
		t.Fatal(err)
	}
	if out := m.refused("edge-facts", "callers", "/x"); !strings.Contains(out, "is a symbolic link") || strings.Contains(out, "lines=") {
		t.Errorf("with a link for the caller log it said:\n%s", out)
	}
}
