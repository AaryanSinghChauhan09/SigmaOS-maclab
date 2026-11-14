package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// opsOf asks labd as one of the mini's own scripts does.
func opsOf(s *Server, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.OpsHandler().ServeHTTP(w, httptest.NewRequest(method, "http://labd"+path, strings.NewReader("")))
	return w
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
	if a, code := b.poll(token, "", 1500*time.Millisecond); code == 200 {
		t.Fatalf("a build was handed out while jobs are drained: %+v", a)
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
	b.poll(token, "", 300*time.Millisecond)
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
