package server

// Maintainers are people with the Maintainer role, and Admins. A request
// acts as one with their browser's session or with a Lab token of theirs,
// and what labd records (jobs, Promotions, Leases) carries their handle.
// maclab's named Maintainers, each with a token of their own, and the
// admin.token file of its first one are retired: neither opens anything.

import (
	"context"
	"net/http"
	"sync"

	"github.com/omacom/omarchy-maclab/internal/web"
)

type maintainerKey struct{}

// maintainerConns are the open requests under each name, so that what ends
// a person's access also ends their streams and waits. A request is open
// under every name that can end it (see accountConnKey).
type maintainerConns struct {
	mu   sync.Mutex
	next int
	by   map[string]map[int]context.CancelFunc
}

func (c *maintainerConns) add(cancel context.CancelFunc, names ...string) (done func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.by == nil {
		c.by = map[string]map[int]context.CancelFunc{}
	}
	c.next++
	id := c.next
	for _, name := range names {
		if c.by[name] == nil {
			c.by[name] = map[int]context.CancelFunc{}
		}
		c.by[name][id] = cancel
	}
	return func() {
		c.mu.Lock()
		for _, name := range names {
			delete(c.by[name], id)
			if len(c.by[name]) == 0 {
				delete(c.by, name)
			}
		}
		c.mu.Unlock()
		cancel()
	}
}

func (c *maintainerConns) end(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cancel := range c.by[name] {
		cancel()
	}
	delete(c.by, name)
}

// maintainer admits a request by a Maintainer and passes on their handle
// (see maintainerName). The request ends when the role, the session or the
// token it came with goes. A person without the role is told so.
func (s *Server) maintainer(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch a, who := s.access(r); a {
		case adminAccess:
			s.asMaintainer(w, r, who, h)
		case viewAccess:
			httpErr(w, 403, "%s may only look; this needs a Maintainer", who)
		case signedInAccess:
			httpErr(w, 403, "@%s is signed in with no role in the lab; this needs a Maintainer", who)
		case teamAccess:
			httpErr(w, 403, "@%s is an M team member; this needs a Maintainer", who)
		case ownAccess:
			httpErr(w, 403, "@%s lends a Mac and sees their own Macs; this needs a Maintainer", who)
		case blockedAccess:
			httpErr(w, 403, "%s", blockedSentence)
		default:
			s.noCredential(w, r)
		}
	}
}

// viewer guards read-only endpoints, which a person labd knows may use too,
// and passes on who they are (see callerOf): what they see is their scope.
func (s *Server) viewer(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch a, who := s.access(r); a {
		case adminAccess:
			s.asMaintainer(w, r, who, h)
		case viewAccess:
			asPerson(w, r, who, h)
		case teamAccess:
			s.asSignedIn(w, r, teamKey{}, h)
		case ownAccess:
			s.asSignedIn(w, r, personKey{}, h)
		case signedInAccess:
			httpErr(w, 403, "@%s is signed in with no role in the lab, which shows the public site only", who)
		case blockedAccess:
			httpErr(w, 403, "%s", blockedSentence)
		default:
			s.noCredential(w, r)
		}
	}
}

type access int

const (
	noAccess access = iota
	viewAccess
	adminAccess
	signedInAccess // signed in with a browser, with no role: the public site only
	teamAccess     // an M team member: reads the whole fleet
	ownAccess      // an External volunteer: reads their own Macs
	blockedAccess  // a Blocked person: nothing, and the one sentence
)

// access decides what a request may do. A Lab token and a browser's session
// both act as their person, with the person's roles at this moment: an
// Admin or a Maintainer gets admin, and who is their handle; an M team
// member reads the fleet, an External volunteer their own Macs, and anyone
// else opens no route here. A person labd knows any other way
// (Config.Person) is a viewer, and who is their login. A Mac, Lab-owned or a
// Volunteer's, talks to labd with its own device secret.
func (s *Server) access(r *http.Request) (a access, who string) {
	if u := tokenUseOf(r); u != nil {
		if u.expired && !u.Blocked {
			return noAccess, ""
		}
		return u.access(), u.Handle
	}
	if si := signedInOf(r); si != nil {
		return si.access(), si.Handle
	}
	if s.cfg.Person != nil {
		if login := s.cfg.Person(r); login != "" {
			return viewAccess, login
		}
	}
	return noAccess, ""
}

// asMaintainer serves r for the Maintainer name until it ends, or their
// role goes, or the session or the Lab token r came with does.
func (s *Server) asMaintainer(w http.ResponseWriter, r *http.Request, name string, h http.HandlerFunc) {
	noteCaller(r, "person:"+name)
	ctx, cancel := context.WithCancel(context.WithValue(r.Context(), maintainerKey{}, name))
	defer s.conns.add(cancel, connKeysOf(r)...)()
	h(w, r.WithContext(ctx))
}

// connKeysOf are the names r is open under: those of the Lab token or the
// browser session it came with.
func connKeysOf(r *http.Request) []string {
	if u := tokenUseOf(r); u != nil {
		return []string{accountConnKey(u.AccountID), tokenConnKey(u.token.ID)}
	}
	if si := signedInOf(r); si != nil {
		return si.connKeys()
	}
	return nil
}

// credentialOf is the person r's Lab token or browser session belongs to,
// nil for neither.
func credentialOf(r *http.Request) *Person {
	if u := tokenUseOf(r); u != nil {
		return &u.Person
	}
	return PersonOf(r)
}

// maintainerName is the Maintainer who made a request on a Maintainer route.
func maintainerName(r *http.Request) string {
	return maintainerOf(r.Context())
}

// maintainerOf is the Maintainer a request's context acts for; "" for labd.
func maintainerOf(ctx context.Context) string {
	name, _ := ctx.Value(maintainerKey{}).(string)
	return name
}

// hMaintainersGone answers maclab's three Maintainer routes, which made and
// removed named Maintainers with a token each.
func (s *Server) hMaintainersGone(w http.ResponseWriter, r *http.Request) {
	httpErr(w, http.StatusGone, "Maintainers are people now, and have no token of their own kind: an Admin makes a Maintainer on People, and each person makes their Lab tokens on their own page, %s", s.publicBase(r)+web.OwnPage)
}
