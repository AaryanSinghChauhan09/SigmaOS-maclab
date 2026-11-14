package reports

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"
)

// The lab publishing a job's report: the way in from inside labd.

var byItself = Lab{Job: "j-refresh"}

func (d *desk) fromLab(t testing.TB, report any, machine string, lab Lab) (*Stored, *Refusal) {
	t.Helper()
	stored, refusal, err := d.uploads.ReceiveFromLab(sent(t, report, machine), site+"/install", lab)
	if err != nil {
		t.Fatal(err)
	}
	return stored, refusal
}

func TestALabRunsReportPassesTheSameChecksAsAnyUpload(t *testing.T) {
	d := newDesk(t)
	good := run(t, 1)
	unsigned := encode(t, good.without("signature"))
	forged := encode(t, put(sign(t, run(t, 2), "a"), "j000", "machine", "board"))
	for _, c := range []struct {
		name   string
		report any
		status int
	}{
		{"not JSON", "not json", http.StatusBadRequest},
		{"the schema", put(run(t, 3), "x86_64", "machine", "arch"), http.StatusUnprocessableEntity},
		{"no signature", unsigned, http.StatusUnprocessableEntity},
		{"a signature of another text", forged, http.StatusUnprocessableEntity},
		{"a check id the catalogue lacks", put(run(t, 4), "wifi.made-up", "checks", 0, "id"), http.StatusUnprocessableEntity},
		{"the evidence", put(run(t, 5), []any{"ssid: " + string(bytes.Repeat([]byte("x"), 70<<10))}, "checks", 0, "evidence"), http.StatusUnprocessableEntity},
	} {
		// The same file sent by the Mac's own tool gets the same answer.
		outside := d.post(t, c.report, "a", "10.9.9.9")
		stored, refusal := d.fromLab(t, c.report, "a", byItself)
		if stored != nil || refusal == nil || refusal.Status != c.status || outside.status != c.status || outside.text("error") != refusal.Error {
			t.Errorf("%s: the lab's own got %+v, the upload %d %q, want %d for both", c.name, refusal, outside.status, outside.text("error"), c.status)
		}
	}
	if d.count(t) != 0 {
		t.Fatalf("%d refused reports were stored", d.count(t))
	}
	stored, refusal := d.fromLab(t, good, "a", byItself)
	if refusal != nil || stored == nil || stored.PublicID == "" || stored.DeletionToken == "" {
		t.Fatalf("a good report: %+v %+v", stored, refusal)
	}
}

func TestTheOneSignatureOnALabRunIsItsMacsMachineKey(t *testing.T) {
	d := newDesk(t)
	stored, refusal := d.fromLab(t, run(t, 1), "a", byItself)
	if refusal != nil {
		t.Fatalf("%+v", refusal)
	}
	report, err := d.store.Report(stored.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if want := testRules(t).MachineID(testKeyBlob("a")); report.MachineID != want || stored.MachineID != want {
		t.Errorf("machine id %q and %q, want the one of the Mac's Machine key %q", report.MachineID, stored.MachineID, want)
	}
	// What is stored is what the Mac signed and nothing the lab added.
	want := encode(t, run(t, 1).without("signature"))
	if !bytes.Equal(report.Body, bytes.TrimSuffix(want, []byte("\n"))) && !bytes.Equal(report.Body, want) {
		t.Errorf("the stored body is not the text the Mac signed:\n%s", report.Body)
	}
	if regexp.MustCompile(`"signature"|j-refresh`).Match(report.Body) {
		t.Error("the stored body carries a signature or the lab's job")
	}
}

func TestTheLabsOwnPublishingIsNotCountedPerAddressAndTenAnHourPerMacHolds(t *testing.T) {
	d := newDesk(t)
	// More Macs in an hour than any one address may send.
	for n := range UploadsPerHour + 5 {
		if _, refusal := d.fromLab(t, golden(t, "m2-max-image2"), fmt.Sprintf("lab%d", n), byItself); refusal != nil {
			t.Fatalf("the lab's report %d: %+v", n, refusal)
		}
	}
	// And no address lost an upload to them.
	for n := range UploadsPerHour {
		if a := d.post(t, golden(t, "m2-max-image2"), fmt.Sprintf("m%d", n), "10.0.0.1"); a.status != http.StatusCreated {
			t.Fatalf("upload %d from an address: %d %v", n, a.status, a.body)
		}
	}
	// One Mac: ten an hour, the lab's and its own tool's counted together.
	for n := range UploadsPerHourPerMachine - 1 {
		if _, refusal := d.fromLab(t, run(t, n), "one", byItself); refusal != nil {
			t.Fatalf("report %d of one Mac: %+v", n, refusal)
		}
	}
	if a := d.post(t, run(t, 50), "one", "10.0.0.2"); a.status != http.StatusCreated {
		t.Fatalf("the Mac's own tenth: %d %v", a.status, a.body)
	}
	_, refusal := d.fromLab(t, run(t, 51), "one", byItself)
	if refusal == nil || refusal.Status != http.StatusTooManyRequests || refusal.Error != "Too many uploads from this Mac: at most 10 an hour. Try again later; the report is saved on your Mac." {
		t.Errorf("the lab's eleventh report of one Mac in an hour: %+v", refusal)
	}
	d.now = d.now.Add(time.Hour)
	if _, refusal := d.fromLab(t, run(t, 51), "one", byItself); refusal != nil {
		t.Errorf("an hour later: %+v", refusal)
	}
}

func TestAnUploadClaimingToBeTheLabsIsCountedAndTreatedAsAnyones(t *testing.T) {
	d := newDesk(t)
	claim := func(n int, machine string) answer {
		query := url.Values{"lab_job": {"j1"}, "job": {"j1"}, "lab": {"1"}, "asked_handle": {"ada"}}
		r := httptest.NewRequest("POST", site+"/api/v1/reports?"+query.Encode(), bytes.NewReader(sent(t, run(t, n), machine)))
		r.Header.Set("Content-Type", "application/json")
		for _, name := range []string{"X-Lab-Job", "X-Lab", "X-Omarchy-Mac-Lab-Job", "Lab-Job"} {
			r.Header.Set(name, "j1")
		}
		r.Header.Set("User-Agent", "labd")
		return d.serve(t, r, "10.0.0.1")
	}
	for n := range UploadsPerHour {
		a := claim(n, fmt.Sprintf("m%d", n))
		if a.status != http.StatusCreated {
			t.Fatalf("upload %d: %d %v", n, a.status, a.body)
		}
		report, err := d.store.Report(a.text("id"))
		if err != nil {
			t.Fatal(err)
		}
		if report.ByLab() || report.LabJob != "" || report.AskedHandle != "" || report.AskedAccountID != 0 {
			t.Fatalf("an upload from outside was recorded as the lab's: %+v", report)
		}
	}
	if a := claim(99, "another"); a.status != http.StatusTooManyRequests {
		t.Errorf("the claim was not counted against its address: %d %v", a.status, a.body)
	}
	// Said in the report itself, it is no report.
	claimed := put(run(t, 100), "j1", "lab_job")
	if a := d.post(t, claimed, "a", "10.0.0.3"); a.status != http.StatusUnprocessableEntity {
		t.Errorf("a report naming a lab job: %d %v", a.status, a.body)
	}
}

func TestHowAReportArrivedIsRecordedWithTheJobAndWhoAsked(t *testing.T) {
	d := newDesk(t)
	tool := d.post(t, run(t, 1), "a")
	itself, _ := d.fromLab(t, run(t, 2), "a", Lab{Job: "j-baseline"})
	asked, _ := d.fromLab(t, run(t, 3), "a", Lab{Job: "j-by-hand", Handle: "edith", AccountID: 7})
	for _, c := range []struct {
		id, job, handle string
		account         int64
	}{{tool.text("id"), "", "", 0}, {itself.PublicID, "j-baseline", "", 0}, {asked.PublicID, "j-by-hand", "edith", 7}} {
		report, err := d.store.Report(c.id)
		if err != nil {
			t.Fatal(err)
		}
		if report.ByLab() != (c.job != "") || report.LabJob != c.job || report.AskedHandle != c.handle || report.AskedAccountID != c.account {
			t.Errorf("report of job %q: by the lab %v, job %q, asked by %q %d", c.job, report.ByLab(), report.LabJob, report.AskedHandle, report.AskedAccountID)
		}
	}
	// It is the job that makes a report the lab's: without one nothing comes in.
	if stored, refusal, err := d.uploads.ReceiveFromLab(sent(t, run(t, 4), "a"), site+"/install", Lab{Handle: "edith", AccountID: 7}); err == nil || stored != nil || refusal == nil {
		t.Errorf("a report from the lab with no job: %+v %+v %v", stored, refusal, err)
	}
	if d.count(t) != 3 {
		t.Errorf("%d reports stored, want 3", d.count(t))
	}
	// Every list reads it too.
	every, err := d.store.Every(testRules(t).Catalogue())
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]bool{}
	for _, run := range every.Runs() {
		jobs[run.LabJob] = true
	}
	if !jobs[""] || !jobs["j-baseline"] || !jobs["j-by-hand"] {
		t.Errorf("the Maintainers' list reads the jobs %v", jobs)
	}
}

func TestARepeatOfALabRunIsAlreadyUploadedWithTheReportsLink(t *testing.T) {
	d := newDesk(t)
	first, _ := d.fromLab(t, run(t, 1), "a", byItself)
	for name, again := range map[string]func() *Refusal{
		"the lab again": func() *Refusal { _, r := d.fromLab(t, run(t, 1), "a", Lab{Job: "j-other"}); return r },
		"the Mac's own tool": func() *Refusal {
			a := d.post(t, run(t, 1), "a")
			if a.text("deletion_url") != "" || a.text("report_url") != site+"/reports/"+first.PublicID {
				t.Errorf("the tool's repeat: %v", a.body)
			}
			return &Refusal{Status: a.status, Error: a.text("error"), Held: first.PublicID}
		},
	} {
		if r := again(); r == nil || r.Status != http.StatusConflict || r.Error != "Already uploaded" || r.Held != first.PublicID {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// What the Mac's own tool uploaded first, the lab does not store again.
	own := d.post(t, run(t, 2), "a")
	if _, r := d.fromLab(t, run(t, 2), "a", byItself); r == nil || r.Status != http.StatusConflict || r.Held != own.text("id") {
		t.Errorf("the lab after the tool: %+v", r)
	}
	// A deleted report's digest stays.
	if err := d.store.Delete(first.PublicID); err != nil {
		t.Fatal(err)
	}
	_, r := d.fromLab(t, run(t, 1), "a", byItself)
	if r == nil || r.Status != http.StatusConflict || r.Error != "Already uploaded" || r.Held != "" || len(r.Details) != 1 {
		t.Errorf("a deleted report published again: %+v", r)
	}
	if d.count(t) != 1 {
		t.Errorf("%d reports stored, want 1", d.count(t))
	}
}

func TestALabRunIsVerifiedOrCommunityByItsMacsSignInAtUpload(t *testing.T) {
	d := newDesk(t)
	d.uploads.OnMTeam = func(account int64) bool { return account == 4242 }
	rules, at := testRules(t), d.now.UnixMicro()
	for machine, account := range map[string]int64{"member": 4242, "outsider": 77} {
		if _, err := d.store.db.Exec(`INSERT INTO mac_sign_ins (machine_id, account_id, handle, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			rules.MachineID(testKeyBlob(machine)), account, machine, at, at); err != nil {
			t.Fatal(err)
		}
	}
	for machine, verified := range map[string]bool{"member": true, "outsider": false, "nobody": false} {
		// Whoever asked for it to be published changes nothing.
		stored, refusal := d.fromLab(t, run(t, 1), machine, Lab{Job: "j1", Handle: "member", AccountID: 4242})
		if refusal != nil {
			t.Fatalf("%s: %+v", machine, refusal)
		}
		report, _ := d.store.Report(stored.PublicID)
		if stored.Verified != verified || report.Verified != verified {
			t.Errorf("a lab run on a Mac signed in as %s: Verified %v, want %v", machine, report.Verified, verified)
		}
		if machine != "nobody" && report.Handle != machine {
			t.Errorf("the report of %s is stamped %q", machine, report.Handle)
		}
	}
}

func TestNoAggregateTreatsALabRunDifferently(t *testing.T) {
	rules := testRules(t)
	tool, lab := newDesk(t), newDesk(t)
	for _, d := range []*desk{tool, lab} {
		d.uploads.OnMTeam = func(int64) bool { return true }
		at := d.now.UnixMicro()
		if _, err := d.store.db.Exec(`INSERT INTO mac_sign_ins (machine_id, account_id, handle, created_at, updated_at) VALUES (?, 1, 'ada', ?, ?)`,
			rules.MachineID(testKeyBlob("a")), at, at); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]string{}
	for n, name := range []string{"m2-max-image2", "m1-pro-converged", "m2-max-converged", "m1-pro-mx-mac"} {
		machine := "a"
		if n%2 == 1 {
			machine = "b"
		}
		a := tool.post(t, golden(t, name), machine, fmt.Sprintf("10.0.0.%d", n))
		stored, refusal := lab.fromLab(t, golden(t, name), machine, Lab{Job: fmt.Sprintf("j%d", n), Handle: "edith", AccountID: 7})
		if a.status != http.StatusCreated || refusal != nil {
			t.Fatalf("%s: %d %v, %+v", name, a.status, a.body, refusal)
		}
		ids[stored.PublicID] = a.text("id")
		tool.now, lab.now = tool.now.Add(time.Minute), lab.now.Add(time.Minute)
	}
	at := tool.now
	said := func(d *desk, same bool) map[string][]byte {
		p, err := d.store.Published(rules.Catalogue())
		if err != nil {
			t.Fatal(err)
		}
		none := p.Filter(url.Values{}, ListKeys)
		out := map[string][]byte{
			"reports.json":       p.ReportsJSON(none, func(run *Run) string { return site + "/reports/" + run.PublicID }, at),
			"matrix by model":    p.MatrixJSON("model", none, at),
			"matrix by stack":    p.MatrixJSON("stack", none, at),
			"verified runs only": p.ReportsJSON(p.Filter(url.Values{"runs": {"tester"}}, ListKeys), func(*Run) string { return "" }, at),
		}
		if same {
			for name, text := range out {
				for labs, tools := range ids {
					text = bytes.ReplaceAll(text, []byte(labs), []byte(tools))
				}
				out[name] = text
			}
		}
		return out
	}
	want, got := said(tool, false), said(lab, true)
	for name := range want {
		if len(want[name]) < 100 {
			t.Fatalf("%s says nothing: %s", name, want[name])
		}
		if !bytes.Equal(want[name], got[name]) {
			t.Errorf("%s differs when the same reports came from the lab", name)
		}
		if bytes.Contains(got[name], []byte("edith")) || regexp.MustCompile(`"(lab_job|asked[a-z_]*)"`).Match(got[name]) {
			t.Errorf("%s says how a report arrived", name)
		}
	}
}
