package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
)

// lab job and lab jobs say what became of a job's report in one of four
// words: not published, published, hidden or deleted.
func TestJobAndJobsSayTheReportsStateInOneOfFourWords(t *testing.T) {
	at := time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)
	job := func(id, state string, change func(*api.OMTResult)) *api.Job {
		j := &api.Job{ID: id, Spec: api.JobSpec{Device: "m2-max"}, State: api.JobDone, Outcome: api.OutcomePass, Created: at, Summary: "booted and healthy"}
		if state != "" {
			j.Result.OMT = &api.OMTResult{Tool: "0.2.0", Kernel: "7.1.12-2-2-ARCH", KnownGood: true, Pass: 40, ComparedTo: "nothing", ReportState: state}
			if change != nil {
				change(j.Result.OMT)
			}
		}
		return j
	}
	jobs := []*api.Job{
		job("j-not", api.ReportNotPublished, func(o *api.OMTResult) { o.PublishNote = "publishing is off on m2-max, a Volunteer's Mac" }),
		job("j-pub", api.ReportPublished, func(o *api.OMTResult) { o.Published, o.ReportID = "https://lab.omarchy-mac.org/reports/abc", "abc" }),
		job("j-hid", api.ReportHidden, func(o *api.OMTResult) { o.Published, o.ReportID = "https://lab.omarchy-mac.org/reports/def", "def" }),
		job("j-del", api.ReportIsDeleted, func(o *api.OMTResult) {
			o.Published, o.ReportID = "https://lab.omarchy-mac.org/reports/ghi", "ghi"
			o.ReportDeleted = &api.ReportDeleted{By: "@ravi", Time: at.Add(48 * time.Hour)}
		}),
		job("j-none", "", nil),
	}
	labd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/jobs" {
			json.NewEncoder(w).Encode(jobs)
			return
		}
		for _, j := range jobs {
			if r.URL.Path == "/api/jobs/"+j.ID {
				json.NewEncoder(w).Encode(j)
				return
			}
		}
		http.NotFound(w, r)
	}))
	defer labd.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MACLAB_URL", labd.URL)
	t.Setenv("MACLAB_TOKEN", "tok")
	c, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}

	list, err := runLab(t, c, "jobs")
	if err != nil {
		t.Fatal(err)
	}
	if head := strings.SplitN(list, "\n", 2)[0]; !regexp.MustCompile(`^JOB +DEVICE +STATE +OUTCOME +REPORT +CREATED +SUMMARY$`).MatchString(head) {
		t.Errorf("lab jobs is headed %q", head)
	}
	for id, want := range map[string]string{"j-not": "not published", "j-pub": "published", "j-hid": "hidden", "j-del": "deleted", "j-none": "-"} {
		row := regexp.MustCompile(`(?m)^` + id + ` +m2-max +done +pass +(.+?) +11-14 09:30 +booted and healthy$`).FindStringSubmatch(list)
		if row == nil || row[1] != want {
			t.Errorf("lab jobs says %q of %s's report, want %q:\n%s", row, id, want, list)
		}
	}

	for id, want := range map[string]string{
		"j-not": "    report: not published (publishing is off on m2-max, a Volunteer's Mac)\n",
		"j-pub": "    report: published https://lab.omarchy-mac.org/reports/abc\n",
		"j-hid": "    report: hidden\n",
		"j-del": "    report: deleted by @ravi on " + at.Add(48*time.Hour).Local().Format("2006-01-02 15:04") + "\n",
	} {
		out, err := runLab(t, c, "job", id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, want) || strings.Count(out, "report: ") != 1 {
			t.Errorf("lab job %s lacks %q:\n%s", id, want, out)
		}
	}
	if out, _ := runLab(t, c, "job", "j-none"); strings.Contains(out, "report:") {
		t.Errorf("a job with no report says something of one:\n%s", out)
	}
	// A lab older than the four words: what the job itself carries.
	jobs[1].Result.OMT.ReportState = ""
	if out, _ := runLab(t, c, "job", "j-pub"); !strings.Contains(out, "    report: published https://lab.omarchy-mac.org/reports/abc\n") {
		t.Errorf("a job from a lab that says no state:\n%s", out)
	}
}
