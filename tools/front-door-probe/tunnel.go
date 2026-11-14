package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// standIn is the edge and the tunnel in front of a local labd, for a run on
// one machine: it takes requests on a loopback port and passes them to
// labd's Unix socket as cloudflared does. Like the edge it names the Lab
// address, says which address the caller has in CF-Connecting-IP in place
// of anything the caller sent, gives each request a ray id, and cuts an
// answer that sends no byte for silence. SIGHUP restarts it as a restarted
// tunnel: every connection is dropped and it is gone for down.
//
// A fault makes it misbehave, to show that a test fails when it should:
//
//	no-keepalives        it swallows the Keepalives of a held response
//	pass-client-address  it believes the CF-Connecting-IP a caller sends
//	roaming-address      every request comes from another address
//	public-cache         it calls every answer cacheable by anyone
//	challenge            it answers the Python tool with a challenge page
//	throttle             it refuses every third download with a page
type standIn struct {
	host    string
	client  string
	silence time.Duration
	faults  map[string]bool
	up      *http.Transport
	n       atomic.Int64
	files   atomic.Int64
}

var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// fromCaller are the headers only the edge may set.
var fromCaller = []string{"CF-Connecting-IP", "CF-Ray", "CF-Visitor", "True-Client-IP", "X-Forwarded-For", "X-Forwarded-Proto", "Early-Data"}

func newRay() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b) + "-BNE"
}

func page(w http.ResponseWriter, status int, ray, what string) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.Header().Set("CF-Ray", ray)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!DOCTYPE html>\n<html><head><title>%s</title></head><body><h1>%s</h1><p>error code: %d</p><p>Ray ID: <code>%s</code></p></body></html>\n", what, what, status, strings.TrimSuffix(ray, "-BNE"))
}

func (t *standIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := t.n.Add(1)
	ray := newRay()
	if t.faults["challenge"] && strings.Contains(r.UserAgent(), "omarchy-m-test") {
		w.Header().Set("CF-Mitigated", "challenge")
		page(w, http.StatusForbidden, ray, "Just a moment...")
		return
	}
	if t.faults["throttle"] && strings.Contains(r.URL.Path, "/files/") && t.files.Add(1)%3 == 0 {
		page(w, http.StatusTooManyRequests, ray, "You are being rate limited")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := r.Clone(ctx)
	out.RequestURI = ""
	out.URL.Scheme, out.URL.Host, out.Host = "http", t.host, t.host
	sent := r.Header.Get("CF-Connecting-IP")
	for _, h := range append(hopHeaders, fromCaller...) {
		out.Header.Del(h)
	}
	addr := t.client
	if t.faults["roaming-address"] {
		addr = fmt.Sprintf("198.51.100.%d", n%250+1)
	}
	if t.faults["pass-client-address"] && sent != "" {
		addr = sent
	}
	out.Header.Set("CF-Connecting-IP", addr)
	out.Header.Set("CF-Ray", ray)
	out.Header.Set("X-Forwarded-Proto", "https")

	// Silent for too long, the edge gives up: before the answer began with
	// a page of its own, afterwards by cutting it.
	quiet := time.AfterFunc(t.silence, cancel)
	defer quiet.Stop()
	resp, err := t.up.RoundTrip(out)
	if err != nil {
		status := http.StatusBadGateway
		if ctx.Err() != nil && r.Context().Err() == nil {
			status = 524
		}
		page(w, status, ray, "The origin did not answer")
		return
	}
	defer resp.Body.Close()
	hdr := w.Header()
	for k, vs := range resp.Header {
		hdr[k] = vs
	}
	for _, h := range hopHeaders {
		hdr.Del(h)
	}
	hdr.Set("CF-Ray", ray)
	if t.faults["public-cache"] {
		hdr.Set("Cache-Control", "public, max-age=60")
	}
	w.WriteHeader(resp.StatusCode)
	fl := http.NewResponseController(w)
	fl.Flush()

	write := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		fl.Flush()
		quiet.Reset(t.silence)
		return true
	}
	var rerr error
	if t.faults["no-keepalives"] && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-ndjson") {
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadBytes('\n')
			if len(strings.TrimSpace(string(line))) > 0 && !write(line) {
				return
			}
			if err != nil {
				rerr = err
				break
			}
		}
	} else {
		buf := make([]byte, 64<<10)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 && !write(buf[:n]) {
				return
			}
			if err != nil {
				rerr = err
				break
			}
		}
	}
	if !errors.Is(rerr, io.EOF) {
		// Cut, by the silence or by labd going away: the caller sees a
		// connection that ended, not an answer that did.
		panic(http.ErrAbortHandler)
	}
}

func tunnelCommand(fs *flag.FlagSet, args []string) error {
	listen := fs.String("listen", "127.0.0.1:0", "the loopback port callers reach it at")
	socket := fs.String("socket", "", "labd's tunnel socket")
	host := fs.String("host", "", "the Lab address's host name, which labd answers to on its socket")
	client := fs.String("client", "198.51.100.7", "the address it says every caller has")
	silence := fs.Duration("silence", 125*time.Second, "how long an answer may send nothing before it is cut")
	down := fs.Duration("down", 2*time.Second, "how long a restart (SIGHUP) leaves it gone")
	faults := fs.String("fault", "", "ways to misbehave, comma-separated")
	fs.Parse(args)
	if *socket == "" || *host == "" || fs.NArg() != 0 {
		fs.Usage()
		return errors.New("tunnel: --socket and --host")
	}
	h, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen %s: the stand-in listens on loopback only", *listen)
	}
	t := &standIn{host: *host, client: *client, silence: *silence, faults: map[string]bool{}}
	known := map[string]bool{"no-keepalives": true, "pass-client-address": true, "roaming-address": true, "public-cache": true, "challenge": true, "throttle": true}
	for _, f := range strings.Split(*faults, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		if !known[f] {
			return fmt.Errorf("--fault %s: no such fault", f)
		}
		t.faults[f] = true
	}
	var d net.Dialer
	t.up = &http.Transport{
		DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "unix", *socket) },
		DisableCompression:  true,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	fmt.Printf("listening=%s\n", addr)

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	for {
		srv := &http.Server{Handler: t, ReadHeaderTimeout: 10 * time.Second}
		go srv.Serve(ln)
		select {
		case <-stop:
			srv.Close()
			return nil
		case <-hup:
		}
		srv.Close()
		t.up.CloseIdleConnections()
		time.Sleep(*down)
		for tries := 0; ; tries++ {
			if ln, err = net.Listen("tcp", addr); err == nil {
				break
			}
			if tries > 50 {
				return err
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Println("restarted=1")
	}
}
