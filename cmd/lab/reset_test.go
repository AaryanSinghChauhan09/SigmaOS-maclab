package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
)

// printed runs one lab command and returns what it wrote to standard output.
func printed(t *testing.T, c *client.Client, cmd string, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	out := make(chan string)
	go func() { b, _ := io.ReadAll(r); out <- string(b) }()
	err = dispatch(context.Background(), c, cmd, args)
	os.Stdout = old
	w.Close()
	return <-out, err
}

const cannotPlace = "no Hard reset was sent: the lab has no Sighting of m2-max. A Maintainer can prove its port: `lab reset m2-max --prove left`"

// lab reset is a Hard reset, refused for a Mac the lab cannot place; with
// --prove it is a Proving reset of the port named.
func TestResetIsRefusedForAMacTheLabCannotPlaceAndProvesAPort(t *testing.T) {
	var asked []string
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/api/devices/m1-pro/reset" && r.URL.Query().Get("prove") == "":
			io.WriteString(w, "Hard reset sent to m1-pro through the right port; watch `lab status m1-pro`\n")
		case r.URL.Path == "/api/devices/m2-max/reset" && r.URL.Query().Get("prove") == "middle":
			json.NewEncoder(w).Encode(api.Proving{Device: "m2-max", Port: "middle", Cable: api.Cable{State: api.CableSighted, Port: "middle", Proved: true}})
		case r.URL.Path == "/api/devices/m2-max/reset" && r.URL.Query().Get("prove") == "left":
			http.Error(w, "the Proving reset of the left port did not count: m3 restarted too, and a Proving reset counts only if every other Mac keeps its boot id. m2-max is not placed", http.StatusConflict)
		case r.URL.Path == "/api/devices/m2-max/reset":
			http.Error(w, cannotPlace, http.StatusConflict)
		case r.URL.Path == "/api/devices/vol-air/reset":
			http.Error(w, "no Hard reset was sent: vol-air is a Volunteer's Mac, and the lab never sends a Volunteer's Mac a Hard reset: it recovers by network means and its own watchdog, then its Volunteer is asked", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	})
	t.Setenv("MACLAB_HOLDER", "ada@mini")

	out, err := printed(t, c, "reset", "m1-pro")
	if err != nil || !strings.Contains(out, "Hard reset sent to m1-pro through the right port") {
		t.Fatalf("lab reset m1-pro: %q %v", out, err)
	}
	if out, err := printed(t, c, "reset", "m2-max"); err == nil || err.Error() != cannotPlace || out != "" {
		t.Fatalf("lab reset of a Mac the lab cannot place: %q %v", out, err)
	}
	if _, err := printed(t, c, "reset", "vol-air"); err == nil || !strings.Contains(err.Error(), "never sends a Volunteer's Mac a Hard reset") {
		t.Fatalf("lab reset of a Volunteer's Mac: %v", err)
	}
	out, err = printed(t, c, "reset", "m2-max", "--prove", "middle")
	if err != nil || out != "m2-max is placed on the middle port: it returned on a new boot and every other Mac kept its boot id\n" {
		t.Fatalf("lab reset --prove middle: %q %v", out, err)
	}
	if out, err := printed(t, c, "reset", "--prove", "left", "m2-max"); err == nil || !strings.Contains(err.Error(), "did not count: m3 restarted too") || out != "" {
		t.Fatalf("a Proving reset that did not count: %q %v", out, err)
	}
	want := []string{
		"POST /api/devices/m1-pro/reset?by=ada%40mini",
		"POST /api/devices/m2-max/reset?by=ada%40mini",
		"POST /api/devices/vol-air/reset?by=ada%40mini",
		"POST /api/devices/m2-max/reset?prove=middle",
		"POST /api/devices/m2-max/reset?prove=left",
	}
	if strings.Join(asked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("asked\n%s\nwant\n%s", strings.Join(asked, "\n"), strings.Join(want, "\n"))
	}
	if _, err := printed(t, c, "reset", "m2-max", "--prove", "front"); err == nil || !strings.Contains(err.Error(), "left, middle and right") || len(asked) != len(want) {
		t.Fatalf("a port the lab server has not: %v, asked %q", err, asked[len(want):])
	}
	for _, piece := range []string{"lab reset <device> --prove left|middle|right", "Refused for a Mac the lab cannot place", "a Proving reset (Maintainers)", "sighted, not sighted or no cable"} {
		if !strings.Contains(usage, piece) {
			t.Errorf("lab help lacks %q", piece)
		}
	}
}

// lab status and lab oob say sighted, not sighted or no cable for each
// Lab-owned Mac, and why no Hard reset reaches one.
func TestStatusAndOOBSaySightedNotSightedOrNoCable(t *testing.T) {
	since := time.Date(2026, time.November, 15, 3, 4, 5, 0, time.UTC)
	notSighted := "the lab has no Sighting of m2-max. A Maintainer can prove its port: `lab reset m2-max --prove left`"
	fleet := []client.Device{
		{Device: api.Device{Name: "m1-pro", State: api.StateReady, Enrollment: api.Enrollment{Kind: api.KindLabOwned}}, Tier: 2, Cable: &api.Cable{State: api.CableSighted, Port: "right", Since: since}},
		{Device: api.Device{Name: "m2-max", State: api.StateReady, Enrollment: api.Enrollment{Kind: api.KindLabOwned}}, Tier: 1, Cable: &api.Cable{State: api.CableNotSighted, NoHardReset: notSighted}},
		{Device: api.Device{Name: "m3", State: api.StateReady, Enrollment: api.Enrollment{Kind: api.KindLabOwned}}, Tier: 1, Cable: &api.Cable{State: api.CableNone, NoHardReset: "no cable can be the one of m3"}},
		{Device: api.Device{Name: "m4", State: api.StateReady, Enrollment: api.Enrollment{Kind: api.KindLabOwned}}, Tier: 1, Cable: &api.Cable{State: api.CableSighted, Port: "left", Proved: true, NoHardReset: "the Out-of-band controller is not healthy: stopped"}},
		{Device: api.Device{Name: "vol-air", State: api.StateReady, Volunteer: true, Enrollment: api.Enrollment{Kind: api.KindVolunteer, VolunteerName: "linus"}}, Tier: 1},
	}
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/devices":
			json.NewEncoder(w).Encode(fleet)
		case "/api/devices/m2-max/oob":
			json.NewEncoder(w).Encode(api.OOBStatus{Device: "m2-max", Controller: api.ControllerHealthy, Cable: fleet[1].Cable,
				Ports: []api.OOBPort{{Port: "left", Connected: true, ProductID: 29453, PlugCount: 28}, {Port: "middle", PlugCount: 42}, {Port: "right", Connected: true, Name: "omarchy-m1-pro", ProductID: 29448, PlugCount: 41, Mac: "m1-pro"}}})
		case "/api/devices/m1-pro/oob":
			json.NewEncoder(w).Encode(api.OOBStatus{Device: "m1-pro", Controller: api.ControllerHealthy, Cable: fleet[0].Cable})
		case "/api/devices/vol-air/oob":
			json.NewEncoder(w).Encode(api.OOBStatus{Device: "vol-air"})
		default:
			for _, d := range fleet {
				if r.URL.Path == "/api/devices/"+d.Name {
					json.NewEncoder(w).Encode(d)
					return
				}
			}
			http.NotFound(w, r)
		}
	})

	out, err := printed(t, c, "status")
	if err != nil {
		t.Fatal(err)
	}
	cable := map[string]string{}
	var header []string
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		cols := bytes.Split([]byte(line), []byte("  "))
		var fields []string
		for _, col := range cols {
			if f := strings.TrimSpace(string(col)); f != "" {
				fields = append(fields, f)
			}
		}
		if i == 0 {
			header = fields
			continue
		}
		cable[fields[0]] = fields[3]
	}
	if len(header) < 4 || header[3] != "CABLE" {
		t.Fatalf("lab status has no CABLE column: %v\n%s", header, out)
	}
	for mac, want := range map[string]string{"m1-pro": "sighted (right)", "m2-max": "not sighted", "m3": "no cable", "m4": "sighted (left), no Hard reset", "vol-air": "-"} {
		if cable[mac] != want {
			t.Errorf("lab status says %q for %s, want %q\n%s", cable[mac], mac, want, out)
		}
	}

	for mac, want := range map[string][]string{
		"m1-pro":  {"  cable:       sighted on the right port\n"},
		"m2-max":  {"  cable:       not sighted\n", "  hard reset:  none: " + notSighted + "\n"},
		"m3":      {"  cable:       no cable\n", "  hard reset:  none: no cable can be the one of m3\n"},
		"m4":      {"  cable:       sighted on the left port by a Proving reset\n", "  hard reset:  none: the Out-of-band controller is not healthy: stopped\n"},
		"vol-air": {"  kind:        Volunteer's Mac"},
	} {
		out, err := printed(t, c, "status", mac)
		if err != nil {
			t.Fatal(err)
		}
		for _, piece := range want {
			if !strings.Contains(out, piece) {
				t.Errorf("lab status %s lacks %q:\n%s", mac, piece, out)
			}
		}
		if mac == "m1-pro" && strings.Contains(out, "hard reset:  none") || mac == "vol-air" && strings.Contains(out, "cable:") {
			t.Errorf("lab status %s:\n%s", mac, out)
		}
	}

	out, err = printed(t, c, "oob", "m2-max")
	if err != nil {
		t.Fatal(err)
	}
	for _, piece := range []string{
		"m2-max: not sighted\n",
		"  hard reset:  none: " + notSighted + "\n",
		"  controller:  healthy\n",
		"  left:        connected, product id 29453, plug count 28, shows no name: no Sighting\n",
		"  middle:      nothing connected\n",
		"  right:       connected, product id 29448, plug count 41, shows the name omarchy-m1-pro: m1-pro\n",
	} {
		if !strings.Contains(out, piece) {
			t.Errorf("lab oob m2-max lacks %q:\n%s", piece, out)
		}
	}
	if out, _ = printed(t, c, "oob", "m1-pro"); !strings.Contains(out, "m1-pro: sighted on the right port\n") || !strings.Contains(out, "  hard reset:  through the right port (`lab reset m1-pro`)\n") {
		t.Errorf("lab oob m1-pro:\n%s", out)
	}
	if out, _ = printed(t, c, "oob", "vol-air"); out != "vol-air: a Volunteer's Mac: no cable joins it to the lab server, and it never gets a Hard reset\n" {
		t.Errorf("lab oob of a Volunteer's Mac: %q", out)
	}
}
