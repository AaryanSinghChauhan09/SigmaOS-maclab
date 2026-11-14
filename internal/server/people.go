package server

// A Person is one GitHub account, kept by its numeric account id; the handle
// is a label that follows renames. A browser session is a random id held in
// the person's browser: labd.db keeps its hash, whose it is and when it was
// made and last used, and nothing else, neither an address nor a browser's
// name. Roles are read on every request, so a change takes effect on the
// next one and ends the person's open streams.

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// Person is someone who signed in, as labd knows them on this request.
type Person struct {
	AccountID int64
	Handle    string
	// Maintainer is the stored role. Admin is being named by account id in
	// labd's configuration, on no screen; an Admin is a Maintainer.
	Maintainer bool
	Admin      bool
}

// Kind is the person's kind in the words of the interface.
func (p *Person) Kind() string {
	switch {
	case p.Admin:
		return "Admin"
	case p.Maintainer:
		return "Maintainer"
	}
	return "Signed in, no role"
}

func (p *Person) isMaintainer() bool { return p.Admin || p.Maintainer }

// connKey names the person's open requests in Server.conns. A Maintainer's
// token goes by its name there, which has no colon.
func (p *Person) connKey() string { return "account:" + strconv.FormatInt(p.AccountID, 10) }

// sessionIdle is how long a browser session lasts unused. A use is written
// down at most once in sessionTouch, so a page costs no write.
const (
	sessionIdle  = 30 * 24 * time.Hour
	sessionTouch = time.Hour
)

// signedIn is the person a request's browser session belongs to.
type signedIn struct {
	Person
	hash string // of the session id, as stored
	csrf string
}

type signedInKey struct{}

func signedInOf(r *http.Request) *signedIn {
	si, _ := r.Context().Value(signedInKey{}).(*signedIn)
	return si
}

// PersonOf is the person signed in on r's browser, nil when signed out.
func PersonOf(r *http.Request) *Person {
	if si := signedInOf(r); si != nil {
		return &si.Person
	}
	return nil
}

// CSRFToken is the token a signed-in form on r's page carries in its
// csrf_token field; "" when signed out.
func CSRFToken(r *http.Request) string {
	if si := signedInOf(r); si != nil {
		return si.csrf
	}
	return ""
}

// sessionOf is the person the session id value belongs to, nil when it is
// no session, or one left unused for sessionIdle, which ends here.
func (s *Server) sessionOf(value string) *signedIn {
	hash := hashSecret(value)
	p, lastUsed, err := s.store.session(hash)
	if err != nil {
		return nil
	}
	now := s.now()
	switch idle := now.Sub(lastUsed); {
	case idle > sessionIdle:
		s.store.endSession(hash)
		return nil
	case idle > sessionTouch:
		if err := s.store.touchSession(hash, p.AccountID, now); err != nil {
			s.log.Printf("browser session of %s: %v", p.Handle, err)
		}
	}
	p.Admin = slices.Contains(s.cfg.Admins, p.AccountID)
	return &signedIn{Person: p, hash: hash, csrf: csrfToken(value)}
}

// setMaintainer gives the person the Maintainer role or takes it away. It
// holds from their next request, and their open streams end now.
func (s *Server) setMaintainer(accountID int64, on bool) error {
	if err := s.store.setMaintainer(accountID, on); err != nil {
		return err
	}
	s.conns.end((&Person{AccountID: accountID}).connKey())
	return nil
}

// withPerson is ctx for a request by a signed-in person.
func withPerson(ctx context.Context, si *signedIn) context.Context {
	return context.WithValue(ctx, signedInKey{}, si)
}

// signedInAs records that GitHub named the account id with this handle:
// a new Person, or the one labd has, under the handle it has now.
func (st *store) signedInAs(id int64, handle string, now time.Time) error {
	_, err := st.db.Exec(`INSERT INTO people (account_id, handle, added, last_seen) VALUES (?, ?, ?, ?)
		ON CONFLICT (account_id) DO UPDATE SET handle = excluded.handle, last_seen = excluded.last_seen`,
		id, handle, now.UnixMicro(), now.UnixMicro())
	return err
}

// addSession stores a new browser session by its hash, and drops the
// sessions nobody used for sessionIdle.
func (st *store) addSession(hash string, accountID int64, now time.Time) error {
	if _, err := st.db.Exec(`DELETE FROM browser_sessions WHERE last_used < ?`, now.Add(-sessionIdle).UnixMicro()); err != nil {
		return err
	}
	_, err := st.db.Exec(`INSERT INTO browser_sessions (hash, account_id, created, last_used) VALUES (?, ?, ?, ?)`,
		hash, accountID, now.UnixMicro(), now.UnixMicro())
	return err
}

func (st *store) session(hash string) (p Person, lastUsed time.Time, err error) {
	var used int64
	err = st.db.QueryRow(`SELECT p.account_id, p.handle, p.maintainer, s.last_used FROM browser_sessions s
		JOIN people p ON p.account_id = s.account_id WHERE s.hash = ?`, hash).Scan(&p.AccountID, &p.Handle, &p.Maintainer, &used)
	return p, time.UnixMicro(used), err
}

func (st *store) touchSession(hash string, accountID int64, now time.Time) error {
	if _, err := st.db.Exec(`UPDATE browser_sessions SET last_used = ? WHERE hash = ?`, now.UnixMicro(), hash); err != nil {
		return err
	}
	_, err := st.db.Exec(`UPDATE people SET last_seen = ? WHERE account_id = ?`, now.UnixMicro(), accountID)
	return err
}

func (st *store) endSession(hash string) error {
	_, err := st.db.Exec(`DELETE FROM browser_sessions WHERE hash = ?`, hash)
	return err
}

func (st *store) setMaintainer(accountID int64, on bool) error {
	res, err := st.db.Exec(`UPDATE people SET maintainer = ? WHERE account_id = ?`, on, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
