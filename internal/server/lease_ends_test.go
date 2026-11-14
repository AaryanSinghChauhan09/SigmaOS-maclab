package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// How a Lease ends, one test a row of the spec's table, on a simulated Mac
// that runs the jobs. grace is an M team member with a Lab token; ada is
// the lab's Maintainer.

type endsLab struct {
	*lab
	sig   *fakeSignal
	grace testMaintainer
}

func newEndsLab(t *testing.T) *endsLab {
	t.Helper()
	sig := &fakeSignal{}
	dir := signalDir(t)
	l := newLabWith(t, labConfig{oob: true, config: func(c *Config) { c.Signal, c.SignalDir = sig, dir }})
	l.baseline()
	e := &endsLab{lab: l, sig: sig}
	e.grace = l.member("grace")
	return e
}

// member is an M team member, no Maintainer, with a Lab token.
func (l *lab) member(name string) testMaintainer {
	l.t.Helper()
	m := l.addMaintainer("", name)
	if err := l.s.store.setMaintainer(testAccount(name), false); err != nil {
		l.t.Fatal(err)
	}
	if err := l.s.store.setMark(testAccount(name), "m_team", true); err != nil {
		l.t.Fatal(err)
	}
	return m
}

func (l *lab) leaseAs(m testMaintainer, ttl time.Duration) api.Lease {
	l.t.Helper()
	r, body := l.as(m.Token, "POST", "/api/devices/"+l.name+"/lease", api.LeaseRequest{Holder: m.Name + "@laptop", TTLSec: int(ttl.Seconds())})
	if r.StatusCode != 200 {
		l.t.Fatalf("%s's Lease: %s %s", m.Name, r.Status, body)
	}
	var got api.Lease
	json.Unmarshal([]byte(body), &got)
	return got
}

func (l *lab) mustSubmitAs(m testMaintainer, spec api.JobSpec) string {
	l.t.Helper()
	code, id, body := l.submitAs(m.Token, spec)
	if code != 200 {
		l.t.Fatalf("%s's job: %d %s", m.Name, code, body)
	}
	return id
}

// longJobAs has m's kernel job running its test on the Mac, for about d.
func (l *lab) longJobAs(m testMaintainer, krel string, d time.Duration) string {
	l.t.Helper()
	l.sim.SetTestLength(d)
	id := l.mustSubmitAs(m, api.JobSpec{Kernel: l.artifact("krel=" + krel + " behavior=ok"), Tests: []api.TestSpec{{Name: "long-work", Builtin: "x"}}})
	l.waitTesting(id, krel)
	return id
}

// leaseEvents is the Mac's Lease record, oldest first, each line as
// "event" or "event by @who: why".
func (l *lab) leaseEvents() []string {
	l.t.Helper()
	events, err := l.s.store.leaseRecord(l.s.dev(l.name).snapshot().ID, 100)
	if err != nil {
		l.t.Fatal(err)
	}
	out := make([]string, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		line := e.Event
		if e.By != "" {
			line += " by @" + e.By
		}
		if e.Why != "" {
			line += ": " + e.Why
		}
		out = append(out, line)
	}
	return out
}

func (l *lab) wantLeaseEvents(want ...string) {
	l.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := l.leaseEvents()
		ok := len(got) == len(want)
		for i := 0; ok && i < len(want); i++ {
			ok = strings.HasPrefix(got[i], want[i])
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("the Lease record:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// labLines waits for the Lab group to have got exactly these lines, each
// starting as given, and returns them.
func (e *endsLab) labLines(want ...string) []string {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got []string
		for _, line := range e.sig.lines() {
			if line.group == testLabGroup {
				got = append(got, line.text)
			}
		}
		ok := len(got) == len(want)
		for i := 0; ok && i < len(want); i++ {
			ok = strings.HasPrefix(got[i], want[i])
		}
		if ok {
			return got
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("the Lab group got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// heldExec starts a `lab exec` of m's that the Mac never answers by itself,
// and returns where its answer arrives.
func (l *lab) heldExec(m testMaintainer) <-chan string {
	l.t.Helper()
	started, hold := make(chan struct{}), make(chan struct{})
	l.t.Cleanup(func() { close(hold) })
	l.sim.OnExec = func(string) (string, bool) {
		close(started)
		<-hold
		return "", true
	}
	answer := make(chan string, 1)
	go func() {
		r, _, body := l.alExec(m.Token, "sleep 3600")
		answer <- r.Status + " " + body
	}()
	select {
	case <-started:
	case got := <-answer:
		l.t.Fatalf("%s's lab exec under their own Lease: %s", m.Name, got)
	case <-time.After(10 * time.Second):
		l.t.Fatalf("%s's lab exec never reached the Mac", m.Name)
	}
	return answer
}

func cutWithin(t *testing.T, answer <-chan string, d time.Duration, why string) {
	t.Helper()
	select {
	case got := <-answer:
		if strings.HasPrefix(got, "200") && !strings.Contains(got, "Lease") {
			t.Fatalf("%s, yet the lab exec was answered as if it ran to its end: %s", why, got)
		}
	case <-time.After(d):
		t.Fatalf("%s, yet the lab exec is still held", why)
	}
}

func (l *lab) wantCanceled(id, saying string) {
	l.t.Helper()
	j := l.jobAPI(id, 30*time.Second)
	if j.Outcome != api.OutcomeCanceled || !strings.Contains(j.Summary, saying) {
		l.t.Fatalf("job %s: %s: %s; want it canceled, saying %q", id, j.Outcome, j.Summary, saying)
	}
}

func (l *lab) noLease(why string) {
	l.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for l.s.leased(l.s.dev(l.name)) {
		if time.Now().After(deadline) {
			l.t.Fatalf("%s, yet the Mac is still leased: %+v", why, l.s.dev(l.name).snapshot().Lease)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Released by the holder: the running job finishes, none is left to run,
// and the holder's sessions are cut.
func TestAReleasedLeaseLetsItsJobFinishAndCutsTheRest(t *testing.T) {
	l := newEndsLab(t)
	taken := l.leaseAs(l.grace, 0)
	if left := time.Until(taken.Expires); left < 3*time.Hour+50*time.Minute || left > 4*time.Hour {
		t.Fatalf("grace's Lease lasts %s, want 4 hours", left)
	}
	l.labLines("grace leased sim-mac until ")
	running := l.longJobAs(l.grace, "7.2-run", 4*time.Second)
	queued := l.mustSubmitAs(l.grace, api.JobSpec{Kernel: l.artifact("krel=7.2-next behavior=ok")})
	exec := l.heldExec(l.grace)

	if r, body := l.as(l.grace.Token, "DELETE", "/api/devices/"+l.name+"/lease", nil); r.StatusCode != 200 {
		t.Fatalf("grace releasing: %s %s", r.Status, body)
	}
	l.noLease("grace released")
	cutWithin(t, exec, 5*time.Second, "the Lease was released")
	l.wantCanceled(queued, "Lease")
	done := l.jobAPI(running, 60*time.Second)
	l.expect(done, api.OutcomePass)
	if done.Result.BootKernel != "7.2-run" || !done.UnderLease || done.AccountID != testAccount("grace") || done.Maintainer != "grace" {
		t.Fatalf("the job that ran under the Lease: kernel %q, under_lease %v, account %d, handle %q", done.Result.BootKernel, done.UnderLease, done.AccountID, done.Maintainer)
	}
	l.wantLeaseEvents("taken", "released")
	l.labLines("grace leased sim-mac until ", "grace released sim-mac.")
}

// Runs out with a job running: the Lease is extended until that job ends
// and no further, the record says "extended", and the holder's queued jobs
// and exec are cut at the original end. An extension is not posted; the
// expiry after it is.
func TestALeaseThatRunsOutUnderItsJobIsExtendedUntilTheJobEnds(t *testing.T) {
	l := newEndsLab(t)
	l.leaseAs(l.grace, 0)
	running := l.longJobAs(l.grace, "7.2-run", 6*time.Second)
	queued := l.mustSubmitAs(l.grace, api.JobSpec{Kernel: l.artifact("krel=7.2-next behavior=ok")})
	exec := l.heldExec(l.grace)

	l.leaseRunsOutNow()
	cutWithin(t, exec, 5*time.Second, "the Lease ran out")
	l.wantCanceled(queued, "Lease")
	l.wantLeaseEvents("taken", "extended")
	// Past its end and extended, it is still grace's: nobody else gets the Mac.
	if j, _ := l.jobNow(running); j.State == api.JobDone {
		t.Fatalf("the running job ended with the Lease: %s %s", j.Outcome, j.Summary)
	}
	if d := l.s.view(l.s.dev(l.name)); d.Lease == nil || !d.Lease.Extended || d.Lease.AccountID != testAccount("grace") {
		t.Fatalf("the Lease while its job still runs: %+v", d.Lease)
	}
	if r, body := l.post("/api/devices/"+l.name+"/lease", api.LeaseRequest{Holder: "ada@laptop"}); r.StatusCode != http.StatusConflict || !strings.Contains(body, "leased by") {
		t.Fatalf("ada leasing a Mac whose Lease is extended: %s %s", r.Status, body)
	}
	if code, _, body := l.submitAs(l.grace.Token, api.JobSpec{Kernel: l.artifact("krel=7.2-more behavior=ok")}); code == 200 {
		t.Fatalf("grace queued another job on an extension, which goes no further than the running job: %s", body)
	}

	l.expect(l.jobAPI(running, 60*time.Second), api.OutcomePass)
	l.noLease("the job the Lease was extended for ended")
	l.wantLeaseEvents("taken", "extended", "expired")
	l.labLines("grace leased sim-mac until ", "grace's Lease on sim-mac expired.")
}

// leaseRunsOutNow moves the Lease's end to a moment ago, as the clock
// would.
func (l *lab) leaseRunsOutNow() {
	l.t.Helper()
	v := l.s.dev(l.name)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.d.Lease == nil {
		l.t.Fatal("the Mac has no Lease to run out")
	}
	ran := *v.d.Lease
	ran.Expires = l.s.now().Add(-time.Second)
	v.d.Lease = &ran
}

// Runs out with nothing running: the holder's queued jobs are canceled and
// their exec cut.
func TestALeaseThatRunsOutWithNothingRunningCutsItsHolder(t *testing.T) {
	l := newEndsLab(t)
	l.leaseAs(l.grace, 0)
	// The Mac goes to sleep, so grace's job waits for it as queued.
	exec := l.heldExec(l.grace)
	l.sim.Sleep()
	l.waitDevice(func(api.Device) bool { return !l.s.view(l.s.dev(l.name)).Alive })
	queued := l.mustSubmitAs(l.grace, api.JobSpec{Kernel: l.artifact("krel=7.2-next behavior=ok")})
	l.waitJob(queued, func(j *api.Job) bool { return hasEvent(j, "waiting for sim-mac to be ready") })

	l.leaseRunsOutNow()
	l.noLease("the Lease ran out")
	cutWithin(t, exec, 5*time.Second, "the Lease ran out")
	l.wantCanceled(queued, "Lease")
	l.wantLeaseEvents("taken", "expired")
	l.labLines("grace leased sim-mac until ", "grace's Lease on sim-mac expired.")
}

// Ended by a Maintainer, or because the holder left the M team, was
// Blocked, or "End all access" was used: the running job is canceled as
// `lab cancel` does, the Mac returns to its Known-good kernel, and the
// holder's queued jobs and exec are cut. The Lab group is told who and why.
func TestALeaseEndedOverItsHolderCancelsTheJobAndRestoresTheMac(t *testing.T) {
	l := newEndsLab(t)
	ada, err := l.s.person(testAccount(harnessMaintainer))
	if err != nil {
		t.Fatal(err)
	}
	grace := func() Person {
		p, err := l.s.person(testAccount("grace"))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct {
		name   string
		end    func()
		record string
		line   string
	}{
		{"a Maintainer", func() {
			if r, body := l.get("DELETE", "/api/devices/"+l.name+"/lease?force=1&why=the+M1+is+needed+for+a+release"); r.StatusCode != 200 {
				t.Fatalf("ada ending grace's Lease: %s %s", r.Status, body)
			}
		}, "ended by @ada: the M1 is needed for a release", "ada ended grace's Lease on sim-mac: the M1 is needed for a release."},
		{"leaving the M team", func() { l.s.endLeasesOf(grace(), leftTheTeam(ada)) },
			"ended by @ada: left the M team", "grace's Lease on sim-mac ended: grace left the M team."},
		{"Blocked", func() { l.s.endLeasesOf(grace(), wasBlocked(ada)) },
			"ended by @ada: Blocked", "grace's Lease on sim-mac ended: grace was Blocked."},
		{"End all access", func() { l.s.endLeasesOf(grace(), allAccessEnded(ada)) },
			`ended by @ada: "End all access"`, `ada ended grace's Lease on sim-mac: "End all access".`},
	} {
		t.Run(c.name, func(t *testing.T) {
			l.t = t
			before, lines := l.leaseEvents(), len(l.labLines2())
			l.leaseAs(l.grace, 0)
			running := l.longJobAs(l.grace, "7.2-run", time.Minute)
			labBoot, _, _ := l.sim.Identity()
			queued := l.mustSubmitAs(l.grace, api.JobSpec{Kernel: l.artifact("krel=7.2-next behavior=ok")})
			exec := l.heldExec(l.grace)

			c.end()
			l.noLease("the Lease was ended")
			cutWithin(t, exec, 5*time.Second, "the Lease was ended")
			l.wantCanceled(queued, "Lease")
			l.wantCanceled(running, "Lease")
			l.backOnItsOwnSystem(labBoot, 20*time.Second, "the Lease was ended over its holder")
			l.wantLeaseEvents(append(before, "taken", c.record)...)
			got := l.labLines2()
			if len(got) != lines+2 || got[len(got)-1] != c.line {
				t.Fatalf("the Lab group got %q, want its last line to be %q", got[lines:], c.line)
			}
			l.waitDevice(func(d api.Device) bool { return d.State == api.StateReady })
		})
	}
}

// labLines2 is the Lab group's lines so far, once no line is waiting.
func (e *endsLab) labLines2() []string {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		e.s.store.db.QueryRow(`SELECT count(*) FROM lines WHERE state IN ('waiting', 'sending')`).Scan(&waiting)
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			e.t.Fatal("the Lab group's lines never went out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var got []string
	for _, line := range e.sig.lines() {
		if line.group == testLabGroup {
			got = append(got, line.text)
		}
	}
	return got
}

// A job that ran under a Lease is never queued again by labd itself: not
// when the window's end cuts it short, which queues any other job again.
func TestAJobThatRanUnderALeaseIsNeverRetriedByLabd(t *testing.T) {
	l, clock := windowEndLab(t, labConfig{})
	l.takeLease("ada@laptop", time.Hour)
	id, _ := l.longJob("7.2-leased")
	end := l.endOpenNowSoon(clock)
	advanceTo(clock, end.Add(time.Second))
	j := l.jobAPI(id, 60*time.Second)
	l.expect(j, api.OutcomeIncomplete)
	if !j.UnderLease {
		t.Fatalf("the job does not say it ran under a Lease: %+v", j)
	}
	time.Sleep(2 * time.Second)
	if hasEvent(l.jobAPI(id, time.Second), "retried in a later window") {
		t.Fatalf("labd queued a job again that ran under a Lease: %v", j.Events)
	}
	jobs, err := l.s.store.jobs(l.name, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range jobs {
		if other.RetryOf == id {
			t.Fatalf("job %s retries %s, which ran under a Lease", other.ID, id)
		}
	}
	// The window's end ended the Lease with it: an expiry, recorded with why.
	l.noLease("the window ended")
	l.wantLeaseEvents("taken", "expired: the Availability window ended")
}

// A renewal and an extension are in the record and not in the Lab group; a
// Volunteer's Mac is named by its Mac name only.
func TestTheLabGroupHearsOfALeaseTakenAndEndedNotRenewed(t *testing.T) {
	sig := &fakeSignal{}
	dir := signalDir(t)
	l, _ := windowEndLab(t, labConfig{config: func(c *Config) { c.Signal, c.SignalDir = sig, dir }})
	e := &endsLab{lab: l, sig: sig}
	l.takeLease("ada@laptop", time.Hour)
	l.takeLease("ada@laptop", 0)
	if r, body := l.get("DELETE", "/api/devices/"+l.name+"/lease"); r.StatusCode != 200 {
		t.Fatalf("release: %s %s", r.Status, body)
	}
	l.wantLeaseEvents("taken", "renewed", "released")
	d := l.s.dev(l.name).snapshot()
	for _, line := range e.labLines("ada leased sim-mac until ", "ada released sim-mac.") {
		for _, private := range []string{d.VolunteerName, d.Contact, d.VolunteeredBy} {
			if private != "" && strings.Contains(line, private) {
				t.Fatalf("a line about a Volunteer's Mac names more than the Mac: %q has %q", line, private)
			}
		}
	}
}

// A send that fails never holds a Lease up.
func TestAFailedSendNeverBlocksALease(t *testing.T) {
	sig := &fakeSignal{answer: func(int) error { return ErrNotReached }}
	dir := signalDir(t)
	l := newLabWith(t, labConfig{config: func(c *Config) { c.Signal, c.SignalDir = sig, dir }})
	l.passedBaseline()
	l.takeLease("ada@laptop", 0)
	if r, body := l.get("DELETE", "/api/devices/"+l.name+"/lease"); r.StatusCode != 200 {
		t.Fatalf("release: %s %s", r.Status, body)
	}
	l.wantLeaseEvents("taken", "released")
}
