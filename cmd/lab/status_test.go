package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
)

// lab status says when a Lease ends, and for a Mac whose last Lease is over,
// how it ended: who ended it and why.
func TestStatusSaysALeasesEndAndWhyItEnded(t *testing.T) {
	end := time.Date(2026, time.November, 15, 12, 0, 0, 0, time.UTC)
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/devices":
			json.NewEncoder(w).Encode([]client.Device{{Device: api.Device{Name: "m1-pro", Lease: &api.Lease{Holder: "ravi@desk", Maintainer: "ravi", Expires: end}}}})
		case "/api/devices/m1-pro":
			json.NewEncoder(w).Encode(client.Device{Device: api.Device{Name: "m1-pro", Lease: &api.Lease{Holder: "ravi@desk", Maintainer: "ravi", Expires: end, Extended: true}}})
		case "/api/devices/m2-air":
			json.NewEncoder(w).Encode(client.Device{Device: api.Device{Name: "m2-air"}})
		case "/api/devices/m2-air/leases":
			json.NewEncoder(w).Encode([]api.LeaseEvent{{Time: end, Mac: "m2-air", Event: api.LeaseEnded, Handle: "grace", Until: end.Add(time.Hour), By: "ada", Why: "needed for the release"}})
		case "/api/devices/m3/leases":
			json.NewEncoder(w).Encode([]api.LeaseEvent{{Time: end, Mac: "m3", Event: api.LeaseTaken, Handle: "grace", Until: end.Add(time.Hour)}})
		case "/api/devices/m3":
			json.NewEncoder(w).Encode(client.Device{Device: api.Device{Name: "m3"}})
		default:
			http.Error(w, "unexpected "+r.URL.Path, 400)
		}
	})
	at := end.Local().Format("15:04:05 on Mon 2 Jan")
	out, err := printed(t, c, "status")
	if err != nil || !strings.Contains(out, "ravi@desk (ravi) until "+at) {
		t.Errorf("lab status: %v\n%s", err, out)
	}
	out, err = printed(t, c, "status", "m1-pro")
	if err != nil || !strings.Contains(out, "lease:       ravi@desk (ravi) until "+at+", extended until its job ends") {
		t.Errorf("lab status m1-pro: %v\n%s", err, out)
	}
	out, err = printed(t, c, "status", "m2-air")
	if err != nil || !strings.Contains(out, "last lease:  "+at+", @grace's Lease on m2-air was ended by @ada: needed for the release") {
		t.Errorf("lab status m2-air: %v\n%s", err, out)
	}
	out, err = printed(t, c, "status", "m3")
	if err != nil || strings.Contains(out, "lease:") {
		t.Errorf("lab status m3, whose record's newest line is no ending: %v\n%s", err, out)
	}
}

// lab release --force carries the Maintainer's reason to the lab, and a
// reason without --force is refused before the lab is asked.
func TestForceReleaseCarriesItsReason(t *testing.T) {
	var asked []string
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Encode())
		io.WriteString(w, "ok")
	})
	ctx := context.Background()
	if err := dispatch(ctx, c, "release", []string{"m1-pro", "--force", "--why", "needed for the release"}); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(ctx, c, "release", []string{"m1-pro", "--force"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"DELETE /api/devices/m1-pro/lease?force=1&why=needed+for+the+release", "DELETE /api/devices/m1-pro/lease?force=1"}
	if strings.Join(asked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the lab was asked\n%s", strings.Join(asked, "\n"))
	}
	if err := dispatch(ctx, c, "release", []string{"m1-pro", "--why", "tidy"}); err == nil || !strings.Contains(err.Error(), "--why goes with --force") || len(asked) != 2 {
		t.Fatalf("--why without --force: %v, asked %v", err, asked)
	}
}

// lab builds says who asked for each build.
func TestBuildsSayWhoAsked(t *testing.T) {
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]*api.Build{{ID: "b1115-080000-abcd", State: api.BuildDone, AskedBy: "ada"}})
	})
	out, err := printed(t, c, "builds")
	if err != nil || !strings.Contains(out, "ASKED BY") || !strings.Contains(out, "ada") {
		t.Errorf("lab builds: %v\n%s", err, out)
	}
}
