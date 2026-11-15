package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/window"
)

// opsOf asks labd as one of the mini's own scripts does.
func opsOf(s *Server, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.OpsHandler().ServeHTTP(w, httptest.NewRequest(method, "http://labd"+path, strings.NewReader("")))
	return w
}

// pollHere polls once as a builder and returns when labd has answered: no
// poll is left waiting in labd to take a build later.
func pollHere(b *builderLab, token string, wait time.Duration) int {
	ctx, cancel := context.WithTimeout(b.ctx, wait)
	defer cancel()
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/builder/poll", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("User-Agent", "lab-builder/test")
	w := httptest.NewRecorder()
	b.s.Handler().ServeHTTP(w, r)
	return w.Code
}

// A Deploy and a Maintenance run restart labd, and a job that runs then is
// lost. While jobs are drained, the one that runs finishes, the one behind
// it waits, and it starts once the drain is lifted.
func TestADrainLetsTheRunningJobFinishAndStartsNoOther(t *testing.T) {
	h := newCommandHold()
	l := newLabWith(t, labConfig{wrap: h.wrap})
	l.baseline()
	h.armed.Store(true)
	first := l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-drain-a behavior=ok")})
	select {
	case <-h.held:
	case <-time.After(20 * time.Second):
		t.Fatal("the first job never reached the Mac")
	}
	second := l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-drain-b behavior=ok")})

	if w := opsOf(l.s, "POST", "/drain"); w.Code != 200 || w.Body.String() != "1 0\n" {
		t.Fatalf("with one job running: %d %q", w.Code, w.Body)
	}
	close(h.release)
	l.waitJob(first, func(j *api.Job) bool { return j.State == api.JobDone })
	deadline := time.Now().Add(10 * time.Second)
	for opsOf(l.s, "POST", "/drain").Body.String() != "0 0\n" {
		if time.Now().After(deadline) {
			t.Fatalf("the drain never ended: %q", opsOf(l.s, "POST", "/drain").Body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	l.staysQueued(second, 1500*time.Millisecond, "jobs are drained")

	if w := opsOf(l.s, "DELETE", "/drain"); w.Code != 200 || w.Body.String() != "lifted\n" {
		t.Fatalf("lifting: %d %q", w.Code, w.Body)
	}
	if j := l.waitJob(second, func(j *api.Job) bool { return j.State == api.JobDone }); j.Outcome != api.OutcomePass {
		t.Errorf("the job that waited ended %s: %s", j.Outcome, j.Summary)
	}
}

// jobSaid waits until job id has an event containing what, and fails the
// test when none comes.
func jobSaid(t *testing.T, s *Server, id, what string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		j, err := s.store.job(id)
		if err == nil {
			for _, e := range j.Events {
				if strings.Contains(e.Msg, what) {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never said %q: %+v", id, what, j.Events)
		}
	}
}

// heldWithin is whether the Mac holds job id at some moment within d.
func heldWithin(v *dev, id string, d time.Duration) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v.snapshot().ActiveJob == id {
			return true
		}
	}
	return false
}

// A Volunteer's job waiting for its Mac's Availability window is not on the
// Mac: a drain does not wait for it, the window opening during the drain
// does not send it, and it is sent once the drain is lifted.
func TestAJobWaitingForAWindowDoesNotHoldADrain(t *testing.T) {
	l, _ := lendLab(t)
	l.signedIn(maintainerID).did("/people/mark", "handle", "grace", "as", "invite")
	v, secret := l.signedIn(graceID).lent("shelf-mac")
	first := v.snapshot().FirstBaseline.Job
	l.checkin(secret, api.Checkin{AccessLevel: api.AccessQualification})
	jobSaid(t, l.s, first, "no Availability window")

	if w := opsOf(l.s, "POST", "/drain"); w.Code != 200 || w.Body.String() != "0 0\n" {
		t.Fatalf("with a job waiting for its window: %d %q", w.Code, w.Body)
	}
	l.checkin(secret, api.Checkin{AccessLevel: api.AccessQualification, Window: &window.Settings{OpenUntil: l.clock.Now().Add(6 * time.Hour)}})
	if heldWithin(v, first, 1500*time.Millisecond) {
		t.Fatal("a job was sent to the Mac while jobs are drained")
	}
	if w := opsOf(l.s, "POST", "/drain"); w.Body.String() != "0 0\n" {
		t.Fatalf("with the window open during the drain: %q", w.Body)
	}
	opsOf(l.s, "DELETE", "/drain")
	if !heldWithin(v, first, 10*time.Second) {
		t.Fatalf("the job was not sent once the drain was lifted: %s", v.snapshot().StateReason)
	}
}

// idleAfterStop waits until a stopped labd runs no job any more.
func idleAfterStop(t *testing.T, s *Server) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		s.mu.Lock()
		n := len(s.cancels)
		s.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("labd still runs %d jobs after it stopped", n)
		}
	}
}

// A Deploy drains, then restarts labd. A Volunteer's job waiting for its
// Mac's window was never sent, so the restart takes nothing from it: it
// is still queued in the store, the next labd queues it again, and it is
// sent once the window opens.
func TestAJobWaitingForAWindowStaysQueuedAcrossARestart(t *testing.T) {
	l, _ := lendLab(t)
	l.signedIn(maintainerID).did("/people/mark", "handle", "grace", "as", "invite")
	v, secret := l.signedIn(graceID).lent("shelf-mac")
	first := v.snapshot().FirstBaseline.Job
	l.checkin(secret, api.Checkin{AccessLevel: api.AccessQualification})
	jobSaid(t, l.s, first, "no Availability window")
	if w := opsOf(l.s, "POST", "/drain"); w.Body.String() != "0 0\n" {
		t.Fatalf("with a job waiting for its window: %q", w.Body)
	}

	stopLabd(t, l.s)
	idleAfterStop(t, l.s)
	j, err := l.s.store.job(first)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != api.JobQueued {
		t.Fatalf("after labd stopped the waiting job is %s: %s", j.State, j.Summary)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	again, err := New(ctx, l.s.cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	l.s, l.h = again, again.Handler()
	l.checkin(secret, api.Checkin{AccessLevel: api.AccessQualification, Window: &window.Settings{OpenUntil: l.clock.Now().Add(6 * time.Hour)}})
	if !heldWithin(again.dev("shelf-mac"), first, 10*time.Second) {
		j, _ := again.store.job(first)
		t.Fatalf("the job was not sent after the restart: %s %s %+v", j.State, j.Summary, j.Events)
	}
}

// A job that waited for its build when labd stopped was never sent to its
// Mac either: the next labd queues it again, and its build is asked for
// again, instead of ending it as one that was running.
func TestAJobWaitingForItsBuildIsQueuedAgainAtTheStart(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	d := api.Device{ID: "1", Name: "built-for", State: api.StateReady, KnownGood: "7.2"}
	st.saveDevice(&d, "h1")
	st.saveJob(&api.Job{ID: "j-building", DeviceID: "1", Spec: api.JobSpec{Device: d.Name, Source: "https://github.com/o/r/commit/" + strings.Repeat("a", 40)},
		State: api.JobBuilding, Created: time.Now()})
	st.db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := newWith(harnessMaintainer, "lab_mtok", ctx, Config{DataDir: dir}, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	jobSaid(t, s, "j-building", "resolving")
	if j, _ := s.store.job("j-building"); strings.Contains(j.Summary, "labd restarted") {
		t.Fatalf("the job that waited for its build: %s %s", j.State, j.Summary)
	}
}

// While a person's Hard reset waits for the controller the Mac takes no
// job, which the reset would cut short; the job is sent once it has gone.
func TestAJobWaitsWhileAHardResetIsOnItsWay(t *testing.T) {
	l := newLab(t, false)
	l.baseline()
	v := l.s.dev(l.name)
	v.mu.Lock()
	v.resetting = true
	v.mu.Unlock()
	id := l.submitJob(api.JobSpec{Kernel: l.artifact("krel=7.2-resetting behavior=ok")})
	jobSaid(t, l.s, id, "a Hard reset of "+l.name+" is on its way")
	l.staysQueued(id, 1500*time.Millisecond, "a Hard reset is on its way")
	v.mu.Lock()
	v.resetting = false
	v.bump()
	v.mu.Unlock()
	l.expect(l.jobAPI(id, 60*time.Second), api.OutcomePass)
}

// With no job running the Builder is handed no build either: the restart
// that follows a drain would end it. A build that runs is counted.
func TestADrainHandsTheBuilderNoBuild(t *testing.T) {
	b := newBuilderLab(t)
	token := b.addBuilder("mini", api.BuilderKernel)
	running := b.queue("")
	if a, code := b.poll(token, "", 5*time.Second); code != 200 || a.Build.ID != running.ID {
		t.Fatalf("the builder got no build: %d %+v", code, a)
	}
	waiting := b.queue("")

	if w := opsOf(b.s, "POST", "/drain"); w.Code != 200 || w.Body.String() != "0 1\n" {
		t.Fatalf("with one build running: %d %q", w.Code, w.Body)
	}
	bd := b.build(running.ID)
	bd.State = api.BuildDone
	b.s.saveBuild(bd)
	b.s.builds.state.Lock()
	delete(b.s.builds.claims, running.ID)
	b.s.builds.state.Unlock()
	if w := opsOf(b.s, "POST", "/drain"); w.Body.String() != "0 0\n" {
		t.Fatalf("with that build done: %q", w.Body)
	}
	if code := pollHere(b, token, time.Second); code == 200 {
		t.Fatal("a build was handed out while jobs are drained")
	}
	if got := b.build(waiting.ID).State; got != api.BuildQueued {
		t.Errorf("the waiting build is %s", got)
	}

	opsOf(b.s, "DELETE", "/drain")
	if a, code := b.poll(token, "", 5*time.Second); code != 200 || a.Build.ID != waiting.ID {
		t.Errorf("after the drain the builder got no build: %d %+v", code, a)
	}
}

// What Deploy asks after its switch: whether the Builder asks this labd for
// work and whether the Out-of-band controller answers it.
func TestPartsSaysHowTheBuilderAndTheControllerAre(t *testing.T) {
	b := newBuilderLab(t)
	if w := opsOf(b.s, "GET", "/parts"); w.Code != 200 || w.Body.String() != "builder none\ncontroller none\n" {
		t.Fatalf("with neither set up: %d %q", w.Code, w.Body)
	}
	token := b.addBuilder("mini", api.BuilderKernel)
	if w := opsOf(b.s, "GET", "/parts"); w.Body.String() != "builder silent\ncontroller none\n" {
		t.Fatalf("with a builder that never asked: %q", w.Body)
	}
	pollHere(b, token, 300*time.Millisecond)
	if w := opsOf(b.s, "GET", "/parts"); w.Body.String() != "builder ok\ncontroller none\n" {
		t.Fatalf("with a builder that asks: %q", w.Body)
	}

	c := newController(t)
	o := newOps(t, func(cfg *Config) { cfg.OOB = c.client(t) })
	if w := opsOf(o.s, "GET", "/parts"); !strings.Contains(w.Body.String(), "controller ok\n") {
		t.Fatalf("with a controller that answers: %q", w.Body)
	}
	c.mu.Lock()
	c.down = true
	c.mu.Unlock()
	if w := opsOf(o.s, "GET", "/parts"); !strings.Contains(w.Body.String(), "controller bad\n") {
		t.Fatalf("with a controller that does not: %q", w.Body)
	}
}

func TestTheDrainIsOnTheOpsSocketOnly(t *testing.T) {
	b := newBuilderLab(t)
	for _, c := range []struct{ method, path string }{{"POST", "/drain"}, {"DELETE", "/drain"}, {"GET", "/parts"}} {
		w := httptest.NewRecorder()
		b.s.Handler().ServeHTTP(w, httptest.NewRequest(c.method, "http://127.0.0.1"+c.path, nil))
		if w.Code == http.StatusOK {
			t.Errorf("%s %s answers on the lab's own routes: %d", c.method, c.path, w.Code)
		}
	}
	if w := opsOf(b.s, "GET", "/drain"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /drain: %d", w.Code)
	}
}
