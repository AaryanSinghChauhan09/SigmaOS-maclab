package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/installed"
)

// Until Rollback exists on real Macs the lab refuses an Installed run on a
// Volunteer's Mac, and with it freeze-entry and Promotion, through every
// way in. Nothing on the Mac refuses one: only labd does, so labd sends no
// Install operation to any Mac.

// ioRelease is the Release these tests install.
const ioRelease = "9.9"

// ioRecorder records every command labd hands the Mac in its check-in and
// report answers, and every result the Mac sends back.
type ioRecorder struct {
	mu   sync.Mutex
	cmds []api.Command
}

func (r *ioRecorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/agent/checkin" && req.URL.Path != "/api/agent/report" {
			next.ServeHTTP(rw, req)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, req)
		var resp struct {
			Commands []api.Command `json:"commands"`
		}
		json.Unmarshal(rec.Body.Bytes(), &resp)
		r.mu.Lock()
		r.cmds = append(r.cmds, resp.Commands...)
		r.mu.Unlock()
		for k, v := range rec.Header() {
			rw.Header()[k] = v
		}
		rw.WriteHeader(rec.Code)
		rw.Write(rec.Body.Bytes())
	})
}

func (r *ioRecorder) commands() []api.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.Command(nil), r.cmds...)
}

// ioInstalls is the install commands labd sent the Mac.
func (r *ioRecorder) ioInstalls() []api.Command {
	var out []api.Command
	for _, c := range r.commands() {
		if c.Kind == api.CmdInstall {
			out = append(out, c)
		}
	}
	return out
}

// ioLab is a Volunteer's Mac at its default Access level, open to the lab
// for two hours, with omarchy-m-test on, a lease held by "io-tester" and
// Release 9.9's expected manifest frozen through labd's endpoint. The Sim
// would answer the identity checks as a Mac with the Release installed. It
// returns the manifest's sha256 and the installer entry the Mac would show.
func ioLab(t *testing.T, c labConfig) (*lab, *agent.FakeClock, string, installed.Entry) {
	t.Helper()
	c.config = func(cfg *Config) { cfg.OMT = true }
	l, clock := windowEndLab(t, c)
	name := l.name

	payload := tVml + "  vmlinuz\n" + tDtbA + "  dtbs/t-a.dtb\n" + tDtbB + "  dtbs/t-b.dtb\n" + tMtr + "  modules.dep\n"
	m := installed.Manifest{Schema: installed.ManifestSchema, Release: ioRelease,
		Artifacts: []installed.Artifact{{Name: "linux-aurora", File: "linux-aurora-9.9.pkg.tar.zst", SHA256: tVml, Version: "9.9-1", MtreeSHA256: tMtr}},
		DTBs:      []installed.DTB{{Name: "t-a.dtb", SHA256: tDtbA}, {Name: "t-b.dtb", SHA256: tDtbB}},
		Macs:      map[string]installed.MacExpect{name: {Board: "apple,sim", BoardDTB: "t-a.dtb", UbootGzSHA256: tUbt, BootBinSHA256: tBoot}}}
	m.DTBsSHA256 = installed.DTBsDigest(m.DTBs)
	m.Kernel.Release, m.Kernel.VmlinuzSHA256 = tKrel, tVml
	m.Kernel.PayloadManifest, m.Kernel.PayloadManifestSHA256, m.Kernel.PayloadCount = "payload/linux-aurora.files.sha256", installed.SHA256([]byte(payload)), 4
	m.Kernel.RegeneratedOnMac = []string{"modules.dep"}
	m.M1n1.BinSHA256, m.M1n1.Version = tM1n1, "v1.6.1-omarchy.sim"
	mb, _ := json.MarshalIndent(m, "", "  ")
	if resp, body := l.post("/api/installed/"+ioRelease+"/manifest", map[string]any{"manifest": l.artifact(string(mb)), "payload": l.artifact(payload), "approved_by": []string{"boss", "dave"}}); resp.StatusCode != 200 {
		t.Fatalf("freeze manifest: %s %s", resp.Status, body)
	}

	tokens := []string{"root=UUID=sim", "rw", "quiet", "splash"}
	path := "boot():/EFI/Linux/omarchy_linux-aurora.efi#" + tB2
	lines := []string{"  //linux-aurora", "  protocol: efi", "  path: " + path, "  cmdline: root=UUID=sim rw quiet splash"}
	entry := installed.Entry{Found: true, Name: "Omarchy / linux-aurora", Protocol: "efi", Path: path,
		Cmdline: strings.Join(tokens, " "), CmdlineTokens: tokens, EntryLines: lines, EntrySHA256: installed.EntryDigest(lines), UKISHA256: tUKI, UKIBlake2b: tB2,
		UKISections: map[string]installed.Section{".linux": {SHA256: tVml}, ".initrd": {SHA256: tIrd}, ".cmdline": {SHA256: "cc", Text: strings.Join(tokens, " ")}, ".uname": {SHA256: "uu", Text: tKrel}}}
	l.sim.SetInstalledObs(func(phase, job, bootID, kernel, cmdline string) []byte {
		stage2 := "v1.6.1-omarchy.sim"
		if phase == installed.PhaseBefore {
			stage2 = "v1.6.1-omarchy.old"
		}
		o := map[string]any{"schema": "maclab.installed-observed/1", "phase": phase, "device": name, "job": job,
			"running":  map[string]any{"boot_id": bootID, "uname_r": kernel, "proc_cmdline": cmdline},
			"packages": map[string]any{"linux-aurora": map[string]any{"version": "9.9-1", "mtree_sha256": tMtr}},
			"payload": map[string]any{"krel": tKrel, "root": "/usr/lib/modules/" + tKrel, "count": 4,
				"files": map[string]string{"vmlinuz": tVml, "dtbs/t-a.dtb": tDtbA, "dtbs/t-b.dtb": tDtbB, "modules.dep": "regenerated"}},
			"installer_entry": entry,
			"boot_bin": map[string]any{"path": "/boot/efi/m1n1/boot.bin", "sha256": tBoot, "parts": map[string]any{"m1n1_sha256": tM1n1,
				"dtb_shas": []string{tDtbA, tDtbB}, "dtb_count": 2, "board_dtb_sha256": tDtbA, "board_dtb_count": 1, "uboot_gz_sha256": tUbt, "trailer": ""}},
			"dt": map[string]any{"board": "apple,sim", "compatible": []string{"apple,sim"}, "live_stage2_version": stage2}}
		b, _ := json.MarshalIndent(o, "", " ")
		return b
	})
	l.takeLease("io-tester", time.Hour)
	return l, clock, installed.SHA256(mb), entry
}

// The Volunteer's Mac is inside its Availability window and its caller
// holds the Lease: the one thing in the way is that it is a Volunteer's.
func TestAnInstalledRunAFreezeAndAPromotionAreRefusedOnAVolunteersMac(t *testing.T) {
	rec := &ioRecorder{}
	l, _, msha, _ := ioLab(t, labConfig{wrap: rec.wrap})
	const sentence = "Volunteer's Mac"
	refused := func(what string, r *http.Response, body string) {
		t.Helper()
		if r.StatusCode != http.StatusForbidden || !strings.Contains(body, sentence) || !strings.Contains(body, l.name) {
			t.Errorf("%s on a Volunteer's Mac: %s %s", what, r.Status, body)
		}
	}
	for _, level := range []string{"qualification", "full"} {
		l.alSetAccess(level)
		r, body := l.as(harnessToken, "POST", "/api/jobs", api.JobSpec{Device: l.name, InstalledManifest: msha, Holder: "io-tester"})
		refused("an Installed run at "+level, r, body)
		r, body = l.as(harnessToken, "POST", "/api/installed/"+ioRelease+"/entry/"+l.name, nil)
		refused("freeze-entry at "+level, r, body)
		r, body = l.as(harnessToken, "POST", "/api/installed/"+ioRelease+"/promote", map[string]any{"device": l.name, "job": "j-any", "confirm": true})
		refused("a Promotion at "+level, r, body)
	}
	// labd's own way in, with nobody asking, is refused the same.
	if _, code, err := l.s.Submit(api.JobSpec{Device: l.name, InstalledManifest: msha, Holder: "io-tester"}); err == nil || code != http.StatusForbidden || !strings.Contains(err.Error(), sentence) {
		t.Errorf("an Installed run queued inside labd: %d %v", code, err)
	}
	// A retry of an Installed run from before the refusal is refused too.
	old := &api.Job{ID: "j-old-installed", Spec: api.JobSpec{Device: l.name, InstalledManifest: msha, Holder: "io-tester"}, State: api.JobDone, Outcome: api.OutcomePass, DeviceID: l.s.dev(l.name).snapshot().ID}
	if err := l.s.store.saveJob(old); err != nil {
		t.Fatal(err)
	}
	r, body := l.as(harnessToken, "POST", "/api/jobs/"+old.ID+"/retry", nil)
	refused("a retry of an Installed run", r, body)

	if jobs, _ := l.s.store.jobs(l.name, 100, true); len(jobs) != 0 {
		t.Fatalf("a refused Installed run was queued: %+v", jobs[0])
	}
	time.Sleep(time.Second)
	for _, c := range rec.commands() {
		var ts api.TestSpec
		json.Unmarshal(c.Args, &ts)
		if c.Kind == api.CmdInstall || c.Kind == api.CmdExec || c.Kind == api.CmdRunTest && ts.Identity != nil {
			t.Errorf("labd sent the Volunteer's Mac %s: %s", c.Kind, c.Args)
		}
	}
	if got := l.sim.Installs(); len(got) != 0 {
		t.Fatalf("the Mac's installer ran: %v", got)
	}
	if _, err := os.Stat(filepath.Join(l.s.frozenDir(ioRelease), "installer-entry", l.name+".json")); err == nil {
		t.Fatal("an installer entry was frozen for the Volunteer's Mac")
	}
}

// A freeze is refused outside the Availability window too, and says so.
func TestFreezeEntryIsRefusedOutsideTheAvailabilityWindow(t *testing.T) {
	l, _, _, _ := ioLab(t, labConfig{})
	l.mustVolunteer("pause")
	l.waitDevice(func(d api.Device) bool { return d.Lease == nil })
	r, body := l.as(harnessToken, "POST", "/api/installed/"+ioRelease+"/entry/"+l.name, nil)
	if r.StatusCode != http.StatusConflict || !strings.Contains(body, "Availability window") {
		t.Fatalf("freeze-entry outside the Availability window: %s %s", r.Status, body)
	}
}

// Allowing the lab's identity checks doesn't let a Maintainer's script
// through under their name.
func TestLabAgentRefusesAScriptNamedLikeAnIdentityCheckAtQualification(t *testing.T) {
	w := newALWire()
	l, _, _, _ := ioLab(t, labConfig{wrap: w.wrap})
	script := l.artifact("#!/bin/sh\ntouch /io-bypassed\n")
	for _, name := range []string{"identity-before", "identity-booted", "identity-after"} {
		id := w.send(api.CmdRunTest, "io-job-bypass", "mallory", api.TestSpec{Name: name, Script: script})
		if res := w.result(t, id); res.OK || strings.Contains(string(res.Output), "sim identity") {
			t.Fatalf("lab-agent at Qualification ran a script named %s: %+v", name, res)
		}
	}
	exec := w.send(api.CmdExec, "io-job-bypass", "mallory", api.ExecArgs{Command: "pacman -U linux-aurora-9.9.pkg.tar.zst"})
	if res := w.result(t, exec); res.OK {
		t.Fatalf("lab-agent at Qualification ran an exec of the installer: %+v", res)
	}
}
