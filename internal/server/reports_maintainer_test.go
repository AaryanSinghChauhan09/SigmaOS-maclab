package server

import (
	"net/http"
	"strings"
	"testing"
)

// Reports for Maintainers, translated from the old site's admin_test.rb:
// what its admin did to reports, a Maintainer does on Reports. Hide and Show
// are Maintainers'; Delete is a Maintainer's or the link holder's; hidden
// reports and who uploaded each show to Maintainers alone; every action
// writes a record line. The old admin session and its token are gone: the
// sign-in cases are slice 08's.

// reportState is "hidden", "published" or "" for a report that is gone.
func (l *issuesLab) reportState(id string) string {
	l.t.Helper()
	report, err := l.s.reports.Report(id)
	switch {
	case err != nil:
		return ""
	case report.Hidden:
		return "hidden"
	}
	return "published"
}

func TestAMaintainerListsEveryReportAndHidesOneWhichLeavesThePublicPagesAndTheMatrix(t *testing.T) {
	l := newIssuesLab(t)
	m2 := l.add(goldenDoc(t, "m2-max-image2"), "a")
	other := l.add(goldenDoc(t, "m2-max-image2"), "b")

	list := l.seen("/reports")
	if got := list.count(t, "#all-reports tr.admin-row"); got != 2 {
		t.Fatalf("the Maintainer's list has %d rows, want 2", got)
	}
	if !list.hasText(t, `tr[id="admin-report-`+m2+`"] .state .badge`, "published") {
		t.Error("a published report's state")
	}
	if list.count(t, `tr[id="admin-report-`+m2+`"] form[action="/reports/`+m2+`/hide"] button`) != 1 ||
		list.count(t, `tr[id="admin-report-`+m2+`"] form[action="/reports/`+m2+`/delete"] button[data-sentence]`) != 1 ||
		list.count(t, `tr[id="admin-report-`+m2+`"] form[action="/reports/`+m2+`/show"]`) != 0 {
		t.Error("a published report's row carries Hide and Delete, and no Show")
	}
	if l.page("/matrix").count(t, `td[data-feature="gpu"][data-state="works"]`) == 0 {
		t.Fatal("two machines that agree do not colour the matrix")
	}

	w := l.maintainer.post("/reports/" + m2 + "/hide")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/reports#admin-report-"+m2 {
		t.Fatalf("Hide: %d to %q", w.Code, w.Header().Get("Location"))
	}
	if l.reportState(m2) != "hidden" || l.reportState(other) != "published" {
		t.Fatalf("after Hide: %s, %s", l.reportState(m2), l.reportState(other))
	}
	list = l.seen("/reports")
	row := list.sole(t, `tr[id="admin-report-`+m2+`"]`)
	if !row.hasText(t, ".state .badge", "hidden") || row.attr("data-state") != "hidden" {
		t.Error("the hidden report's state in the Maintainer's list")
	}
	if row.count(t, `form[action="/reports/`+m2+`/show"] button`) != 1 || row.count(t, `form[action="/reports/`+m2+`/hide"]`) != 0 {
		t.Error("a Hidden report's row carries Show, and no Hide")
	}
	if got := list.count(t, "#all-reports tr.admin-row"); got != 2 {
		t.Errorf("the Maintainer's list has %d rows after hiding, want 2", got)
	}

	// The Maintainer still opens it, and is told it is hidden.
	page := l.seen("/reports/" + m2)
	if !page.hasText(t, ".report-badges #hidden", "hidden by a Maintainer") {
		t.Error("the Maintainer's view of a Hidden report does not say so")
	}

	// Nobody else does, and nothing public counts it.
	if got := l.status("/reports/" + m2); got != http.StatusNotFound {
		t.Errorf("a Hidden report's page, signed out: %d", got)
	}
	if got := l.page("/reports").count(t, "tr.report-row"); got != 1 {
		t.Errorf("the public list has %d rows, want 1", got)
	}
	if l.page("/matrix").count(t, `td[data-feature="gpu"][data-state="unconfirmed"]`) == 0 {
		t.Error("the matrix still counts a Hidden report")
	}
	for _, export := range []string{"/api/v1/reports.json", "/api/v1/reports.csv", "/api/v1/checks.csv"} {
		if body := l.do("GET", export, "").Body.String(); strings.Contains(body, m2) || !strings.Contains(body, other) {
			t.Errorf("%s: the Hidden report is exported, or the published one is not", export)
		}
	}
	// The Maintainer's own public list is the public's.
	if got := list.count(t, "tr.report-row"); got != 1 {
		t.Errorf("the Maintainer's public list has %d rows, want 1", got)
	}
}

func TestAMaintainerShowsAHiddenReportAgain(t *testing.T) {
	l := newIssuesLab(t)
	m2 := l.add(goldenDoc(t, "m2-max-image2"), "a")
	l.exec(`UPDATE reports SET hidden_at = ? WHERE public_id = ?`, l.at.UnixMicro(), m2)
	if got := l.status("/reports/" + m2); got != http.StatusNotFound {
		t.Fatalf("a Hidden report's page: %d", got)
	}

	w := l.maintainer.post("/reports/" + m2 + "/show")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/reports#admin-report-"+m2 {
		t.Fatalf("Show: %d to %q", w.Code, w.Header().Get("Location"))
	}
	page := l.page("/reports/" + m2)
	if page.count(t, "#hidden") != 0 {
		t.Error("a report shown again still says hidden")
	}
	if l.seen("/reports/"+m2).count(t, "#hidden") != 0 {
		t.Error("a report shown again still says hidden to the Maintainer")
	}
}

func TestAMaintainerDeletesAnyReportAskedTwice(t *testing.T) {
	l := newIssuesLab(t)
	m2 := l.add(goldenDoc(t, "m2-max-image2"), "a")
	hidden := l.add(goldenDoc(t, "m2-max-image2"), "b")
	l.maintainer.did("/reports/" + hidden + "/hide")

	// Asked once, the lab asks again and deletes nothing.
	w := l.maintainer.post("/reports/" + m2 + "/delete")
	asked := parseHTML(t, w.Body.String())
	if w.Code != http.StatusOK || l.reportState(m2) != "published" {
		t.Fatalf("Delete asked once: %d, the report is %q", w.Code, l.reportState(m2))
	}
	if !asked.hasText(t, `form[action="/reports/`+m2+`/delete"] button`, "Delete this report and its evidence for good") ||
		asked.count(t, `form[action="/reports/`+m2+`/delete"] input[name="sure"][value="yes"]`) != 1 {
		t.Error("the lab does not ask again with the sentence that states the effect")
	}

	for _, id := range []string{m2, hidden} {
		w = l.maintainer.post("/reports/"+id+"/delete", "sure", "yes")
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/reports#all-reports" {
			t.Fatalf("Delete: %d to %q", w.Code, w.Header().Get("Location"))
		}
		if l.reportState(id) != "" {
			t.Errorf("the report %s is still there", id)
		}
		if got := l.maintainer.do("GET", "/reports/"+id, "").Code; got != http.StatusNotFound {
			t.Errorf("a deleted report's page, for the Maintainer: %d", got)
		}
	}
	if got := l.seen("/reports").count(t, "tr.admin-row"); got != 0 {
		t.Errorf("the Maintainer's list still has %d rows", got)
	}
	// What is gone cannot be hidden, shown or deleted.
	for _, action := range []string{"hide", "show", "delete"} {
		if w := l.maintainer.post("/reports/"+m2+"/"+action, "sure", "yes"); w.Code != http.StatusNotFound {
			t.Errorf("%s of a deleted report: %d", action, w.Code)
		}
	}
}

func TestNobodyButAMaintainerHidesShowsOrDeletes(t *testing.T) {
	l := newIssuesLab(t)
	m2 := l.add(goldenDoc(t, "m2-max-image2"), "a")
	hidden := l.add(goldenDoc(t, "m2-max-image2"), "b")
	l.maintainer.did("/reports/" + hidden + "/hide")

	member := l.people.signedIn(edithID)
	l.maintainer.did("/people/mark", "handle", "edith", "as", "m-team")
	nobody := l.people.signedIn(graceID)
	actions := []string{"/reports/" + m2 + "/hide", "/reports/" + hidden + "/show", "/reports/" + m2 + "/delete", "/reports/" + hidden + "/delete"}
	for name, b := range map[string]*browser{"an M team member": member, "a person with no role": nobody} {
		for _, action := range actions {
			if w := b.post(action, "sure", "yes"); w.Code != http.StatusNotFound {
				t.Errorf("%s, POST %s: %d, want 404", name, action, w.Code)
			}
		}
	}
	// Signed out, an action leads to sign-in and does nothing.
	for _, action := range actions {
		w := l.do("POST", action, "sure=yes", "Content-Type", "application/x-www-form-urlencoded", "Origin", labAddress)
		if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/sign-in") {
			t.Errorf("signed out, POST %s: %d to %q", action, w.Code, w.Header().Get("Location"))
		}
	}
	// A Maintainer's own session, sent from another site's page or without the session's token, does nothing either.
	if w := l.maintainer.do("POST", actions[0], "", "Content-Type", "application/x-www-form-urlencoded", "Origin", "https://evil.example"); w.Code < 400 {
		t.Errorf("a cross-site Hide: %d", w.Code)
	}
	if w := l.maintainer.do("POST", actions[0], "", "Content-Type", "application/x-www-form-urlencoded", "Origin", labAddress); w.Code < 400 {
		t.Errorf("a Hide without the session's token: %d", w.Code)
	}
	if l.reportState(m2) != "published" || l.reportState(hidden) != "hidden" {
		t.Fatalf("something changed: %s, %s", l.reportState(m2), l.reportState(hidden))
	}
	// The actions are forms: a link does nothing.
	for _, action := range actions {
		if w := l.maintainer.do("GET", action, ""); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d", action, w.Code)
		}
	}
	if l.reportState(m2) != "published" || l.reportState(hidden) != "hidden" {
		t.Fatalf("a GET changed something: %s, %s", l.reportState(m2), l.reportState(hidden))
	}
}

func TestHiddenReportsAndWhoUploadedEachShowToMaintainersOnly(t *testing.T) {
	l := newIssuesLab(t)
	verified := l.add(goldenDoc(t, "m2-max-image2"), "verified:t")
	community := l.add(goldenWith(t, "m2-max-image2", "gpu.driver", "fails"), "a")
	hidden := l.add(goldenWith(t, "m2-max-image2", "gpu.driver", "fails"), "verified:u")
	l.maintainer.did("/reports/" + hidden + "/hide")
	member := l.people.signedIn(edithID)
	l.maintainer.did("/people/mark", "handle", "edith", "as", "m-team")

	// The Maintainer's list names who each Mac was signed in as.
	list := l.seen("/reports")
	for id, want := range map[string]string{verified: "@team-t", hidden: "@team-u", community: "nobody signed in"} {
		if got := list.sole(t, `tr[id="admin-report-`+id+`"] .uploader`); got.words() != want {
			t.Errorf("who uploaded %s: %q, want %q", id, got.words(), want)
		}
	}
	if !list.hasText(t, `tr[id="admin-report-`+verified+`"] .badge-tester`, "verified") || !list.hasText(t, `tr[id="admin-report-`+community+`"] .badge-community`, "community") {
		t.Error("the Maintainer's list does not say Verified run or Community run")
	}
	// And so does a report's page, for the Maintainer.
	if !l.seen("/reports/"+verified).hasText(t, "#uploader", "@team-t") || !l.seen("/reports/"+community).hasText(t, "#uploader", "nobody signed in") {
		t.Error("the Maintainer's view of a report does not say who uploaded it")
	}
	page := l.seen("/reports/" + hidden)
	if !page.hasText(t, "#uploader", "@team-u") {
		t.Error("the Maintainer's view of a Hidden report does not say who uploaded it")
	}
	// A Hidden report's link opens for nobody else, so it gets no issue.
	if page.count(t, "a.issue-link") != 0 {
		t.Error("a Hidden report offers an issue")
	}
	if l.seen("/reports/"+community).count(t, "a.issue-link") == 0 {
		t.Error("a published report offers the Maintainer no issue")
	}

	// Nobody else sees the list, a handle, or a Hidden report.
	seenBy := map[string]func(string) (int, string){
		"a visitor":        func(target string) (int, string) { w := l.do("GET", target, ""); return w.Code, w.Body.String() },
		"an M team member": func(target string) (int, string) { w := member.do("GET", target, ""); return w.Code, w.Body.String() },
	}
	for who, get := range seenBy {
		for _, target := range []string{"/reports", "/reports/" + verified, "/reports/" + community} {
			code, body := get(target)
			if code != http.StatusOK {
				t.Fatalf("%s, GET %s: %d", who, target, code)
			}
			for _, secret := range []string{"admin-row", "all-reports", "team-t", "team-u", "uploader", "nobody signed in", "/hide", "/show", "/delete", hidden} {
				if strings.Contains(body, secret) {
					t.Errorf("%s, GET %s: the page says %q", who, target, secret)
				}
			}
		}
		if code, _ := get("/reports/" + hidden); code != http.StatusNotFound {
			t.Errorf("%s opens a Hidden report: %d", who, code)
		}
	}
}

func TestEachReportActionWritesARecordLine(t *testing.T) {
	l := newIssuesLab(t)
	m2 := l.add(goldenDoc(t, "m2-max-image2"), "a")
	before := len(l.people.recorded())
	l.maintainer.did("/reports/" + m2 + "/hide")
	l.maintainer.did("/reports/" + m2 + "/show")
	// Asking again is no action.
	l.maintainer.post("/reports/" + m2 + "/delete")
	l.maintainer.did("/reports/"+m2+"/delete", "sure", "yes")
	// Nor is what did nothing.
	l.maintainer.post("/reports/"+m2+"/hide", "sure", "yes")

	got := l.people.recorded()[before:]
	want := []string{
		"@ravi hid the report " + m2 + " (MacBook Pro (16-inch, M2 Max, 2023))",
		"@ravi showed the report " + m2 + " (MacBook Pro (16-inch, M2 Max, 2023)) again",
		"@ravi deleted the report " + m2 + " (MacBook Pro (16-inch, M2 Max, 2023))",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the record:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	lines, err := l.s.store.recordLines(3)
	if err != nil || len(lines) != 3 {
		t.Fatal(err)
	}
	for _, line := range lines {
		if line.ActorID != maintainerID || line.At.IsZero() {
			t.Errorf("a line without its actor or its time: %+v", line)
		}
	}
	// The Maintainers' record on People shows them.
	if body := l.maintainer.page("/people"); !strings.Contains(body, "deleted the report "+m2) {
		t.Error("People's record does not show the deletion")
	}
}

// The link holder still deletes, signed in or not, whatever their role: the
// link's secret is the credential. Without it an M team member deletes
// nothing.
func TestTheLinkHolderDeletesAndAnMTeamMemberWithoutTheLinkDoesNot(t *testing.T) {
	l := newIssuesLabWith(t, func(c *Config) { c.MachineIDKey = testMachineIDKey() })
	up := l.reportsLab.upload("m2-max-image2")
	member := l.people.signedIn(edithID)
	l.maintainer.did("/people/mark", "handle", "edith", "as", "m-team")

	if w := member.post("/reports/"+up.ID, "_method", "delete", "token", "not-the-secret"); w.Code != http.StatusNotFound {
		t.Errorf("an M team member with a wrong secret: %d", w.Code)
	}
	if w := member.post("/reports/"+up.ID+"/delete", "sure", "yes"); w.Code != http.StatusNotFound {
		t.Errorf("an M team member, Delete: %d", w.Code)
	}
	if l.reportState(up.ID) != "published" {
		t.Fatal("the report is gone")
	}
	if w := member.post("/reports/"+up.ID, "_method", "delete", "token", up.token(t)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Report deleted") {
		t.Errorf("the link holder, signed in: %d", w.Code)
	}
	if l.reportState(up.ID) != "" {
		t.Error("the link did not delete its report")
	}
}
