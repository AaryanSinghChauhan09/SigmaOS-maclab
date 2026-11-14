package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/installed"
)

// Releases, verdicts and Promotion. A Release is listed from the moment its
// manifest is frozen and carries one of four states; freeze-entry reads the
// Mac through the identity check, never an exec; a Volunteer's Mac takes no
// Installed run, no freeze and no Promotion.

func (l *lab) releases(token string) []api.Release {
	l.t.Helper()
	r, body := l.as(token, "GET", "/api/installed", nil)
	var out []api.Release
	if r.StatusCode != 200 || json.Unmarshal([]byte(body), &out) != nil {
		l.t.Fatalf("the Releases: %s %s", r.Status, body)
	}
	return out
}

func (l *lab) release(token, name string) api.Release {
	l.t.Helper()
	r, body := l.as(token, "GET", "/api/installed/"+name, nil)
	var out struct {
		Release api.Release `json:"release"`
	}
	if r.StatusCode != 200 || json.Unmarshal([]byte(body), &out) != nil {
		l.t.Fatalf("Release %s: %s %s", name, r.Status, body)
	}
	return out.Release
}

func (l *lab) verdict(token string, req api.VerdictRequest) (*http.Response, api.ReleaseAnswer, string) {
	l.t.Helper()
	r, body := l.as(token, "POST", "/api/installed/9.9/verdict", req)
	var a api.ReleaseAnswer
	json.Unmarshal([]byte(body), &a)
	return r, a, body
}

func (l *lab) mustVerdict(token, state, note string) api.ReleaseAnswer {
	l.t.Helper()
	r, a, body := l.verdict(token, api.VerdictRequest{State: state, Note: note, Confirm: true})
	if r.StatusCode != 200 || a.Release == nil {
		l.t.Fatalf("verdict %s: %s %s", state, r.Status, body)
	}
	return a
}

// freeLease drops the Lease installedLab left on the Mac for its own jobs.
func (l *lab) freeLease() {
	v := l.s.dev(l.name)
	v.mu.Lock()
	v.d.Lease = nil
	v.mu.Unlock()
}

func TestAReleaseIsListedFromTheMomentItsManifestIsFrozen(t *testing.T) {
	l, msha, _ := installedLab(t, nil)
	grace := l.member("grace")
	before := time.Now().Add(-time.Minute)

	got := l.releases(grace.Token)
	if len(got) != 1 {
		t.Fatalf("Releases listed: %+v", got)
	}
	r := got[0]
	if r.Name != "9.9" || r.FrozenBy != harnessMaintainer || r.FrozenByID != testAccount(harnessMaintainer) || r.FrozenAt.Before(before) || r.ManifestSHA256 != msha || r.Kernel != tKrel {
		t.Fatalf("the Release: %+v", r)
	}
	if len(r.Packages) != 1 || r.Packages[0] != (api.ReleasePackage{Name: "linux-aurora", Version: "9.9-1"}) {
		t.Fatalf("its packages: %+v", r.Packages)
	}
	if len(r.Macs) != 1 || r.Macs[0].Mac != "sim-mac" || r.Macs[0].Board != "apple,sim" || r.Macs[0].Configuration == "" || r.Macs[0].DeviceID != l.s.dev("sim-mac").snapshot().ID {
		t.Fatalf("its Macs: %+v", r.Macs)
	}
	if r.State != api.ReleaseNoVerdict || len(r.Verdicts) != 0 || r.Configurations != 1 || r.Passed != 0 {
		t.Fatalf("a Release nobody judged: state %q, verdicts %v, coverage %d of %d", r.State, r.Verdicts, r.Passed, r.Configurations)
	}

	// Its Macs are fixed at that moment: a Configuration set later changes nothing.
	l.s.setConfiguration(l.s.dev("sim-mac"), harnessMaintainer, 12, 38)
	if now := l.release(grace.Token, "9.9"); now.Macs[0].Configuration != r.Macs[0].Configuration {
		t.Fatalf("the Release's Mac changed after the freeze: %q, was %q", now.Macs[0].Configuration, r.Macs[0].Configuration)
	}

	// An M team member freezes a manifest too; nobody else reads or freezes.
	payload := tVml + "  vmlinuz\n"
	m := installed.Manifest{Schema: installed.ManifestSchema, Release: "10.0",
		Artifacts: []installed.Artifact{{Name: "linux-aurora", File: "f", SHA256: tVml, Version: "10.0-1", MtreeSHA256: tMtr}},
		DTBs:      []installed.DTB{{Name: "t-a.dtb", SHA256: tDtbA}},
		Macs: map[string]installed.MacExpect{"not-enrolled": {Board: "apple,other", BoardDTB: "t-a.dtb", UbootGzSHA256: tUbt},
			"sim-mac": {Board: "apple,sim", BoardDTB: "t-a.dtb", UbootGzSHA256: tUbt}}}
	m.DTBsSHA256 = installed.DTBsDigest(m.DTBs)
	m.Kernel.Release, m.Kernel.VmlinuzSHA256 = "7.2.0-sim-10.0", tVml
	m.Kernel.PayloadManifest, m.Kernel.PayloadManifestSHA256, m.Kernel.PayloadCount = "payload/k.sha256", installed.SHA256([]byte(payload)), 1
	m.M1n1.BinSHA256, m.M1n1.Version = tM1n1, "v1"
	mb, _ := json.Marshal(m)
	freeze := map[string]any{"manifest": l.artifact(string(mb)), "payload": l.artifact(payload)}
	if resp, body := l.as(grace.Token, "POST", "/api/installed/10.0/manifest", freeze); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "Lease") {
		t.Fatalf("an M team member freezing a manifest with no Lease on one of its Macs: %s %s", resp.Status, body)
	}
	l.freeLease()
	l.leaseAs(grace, 0)
	if resp, body := l.as(grace.Token, "POST", "/api/installed/10.0/manifest", freeze); resp.StatusCode != 200 {
		t.Fatalf("an M team member freezing a manifest: %s %s", resp.Status, body)
	}
	got = l.releases(grace.Token)
	if len(got) != 2 || got[0].Name != "10.0" || got[0].FrozenBy != "grace" || len(got[0].Macs) != 2 || got[0].Macs[0].Mac != "not-enrolled" || got[0].Macs[0].Configuration == "" || got[0].Configurations != 2 {
		t.Fatalf("Releases, newest first: %+v", got)
	}
	eve := l.addMaintainer("", "eve")
	if err := l.s.store.setMaintainer(testAccount("eve"), false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/installed", "/api/installed/9.9"} {
		if resp, body := l.as(eve.Token, "GET", path, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("a person with no role read %s: %s %s", path, resp.Status, body)
		}
	}
}

func TestTheFirstVerdictIsAnyMTeamMembersAndEveryLaterOneAMaintainers(t *testing.T) {
	l, _, _ := installedLab(t, nil)
	grace, linus := l.member("grace"), l.member("linus")

	// A note is required every time, and nothing is recorded until the
	// sentence is confirmed.
	if r, _, body := l.verdict(grace.Token, api.VerdictRequest{State: api.ReleaseNotQualified, Note: "  ", Confirm: true}); r.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "note") {
		t.Fatalf("a verdict with no note: %s %s", r.Status, body)
	}
	r, a, body := l.verdict(grace.Token, api.VerdictRequest{State: api.ReleaseNotQualified, Note: "the display stays dark"})
	if r.StatusCode != 200 || a.Release != nil || !strings.Contains(a.Confirm, "9.9") || !strings.Contains(a.Confirm, "not qualified") {
		t.Fatalf("the sentence to confirm: %s %s", r.Status, body)
	}
	if got := l.release(grace.Token, "9.9"); got.State != api.ReleaseNoVerdict {
		t.Fatalf("an unconfirmed verdict was recorded: %+v", got)
	}
	if r, _, body := l.verdict(grace.Token, api.VerdictRequest{State: api.ReleaseRevoked, Note: "no", Confirm: true}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("an M team member revoking: %s %s", r.Status, body)
	}
	if r, _, body := l.verdict(harnessToken, api.VerdictRequest{State: api.ReleaseRevoked, Note: "no", Confirm: true}); r.StatusCode != http.StatusConflict {
		t.Fatalf("revoking a Release with no verdict: %s %s", r.Status, body)
	}
	if r, _, body := l.verdict(grace.Token, api.VerdictRequest{State: "splendid", Note: "x", Confirm: true}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a verdict of no known state: %s %s", r.Status, body)
	}

	first := l.mustVerdict(grace.Token, api.ReleaseNotQualified, "the display stays dark")
	if first.Release.State != api.ReleaseNotQualified || first.Release.StateAt == nil {
		t.Fatalf("after the first verdict: %+v", first.Release)
	}
	// Every later one is a Maintainer's.
	for _, m := range []testMaintainer{grace, linus} {
		if r, _, body := l.verdict(m.Token, api.VerdictRequest{State: api.ReleaseNotQualified, Note: "again", Confirm: true}); r.StatusCode != http.StatusForbidden || !strings.Contains(body, "Maintainer") {
			t.Fatalf("%s changing the verdict: %s %s", m.Name, r.Status, body)
		}
	}
	// A Maintainer changing it is warned of what it replaces.
	_, a, _ = l.verdict(harnessToken, api.VerdictRequest{State: api.ReleaseNotQualified, Note: "still dark on j314"})
	if !strings.Contains(a.Confirm, "@grace") || len(a.Warnings) == 0 {
		t.Fatalf("the change's sentence does not say whose verdict it replaces: %+v", a)
	}
	l.mustVerdict(harnessToken, api.ReleaseNotQualified, "still dark on j314")
	l.mustVerdict(harnessToken, api.ReleaseRevoked, "recorded against the wrong Release")
	if r, _, body := l.verdict(harnessToken, api.VerdictRequest{State: api.ReleaseRevoked, Note: "twice", Confirm: true}); r.StatusCode != http.StatusConflict {
		t.Fatalf("revoking twice: %s %s", r.Status, body)
	}
	if r, _, body := l.verdict(grace.Token, api.VerdictRequest{State: api.ReleaseNotQualified, Note: "after a revoke", Confirm: true}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("an M team member recording after a revoke: %s %s", r.Status, body)
	}
	last := l.mustVerdict(harnessToken, api.ReleaseNotQualified, "recorded again")

	// History keeps every verdict, newest first, with who, when and the note.
	h := last.Release.Verdicts
	want := []struct{ state, handle, note string }{
		{api.ReleaseNotQualified, harnessMaintainer, "recorded again"},
		{api.ReleaseRevoked, harnessMaintainer, "recorded against the wrong Release"},
		{api.ReleaseNotQualified, harnessMaintainer, "still dark on j314"},
		{api.ReleaseNotQualified, "grace", "the display stays dark"},
	}
	if len(h) != len(want) {
		t.Fatalf("the verdict history: %+v", h)
	}
	for i, w := range want {
		if h[i].State != w.state || h[i].Handle != w.handle || h[i].Note != w.note || h[i].Time.IsZero() || h[i].AccountID != testAccount(w.handle) {
			t.Errorf("history line %d: %+v, want %+v", i, h[i], w)
		}
	}
	if got := l.releases(linus.Token)[0]; got.State != api.ReleaseNotQualified {
		t.Fatalf("the list's state: %+v", got)
	}
}

func TestQualifiedNeedsAPassedInstalledRunOfThatRelease(t *testing.T) {
	broken := false
	l, msha, _ := installedLab(t, func(phase string, o map[string]any) {
		if broken && phase == installed.PhaseBooted {
			o["running"].(map[string]any)["uname_r"] = "7.0.0-not-the-release"
		}
	})
	grace := l.member("grace")

	r, _, body := l.verdict(grace.Token, api.VerdictRequest{State: api.ReleaseQualified, Note: "looks fine", Confirm: true})
	if r.StatusCode != http.StatusConflict || !strings.Contains(body, "passed Installed run") {
		t.Fatalf("qualified with no passed Installed run: %s %s", r.Status, body)
	}
	if got := l.release(grace.Token, "9.9"); got.State != api.ReleaseNoVerdict || len(got.Verdicts) != 0 {
		t.Fatalf("the refused verdict left a trace: %+v", got)
	}

	// Everything else is a warning in the sentence: "not qualified" while
	// every Configuration has a passed run.
	pass := l.run(api.JobSpec{InstalledManifest: msha, Holder: "tester"})
	l.expect(pass, api.OutcomePass)
	_, a, _ := l.verdict(grace.Token, api.VerdictRequest{State: api.ReleaseNotQualified, Note: "a hunch"})
	if len(a.Warnings) == 0 || !strings.Contains(a.Confirm, a.Warnings[0]) {
		t.Fatalf("not qualified over a full set of passed runs carries no warning: %+v", a)
	}
	done := l.mustVerdict(grace.Token, api.ReleaseQualified, "one passed run on the one Configuration")
	if done.Release.State != api.ReleaseQualified || done.Release.Passed != 1 || done.Release.Configurations != 1 {
		t.Fatalf("after qualified: %+v", done.Release)
	}
	mac := done.Release.Macs[0]
	if mac.Runs != 1 || mac.PassedRun != pass.ID || mac.LatestRun != pass.ID || mac.LatestOutcome != api.OutcomePass {
		t.Fatalf("the Release's Mac and its runs: %+v", mac)
	}

	// An Installed run that fails later adds a warning and changes nothing.
	l.waitDevice(func(d api.Device) bool { return d.State == api.StateReady })
	broken = true
	failed := l.run(api.JobSpec{InstalledManifest: msha, Holder: "tester"})
	if failed.Outcome == api.OutcomePass {
		t.Fatalf("the tampered run passed: %s", failed.Summary)
	}
	after := l.release(grace.Token, "9.9")
	if after.State != api.ReleaseQualified || len(after.Verdicts) != 1 {
		t.Fatalf("a failed run changed the verdict: %+v", after)
	}
	if len(after.Warnings) != 1 || !strings.Contains(after.Warnings[0], failed.ID) {
		t.Fatalf("a failed run after the verdict added no warning: %v", after.Warnings)
	}
	if m := after.Macs[0]; m.Runs != 2 || m.LatestRun != failed.ID || m.PassedRun != pass.ID {
		t.Fatalf("the Mac's runs: %+v", m)
	}
}

// heldAnswer asks as a caller through the tunnel would be answered: as a
// stream, whose last line holds the result or the error.
func (l *lab) heldAnswer(token, method, path string) (contentType string, last api.HeldLine, keepalives int) {
	l.t.Helper()
	req, _ := http.NewRequest(method, l.url+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", heldType)
	resp, err := l.client().Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return resp.Header.Get("Content-Type"), api.HeldLine{Status: resp.StatusCode, Error: string(b)}, 0
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			keepalives++
			continue
		}
		if err := json.Unmarshal(sc.Bytes(), &last); err != nil {
			l.t.Fatalf("a line of the stream: %q", sc.Text())
		}
	}
	return resp.Header.Get("Content-Type"), last, keepalives
}

func TestFreezeEntryReadsTheMacThroughTheIdentityCheckUnderTheCallersOwnLease(t *testing.T) {
	rec := &ioRecorder{}
	l, _, entry := installedLabWith(t, labConfig{wrap: rec.wrap}, nil)
	grace, linus := l.member("grace"), l.member("linus")
	l.freeLease()
	dir := l.s.frozenDir("9.9")
	for _, f := range []string{"installer-entry/sim-mac.json", "PINS.installer-entry.sim-mac"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/installed/9.9/entry/sim-mac"
	refused := func(who string, token string, saying string) {
		t.Helper()
		r, body := l.as(token, "POST", path, nil)
		if r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusConflict || !strings.Contains(body, saying) {
			t.Fatalf("%s: %s %s", who, r.Status, body)
		}
		if _, err := os.Stat(filepath.Join(dir, "installer-entry", "sim-mac.json")); err == nil {
			t.Fatalf("%s: the entry was frozen", who)
		}
	}
	refused("an M team member with no Lease", grace.Token, "Lease")
	refused("a Maintainer with no Lease", harnessToken, "Lease")
	l.leaseAs(linus, 0)
	refused("under another person's Lease", grace.Token, "Lease")
	refused("a Maintainer under another person's Lease", harnessToken, "Lease")
	if r, body := l.as(linus.Token, "DELETE", "/api/devices/sim-mac/lease", nil); r.StatusCode != 200 {
		t.Fatalf("linus releasing: %s %s", r.Status, body)
	}
	if r, body := l.as(grace.Token, "POST", "/api/installed/9.9/entry/no-such-mac", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("a Mac labd does not know: %s %s", r.Status, body)
	}
	sent := len(rec.commands())

	l.leaseAs(grace, 0)
	before := time.Now().Add(-time.Second)
	ctype, last, keepalives := l.heldAnswer(grace.Token, "POST", path)
	if !strings.HasPrefix(ctype, heldType) || keepalives == 0 || last.Error != "" || last.Result == nil {
		t.Fatalf("the freeze's answer: %s, %d Keepalives, %+v", ctype, keepalives, last)
	}
	var res struct {
		Entry       installed.InstallerEntry `json:"entry"`
		EntrySHA256 string                   `json:"entry_sha256"`
		FrozenBy    string                   `json:"frozen_by"`
	}
	if json.Unmarshal(last.Result, &res) != nil || res.Entry.Path != entry.Path || res.Entry.Release != "9.9" || res.FrozenBy != "grace" {
		t.Fatalf("the frozen entry: %s", last.Result)
	}
	eb, err := os.ReadFile(filepath.Join(dir, "installer-entry", "sim-mac.json"))
	if err != nil || installed.SHA256(eb) != res.EntrySHA256 {
		t.Fatalf("the entry in the frozen store: %v", err)
	}
	pins, _ := os.ReadFile(filepath.Join(dir, "PINS.installer-entry.sim-mac"))
	if !strings.Contains(string(pins), res.EntrySHA256+"  installer-entry/sim-mac.json") {
		t.Fatalf("the entry's pins: %s", pins)
	}

	// It read the Mac through the identity check, for grace, and sent no exec.
	var checks int
	for _, c := range rec.commands()[sent:] {
		switch c.Kind {
		case api.CmdExec:
			t.Errorf("the freeze sent the Mac an exec: %s", c.Args)
		case api.CmdRunTest:
			var ts api.TestSpec
			json.Unmarshal(c.Args, &ts)
			if ts.Identity == nil || ts.Identity.Phase != installed.PhaseBefore || ts.Script != "" || ts.Builtin != "" || c.Maintainer != "grace" {
				t.Errorf("the freeze sent test %+v as %q", ts, c.Maintainer)
			}
			checks++
		case api.CmdStage, api.CmdBootOnce, api.CmdReboot, api.CmdInstall:
			t.Errorf("the freeze sent the Mac %s", c.Kind)
		}
	}
	if checks != 1 {
		t.Fatalf("the freeze sent %d identity checks, want 1", checks)
	}
	if jobs, _ := l.s.store.jobs("sim-mac", 100, true); len(jobs) != 0 {
		t.Fatalf("the freeze queued a job: %+v", jobs[0])
	}

	if execs, err := l.s.store.execRecord(l.s.dev("sim-mac").snapshot().ID, 0, 100); err != nil || len(execs) != 0 {
		t.Fatalf("the freeze wrote to the Exec record: %+v (%v)", execs, err)
	}

	// Who froze the entry, and when, is recorded.
	mac := l.release(grace.Token, "9.9").Macs[0]
	if mac.EntryFrozenBy != "grace" || mac.EntryFrozenAt == nil || mac.EntryFrozenAt.Before(before) {
		t.Fatalf("who froze the entry: %+v", mac)
	}
	if r, body := l.as(grace.Token, "POST", path, nil); r.StatusCode != http.StatusConflict || !strings.Contains(body, "already frozen") {
		t.Fatalf("a second freeze: %s %s", r.Status, body)
	}
}

// A freeze is cut off when the Mac's observation is not one of the lab's
// own identity check: an upload under a made-up name reaches no job.
func TestOnlyAFreezeInProgressTakesAnObservationOutsideAJob(t *testing.T) {
	l, _, _ := installedLab(t, nil)
	req, _ := http.NewRequest("POST", l.url+"/api/agent/upload?job=freeze-0000&name=test-identity-before/observed.json", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+l.secret)
	resp, err := l.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an observation for no freeze in progress: %s", resp.Status)
	}
}

func TestAnMTeamMemberRunsAnInstalledRunUnderTheirOwnLease(t *testing.T) {
	l, msha, _ := installedLab(t, nil)
	grace, linus := l.member("grace"), l.member("linus")
	l.freeLease()
	spec := api.JobSpec{Device: "sim-mac", InstalledManifest: msha, Holder: "any-label"}

	if code, _, body := l.submitAs(grace.Token, spec); code != http.StatusForbidden || !strings.Contains(body, "Lease") {
		t.Fatalf("an Installed run with no Lease: %d %s", code, body)
	}
	if code, _, body := l.submitAs(harnessToken, spec); code != http.StatusConflict || !strings.Contains(body, "Lease") {
		t.Fatalf("a Maintainer's Installed run with no Lease: %d %s", code, body)
	}
	l.leaseAs(linus, 0)
	if code, _, body := l.submitAs(grace.Token, spec); code == 200 {
		t.Fatalf("an Installed run under another person's Lease: %s", body)
	}
	if r, body := l.as(linus.Token, "DELETE", "/api/devices/sim-mac/lease", nil); r.StatusCode != 200 {
		t.Fatalf("linus releasing: %s %s", r.Status, body)
	}
	// The Lease is the person's, whatever label the job names.
	l.leaseAs(grace, 0)
	j := l.jobAPI(l.mustSubmitAs(grace, spec), 120*time.Second)
	l.expect(j, api.OutcomePass)
	if j.AccountID != testAccount("grace") || j.Result.Installed == nil || j.Result.Installed.Release != "9.9" {
		t.Fatalf("grace's Installed run: %+v", j.Result.Installed)
	}
}

func TestPromotionNeedsThatMacsOwnPassedRunAndRevokingUndoesNothing(t *testing.T) {
	l, msha, _ := installedLab(t, nil)
	grace := l.member("grace")
	promote := func(token string, body map[string]any) (*http.Response, map[string]any, string) {
		t.Helper()
		r, raw := l.as(token, "POST", "/api/installed/9.9/promote", body)
		var out map[string]any
		json.Unmarshal([]byte(raw), &out)
		return r, out, raw
	}
	kg := l.s.dev("sim-mac").snapshot().KnownGood

	// A run that did not pass promotes nothing; nor does another Mac's.
	if r, _, raw := promote(harnessToken, map[string]any{"device": "sim-mac", "job": "j-none", "confirm": true}); r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusConflict {
		t.Fatalf("promoting with no run: %s %s", r.Status, raw)
	}
	j := l.run(api.JobSpec{InstalledManifest: msha, Holder: "tester"})
	l.expect(j, api.OutcomePass)
	ask := map[string]any{"device": "sim-mac", "job": j.ID}
	if r, _, raw := promote(grace.Token, ask); r.StatusCode != http.StatusForbidden {
		t.Fatalf("an M team member promoting: %s %s", r.Status, raw)
	}

	// It does not wait for the verdict, and the sentence says where the
	// Release stands.
	r, out, raw := promote(harnessToken, ask)
	if s, _ := out["confirm"].(string); r.StatusCode != 200 || !strings.Contains(s, "sim-mac") || !strings.Contains(s, "no verdict") {
		t.Fatalf("the sentence for a Release with no verdict: %s %s", r.Status, raw)
	}
	l.mustVerdict(grace.Token, api.ReleaseNotQualified, "the fan is loud")
	_, out, raw = promote(harnessToken, ask)
	if s, _ := out["confirm"].(string); !strings.Contains(s, "not qualified") {
		t.Fatalf("the sentence for a Release not qualified: %s", raw)
	}
	if got := l.s.dev("sim-mac").snapshot().KnownGood; got != kg {
		t.Fatalf("an unconfirmed Promotion moved the known-good to %s", got)
	}
	ask["confirm"] = true
	if r, _, raw := promote(harnessToken, ask); r.StatusCode != 200 || l.s.dev("sim-mac").snapshot().KnownGood != tKrel {
		t.Fatalf("the Promotion: %s %s", r.Status, raw)
	}
	got := l.release(grace.Token, "9.9")
	p := got.Macs[0].Promoted
	if got.Promoted != 1 || p == nil || p.Handle != harnessMaintainer || p.Job != j.ID || p.Kernel != tKrel || p.Time.IsZero() || p.Mark != "" {
		t.Fatalf("who promoted: %+v", p)
	}

	// Revoking undoes nothing: the Mac stays promoted, and is marked.
	_, a, _ := l.verdict(harnessToken, api.VerdictRequest{State: api.ReleaseRevoked, Note: "the fan"})
	if !strings.Contains(a.Confirm, "stay promoted") {
		t.Fatalf("the revoke's sentence says nothing of the promoted Mac: %+v", a)
	}
	l.mustVerdict(harnessToken, api.ReleaseRevoked, "the fan")
	got = l.release(grace.Token, "9.9")
	if got.State != api.ReleaseRevoked || got.Promoted != 1 || got.Macs[0].Promoted == nil || got.Macs[0].Promoted.Mark != "promoted from a revoked Release" {
		t.Fatalf("after the revoke: state %s, %+v", got.State, got.Macs[0].Promoted)
	}
	if l.s.dev("sim-mac").snapshot().KnownGood != tKrel {
		t.Fatal("revoking moved the Mac's known-good")
	}
	if release, revoked := l.s.promotedFrom(l.s.dev("sim-mac")); release != "9.9" || !revoked {
		t.Fatalf("the Mac's own mark: %q %v", release, revoked)
	}
	_, out, raw = promote(harnessToken, map[string]any{"device": "sim-mac", "job": j.ID})
	if s, _ := out["confirm"].(string); !strings.Contains(s, "revoked") && !strings.Contains(raw, "already promoted") {
		t.Fatalf("the sentence for a revoked Release: %s", raw)
	}
}

// A labd that starts over a frozen store with no Release record lists what
// is frozen there: the old lab's Releases come along.
func TestAFrozenManifestWithNoRecordIsListedAtStart(t *testing.T) {
	l, msha, _ := installedLab(t, nil)
	if _, err := l.s.store.db.Exec(`DELETE FROM releases`); err != nil {
		t.Fatal(err)
	}
	if got := l.releases(harnessToken); len(got) != 0 {
		t.Fatalf("Releases after the record was dropped: %+v", got)
	}
	l.s.adoptReleases()
	got := l.releases(harnessToken)
	if len(got) != 1 || got[0].Name != "9.9" || got[0].ManifestSHA256 != msha || got[0].FrozenBy != "" || got[0].FrozenAt.IsZero() || len(got[0].Macs) != 1 {
		t.Fatalf("the adopted Release: %+v", got)
	}
}

// labd's own code never names the Install operation: no Install operation
// runs anywhere.
func TestLabdSendsNoInstallOperation(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(src, []byte("CmdInstall")) {
			t.Errorf("%s can send a Mac the Install operation", f)
		}
	}
}
