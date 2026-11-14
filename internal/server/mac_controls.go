package server

// The Volunteer's controls on the site. The Mac sets the ceiling: only the
// Mac gives (Open now, the weekly schedule, raising to Full, turning
// publishing on are `sudo lab-agent …` at the Mac). The site only takes
// away, and undoes only what it took: Pause until a time or Resume, Limit
// to Qualification until "Remove limit", and Leave. They are the buttons of
// a Mac's card on My Macs, and work for the Mac's own Volunteer only.

import (
	"errors"
	"net/http"
	"time"

	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("POST /my-macs/{name}/pause", web.SignedIn, (*Server).pPauseMac)
	addPage("POST /my-macs/{name}/resume", web.SignedIn, controlOf((*Server).resumeMac))
	addPage("POST /my-macs/{name}/limit", web.SignedIn, controlOf((*Server).limitMac))
	addPage("POST /my-macs/{name}/remove-limit", web.SignedIn, controlOf((*Server).unlimitMac))
	addPage("POST /my-macs/{name}/leave", web.SignedIn, (*Server).pLeaveMac)
	addPage("POST /people/{id}/enrollments/{mac}/end", web.Maintainers, (*Server).pEndEnrollment)
}

// myMac is the Mac a button of My Macs names, when the person signed in on
// r lends it; otherwise it answers as for a page the lab does not have.
func (s *Server) myMac(w http.ResponseWriter, r *http.Request) *dev {
	v := s.dev(r.PathValue("name"))
	if p := PersonOf(r); v == nil || p == nil || s.lends(v, *p) != nil {
		s.notFound(w, r)
		return nil
	}
	return v
}

// controlDone answers a button of My Macs: back to My Macs, or the Mac's
// own page saying why the lab refused.
func (s *Server) controlDone(w http.ResponseWriter, r *http.Request, v *dev, err error) {
	var no *refusal
	switch {
	case err == nil:
		http.Redirect(w, r, s.basePath()+"/my-macs", http.StatusSeeOther)
	case errors.As(err, &no) && no.code == http.StatusNotFound:
		s.notFound(w, r)
	case errors.As(err, &no):
		s.renderMac(w, r, v, tabOverview, err)
	default:
		s.log.Printf("My Macs, %s: %v", r.URL.Path, err)
		s.renderMac(w, r, v, tabOverview, refuse(http.StatusInternalServerError, "The lab couldn't do that."))
	}
}

func controlOf(control func(*Server, *dev, Person) error) func(*Server, http.ResponseWriter, *http.Request) {
	return func(s *Server, w http.ResponseWriter, r *http.Request) {
		if v := s.myMac(w, r); v != nil {
			s.controlDone(w, r, v, control(s, v, *PersonOf(r)))
		}
	}
}

// pPauseMac takes a Pause: with no "until", until Resume; otherwise until
// that time, written as the Mac's own clock reads (2026-11-20T07:00, in the
// zone of its Availability window) or with its zone (RFC 3339).
func (s *Server) pPauseMac(w http.ResponseWriter, r *http.Request) {
	v := s.myMac(w, r)
	if v == nil {
		return
	}
	var until time.Time
	if raw := r.PostFormValue("until"); raw != "" {
		var err error
		if until, err = time.Parse(time.RFC3339, raw); err != nil {
			v.mu.Lock()
			settings, _ := v.windowStateLocked()
			v.mu.Unlock()
			if until, err = time.ParseInLocation("2006-01-02T15:04", raw, settings.Location()); err != nil {
				s.controlDone(w, r, v, refuse(http.StatusUnprocessableEntity, "Pause until a date and a time, like 2026-11-20T07:00, or leave it empty to Pause until you Resume."))
				return
			}
		}
	}
	s.controlDone(w, r, v, s.pauseMac(v, *PersonOf(r), until))
}

// pLeaveMac is Leave, which asks twice.
func (s *Server) pLeaveMac(w http.ResponseWriter, r *http.Request) {
	v := s.myMac(w, r)
	if v == nil {
		return
	}
	name := v.snapshot().Name
	if !s.sure(w, r, "Leave: "+name+" leaves the lab now, any job on it stops, and the lab is removed from it when it next reaches the lab", "/my-macs") {
		return
	}
	s.controlDone(w, r, v, s.leaveMac(v, *PersonOf(r)))
}

// pEndEnrollment is "End enrollment" beside one of a person's Macs on
// People: a Maintainer ends it as `lab remove` does. It asks twice.
func (s *Server) pEndEnrollment(w http.ResponseWriter, r *http.Request) {
	k, err := s.personAt(r)
	mac := r.PathValue("mac")
	v := s.dev(mac)
	if err == nil && (v == nil || v.snapshot().VolunteerID != k.AccountID) {
		err = refuse(http.StatusConflict, "@%s lends no Mac called %s.", k.Handle, mac)
	}
	if err == nil {
		if !s.sure(w, r, "End the enrollment of "+mac+": it leaves the lab now, and any job on it stops", "/people?open="+r.PathValue("id")) {
			return
		}
		if !s.endEnrollment(v, leaving{how: leftByMaintainer, by: *PersonOf(r)}) {
			err = refuse(http.StatusConflict, "@%s lends no Mac called %s.", k.Handle, mac)
		}
	}
	s.done(w, r, k.AccountID, err)
}
