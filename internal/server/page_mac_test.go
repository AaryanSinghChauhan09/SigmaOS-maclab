package server

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// macPage is a Mac's page as b sees it, from <main> on.
func (b *browser) macPage(mac, tab string) string {
	b.l.t.Helper()
	target := "/macs/" + mac
	if tab != "" {
		target += "?tab=" + tab
	}
	w := b.do("GET", target, "")
	if w.Code != http.StatusOK {
		b.l.t.Fatalf("GET %s: %d", target, w.Code)
	}
	return part(b.l.t, w.Body.String(), `<main class="site-main">`, `</main>`)
}

var reCard = regexp.MustCompile(`<section class="card[^"]*" id="([a-z-]+)">`)

// cards are the ids of the panels on a piece of a page, in order.
func cards(page string) string {
	var out []string
	for _, m := range reCard.FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return strings.Join(out, " ")
}

// macJob leaves a job the Mac ran, asked for by this handle.
func (l *signInLab) macJob(mac, id, by string, at time.Time) {
	l.t.Helper()
	j := &api.Job{ID: id, Spec: api.JobSpec{Device: mac, Kernel: strings.Repeat("ab", 32)}, DeviceID: l.s.dev(mac).snapshot().ID,
		State: api.JobDone, Outcome: api.OutcomePass, Created: at, Updated: at, Maintainer: by}
	if err := l.s.store.saveJob(j); err != nil {
		l.t.Fatal(err)
	}
}

// A Mac's page is for whoever sees the Mac: the M team and the Maintainers
// any Mac, a Volunteer their own. To anyone else it is not there.
func TestAMacsPageIsForWhoeverSeesTheMac(t *testing.T) {
	l := leaseLab(t)
	l.lrMac("m1-pro", "j316s", nil)
	l.lrMac("vol-air", "j313", lrVolunteers(api.AccessQualification, l.s.now().Add(2*time.Hour)))
	l.lrMac("other-air", "j313", lrVolunteers(api.AccessQualification, l.s.now().Add(2*time.Hour)))
	l.lrMac("gone", "j274", func(d *api.Device) { d.State = api.StateLeft })
	l.lrLend("vol-air", lrLinus)
	grace, ravi, linus := l.lrMember(lrGrace), l.lrMaintainer(), l.signedIn(lrLinus)

	if w := l.browser().do("GET", "/macs/m1-pro", ""); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/sign-in?return=") {
		t.Errorf("signed out: %d to %q", w.Code, w.Header().Get("Location"))
	}
	for _, c := range []struct {
		who  string
		b    *browser
		mac  string
		want int
	}{
		{"someone with no role", l.signedIn(nobodyID), "m1-pro", 404},
		{"a Volunteer, a Lab-owned Mac", linus, "m1-pro", 404},
		{"a Volunteer, another Volunteer's Mac", linus, "other-air", 404},
		{"a Volunteer, their own Mac", linus, "vol-air", 200},
		{"an M team member, a Lab-owned Mac", grace, "m1-pro", 200},
		{"an M team member, a Volunteer's Mac", grace, "vol-air", 200},
		{"a Maintainer", ravi, "vol-air", 200},
		{"a Maintainer, a Mac that left", ravi, "gone", 404},
		{"a Maintainer, no such Mac", ravi, "nope", 404},
	} {
		for _, tab := range []string{"", "?tab=live", "?tab=records"} {
			if w := c.b.do("GET", "/macs/"+c.mac+tab, ""); w.Code != c.want {
				t.Errorf("%s, GET /macs/%s%s: %d, want %d", c.who, c.mac, tab, w.Code, c.want)
			}
		}
	}

	page := grace.macPage("m1-pro", "")
	tabs := part(t, page, `<nav class="tabs"`, "</nav>")
	if got := linkLabels(tabs); got != "Overview, Live, Records" {
		t.Fatalf("the tabs: %q", got)
	}
	if !strings.Contains(tabs, `<a class="current" aria-current="page" href="/macs/m1-pro">Overview</a>`) {
		t.Errorf("Overview is not the tab in use: %s", tabs)
	}
	if tabs := part(t, grace.macPage("m1-pro", "records"), `<nav class="tabs"`, "</nav>"); !strings.Contains(tabs, `<a class="current" aria-current="page" href="/macs/m1-pro?tab=records">Records</a>`) {
		t.Errorf("Records is not the tab in use: %s", tabs)
	}
	if got := cards(grace.macPage("m1-pro", "no-such-tab")); got != "mac-facts mac-jobs" {
		t.Errorf("a tab the page lacks shows %q, want Overview", got)
	}
	if !strings.Contains(page, `<a href="/macs">Macs</a>`) || !strings.Contains(page, "<h1>m1-pro</h1>") {
		t.Errorf("an M team member's page does not lead back to Macs under the Mac's name: %s", page)
	}
	if own := linus.macPage("vol-air", ""); !strings.Contains(own, `<a href="/my-macs">My Macs</a>`) || strings.Contains(own, `href="/macs"`) {
		t.Errorf("a Volunteer's page does not lead back to My Macs: %s", own)
	}
	// Macs names each Mac as the link to its page.
	if rows, _ := macRows(grace.macsPage("")); !strings.Contains(rows["m1-pro"], `<a href="/macs/m1-pro">m1-pro</a>`) {
		t.Errorf("Macs does not link a Mac's page: %s", rows["m1-pro"])
	}
}

// Overview has the facts, the Lease and its buttons, the jobs and "Boot a
// kernel".
func TestOverviewHasTheFactsTheLeaseItsButtonsTheJobsAndBootAKernel(t *testing.T) {
	l := leaseLab(t)
	l.lrMac("m1-pro", "j316s", func(d *api.Device) { d.Facts.AgentVersion = "0.2.0" })
	windowEnd := l.s.now().Add(2 * time.Hour)
	l.lrMac("vol-air", "j313", lrVolunteers(api.AccessQualification, windowEnd))
	l.lrLend("vol-air", lrLinus)
	grace, ravi := l.lrMember(lrGrace), l.lrMaintainer()
	linus := l.signedIn(lrLinus)
	l.macJob("m1-pro", "j-older", "ravi", l.s.now().Add(-2*time.Hour))
	l.macJob("m1-pro", "j-newer", "grace", l.s.now().Add(-time.Hour))
	l.macJob("vol-air", "j-air", "ravi", l.s.now().Add(-time.Hour))

	page := grace.macPage("m1-pro", "")
	if got := cards(page); got != "mac-facts mac-jobs" {
		t.Fatalf("Overview's panels: %q", got)
	}
	facts := part(t, page, `id="mac-facts"`, "</section>")
	text := rowText(facts)
	for _, want := range []string{"Configuration j316s, 10 CPU and 16 GPU cores", "Kind Lab-owned", "State free", "Access level Full",
		"Known-good kernel " + lrKnownGood, "Running " + lrKnownGood, "lab-agent 0.2.0", "Lease none"} {
		if !strings.Contains(text, want) {
			t.Errorf("the facts lack %q: %s", want, text)
		}
	}
	if got := buttons(facts); got != "Lease" {
		t.Errorf("a member's buttons on a free Mac: %q", got)
	}
	if !strings.Contains(facts, `<a class="button button-small" href="/boot?mac=m1-pro">Boot a kernel</a>`) {
		t.Errorf("no way to Boot a kernel with this Mac chosen: %s", facts)
	}
	jobs := part(t, page, `id="mac-jobs"`, "</section>")
	newer, older := strings.Index(jobs, `<a href="/jobs/j-newer">j-newer</a>`), strings.Index(jobs, `<a href="/jobs/j-older">j-older</a>`)
	if newer < 0 || older < newer || strings.Contains(jobs, "j-air") {
		t.Errorf("the Mac's jobs, newest first, each a link: %s", jobs)
	}
	if text := rowText(jobs); !strings.Contains(text, "@grace") || !strings.Contains(text, "pass") || !strings.Contains(text, "Boot kernel abababababab") {
		t.Errorf("a job's row says who asked, what and how it ended: %s", text)
	}

	// The Lease's buttons act from here and come back here.
	if w := grace.form("/macs/m1-pro/lease", "back", "mac"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/macs/m1-pro" {
		t.Fatalf("Lease from the Mac's page: %d to %q", w.Code, w.Header().Get("Location"))
	}
	lease := l.leaseOn("m1-pro")
	facts = part(t, grace.macPage("m1-pro", ""), `id="mac-facts"`, "</section>")
	if text := rowText(facts); !strings.Contains(text, "Lease @grace M team until "+when(lease.Expires)) || !strings.Contains(text, "State leased") {
		t.Errorf("the facts of a leased Mac: %s", text)
	}
	if got := buttons(facts); got != "Renew, Release" {
		t.Errorf("the holder's buttons: %q", got)
	}
	// A Maintainer sees whose it is, no button to take it, and where a Lease is ended.
	facts = part(t, ravi.macPage("m1-pro", ""), `id="mac-facts"`, "</section>")
	if got := buttons(facts); got != "" || !strings.Contains(rowText(facts), "leased by @grace until") || !strings.Contains(facts, `href="/people"`) {
		t.Errorf("a Maintainer on a Mac grace holds: buttons %q: %s", got, rowText(facts))
	}
	if w := ravi.form("/macs/m1-pro/lease", "back", "mac"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `role="alert"`) ||
		!strings.Contains(w.Body.String(), "m1-pro is leased by grace until") || !strings.Contains(w.Body.String(), "<h1>m1-pro</h1>") {
		t.Fatalf("a refused Lease is said on the Mac's page: %d %s", w.Code, w.Body)
	}
	l.clock.Advance(time.Hour)
	if w := grace.form("/macs/m1-pro/renew", "back", "mac"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/macs/m1-pro" {
		t.Fatalf("Renew from the Mac's page: %d to %q", w.Code, w.Header().Get("Location"))
	}
	if q := l.leaseOn("m1-pro"); !q.Expires.Equal(l.s.now().Add(4 * time.Hour)) {
		t.Fatalf("renewed from the Mac's page, it ends %s", q.Expires)
	}
	if w := grace.form("/macs/m1-pro/release", "back", "mac"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<a href="/macs/m1-pro">Leave it as it is</a>`) || l.leaseOn("m1-pro") == nil {
		t.Fatalf("the first Release asks again and leads back to the Mac's page: %d %s", w.Code, w.Body)
	}
	if w := grace.form("/macs/m1-pro/release", "back", "mac", "sure", "yes"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/macs/m1-pro" || l.leaseOn("m1-pro") != nil {
		t.Fatalf("the second Release: %d to %q", w.Code, w.Header().Get("Location"))
	}

	// A Volunteer's Mac: whose it is, its Access level and its window.
	air := rowText(part(t, grace.macPage("vol-air", ""), `id="mac-facts"`, "</section>"))
	for _, want := range []string{"Kind Volunteer's of @linus", "Access level Qualification", "Availability window open until " + when(windowEnd)} {
		if !strings.Contains(air, want) {
			t.Errorf("a Volunteer's Mac's facts lack %q: %s", want, air)
		}
	}
	// Its Volunteer reads the same facts and jobs, and takes nothing.
	own := linus.macPage("vol-air", "")
	if got := cards(own); got != "mac-facts mac-jobs" {
		t.Fatalf("a Volunteer's Overview: %q", got)
	}
	if got := buttons(own); got != "" || strings.Contains(own, "Boot a kernel") {
		t.Errorf("a Volunteer is offered a Lease or a kernel to boot: buttons %q", got)
	}
	if !strings.Contains(own, "j-air") {
		t.Errorf("a Volunteer is not shown their Mac's jobs: %s", own)
	}
}

// An M team member sees Live only under their own Lease, and otherwise who
// holds the Mac and until when.
func TestAnMTeamMemberSeesLiveOnlyUnderTheirOwnLease(t *testing.T) {
	l := leaseLab(t)
	l.lrMac("m1-pro", "j316s", nil)
	grace, linus, ravi := l.lrMember(lrGrace), l.lrMember(lrLinus), l.lrMaintainer()

	page := grace.macPage("m1-pro", "live")
	if strings.Contains(page, "data-live") || cards(page) != "" {
		t.Fatalf("an M team member with no Lease is shown Live: %s", page)
	}
	fence := part(t, page, `<div class="fence" id="live-fence">`, "</div>")
	if !strings.Contains(rowText(fence), "Live needs your own Lease on m1-pro.") || buttons(fence) != "Lease" {
		t.Errorf("the fence on a free Mac: %s", fence)
	}
	if w := grace.form("/macs/m1-pro/lease", "back", "live"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/macs/m1-pro?tab=live" {
		t.Fatalf("Lease from the fence: %d to %q", w.Code, w.Header().Get("Location"))
	}
	lease := l.leaseOn("m1-pro")

	page = grace.macPage("m1-pro", "live")
	if got := cards(page); got != "live-screen live-logs" {
		t.Fatalf("Live under the member's own Lease: %q", got)
	}
	live := part(t, page, "<div data-live", ">")
	for _, want := range []string{`data-mac="m1-pro"`, `data-api="/api/devices/m1-pro"`, `data-stream="/api/stream?device=m1-pro"`, `data-csrf="` + grace.csrf() + `"`} {
		if !strings.Contains(live, want) {
			t.Errorf("what the page's script reads lacks %s: %s", want, live)
		}
	}
	if got := buttons(part(t, page, `id="live-screen"`, "</section>")); !strings.Contains(got, "Capture") {
		t.Errorf("the holder's Screen has no Capture: %q", got)
	}
	if !regexp.MustCompile(`<script src="/assets/live-[0-9a-f]+\.js" defer></script>`).MatchString(grace.do("GET", "/macs/m1-pro?tab=live", "").Body.String()) {
		t.Error("the page loads no script for the event stream")
	}

	// Another member: who holds it and until when, and nothing to take.
	page = linus.macPage("m1-pro", "live")
	if strings.Contains(page, "data-live") || cards(page) != "" {
		t.Fatalf("an M team member is shown Live under another's Lease: %s", page)
	}
	fence = part(t, page, `<div class="fence" id="live-fence">`, "</div>")
	if text := rowText(fence); !strings.Contains(text, "Live needs your own Lease on m1-pro.") || !strings.Contains(text, "@grace holds it until "+when(lease.Expires)) || buttons(fence) != "" {
		t.Errorf("the fence under another's Lease: %s", fence)
	}
	// A Maintainer still looks.
	if got := cards(ravi.macPage("m1-pro", "live")); got != "live-screen live-logs" {
		t.Errorf("a Maintainer's Live under another's Lease: %q", got)
	}

	// What the stream carries of a Mac's live views follows the same rule.
	graceIs, linusIs := caller{team: "grace", account: lrGrace}, caller{team: "linus", account: lrLinus}
	for _, kind := range []string{"kevent", "screen"} {
		if !l.s.streamsTo(graceIs, kind, "m1-pro") {
			t.Errorf("the holder's stream lacks %s", kind)
		}
		if l.s.streamsTo(linusIs, kind, "m1-pro") {
			t.Errorf("a member without the Lease is streamed %s", kind)
		}
		if !l.s.streamsTo(caller{maintainer: "ravi"}, kind, "m1-pro") {
			t.Errorf("a Maintainer's stream lacks %s", kind)
		}
	}
	for _, kind := range []string{"device", "job"} {
		if !l.s.streamsTo(linusIs, kind, "m1-pro") {
			t.Errorf("a member's stream lacks %s, which is the fleet's", kind)
		}
	}
	if l.s.streamsTo(graceIs, "serial", "m1-pro") || l.s.streamsTo(graceIs, "build", "") {
		t.Error("a member is streamed the serial line or the builds")
	}

	// When the Lease ends, so does Live.
	if code, body := grace.release("m1-pro", ""); code != http.StatusOK {
		t.Fatalf("release: %d %s", code, body)
	}
	if page := grace.macPage("m1-pro", "live"); strings.Contains(page, "data-live") {
		t.Error("Live outlasts the member's Lease")
	}
}

// A Volunteer on their own Mac's page sees Overview, Live (view only) and
// Records.
func TestAVolunteerSeesOverviewLiveViewOnlyAndRecordsOfTheirOwnMac(t *testing.T) {
	l := leaseLab(t)
	l.lrMac("vol-air", "j313", lrVolunteers(api.AccessFull, l.s.now().Add(2*time.Hour)))
	l.lrLend("vol-air", lrLinus)
	linus, grace := l.signedIn(lrLinus), l.lrMember(lrGrace)

	if got := linkLabels(part(t, linus.macPage("vol-air", ""), `<nav class="tabs"`, "</nav>")); got != "Overview, Live, Records" {
		t.Fatalf("a Volunteer's tabs: %q", got)
	}
	page := linus.macPage("vol-air", "live")
	if got := cards(page); got != "live-screen live-logs" {
		t.Fatalf("a Volunteer's Live, with no Lease of their own: %q", got)
	}
	if got := buttons(page); got != "" || strings.Contains(page, "data-capture") || strings.Contains(page, "data-auto") {
		t.Errorf("a Volunteer's Live is view only, yet has buttons: %q", got)
	}
	if live := part(t, page, "<div data-live", ">"); !strings.Contains(live, "data-view-only") || strings.Contains(live, "data-csrf") {
		t.Errorf("the script is not told the page is view only: %s", live)
	}
	// Nothing holds the Mac: its live journal is not read, and the page says why.
	why := "shown only while a job or lease holds it inside its Availability window"
	if !strings.Contains(page, why) || !strings.Contains(part(t, page, "<div data-live", ">"), "data-not-now") {
		t.Errorf("a Volunteer's Mac nothing holds: Live does not say why its journal is not read: %s", page)
	}
	grace.mustLease("vol-air", "grace@laptop", 0)
	if page := linus.macPage("vol-air", "live"); strings.Contains(page, why) || strings.Contains(part(t, page, "<div data-live", ">"), "data-not-now") {
		t.Errorf("under a Lease the Volunteer's Live still says nothing holds the Mac: %s", page)
	}
	if w := linus.write("POST", "/api/devices/vol-air/screenshot", ""); w.Code != http.StatusForbidden {
		t.Errorf("a Volunteer capturing their Mac's screen: %d %s", w.Code, w.Body)
	}

	records := linus.macPage("vol-air", "records")
	if got := cards(records); got != "lease-record mac-record audit-log exec-record" {
		t.Fatalf("a Volunteer's Records: %q", got)
	}
	if !strings.Contains(rowText(part(t, records, `id="lease-record"`, "</section>")), "@grace took a Lease on vol-air") {
		t.Errorf("a Volunteer is not shown who took a Lease on their Mac: %s", records)
	}
}

// Records holds the Lease record, the Mac's record, the Audit log for
// Maintainers and the Mac's Volunteer, and the place of the Exec record.
func TestRecordsHoldsTheLeaseRecordTheMacsRecordTheAuditLogAndTheExecRecord(t *testing.T) {
	l := leaseLab(t)
	l.lrMac("vol-air", "j313", lrVolunteers(api.AccessFull, l.s.now().Add(6*time.Hour)))
	l.lrLend("vol-air", lrGrace)
	grace, linus, ravi := l.lrMember(lrGrace), l.lrMember(lrLinus), l.lrMaintainer()
	linus.mustLease("vol-air", "linus@desk", 0)
	l.clock.Advance(time.Minute)
	if code, body := ravi.release("vol-air", "?force=1&why=needed+for+the+release"); code >= 300 {
		t.Fatalf("ending the Lease: %d %s", code, body)
	}
	at := l.s.now()
	entries := []api.AuditEntry{
		{Time: at, Maintainer: "ravi", Job: "j-77", Action: "boot linux-aurora 7.2", End: 10},
		{Time: at.Add(time.Second), Maintainer: "linus", Action: "lab exec: uname -a", Refused: "the Access level is Qualification", End: 20},
	}
	l.s.dev("vol-air").copyAudit(entries)

	for who, c := range map[string]struct {
		b     *browser
		cards string
	}{
		"a Maintainer":                      {ravi, "lease-record mac-record audit-log exec-record"},
		"the Mac's Volunteer":               {grace, "lease-record mac-record audit-log exec-record"},
		"an M team member who lends it not": {linus, "lease-record mac-record exec-record"},
	} {
		page := c.b.macPage("vol-air", "records")
		if got := cards(page); got != c.cards {
			t.Errorf("%s's Records: %q, want %q", who, got, c.cards)
			continue
		}
		record := part(t, page, `id="lease-record"`, "</section>")
		ended, took := strings.Index(record, "@linus&#39;s Lease on vol-air was ended by @ravi: needed for the release"), strings.Index(record, "@linus took a Lease on vol-air")
		if ended < 0 || took < ended {
			t.Errorf("%s's Lease record, newest first, with who and why: %s", who, record)
		}
		if !strings.Contains(record, `<span class="when">`+whenDay(at)) {
			t.Errorf("%s's Lease record does not say when: %s", who, record)
		}
		if strings.Contains(page, "uname -a") != strings.Contains(c.cards, "audit-log") {
			t.Errorf("%s and the Audit log's lines: %s", who, page)
		}
		if exec := rowText(part(t, page, `id="exec-record"`, "</section>")); !strings.Contains(exec, "Maintainers, whoever ran the command and the Mac's Volunteer") {
			t.Errorf("%s's place for the Exec record: %s", who, exec)
		}
	}
	audit := rowText(part(t, ravi.macPage("vol-air", "records"), `id="audit-log"`, "</section>"))
	for _, want := range []string{"@ravi", "job j-77", "boot linux-aurora 7.2", "REFUSED lab exec: uname -a (the Access level is Qualification)", "written by the Mac itself"} {
		if !strings.Contains(audit, want) {
			t.Errorf("the Audit log lacks %q: %s", want, audit)
		}
	}
	if i, j := strings.Index(audit, "uname -a"), strings.Index(audit, "boot linux-aurora"); i < 0 || j < i {
		t.Errorf("the Audit log is not newest first: %s", audit)
	}
}

// There is no serial console panel and no console record, on any Mac, for
// anyone.
func TestAMacsPageHasNoSerialConsoleAndNoConsoleRecord(t *testing.T) {
	l := leaseLab(t)
	l.lrMac("m1-pro", "j316s", func(d *api.Device) { d.OOB = &api.OOBConfig{Driver: "fake"} })
	ravi, owner := l.lrMaintainer(), l.signedIn(ownerID)
	ravi.mustLease("m1-pro", "ravi@mini", 0)
	for _, b := range []*browser{ravi, owner} {
		for _, tab := range []string{"", "live", "records"} {
			page := strings.ToLower(b.macPage("m1-pro", tab))
			for _, word := range []string{"serial", "console", "shell", "xterm"} {
				if strings.Contains(page, word) {
					t.Errorf("tab %q speaks of the %s", tab, word)
				}
			}
		}
	}
	script, err := os.ReadFile("../web/static/live.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"serial", "/console", "exec"} {
		if strings.Contains(string(script), word) {
			t.Errorf("the page's script speaks of %q", word)
		}
	}
}
