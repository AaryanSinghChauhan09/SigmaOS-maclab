package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/oob"
)

// A Mac lab controller on another Mac is added by a Maintainer beside the
// mini's own. Each Mac is sighted on the controller it is cabled to, and its
// Hard reset goes there and nowhere else; the mini's Mac is as before.
func TestHardResetsGoToTheControllerThatSeesTheMac(t *testing.T) {
	l := sightLab(t, labConfig{})
	studio := newFakeController(t, true, []int{0, 1})
	add := api.ControllerAdd{Name: "studio", URL: studio.url, Fingerprint: studio.fingerprint, Token: studio.token}

	bad := add
	bad.Fingerprint = strings.Repeat("ab", 32)
	if r, body := l.as(harnessToken, "POST", "/api/controllers", bad); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a controller with the wrong certificate pinned: %d %s", r.StatusCode, body)
	}
	bad = add
	bad.Name = "mini"
	if r, _ := l.as(harnessToken, "POST", "/api/controllers", bad); r.StatusCode != http.StatusConflict {
		t.Fatalf("a second controller named mini: %d", r.StatusCode)
	}
	if r, body := l.as(harnessToken, "POST", "/api/controllers", add); r.StatusCode != http.StatusOK {
		t.Fatalf("adding studio: %d %s", r.StatusCode, body)
	}
	_, body := l.as(harnessToken, "GET", "/api/controllers", nil)
	for _, want := range []string{`"name":"mini"`, `"name":"studio"`, `"studio:0"`, `"studio:1"`, `"left"`, studio.fingerprint} {
		if !strings.Contains(body, want) {
			t.Fatalf("the controllers lack %s: %s", want, body)
		}
	}
	if strings.Contains(body, studio.token) {
		t.Fatal("the list shows a controller's token")
	}

	l.addMac("m2-max")
	studio.plug(1, nil, "m2-max", productM2Max)
	l.sight()
	if c := l.cableOf("m2-max"); c["state"] != api.CableSighted || c["port"] != "studio:1" {
		t.Fatalf("m2-max on the studio's port 1: %v", c)
	}
	if c := l.cableOf(l.name); c["state"] != api.CableSighted || c["port"] != "right" {
		t.Fatalf("the mini's Mac: %v", c)
	}

	if r, body := l.resetAs(harnessToken, "m2-max", ""); r.StatusCode != http.StatusOK {
		t.Fatalf("Hard reset of m2-max: %d %s", r.StatusCode, body)
	}
	if _, sent := studio.resetsSent(); len(sent) != 1 || sent[0] != 1 {
		t.Fatalf("the studio sent resets to %v, want its port 1", sent)
	}
	l.wantResets("m2-max is on the studio")
	if r, body := l.resetAs(harnessToken, l.name, ""); r.StatusCode != http.StatusOK {
		t.Fatalf("Hard reset of %s: %d %s", l.name, r.StatusCode, body)
	}
	l.wantResets("the mini's Mac", portRight)

	// A controller that stops answering keeps its Macs' Sightings and does
	// not stop the mini's.
	studio.stop(true)
	l.sight()
	l.wantCable(l.name, api.CableSighted)
	if c := l.cableOf("m2-max"); c["state"] != api.CableSighted {
		t.Fatalf("m2-max with its controller down: %v", c)
	}
	studio.stop(false)

	if r, _ := l.as(harnessToken, "DELETE", "/api/controllers/mini", nil); r.StatusCode != http.StatusConflict {
		t.Fatalf("removing the mini's own: %d", r.StatusCode)
	}
	if r, body := l.as(harnessToken, "DELETE", "/api/controllers/studio", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("removing studio: %d %s", r.StatusCode, body)
	}
	if c := l.cableOf("m2-max"); c["state"] == api.CableSighted {
		t.Fatalf("m2-max still sighted on a removed controller: %v", c)
	}
	l.wantCable(l.name, api.CableSighted)
}

// Controllers are labd's data: added ones are back after a restart, and
// only a Maintainer adds or removes one.
func TestControllersAreAMaintainersAndOutliveARestart(t *testing.T) {
	l := sightLab(t, labConfig{})
	studio := newFakeController(t, true, []int{0})
	add := api.ControllerAdd{Name: "studio", URL: studio.url, Fingerprint: studio.fingerprint, Token: studio.token}
	if r, body := l.as(harnessToken, "POST", "/api/controllers", add); r.StatusCode != http.StatusOK {
		t.Fatalf("adding studio: %d %s", r.StatusCode, body)
	}
	if r, _ := l.as("", "POST", "/api/controllers", add); r.StatusCode < 400 {
		t.Fatalf("adding one with no sign-in: %d", r.StatusCode)
	}
	s2 := &Server{cfg: l.s.cfg, oob: oob.NewFleet(l.s.cfg.OOB), log: quietLog()}
	if err := s2.loadControllers(); err != nil {
		t.Fatal(err)
	}
	if names := s2.oob.Names(); strings.Join(names, " ") != "left middle right studio:0" {
		t.Fatalf("after a restart the ports are %v", names)
	}
}
