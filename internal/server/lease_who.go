package server

// What a person is to the Lease rule, asked in one place. Until roles are
// kept on People, a person is an Admin or a Maintainer by what sign-in
// left (Config.Admins, people.maintainer), nobody is Blocked or an External
// volunteer, and the M team's mark is held in memory for the tests alone:
// no screen sets it. The roles on People replace this file.

import (
	"context"
	"net/http"
)

// asker is the person a request about a Lease acts for.
type asker struct {
	AccountID int64
	Handle    string
	// Maintainer is a Maintainer or an Admin; MTeam an M team member who is
	// neither. Neither is anyone else, who takes no Lease.
	Maintainer, MTeam bool
}

// kind is the person's kind beside their handle.
func (a asker) kind() string {
	switch {
	case a.Maintainer:
		return "Maintainer"
	case a.MTeam:
		return "M team"
	}
	return ""
}

// asking is who r asks as: the person whose Lab token it carries, or the
// one signed in on its browser.
func (s *Server) asking(r *http.Request) asker {
	if p := requestPerson(r); p != nil {
		return s.askerOf(*p)
	}
	return asker{}
}

// requestPerson is the person r acts as, by Lab token or browser session.
func requestPerson(r *http.Request) *Person {
	if u := tokenUseOf(r); u != nil {
		if u.expired {
			return nil
		}
		return &u.Person
	}
	return PersonOf(r)
}

func (s *Server) askerOf(p Person) asker {
	a := asker{AccountID: p.AccountID, Handle: p.Handle, Maintainer: p.isMaintainer()}
	_, marked := s.mteamForNow.Load(p.AccountID)
	a.MTeam = marked && !a.Maintainer
	return a
}

// askerByAccount is the person with this account id as the lab knows them
// now; ok is false for an account it does not know.
func (s *Server) askerByAccount(accountID int64) (a asker, ok bool) {
	p, err := s.personNow(accountID)
	if err != nil {
		return asker{}, false
	}
	return s.askerOf(p), true
}

// lends reports whether the Mac is one a lends: its Volunteer. A Mac does
// not carry its Volunteer's account yet, so like the mark this is held in
// memory for the tests alone.
func (s *Server) lends(a asker, v *dev) bool {
	id, ok := s.lentForNow.Load(v.snapshot().Name)
	return ok && a.AccountID != 0 && id == a.AccountID
}

// teamUp admits a Maintainer or an M team member: the routes of a Lease.
// The request ends when the role or the mark goes, or the session or the
// Lab token it came with.
func (s *Server) teamUp(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch a, who := s.access(r); a {
		case adminAccess:
			s.asMaintainer(w, r, who, h)
		case signedInAccess:
			if !s.asking(r).MTeam {
				httpErr(w, 403, "@%s is signed in with no role in the lab; a Lease is for the M team and Maintainers", who)
				return
			}
			noteCaller(r, "person:"+who)
			ctx, cancel := context.WithCancel(r.Context())
			var keys []string
			if u := tokenUseOf(r); u != nil {
				keys = []string{accountConnKey(u.AccountID), tokenConnKey(u.token.ID)}
			} else if si := signedInOf(r); si != nil {
				keys = si.connKeys()
			}
			defer s.conns.add(cancel, keys...)()
			h(w, r.WithContext(ctx))
		case viewAccess:
			httpErr(w, 403, "%s may only look; a Lease is for the M team and Maintainers", who)
		default:
			s.noCredential(w, r)
		}
	}
}
