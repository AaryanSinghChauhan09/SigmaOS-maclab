package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
)

// lab lease asks for no length unless --ttl gives one, so the Lease is the
// lab's 4 hours; a refusal is said as the lab said it; a Maintainer's
// warning is printed and the Lease stands.
func TestLeaseAsksForTheLabsFourHoursAndSaysWhatTheLabSaid(t *testing.T) {
	end := time.Date(2026, time.November, 14, 16, 0, 0, 0, time.UTC)
	var asked []api.LeaseRequest
	labd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, "/api/devices/")
		if r.Method != "POST" || !ok || !strings.HasSuffix(name, "/lease") {
			http.Error(w, "unexpected request", 400)
			return
		}
		var req api.LeaseRequest
		json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		switch strings.TrimSuffix(name, "/lease") {
		case "pro-b":
			http.Error(w, "@grace already holds a Lease on pro-a, the same Configuration (j316s, 10 CPU and 16 GPU cores): one Lease per Configuration for an M team member. Release pro-a first", http.StatusConflict)
		case "held":
			http.Error(w, "held is leased by linus@desk (linus) until 15:30:00", http.StatusConflict)
		case "pro-d":
			json.NewEncoder(w).Encode(api.Lease{Holder: req.Holder, Maintainer: "ravi", Expires: end, Warning: "you already hold a Lease on pro-c, the same Configuration (j316s, 10 CPU and 16 GPU cores); an M team member would be refused a second"})
		default:
			json.NewEncoder(w).Encode(api.Lease{Holder: req.Holder, Maintainer: "ravi", Expires: end})
		}
	}))
	defer labd.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MACLAB_URL", labd.URL)
	t.Setenv("MACLAB_TOKEN", "tok")
	t.Setenv("MACLAB_HOLDER", "ravi@mini")
	c, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	said := func(args ...string) (stderr string, err error) {
		t.Helper()
		r, w, _ := os.Pipe()
		old := os.Stderr
		os.Stderr = w
		err = dispatch(ctx, c, "lease", args)
		os.Stderr = old
		w.Close()
		b, _ := io.ReadAll(r)
		return string(b), err
	}

	if _, err := said("m1-pro"); err != nil {
		t.Fatal(err)
	}
	if _, err := said("m1-pro", "--ttl", "24h"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != (api.LeaseRequest{Holder: "ravi@mini"}) || asked[1] != (api.LeaseRequest{Holder: "ravi@mini", TTLSec: 86400}) {
		t.Fatalf("asked %+v", asked)
	}
	if _, err := said("pro-b"); err == nil || !strings.Contains(err.Error(), "pro-a, the same Configuration (j316s, 10 CPU and 16 GPU cores)") {
		t.Fatalf("a second Lease on a Configuration: %v", err)
	}
	if _, err := said("held"); err == nil || !strings.Contains(err.Error(), "leased by linus@desk (linus) until 15:30:00") {
		t.Fatalf("a Lease on a Mac someone holds: %v", err)
	}
	stderr, err := said("pro-d")
	if err != nil || !strings.Contains(stderr, "warning: you already hold a Lease on pro-c") {
		t.Fatalf("a Maintainer's second Lease on a Configuration: %v, stderr %q", err, stderr)
	}
	if got := leaseLine("m1-pro", &api.Lease{Holder: "ravi@mini", Maintainer: "ravi", Expires: end}); !strings.HasPrefix(got, "m1-pro leased by ravi@mini (ravi) until ") || !strings.Contains(got, end.Local().Format("15:04:05")) {
		t.Fatalf("the line for a Lease taken: %q", got)
	}
}
