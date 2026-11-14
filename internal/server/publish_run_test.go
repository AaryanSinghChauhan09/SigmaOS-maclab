package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/reports"
)

// The lab publishing by itself and at a job's own asking, with the runner
// and a simulated Mac whose omarchy-m-test signs real reports.

// publishingLab is a lab that takes reports and publishes by itself, whose
// Mac runs omarchy-m-test.
func publishingLab(t *testing.T, c labConfig) *lab {
	t.Helper()
	c.reports = true
	l := newLabWith(t, c)
	l.waitDevice(func(d api.Device) bool { return d.Facts.AgentVersion != "" })
	return l
}

// publishingWindowLab is publishingLab with a Volunteer's Mac, open for two
// hours, that passed its baseline.
func publishingWindowLab(t *testing.T) (*lab, *agent.FakeClock) {
	t.Helper()
	return windowEndLab(t, labConfig{reports: true})
}

func (l *lab) labReports() []*reports.Run {
	l.t.Helper()
	every, err := l.s.reports.Every(l.s.catalogue)
	if err != nil {
		l.t.Fatal(err)
	}
	return every.Runs()
}

// byLab is the report the lab published from the job; nil for none.
func (l *lab) byLab(job string) *reports.Report {
	l.t.Helper()
	for _, run := range l.labReports() {
		if run.LabJob == job {
			return run.Report
		}
	}
	return nil
}

func said(j *api.Job, part string) bool {
	for _, e := range j.Events {
		if strings.Contains(e.Msg, part) {
			return true
		}
	}
	return false
}

// refreshedAgo has the Mac's last refresh the lab published by itself be
// one queued that long ago.
func (l *lab) refreshedAgo(d time.Duration) {
	v := l.s.dev(l.name)
	v.mu.Lock()
	v.d.RefreshPublished = l.s.now().Add(-d)
	v.mu.Unlock()
}

// By itself the lab publishes only a run that became the Mac's Reference:
// each baseline, and a refresh once a week.
func TestByItselfTheLabPublishesOnlyARunThatBecameTheReference(t *testing.T) {
	l := publishingLab(t, labConfig{oob: true})

	// Each baseline.
	l.baseline()
	jobs, _ := l.s.store.jobs(l.name, 1, false)
	base := jobs[0]
	first := l.byLab(base.ID)
	if o := base.Result.OMT; o == nil || o.Reference != "became the reference" || first == nil || o.ReportID != first.PublicID || !strings.HasSuffix(o.Published, "/reports/"+first.PublicID) ||
		l.s.reportState(base) != api.ReportPublished || !said(base, "omarchy-m-test report published: ") {
		t.Fatalf("the baseline was not published by itself: %+v, report %+v", base.Result.OMT, first)
	}
	if first.AskedHandle != "" || first.AskedAccountID != 0 || first.MachineID != machineIDOf(t, l.name) {
		t.Errorf("the baseline's report: asked by %q, machine id %q", first.AskedHandle, first.MachineID)
	}
	if got := l.s.dev(l.name).snapshot().MachineID; got != first.MachineID {
		t.Errorf("the Mac's lab record has the machine id %q", got)
	}

	// Another Known-good run that became the Reference is nobody's baseline
	// and no refresh: it waits for a person.
	j := l.run(api.JobSpec{})
	l.expect(j, api.OutcomePass)
	if o := j.Result.OMT; o == nil || o.Reference != "became the reference" || o.ReportID != "" || o.PublishNote != "" || l.byLab(j.ID) != nil {
		t.Fatalf("a Known-good run nobody asked to publish: %+v", j.Result.OMT)
	}

	// The scheduled refresh, the job labd's scheduler queues.
	refresh := l.run(omtRefresh(l.name))
	l.expect(refresh, api.OutcomePass)
	report := l.byLab(refresh.ID)
	if o := refresh.Result.OMT; o.Reference != "became the reference" || report == nil || report.AskedHandle != "" {
		t.Fatalf("the refresh was not published by itself: %+v", refresh.Result.OMT)
	}
	if got := l.s.dev(l.name).snapshot().RefreshPublished; !got.Equal(refresh.Created) {
		t.Errorf("the Mac's last published refresh is %s, want %s", got, refresh.Created)
	}

	// Once a week: a refresh that comes sooner becomes the Reference and stays unpublished.
	soon := l.run(omtRefresh(l.name))
	l.expect(soon, api.OutcomePass)
	if o := soon.Result.OMT; o.Reference != "became the reference" || o.ReportID != "" || l.byLab(soon.ID) != nil ||
		!strings.Contains(o.PublishNote, "by itself the lab publishes one refresh a week") || !said(soon, "omarchy-m-test report not published: ") {
		t.Fatalf("a second refresh in a week: %+v", soon.Result.OMT)
	}
	l.refreshedAgo(6*24*time.Hour + 23*time.Hour)
	if soon = l.run(omtRefresh(l.name)); l.byLab(soon.ID) != nil {
		t.Fatal("a refresh six days after the last was published")
	}
	l.refreshedAgo(7*24*time.Hour + time.Minute)
	if week := l.run(omtRefresh(l.name)); l.byLab(week.ID) == nil {
		t.Fatalf("a refresh a week after the last was not published: %+v", week.Result.OMT)
	}

	// A refresh that did not become the Reference is not published, a week on or not.
	l.refreshedAgo(30 * 24 * time.Hour)
	worse := func(b map[string]any) { c := check(b, "boot.encryption"); c["status"] = "fail" }
	l.changeReport.Store(&worse)
	kept := l.run(omtRefresh(l.name))
	l.changeReport.Store(nil)
	if o := kept.Result.OMT; o == nil || strings.Contains(o.Reference, "became") || !strings.HasPrefix(o.Reference, "kept ") || o.ReportID != "" || l.byLab(kept.ID) != nil ||
		!strings.Contains(o.PublishNote, "only a run that became the Mac's Reference") {
		t.Fatalf("a refresh that regressed: %+v", kept.Result.OMT)
	}
	if n := len(l.labReports()); n != 3 {
		t.Errorf("%d reports published, want the baseline and two refreshes", n)
	}
	// labd's scheduler asks for no publishing: the rule is the runner's.
	if spec := omtRefresh(l.name); spec.Publish || spec.Holder != omtHolder || spec.Kernel != "" {
		t.Errorf("the scheduler queues %+v", spec)
	}
}

// By hand: `run --publish` publishes a Known-good run for whoever asked,
// and never a test-kernel run.
func TestRunPublishPublishesAKnownGoodRunForWhoeverAsked(t *testing.T) {
	l := publishingLab(t, labConfig{oob: true, config: func(c *Config) { c.OMTPublish = false }})
	l.baseline()
	if n := len(l.labReports()); n != 0 {
		t.Fatalf("a lab that publishes nothing by itself published %d baselines", n)
	}
	submit := func(spec api.JobSpec) *api.Job {
		t.Helper()
		spec.Device = l.name
		r, body := l.post("/api/jobs", spec)
		var j api.Job
		if r.StatusCode != 200 || json.Unmarshal([]byte(body), &j) != nil {
			t.Fatalf("job: %s %s", r.Status, body)
		}
		return l.jobAPI(j.ID, 90*time.Second)
	}

	j := submit(api.JobSpec{Publish: true})
	l.expect(j, api.OutcomePass)
	report := l.byLab(j.ID)
	if o := j.Result.OMT; report == nil || o.ReportState != api.ReportPublished || o.ReportID != report.PublicID {
		t.Fatalf("run --publish on the known-good kernel: %+v", j.Result.OMT)
	}
	if report.AskedHandle != harnessMaintainer || report.AskedAccountID != testAccount(harnessMaintainer) {
		t.Errorf("who asked: %q %d", report.AskedHandle, report.AskedAccountID)
	}

	test := submit(api.JobSpec{Kernel: l.artifact("krel=7.2-test behavior=ok"), Publish: true})
	if o := test.Result.OMT; o == nil || o.KnownGood || o.ReportState != api.ReportNotPublished || l.byLab(test.ID) != nil || !strings.Contains(o.PublishNote, "only a Known-good run is published") {
		t.Fatalf("run --publish on a test kernel: %s, %+v", test.Outcome, test.Result.OMT)
	}

	// Asked for on a job labd queued itself, it is nobody's asking.
	own := l.run(api.JobSpec{Publish: true})
	if o := own.Result.OMT; l.byLab(own.ID) != nil || !strings.Contains(o.PublishNote, "publishes no report by itself") {
		t.Fatalf("a job of labd's own asking to be published: %+v", o)
	}
	if n := len(l.labReports()); n != 1 {
		t.Errorf("%d reports published, want 1", n)
	}
}

// With Preview on, the lab publishes nothing: not the baseline by itself.
func TestInPreviewABaselineIsNotPublished(t *testing.T) {
	l := publishingLab(t, labConfig{oob: true, config: func(c *Config) { c.Preview = true }})
	l.baseline()
	jobs, _ := l.s.store.jobs(l.name, 1, false)
	if o := jobs[0].Result.OMT; o == nil || o.Reference != "became the reference" || o.ReportID != "" || !strings.Contains(o.PublishNote, "not open yet and publishes no report") {
		t.Fatalf("a baseline in Preview: %+v", o)
	}
	if n := len(l.labReports()); n != 0 {
		t.Fatalf("%d reports published in Preview", n)
	}
}
