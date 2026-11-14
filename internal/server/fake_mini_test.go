package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/oob"
	"github.com/omacom/omarchy-maclab/internal/oobd"
)

// The mini's ports as the tests name them.
const (
	portLeft   = 1
	portMiddle = 0
	portRight  = 2
)

// Product ids the mini read on the two test Macs.
const (
	productM1Pro = 29448
	productM2Max = 29453
)

// fakeCable is what is plugged into one of the mini's ports.
type fakeCable struct {
	sim       *agent.Sim // the Mac at its other end; nil for none of the lab's
	name      string     // the name its Linux shows; "" for none
	productID int
}

// fakeMini is the mini as labd reaches it: the Out-of-band controller's own
// code, with made-up ports and a made-up reset tool whose resets land on
// simulated Macs.
type fakeMini struct {
	mu     sync.Mutex
	cables map[int]*fakeCable
	plugs  map[int]int
	resets int   // resets the tool sent
	sent   []int // the RID of each, in order
	down   bool  // the controller does not answer
	// afterRead runs after the controller read a port for labd, with no lock
	// held: a test changes the port between labd's look and the controller's.
	afterRead func(rid int)
	// hold, when set, keeps the tool's nop from ending until it is closed;
	// probing is closed when the first nop held began.
	hold    chan struct{}
	probing chan struct{}
	client  *oob.Client
	log     *logBuf
}

// holdProbes makes the tool's next nop run until release is called: a tool
// run that holds the controller's lock. probing is closed when it began.
func (m *fakeMini) holdProbes(t *testing.T) (probing <-chan struct{}, release func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hold, began := make(chan struct{}), make(chan struct{})
	m.hold, m.probing = hold, began
	var once sync.Once
	release = func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	return began, release
}

func newFakeMini(t *testing.T) *fakeMini {
	t.Helper()
	m := &fakeMini{cables: map[int]*fakeCable{}, plugs: map[int]int{portLeft: 27, portMiddle: 42, portRight: 40}, log: &logBuf{}}
	const token = "a-token-of-the-tests-and-of-no-real-controller"
	ctl, err := oobd.New(oobd.Config{Token: token, Tool: m, Ports: m}, log.New(m.log, "oobd: ", 0))
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		down := m.down
		m.mu.Unlock()
		if down {
			http.Error(w, "stopped", http.StatusServiceUnavailable)
			return
		}
		ctl.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	file := filepath.Join(t.TempDir(), "oobd.token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m.client, err = oob.NewClient(hs.URL, file); err != nil {
		t.Fatal(err)
	}
	return m
}

// plug puts a cable into a port: the port counts one more plug.
func (m *fakeMini) plug(rid int, sim *agent.Sim, name string, productID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cables[rid] = &fakeCable{sim: sim, name: name, productID: productID}
	m.plugs[rid]++
}

func (m *fakeMini) unplug(rid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cables, rid)
}

// shows changes the name the Mac on a port shows, its cable untouched.
func (m *fakeMini) shows(rid int, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cables[rid].name = name
}

// swap changes what a port's partner says it is, its cable untouched.
func (m *fakeMini) swap(rid, productID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cables[rid].productID = productID
}

// onRead sets what runs after the controller read a port for labd.
func (m *fakeMini) onRead(f func(rid int)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.afterRead = f
}

func (m *fakeMini) stop(down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.down = down
}

// resetsSent is how many resets the tool sent, and to which RIDs.
func (m *fakeMini) resetsSent() (int, []int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resets, append([]int(nil), m.sent...)
}

func (m *fakeMini) look(rid int) oob.Port {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := oob.Port{RID: rid, PlugCount: m.plugs[rid]}
	if c := m.cables[rid]; c != nil {
		p.Connected, p.Name, p.ProductID = true, c.name, c.productID
	}
	return p
}

func (m *fakeMini) Read(_ context.Context, rid int) (oob.Port, error) {
	p := m.look(rid)
	m.mu.Lock()
	after := m.afterRead
	m.mu.Unlock()
	if after != nil {
		after(rid)
	}
	return p, nil
}

func (m *fakeMini) Look(_ context.Context, rid int) (oob.Port, error) {
	p := m.look(rid)
	p.Name = ""
	return p, nil
}

func (m *fakeMini) Run(_ context.Context, rid int, command string) (string, error) {
	if command != "reboot" {
		m.mu.Lock()
		hold, probing := m.hold, m.probing
		m.hold, m.probing = nil, nil
		m.mu.Unlock()
		if hold != nil {
			close(probing)
			<-hold
		}
		return "nop", nil
	}
	m.mu.Lock()
	m.resets++
	m.sent = append(m.sent, rid)
	var sim *agent.Sim
	if c := m.cables[rid]; c != nil {
		sim = c.sim
	}
	m.mu.Unlock()
	if sim != nil {
		sim.PowerCycle()
	}
	return "rebooting", nil
}

// cableOf is the Mac's Cable as labd's API shows it; nil for none.
func (l *lab) cableOf(name string) map[string]any {
	l.t.Helper()
	c, _ := l.deviceJSON(name)["cable"].(map[string]any)
	return c
}

// sight has labd look at the mini's ports now.
func (l *lab) sight() {
	l.t.Helper()
	l.s.sight(l.ctx)
}

// agentsLines are the lines labd recorded for "Agents", oldest first.
func (l *lab) agentsLines() []string { return agentsLinesOf(l.t, l.s) }

func agentsLinesOf(t *testing.T, s *Server) []string {
	t.Helper()
	rows, err := s.store.db.Query(`SELECT text FROM lines WHERE channel = ? ORDER BY id`, string(lineAgents))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatal(err)
		}
		out = append(out, text)
	}
	return out
}

// quiet is a logger that keeps nothing.
func quietLog() *log.Logger { return log.New(io.Discard, "", 0) }

// has reports whether one of lines contains every piece.
func has(lines []string, pieces ...string) bool {
	for _, line := range lines {
		all := true
		for _, p := range pieces {
			all = all && strings.Contains(line, p)
		}
		if all {
			return true
		}
	}
	return false
}

// sightLab is a lab whose Mac is on the mini's right port, and whose labd
// looks at the ports only when the test says so (l.sight).
func sightLab(t *testing.T, c labConfig) *lab {
	t.Helper()
	c.oob = true
	config := c.config
	c.config = func(cfg *Config) {
		if config != nil {
			config(cfg)
		}
		cfg.SightEvery = time.Hour
	}
	return newLabWith(t, c)
}

// addMac enrolls one more Lab-owned Mac, a simulated one whose lab-agent
// runs, and waits for its first check-in.
func (l *lab) addMac(name string) *agent.Sim {
	l.t.Helper()
	sim := agent.NewSim(l.t.TempDir())
	sim.RebootDelay = 300 * time.Millisecond
	l.enrollMac(name, nil, sim, true)
	l.waitDeviceJSON(name, func(m map[string]any) bool { return m["boot_id"] != "" && m["alive"] == true })
	return sim
}

// bootOf is the boot id labd has for a Mac.
func (l *lab) bootOf(name string) string { return l.s.dev(name).snapshot().BootID }

// resetAs asks for a Hard reset of a Mac, or with prove a Proving reset.
func (l *lab) resetAs(token, name, prove string) (*http.Response, string) {
	l.t.Helper()
	path := "/api/devices/" + name + "/reset"
	if prove != "" {
		path += "?prove=" + prove
	}
	return l.as(token, "POST", path, nil)
}

// wantResets fails unless the reset tool sent resets to exactly these RIDs.
func (l *lab) wantResets(why string, rids ...int) {
	l.t.Helper()
	if _, sent := l.oob.resetsSent(); !slices.Equal(sent, rids) {
		l.t.Fatalf("%s: resets went to RIDs %v, want %v", why, sent, rids)
	}
}

// wantCable fails unless labd shows the Mac's cable in this state, with a
// reason for no Hard reset that contains every piece of why ("" for none).
func (l *lab) wantCable(name, state string, why ...string) {
	l.t.Helper()
	c := l.cableOf(name)
	if c == nil || c["state"] != state {
		l.t.Fatalf("%s's cable is %v, want %q", name, c, state)
	}
	no, _ := c["no_hard_reset"].(string)
	if len(why) == 0 && no != "" {
		l.t.Fatalf("%s is shown as having no Hard reset: %s", name, no)
	}
	for _, piece := range why {
		if !strings.Contains(no, piece) {
			l.t.Fatalf("%s has no Hard reset because %q, which lacks %q", name, no, piece)
		}
	}
}
