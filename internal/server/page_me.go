package server

// A person's own page, and the cuts: their Lab tokens (make, revoke,
// renew), their browsers ("Sign out everywhere") and "End all access". A
// Maintainer makes the first and the last cut for another person from
// People, at the two addresses under /people at the end of this file.

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /me", web.SignedIn, (*Server).pMe)
	addPage("POST /me/tokens", web.SignedIn, (*Server).pMakeToken)
	addPage("POST /me/tokens/{token}/revoke", web.SignedIn, (*Server).pRevokeOwnToken)
	addPage("POST /me/tokens/{token}/renew", web.SignedIn, (*Server).pRenewToken)
	addPage("POST /me/sign-out-everywhere", web.SignedIn, (*Server).pSignOutEverywhere)
	addPage("POST /me/end-all-access", web.SignedIn, (*Server).pEndOwnAccess)
	addPage("POST /people/{account}/tokens/{token}/revoke", web.Maintainers, (*Server).pRevokeToken)
	addPage("POST /people/{account}/end-all-access", web.Maintainers, (*Server).pEndAccess)
}

// kindAllows is what each kind of person may do, in a sentence.
var kindAllows = map[web.Kind]string{
	web.NoRole:     "You see the public site, and can ask to lend a Mac.",
	web.External:   "You lend your own Macs, up to 5, and see them and their jobs.",
	web.MTeam:      "You see the whole fleet, lease a Mac and use it, and your Macs' runs are Verified runs.",
	web.Maintainer: "You do everything on any Mac: baselines, Promotion, verdicts, Macs, builders, reports and People.",
	web.Admin:      "You do everything a Maintainer does, and add and remove Maintainers.",
}

// mePage is what the own page's template reads.
type mePage struct {
	Handle, Kind, Allows string
	Tokens               []meToken
	Count, Cap           int
	Periods              []mePeriod
	Browsers             string
	EndAll               string
	// Shown is a token just made: its secret is on this one answer only.
	Shown *meShown
	// Refused says why no token was made.
	Refused string
}

type meToken struct {
	ID                          int64
	Name, Made, Expires, Period string
	LastUsed, Address           string
	Expired, Never              bool
}

type mePeriod struct {
	Value, Label string
	Default      bool
}

type meShown struct{ Name, Secret, Expires string }

const (
	pageDate = "2006-01-02"
	pageTime = "2006-01-02 15:04 UTC"
)

func periodLabel(days int) string {
	switch days {
	case 0:
		return "never"
	case 365:
		return "1 year unused"
	}
	return fmt.Sprintf("%d days unused", days)
}

func counted(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func tokenRows(tokens []labToken, now time.Time) []meToken {
	rows := make([]meToken, 0, len(tokens))
	for _, t := range tokens {
		row := meToken{ID: t.ID, Name: t.Name, Made: t.Made.UTC().Format(pageDate), Period: periodLabel(t.Period), Never: t.Expires.IsZero(), Expired: t.expired(now), Address: t.Address}
		if !row.Never {
			row.Expires = t.Expires.UTC().Format(pageDate)
		}
		if !t.LastUsed.IsZero() {
			row.LastUsed = t.LastUsed.UTC().Format(pageTime)
		}
		rows = append(rows, row)
	}
	return rows
}

// me is the own page of the person signed in on r.
func (s *Server) me(r *http.Request) (*mePage, error) {
	p, now := PersonOf(r), s.now()
	tokens, err := s.store.labTokens(p.AccountID)
	if err != nil {
		return nil, err
	}
	browsers, err := s.store.countSessions(p.AccountID, now)
	if err != nil {
		return nil, err
	}
	kind := s.viewerOf(r).Kind
	page := &mePage{Handle: p.Handle, Kind: kind.String(), Allows: kindAllows[kind], Tokens: tokenRows(tokens, now), Count: len(tokens), Cap: labTokenCap,
		Browsers: counted(browsers, "browser", "browsers"), EndAll: endAllSentence(browsers, len(tokens))}
	for _, days := range labTokenPeriods {
		page.Periods = append(page.Periods, mePeriod{Value: map[bool]string{true: "never", false: strconv.Itoa(days)}[days == 0], Label: periodLabel(days), Default: days == labTokenDefaultPeriod})
	}
	return page, nil
}

func endAllSentence(browsers, tokens int) string {
	return fmt.Sprintf("End all access: %s and %s stop now", counted(browsers, "browser", "browsers"), counted(tokens, "Lab token", "Lab tokens"))
}

func (s *Server) renderMe(w http.ResponseWriter, r *http.Request, status int, change func(*mePage)) {
	page, err := s.me(r)
	if err != nil {
		s.pageFailed(w, err)
		return
	}
	if change != nil {
		change(page)
	}
	s.render(w, r, "me", web.Page{Title: "@" + page.Handle, Status: status, Data: page})
}

func (s *Server) pMe(w http.ResponseWriter, r *http.Request) { s.renderMe(w, r, 0, nil) }

// labTokenPeriod is the period a form names: 30, 90, 365 or never, and the
// default for none.
func labTokenPeriod(value string) (days int, ok bool) {
	switch value {
	case "":
		return labTokenDefaultPeriod, true
	case "never":
		return 0, true
	case "30", "90", "365":
		days, _ = strconv.Atoi(value)
		return days, true
	}
	return 0, false
}

func (s *Server) pMakeToken(w http.ResponseWriter, r *http.Request) {
	p := PersonOf(r)
	refuse := func(status int, why string) {
		s.renderMe(w, r, status, func(page *mePage) { page.Refused = why })
	}
	name, ok := labTokenName(r.PostFormValue("name"))
	if !ok {
		refuse(http.StatusBadRequest, "A Lab token's name is one line of up to 64 characters, without a double quote.")
		return
	}
	period, ok := labTokenPeriod(r.PostFormValue("period"))
	if !ok {
		refuse(http.StatusBadRequest, "A Lab token lasts 30 days, 90 days or 1 year unused, or never expires.")
		return
	}
	secret, t, err := s.store.makeLabToken(p.AccountID, name, period, s.now(), true)
	switch {
	case errors.Is(err, errTooManyTokens):
		refuse(http.StatusConflict, fmt.Sprintf("You have %d Lab tokens, which is the most a person has. Revoke one first.", labTokenCap))
		return
	case err != nil:
		s.pageFailed(w, err)
		return
	}
	if err := s.record(r, fmt.Sprintf("made the Lab token %q", t.Name), nil); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	s.agents.LabTokenMade(p.Handle, t.Name, t.Expires)
	s.renderMe(w, r, 0, func(page *mePage) {
		page.Shown = &meShown{Name: t.Name, Secret: secret, Expires: "never expires"}
		if !t.Expires.IsZero() {
			page.Shown.Expires = "expires on " + t.Expires.UTC().Format(pageDate) + " if it is not used"
		}
	})
}

// tokenNamed is the token of the person account that r's path names; it
// answers 404 and reports false when that person has none such.
func (s *Server) tokenNamed(w http.ResponseWriter, r *http.Request, account int64) (labToken, bool) {
	id, err := strconv.ParseInt(r.PathValue("token"), 10, 64)
	if err == nil {
		var t labToken
		if t, err = s.store.labToken(account, id); err == nil {
			return t, true
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, strconv.ErrSyntax) && !errors.Is(err, strconv.ErrRange) {
		s.pageFailed(w, err)
		return labToken{}, false
	}
	s.notFound(w, r)
	return labToken{}, false
}

func (s *Server) pRevokeOwnToken(w http.ResponseWriter, r *http.Request) {
	p := PersonOf(r)
	t, ok := s.tokenNamed(w, r, p.AccountID)
	if !ok || !s.sure(w, r, fmt.Sprintf("Revoke “%s”: whatever uses it stops now", t.Name), web.OwnPage) {
		return
	}
	if err := s.revokeLabToken(t); err != nil {
		s.pageFailed(w, err)
		return
	}
	if err := s.record(r, fmt.Sprintf("revoked their Lab token %q", t.Name), nil); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	http.Redirect(w, r, web.OwnPage, http.StatusSeeOther)
}

func (s *Server) pRenewToken(w http.ResponseWriter, r *http.Request) {
	p := PersonOf(r)
	t, ok := s.tokenNamed(w, r, p.AccountID)
	if !ok {
		return
	}
	renewed, err := s.store.renewLabToken(t, s.now())
	if err != nil {
		s.pageFailed(w, err)
		return
	}
	if renewed {
		if err := s.record(r, fmt.Sprintf("renewed the Lab token %q", t.Name), nil); err != nil {
			s.log.Printf("the People record: %v", err)
		}
	}
	http.Redirect(w, r, web.OwnPage, http.StatusSeeOther)
}

// signedOutHere takes the session's cookie from r's browser and sends it to
// the home page.
func (s *Server) signedOutHere(w http.ResponseWriter, r *http.Request) {
	origin, _ := s.externalOrigin(r)
	secure := strings.HasPrefix(origin, "https://")
	setCookie(w, sessionCookie(secure), "", secure, -1)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) pSignOutEverywhere(w http.ResponseWriter, r *http.Request) {
	p := PersonOf(r)
	browsers, err := s.store.countSessions(p.AccountID, s.now())
	if err != nil {
		s.pageFailed(w, err)
		return
	}
	if !s.sure(w, r, fmt.Sprintf("Sign out everywhere: %s, this one too, are signed out now", counted(browsers, "browser", "browsers")), web.OwnPage) {
		return
	}
	if err := s.record(r, "signed out everywhere", nil); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	if err := s.signOutEverywhere(p.AccountID); err != nil {
		s.pageFailed(w, err)
		return
	}
	s.signedOutHere(w, r)
}

func (s *Server) pEndOwnAccess(w http.ResponseWriter, r *http.Request) {
	p := PersonOf(r)
	page, err := s.me(r)
	if err != nil {
		s.pageFailed(w, err)
		return
	}
	if !s.sure(w, r, page.EndAll, web.OwnPage) {
		return
	}
	if err := s.record(r, `used "End all access"`, nil); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	if err := s.endAllAccess(*p, `ended: @`+p.Handle+` used "End all access"`); err != nil {
		s.pageFailed(w, err)
		return
	}
	s.signedOutHere(w, r)
}

// personNamed is the person r's path names by account id; it answers 404
// and reports false for nobody the lab knows.
func (s *Server) personNamed(w http.ResponseWriter, r *http.Request) (Person, bool) {
	id, err := strconv.ParseInt(r.PathValue("account"), 10, 64)
	if err == nil {
		var p Person
		if p, err = s.personNow(id); err == nil {
			return p, true
		}
		if !errors.Is(err, sql.ErrNoRows) {
			s.pageFailed(w, err)
			return Person{}, false
		}
	}
	s.notFound(w, r)
	return Person{}, false
}

const peoplePage = "/people"

// pRevokeToken is the first cut made by a Maintainer: any Maintainer
// revokes any person's token.
func (s *Server) pRevokeToken(w http.ResponseWriter, r *http.Request) {
	by := PersonOf(r)
	p, ok := s.personNamed(w, r)
	if !ok {
		return
	}
	t, ok := s.tokenNamed(w, r, p.AccountID)
	if !ok || !s.sure(w, r, fmt.Sprintf("Revoke “%s” of @%s: whatever uses it stops now", t.Name, p.Handle), peoplePage) {
		return
	}
	if err := s.revokeLabToken(t); err != nil {
		s.pageFailed(w, err)
		return
	}
	if err := s.record(r, fmt.Sprintf("revoked the Lab token %q of", t.Name), &p); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	if by.AccountID != p.AccountID {
		s.agents.LabTokenRevoked(by.Handle, p.Handle, t.Name)
	}
	http.Redirect(w, r, peoplePage, http.StatusSeeOther)
}

// pEndAccess is "End all access" used by a Maintainer for a person, by an
// Admin for a Maintainer.
func (s *Server) pEndAccess(w http.ResponseWriter, r *http.Request) {
	by := PersonOf(r)
	p, ok := s.personNamed(w, r)
	if !ok {
		return
	}
	if p.isMaintainer() && !by.Admin && by.AccountID != p.AccountID {
		httpErr(w, http.StatusForbidden, `@%s is a Maintainer: only an Admin uses "End all access" on a Maintainer`, p.Handle)
		return
	}
	tokens, err := s.store.labTokens(p.AccountID)
	if err != nil {
		s.pageFailed(w, err)
		return
	}
	browsers, err := s.store.countSessions(p.AccountID, s.now())
	if err != nil {
		s.pageFailed(w, err)
		return
	}
	if !s.sure(w, r, fmt.Sprintf("%s for @%s; they can sign in again", endAllSentence(browsers, len(tokens)), p.Handle), peoplePage) {
		return
	}
	if err := s.record(r, `used "End all access" on`, &p); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	if err := s.endAllAccess(p, `ended: @`+by.Handle+` used "End all access" on @`+p.Handle); err != nil {
		s.pageFailed(w, err)
		return
	}
	if by.AccountID != p.AccountID {
		s.agents.EndedAllAccess(by.Handle, p.Handle)
	}
	http.Redirect(w, r, peoplePage, http.StatusSeeOther)
}
