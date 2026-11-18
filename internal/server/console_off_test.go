package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

const noConsoleLine = "The serial console is not part of Omarchy Mac Lab.\n"

// The serial console is out: the three console routes, the serial log,
// exec's serial path and the serial-assisted mode are each refused with
// one plain line, and the Mac's controller is never asked.
func TestTheSerialConsoleIsRefusedEverywhere(t *testing.T) {
	l, c := countedModeLab(t, "m1-console")
	grace := l.member("grace")
	ada := testMaintainer{Name: harnessMaintainer, Token: harnessToken}
	l.leaseAs(ada, 0)
	before := c.counts()
	mac := "/api/devices/" + l.name

	for _, ask := range []struct {
		what, method, path string
		body               any
	}{
		{"the console's stream", "GET", mac + "/console", nil},
		{"typing into the console", "POST", mac + "/console", nil},
		{"arming the console", "POST", mac + "/console/arm", nil},
		{"the serial log", "GET", mac + "/serial", nil},
		{"the serial-assisted mode", "POST", mac + "/recovery-mode", api.RecoveryModeRequest{Mode: api.RecoverySerialAssisted, Holder: "ada@laptop", Transaction: "t1"}},
		{"a serial-assisted job", "POST", "/api/jobs", api.JobSpec{Device: l.name, Holder: "ada@laptop", RecoveryMode: api.RecoverySerialAssisted, Kernel: l.artifact("krel=7.2-serial behavior=ok")}},
	} {
		for _, m := range []testMaintainer{ada, grace} {
			if ask.what == "the serial-assisted mode" && m == grace {
				continue // the recovery policy is a Maintainer's
			}
			if r, body := l.as(m.Token, ask.method, ask.path, ask.body); r.StatusCode != http.StatusGone || body != noConsoleLine {
				t.Errorf("%s asked for %s: %s %q; want 410 and one plain line", m.Name, ask.what, r.Status, body)
			}
		}
	}
	if jobs, _ := l.s.store.jobs(l.name, 100, true); len(jobs) != 0 {
		t.Fatalf("a serial-assisted job was queued: %+v", jobs[0])
	}

	var caps api.RecoveryCapabilities
	if r, body := l.get("GET", "/api/recovery"); r.StatusCode != 200 || json.Unmarshal([]byte(body), &caps) != nil || len(caps.Modes) != 1 || caps.Modes[0] != api.RecoveryNetworkOnly {
		t.Fatalf("the recovery modes labd offers: %s %s", r.Status, body)
	}

	// With the Mac off the network, lab exec has no other way to it.
	l.offNetwork()
	r, body := l.execAs(ada, "DO_NOT_TYPE")
	if r.StatusCode != http.StatusConflict || !strings.Contains(body, l.name+" is offline") || !strings.Contains(body, strings.TrimSpace(noConsoleLine)) || strings.Count(body, "\n") != 1 {
		t.Fatalf("lab exec on a Mac off the network: %s %q; want 409 and one plain line", r.Status, body)
	}
	var last api.ExecEvent
	for _, e := range execLines(t, l.s, l.name) {
		if e.Command == "DO_NOT_TYPE" {
			last = e
		}
	}
	if last.State != api.ExecRefused || !strings.Contains(last.Why, "offline") {
		t.Fatalf("the refused exec in the Exec record: %+v", last)
	}

	if got := c.counts(); got != before {
		t.Fatalf("the Mac's controller was asked: before %v, after %v", before, got)
	}
}
