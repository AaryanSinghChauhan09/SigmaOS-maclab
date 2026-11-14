package server

import (
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/reports"
)

// The prefilled GitHub issue a Maintainer opens from a failure or a gap,
// translated from the old site's issue_prefill_test.rb: on omacom/linux
// (Asahi hardware, Aurora additions) or omacom/omarchy-mac (Omarchy
// integration). The lab only builds the link; it never calls GitHub.

// issuesLab is a lab with a Maintainer signed in and reports the test stores.
type issuesLab struct {
	*publicLab
	people     *signInLab
	maintainer *browser
}

func newIssuesLab(t *testing.T) *issuesLab {
	t.Helper()
	people := rolesLab(t, nil)
	db, err := sql.Open("sqlite", filepath.Join(people.s.cfg.DataDir, "reports.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &issuesLab{publicLab: &publicLab{reportsLab: &reportsLab{t: t, s: people.s, h: people.h, remote: viaProxy}, db: db, at: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)},
		people: people, maintainer: people.signedIn(maintainerID)}
}

// seen is a page as the Maintainer sees it.
func (l *issuesLab) seen(target string) *node {
	l.t.Helper()
	w := l.maintainer.do("GET", target, "")
	if w.Code != http.StatusOK {
		l.t.Fatalf("GET %s: %d", target, w.Code)
	}
	return parseHTML(l.t, w.Body.String())
}

// issue is an issue link's target repository, title and body.
func issue(t *testing.T, link *node) (repo, title, body string) {
	t.Helper()
	u, err := url.Parse(link.attr("href"))
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasSuffix(u.Path, "/issues/new") {
		t.Fatalf("the issue link %q: %v", link.attr("href"), err)
	}
	if link.attr("target") != "_blank" || link.attr("rel") != "noopener noreferrer" || link.attr("data-repo") == "" {
		t.Errorf("the issue link's attributes: %v", link.attrs)
	}
	return strings.Trim(strings.TrimSuffix(u.Path, "/issues/new"), "/"), u.Query().Get("title"), u.Query().Get("body")
}

func TestOnlyAMaintainerSeesIssueLinks(t *testing.T) {
	l := newIssuesLab(t)
	id := l.add(goldenWith(t, "m2-max-image2", "gpu.driver", "fails"), "a")
	for _, target := range []string{"/reports/" + id, "/gaps"} {
		if l.page(target).count(t, "a.issue-link") != 0 {
			t.Errorf("%s: a visitor sees an issue link", target)
		}
		if l.seen(target).count(t, "a.issue-link") == 0 {
			t.Errorf("%s: a Maintainer sees no issue link", target)
		}
	}
	// An M team member is not a Maintainer.
	member := l.people.signedIn(edithID)
	l.maintainer.did("/people/mark", "handle", "edith", "as", "m-team")
	for _, target := range []string{"/reports/" + id, "/gaps"} {
		w := member.do("GET", target, "")
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "issue-link") {
			t.Errorf("%s: an M team member sees an issue link (%d)", target, w.Code)
		}
	}
}

func TestAFailingChecksIssueGoesToItsLayersProjectWithTheReportTheMacAndTheEvidence(t *testing.T) {
	l := newIssuesLab(t)
	id := l.add(goldenWith(t, "m2-max-image2", "gpu.driver", "fails"), "a")
	page := l.seen("/reports/" + id)
	repo, title, body := issue(t, page.sole(t, `li[id="check-setup.first-boot-hardware"] a.issue-link`))
	if repo != "omacom/omarchy-mac" || title != "First-boot hardware setup: doesn't work, but should on this Mac · MacBook Pro (16-inch, M2 Max, 2023), converged 4.0.0-alpha" {
		t.Errorf("the issue: %s %q", repo, title)
	}
	for _, want := range []string{"Report: " + labAddress + "/reports/" + id, "Apple MacBook Pro (16-inch, M2 Max, 2023), M2 Max (t6021, board j416c)",
		"- Kernel: 7.1.12-2-2-ARCH, linux-aurora 7.1.12.aurora2-2", "- Expected: aurora supported, omarchy supported",
		"```text\n2026-09-26T08:37:37+10:00 <hostname> omarchy-provision-hardware", "- Feature: First-boot hardware setup (`first-boot-hardware-setup`), Omarchy integration layer",
		"- Stack: converged 4.0.0-alpha", "From " + labHost + ", feature catalogue v"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %q:\n%s", want, body)
		}
	}
	repo, title, _ = issue(t, page.sole(t, `li[id="check-gpu.driver"] a.issue-link`))
	if repo != "omacom/linux" || !strings.HasPrefix(title, "GPU: ") {
		t.Errorf("the Asahi hardware layer goes to the Aurora kernel: %s %q", repo, title)
	}
	if page.count(t, `li[id="check-gpu.vulkan"] a.issue-link`) != 0 {
		t.Error("an issue for what works")
	}
}

func TestARegressionsIssueSaysSoAndLinksTheLastVerifiedPass(t *testing.T) {
	l := newIssuesLab(t)
	pass := l.add(goldenDoc(t, "m2-max-image2"), "verified:t")
	failing := l.add(goldenWith(t, "m2-max-image2", "gpu.driver", "fails", "gpu.vulkan", "fails"), "a")
	const title = "Regression: GPU · MacBook Pro (16-inch, M2 Max, 2023), converged 4.0.0-alpha"
	repo, got, body := issue(t, l.seen("/reports/"+failing).sole(t, `li[id="check-gpu.driver"] a.issue-link`))
	if repo != "omacom/linux" || got != title || !strings.Contains(body, "Last verified pass on the same model and stack: "+labAddress+"/reports/"+pass) || !strings.Contains(body, "(a regression)") {
		t.Errorf("the check's issue: %s %q\n%s", repo, got, body)
	}
	repo, got, body = issue(t, l.seen("/gaps").sole(t, `#regressions tr.regression-row[data-feature="gpu"] a.issue-link`))
	if repo != "omacom/linux" || got != title || !strings.Contains(body, "Last verified pass: "+labAddress+"/reports/"+pass) ||
		!strings.Contains(body, "Runs with the regression:\n- "+labAddress+"/reports/"+failing+" (converged 4.0.0-alpha, 2026-10-04)") {
		t.Errorf("the open regression's issue: %s %q\n%s", repo, got, body)
	}
	if strings.Contains(body, "team-t") {
		t.Error("the issue names who the Mac was signed in as")
	}
}

func TestEveryGapOnTheKernelGapPageHasAnIssue(t *testing.T) {
	l := newIssuesLab(t)
	id := l.add(goldenWith(t, "m2-max-image2", "input.ambient-light", "not-in-aurora"), "a")
	page := l.seen("/gaps")
	link := labAddress + "/reports/" + id
	repo, title, body := issue(t, page.sole(t, `tr.gap-row[data-outcome="not-in-aurora"][data-feature="aop"] a.issue-link`))
	if repo != "omacom/linux" || title != "Always-on processor (AOP: ambient light sensor, lid angle): not yet supported by Aurora on M2 Max" || !strings.Contains(body, "Reports:\n- "+link) ||
		!strings.Contains(body, "found by 1 machine: MacBook Pro (16-inch, M2 Max, 2023).") {
		t.Errorf("the reported gap's issue: %s %q\n%s", repo, title, body)
	}
	repo, title, body = issue(t, page.sole(t, `tr.unclaimed-row[data-compatible="apple,t6020-avd"] a.issue-link`))
	if repo != "omacom/linux" || title != "No driver claims apple,t6020-avd on M2 Max" || !strings.Contains(body, link) || !strings.Contains(body, "Feature catalogue: Video decoder: ") {
		t.Errorf("the unclaimed hardware's issue: %s %q\n%s", repo, title, body)
	}
	gaps := page.all(t, "#unverified-in-aurora tr.gap-row")
	if len(gaps) == 0 {
		t.Fatal("no catalogue gap")
	}
	repo, title, body = issue(t, gaps[0].sole(t, "a.issue-link"))
	if repo != "omacom/linux" || !strings.Contains(title, ": verify Aurora on ") || !strings.Contains(body, "Feature page: "+labAddress+"/features/") {
		t.Errorf("the catalogue gap's issue: %s %q\n%s", repo, title, body)
	}
}

func TestLongEvidenceIsCutShortWithItsFenceClosed(t *testing.T) {
	l := newIssuesLab(t)
	long := goldenDoc(t, "m2-max-image2")
	var evidence []any
	for i := 0; i < 50; i++ {
		evidence = append(evidence, "line: [probe] (/sys/x) "+strings.Repeat(":/[", 150))
	}
	checkOf(t, long, "setup.first-boot-hardware")["evidence"] = evidence
	id := l.add(long, "a")
	link := l.seen("/reports/"+id).sole(t, `li[id="check-setup.first-boot-hardware"] a.issue-link`)
	if len(link.attr("href")) > reports.IssueMaxURL {
		t.Errorf("the link is %d bytes: percent-encoded, it must still fit in a GitHub link", len(link.attr("href")))
	}
	_, _, body := issue(t, link)
	if len([]rune(body)) >= reports.IssueMaxBody+100 || !strings.Contains(body, "Report: "+labAddress+"/reports/"+id) ||
		strings.Count(body, "```")%2 != 0 || !strings.Contains(body, "cut short") {
		t.Errorf("the body, %d characters:\n%s", len([]rune(body)), body[:min(len(body), 600)])
	}
}
