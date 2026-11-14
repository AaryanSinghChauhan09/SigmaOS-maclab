package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/api"
)

// Tokens and devices stored before the volunteer flag existed read as a
// lab Mac's.
func TestTokensFromBeforeVolunteers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labd.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE devices (id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, secret_hash TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE tokens (token_hash TEXT PRIMARY KEY, name TEXT NOT NULL, label TEXT, model TEXT, expires INTEGER NOT NULL);
INSERT INTO tokens VALUES ('old', 'm1air', 'left desk', 'laptop', 9999999999);
INSERT INTO devices VALUES ('d-1', 'm2air', 'x', '{"id":"d-1","name":"m2air","label":"right desk","model":"laptop","state":"ready"}');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	tok, err := st.token("old", time.Now())
	if err != nil || tok.Name != "m1air" || tok.Volunteer {
		t.Fatalf("old token: %+v, %v", tok, err)
	}
	rows, err := st.devices()
	if err != nil || len(rows) != 1 || rows[0].d.Volunteer {
		t.Fatalf("old device: %+v, %v", rows, err)
	}
	if _, err := openStore(path); err != nil {
		t.Fatalf("opening it again: %v", err)
	}
}

// filler reads as n bytes of one character, made as they are read.
type filler struct {
	n     int64
	chunk []byte
}

func fill(n int64, c byte) *filler { return &filler{n, bytes.Repeat([]byte{c}, 32<<10)} }

func (f *filler) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(int64(len(p)), f.n)], f.chunk)
	f.n -= int64(n)
	return n, nil
}

// Enrollments, check-ins and reports have size limits real agents are far
// below; uploads go elsewhere and have none.
func TestAgentBodyLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := newWith("ada", "lab_adm_secret", ctx, Config{DataDir: t.TempDir()}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	// post sends body, of length bytes if length > 0, else of a length it
	// doesn't say.
	post := func(path, secret string, body io.Reader, length ...int64) (int, string) {
		t.Helper()
		r := httptest.NewRequest("POST", labdAt+path, body)
		r.ContentLength = -1
		for _, n := range length {
			r.ContentLength = n
		}
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	// huge is a JSON body with a string of n bytes in it.
	huge := func(prefix string, n int64, suffix string) io.Reader {
		return io.MultiReader(strings.NewReader(prefix), fill(n, 'a'), strings.NewReader(suffix))
	}
	// The facts of a Mac with plenty to say: a long command line and many
	// preflight problems and warnings.
	facts := api.Facts{Hostname: "omarchy", Machine: "aarch64", DTModel: "Apple MacBook Air (13-inch, M2, 2022)",
		Compatible: []string{"apple,j413", "apple,t8112", "apple,arm-platform"}, Bootloader: "limine", Initramfs: "mkinitcpio",
		Watchdog: true, PanicTimeout: 10, GUIUser: "omarchy", AgentVersion: "0.9.0"}
	for i := range 20 {
		facts.Problems = append(facts.Problems, fmt.Sprintf("problem %d: /boot has %d MB free; a lab kernel and its initramfs need 200 MB, so clear old kernels out first", i, i))
		facts.Warnings = append(facts.Warnings, fmt.Sprintf("warning %d: journald keeps the journal in memory only, so a lab boot that fails takes its journal with it", i))
	}
	enrollBody, _ := json.Marshal(api.EnrollRequest{Token: "enr_test_m1", Facts: facts, ActivationKey: testActivationKey, TermsSHA256: termsSum()})
	checkin, _ := json.Marshal(api.Checkin{BootID: "b1", Kernel: "6.17.0-asahi", Cmdline: strings.Repeat("x", 4096), Facts: &facts,
		Health: api.Health{Uptime: 100, SystemState: "running"}, Running: []string{"c1", "c2"}})
	if len(enrollBody) > enrollBodyMax/16 || len(checkin) > checkinBodyMax/16 {
		t.Errorf("a full enrollment is %d bytes and a full check-in %d: less headroom than wanted", len(enrollBody), len(checkin))
	}

	tok, err := s.store.addTokenForTest("m1")
	if err != nil {
		t.Fatal(err)
	}
	if code, body := post("/api/agent/enroll", "", huge(`{"token":"`+tok+`","facts":{"hostname":"`, enrollBodyMax, `"}}`)); code != 413 {
		t.Errorf("an enrollment over %d bytes: %d %s", enrollBodyMax, code, body)
	}
	code, body := post("/api/agent/enroll", "", bytes.NewReader(enrollBody))
	var e api.EnrollResponse
	if json.Unmarshal([]byte(body), &e); code != 200 || e.Secret == "" {
		t.Fatalf("enroll after the refusal, with the same token: %d %s", code, body)
	}

	if code, body := post("/api/agent/checkin", e.Secret, bytes.NewReader(checkin)); code != 200 {
		t.Errorf("a full check-in: %d %s", code, body)
	}
	if code, body := post("/api/agent/checkin", e.Secret, huge(`{"boot_id":"b1","facts":{"hostname":"`, checkinBodyMax, `"}}`)); code != 413 {
		t.Errorf("a check-in over %d bytes: %d %s", checkinBodyMax, code, body)
	}
	if d := s.dev("m1").snapshot(); d.Facts.Hostname != "omarchy" {
		t.Errorf("the refused check-in's facts were kept: hostname of %d bytes", len(d.Facts.Hostname))
	}

	// The largest real report: an exec's whole output, 1 MB per stream, all
	// control characters, which JSON writes six bytes each.
	junk := strings.Repeat("\x01", 1<<20)
	out, _ := json.Marshal(api.ExecResult{Stdout: junk, Stderr: junk, Via: "agent"})
	rep, _ := json.Marshal(api.Report{Results: []api.CommandResult{{ID: "c1", OK: true, Output: out}}})
	if code, body := post("/api/agent/report", e.Secret, bytes.NewReader(rep)); code != 200 {
		t.Errorf("a report of %d bytes: %d %s", len(rep), code, body)
	}
	// One that says it's too large is turned away unread; one that doesn't
	// say is cut off where it passes the limit, as check-ins are above.
	if code, body := post("/api/agent/report", e.Secret, fill(reportBodyMax+1, ' '), reportBodyMax+1); code != 413 {
		t.Errorf("a report over %d bytes: %d %s", reportBodyMax, code, body)
	}

	// The Mac adds files to its running job, which was submitted for it.
	if err := s.store.saveJob(&api.Job{ID: "j1016-120000-ab12", Spec: api.JobSpec{Device: "m1"}, DeviceID: e.DeviceID, State: api.JobTesting}); err != nil {
		t.Fatal(err)
	}
	v := s.dev("m1")
	v.mu.Lock()
	v.d.ActiveJob = "j1016-120000-ab12"
	v.mu.Unlock()
	if code, body := post("/api/agent/upload?job=j1016-120000-ab12&name=boot0/journal.txt", e.Secret, fill(reportBodyMax+1, 'j')); code != 200 {
		t.Errorf("an upload larger than any report: %d %s", code, body)
	}
	if fi, err := os.Stat(filepath.Join(s.jobDir("j1016-120000-ab12"), "boot0", "journal.txt")); err != nil || fi.Size() != reportBodyMax+1 {
		t.Errorf("the upload: %v, %v", fi, err)
	}
}

func TestVolunteerLabRunsVolunteerCommands(t *testing.T) {
	l := newLabWith(t, labConfig{volunteer: true})
	l.waitDevice(func(d api.Device) bool { return d.BootID != "" })
	if _, err := l.volunteer("no-such-command"); err == nil || !strings.Contains(err.Error(), `"no-such-command"`) {
		t.Fatalf("an unknown Volunteer command: %v", err)
	}
}

func TestEncryptedMacWaitsForItsPassword(t *testing.T) {
	l := newLabWith(t, labConfig{volunteer: true})
	l.sim.Encrypted = true
	before := l.waitDevice(func(d api.Device) bool { return d.BootID != "" })
	l.sim.PowerCycle()
	time.Sleep(time.Second) // several reboot delays
	if d := l.s.dev(l.name).snapshot(); d.BootID != before.BootID {
		t.Fatal("an encrypted Mac booted without its disk password")
	}
	l.sim.TypePassword()
	l.waitDevice(func(d api.Device) bool { return d.BootID != "" && d.BootID != before.BootID })
}

func TestRollbackReturnsTheMacToItsSnapshot(t *testing.T) {
	l := newLabWith(t, labConfig{volunteer: true})
	l.waitDevice(func(d api.Device) bool { return d.BootID != "" })
	l.sim.Snapshot()
	l.sim.DefaultKernel = "7.3-installed" // what an installer leaves behind
	l.sim.PowerCycle()
	l.waitDevice(func(d api.Device) bool { return d.Kernel == "7.3-installed" })
	if err := l.sim.Rollback(); err != nil {
		t.Fatal(err)
	}
	l.sim.PowerCycle()
	l.waitDevice(func(d api.Device) bool { return d.Kernel == agent.SimKnownGood })
}

func TestLabBootThatLosesTheNetworkNeverChecksIn(t *testing.T) {
	l := newLabWith(t, labConfig{volunteer: true, oob: true})
	l.openForTheLab(time.Now())
	l.baseline()
	j := l.run(api.JobSpec{Kernel: l.artifact("krel=7.2-offline behavior=no-network")})
	if j.Result.Booted || j.Outcome == api.OutcomePass {
		t.Fatalf("a Lab boot with no network reached labd: %s %+v", j.Outcome, j.Result)
	}
	l.waitDevice(func(d api.Device) bool { return d.State == api.StateReady && d.Kernel == agent.SimKnownGood })
}
