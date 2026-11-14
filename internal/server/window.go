package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/window"
)

// windowHoldLocked is why a job expected to take d may not start on the Mac
// now, or "" if it may. A Volunteer's Mac takes a job only inside its
// Availability window, on AC power, and when the job fits in what's left of
// the window; one whose Volunteer has set no window is closed. Lab-owned Macs
// are never held. Caller holds v.mu.
func (v *dev) windowHoldLocked(d time.Duration) string {
	if !v.d.IsVolunteer() {
		return ""
	}
	s, st := v.windowStateLocked()
	now := v.s.now()
	when := func(t time.Time) string { return t.In(s.Location()).Format("Mon 2 Jan 15:04 MST") }
	switch {
	case v.termsWait:
		return termsWaitingLine(v.d.Name)
	case v.d.SitePause.Active(now) && v.d.SitePause.Until.IsZero():
		return fmt.Sprintf("%s is Paused by its Volunteer until they resume it", v.d.Name)
	case v.d.SitePause.Active(now):
		return fmt.Sprintf("%s is Paused by its Volunteer until %s", v.d.Name, when(v.d.SitePause.Until))
	case !st.Open && st.Next.IsZero():
		return fmt.Sprintf("%s has no Availability window ahead", v.d.Name)
	case !st.Open:
		return fmt.Sprintf("%s's Availability window is closed until %s", v.d.Name, when(st.Next))
	case v.d.Health.OnBattery:
		return fmt.Sprintf("%s is on battery; jobs start only on AC power", v.d.Name)
	case st.End.Sub(now) < d:
		return fmt.Sprintf("the job (expected to take %s) doesn't fit in what's left of %s's Availability window, which ends %s", d.Round(time.Minute), v.d.Name, when(st.End))
	}
	return ""
}

// windowStateLocked is the Mac's window settings as its check-ins carried
// them, and whether the Mac is open to the lab now: by those settings, and
// closed under a Pause its Volunteer took on the site. Caller holds v.mu.
func (v *dev) windowStateLocked() (window.Settings, window.State) {
	var s window.Settings
	if v.d.Window != nil {
		s = *v.d.Window
	}
	now := v.s.now()
	if p := v.d.SitePause; p.Active(now) {
		return s, window.Paused(s, p.Until, now)
	}
	return s, window.Evaluate(s, now)
}

// syncWindowLocked takes the window settings a check-in carried as labd's
// copy, and says whether they changed. Only a Volunteer's Mac has them. What
// the Volunteer changed on the Mac is an event of the Mac's record and a
// line for the Lab group: a new weekly schedule, a Pause, an Open now, a
// Resume. A window opening or closing on schedule changes no setting, so it
// is no event. Caller holds v.mu.
func (v *dev) syncWindowLocked(s *window.Settings) bool {
	if s == nil || !v.d.IsVolunteer() {
		return false
	}
	old, _ := json.Marshal(v.d.Window)
	now, _ := json.Marshal(s)
	if string(old) == string(now) {
		return false
	}
	var was window.Settings
	if v.d.Window != nil {
		was = *v.d.Window
	}
	v.d.Window = s
	v.windowChangedLocked(was, *s)
	return true
}

// windowChangedLocked records what the Volunteer changed on the Mac
// between two copies of its window settings. Caller holds v.mu.
func (v *dev) windowChangedLocked(was, is window.Settings) {
	now, name := v.s.now(), v.d.Name
	if was.Schedule() != is.Schedule() {
		words := is.Schedule()
		if words == "" {
			v.said(macWindow, "its Volunteer removed its weekly Availability window")
			words = "none"
		} else {
			v.said(macWindow, "its Volunteer set its weekly Availability window: "+words)
		}
		v.s.labGroup.WindowChanged(name, words)
	}
	// The Mac has no command that takes a Pause or an Open now back: each
	// ends at its time, or gives way to the other.
	switch {
	case is.PausedUntil.After(now) && !is.PausedUntil.Equal(was.PausedUntil):
		v.said(macPaused, "its Volunteer Paused it at the Mac until "+whenDated(is.PausedUntil))
		v.s.labGroup.Paused(name, is.PausedUntil)
	case is.OpenUntil.After(now) && !is.OpenUntil.Equal(was.OpenUntil):
		v.said(macOpenNow, "its Volunteer opened it now, until "+whenDated(is.OpenUntil))
		v.s.labGroup.OpenNow(name, is.OpenUntil)
	}
}

// ---- Pause, taken on the site ----

// sitePaused is why a Pause its Volunteer took on the site cut a job or a
// Lease short.
const sitePaused = "its Volunteer Paused the Mac from the site"

// pauseMac is the Mac's Volunteer taking a Pause on the site: the Mac is
// closed to the lab until then, or until Resume when until is zero. It
// stops a running job as a closing window does, incomplete and queued
// again for a later window, and ends a Lease.
func (s *Server) pauseMac(v *dev, by Person, until time.Time) error {
	if err := s.lends(v, by); err != nil {
		return err
	}
	v.mu.Lock()
	now := s.now()
	if !until.IsZero() && !until.After(now) {
		v.mu.Unlock()
		return refuse(http.StatusUnprocessableEntity, "A Pause ends at a time still to come.")
	}
	v.d.SitePause = &api.SitePause{Since: now, Until: until}
	active := v.cutJobLocked(sitePaused)
	if v.capLeaseLocked() {
		v.leaseCut = sitePaused
	}
	line := "its Volunteer Paused it from the site until they resume it"
	if !until.IsZero() {
		line = "its Volunteer Paused it from the site until " + whenDated(until)
	}
	v.said(macPaused, line)
	name := v.d.Name
	v.saveLocked()
	v.publishLocked()
	v.bump()
	v.mu.Unlock()
	s.labGroup.Paused(name, until)
	if active != "" {
		s.cancelJob(active, sitePaused)
	}
	s.lapse(v)
	return nil
}

// resumeMac undoes the Pause taken on the site, and nothing else: a Pause
// the Volunteer set on the Mac stays, and so does a closed window.
func (s *Server) resumeMac(v *dev, by Person) error {
	if err := s.lends(v, by); err != nil {
		return err
	}
	v.mu.Lock()
	if !v.d.SitePause.Active(s.now()) {
		v.d.SitePause = nil
		v.mu.Unlock()
		return refuse(http.StatusConflict, "No Pause taken on the site holds this Mac. The site undoes only what it took: what was set on the Mac is changed on the Mac.")
	}
	v.d.SitePause = nil
	v.said(macResumed, "its Volunteer resumed it from the site")
	name := v.d.Name
	v.saveLocked()
	v.publishLocked()
	v.bump()
	v.mu.Unlock()
	s.labGroup.Resumed(name)
	return nil
}

// cutJobLocked marks the job running on the Mac as cut short by what its
// Volunteer took away on the site (why), and names it for the caller to
// cancel once v.mu is released; "" when no job runs. The runner ends it
// incomplete, not canceled, puts the Mac back on its own system and queues
// it again. Caller holds v.mu.
func (v *dev) cutJobLocked(why string) string {
	if v.d.ActiveJob == "" {
		return ""
	}
	v.siteCut = siteCut{job: v.d.ActiveJob, why: why}
	return v.d.ActiveJob
}

// siteCut is a running job and what the Mac's Volunteer took away on the
// site that stops it.
type siteCut struct{ job, why string }

// siteCutOf is why the Mac's Volunteer stopped job id from the site, or "".
func (v *dev) siteCutOf(id string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.siteCut.job != id {
		return ""
	}
	return v.siteCut.why
}

// lends is why by may not use the Volunteer's controls of v, or nil: they
// are for the Mac's own Volunteer, on a Mac still in the lab. Anyone else
// is told what a Mac the lab does not have would be.
func (s *Server) lends(v *dev, by Person) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.forgotten || v.d.State == api.StateLeft || !v.d.IsVolunteer() || by.Blocked || by.AccountID == 0 || v.d.VolunteerID != by.AccountID {
		return refuse(http.StatusNotFound, "No Mac of yours has that name.")
	}
	return nil
}

// openUntilLocked is when a Volunteer's Mac's Availability window, open now,
// ends; zero while it is closed. Caller holds v.mu.
func (v *dev) openUntilLocked() time.Time {
	if _, st := v.windowStateLocked(); st.Open {
		return st.End
	}
	return time.Time{}
}

// capLeaseLocked ends a lease on a Volunteer's Mac with its Availability
// window at the latest: at the window's end, or now once it has closed (a
// Pause). An end that moves later doesn't stretch a lease again, and a
// lease cut so is not extended for a job (see liveLeaseLocked): lab-agent
// stops the job. It says whether the lease changed. Caller holds v.mu.
func (v *dev) capLeaseLocked() bool {
	l := v.d.Lease
	if l == nil || !v.d.IsVolunteer() {
		return false
	}
	end, why := v.openUntilLocked(), "the Availability window ended"
	if end.IsZero() {
		end, why = v.s.now(), "the Availability window closed"
	}
	if !l.Expires.After(end) {
		return false
	}
	capped := *l
	capped.Expires = end
	v.d.Lease, v.leaseCut = &capped, why
	return true
}

// windowClosed is whether labd's copy of a Volunteer's Mac's Availability
// window is closed now; a Lab-owned Mac's never is.
func (v *dev) windowClosed() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.d.IsVolunteer() && v.openUntilLocked().IsZero()
}

// paused is whether a Pause closes the Mac now: one its Volunteer took on
// the site, or one they set on the Mac.
func (v *dev) paused() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.d.IsVolunteer() {
		return false
	}
	now := v.s.now()
	if v.d.SitePause.Active(now) {
		return true
	}
	return v.d.Window != nil && v.d.Window.PausedUntil.After(now) && v.openUntilLocked().IsZero()
}
