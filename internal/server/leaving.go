package server

// A Mac's enrollment ends from either side: its Volunteer runs
// `sudo lab-agent leave` or leaves from My Macs, a Maintainer ends it in
// labd, or its Volunteer is Blocked. Either way the Mac is gone from labd at
// once, its record Retired and its device secret revoked, and a job running
// on it ends incomplete. When it was ended in the lab, labd remembers whose
// secret it was and who ended it, so a Mac that checks in again with that
// secret learns it was ended, not just revoked, and cleans up.

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// endedHeader carries, on the 401 an ended Mac's old secret gets, who
// ended its enrollment.
const endedHeader = api.EndedHeader

var errMacGone = errors.New("the Mac is no longer enrolled")

// goneReason is why the Mac left the lab, or "" while it is enrolled.
func (v *dev) goneReason() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.goneWhy
}

// leaving is how a Mac's enrollment ends: who ended it (leftByVolunteer,
// leftByMaintainer, leftBlocked) and the person who acted. by is the
// Maintainer who ended it or who Blocked its Volunteer; for the Volunteer
// it is the Volunteer when they left from the site, and nobody when the
// Mac itself told the lab (`sudo lab-agent leave`). atMac is that case:
// the Mac has left already and has nothing more to learn.
type leaving struct {
	how   string
	by    Person
	atMac bool
}

// reason is the ending in the words a job cut short by it carries.
func (l leaving) reason() string {
	switch l.how {
	case leftByMaintainer:
		return "the Maintainer " + l.by.Handle + " ended its enrollment"
	case leftBlocked:
		return "its Volunteer was Blocked"
	case leftForgotten:
		return "its Volunteer was Forgotten"
	}
	return "its Volunteer left the lab"
}

// endEnrollment has v leave the lab, as l says. Its record is Retired at
// once: out of the fleet, its name free, counting toward no cap, its device
// secret revoked. Its screenshots, serial log and baselines are deleted;
// its jobs, their results, labd's copy of its Audit log and the Mac's
// record stay for Maintainers. A job running on it ends incomplete. The
// leaving is a line of the Mac's record, of the People record and for the
// Lab group. Unless the Mac itself said it left, labd remembers whose
// secret it was and who ended it: a Mac that was out of reach learns it
// was ended when it next reaches the lab, and finishes leaving then. It is
// false when the Mac was not enrolled any more.
func (s *Server) endEnrollment(v *dev, l leaving) bool {
	reason := l.reason()
	v.mu.Lock()
	name, secretHash, e := v.d.Name, v.secretHash, v.d.Enrollment
	// As lab forget does: nothing still holding v may write its row back.
	if v.forgotten {
		v.mu.Unlock()
		return false
	}
	v.forgotten = true
	d := v.d
	v.mu.Unlock()
	s.endLeaseOfLeaving(v, reason)
	s.mu.Lock()
	if s.devs[name] != v {
		s.mu.Unlock()
		return false
	}
	// The record of who ended it is written before the Mac disappears, so its
	// secret is never unknown without an answer to why.
	if !l.atMac {
		if err := s.store.addEnded(secretHash, name, l.by.Handle, s.now()); err != nil {
			s.log.Printf("record that %s ended %s's enrollment: %v", l.by.Handle, name, err)
		}
	}
	delete(s.devs, name)
	s.mu.Unlock()
	s.retired(&d, l)

	v.mu.Lock()
	v.goneWhy = reason
	v.d.Lease = nil
	active := v.d.ActiveJob
	close(v.stop)
	v.bump()
	v.mu.Unlock()
	s.log.Printf("%s: enrollment ended: %s", name, reason)

	// The job holding the Mac ends incomplete (see runJob); the ones queued
	// behind it have no Mac to run on.
	s.mu.Lock()
	cancel := s.cancels[active]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if jobs, err := s.store.jobs(name, 1000, true); err == nil {
		for _, j := range jobs {
			if j.ID != active {
				s.updateJobByID(j.ID, func(j *api.Job) {
					j.State, j.Outcome, j.Summary = api.JobDone, api.OutcomeCanceled, "canceled: "+reason
				})
			}
		}
	}

	if l.how != leftByVolunteer && e.IsVolunteer() {
		by := l.by.Handle
		s.notify(Notification{Device: name, Title: fmt.Sprintf("Enrollment of %s ended by %s", name, by),
			Body:     fmt.Sprintf("The Maintainer %s ended your Mac's enrollment in the lab. Its lab-agent removes the lab from your Mac at its next check-in; `sudo lab-agent log` records it.", by),
			Priority: "high", Volunteer: true, MacTopic: s.macTopicURL(e)})
	}
	return true
}

// retired does what leaving the lab does to a Mac's record, d as it stood:
// the three lines (the Mac's record, the People record, the Lab group),
// then the record is Retired and what labd kept under the Mac's name goes.
func (s *Server) retired(d *api.Device, l leaving) {
	line := "left the lab: its Volunteer ended it"
	switch l.how {
	case leftByMaintainer:
		line = "left the lab: a Maintainer ended it"
		s.labGroup.MacLeftByMaintainer(d.Name)
	case leftBlocked:
		line = "left the lab: its Volunteer was Blocked"
		s.labGroup.MacLeftVolunteerBlocked(d.Name)
	case leftForgotten:
		line = "left the lab: a Maintainer ended it"
		s.labGroup.MacLeftByMaintainer(d.Name)
	default:
		s.labGroup.MacLeftByVolunteer(d.Name)
	}
	s.macSaid(d.Name, d.ID, macLeft, line)
	s.leftOnPeopleRecord(d, l)
	if err := s.store.retire(d, l.how, s.now()); err != nil {
		s.log.Printf("retire %s: %v", d.Name, err)
	}
	if _, err := s.store.dropTokens(d.Name); err != nil {
		s.log.Printf("retire %s: its enrollment tokens: %v", d.Name, err)
	}
	s.dropNamedState(d.Name, d.ID)
}

// leftOnPeopleRecord writes a Mac's leaving in the People record, which is
// for Maintainers: who ended it, and whose Mac it was.
func (s *Server) leftOnPeopleRecord(d *api.Device, l leaving) {
	var whose *Person
	if d.VolunteerID != 0 {
		if p, err := s.person(d.VolunteerID); err == nil {
			whose = &p
		}
	}
	line := recordLine{At: s.now(), ActorID: l.by.AccountID, Actor: l.by.Handle}
	switch {
	case l.how == leftByVolunteer && whose == nil:
		return // a Mac enrolled before the lab kept people: nobody to name
	case l.how == leftByVolunteer:
		line.ActorID, line.Actor, line.What = whose.AccountID, whose.Handle, "left the lab with "+d.Name
	case l.by.Handle == "":
		return
	default:
		line.What = "ended the enrollment of " + d.Name
		switch l.how {
		case leftBlocked:
			line.What = "blocked its Volunteer, which ended the enrollment of " + d.Name
		case leftForgotten:
			line.What = "forgot its Volunteer, which ended the enrollment of " + d.Name
		}
		if whose != nil {
			line.What += ", a Mac of"
			line.AboutID, line.About = whose.AccountID, whose.Handle
		}
	}
	if err := s.store.addRecord(line); err != nil {
		s.log.Printf("the People record, %s: %v", line, err)
	}
}

// endEnrollmentsOf has every Mac the person lends leave the lab, as l says.
func (s *Server) endEnrollmentsOf(accountID int64, l leaving) {
	for _, name := range s.macsLentBy(accountID) {
		if v := s.dev(name); v != nil {
			s.endEnrollment(v, l)
		}
	}
}

// leaveMac is the Mac's Volunteer leaving the lab from the site. The Mac
// learns of it when it next reaches the lab, and removes the lab then.
func (s *Server) leaveMac(v *dev, by Person) error {
	if err := s.lends(v, by); err != nil {
		return err
	}
	if !s.endEnrollment(v, leaving{how: leftByVolunteer, by: by}) {
		return refuse(http.StatusNotFound, "No Mac of yours has that name.")
	}
	return nil
}

// hEndEnrollment is a Maintainer ending a Mac's enrollment (`lab remove`).
func (s *Server) hEndEnrollment(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	v := s.dev(name)
	if v == nil || !s.endEnrollment(v, leaving{how: leftByMaintainer, by: leaser(r)}) {
		httpErr(w, 404, "no device %q", name)
		return
	}
	w.Write([]byte("enrollment ended\n"))
}

// hAgentLeave is a Mac telling labd its Volunteer has left the lab.
func (s *Server) hAgentLeave(w http.ResponseWriter, r *http.Request) {
	v := agentDev(r)
	s.endEnrollment(v, leaving{how: leftByVolunteer, atMac: true})
	w.Write([]byte("left\n"))
}

// ended answers a request carrying the secret of a Mac whose enrollment a
// Maintainer ended: 401, with the Maintainer's name for the Mac to read.
func (s *Server) ended(w http.ResponseWriter, r *http.Request, secret string) bool {
	by, err := s.store.endedBy(hashSecret(secret))
	if err != nil {
		return false
	}
	w.Header().Set(endedHeader, by)
	s.refuse(w, r, 401, "enrollment ended by %s", by)
	return true
}

func (st *store) addEnded(secretHash, name, by string, at time.Time) error {
	_, err := st.db.Exec(`INSERT OR REPLACE INTO ended(secret_hash,name,maintainer,at) VALUES(?,?,?,?)`, secretHash, name, by, at.Unix())
	return err
}

// endedBy is the Maintainer who ended the enrollment the secret belonged to.
func (st *store) endedBy(secretHash string) (string, error) {
	var by string
	err := st.db.QueryRow(`SELECT maintainer FROM ended WHERE secret_hash=?`, secretHash).Scan(&by)
	return by, err
}
