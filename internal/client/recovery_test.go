package client

import (
	"context"
	"encoding/json"
	"github.com/omacom/omarchy-maclab/internal/api"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveryCapabilityGatePrecedesEveryExplicitPost(t *testing.T) {
	for _, mode := range []string{api.RecoveryNetworkOnly} {
		for _, kind := range []string{"old-server", "old-schema", "no-isolation", "missing-mode", "valid", "mismatched-return"} {
			t.Run(mode+kind, func(t *testing.T) {
				posts := 0
				gets := 0
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "GET" && r.URL.Path == "/api/recovery" {
						gets++
						if kind == "old-server" {
							http.NotFound(w, r)
							return
						}
						caps := api.RecoveryCapabilities{Schema: 1, Modes: []string{mode}, PerJobOOBIsolation: true}
						if kind == "old-schema" {
							caps.Schema = 0
						}
						if kind == "no-isolation" {
							caps.PerJobOOBIsolation = false
						}
						if kind == "missing-mode" {
							caps.Modes = nil
						}
						json.NewEncoder(w).Encode(caps)
						return
					}
					if r.Method == "POST" && r.URL.Path == "/api/jobs" {
						posts++
						var spec api.JobSpec
						json.NewDecoder(r.Body).Decode(&spec)
						if spec.RecoveryMode != mode {
							t.Fatal("requested mode changed")
						}
						if kind == "mismatched-return" {
							spec.RecoveryMode = ""
						}
						json.NewEncoder(w).Encode(api.Job{ID: "exact-job", Spec: spec})
						return
					}
					t.Fatal(r.URL.Path)
				}))
				defer s.Close()
				c := &Client{URL: s.URL, Token: "CPU_ONLY", http: s.Client()}
				j, e := c.Submit(context.Background(), api.JobSpec{Device: "m1", RecoveryMode: mode})
				if kind == "valid" {
					if e != nil || j.Spec.RecoveryMode != mode || posts != 1 {
						t.Fatal(j, e, posts)
					}
				} else {
					if e == nil {
						t.Fatal("unsupported/mismatched mode admitted")
					}
					if kind == "mismatched-return" {
						if posts != 1 || !strings.Contains(e.Error(), "exact-job") {
							t.Fatal("ambiguous real job identity lost", e)
						}
					} else if posts != 0 {
						t.Fatal("old server queued unsafe job", posts)
					}
				}
				if gets != 1 {
					t.Fatal("capability request missing", gets)
				}
			})
		}
	}
}

// The serial-assisted mode went with the serial console: lab says so and
// asks labd nothing.
func TestASerialAssistedJobIsRefusedBeforeLabdIsAsked(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("labd was asked %s %s", r.Method, r.URL.Path)
	}))
	defer s.Close()
	c := &Client{URL: s.URL, Token: "CPU_ONLY", http: s.Client()}
	_, err := c.Submit(context.Background(), api.JobSpec{Device: "m1", RecoveryMode: api.RecoverySerialAssisted})
	if err == nil || err.Error() != "The serial console is not part of Omarchy Mac Lab." {
		t.Fatalf("a serial-assisted job: %v", err)
	}
}

// A Proving reset names the port and waits for the lab's answer, which is
// held for as long as the Mac takes to return.
func TestAProvingResetNamesThePortAndWaitsForTheLabsAnswer(t *testing.T) {
	var asked []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Query().Get("prove") {
		case "middle":
			w.Header().Set("Content-Type", heldType)
			w.Write([]byte("\n\n"))
			result, _ := json.Marshal(api.Proving{Device: "m2-max", Port: "middle", Cable: api.Cable{State: api.CableSighted, Port: "middle", Proved: true}})
			json.NewEncoder(w).Encode(api.HeldLine{Result: result})
		case "left":
			w.Header().Set("Content-Type", heldType)
			w.Write([]byte("\n"))
			json.NewEncoder(w).Encode(api.HeldLine{Status: 409, Error: "the Proving reset of the left port did not count: m2-max did not return on a new boot within 4m0s. m2-max is not placed"})
		default:
			http.Error(w, "a Proving reset is a Maintainer's: it is aimed at a port, not at a Mac", http.StatusForbidden)
		}
	}))
	defer s.Close()
	c := &Client{URL: s.URL, Token: "lab_tok", http: s.Client()}
	p, err := c.ProvingReset(context.Background(), "m2-max", "middle")
	if err != nil || p.Device != "m2-max" || p.Port != "middle" || !p.Cable.Proved || p.Cable.String() != "sighted on the middle port by a Proving reset" {
		t.Fatalf("a Proving reset that counted: %+v %v", p, err)
	}
	if _, err := c.ProvingReset(context.Background(), "m2-max", "left"); err == nil || !strings.Contains(err.Error(), "did not count: m2-max did not return") {
		t.Fatalf("a Proving reset that did not count: %v", err)
	}
	if _, err := c.ProvingReset(context.Background(), "m2-max", "right"); err == nil || !strings.Contains(err.Error(), "a Proving reset is a Maintainer's") {
		t.Fatalf("a Proving reset the lab refused: %v", err)
	}
	want := []string{"POST /api/devices/m2-max/reset?prove=middle", "POST /api/devices/m2-max/reset?prove=left", "POST /api/devices/m2-max/reset?prove=right"}
	if strings.Join(asked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("asked %q, want %q", asked, want)
	}
	// A port the lab server has not is refused before the lab is asked.
	for _, port := range []string{"front", "0", "RIGHT", ""} {
		if _, err := c.ProvingReset(context.Background(), "m2-max", port); err == nil || !strings.Contains(err.Error(), "left, middle and right") {
			t.Fatalf("port %q: %v", port, err)
		}
	}
	if len(asked) != len(want) {
		t.Fatalf("the lab was asked for a port it has not: %q", asked[len(want):])
	}
}

// A Hard reset says what the lab said, a refusal for a Mac the lab cannot
// place included; the cable is read as the lab shows it.
func TestAHardResetSaysWhatTheLabSaidAndTheCableIsRead(t *testing.T) {
	const cannotPlace = "no Hard reset was sent: the lab has no Sighting of m2-max. A Maintainer can prove its port: `lab reset m2-max --prove left`"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.RequestURI() {
		case "POST /api/devices/m1-pro/reset?by=ada%40mini":
			w.Write([]byte("Hard reset sent to m1-pro through the right port; watch `lab status m1-pro`\n"))
		case "POST /api/devices/m2-max/reset?by=ada%40mini":
			http.Error(w, cannotPlace, http.StatusConflict)
		case "GET /api/devices/m2-max/oob":
			json.NewEncoder(w).Encode(api.OOBStatus{Device: "m2-max", Controller: api.ControllerHealthy,
				Cable: &api.Cable{State: api.CableNotSighted, NoHardReset: "the lab has no Sighting of m2-max"},
				Ports: []api.OOBPort{{Port: "left", Connected: true, ProductID: 29453, PlugCount: 28}, {Port: "middle", PlugCount: 42}, {Port: "right", Connected: true, Name: "omarchy-m1-pro", ProductID: 29448, PlugCount: 41, Mac: "m1-pro"}}})
		default:
			t.Errorf("asked %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := &Client{URL: s.URL, Token: "lab_tok", http: s.Client()}
	if out, err := c.Reset(context.Background(), "m1-pro", "ada@mini"); err != nil || !strings.Contains(out, "through the right port") {
		t.Fatalf("a Hard reset: %q %v", out, err)
	}
	if _, err := c.Reset(context.Background(), "m2-max", "ada@mini"); err == nil || err.Error() != cannotPlace {
		t.Fatalf("a Hard reset of a Mac the lab cannot place: %v", err)
	}
	st, err := c.Cable(context.Background(), "m2-max")
	if err != nil || st.Cable == nil || st.Cable.State != api.CableNotSighted || st.Cable.String() != "not sighted" || len(st.Ports) != 3 || st.Ports[2].Mac != "m1-pro" {
		t.Fatalf("the cable: %+v %v", st, err)
	}
}
