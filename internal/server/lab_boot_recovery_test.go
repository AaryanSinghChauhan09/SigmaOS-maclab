package server

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/api"
)

// submitJob submits a job for the lab's Mac through labd's API.
func (l *lab) submitJob(spec api.JobSpec) string {
	l.t.Helper()
	spec.Device = l.name
	r, body := l.post("/api/jobs", spec)
	var j api.Job
	if r.StatusCode != 200 || json.Unmarshal([]byte(body), &j) != nil || j.ID == "" {
		l.t.Fatalf("submit: %s %s", r.Status, body)
	}
	return j.ID
}

// jobAPI waits up to d for a job to finish and returns it as labd's API shows it.
func (l *lab) jobAPI(id string, d time.Duration) *api.Job {
	l.t.Helper()
	l.wait(id, d)
	r, body := l.get("GET", "/api/jobs/"+id)
	var j api.Job
	if r.StatusCode != 200 || json.Unmarshal([]byte(body), &j) != nil {
		l.t.Fatalf("job %s: %s %s", id, r.Status, body)
	}
	return &j
}

// lostBoot waits for the Mac to be up on a kernel and returns that boot's ID.
func (l *lab) lostBoot(kernel string) string {
	l.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if id, k, _ := l.sim.Identity(); k == kernel {
			return id
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.t.Fatalf("the Mac never booted %s", kernel)
	return ""
}

// journalOf is the job's attached journal file that names a boot, or "".
func (l *lab) journalOf(j *api.Job, boot string) string {
	l.t.Helper()
	for _, f := range j.Result.Logs {
		if !strings.Contains(f, "journal") {
			continue
		}
		if r, body := l.get("GET", "/api/jobs/"+j.ID+"/files/"+(&url.URL{Path: f}).EscapedPath()); r.StatusCode == 200 && strings.Contains(body, boot) {
			return f
		}
	}
	return ""
}

// bootIDs records every boot ID the Mac goes through until stop.
func (l *lab) bootIDs() (stop func() []string) {
	var mu sync.Mutex
	var ids []string
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			id, _, _ := l.sim.Identity()
			mu.Lock()
			if len(ids) == 0 || ids[len(ids)-1] != id {
				ids = append(ids, id)
			}
			mu.Unlock()
			select {
			case <-quit:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return func() []string { close(quit); <-done; mu.Lock(); defer mu.Unlock(); return ids }
}

// lostContactLab is a lab on a fake clock that runs at wall speed; tests jump
// it past a deadline.
func lostContactLab(t *testing.T, c labConfig) (*lab, *agent.FakeClock) {
	t.Helper()
	clock := agent.NewFakeClock(time.Now())
	c.clock = clock
	l := newLabWith(t, c)
	t.Cleanup(pumpClock(clock))
	if c.volunteer {
		l.openForTheLab(clock.Now())
	}
	l.baseline()
	return l, clock
}

// The harness scenario of #30: a Sim kernel that loses the network after
// boot is rebooted by its deadline, and the job fails lost_contact with the
// lost boot's journal attached.
func TestLabBootThatLosesTheNetworkIsRebootedByItsDeadline(t *testing.T) {
	l, clock := lostContactLab(t, labConfig{volunteer: true})
	id := l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-offline behavior=no-network")})
	lost := l.lostBoot("7.2-offline")
	clock.Advance(2 * 4 * time.Second) // past the no-contact deadline: 2x labd's 4s boot timeout
	j := l.jobAPI(id, 60*time.Second)
	l.expect(j, api.OutcomeLostContact)
	if f := l.journalOf(j, lost); f == "" {
		t.Fatalf("no journal of the lost boot %s attached: %v", lost, j.Result.Logs)
	}
	l.waitDevice(func(d api.Device) bool {
		return d.State == api.StateReady && d.Kernel == agent.SimKnownGood && d.BootID != lost
	})
}

func TestLostContactIsAFailure(t *testing.T) {
	l, clock := lostContactLab(t, labConfig{volunteer: true})
	id := l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-offline behavior=no-network")})
	l.lostBoot("7.2-offline")
	clock.Advance(2 * 4 * time.Second)
	j := l.jobAPI(id, 60*time.Second)
	l.expect(j, api.OutcomeLostContact)
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		var failed bool
		for _, n := range l.notes {
			failed = failed || strings.Contains(n.Title, id) && strings.Contains(n.Title, api.OutcomeLostContact)
		}
		l.mu.Unlock()
		if failed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no failed-job notice for %s: %+v", id, l.notes)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLabOwnedMacComesBackThroughItsDeadlineBeforeAnOOBHardReset(t *testing.T) {
	l, clock := lostContactLab(t, labConfig{oob: true})
	id := l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-offline behavior=no-network")})
	l.lostBoot("7.2-offline")
	clock.Advance(2 * 4 * time.Second)
	j := l.jobAPI(id, 60*time.Second)
	l.expect(j, api.OutcomeLostContact)
	if resets, _ := l.oob.resetsSent(); resets != 0 {
		t.Fatalf("%d Hard resets before the deadline brought the Mac back; recovery %v", resets, j.Result.Recovery)
	}
	l.waitDevice(func(d api.Device) bool { return d.State == api.StateReady && d.Kernel == agent.SimKnownGood })
}

func TestPanickedLabBootsJournalComesBackByBootID(t *testing.T) {
	l := newLabWith(t, labConfig{volunteer: true})
	l.openForTheLab(time.Now())
	l.baseline()
	ids := l.bootIDs()
	id := l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-bad behavior=panic")})
	j := l.jobAPI(id, 60*time.Second)
	boots := ids()
	if j.Outcome == api.OutcomePass || j.Outcome == api.OutcomeLostContact {
		t.Fatalf("a boot that came back through panic=10: outcome %s", j.Outcome)
	}
	if len(boots) < 3 {
		t.Fatalf("boots %v: want the one before, the lost one and the one after", boots)
	}
	lost := boots[1]
	if f := l.journalOf(j, lost); f == "" {
		t.Fatalf("no journal of the panicked boot %s attached: %v", lost, j.Result.Logs)
	}
}

func TestLabBootCmdlineCarriesPanicAndWatchdogTimeouts(t *testing.T) {
	l := newLabWith(t, labConfig{volunteer: true})
	l.openForTheLab(time.Now())
	l.baseline()
	j := l.jobAPI(l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-ok behavior=ok")}), 60*time.Second)
	l.expect(j, api.OutcomePass)
	fields := strings.Fields(j.Result.BootCmdline)
	var panic10, wdt bool
	for _, f := range fields {
		panic10 = panic10 || f == "panic=10"
		wdt = wdt || strings.HasPrefix(f, "watchdog.open_timeout=") && f != "watchdog.open_timeout=0"
	}
	if !panic10 || !wdt {
		t.Fatalf("Lab boot cmdline %q lacks panic=10 or watchdog.open_timeout=N", j.Result.BootCmdline)
	}
}

// A Sim kernel artifact marked apple_wdt=n was built without apple_wdt.
func TestJobFlagsATestKernelBuiltWithoutAppleWDT(t *testing.T) {
	l := newLabWith(t, labConfig{volunteer: true})
	l.openForTheLab(time.Now())
	l.baseline()
	flagged := func(j *api.Job) bool {
		if strings.Contains(j.Summary, "apple_wdt") {
			return true
		}
		for _, e := range j.Events {
			if strings.Contains(e.Msg, "apple_wdt") {
				return true
			}
		}
		return false
	}
	with := l.jobAPI(l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-wdt behavior=ok")}), 60*time.Second)
	if flagged(with) {
		t.Fatalf("a kernel with apple_wdt was flagged: %s %v", with.Summary, with.Events)
	}
	l.waitDevice(func(d api.Device) bool { return d.State == api.StateReady })
	without := l.jobAPI(l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-nowdt behavior=ok apple_wdt=n")}), 60*time.Second)
	if !flagged(without) {
		t.Fatalf("a kernel without apple_wdt was not flagged: %s %v", without.Summary, without.Events)
	}
}

// withoutLabBootAdditions drops what #30 adds to a Lab boot's command line
// (watchdog.open_timeout=N and the Lab boot marker), so tests that pin the rest
// hold before and after it.
func withoutLabBootAdditions(cmdline string) string {
	var keep []string
	for _, f := range strings.Fields(cmdline) {
		v, wdt := strings.CutPrefix(f, "watchdog.open_timeout=")
		if wdt && v != "" && strings.Trim(v, "0123456789") == "" || agent.IsLabBoot(f) && !strings.HasPrefix(f, "maclab.job=") {
			continue
		}
		keep = append(keep, f)
	}
	return strings.Join(keep, " ")
}

// With no Sighting the ladder skips the Hard-reset rung: the Mac's own
// watchdog has its time, then the Mac Needs hands, with the reason shown.
func TestWithNoSightingTheLadderSkipsTheHardResetRung(t *testing.T) {
	for _, c := range []struct {
		name string
		// cable changes what the mini's right port holds before the job.
		cable func(l *lab)
		state string
		why   []string
	}{
		{"no cable", func(l *lab) { l.oob.unplug(portRight) }, api.CableNone, []string{"no cable can be the one of sim-mac"}},
		{"not sighted", func(l *lab) { l.oob.plug(portRight, l.sim, "", productM1Pro) }, api.CableNotSighted,
			[]string{"the lab has no Sighting of sim-mac", "A Maintainer can prove its port: `lab reset sim-mac --prove right`"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := sightLab(t, labConfig{})
			l.baseline()
			c.cable(l)
			l.sight()
			l.wantCable(l.name, c.state, c.why...)
			if tier := l.deviceJSON(l.name)["tier"]; tier == float64(2) {
				t.Fatal("a Mac the lab cannot place is shown with a Hard reset")
			}
			started := time.Now()
			j := l.run(api.JobSpec{Kernel: l.artifact("krel=7.2-hang behavior=hang")})
			l.expect(j, api.OutcomeHung)
			steps := strings.Join(j.Result.Recovery, "|")
			if strings.Contains(steps, "hard reset") || !strings.Contains(steps, "no Hard reset: "+c.why[0]) {
				t.Fatalf("recovery: %v", j.Result.Recovery)
			}
			// The Mac's own watchdog came first: labd waited for it and a
			// boot (2 s and 4 s here) past the boot timeout before the ladder.
			if waited := time.Since(started); waited < 10*time.Second || !strings.Contains(steps, "nor come back by itself") {
				t.Fatalf("the ladder began %s after the job, before the Mac's own watchdog had its time: %v", waited.Round(time.Second), j.Result.Recovery)
			}
			l.wantResets("the lab had no Sighting")
			d := l.waitDevice(func(d api.Device) bool { return d.State == api.StateNeedsHands })
			if d.Action == nil {
				t.Fatal("no instruction for the person at the Mac")
			}
			for _, piece := range append(c.why, "No Hard reset was sent: ") {
				if !strings.Contains(d.Action.Why, piece) {
					t.Fatalf("the reason shown %q lacks %q", d.Action.Why, piece)
				}
			}
			l.sim.PowerCycle() // the person
			l.waitDevice(func(d api.Device) bool { return d.State == api.StateReady })
		})
	}
}

// The Mac's own watchdog comes before a person exactly where no Hard reset
// reaches the Mac: labd's wait for a boot it lost allows for the watchdog
// with no Sighting and with the controller stopped, and for none while the
// Mac is sighted, when the Hard reset is the next rung.
func TestTheMacsOwnWatchdogGetsItsTimeWhereNoHardResetReaches(t *testing.T) {
	l := sightLab(t, labConfig{})
	l.baseline()
	go l.sim.Quiet(time.Minute)
	l.waitDevice(func(d api.Device) bool { return time.Since(d.LastSeen) > 2*aliveWindow })
	r := &jobRun{s: l.s, v: l.s.dev(l.name), boot: 4 * time.Second}
	const watchdog = 2*time.Second + 4*time.Second // lab-agent's idle reset here, and a boot
	check := func(when string, waits bool) {
		t.Helper()
		allowance, more := r.watchdogAllowance(), r.watchdogRecovery(time.Second)
		if waits && (allowance != watchdog || more <= 0) || !waits && (allowance != 0 || more != 0) {
			t.Fatalf("%s: labd allows the watchdog %s and waits %s more for it; waits for it: %v", when, allowance, more, waits)
		}
	}
	check("sighted", false)
	l.oob.stop(true)
	l.sight()
	check("the controller stopped", true)
	l.oob.stop(false)
	l.sight()
	check("the controller back", false)
	l.oob.shows(portRight, "")
	l.oob.plug(portRight, l.sim, "", productM1Pro)
	l.sight()
	check("not sighted", true)
	l.oob.unplug(portRight)
	l.sight()
	check("no cable", true)
}

// swapped serves whichever labd the test has put behind the lab's address.
type swapped struct {
	mu sync.Mutex
	h  http.Handler
}

func (s *swapped) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	h.ServeHTTP(w, r)
}

func (s *swapped) set(h http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.h = h
}

// After labd starts again it runs recovery on whatever did not return and
// writes the result for "Agents": a Hard reset where it has a Sighting,
// Needs hands with the reason where it has none, and nothing for a Mac that
// returned.
func TestAfterLabdStartsAgainItRunsRecoveryOnWhateverDidNotReturn(t *testing.T) {
	front := &swapped{}
	l := newLabWith(t, labConfig{oob: true, wrap: func(h http.Handler) http.Handler { front.set(h); return front }})
	l.baseline()
	uncabled, well := l.addMac("m2-max"), l.addMac("m3")
	_ = well
	// Two Macs hang while the lab is Dark; the third stays up.
	go l.sim.Quiet(time.Hour)
	go uncabled.Quiet(time.Hour)
	l.waitDevice(func(d api.Device) bool { return time.Since(d.LastSeen) > 2*aliveWindow })

	logs := &logBuf{}
	fresh, err := New(l.ctx, l.s.cfg, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var notes []Notification
	fresh.OnNotify(func(n Notification) { mu.Lock(); notes = append(notes, n); mu.Unlock() })
	front.set(fresh.Handler())
	boot := fresh.dev(l.name).snapshot().BootID

	var line string
	for deadline := time.Now().Add(40 * time.Second); line == ""; time.Sleep(100 * time.Millisecond) {
		for _, text := range agentsLinesOf(t, fresh) {
			if strings.HasPrefix(text, "After the lab started again") {
				line = text
			}
		}
		if line == "" && time.Now().After(deadline) {
			t.Fatalf("no result for \"Agents\"; labd said:\n%s", logs)
		}
	}
	for _, piece := range []string{"2 Lab-owned Macs did not return, and the lab ran recovery.", "sim-mac is back after a Hard reset.", "m2-max needs hands: did not return after the lab started again. No Hard reset was sent: no cable can be the one of m2-max"} {
		if !strings.Contains(line, piece) {
			t.Fatalf("the line %q lacks %q", line, piece)
		}
	}
	if strings.Contains(line, "m3") {
		t.Fatalf("a Mac that returned is in the line: %q", line)
	}
	l.wantResets("recovery after the start", portRight)
	if d := fresh.dev(l.name).snapshot(); d.BootID == boot || d.State != api.StateReady {
		t.Fatalf("%s after its Hard reset: boot %s (was %s), %s", l.name, d.BootID, boot, d.State)
	}
	if d := fresh.dev("m2-max").snapshot(); d.State != api.StateNeedsHands || d.Action == nil {
		t.Fatalf("m2-max: %s %+v", d.State, d.Action)
	}
	if d := fresh.dev("m3").snapshot(); d.State == api.StateNeedsHands || d.State == api.StateRecovering {
		t.Fatalf("m3, which returned: %s", d.State)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notes) != 1 || notes[0].Device != "m2-max" || notes[0].Priority != "urgent" {
		t.Fatalf("who was asked for hands: %+v", notes)
	}
	// It is written once.
	time.Sleep(time.Second)
	var n int
	for _, text := range agentsLinesOf(t, fresh) {
		if strings.HasPrefix(text, "After the lab started again") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the result was written %d times", n)
	}
}
