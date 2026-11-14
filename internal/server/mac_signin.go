package server

// Mac sign-in: `omarchy-m-test --sign-in` ties a Mac's Machine key to a
// person, and that Mac's reports are stamped with the person from then on.
// The sign-ins live in reports.db and internal/reports answers the tool;
// here is what labd.db adds: GitHub's word on a device-flow token, who is
// Blocked, who is on the M team, and the two places a sign-in is ended from
// a page: the person's own, and People for a Maintainer.
//
// A Mac sign-in is no credential of a person: it opens no page and no
// route, and a first one only makes the lab know the person.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/omacom/omarchy-maclab/internal/reports"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("POST /me/macs/{signin}/sign-out", web.SignedIn, (*Server).pSignOwnMacOut)
	addPage("POST /people/{id}/macs/{signin}/sign-out", web.Maintainers, (*Server).pSignMacOut)
}

// macSignInGitHub is the lab's OAuth app, as Mac sign-in asks it.
type macSignInGitHub struct{ app *GitHubApp }

func (g macSignInGitHub) TokenUser(ctx context.Context, token string) (int64, string, error) {
	who, err := g.app.tokenUser(ctx, token)
	return who.ID, who.Login, err
}

func (g macSignInGitHub) Revoke(ctx context.Context, token string) { g.app.revoke(ctx, token) }

// macSignInPeople is labd.db, as Mac sign-in asks it.
type macSignInPeople struct{ s *Server }

// Know keeps the account GitHub named, under the handle it has now, and
// notes that the person was seen. It gives nothing: no session, no Lab
// token, no mark and no leave to lend.
func (p macSignInPeople) Know(accountID int64, handle string) (blocked bool, err error) {
	k, err := p.s.store.knownPerson(accountID)
	switch {
	case err == nil && k.Blocked:
		return true, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	return false, p.s.store.signedInAs(accountID, handle, p.s.now())
}

func (p macSignInPeople) Blocked(accountID int64) bool {
	k, err := p.s.store.knownPerson(accountID)
	return err == nil && k.Blocked
}

func (p macSignInPeople) OnMTeam(accountID int64) bool { return p.s.onMTeam(accountID) }

// onMTeam reports whether the person with that account id is on the Omarchy
// M team at this moment: what makes a run of their Mac a Verified run.
func (s *Server) onMTeam(accountID int64) bool {
	p, err := s.person(accountID)
	return err == nil && p.OnMTeam()
}

// hMacSignIn is POST /api/v1/tester_bindings and hMacRequest POST
// /api/v1/tester_requests. The address their limits count on is the
// caller's, as the upload's is.
func (s *Server) hMacSignIn(w http.ResponseWriter, r *http.Request) {
	s.macSignIn(w, r, func() error { return s.macSignIns.HandleSignIn(w, r, callerAddr(r)) })
}

func (s *Server) hMacRequest(w http.ResponseWriter, r *http.Request) {
	s.macSignIn(w, r, func() error { return s.macSignIns.HandleRequest(w, r, callerAddr(r)) })
}

func (s *Server) macSignIn(w http.ResponseWriter, r *http.Request, answer func() error) {
	switch {
	case s.Preview():
		refuseJSON(w, http.StatusForbidden, "Omarchy Mac Lab is not open yet and signs no Mac in. Until it opens, "+oldSite+" is the live site: sign in there.")
	case s.macSignIns == nil:
		refuseJSON(w, http.StatusServiceUnavailable, "This lab signs no Mac in yet: it was given no machine-id key.")
	default:
		if err := answer(); err != nil {
			s.log.Printf("%v", err)
		}
	}
}

// endMacSignInsOf signs every Mac out that is signed in as the person:
// their later reports are stamped with nobody.
func (s *Server) endMacSignInsOf(accountID int64) {
	if _, err := s.reports.EndSignInsOf(accountID); err != nil {
		s.log.Printf("ending the Mac sign-ins of account %d: %v", accountID, err)
	}
}

// macSignedIn is a Mac signed in as a person, as a page shows it. The lab
// knows such a Mac by its Machine key alone, and shows the key to nobody:
// the row says what the Mac's reports say of it.
type macSignedIn struct {
	ID      int64
	Model   string // "" for a Mac that sent no report yet
	Since   string
	Reports string
}

// macsSignedInAs are the Macs signed in as the person, oldest sign-in first.
func (s *Server) macsSignedInAs(accountID int64) ([]macSignedIn, error) {
	signIns, err := s.reports.SignInsOf(accountID)
	if err != nil {
		return nil, err
	}
	out := make([]macSignedIn, 0, len(signIns))
	for _, signIn := range signIns {
		model, n, err := s.reports.MacOf(signIn.MachineID)
		if err != nil {
			return nil, err
		}
		out = append(out, macSignedIn{ID: signIn.ID, Model: model, Since: utcMinute(signIn.At), Reports: counted(n, "report", "reports")})
	}
	return out, nil
}

// signMacOut ends the sign-in r's path names, which must be the person's.
// It answers 404 and reports false when the person has none such.
func (s *Server) signMacOut(w http.ResponseWriter, r *http.Request, accountID int64) bool {
	id, err := strconv.ParseInt(r.PathValue("signin"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return false
	}
	ended, err := s.reports.EndSignInOf(accountID, id)
	switch {
	case err != nil:
		s.pageFailed(w, err)
	case ended == nil:
		s.notFound(w, r)
	}
	return err == nil && ended != nil
}

// hasMacSignIn reports whether the sign-in r's path names is the person's;
// it answers 404 when it is not.
func (s *Server) hasMacSignIn(w http.ResponseWriter, r *http.Request, accountID int64) bool {
	signIns, err := s.reports.SignInsOf(accountID)
	if err != nil {
		s.pageFailed(w, err)
		return false
	}
	for _, signIn := range signIns {
		if strconv.FormatInt(signIn.ID, 10) == r.PathValue("signin") {
			return true
		}
	}
	s.notFound(w, r)
	return false
}

func (s *Server) pSignOwnMacOut(w http.ResponseWriter, r *http.Request) {
	p := PersonOf(r)
	if !s.hasMacSignIn(w, r, p.AccountID) || !s.sure(w, r, "Sign out: this Mac's runs are Community runs from now on", web.OwnPage) {
		return
	}
	if !s.signMacOut(w, r, p.AccountID) {
		return
	}
	if err := s.record(r, "signed a Mac out", nil); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	http.Redirect(w, r, web.OwnPage, http.StatusSeeOther)
}

// pSignMacOut is a Maintainer ending a person's Mac sign-in from People.
func (s *Server) pSignMacOut(w http.ResponseWriter, r *http.Request) {
	k, err := s.personAt(r)
	if err != nil {
		s.done(w, r, k.AccountID, err)
		return
	}
	if !s.hasMacSignIn(w, r, k.AccountID) ||
		!s.sure(w, r, fmt.Sprintf("Sign this Mac of @%s out: its runs are Community runs from now on", k.Handle), "/people?open="+r.PathValue("id")) {
		return
	}
	if !s.signMacOut(w, r, k.AccountID) {
		return
	}
	s.done(w, r, k.AccountID, s.record(r, "signed a Mac out of", &k.Person))
}

var _ reports.People = macSignInPeople{}
