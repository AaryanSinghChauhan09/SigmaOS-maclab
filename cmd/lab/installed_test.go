package main

import (
	"bytes"
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

// installedLabd answers the Release routes as labd does and keeps what it
// was asked.
type installedLabd struct {
	asked []string // "METHOD path body"
}

const volunteersRefusal = "an Installed run is refused on vol-mac: it is a Volunteer's Mac, and until Rollback exists the lab runs no Installed run, freezes no installer entry and promotes nothing on a Volunteer's Mac"

func (l *installedLabd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	l.asked = append(l.asked, r.Method+" "+r.URL.Path+" "+string(bytes.TrimSpace(body)))
	at := time.Date(2026, time.November, 14, 3, 0, 0, 0, time.UTC)
	rel := api.Release{Name: "12.0", FrozenBy: "grace", FrozenAt: at, Kernel: "7.1.12-aurora", State: api.ReleaseNoVerdict, Configurations: 2, Passed: 1,
		Macs: []api.ReleaseMac{{Mac: "pro-a", Configuration: "j316s, 10 CPU and 16 GPU cores", Runs: 2, PassedRun: "j1114-0300-ab"}, {Mac: "air-b", Configuration: "j413, 8 CPU and 8 GPU cores"}}}
	switch {
	case strings.Contains(r.URL.Path, "vol-mac") || bytes.Contains(body, []byte("vol-mac")):
		http.Error(w, volunteersRefusal, http.StatusForbidden)
	case r.Method == "GET" && r.URL.Path == "/api/installed":
		json.NewEncoder(w).Encode([]api.Release{rel})
	case r.Method == "POST" && r.URL.Path == "/api/installed/12.0/verdict":
		var req api.VerdictRequest
		json.Unmarshal(body, &req)
		if !req.Confirm {
			json.NewEncoder(w).Encode(api.ReleaseAnswer{Confirm: `Record "qualified" for Release 12.0 as @grace, with the note "ok"? Warning: only 1 of 2 Configurations have a passed Installed run; none on air-b.`})
			return
		}
		rel.State, rel.StateAt = req.State, &at
		json.NewEncoder(w).Encode(api.ReleaseAnswer{Release: &rel})
	case r.Method == "POST" && r.URL.Path == "/api/installed/12.0/promote":
		var req map[string]any
		json.Unmarshal(body, &req)
		if req["confirm"] != true {
			json.NewEncoder(w).Encode(map[string]any{"confirm": "Promote pro-a from Release 12.0 as @ada? Warning: Release 12.0 is not qualified."})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"promoted": "pro-a"})
	default:
		http.Error(w, "unexpected request", 400)
	}
}

func installedLab(t *testing.T) (*installedLabd, func(args ...string) (stdout, stderr string, err error)) {
	t.Helper()
	l := &installedLabd{}
	srv := httptest.NewServer(l)
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MACLAB_URL", srv.URL)
	t.Setenv("MACLAB_TOKEN", "tok")
	t.Setenv("MACLAB_HOLDER", "grace@laptop")
	c, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	return l, func(args ...string) (string, string, error) {
		t.Helper()
		or, ow, _ := os.Pipe()
		er, ew, _ := os.Pipe()
		oldOut, oldErr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = ow, ew
		err := dispatch(context.Background(), c, args[0], args[1:])
		os.Stdout, os.Stderr = oldOut, oldErr
		ow.Close()
		ew.Close()
		o, _ := io.ReadAll(or)
		e, _ := io.ReadAll(er)
		return string(o), string(e), err
	}
}

func TestInstalledListShowsEveryReleaseWithItsVerdict(t *testing.T) {
	_, lab := installedLab(t)
	out, _, err := lab("installed", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"12.0  no verdict", "by @grace", "1 of 2 Configurations have a passed run", "pro-a (j316s, 10 CPU and 16 GPU cores): 2 Installed runs, passed j1114-0300-ab", "air-b"} {
		if !strings.Contains(out, want) {
			t.Errorf("lab installed list lacks %q:\n%s", want, out)
		}
	}
}

// A verdict is recorded only once its sentence is confirmed: lab shows the
// sentence, warnings and all, and --yes is the answer where nobody can type.
func TestInstalledVerdictShowsTheSentenceAndRecordsOnceConfirmed(t *testing.T) {
	l, lab := installedLab(t)
	_, said, err := lab("installed", "verdict", "--release", "12.0", "qualified", "--note", "ok")
	if err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(said, "none on air-b") {
		t.Fatalf("with nobody to confirm: %v; said %q", err, said)
	}
	if len(l.asked) != 1 || strings.Contains(l.asked[0], `"confirm":true`) {
		t.Fatalf("an unconfirmed verdict was sent as confirmed: %v", l.asked)
	}
	l.asked = nil
	out, said, err := lab("installed", "verdict", "--release", "12.0", "qualified", "--note", "ok", "--yes")
	if err != nil || !strings.Contains(said, "Warning: only 1 of 2") || !strings.Contains(out, "12.0  qualified 2026-11-14") {
		t.Fatalf("a confirmed verdict: %v; said %q; printed %q", err, said, out)
	}
	if len(l.asked) != 2 || !strings.Contains(l.asked[1], `"state":"qualified"`) || !strings.Contains(l.asked[1], `"note":"ok"`) || !strings.Contains(l.asked[1], `"confirm":true`) {
		t.Fatalf("what lab sent: %v", l.asked)
	}
	l.asked = nil
	if _, _, err := lab("installed", "verdict", "--release", "12.0", "--revoke", "--note", "wrong Release", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(l.asked) != 2 || !strings.Contains(l.asked[1], `"state":"revoked"`) {
		t.Fatalf("what a revoke sent: %v", l.asked)
	}
	l.asked = nil
	if _, _, err := lab("installed", "verdict", "--release", "12.0", "not-qualified", "--note", "dark", "--yes"); err != nil || !strings.Contains(l.asked[1], `"state":"not_qualified"`) {
		t.Fatalf("not-qualified: %v %v", err, l.asked)
	}
	if _, _, err := lab("installed", "verdict", "--release", "12.0", "splendid", "--note", "x", "--yes"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("a verdict of no known state: %v", err)
	}
}

func TestInstalledPromoteSaysWhenTheReleaseIsNotQualifiedBeforeItGoesOn(t *testing.T) {
	l, lab := installedLab(t)
	_, said, err := lab("installed", "promote", "pro-a", "--release", "12.0", "--job", "j1114-0300-ab")
	if err == nil || !strings.Contains(said, "is not qualified") || len(l.asked) != 1 {
		t.Fatalf("with nobody to confirm: %v; said %q; asked %v", err, said, l.asked)
	}
	out, _, err := lab("installed", "promote", "pro-a", "--release", "12.0", "--job", "j1114-0300-ab", "--yes")
	if err != nil || !strings.Contains(out, `"promoted": "pro-a"`) || !strings.Contains(l.asked[len(l.asked)-1], `"confirm":true`) {
		t.Fatalf("a confirmed Promotion: %v %q %v", err, out, l.asked)
	}
}

// On a Volunteer's Mac lab says what the lab said, and nothing else happens.
func TestInstalledIsRefusedOnAVolunteersMacThroughLab(t *testing.T) {
	l, lab := installedLab(t)
	for what, args := range map[string][]string{
		"run --installed": {"run", "vol-mac", "--installed", strings.Repeat("ab", 32), "--no-wait"},
		"freeze-entry":    {"installed", "freeze-entry", "vol-mac", "--release", "12.0"},
		"promote":         {"installed", "promote", "vol-mac", "--release", "12.0", "--job", "j1", "--yes"},
	} {
		l.asked = nil
		_, _, err := lab(args...)
		if err == nil || !strings.Contains(err.Error(), "Volunteer's Mac") {
			t.Errorf("lab %s on a Volunteer's Mac: %v", what, err)
		}
		if len(l.asked) != 1 {
			t.Errorf("lab %s went on after the refusal: %v", what, l.asked)
		}
	}
}
