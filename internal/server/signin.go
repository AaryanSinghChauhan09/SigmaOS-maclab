package server

// Browser sign-in. "Sign in" is a plain link to /sign-in, a no-store address
// that sets a short-lived state cookie and sends the browser to GitHub.
// GitHub sends it back to /auth/github/callback, where the state must be the
// cookie's and one labd handed out and has not seen come back: it works
// once. The person then returns to the path they came from, and only to a
// path on the Lab address. A refusal is said on the callback's own answer:
// no page that follows carries a line about it, so a signed-out page is the
// same for every visitor.
//
// Every write from a browser must come from the lab's own pages (sameOriginOK
// for any browser; browserSession for a signed-in one, which must say so and
// carry the CSRF token as well).

import (
	"context"
	"crypto/subtle"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	stateTTL = 10 * time.Minute
	// maxPendingSignIns bounds what labd holds for people on their way to
	// GitHub and back.
	maxPendingSignIns = 4096
	// cookieDays is how long a browser keeps the session cookie; the 30
	// days unused are counted by labd.
	cookieDays = 400
	formMax    = 1 << 20
)

// sessionCookie and stateCookie name labd's two cookies. Over https they are
// Secure and host-only by name; at a plain-http loopback address, where a
// lab under development is reached, a Secure cookie would never come back.
func sessionCookie(secure bool) string { return cookieName("lab_session", secure) }
func stateCookie(secure bool) string   { return cookieName("lab_sign_in", secure) }

func cookieName(name string, secure bool) string {
	if secure {
		return "__Host-" + name
	}
	return name
}

func setCookie(w http.ResponseWriter, name, value string, secure bool, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

// csrfToken is the CSRF token of the session whose id is value: a page of
// that session can state it, and nothing that lacks the cookie can.
func csrfToken(value string) string { return hashSecret("csrf:" + value) }

type pendingSignIn struct {
	returnTo string
	expires  time.Time
}

// signInStates are the OAuth states labd handed out and has not seen come
// back, by their hash.
type signInStates struct {
	mu sync.Mutex
	m  map[string]pendingSignIn
}

func (p *signInStates) put(hash string, s pendingSignIn, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]pendingSignIn{}
	}
	if len(p.m) >= maxPendingSignIns {
		for h, old := range p.m {
			if now.After(old.expires) {
				delete(p.m, h)
			}
		}
	}
	for h := range p.m {
		if len(p.m) < maxPendingSignIns {
			break
		}
		delete(p.m, h)
	}
	p.m[hash] = s
}

// take is the pending sign-in for a state, once.
func (p *signInStates) take(hash string, now time.Time) (pendingSignIn, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.m[hash]
	delete(p.m, hash)
	return s, ok && !now.After(s.expires)
}

// returnPath is raw as a path on the Lab address a person may return to
// after sign-in, with its query, or "" when it is anything else: another
// site, a scheme, a path a browser could read as either.
func returnPath(raw string) string {
	if len(raw) > 2000 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || unsafeInPath(raw) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || unsafeInPath(u.Path) {
		return ""
	}
	if clean := path.Clean(u.Path); clean != u.Path && clean+"/" != u.Path {
		return ""
	}
	out := u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

func unsafeInPath(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' })
}

// home is the lab's home page as r's browser reaches it.
func (s *Server) home(origin string) string { return origin + s.basePath() + "/" }

func (s *Server) hSignIn(w http.ResponseWriter, r *http.Request) {
	app := s.cfg.GitHub
	if app == nil {
		httpErr(w, 404, "sign-in is not set up on this lab")
		return
	}
	origin, _ := s.externalOrigin(r)
	secure := strings.HasPrefix(origin, "https://")
	state := randHex(24)
	s.signIns.put(hashSecret(state), pendingSignIn{returnTo: returnPath(r.URL.Query().Get("return")), expires: s.now().Add(stateTTL)}, s.now())
	setCookie(w, stateCookie(secure), state, secure, int(stateTTL.Seconds()))
	w.Header().Set("Location", app.authorizeURL(s.publicBase(r)+"/auth/github/callback", state))
	w.WriteHeader(http.StatusFound)
}

func (s *Server) hSignInCallback(w http.ResponseWriter, r *http.Request) {
	app := s.cfg.GitHub
	if app == nil {
		httpErr(w, 404, "sign-in is not set up on this lab")
		return
	}
	origin, _ := s.externalOrigin(r)
	secure := strings.HasPrefix(origin, "https://")
	again := "Sign in again: " + s.publicBase(r) + "/sign-in"
	var mine string
	if c, err := r.Cookie(stateCookie(secure)); err == nil {
		mine = c.Value
		setCookie(w, stateCookie(secure), "", secure, -1)
	}
	q := r.URL.Query()
	state := q.Get("state")
	pending, ok := s.signIns.take(hashSecret(state), s.now())
	// A sign-in turned down counts as a refused credential (refused.go).
	if state == "" || subtle.ConstantTimeCompare([]byte(mine), []byte(state)) != 1 || !ok {
		if !s.tooManyRefused(w, r) {
			httpErr(w, 400, "That sign-in link is stale. %s", again)
		}
		return
	}
	code := q.Get("code")
	if code == "" {
		if !s.tooManyRefused(w, r) {
			httpErr(w, 400, "GitHub didn't sign you in. %s", again)
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	token, err := app.exchange(ctx, code, s.publicBase(r)+"/auth/github/callback")
	var who githubIdentity
	if err == nil {
		who, err = app.user(ctx, token)
	}
	if err != nil {
		if !s.tooManyRefused(w, r) {
			httpErr(w, 502, "GitHub didn't sign you in (%v). %s", err, again)
		}
		return
	}
	if k, err := s.store.knownPerson(who.ID); err == nil && k.Blocked {
		httpErr(w, 403, "%s", blockedSentence)
		return
	}
	id, now := randHex(32), s.now()
	if err = s.store.signedInAs(who.ID, who.Login, now); err == nil {
		err = s.store.addSession(hashSecret(id), who.ID, now)
	}
	if err != nil {
		s.log.Printf("sign-in of %s: %v", who.Login, err)
		httpErr(w, 500, "The lab couldn't sign you in. %s", again)
		return
	}
	// The session this browser held is replaced: nothing can use it again.
	if c, err := r.Cookie(sessionCookie(secure)); err == nil && c.Value != "" {
		s.store.endSession(hashSecret(c.Value))
	}
	noteCaller(r, "person:"+who.Login)
	setCookie(w, sessionCookie(secure), id, secure, cookieDays*24*60*60)
	to := s.home(origin)
	if pending.returnTo != "" {
		to = origin + pending.returnTo
	}
	w.Header().Set("Location", to)
	w.WriteHeader(http.StatusSeeOther)
}

// hSignOut ends the browser's session. The edge has already seen that the
// request came from the lab's own page with the session's token.
func (s *Server) hSignOut(w http.ResponseWriter, r *http.Request) {
	origin, _ := s.externalOrigin(r)
	if si := signedInOf(r); si != nil {
		if err := s.store.endSession(si.hash); err != nil {
			httpErr(w, 500, "%v", err)
			return
		}
		s.conns.end(sessionConnKey(si.hash))
		secure := strings.HasPrefix(origin, "https://")
		setCookie(w, sessionCookie(secure), "", secure, -1)
	}
	w.Header().Set("Location", s.home(origin))
	w.WriteHeader(http.StatusSeeOther)
}

// browserSession finds the person signed in on r's browser and passes them
// on (see signedInOf). A signed-in request that can change something must
// say it comes from the lab's own page (see ownPage) and carry the session's
// CSRF token, in X-CSRF-Token or a form's csrf_token field; otherwise it
// answers w and reports false. A request with its own credential in
// Authorization is not the browser session's: the cookie is not read for it.
//
// A form is read whole before the session is looked up: a body held back is
// then judged by the session and the person as they are when the body has
// come, so a form let in before a cut does nothing after it. Any other body
// is read through liveBody, which holds each read to the same.
func (s *Server) browserSession(w http.ResponseWriter, r *http.Request, origin string) (*http.Request, bool) {
	if r.Header.Get("Authorization") != "" {
		return r, true
	}
	c, err := r.Cookie(sessionCookie(strings.HasPrefix(origin, "https://")))
	if err != nil || c.Value == "" {
		return r, true
	}
	changes, form, read := true, false, false
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		changes = false
	default:
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/x-www-form-urlencoded" {
			form = true
			r.Body = http.MaxBytesReader(w, r.Body, formMax)
			read = r.ParseForm() == nil
		}
	}
	si := s.sessionOf(c.Value)
	if si == nil {
		return r, true
	}
	noteCaller(r, "person:"+si.Handle)
	if changes {
		if !ownPage(r, origin) {
			httpErr(w, 403, "refused: a signed-in browser changes things only from the lab's own pages at %s", origin)
			return r, false
		}
		if form && !read {
			httpErr(w, 400, "refused: the form could not be read, or is longer than the lab takes")
			return r, false
		}
		token := r.Header.Get("X-CSRF-Token")
		if token == "" && form {
			token = r.PostForm.Get("csrf_token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(si.csrf)) != 1 {
			httpErr(w, 403, "refused: this page is out of date. Open it again and retry")
			return r, false
		}
		if !form && r.Body != nil && r.Body != http.NoBody {
			r.Body = liveBody{r.Body, func() bool {
				now := s.sessionOf(c.Value)
				return now != nil && now.AccountID == si.AccountID && now.Role() == si.Role()
			}}
		}
	}
	return r.WithContext(withPerson(r.Context(), si)), true
}

// liveBody is the body of a signed-in browser's request that changes
// something and is no form. A read answers only while the session is there
// and its person has the role they had when the request was let in: what
// comes after a cut is not read, so the request does nothing with it.
type liveBody struct {
	io.ReadCloser
	live func() bool
}

func (b liveBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if !b.live() {
		return 0, errSessionEnded
	}
	return n, err
}
