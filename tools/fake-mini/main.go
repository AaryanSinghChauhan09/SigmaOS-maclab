// Command fake-mini is a made-up mini for a labd under test, so that the
// Out-of-band wiring's tests (scripts/cutover/out-of-band/) can be tried with
// no hardware:
//
//	fake-mini --files DIR
//
// It serves three things, each on a loopback port it says at start:
//
//   - the Out-of-band controller: the controller's own code (internal/oobd)
//     over made-up ports and a made-up reset tool, whose resets land on
//     simulated Macs that this same process runs;
//   - a stand-in for GitHub (internal/githubtest), so that one account can
//     sign in and lend a Volunteer's Mac;
//   - a control port, which plays the owner's hands: a cable plugged, pulled
//     or moved, a Mac hung or shut down, the controller stopped, the mini
//     restarted.
//
// --files gets oobd.token (labd's --oobd-token-file) and sign-in/ (labd's
// --sign-in). It resets nothing real and is in no release.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/githubtest"
	"github.com/omacom/omarchy-maclab/internal/labhttp"
	"github.com/omacom/omarchy-maclab/internal/oob"
	"github.com/omacom/omarchy-maclab/internal/oobd"
)

// Product ids the real mini read on the M1 Pro and the M2 Max.
const (
	productM1Pro = 29448
	productM2Max = 29453
)

type cable struct {
	mac     string // the simulated Mac at its other end
	name    string // the name its Linux shows while it is up
	product int
	uuid    string
}

type run struct {
	rid          int
	began, ended time.Time
}

type mini struct {
	mu      sync.Mutex
	dir     string
	cables  map[int]*cable
	plugs   map[int]int
	sims    map[string]*agent.Sim
	silent  map[string]bool // hung or shut down: no name on its port
	runs    []run
	down    bool
	between map[int]bool // replug this port right after labd's next look
	toolFor time.Duration
	// replug makes a reset look like a pull to the port: its plug count goes
	// up and its connection id changes.
	replug bool
	// encrypted names a Mac whose disk waits for its passphrase at every
	// boot once it is cabled: one with no auto-unlock. watchdog brings a silenced Mac back by
	// itself: one whose watchdog was left on.
	encrypted string
	watchdog  bool
	// sloppy is a reset tool that does not keep to its port: every cabled
	// Mac restarts.
	sloppy bool
	ctx    context.Context
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	h := strings.ToUpper(hex.EncodeToString(b))
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (m *mini) plugLocked(rid int, mac, name string, product int) {
	m.cables[rid] = &cable{mac: mac, name: name, product: product, uuid: newUUID()}
	m.plugs[rid]++
}

func (m *mini) look(rid int) oob.Port {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := oob.Port{RID: rid, PlugCount: m.plugs[rid]}
	if c := m.cables[rid]; c != nil {
		p.Connected, p.ProductID = true, c.product
		if !m.silent[c.mac] {
			p.Name = c.name
		}
	}
	return p
}

func (m *mini) Read(_ context.Context, rid int) (oob.Port, error) {
	p := m.look(rid)
	m.mu.Lock()
	if c := m.cables[rid]; m.between[rid] && c != nil {
		delete(m.between, rid)
		m.plugLocked(rid, c.mac, c.name, c.product)
	}
	m.mu.Unlock()
	return p, nil
}

func (m *mini) Look(_ context.Context, rid int) (oob.Port, error) {
	p := m.look(rid)
	p.Name = ""
	return p, nil
}

// Run is the made-up reset tool: a reset takes toolFor, and restarts the
// simulated Mac on that port.
func (m *mini) Run(_ context.Context, rid int, command string) (string, error) {
	m.mu.Lock()
	c := m.cables[rid]
	var sim *agent.Sim
	if c != nil {
		sim = m.sims[c.mac]
	}
	m.mu.Unlock()
	if c == nil {
		return "No connection detected", fmt.Errorf("exit status 1")
	}
	if command != "reboot" {
		return "Connection: Source", nil
	}
	began := time.Now()
	time.Sleep(m.toolFor)
	if sim != nil {
		sim.PowerCycle()
	}
	m.mu.Lock()
	if m.sloppy {
		for _, other := range m.cables {
			if s := m.sims[other.mac]; s != nil && s != sim {
				go s.PowerCycle()
			}
		}
	}
	delete(m.silent, c.mac)
	if m.replug && m.cables[rid] == c {
		m.plugs[rid]++
		c.uuid = newUUID()
	}
	m.runs = append(m.runs, run{rid, began, time.Now()})
	m.mu.Unlock()
	return "Rebooting target", nil
}

// join enrolls a simulated Mac with a Join command's token and runs its
// lab-agent until this process ends.
func (m *mini) join(lab, name, token string) error {
	dir, err := os.MkdirTemp(m.dir, "mac-")
	if err != nil {
		return err
	}
	sim := agent.NewSim(dir)
	sim.RebootDelay = 500 * time.Millisecond
	terms, err := agent.FetchTerms(m.ctx, lab, token, labhttp.Policy{})
	if err != nil {
		return err
	}
	if err := agent.Setup(m.ctx, agent.SetupOptions{Server: lab, Token: token, AcceptTerms: terms.SHA256, Mac: sim, Log: func(string, ...any) {}}); err != nil {
		return err
	}
	cfg, err := agent.LoadConfig(filepath.Join(dir, "agent.json"))
	if err != nil {
		return err
	}
	if cfg.Name != name {
		return fmt.Errorf("the Mac joined as %s, not %s", cfg.Name, name)
	}
	m.mu.Lock()
	m.sims[name] = sim
	m.mu.Unlock()
	go agent.New(cfg, sim, log.New(io.Discard, "", 0)).Run(m.ctx)
	return nil
}

type named struct{ http.RoundTripper }

func (n named) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("User-Agent", "fake-mini/1")
	return n.RoundTripper.RoundTrip(r)
}

var (
	csrfField = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	joinToken = regexp.MustCompile(`enr_[0-9a-f]{32}`)
)

// volunteer has the account sign in at the lab as a browser would, make a
// Join command for a Mac to lend, and Joins a simulated Mac with it.
func (m *mini) volunteer(lab, name, account string) error {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Transport: named{http.DefaultTransport}, Timeout: time.Minute}
	c.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		if strings.HasSuffix(req.URL.Path, "/login/oauth/authorize") {
			q := req.URL.Query()
			q.Set("as", account)
			req.URL.RawQuery = q.Encode()
		}
		return nil
	}
	page := func(resp *http.Response, err error) (string, error) {
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("%s %s: %s", resp.Request.Method, resp.Request.URL.Path, resp.Status)
		}
		return string(b), nil
	}
	if _, err := page(c.Get(lab + "/sign-in")); err != nil {
		return err
	}
	lend, err := page(c.Get(lab + "/lend"))
	if err != nil {
		return err
	}
	csrf := csrfField.FindStringSubmatch(lend)
	if csrf == nil {
		return fmt.Errorf("account %s is not signed in, or /lend has no form", account)
	}
	form := url.Values{"csrf_token": {csrf[1]}, "model": {"j314s"}, "name": {name}}
	req, _ := http.NewRequest("POST", lab+"/lend/join-command", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", lab)
	made, err := page(c.Do(req))
	if err != nil {
		return err
	}
	token := joinToken.FindString(html.UnescapeString(made))
	if token == "" {
		return fmt.Errorf("the lab showed no Join command for %s", name)
	}
	return m.join(lab, name, token)
}

func ridOf(r *http.Request) (int, error) {
	rid, ok := oob.RIDOf(r.FormValue("port"))
	if !ok {
		return 0, fmt.Errorf("port is left, middle or right, not %q", r.FormValue("port"))
	}
	return rid, nil
}

func (m *mini) control() http.Handler {
	mux := http.NewServeMux()
	do := func(pattern string, f func(r *http.Request) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := f(r); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			fmt.Fprintln(w, "ok")
		})
	}
	do("POST /join", func(r *http.Request) error {
		return m.join(r.FormValue("lab"), r.FormValue("name"), r.FormValue("token"))
	})
	do("POST /volunteer", func(r *http.Request) error {
		return m.volunteer(r.FormValue("lab"), r.FormValue("name"), r.FormValue("account"))
	})
	do("POST /plug", func(r *http.Request) error {
		rid, err := ridOf(r)
		if err != nil {
			return err
		}
		mac := r.FormValue("mac")
		name, product := mac, productM2Max
		if r.Form.Has("name") {
			name = r.FormValue("name")
		}
		if strings.HasPrefix(mac, "m1") {
			product = productM1Pro
		}
		if r.Form.Has("product") {
			if product, err = strconv.Atoi(r.FormValue("product")); err != nil {
				return err
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if sim := m.sims[mac]; sim != nil && mac == m.encrypted {
			sim.Encrypted = true
		}
		m.plugLocked(rid, mac, name, product)
		return nil
	})
	do("POST /unplug", func(r *http.Request) error {
		rid, err := ridOf(r)
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.cables, rid)
		return err
	})
	do("POST /between", func(r *http.Request) error {
		rid, err := ridOf(r)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.between[rid] = err == nil
		return err
	})
	// A Mac hung or shut down: silent, and its port shows no name, until a
	// reset reaches it.
	do("POST /silence", func(r *http.Request) error {
		m.mu.Lock()
		sim := m.sims[r.FormValue("mac")]
		if sim != nil {
			m.silent[r.FormValue("mac")] = true
		}
		m.mu.Unlock()
		if sim == nil {
			return fmt.Errorf("no simulated Mac %q", r.FormValue("mac"))
		}
		go sim.Quiet(24 * time.Hour)
		if m.watchdog {
			time.AfterFunc(time.Second, sim.PowerCycle)
		}
		return nil
	})
	do("POST /controller", func(r *http.Request) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.down = r.FormValue("state") == "stopped"
		return nil
	})
	// The mini restarted: every port counts its plugs from the start.
	do("POST /restart", func(*http.Request) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, rid := range oob.RIDs {
			m.plugs[rid] = 0
			if c := m.cables[rid]; c != nil {
				m.plugs[rid], c.uuid = 1, newUUID()
			}
		}
		return nil
	})
	// The ports as scripts/mini/ports --seen says them.
	mux.HandleFunc("GET /seen", func(w http.ResponseWriter, _ *http.Request) {
		for _, rid := range oob.RIDs {
			p := m.look(rid)
			m.mu.Lock()
			c := m.cables[rid]
			m.mu.Unlock()
			if c == nil {
				fmt.Fprintf(w, "rid=%d port=%s connected=no product=none plugs=%d connection=none\n", rid, oob.PortName(rid), p.PlugCount)
				continue
			}
			fmt.Fprintf(w, "rid=%d port=%s connected=yes product=%d plugs=%d connection=%s\n", rid, oob.PortName(rid), p.ProductID, p.PlugCount, c.uuid)
		}
	})
	// The reset tool's runs, in order, in milliseconds since 1970.
	mux.HandleFunc("GET /resets", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, r := range m.runs {
			fmt.Fprintf(w, "rid=%d port=%s began=%d ended=%d\n", r.rid, oob.PortName(r.rid), r.began.UnixMilli(), r.ended.UnixMilli())
		}
	})
	return mux
}

func serve(what string, h http.Handler) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s http://%s\n", what, ln.Addr())
	go func() { log.Fatal((&http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}).Serve(ln)) }()
}

func main() {
	files := flag.String("files", "", "a folder for oobd.token, sign-in/ and the simulated Macs' own files")
	toolFor := flag.Duration("tool-time", 400*time.Millisecond, "how long the made-up reset tool takes for one reset")
	replug := flag.Bool("reset-counts-as-plug", false, "a reset moves the port's plug count and connection id, as a pull would")
	encrypted := flag.String("encrypted", "", "a Mac that stops at its passphrase after every reset: no auto-unlock")
	watchdog := flag.Bool("watchdog", false, "a hung Mac comes back by itself after a second: its watchdog was left on")
	sloppy := flag.Bool("reset-disturbs-all", false, "a reset restarts every cabled Mac, not only the one on its port")
	flag.Parse()
	if *files == "" || flag.NArg() != 0 {
		log.Fatal("usage: fake-mini --files DIR")
	}
	m := &mini{dir: *files, cables: map[int]*cable{}, plugs: map[int]int{1: 27, 0: 42, 2: 40}, sims: map[string]*agent.Sim{},
		silent: map[string]bool{}, between: map[int]bool{}, toolFor: *toolFor, replug: *replug, encrypted: *encrypted, watchdog: *watchdog, sloppy: *sloppy, ctx: context.Background()}

	secret := make([]byte, 32)
	rand.Read(secret)
	token := hex.EncodeToString(secret)
	signIn := filepath.Join(*files, "sign-in")
	if err := os.MkdirAll(signIn, 0o700); err != nil {
		log.Fatal(err)
	}
	g := githubtest.New("fake-client-id", "fake-client-secret")
	g.AddUser(1001, "owner")
	for name, value := range map[string]string{"oobd.token": token, "sign-in/github-client-id": g.ClientID, "sign-in/github-client-secret": g.ClientSecret, "sign-in/admins": "1001"} {
		if err := os.WriteFile(filepath.Join(*files, name), []byte(value+"\n"), 0o600); err != nil {
			log.Fatal(err)
		}
	}
	ctl, err := oobd.New(oobd.Config{Token: token, Tool: m, Ports: m}, log.New(os.Stderr, "oobd: ", log.LstdFlags))
	if err != nil {
		log.Fatal(err)
	}
	serve("controller", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		down := m.down
		m.mu.Unlock()
		if !down {
			ctl.Handler().ServeHTTP(w, r)
			return
		}
		// A stopped controller answers nothing at all.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
			}
		}
	}))
	serve("github", g)
	serve("control", m.control())
	select {}
}
