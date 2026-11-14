package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
	"github.com/omacom/omarchy-maclab/internal/labhttp"
)

// asLab is the saved login and an HTTP client that reaches the lab as the
// lab command does.
type asLab struct {
	c     *client.Client
	http  *http.Client
	agent string
}

func login() (*asLab, error) {
	c, err := client.Load()
	if err != nil {
		return nil, err
	}
	agent := "lab/" + strings.TrimPrefix(client.Release, "v") + " (front-door probe)"
	p := labhttp.Policy{AllowPlainHTTP: c.AllowPlainHTTP, UserAgent: agent}
	if c.CAFile != "" {
		if p.CAPEM, err = labhttp.ReadCAFile(c.CAFile); err != nil {
			return nil, err
		}
	}
	hc, err := p.Client(0)
	if err != nil {
		return nil, err
	}
	return &asLab{c, hc, agent}, nil
}

func (l *asLab) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, l.c.URL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+l.c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// plain is what a caller said, fit for a record: one line of plain
// characters, and never a page.
func plain(b []byte) string {
	head := strings.ToLower(string(b[:min(len(b), 512)]))
	if strings.Contains(head, "<!doctype") || strings.Contains(head, "<html") || strings.Contains(head, "<body") {
		return "an error page, not shown"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	var out strings.Builder
	for _, r := range line {
		if r < ' ' || r > '~' || r == '<' || r == '>' {
			r = '?'
		}
		out.WriteRune(r)
		if out.Len() >= 300 {
			break
		}
	}
	return out.String()
}

// out is where the facts go.
var out io.Writer = os.Stdout

func say(key string, value any) { fmt.Fprintf(out, "%s=%v\n", key, value) }

// edgeSaid prints what the edge said of an answer: never an address.
func edgeSaid(resp *http.Response) {
	say("cache_status", resp.Header.Get("CF-Cache-Status"))
	say("cache_control", resp.Header.Get("Cache-Control"))
	say("edge_cache_control", resp.Header.Get("Cloudflare-CDN-Cache-Control"))
	say("mitigated", resp.Header.Get("CF-Mitigated"))
	say("ray", plain([]byte(resp.Header.Get("CF-Ray"))))
}

func heldCommand(fs *flag.FlagSet, args []string) error {
	timeout := fs.Int("timeout", 120, "lab exec's --timeout, in seconds")
	cutAfter := fs.Float64("cut-after", 0, "close the connection this many seconds after the request left")
	holdFor := fs.Float64("for", 60, "how long to hold the event stream, in seconds")
	fs.Parse(args)
	pos := fs.Args()
	l, err := login()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var req *http.Request
	events := false
	switch {
	case len(pos) >= 3 && pos[0] == "exec":
		req, err = l.request(ctx, "POST", "/api/devices/"+url.PathEscape(pos[1])+"/exec", api.ExecArgs{Command: strings.Join(pos[2:], " "), TimeoutSec: *timeout})
	case len(pos) == 3 && pos[0] == "freeze":
		req, err = l.request(ctx, "POST", "/api/installed/"+url.PathEscape(pos[2])+"/entry/"+url.PathEscape(pos[1]), nil)
	case len(pos) == 1 && pos[0] == "events":
		events = true
		*cutAfter = *holdFor
		req, err = l.request(ctx, "GET", "/api/stream", nil)
	default:
		fs.Usage()
		return errors.New("held: exec MAC COMMAND..., freeze MAC RELEASE, or events")
	}
	if err != nil {
		return err
	}
	// Through the tunnel a held response is a stream whatever the caller
	// asks for: the probe asks for the silent form, as an old client would.
	if events {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	start := time.Now()
	var cut atomic.Bool
	if *cutAfter > 0 {
		time.AfterFunc(time.Duration(*cutAfter*float64(time.Second)), func() {
			cut.Store(true)
			cancel()
		})
	}
	resp, err := l.http.Do(req)
	if err != nil {
		say("status", 0)
		if cut.Load() {
			say("ended", "cut_here")
			return nil
		}
		say("ended", "unreachable")
		say("said", plain([]byte(err.Error())))
		return nil
	}
	defer resp.Body.Close()
	say("status", resp.StatusCode)
	say("content_type", plain([]byte(resp.Header.Get("Content-Type"))))
	edgeSaid(resp)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		say("ended", "refused")
		say("said", plain(b))
		return nil
	}
	want := "application/x-ndjson"
	if events {
		want = "text/event-stream"
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), want) {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		say("stream", 0)
		say("ended", "silent_form")
		say("seconds", fmt.Sprintf("%.2f", time.Since(start).Seconds()))
		say("said", plain(b))
		return nil
	}
	say("stream", 1)

	var keepalives []float64
	var last []byte
	br := bufio.NewReaderSize(resp.Body, 1<<20)
	whole := true
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			whole = len(line) == 0
			break
		}
		at := time.Since(start).Seconds()
		text := bytes.TrimSpace(line)
		switch {
		case events && bytes.HasPrefix(text, []byte(":")):
			keepalives = append(keepalives, at)
		case events:
		case len(text) == 0:
			keepalives = append(keepalives, at)
		default:
			last = text
		}
	}
	say("keepalives", len(keepalives))
	if len(keepalives) > 0 {
		say("first", fmt.Sprintf("%.2f", keepalives[0]))
		lo, hi := 0.0, 0.0
		for i := 1; i < len(keepalives); i++ {
			gap := keepalives[i] - keepalives[i-1]
			if i == 1 || gap < lo {
				lo = gap
			}
			hi = max(hi, gap)
		}
		say("gap_min", fmt.Sprintf("%.2f", lo))
		say("gap_max", fmt.Sprintf("%.2f", hi))
	}
	say("seconds", fmt.Sprintf("%.2f", time.Since(start).Seconds()))
	if events {
		if cut.Load() {
			say("ended", "held")
		} else {
			say("ended", "cut")
		}
		return nil
	}
	var hl api.HeldLine
	switch {
	case cut.Load():
		say("ended", "cut_here")
	case !whole || last == nil || json.Unmarshal(last, &hl) != nil || (hl.Error == "" && hl.Result == nil):
		say("ended", "cut")
	case hl.Error != "":
		say("ended", "error")
		say("error_status", hl.Status)
		say("said", plain([]byte(hl.Error)))
	default:
		say("ended", "result")
		var res struct {
			ExitCode *int   `json:"exit_code"`
			Stdout   string `json:"stdout"`
			SHA      string `json:"entry_sha256"`
		}
		json.Unmarshal(hl.Result, &res)
		if res.ExitCode != nil {
			say("exit_code", *res.ExitCode)
			say("stdout", plain([]byte(res.Stdout)))
		}
		if res.SHA != "" {
			say("entry_sha256", plain([]byte(res.SHA)))
		}
	}
	return nil
}

func execsCommand(fs *flag.FlagSet, args []string) error {
	fs.Parse(args)
	if fs.NArg() != 2 {
		fs.Usage()
		return errors.New("execs: MAC TEXT")
	}
	l, err := login()
	if err != nil {
		return err
	}
	req, err := l.request(context.Background(), "GET", "/api/devices/"+url.PathEscape(fs.Arg(0))+"/execs?limit=1000", nil)
	if err != nil {
		return err
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("the Exec record: HTTP %d: %s", resp.StatusCode, plain(b))
	}
	var events []api.ExecEvent
	if err := json.Unmarshal(b, &events); err != nil {
		return fmt.Errorf("the Exec record: %v", err)
	}
	count := map[string]int{}
	lines := 0
	for _, e := range events {
		if strings.Contains(e.Command, fs.Arg(1)) {
			lines++
			count[e.State]++
		}
	}
	say("lines", lines)
	for _, state := range []string{api.ExecRan, api.ExecLost, api.ExecRefused, api.ExecRunning} {
		say(state, count[state])
	}
	return nil
}

func getCommand(fs *flag.FlagSet, args []string) error {
	ipv4 := fs.Bool("ipv4", false, "reach the lab over IPv4 alone, as curl -4 does, so that the request leaves from the same address as curl's")
	fs.Parse(args)
	if fs.NArg() != 1 || !strings.HasPrefix(fs.Arg(0), "/") {
		fs.Usage()
		return errors.New("get: [--ipv4] PATH")
	}
	l, err := login()
	if err != nil {
		return err
	}
	if *ipv4 {
		if l.c.CAFile != "" {
			return errors.New("get --ipv4: not with a login that names a CA file")
		}
		var d net.Dialer
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp4", addr) }
		l.http = &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	req, err := l.request(context.Background(), "GET", fs.Arg(0), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", l.agent)
	resp, err := l.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	say("status", resp.StatusCode)
	say("bytes", n)
	say("content_type", plain([]byte(resp.Header.Get("Content-Type"))))
	edgeSaid(resp)
	return nil
}

func macCommand(fs *flag.FlagSet, args []string) error {
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("mac: MAC")
	}
	l, err := login()
	if err != nil {
		return err
	}
	d, err := l.c.Device(context.Background(), fs.Arg(0))
	if err != nil {
		say("alive", "unknown")
		say("said", plain([]byte(err.Error())))
		return nil
	}
	say("alive", d.Alive)
	say("state", plain([]byte(d.State)))
	say("active_job", plain([]byte(d.ActiveJob)))
	return nil
}

func jobCommand(fs *flag.FlagSet, args []string) error {
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("job: ID")
	}
	l, err := login()
	if err != nil {
		return err
	}
	j, err := l.c.Job(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	say("state", plain([]byte(j.State)))
	say("outcome", plain([]byte(j.Outcome)))
	return nil
}
