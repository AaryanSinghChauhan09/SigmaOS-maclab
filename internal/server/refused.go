package server

// Refused credentials. labd counts each credential it does not admit against
// the address it came from: a Lab token, a Mac's device secret, a builder's
// token, an enrollment token, a sign-in that GitHub or labd turns down. Past
// refusedLimit in refusedWindow from one address, further bad ones from it
// get 429 for refusedFor, and "Agents" is told once. A valid credential is
// never counted and never refused, so it always passes.
//
// The address is the one the edge names on the tunnel's socket (see
// callerAddr). A caller at the loopback port is on the mini itself and is
// not counted. A request that carries no credential is refused without
// being counted, and so is a browser's cookie labd no longer knows: that
// browser is signed out, not guessing. Each counted refusal is marked in
// the caller log, which is where the address is named.

import (
	"net/http"
	"sync"
	"time"

	"github.com/omacom/omarchy-maclab/internal/web"
)

const (
	refusedLimit  = 10
	refusedWindow = 10 * time.Minute
	refusedFor    = 15 * time.Minute
)

const refusedSentence = "Too many refused credentials from your network: over 10 in 10 minutes. A wrong one is refused like this for 15 minutes; a valid one still works."

// refusals is the count, by address.
type refusals struct {
	mu    sync.Mutex
	by    map[string]*refusedAddress
	swept time.Time
	// told is whether "Agents" was told the limiter refuses an address.
	told bool
}

// refusedAddress is one address's count over the refusedWindow that began
// with its first refusal, and until when it is refused once past the limit.
type refusedAddress struct {
	count int
	ends  time.Time
	until time.Time
}

func (a *refusedAddress) over(now time.Time) bool { return now.Before(a.until) }

func (a *refusedAddress) done(now time.Time) bool {
	return !a.over(now) && (!a.until.IsZero() || !now.Before(a.ends))
}

// count counts one more refusal from addr. over is whether the address is
// past the limit; began is whether this refusal put the first address there.
func (l *refusals) count(addr string, now time.Time) (over, began bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.by == nil {
		l.by = map[string]*refusedAddress{}
	}
	if now.Sub(l.swept) >= time.Minute {
		for k, a := range l.by {
			if a.done(now) {
				delete(l.by, k)
			}
		}
		l.swept = now
	}
	a := l.by[addr]
	if a == nil || a.done(now) {
		a = &refusedAddress{ends: now.Add(refusedWindow)}
		l.by[addr] = a
	}
	if a.over(now) {
		return true, false
	}
	a.count++
	if a.count <= refusedLimit {
		return false, false
	}
	a.until = now.Add(refusedFor)
	began = !l.told
	l.told = true
	return true, began
}

// quiet reports, once, that no address is refused any more.
func (l *refusals) quiet(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.told {
		return false
	}
	for _, a := range l.by {
		if a.over(now) {
			return false
		}
	}
	l.told = false
	return true
}

func (l *refusals) addresses() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.by)
}

// refusedLook tells "Agents" when the limiter refuses nobody any more.
// labd looks every second (monitor).
func (s *Server) refusedLook() {
	if s.refusals.quiet(s.now()) {
		s.agents.LimiterQuiet()
	}
}

// tooManyRefused counts the credential r carried, which labd does not
// admit, and reports whether its address is past the limit: it has then
// answered 429.
func (s *Server) tooManyRefused(w http.ResponseWriter, r *http.Request) bool {
	if !viaTunnel(r) {
		return false
	}
	if c, ok := r.Context().Value(callKey{}).(*call); ok {
		c.refused = true
	}
	over, began := s.refusals.count(callerAddr(r), s.now())
	if began {
		s.agents.LimiterRefusing()
	}
	if over {
		httpErr(w, http.StatusTooManyRequests, "%s", refusedSentence)
	}
	return over
}

// refuse answers a request whose credential labd does not admit with code
// and the line, or with 429 once its address is past the limit. A request
// that carried no credential in Authorization is answered and not counted.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, code int, format string, args ...any) {
	if r.Header.Get("Authorization") != "" && s.tooManyRefused(w, r) {
		return
	}
	httpErr(w, code, format, args...)
}

// noCredential answers a request that came with nothing labd admits. An
// expired Lab token is told where it is renewed.
func (s *Server) noCredential(w http.ResponseWriter, r *http.Request) {
	if u := tokenUseOf(r); u != nil && u.expired {
		s.refuse(w, r, 401, "this Lab token, %q of @%s, expired on %s after %d days unused: renew it on your own page, %s",
			u.token.Name, u.Handle, u.token.Expires.UTC().Format(time.DateOnly), u.token.Period, s.publicBase(r)+web.OwnPage)
		return
	}
	s.refuse(w, r, 401, "not a Lab token")
}
