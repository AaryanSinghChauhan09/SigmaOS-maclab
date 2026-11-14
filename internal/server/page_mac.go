package server

// A Mac's page: Overview (its facts, its Lease and the Lease's buttons, its
// jobs, the way to "Boot a kernel"), Live (its screen and its logs) and
// Records (the Lease record, the Mac's record, the Audit log, the Exec
// record). It is for whoever sees the Mac: the M team and the Maintainers
// any Mac, a Volunteer their own. Each part is shown to who may see it: an
// M team member has Live only under their own Lease, a Volunteer's Live is
// view only, and the Audit log is for Maintainers and the Mac's Volunteer.
// There is no serial console here.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /macs/{name}", web.SignedIn, (*Server).pMac)
	addPage("POST /macs/{name}/hard-reset", web.MTeamUp, (*Server).pMacHardReset)
}

const (
	tabOverview = "overview"
	tabLive     = "live"
	tabRecords  = "records"
)

const (
	logsJournal = "journal"
	logsKernel  = "kernel"
	logsEvents  = "events"
)

type macPageData struct {
	Tab string
	// Flash is a refusal of one of the Lease's buttons.
	Flash string
	Mac   macRow
	// Path is the page's own path; Back and BackLabel the list it belongs to.
	Path, Back, BackLabel string
	// Team is the M team and up: the Lease's buttons and "Boot a kernel".
	Team, Maintainer bool
	// Stream and Changes are for the page's script: the Mac's events, and
	// what of the Mac the page was made from (see macChanges).
	Stream, Changes string

	Facts macFacts
	Jobs  []macJobRow

	// Fence stands in place of Live for an M team member without their own
	// Lease on the Mac.
	Fence *liveFence
	Live  *liveData

	Leases, MacRecord, Audit, Exec []recordShownLine
	// ShowAudit: the Audit log is for Maintainers and the Mac's Volunteer.
	ShowAudit bool
}

type macFacts struct {
	Volunteers                         bool
	AccessLevel, Window                string
	KnownGood, Kernel, Agent, LastSeen string
	Reason                             string
	Queued                             int
	// Cable is a Lab-owned Mac's place on the lab server's cables, and
	// NoHardReset why no Hard reset can reach it now.
	Cable, NoHardReset string
}

type macJobRow struct {
	ID, What, By, State, When string
}

type liveFence struct {
	// Holder and Until name the Lease that holds the Mac, when one does.
	Holder, Until string
	CanLease      bool
	Why           string
}

type liveData struct {
	API, CSRF string
	// ViewOnly is a Volunteer's Live: nothing on it asks the Mac for a
	// capture.
	ViewOnly bool
	// NotNow is why the Mac's screen and journal are not read now.
	NotNow string
	// Screen is the latest kept capture's address, ScreenAt when it was made.
	Screen, ScreenAt string
	Logs             string
	Events           []recordShownLine
	// Reset is the Hard reset of a Lab-owned Mac, for who may send one.
	Reset *liveReset
}

type liveReset struct {
	// Why is why the viewer can send none now; "" shows the button, which
	// turns into Sentence.
	Why, Sentence string
}

// hardResetSentence is what the Hard reset button turns into.
func hardResetSentence(name string) string {
	return "Hard reset: " + name + " is forced to restart now, whatever it is doing"
}

// whenDay is a time as a record says it, in Brisbane.
func whenDay(t time.Time) string { return t.In(brisbane).Format("2006-01-02 15:04 MST") }

// callerAs is the caller a page acts as for the person signed in on it: who
// they are to Scope (see Person.access).
func callerAs(p Person) caller {
	switch {
	case p.Blocked:
	case p.isMaintainer():
		return caller{maintainer: p.Handle}
	case p.MTeam:
		return caller{team: p.Handle, account: p.AccountID}
	case p.Volunteer:
		return caller{login: p.Handle, account: p.AccountID}
	}
	return caller{}
}

func tabOf(raw string) string {
	if raw == tabLive || raw == tabRecords {
		return raw
	}
	return tabOverview
}

func logsOf(raw string) string {
	if raw == logsKernel || raw == logsEvents {
		return raw
	}
	return logsJournal
}

// macPagePath is a Mac's page under a tab.
func macPagePath(name, tab string) string {
	p := "/macs/" + url.PathEscape(name)
	if tab != tabOverview && tab != "" {
		p += "?tab=" + tab
	}
	return p
}

// macChanges is what of a Mac a page shows that the stream can tell has
// changed: its state, its job and its Lease. The page's script makes the
// same line from a device event and compares.
func macChanges(d DeviceView) string {
	var account, expires int64
	if d.Lease != nil {
		account, expires = d.Lease.AccountID, d.Lease.Expires.Unix()
	}
	return fmt.Sprintf("%s|%s|%d|%d", d.State, d.ActiveJob, account, expires)
}

func (s *Server) pMac(w http.ResponseWriter, r *http.Request) {
	v := s.dev(r.PathValue("name"))
	if v == nil || v.snapshot().State == api.StateLeft || !s.sees(callerAs(leaser(r)), r.PathValue("name")) {
		s.notFound(w, r)
		return
	}
	s.renderMac(w, r, v, tabOf(r.URL.Query().Get("tab")), nil)
}

// renderMac writes a Mac's page; with a refusal of one of its buttons, it
// is said above the tabs under its own status.
func (s *Server) renderMac(w http.ResponseWriter, r *http.Request, v *dev, tab string, refused error) {
	p := leaser(r)
	c := callerAs(p)
	d := s.viewFor(c, v)
	own := d.VolunteerID != 0 && d.VolunteerID == p.AccountID
	data := macPageData{Tab: tab, Mac: s.macRow(v, d, p, s.holdsLease(v, p)), Path: "/macs/" + url.PathEscape(d.Name),
		Back: "/macs", BackLabel: "Macs", Team: p.OnMTeam(), Maintainer: p.isMaintainer(),
		Stream: "/api/stream?device=" + url.QueryEscape(d.Name), Changes: macChanges(d)}
	page := web.Page{Title: d.Name, Nav: "macs"}
	if !data.Team {
		data.Back, data.BackLabel, page.Nav = "/my-macs", "My Macs", "my-macs"
		data.Mac.CanLease, data.Mac.Why, data.Mac.Warning = false, "", ""
	}
	if refused != nil {
		data.Flash, page.Status = refused.Error(), statusOf(refused)
	}
	switch tab {
	case tabLive:
		data.Fence, data.Live = s.macLive(r, v, d, c, data.Mac)
	case tabRecords:
		s.macRecords(&data, v, d, p, p.isMaintainer() || own)
	default:
		data.Facts, data.Jobs = s.macFacts(v, d), s.macJobs(c, d)
	}
	page.Data = data
	s.render(w, r, "mac", page)
}

func (s *Server) macFacts(v *dev, d DeviceView) macFacts {
	f := macFacts{Volunteers: d.IsVolunteer(), AccessLevel: "Full", KnownGood: d.KnownGood, Kernel: d.Kernel,
		Agent: d.Facts.AgentVersion, Reason: d.StateReason, Queued: d.Queued}
	if d.Cable != nil {
		f.Cable, f.NoHardReset = d.Cable.String(), d.Cable.NoHardReset
	}
	if !d.LastSeen.IsZero() {
		f.LastSeen = whenDay(d.LastSeen)
	}
	if f.Volunteers {
		if d.AccessLevel != api.AccessFull {
			f.AccessLevel = "Qualification"
		}
		v.mu.Lock()
		until := v.openUntilLocked()
		v.mu.Unlock()
		f.Window = "closed"
		if !until.IsZero() {
			f.Window = "open until " + when(until)
		}
	}
	return f
}

// macJobsShown is how many of a Mac's jobs Overview lists.
const macJobsShown = 20

// macJobs are the Mac's newest jobs as c may read them: a Volunteer only
// those of the Mac's enrollment now.
func (s *Server) macJobs(c caller, d DeviceView) []macJobRow {
	jobs, err := s.store.jobs(d.Name, macJobsShown, false)
	if err != nil {
		s.log.Printf("%s's jobs: %v", d.Name, err)
		return nil
	}
	var out []macJobRow
	for _, j := range jobs {
		if !c.reads() && (j.DeviceID == "" || j.DeviceID != d.ID) {
			continue
		}
		row := macJobRow{ID: j.ID, What: jobWhat(j), By: j.Maintainer, State: string(j.State), When: whenDay(j.Created)}
		if j.State == api.JobDone {
			row.State = strings.ReplaceAll(j.Outcome, "_", " ")
		}
		out = append(out, row)
	}
	return out
}

// macEventsShown is how many kernel events Live starts with.
const macEventsShown = 100

// macLive is Live for c: the fence for an M team member who may not watch
// the Mac, else what the page's script shows and keeps fresh.
func (s *Server) macLive(r *http.Request, v *dev, d DeviceView, c caller, row macRow) (*liveFence, *liveData) {
	watch := s.mayDo(c, v, actWatch)
	if c.team != "" && watch != nil {
		f := &liveFence{CanLease: row.CanLease}
		if d.Lease != nil {
			f.Holder, f.Until = row.Holder, row.Until
		} else if !row.CanLease {
			f.Why = row.Why
		}
		return f, nil
	}
	base := s.basePath() + "/api/devices/" + url.PathEscape(d.Name)
	live := &liveData{API: base, ViewOnly: !c.reads(), Logs: logsOf(r.URL.Query().Get("logs"))}
	if !live.ViewOnly {
		live.CSRF = CSRFToken(r)
	}
	if watch != nil {
		live.NotNow = watch.Error()
	}
	if kept := s.screens(d.Name); len(kept) > 0 {
		live.Screen, live.ScreenAt = base+"/screens/"+url.PathEscape(kept[0].Name), whenDay(kept[0].Time)
	}
	if d.Cable != nil && !live.ViewOnly {
		live.Reset = &liveReset{Why: d.Cable.NoHardReset, Sentence: hardResetSentence(d.Name)}
		if err := s.fence(v, leaser(r), fenceReset); err != nil {
			live.Reset.Why = err.Error()
		}
	}
	if live.Logs == logsEvents {
		events := v.eventsSince(s.now().Add(-24 * time.Hour))
		for i := len(events) - 1; i >= 0 && len(live.Events) < macEventsShown; i-- {
			e := events[i]
			live.Events = append(live.Events, recordShownLine{whenDay(e.Time), e.Kind + ": " + e.Line})
		}
	}
	return nil, live
}

// macRecordsShown is how many lines of each record Records shows.
const macRecordsShown = 100

func (s *Server) macRecords(data *macPageData, v *dev, d DeviceView, p Person, audit bool) {
	events, err := s.store.leaseRecord(d.ID, macRecordsShown)
	if err != nil {
		s.log.Printf("%s's Lease record: %v", d.Name, err)
	}
	for _, e := range events {
		data.Leases = append(data.Leases, recordShownLine{whenDay(e.Time), e.Line()})
	}
	data.MacRecord = s.macRecord(d)
	data.Exec = s.execRecordFor(v, p)
	if data.ShowAudit = audit; !audit {
		return
	}
	entries, err := s.readAuditCopy(d.Name)
	if err != nil {
		s.log.Printf("%s's Audit log: %v", d.Name, err)
	}
	for i := len(entries) - 1; i >= 0 && len(data.Audit) < macRecordsShown; i-- {
		e := entries[i]
		who := "the lab"
		switch {
		case e.VolunteerLogin != "":
			who = "its Volunteer " + e.VolunteerLogin
		case e.Maintainer != "":
			who = "@" + e.Maintainer
		}
		line := who + " · "
		if e.Job != "" {
			line += "job " + e.Job + " · "
		}
		if e.Refused != "" {
			line += "REFUSED " + e.Action + " (" + e.Refused + ")"
		} else {
			line += e.Action
		}
		data.Audit = append(data.Audit, recordShownLine{whenDay(e.Time), line})
	}
}

// macRecord is the Mac's record: joined, window, Access level, left. The
// lab writes no such line yet.
func (s *Server) macRecord(d DeviceView) []recordShownLine { return nil }

// execRecordFor is the Exec record's lines on v that p may read, newest
// first: Maintainers and the Mac's Volunteer all of them, anyone else their
// own (see execRecordOf).
func (s *Server) execRecordFor(v *dev, p Person) []recordShownLine {
	events, err := s.execRecordOf(p, v, execRecordShown)
	if err != nil {
		s.log.Printf("the Exec record of %s for its page: %v", v.snapshot().Name, err)
		return nil
	}
	var out []recordShownLine
	for _, e := range events {
		out = append(out, recordShownLine{whenDay(e.Time), e.Line()})
	}
	return out
}

// pMacHardReset is the Hard reset button of a Lab-owned Mac's Live.
func (s *Server) pMacHardReset(w http.ResponseWriter, r *http.Request) {
	v := s.macOr404(w, r)
	if v == nil {
		return
	}
	name := v.snapshot().Name
	if !s.sure(w, r, hardResetSentence(name), macPagePath(name, tabLive)) {
		return
	}
	p := leaser(r)
	_, err := s.hardResetBy(r.Context(), v, p, p.Handle)
	s.backToMacs(w, r, err)
}
