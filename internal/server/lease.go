package server

// A Lease holds a Mac for one person, a Maintainer or an M team member: the
// account id here, the handle in the field the agent's commands already
// carry, the free-text holder label inside it. Everything about it is
// labd's; the running agent knows nothing of Leases.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// A Lease lasts leaseDefault unless a Maintainer asks for longer, up to
// leaseLongest. A renewal with no length asked ends leaseDefault from now.
const (
	leaseDefault = 4 * time.Hour
	leaseLongest = 24 * time.Hour
)

// leaser is the person r asks about a Lease as, by Lab token or browser
// session; nobody for neither, who takes no Lease.
func leaser(r *http.Request) Person {
	if p := credentialOf(r); p != nil {
		return *p
	}
	return Person{}
}

// teamUp admits a Maintainer or an M team member: the routes of a Lease.
// The request ends when the role or the mark goes, or the session or the
// Lab token it came with.
func (s *Server) teamUp(h http.HandlerFunc) http.HandlerFunc {
	const need = "a Lease is for the M team and Maintainers"
	return func(w http.ResponseWriter, r *http.Request) {
		switch a, who := s.access(r); a {
		case adminAccess:
			s.asMaintainer(w, r, who, h)
		case teamAccess:
			s.asSignedIn(w, r, teamKey{}, h)
		case ownAccess:
			httpErr(w, 403, "@%s lends a Mac and sees their own Macs; %s", who, need)
		case signedInAccess:
			httpErr(w, 403, "@%s is signed in with no role in the lab; %s", who, need)
		case viewAccess:
			httpErr(w, 403, "%s may only look; %s", who, need)
		case blockedAccess:
			httpErr(w, 403, "%s", blockedSentence)
		default:
			s.noCredential(w, r)
		}
	}
}

// leaseIsOf is whether l is p's Lease, whatever its holder label: by account
// id, and for a Lease taken before labd kept people, by the name recorded.
func leaseIsOf(l *api.Lease, p Person) bool {
	if l.AccountID != 0 {
		return l.AccountID == p.AccountID
	}
	return l.Owner() != "" && l.Owner() == p.Handle
}

// liveLeaseLocked is the Mac's Lease while it runs, else nil. Caller holds
// v.mu.
func (v *dev) liveLeaseLocked() *api.Lease {
	if l := v.d.Lease; l != nil && v.s.now().Before(l.Expires) {
		return l
	}
	return nil
}

// leased is whether a Lease holds the Mac now: labd's scheduled refresh
// skips it, and it is not free.
func (s *Server) leased(v *dev) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.liveLeaseLocked() != nil
}

// holdsLease is whether p holds the Lease on v now, under any holder label.
func (s *Server) holdsLease(v *dev, p Person) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	l := v.liveLeaseLocked()
	return l != nil && leaseIsOf(l, p)
}

// leaseAsked is what the Lease rule found for a person asking for a Lease
// on a Mac: that they would renew the one they hold, or what a Maintainer
// is warned of before going on.
type leaseAsked struct {
	renews  bool
	warning string
}

// mayLease is the Lease rule for p taking or renewing a Lease on v under
// this holder label: a refusal, or what was found. Only a Maintainer or an
// M team member; only a Mac that passed its baseline and has not left; a
// Volunteer's Mac only inside its Availability window, at either Access
// level; not while someone else holds it; and one Lease per Configuration
// for an M team member, where a Maintainer is only warned.
func (s *Server) mayLease(v *dev, p Person, holder string) (leaseAsked, error) {
	var found leaseAsked
	if !p.OnMTeam() {
		return found, refuse(403, "a Lease is for the M team and Maintainers")
	}
	v.mu.Lock()
	name := v.d.Name
	var err error
	switch l := v.liveLeaseLocked(); {
	case v.forgotten || v.d.State == api.StateLeft:
		err = refuse(409, "%s has left the lab", name)
	case v.d.KnownGood == "":
		err = refuse(409, "%s has not passed its baseline yet, so it takes no Lease", name)
	case v.d.IsVolunteer() && v.openUntilLocked().IsZero():
		err = refuse(409, "%s's Availability window is closed: a Lease on a Volunteer's Mac lasts only inside it", name)
	case l != nil && !(leaseIsOf(l, p) && l.Holder == holder):
		err = refuse(409, "%s is leased by %s until %s", name, l.Who(), l.Expires.Format(time.TimeOnly))
	case l != nil:
		found.renews = true
	}
	v.mu.Unlock()
	if err != nil || found.renews {
		return found, err
	}
	if held, same := s.heldOfConfiguration(v, p); held != "" {
		if !p.isMaintainer() {
			return found, refuse(409, "@%s already holds a Lease on %s: one Lease per Configuration for an M team member. Release %s first", p.Handle, same, held)
		}
		found.warning = "you already hold a Lease on " + same + "; an M team member would be refused a second"
	}
	return found, nil
}

// heldOfConfiguration names a Mac other than v that p leases now and that
// counts as v's Configuration, and says in what way it does; "" when p
// holds none.
func (s *Server) heldOfConfiguration(v *dev, p Person) (held, same string) {
	mine := s.configuration(v)
	for _, o := range s.allDevs() {
		if o == v || !s.holdsLease(o, p) {
			continue
		}
		theirs := s.configuration(o)
		if !mine.Same(theirs) {
			continue
		}
		name := o.snapshot().Name
		if mine.Known() && theirs.Known() {
			return name, fmt.Sprintf("%s, the same Configuration (%s)", name, theirs)
		}
		return name, fmt.Sprintf("%s (%s), and %s (%s) counts as the same Configuration until a Maintainer sets the core counts", name, theirs, v.snapshot().Name, mine)
	}
	return "", ""
}

// takeLease takes a Lease on v for p, or renews the one p holds under this
// holder label, for ttl; 0 is leaseDefault. On a Volunteer's Mac it is cut
// to the window's end. The Lease it returns carries the warning, which is
// not kept.
func (s *Server) takeLease(v *dev, p Person, holder string, ttl time.Duration) (api.Lease, error) {
	if holder == "" {
		return api.Lease{}, refuse(400, "holder required")
	}
	if ttl <= 0 {
		ttl = leaseDefault
	}
	switch {
	case p.isMaintainer() && ttl > leaseLongest:
		return api.Lease{}, refuse(400, "a Lease lasts 24 hours at most")
	case !p.isMaintainer() && ttl > leaseDefault:
		return api.Lease{}, refuse(400, "an M team member's Lease lasts 4 hours at most; renew it at any time, and it ends 4 hours from then")
	}
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	found, err := s.mayLease(v, p, holder)
	if err != nil {
		return api.Lease{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.d.Lease = &api.Lease{Holder: holder, Maintainer: p.Handle, By: p.Handle, AccountID: p.AccountID, Expires: s.now().Add(ttl)}
	v.capLeaseLocked()
	v.saveLocked()
	taken := *v.d.Lease
	taken.Warning = found.warning
	return taken, nil
}

// releaseLease ends p's Lease on v; holder, when given, must be its label.
// With force a Maintainer ends another person's, which labd records.
func (s *Server) releaseLease(v *dev, p Person, holder string, force bool) error {
	v.mu.Lock()
	l := v.liveLeaseLocked()
	name := v.d.Name
	v.mu.Unlock()
	switch {
	case l == nil || leaseIsOf(l, p) && (holder == "" || l.Holder == holder):
		s.endLease(v, nil, "")
	case !force && !leaseIsOf(l, p):
		return refuse(409, "%s is leased by %s until %s; only %s can release it, or a Maintainer with `lab release %s --force`", name, l.Who(), l.Expires.Format(time.TimeOnly), orStr(l.Owner(), l.Holder), name)
	case !force:
		return refuse(409, "leased by %s, not %s", l.Holder, holder)
	case !p.isMaintainer():
		return refuse(403, "only a Maintainer ends another person's Lease")
	case !s.endLease(v, l, "ended early by Maintainer "+p.Handle+" (lab release --force)"):
		return refuse(409, "%s's lease changed meanwhile; look again before releasing it", name)
	}
	return nil
}

func (s *Server) hLease(w http.ResponseWriter, r *http.Request) {
	v := s.devOr404(w, r)
	if v == nil {
		return
	}
	var req api.LeaseRequest
	json.NewDecoder(r.Body).Decode(&req)
	l, err := s.takeLease(v, leaser(r), req.Holder, time.Duration(req.TTLSec)*time.Second)
	if err != nil {
		httpErr(w, statusOf(err), "%v", err)
		return
	}
	writeJSON(w, l)
}

func (s *Server) hRelease(w http.ResponseWriter, r *http.Request) {
	v := s.devOr404(w, r)
	if v == nil {
		return
	}
	force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
	if err := s.releaseLease(v, leaser(r), r.URL.Query().Get("holder"), force); err != nil {
		httpErr(w, statusOf(err), "%v", err)
		return
	}
	w.Write([]byte("ok"))
}

// parkedJob is a queued job someone else's lease holds back: who it is for,
// so labd can tell when the lease no longer holds it.
type parkedJob struct {
	id, holder, maintainer string
}

// leaseHoldsBackLocked says why the Mac's lease keeps a job of holder's,
// submitted by maintainer, from running now, or "" when it doesn't: no lease
// is live, or the job is its holder's own. Caller holds v.mu.
func (v *dev) leaseHoldsBackLocked(holder, maintainer string) string {
	l := v.d.Lease
	if l == nil || !v.s.now().Before(l.Expires) || l.HeldBy(holder, maintainer) {
		return ""
	}
	return fmt.Sprintf("%s is leased by %s until %s", v.d.Name, l.Who(), l.Expires.Format(time.TimeOnly))
}

// park sets a queued job aside while someone else's lease holds the Mac, so
// the lease holder's jobs queued after it run first, and says whether it did.
// unpark queues it again once the lease no longer holds it.
func (r *jobRun) park() bool {
	v := r.v
	v.mu.Lock()
	why := v.leaseHoldsBackLocked(r.j.Spec.Holder, r.j.Maintainer)
	if why != "" {
		v.parked = append(v.parked, parkedJob{r.j.ID, r.j.Spec.Holder, r.j.Maintainer})
	}
	v.mu.Unlock()
	if why == "" {
		return false
	}
	r.state(api.JobQueued)
	r.ev("%s; waiting for the lease to end", why)
	return true
}

// unpark queues again, in the order they were set aside, the jobs a lease no
// longer holds back: it ended, ran out or was released. It never waits for
// room in the queue: deviceLoop, the queue's only reader, calls it, so a
// full queue would stop the Mac for good. What doesn't fit stays parked, in
// order, for the next pass.
func (v *dev) unpark() {
	v.mu.Lock()
	defer v.mu.Unlock()
	keep := v.parked[:0]
	full := false
	for _, p := range v.parked {
		if !full && v.leaseHoldsBackLocked(p.holder, p.maintainer) == "" {
			select {
			case v.queue <- p.id:
				continue
			default:
				full = true
			}
		}
		keep = append(keep, p)
	}
	clear(v.parked[len(keep):])
	v.parked = keep
}

// endLease ends v's lease and queues again the jobs it held back. When l, the
// lease as the caller saw it, is ended by someone other than its own
// Maintainer, why says by whom: labd's log records it, and so does each job
// it had set aside. It says whether it ended the lease: not when l is no
// longer v's lease, renewed or ended meanwhile. Caller doesn't hold v.mu.
func (s *Server) endLease(v *dev, l *api.Lease, why string) bool {
	v.mu.Lock()
	if l != nil && v.d.Lease != l {
		v.mu.Unlock()
		return false
	}
	v.d.Lease = nil
	v.saveLocked()
	var aside []string
	for _, p := range v.parked {
		aside = append(aside, p.id)
	}
	name := v.d.Name
	v.mu.Unlock()
	if l != nil {
		msg := fmt.Sprintf("%s's lease by %s, until %s, %s", name, l.Who(), l.Expires.Format(time.DateTime), why)
		s.log.Printf("%s", msg)
		for _, id := range aside {
			s.updateJobByID(id, func(j *api.Job) { j.Events = append(j.Events, api.JobEvent{Time: s.now(), Msg: msg}) })
		}
	}
	v.unpark()
	return true
}

// endLeasesOf ends the leases the Maintainer name holds, as when they are
// removed: nobody else could release them, and the jobs they hold back
// would wait for them to run out.
func (s *Server) endLeasesOf(name, why string) {
	for _, v := range s.allDevs() {
		v.mu.Lock()
		l := v.d.Lease
		mine := l != nil && l.Owner() == name && s.now().Before(l.Expires)
		v.mu.Unlock()
		if mine {
			s.endLease(v, l, why)
		}
	}
}
