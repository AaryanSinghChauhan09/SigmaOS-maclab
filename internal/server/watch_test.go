package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/oob"
)

// opsStart is a Saturday afternoon in Brisbane: 2026-11-14 14:00 AEST.
var opsStart = time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)

// machine stands in for what labd watches of the mini: what the helper
// says, the disk, the Alias and the Lab address as the outside sees them.
type machine struct {
	mu          sync.Mutex
	mini        Mini
	miniErr     error
	free, total uint64
	alias       int // the status the Alias answers with; 0 is no answer
	lab         int
	asked       []string
}

func (m *machine) set(f func(m *machine)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(m)
}

type opsLab struct {
	t     *testing.T
	s     *Server
	data  string
	marks string
	clock *agent.FakeClock
	m     *machine
	stop  context.CancelFunc
}

const opsAlias = "maclab.omarchy.org"

// newOpsAt starts a labd on data whose mini is m, as a labd on the mini is
// started: with the Lab address, the Alias and the folder of marker files.
func newOpsAt(t *testing.T, data string, clock *agent.FakeClock, m *machine, mod func(*Config)) *opsLab {
	t.Helper()
	marks := filepath.Join(data, "marks")
	if err := os.MkdirAll(marks, 0o700); err != nil {
		t.Fatal(err)
	}
	pub, err := ParsePublicURL("https://lab.omarchy-mac.org")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataDir: data, Clock: clock, PublicURL: pub, Alias: opsAlias, Release: "v0.2.0-rc.1", Watch: &Watch{
		ByHand: true,
		Marks:  marks,
		Mini: func(context.Context) (Mini, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			out := m.mini
			out.Starts = map[string]int{}
			for k, v := range m.mini.Starts {
				out.Starts[k] = v
			}
			return out, m.miniErr
		},
		Disk: func() (uint64, uint64, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.free, m.total, nil
		},
		Ask: func(_ context.Context, address string) (int, string, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.asked = append(m.asked, address)
			status := m.lab
			if strings.HasPrefix(address, "https://"+opsAlias+"/") {
				status = m.alias
			}
			switch status {
			case 0:
				return 0, "", errors.New("no answer")
			case http.StatusPermanentRedirect:
				return status, "https://lab.omarchy-mac.org" + strings.TrimPrefix(address, "https://"+opsAlias), nil
			}
			return status, "", nil
		},
	}}
	if mod != nil {
		mod(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := New(ctx, cfg, log.New(io.Discard, "", 0))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	// labd looks once as it starts; the tests go on from there.
	s.look(context.Background())
	return &opsLab{t: t, s: s, data: data, marks: marks, clock: clock, m: m, stop: cancel}
}

// healthy is a mini with nothing wrong: started an hour before labd.
func healthy() *machine {
	return &machine{free: 60 << 30, total: 245 << 30, lab: 200,
		mini: Mini{Boot: opsStart.Add(-time.Hour), Tunnel: "up", Litestream: "running", Starts: map[string]int{"cloudflared": 1, "litestream": 1, "signal-cli": 1}}}
}

func newOps(t *testing.T, mod func(*Config)) *opsLab {
	t.Helper()
	return newOpsAt(t, t.TempDir(), agent.NewFakeClock(opsStart), healthy(), mod)
}

// told is every line recorded for "Agents" but the weekly digest, oldest
// first.
func (o *opsLab) told() []string {
	o.t.Helper()
	rows, err := o.s.store.db.Query(`SELECT text FROM lines WHERE channel = 'agents' AND text NOT LIKE 'Omarchy Mac Lab, weekly digest%' ORDER BY id`)
	if err != nil {
		o.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			o.t.Fatal(err)
		}
		out = append(out, text)
	}
	return out
}

// later moves the clock on and has labd look once more.
func (o *opsLab) later(d time.Duration) {
	o.t.Helper()
	o.clock.Advance(d)
	o.s.look(context.Background())
}

// minutes has labd look once a minute, n times.
func (o *opsLab) minutes(n int) {
	o.t.Helper()
	for i := 0; i < n; i++ {
		o.later(time.Minute)
	}
}

func (o *opsLab) wantTold(want ...string) {
	o.t.Helper()
	got := o.told()
	if len(got) != len(want) {
		o.t.Fatalf("\"Agents\" was told %d lines, want %d:\n%s\nwant:\n%s", len(got), len(want), strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			o.t.Errorf("line %d is\n%s\nwant\n%s", i+1, got[i], want[i])
		}
	}
}

// once checks one alert from its change to its recovery: nothing while all
// is well, nothing before hold has passed, one line then, none while the
// trouble lasts, one line when it is over, and none after.
func (o *opsLab) once(hold time.Duration, breakIt, mend func(m *machine), trouble, recovered string) {
	o.t.Helper()
	o.minutes(3)
	o.wantTold()
	o.m.set(breakIt)
	o.later(time.Second)
	if hold > 0 {
		o.later(hold - time.Minute)
		o.wantTold()
		o.later(time.Minute)
	}
	o.wantTold(trouble)
	o.minutes(45)
	o.wantTold(trouble)
	o.m.set(mend)
	o.later(time.Minute)
	o.wantTold(trouble, recovered)
	o.minutes(45)
	o.wantTold(trouble, recovered)
}

func TestWithEveryPartWellAgentsIsToldNothing(t *testing.T) {
	o := newOps(t, nil)
	o.minutes(120)
	o.wantTold()
}

func TestTheTunnelDisconnectedForOverFiveMinutesIsToldOnce(t *testing.T) {
	o := newOps(t, nil)
	o.once(5*time.Minute,
		func(m *machine) { m.mini.Tunnel = "down" },
		func(m *machine) { m.mini.Tunnel = "up" },
		"The tunnel has been disconnected for over 5 minutes, since Sat 14:03 AEST: the lab is Dark until it connects again.",
		"The tunnel is connected again. It was disconnected for 51 min.")
}

func TestATunnelThatComesBackWithinFiveMinutesIsNotTold(t *testing.T) {
	o := newOps(t, nil)
	o.m.set(func(m *machine) { m.mini.Tunnel = "down" })
	o.minutes(4)
	o.m.set(func(m *machine) { m.mini.Tunnel = "up" })
	o.minutes(1)
	o.m.set(func(m *machine) { m.mini.Tunnel = "down" })
	o.minutes(4)
	o.m.set(func(m *machine) { m.mini.Tunnel = "up" })
	o.minutes(10)
	o.wantTold()
}

func TestLitestreamFailingIsToldOnce(t *testing.T) {
	t.Run("not running", func(t *testing.T) {
		o := newOps(t, nil)
		o.once(2*time.Minute,
			func(m *machine) { m.mini.Litestream = "stopped" },
			func(m *machine) { m.mini.Litestream = "running" },
			"Litestream is failing on the mini: it is not running. The databases are not being copied to the house.",
			"Litestream copies the databases to the house again.")
	})
	t.Run("errors in its log", func(t *testing.T) {
		o := newOps(t, nil)
		o.minutes(3)
		for i := 0; i < 50; i++ { // a new error in its log at each look
			o.m.set(func(m *machine) { m.mini.LitestreamErrors += 7 })
			o.later(time.Minute)
		}
		trouble := "Litestream is failing on the mini: it keeps logging errors. The databases are not being copied to the house."
		o.wantTold(trouble)
		o.minutes(3) // no new error
		o.wantTold(trouble, "Litestream copies the databases to the house again.")
	})
	t.Run("one error is not failing", func(t *testing.T) {
		o := newOps(t, nil)
		o.minutes(3)
		o.m.set(func(m *machine) { m.mini.LitestreamErrors = 1 })
		o.minutes(30)
		o.wantTold()
	})
}

func (o *opsLab) mark(name string, at time.Time) {
	o.t.Helper()
	if err := os.WriteFile(filepath.Join(o.marks, name), []byte(at.UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		o.t.Fatal(err)
	}
}

func TestABackupOlderThan36HoursIsToldOnce(t *testing.T) {
	o := newOps(t, nil)
	o.mark("backup.ok", opsStart.Add(-11*time.Hour-30*time.Minute)) // Sat 02:30 AEST
	o.later(24 * time.Hour)
	o.wantTold()
	o.later(30 * time.Minute) // 36 hours to the minute
	o.wantTold()
	o.later(time.Minute)
	trouble := "The last good nightly backup is from Sat 14 Nov 02:30 AEST, 36 h 1 min ago: older than 36 hours."
	o.wantTold(trouble)
	o.later(20 * time.Hour)
	o.minutes(5)
	o.wantTold(trouble)
	o.mark("backup.ok", o.clock.Now().Add(-time.Minute))
	o.later(time.Minute)
	o.wantTold(trouble, "The nightly backup ran again: the last good one is from Mon 16 Nov 10:35 AEST.")
	o.minutes(30)
	o.wantTold(trouble, "The nightly backup ran again: the last good one is from Mon 16 Nov 10:35 AEST.")
}

func TestALabThatNeverMadeABackupIsNotToldOfAnOldOne(t *testing.T) {
	o := newOps(t, nil)
	o.later(72 * time.Hour)
	o.wantTold()
}

func TestDiskUnder15PercentIsToldOnce(t *testing.T) {
	o := newOps(t, nil)
	o.once(0,
		func(m *machine) { m.free = 30 << 30 },
		func(m *machine) { m.free = 52 << 30 },
		"The mini's disk is 12 % free (30 GB of 245 GB): under 15 %.",
		"The mini's disk is 21 % free again (52 GB of 245 GB).")
}

func TestDiskAt15PercentIsNotTold(t *testing.T) {
	o := newOps(t, nil)
	o.m.set(func(m *machine) { m.free, m.total = 15, 100 })
	o.minutes(10)
	o.wantTold()
}

// seen has the Builder ask labd for work now, as its poll does.
func (o *opsLab) seen() {
	o.s.builds.mu.Lock()
	o.s.builds.builders[testBuilderName] = o.s.now()
	o.s.builds.mu.Unlock()
}

func TestTheBuilderUnhealthyIsToldOnce(t *testing.T) {
	o := newOps(t, nil)
	testBuilder(t, o.s)
	for i := 0; i < 5; i++ {
		o.seen()
		o.later(30 * time.Second)
	}
	o.wantTold()
	// It stops asking. A minute of silence is not alive; two more and
	// "Agents" is told.
	o.minutes(2)
	o.wantTold()
	o.minutes(2)
	trouble := "The Builder is not healthy: it has not asked labd for work since Sat 14:02 AEST. Builds wait until it does."
	o.wantTold(trouble)
	o.minutes(45)
	o.wantTold(trouble)
	o.seen()
	o.later(10 * time.Second)
	o.wantTold(trouble, "The Builder asks labd for work again.")
	for i := 0; i < 30; i++ {
		o.seen()
		o.later(30 * time.Second)
	}
	o.wantTold(trouble, "The Builder asks labd for work again.")
}

func TestALabWithNoBuilderNamedIsNotToldOfOne(t *testing.T) {
	o := newOps(t, nil)
	o.minutes(30)
	o.wantTold()
	if got := o.status("Builder"); got != "none named" {
		t.Errorf("/status says of the Builder: %q", got)
	}
}

// controller stands in for the Out-of-band controller on this machine.
type controller struct {
	mu    sync.Mutex
	down  bool
	asked []string
	srv   *httptest.Server
	token string
}

func newController(t *testing.T) *controller {
	t.Helper()
	c := &controller{token: filepath.Join(t.TempDir(), "oobd.token")}
	if err := os.WriteFile(c.token, []byte(strings.Repeat("c", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.asked = append(c.asked, r.Method+" "+r.URL.RequestURI())
		if c.down {
			http.Error(w, "stopped", http.StatusBadGateway)
			return
		}
		io.WriteString(w, `{"rid":0,"connected":false,"plug_count":0}`)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *controller) client(t *testing.T) *oob.Client {
	t.Helper()
	cl, err := oob.NewClient(c.srv.URL, c.token)
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func TestTheOutOfBandControllerUnhealthyIsToldOnce(t *testing.T) {
	c := newController(t)
	o := newOps(t, func(cfg *Config) { cfg.OOB = c.client(t) })
	set := func(down bool) func(*machine) {
		return func(*machine) {
			c.mu.Lock()
			c.down = down
			c.mu.Unlock()
		}
	}
	o.once(2*time.Minute, set(true), set(false),
		"The Out-of-band controller on the mini is not healthy: it does not answer as it should. No Mac gets a Hard reset until it does; jobs still run.",
		"The Out-of-band controller on the mini answers again.")
	// labd asks what a port shows and never has the reset tool run.
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, a := range c.asked {
		if a != "GET /v1/ports/0/status" {
			t.Fatalf("labd asked the controller %q to see whether it is healthy", a)
		}
	}
}

func TestAControllerWithNoTokenYetIsNotSetUpAndNotTold(t *testing.T) {
	c := newController(t)
	os.Remove(c.token)
	o := newOps(t, func(cfg *Config) { cfg.OOB = c.client(t) })
	o.minutes(30)
	o.wantTold()
	if got := o.status("Out-of-band controller"); got != "not set up" {
		t.Errorf("/status says of the controller: %q", got)
	}
}

func TestAServiceRestartingRepeatedlyIsToldOnce(t *testing.T) {
	o := newOps(t, nil)
	o.minutes(3)
	// One restart, as a Deploy or the set-up makes: nothing.
	o.m.set(func(m *machine) { m.mini.Starts["cloudflared"] = 2 })
	o.minutes(15)
	o.wantTold()
	// It dies and launchd starts it again, every few seconds.
	for i := 0; i < 20; i++ {
		o.m.set(func(m *machine) { m.mini.Starts["cloudflared"] += 4 })
		o.later(time.Minute)
	}
	trouble := "cloudflared on the mini keeps restarting: launchd started it 4 times in the last 10 minutes."
	o.wantTold(trouble)
	o.minutes(9)
	o.wantTold(trouble)
	o.minutes(2)
	o.wantTold(trouble, "cloudflared on the mini has stopped restarting: launchd started it no more in 10 minutes.")
	// Loaded again by the set-up: the count starts over, and that is no news.
	o.m.set(func(m *machine) { m.mini.Starts["cloudflared"] = 1 })
	o.minutes(30)
	o.wantTold(trouble, "cloudflared on the mini has stopped restarting: launchd started it no more in 10 minutes.")
}

func TestLabdRestartingRepeatedlyIsToldOnce(t *testing.T) {
	data := t.TempDir()
	clock := agent.NewFakeClock(opsStart)
	m := healthy()
	a := newOpsAt(t, data, clock, m, nil)
	a.minutes(1)
	a.stop()
	clock.Advance(2 * time.Minute)
	b := newOpsAt(t, data, clock, m, nil)
	b.later(time.Second)
	b.wantTold()
	b.stop()
	clock.Advance(2 * time.Minute)
	c := newOpsAt(t, data, clock, m, nil)
	c.later(time.Second)
	trouble := "labd on the mini keeps restarting: it started 3 times in the last 10 minutes."
	c.wantTold(trouble)
	c.stop()
	clock.Advance(time.Minute)
	d := newOpsAt(t, data, clock, m, nil)
	d.later(time.Second)
	d.wantTold(trouble)
	d.minutes(9)
	d.wantTold(trouble)
	d.minutes(2)
	d.wantTold(trouble, "labd on the mini has stayed up for 10 minutes.")
	d.minutes(30)
	d.wantTold(trouble, "labd on the mini has stayed up for 10 minutes.")
}

func TestBackAfterARestartSaysHowLongTheLabWasDark(t *testing.T) {
	data := t.TempDir()
	clock := agent.NewFakeClock(opsStart)
	m := healthy()
	a := newOpsAt(t, data, clock, m, nil)
	a.minutes(30) // last known up at Sat 14:30 AEST
	a.wantTold()
	a.stop()

	// The power goes; the mini starts at 16:10 and waits for the Unlock;
	// labd runs again at 16:44.
	clock.Advance(134 * time.Minute)
	m.set(func(m *machine) { m.mini.Boot = opsStart.Add(130 * time.Minute) })
	b := newOpsAt(t, data, clock, m, nil)
	b.later(time.Second)
	back := "The mini restarted and the lab is back. It was Dark for 2 h 14 min, from Sat 14:30 AEST to Sat 16:44 AEST."
	b.wantTold(back)
	b.minutes(30)
	b.wantTold(back)
	b.stop()

	// labd alone restarts, as in a Deploy: the mini did not, and nothing is said.
	clock.Advance(time.Minute)
	c := newOpsAt(t, data, clock, m, nil)
	c.minutes(30)
	c.wantTold(back)
}

func TestAFirstStartIsNotBackFromAnything(t *testing.T) {
	o := newOps(t, nil)
	o.minutes(5)
	o.wantTold()
}

func TestTheAliasNoLongerActiveIsToldOnce(t *testing.T) {
	o := newOps(t, nil)
	// Never active: the other side has not added its record. Nothing to tell.
	o.minutes(90)
	o.wantTold()
	o.m.set(func(m *machine) { m.alias = http.StatusPermanentRedirect })
	o.minutes(30)
	o.wantTold()
	// It stops leading to the lab while the Lab address answers.
	o.m.set(func(m *machine) { m.alias = 0 })
	o.minutes(15)
	o.wantTold()
	o.minutes(20)
	trouble := "The Alias is no longer active: it stopped leading to the Lab address, which answers."
	o.wantTold(trouble)
	o.minutes(240)
	o.wantTold(trouble)
	o.m.set(func(m *machine) { m.alias = http.StatusPermanentRedirect })
	o.minutes(16)
	o.wantTold(trouble, "The Alias is active again.")
	o.minutes(60)
	o.wantTold(trouble, "The Alias is active again.")

	o.m.mu.Lock()
	defer o.m.mu.Unlock()
	for _, a := range o.m.asked {
		if a != "https://"+opsAlias+"/healthz" && a != "https://lab.omarchy-mac.org/healthz" {
			t.Fatalf("labd asked %s", a)
		}
	}
}

func TestWhileTheLabItselfIsDarkTheAliasIsNotBlamed(t *testing.T) {
	o := newOps(t, nil)
	o.m.set(func(m *machine) { m.alias = http.StatusPermanentRedirect })
	o.minutes(20)
	o.m.set(func(m *machine) { m.alias, m.lab = 0, 0 })
	o.minutes(120)
	o.wantTold()
}

func TestTheRefusedCredentialLimiterIsToldOnce(t *testing.T) {
	o := newOps(t, nil)
	o.s.agents.LimiterRefusing()
	o.s.agents.LimiterRefusing()
	trouble := "The refused-credential limiter is refusing an address: over 10 refused credentials came from it in 10 minutes, and further bad ones get 429 for 15 minutes. The caller log names it."
	o.wantTold(trouble)
	o.s.agents.LimiterQuiet()
	o.s.agents.LimiterQuiet()
	o.wantTold(trouble, "The refused-credential limiter refuses nobody now.")
}

func TestWhatTheHelperCannotSayIsNeitherTroubleNorRecovery(t *testing.T) {
	o := newOps(t, nil)
	o.m.set(func(m *machine) { m.mini.Tunnel = "down" })
	o.minutes(6)
	if len(o.told()) != 1 {
		t.Fatalf("told %q", o.told())
	}
	// The helper fails: the tunnel is not known to be back.
	o.m.set(func(m *machine) { m.miniErr = errors.New("parts: exit 1") })
	o.minutes(30)
	if len(o.told()) != 1 {
		t.Fatalf("told %q", o.told())
	}
	if got := o.status("Tunnel"); got != "not known: the mini's helper did not answer" {
		t.Errorf("/status says of the tunnel: %q", got)
	}
}

func TestParseParts(t *testing.T) {
	m, err := ParseParts("boot 1763092800\ntunnel down\nlitestream running 12\nservice cloudflared 3\nservice signal-cli 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Boot.Equal(time.Unix(1763092800, 0)) || m.Tunnel != "down" || m.Litestream != "running" || m.LitestreamErrors != 12 || m.Starts["cloudflared"] != 3 || m.Starts["signal-cli"] != 1 || len(m.Starts) != 2 {
		t.Errorf("read as %+v", m)
	}
	// A mini with no tunnel and no Litestream yet says nothing of them.
	m, err = ParseParts("boot 1763092800\n")
	if err != nil || m.Tunnel != "" || m.Litestream != "" || len(m.Starts) != 0 {
		t.Errorf("read as %+v, %v", m, err)
	}
	for _, bad := range []string{"", "tunnel up\n", "boot soon\n", "boot 1763092800\ntunnel maybe\n", "boot 1763092800\nlitestream running many\n", "boot 1763092800\nservice cloudflared\n", "boot 1763092800\nweather fine\n"} {
		if _, err := ParseParts(bad); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

func TestMiniPartsRunsTheHelper(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "parts")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho boot 1763092800\necho tunnel up\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := MiniParts(script)(context.Background())
	if err != nil || m.Tunnel != "up" {
		t.Fatalf("%+v, %v", m, err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho boot 1763092800\necho went wrong >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := MiniParts(script)(context.Background()); err == nil || !strings.Contains(err.Error(), "went wrong") {
		t.Fatalf("a helper that failed gave %v", err)
	}
	updates := filepath.Join(dir, "pending-updates")
	if err := os.WriteFile(updates, []byte("#!/bin/sh\necho 'macOS: macOS Tahoe 26.1.1-25B80'\necho\necho 'restic 0.19.1 -> 0.19.2'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := PendingUpdates(updates)(context.Background())
	if err != nil || fmt.Sprint(got) != "[macOS: macOS Tahoe 26.1.1-25B80 restic 0.19.1 -> 0.19.2]" {
		t.Fatalf("%q, %v", got, err)
	}
}

// status is what /status says of the part named name.
func (o *opsLab) status(name string) string {
	o.t.Helper()
	for _, p := range o.s.statusNow().Parts {
		if p.Name == name {
			return p.Text
		}
	}
	o.t.Fatalf("/status has no part %q", name)
	return ""
}

func (o *opsLab) state(name string) string {
	o.t.Helper()
	for _, p := range o.s.statusNow().Parts {
		if p.Name == name {
			return p.State
		}
	}
	o.t.Fatalf("/status has no part %q", name)
	return ""
}

// page is /status as a visitor who is not signed in gets it, at the
// loopback port.
func (o *opsLab) page() (int, string) {
	o.t.Helper()
	r := httptest.NewRequest("GET", "http://127.0.0.1/status", nil)
	w := httptest.NewRecorder()
	o.s.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func TestStatusShowsTheLabsSevenParts(t *testing.T) {
	c := newController(t)
	o := newOps(t, func(cfg *Config) { cfg.OOB = c.client(t) })
	testBuilder(t, o.s)
	o.seen()
	o.mark("backup.ok", opsStart.Add(-11*time.Hour-30*time.Minute))
	o.mark("restore-drill.ok", time.Date(2026, 10, 31, 19, 34, 0, 0, time.UTC))
	o.later(time.Second)
	want := []struct{ name, state, text string }{
		{"Server", "ok", "up since Sat 14 Nov 14:00 AEST, release v0.2.0-rc.1"},
		{"Tunnel", "ok", "connected"},
		{"Builder", "ok", "asking for work"},
		{"Out-of-band controller", "ok", "answering"},
		{"Last good backup", "ok", "Sat 14 Nov 02:30 AEST, 11 h 30 min ago"},
		{"Last Restore drill", "ok", "passed on Sun 1 Nov 05:34 AEST"},
		{"Last restart", "ok", "the mini on Sat 14 Nov 13:00 AEST"},
	}
	parts := o.s.statusNow().Parts
	if len(parts) != len(want) {
		t.Fatalf("/status has %d parts, want %d: %+v", len(parts), len(want), parts)
	}
	for i, w := range want {
		if p := parts[i]; p.Name != w.name || p.State != w.state || p.Text != w.text {
			t.Errorf("part %d is %q %q %q, want %q %q %q", i+1, p.Name, p.State, p.Text, w.name, w.state, w.text)
		}
	}
	code, body := o.page()
	if code != http.StatusOK {
		t.Fatalf("GET /status signed out: %d\n%s", code, body)
	}
	for _, w := range want {
		if !strings.Contains(body, w.name) || !strings.Contains(body, w.text) {
			t.Errorf("the page lacks %q with %q", w.name, w.text)
		}
	}
	if !strings.Contains(body, "Looked at Sat 14 Nov 14:00 AEST") {
		t.Errorf("the page does not say when labd last looked:\n%s", body)
	}
}

func TestStatusShowsWhatIsWrong(t *testing.T) {
	c := newController(t)
	o := newOps(t, func(cfg *Config) { cfg.OOB = c.client(t) })
	testBuilder(t, o.s)
	o.seen()
	o.mark("backup.ok", opsStart.Add(-40*time.Hour))
	o.later(time.Second)
	c.mu.Lock()
	c.down = true
	c.mu.Unlock()
	o.m.set(func(m *machine) { m.mini.Tunnel = "down" })
	o.minutes(3)
	for name, want := range map[string]string{
		"Tunnel":                 "disconnected since Sat 14 Nov 14:01 AEST",
		"Builder":                "not heard from since Sat 14 Nov 14:00 AEST",
		"Out-of-band controller": "not answering since Sat 14 Nov 14:01 AEST",
		"Last good backup":       "Thu 12 Nov 22:00 AEST, 40 h 3 min ago: older than 36 hours",
		"Last Restore drill":     "none yet",
	} {
		if got := o.status(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{"Tunnel": "bad", "Builder": "bad", "Out-of-band controller": "bad", "Last good backup": "bad", "Last Restore drill": "none", "Server": "ok"} {
		if got := o.state(name); got != want {
			t.Errorf("%s is %q, want %q", name, got, want)
		}
	}
	// The one line of the build plan: the controller stopped shows on
	// /status and gives one line; started again, one recovery line.
	_, body := o.page()
	if !strings.Contains(body, "not answering since Sat 14 Nov 14:01 AEST") {
		t.Errorf("the page does not show the stopped controller:\n%s", body)
	}
	c.mu.Lock()
	c.down = false
	c.mu.Unlock()
	o.later(time.Minute)
	if got := o.status("Out-of-band controller"); got != "answering" {
		t.Errorf("the controller, started again: %q", got)
	}
	var lines []string
	for _, l := range o.told() {
		if strings.Contains(l, "Out-of-band controller") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "is not healthy") || !strings.Contains(lines[1], "answers again") {
		t.Errorf("the controller's lines: %q", lines)
	}
}

// The page is open to anyone and so says nothing of anyone: no reason an
// outsider could use, no address, no path of the mini.
func TestStatusTellsAVisitorNoMoreThanStates(t *testing.T) {
	c := newController(t)
	c.down = true
	o := newOps(t, func(cfg *Config) { cfg.OOB = c.client(t) })
	o.m.set(func(m *machine) { m.miniErr = errors.New("/usr/local/lab/current/scripts/mini/parts: exit 1") })
	o.minutes(5)
	_, body := o.page()
	for _, leak := range []string{"127.0.0.1", "/usr/local", "/var/lab", "exit 1", "oobd.token", "Bad Gateway", "stopped"} {
		if strings.Contains(body, leak) {
			t.Errorf("the page shows %q", leak)
		}
	}
}

func TestAFailedRestoreDrillShowsOnStatusUntilOnePasses(t *testing.T) {
	o := newOps(t, nil)
	o.mark("restore-drill.ok", time.Date(2026, 10, 31, 19, 34, 0, 0, time.UTC))
	o.later(time.Minute)
	if w := o.ops("POST", "/agents/trouble/restore-drill", "The Restore drill failed: the house copy's labd.db is damaged."); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	o.later(time.Minute)
	if got, state := o.status("Last Restore drill"), o.state("Last Restore drill"); got != "failed on Sat 14 Nov 14:01 AEST; the last one that passed was on Sun 1 Nov 05:34 AEST" || state != "bad" {
		t.Errorf("%q %q", state, got)
	}
	o.mark("restore-drill.ok", o.clock.Now())
	o.ops("POST", "/agents/recovered/restore-drill", "The Restore drill passed again.")
	o.later(time.Minute)
	if got := o.status("Last Restore drill"); got != "passed on Sat 14 Nov 14:02 AEST" {
		t.Errorf("%q", got)
	}
}

// ops asks labd as one of the mini's own scripts does, on the socket only
// the lab account opens.
func (o *opsLab) ops(method, path, body string) *httptest.ResponseRecorder {
	o.t.Helper()
	r := httptest.NewRequest(method, "http://labd"+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	o.s.OpsHandler().ServeHTTP(w, r)
	return w
}

func TestAScriptsLineReachesAgentsOnce(t *testing.T) {
	o := newOps(t, nil)
	// A Deploy and a Maintenance run each say what they do.
	for _, line := range []string{"Deploy of v0.2.0-rc.2 on the mini: started.", "Deployed v0.2.0-rc.2: labd answers on the mini and through the Front door.", "Maintenance run on the mini: started. The lab is Dark while it restarts."} {
		if w := o.ops("POST", "/agents/line", line+"\n"); w.Code != 200 || w.Body.String() != "recorded\n" {
			t.Fatalf("%q: %d %q", line, w.Code, w.Body)
		}
	}
	o.wantTold("Deploy of v0.2.0-rc.2 on the mini: started.", "Deployed v0.2.0-rc.2: labd answers on the mini and through the Front door.", "Maintenance run on the mini: started. The lab is Dark while it restarts.")
}

func TestAScriptsTroubleIsToldOnChangeAndOnRecovery(t *testing.T) {
	o := newOps(t, nil)
	failed := "The Restore drill failed: the house copy's labd.db is damaged."
	if w := o.ops("POST", "/agents/recovered/restore-drill", "The Restore drill passed again."); w.Code != 200 || w.Body.String() != "nothing to tell\n" {
		t.Fatalf("a recovery from nothing: %d %q", w.Code, w.Body)
	}
	o.wantTold()
	if w := o.ops("POST", "/agents/trouble/restore-drill", failed); w.Code != 200 || w.Body.String() != "recorded\n" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if w := o.ops("POST", "/agents/trouble/restore-drill", "The Restore drill failed: R2 has no snapshot."); w.Code != 200 || w.Body.String() != "nothing to tell\n" {
		t.Fatalf("the same trouble again: %d %q", w.Code, w.Body)
	}
	o.wantTold(failed)
	o.ops("POST", "/agents/recovered/restore-drill", "The Restore drill passed again.")
	o.ops("POST", "/agents/recovered/restore-drill", "The Restore drill passed again.")
	o.wantTold(failed, "The Restore drill passed again.")
}

func TestTheOpsSocketTakesNothingElse(t *testing.T) {
	o := newOps(t, nil)
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/agents/line", "", 405},
		{"POST", "/agents/line", "", 400},
		{"POST", "/agents/line", "   \n", 400},
		{"POST", "/agents/line", strings.Repeat("x", 2001), 413},
		{"POST", "/agents/trouble/", "x", 404},
		{"POST", "/agents/trouble/Not%20A%20Key", "x", 400},
		{"POST", "/agents/trouble/" + strings.Repeat("k", 41), "x", 400},
		{"POST", "/lab/line", "x", 404},
		{"GET", "/api/devices", "", 404},
		{"GET", "/status", "", 404},
		{"GET", "/", "", 404},
	} {
		if w := o.ops(c.method, c.path, c.body); w.Code != c.want {
			t.Errorf("%s %s with %d bytes: %d, want %d", c.method, c.path, len(c.body), w.Code, c.want)
		}
	}
	o.wantTold()
	// And the lab's own routes have no such door.
	for _, path := range []string{"/agents/line", "/agents/trouble/x"} {
		r := httptest.NewRequest("POST", "http://127.0.0.1"+path, strings.NewReader("x"))
		w := httptest.NewRecorder()
		o.s.Handler().ServeHTTP(w, r)
		if w.Code != 404 && w.Code != 405 && w.Code != 403 {
			t.Errorf("POST %s at the loopback port: %d", path, w.Code)
		}
	}
	o.wantTold()
}

func TestTheOpsSocketIsTheLabAccountsAlone(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "labops")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "ops.sock")
	ln, err := ListenOps(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("the socket is %v, %v", st.Mode(), err)
	}
	if _, err := ListenOps(path); err == nil {
		t.Error("a second labd took the socket of one that is serving")
	}
	file := filepath.Join(dir, "a-file")
	os.WriteFile(file, nil, 0o600)
	if _, err := ListenOps(file); err == nil {
		t.Error("a file was taken for the socket")
	}
	if _, err := ListenOps("ops.sock"); err == nil {
		t.Error("a path that is not a full one was taken")
	}
}

func TestNextDigestIsTheNextMonday0900InBrisbane(t *testing.T) {
	for at, want := range map[string]string{
		"2026-11-14T04:00:00Z": "2026-11-15T23:00:00Z", // Sat 14:00 AEST: Mon 16 Nov 09:00 AEST
		"2026-11-15T22:59:59Z": "2026-11-15T23:00:00Z",
		"2026-11-15T23:00:00Z": "2026-11-22T23:00:00Z",
		"2026-11-16T10:00:00Z": "2026-11-22T23:00:00Z",
	} {
		from, _ := time.Parse(time.RFC3339, at)
		if got := nextDigest(from).UTC().Format(time.RFC3339); got != want {
			t.Errorf("after %s the digest goes at %s, want %s", at, got, want)
		}
	}
}

// digests is every weekly digest recorded for "Agents".
func (o *opsLab) digests() []string {
	o.t.Helper()
	rows, err := o.s.store.db.Query(`SELECT text FROM lines WHERE channel = 'agents' AND text LIKE 'Omarchy Mac Lab, weekly digest%' ORDER BY id`)
	if err != nil {
		o.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var text string
		rows.Scan(&text)
		out = append(out, text)
	}
	return out
}

func TestTheWeeklyDigestListsPendingUpdatesAndTheBackupAndDrillDates(t *testing.T) {
	var asked int
	var mu sync.Mutex
	updates := []string{"macOS: macOS Tahoe 26.1.1-25B80", "cloudflared 2026.10.1 -> 2026.11.0", "restic 0.19.1 -> 0.19.2"}
	o := newOps(t, func(cfg *Config) {
		cfg.Watch.Updates = func(context.Context) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			asked++
			return updates, nil
		}
	})
	o.mark("backup.ok", time.Date(2026, 11, 15, 16, 31, 0, 0, time.UTC))        // Mon 02:31 AEST
	o.mark("restore-drill.ok", time.Date(2026, 10, 31, 19, 34, 0, 0, time.UTC)) // Sun 1 Nov 05:34 AEST
	monday := time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)
	o.clock.Advance(monday.Add(-time.Minute).Sub(opsStart))
	if o.s.digestIfDue(context.Background()) {
		t.Fatal("a digest went before Monday 09:00")
	}
	o.clock.Advance(time.Minute)
	o.s.digestIfDue(context.Background())
	want := "Omarchy Mac Lab, weekly digest, Mon 16 Nov\n" +
		"Pending updates:\n- macOS: macOS Tahoe 26.1.1-25B80\n- cloudflared 2026.10.1 -> 2026.11.0\n- restic 0.19.1 -> 0.19.2\n" +
		"Last good backup: Mon 16 Nov 02:31 AEST, 6 h 29 min ago\n" +
		"Last good Restore drill: Sun 1 Nov 05:34 AEST"
	got := o.digests()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("the digest is\n%s\nwant\n%s", strings.Join(got, "\n---\n"), want)
	}
	// Not again that week, whoever asks and however often labd restarts.
	for i := 0; i < 3; i++ {
		o.clock.Advance(time.Minute)
		o.s.digestIfDue(context.Background())
	}
	o.stop()
	again := newOpsAt(t, o.data, o.clock, o.m, nil)
	again.clock.Advance(time.Hour)
	again.s.digestIfDue(context.Background())
	if got := again.digests(); len(got) != 1 {
		t.Fatalf("%d digests in one week", len(got))
	}
	// The next Monday, the next one.
	again.clock.Advance(7 * 24 * time.Hour)
	again.s.digestIfDue(context.Background())
	if got := again.digests(); len(got) != 2 {
		t.Fatalf("%d digests after two Mondays", len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != 1 {
		t.Errorf("the pending updates were asked for %d times for the first labd's one digest", asked)
	}
}

func TestTheWeeklyDigestSaysWhatItCouldNotList(t *testing.T) {
	for name, c := range map[string]struct {
		updates func(context.Context) ([]string, error)
		want    string
	}{
		"nothing waits":     {func(context.Context) ([]string, error) { return nil, nil }, "Pending updates: none\n"},
		"the helper failed": {func(context.Context) ([]string, error) { return nil, errors.New("exit 1") }, "Pending updates: could not be listed (see labd's log)\n"},
		"no helper":         {nil, "Pending updates: not looked at on this machine\n"},
	} {
		t.Run(name, func(t *testing.T) {
			o := newOps(t, func(cfg *Config) { cfg.Watch.Updates = c.updates })
			o.s.postWeeklyDigest(context.Background(), time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC))
			want := "Omarchy Mac Lab, weekly digest, Mon 16 Nov\n" + c.want + "Last good backup: none yet\nLast good Restore drill: none yet"
			if got := o.digests(); len(got) != 1 || got[0] != want {
				t.Fatalf("the digest is\n%s\nwant\n%s", strings.Join(got, "\n---\n"), want)
			}
		})
	}
}

// labd looks by itself as it starts and every minute after, and posts the
// digest when its hour comes.
func TestLabdLooksEveryMinuteByItself(t *testing.T) {
	monday := time.Date(2026, 11, 15, 23, 0, 0, 0, time.UTC)
	clock := agent.NewFakeClock(monday.Add(-90 * time.Second))
	m := healthy()
	m.mini.Boot = monday.Add(-time.Hour)
	o := newOpsAt(t, t.TempDir(), clock, m, func(cfg *Config) { cfg.Watch.ByHand = false })
	until := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("labd never %s", what)
			}
		}
	}
	until("looked as it started", func() bool { return o.s.statusNow().Looked == "Mon 16 Nov 08:58 AEST" })
	o.m.set(func(m *machine) { m.mini.Tunnel = "down" })
	clock.Advance(time.Minute)
	until("looked a minute later", func() bool { return o.status("Tunnel") == "disconnected since Mon 16 Nov 08:59 AEST" })
	if got := o.digests(); len(got) != 0 {
		t.Fatalf("a digest went before 09:00: %q", got)
	}
	clock.Advance(time.Minute)
	until("posted the weekly digest", func() bool { return len(o.digests()) == 1 })
}

// A labd that was given nothing to watch with watches nothing: no line, no
// note of its starts, and /status says so.
func TestALabdGivenNothingToWatchWithTellsNothing(t *testing.T) {
	data := t.TempDir()
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		s, err := New(ctx, Config{DataDir: data}, log.New(io.Discard, "", 0))
		if err != nil {
			t.Fatal(err)
		}
		var lines, marks int
		s.store.db.QueryRow(`SELECT count(*) FROM lines`).Scan(&lines)
		s.store.db.QueryRow(`SELECT count(*) FROM line_marks`).Scan(&marks)
		if lines != 0 || marks != 0 {
			t.Fatalf("start %d: %d lines and %d notes", i+1, lines, marks)
		}
		st := s.statusNow()
		if len(st.Parts) != 7 || st.Parts[0].State != partOK || st.Parts[1].Text != "not watched on this machine" {
			t.Fatalf("/status: %+v", st)
		}
		cancel()
	}
}
