package server

// People, for Maintainers: everyone the lab knows, one row a person, with
// the actions that change who someone is, and the record of who did what.
// A row opens in place (?open=<account id>) and a chip keeps to one kind
// (?kind=). Each action is a form posted to its own address; a refusal is
// said on the page it came from, and what was done shows in the record.

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /people", web.Maintainers, (*Server).pPeople)
	addPage("POST /people/mark", web.Maintainers, (*Server).pMarkHandle)
	addPage("POST /people/{id}/m-team", web.Maintainers, onPerson((*Server).markMTeam))
	addPage("POST /people/{id}/m-team/remove", web.Maintainers, (*Server).pUnmarkMTeam)
	addPage("POST /people/{id}/maintainer", web.Admins, onPerson((*Server).makeMaintainer))
	addPage("POST /people/{id}/maintainer/remove", web.Admins, (*Server).pRemoveMaintainer)
	addPage("POST /people/{id}/block", web.Maintainers, (*Server).pBlock)
	addPage("POST /people/{id}/unblock", web.Maintainers, onPerson((*Server).unblock))
	addPage("POST /people/{id}/leases/{mac}/end", web.Maintainers, (*Server).pEndLease)
}

const recordShown = 200

// peopleKinds are the chips, in the order of the interface's table, and
// what each calls its kind in ?kind=.
var peopleKinds = []struct{ id, label string }{
	{"admin", "Admin"}, {"maintainer", "Maintainer"}, {"m-team", "M team"},
	{"external", "External volunteer"}, {"none", "No role"}, {"blocked", "Blocked"},
}

func kindID(k knownPerson) string {
	if k.Blocked {
		return "blocked"
	}
	return [...]string{"", "none", "external", "m-team", "maintainer", "admin"}[k.Role()]
}

type peopleChip struct {
	Label, Path string
	Count       int
	Current     bool
}

type personRow struct {
	knownPerson
	You bool
	// Twin marks a handle the lab keeps for more than one account, as it
	// does after a rename until the account that left it signs in again.
	Twin       bool
	Badge      string
	BadgeClass string
	Macs       []string
	Leases     []string
	Seen       string
	Path       string // opens the row, or shuts it when it is open
	Open       *personOpen

	// SignedIn is how many Macs are signed in as the person, and Counted
	// whether their runs are Verified runs: the person is on the M team.
	SignedIn int
	Counted  bool
	// LeaveNoMac is leave to lend with no Mac lent yet.
	LeaveNoMac bool
}

// personOpen is the inside of an opened row.
type personOpen struct {
	// Viewer is who looks, for what only an Admin is offered.
	ViewerAdmin   bool
	BlockedAt     string
	BlockedBy     string
	MayBlock      bool
	BlockSentence string
	LeavesTeam    string // the sentence of Remove from M team
	Tokens        []meToken
	// MacsSignedIn are the Macs signed in as the person.
	MacsSignedIn []macSignedIn
	// EndAll is the sentence of "End all access", "" when the viewer may
	// not use it on this person.
	EndAll string
	// Forget is the sentence of Forget, ForgetWhy why the viewer may not.
	Forget, ForgetWhy string
	Record            []recordShownLine
	// Leases is the person's part of the Lease record: their Leases, and
	// those they ended.
	Leases []recordShownLine
	// Lends is what counts toward the person's cap, Cap the cap (0 for a
	// Maintainer, who has none), and JoinCommand the Mac their unused Join
	// command names.
	Lends, Cap  int
	JoinCommand string
}

type recordShownLine struct {
	When, Line string
}

// peopleRequest is a Request to lend waiting for a Maintainer.
type peopleRequest struct {
	AccountID                           int64
	Handle, Model, Note, Contact, Asked string
}

type peopleData struct {
	Admin    bool
	Refused  string
	Requests []peopleRequest
	// Forgets are the requests to be Forgotten.
	Forgets []forgetRequest
	Chips   []peopleChip
	Rows    []personRow
	Record  []recordShownLine
	Kind    string
}

// utcMinute is a time as People shows it.
func utcMinute(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

func shown(lines []recordLine) []recordShownLine {
	out := make([]recordShownLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, recordShownLine{utcMinute(l.At), l.String()})
	}
	return out
}

func (s *Server) pPeople(w http.ResponseWriter, r *http.Request) {
	s.showPeople(w, r, http.StatusOK, "")
}

// showPeople renders People, with a refusal said at its top when there is
// one.
func (s *Server) showPeople(w http.ResponseWriter, r *http.Request, status int, refused string) {
	me := PersonOf(r)
	q := r.URL.Query()
	kind, open := q.Get("kind"), q.Get("open")
	if r.Method == http.MethodPost {
		kind, open = "", r.PathValue("id")
	}
	s.expireJoinCommands()
	people, err := s.store.knownPeople()
	lines, lerr := s.store.recordLines(recordShown)
	signIns, serr := s.reports.SignIns()
	waiting, werr := s.store.waitingRequests()
	forgets, ferr := s.forgetRequestsFor(me)
	if err = errors.Join(err, lerr, serr, werr, ferr); err != nil {
		s.log.Printf("People: %v", err)
		httpErr(w, 500, "The lab couldn't read its people.")
		return
	}
	page := peopleData{Admin: me.Admin, Refused: refused, Record: shown(lines), Kind: kind, Forgets: forgets}
	for _, q := range waiting {
		page.Requests = append(page.Requests, peopleRequest{AccountID: q.AccountID, Handle: q.Handle, Model: q.ModelName(), Note: q.Note, Contact: q.Contact, Asked: utcMinute(q.Asked)})
	}
	counts := map[string]int{}
	for i := range people {
		k := &people[i]
		s.fillIn(&k.Person)
		counts[kindID(*k)]++
	}
	page.Chips = append(page.Chips, peopleChip{"All", "/people", len(people), kind == ""})
	for _, c := range peopleKinds {
		page.Chips = append(page.Chips, peopleChip{c.label, "/people?kind=" + c.id, counts[c.id], kind == c.id})
	}
	signedIn := map[int64]int{}
	for _, signIn := range signIns {
		signedIn[signIn.AccountID]++
	}
	handles, called := map[int64]string{}, map[string]int{}
	for _, k := range people {
		handles[k.AccountID] = k.Handle
		called[strings.ToLower(k.Handle)]++
	}
	for _, k := range people {
		id := strconv.FormatInt(k.AccountID, 10)
		if kind != "" && kindID(k) != kind && id != open {
			continue
		}
		row := personRow{knownPerson: k, You: k.AccountID == me.AccountID, Twin: called[strings.ToLower(k.Handle)] > 1, Badge: k.Role().Badge(), BadgeClass: k.Role().BadgeClass(),
			Macs: s.macsLentBy(k.AccountID), Leases: s.leasesOf(k.Person), SignedIn: signedIn[k.AccountID], Counted: k.OnMTeam(), Seen: "not signed in yet"}
		if k.Blocked {
			row.Badge, row.BadgeClass = "Blocked", "kind-blocked"
		}
		if !k.LastSeen.IsZero() {
			row.Seen = utcMinute(k.LastSeen)
		}
		row.LeaveNoMac = k.MayLend() && !k.OnMTeam() && len(row.Macs) == 0
		to := url.Values{}
		if kind != "" {
			to.Set("kind", kind)
		}
		if id == open {
			row.Open = s.openPerson(me, k, handles, len(row.Leases))
		} else {
			to.Set("open", id)
		}
		row.Path = "/people"
		if len(to) > 0 {
			row.Path += "?" + to.Encode()
		}
		page.Rows = append(page.Rows, row)
	}
	s.render(w, r, "people", web.Page{Title: "People", Nav: "people", Status: status, Data: page})
}

func (s *Server) openPerson(me *Person, k knownPerson, handles map[int64]string, leases int) *personOpen {
	o := &personOpen{ViewerAdmin: me.Admin, MayBlock: mayBlock(me, k) == nil, LeavesTeam: leavesTeam(leases)}
	if o.MayBlock {
		o.BlockSentence = s.blockEnds(k)
	}
	if k.Blocked {
		o.BlockedAt = utcMinute(k.BlockedAt)
		o.BlockedBy = handles[k.BlockedBy]
	}
	if !k.Blocked {
		tokens, _ := s.store.labTokens(k.AccountID)
		o.Tokens = tokenRows(tokens, s.now())
		if !k.isMaintainer() || me.Admin || me.AccountID == k.AccountID {
			browsers, _ := s.store.countSessions(k.AccountID, s.now())
			o.EndAll = endAllSentence(browsers, len(tokens)) + " for @" + k.Handle + "; they can sign in again"
		}
	}
	o.Forget, o.ForgetWhy = s.forgetControl(me, k)
	if macs, err := s.macsSignedInAs(k.AccountID); err == nil {
		o.MacsSignedIn = macs
	} else {
		s.log.Printf("People: the Macs signed in as @%s: %v", k.Handle, err)
	}
	if lines, err := s.store.recordLinesOf(k.AccountID, recordShown); err == nil {
		o.Record = shown(lines)
	}
	if events, err := s.store.leaseRecordOf(k.AccountID, recordShown); err == nil {
		for _, e := range events {
			o.Leases = append(o.Leases, recordShownLine{utcMinute(e.Time), e.Line()})
		}
	}
	if l, err := s.lendsOf(k.Person); err == nil {
		o.Lends, o.Cap = l.Count(), l.Cap
		if l.Unused != nil {
			o.JoinCommand = l.Unused.Name
		}
	}
	return o
}

// endLeaseSentence is what End Lease states before it is pressed again.
func endLeaseSentence(handle, mac string) string {
	return "End @" + handle + "'s Lease on " + mac + ": its job is canceled and its sessions are cut"
}

// pEndLease is End Lease in a person's row: a Maintainer ends the Lease
// the person holds on the Mac, saying why if they wish.
func (s *Server) pEndLease(w http.ResponseWriter, r *http.Request) {
	k, err := s.personAt(r)
	mac := r.PathValue("mac")
	var why string
	if err == nil {
		why, err = leaseWhy(r.PostFormValue("why"))
	}
	if v := s.dev(mac); err == nil && (v == nil || !s.holdsLease(v, k.Person)) {
		err = refuse(http.StatusConflict, "@%s holds no Lease on %s.", k.Handle, mac)
	} else if err == nil {
		if !s.sure(w, r, endLeaseSentence(k.Handle, mac), "/people?open="+r.PathValue("id")) {
			return
		}
		if err = s.releaseLease(v, *PersonOf(r), "", true, why); err == nil {
			err = s.recordNoted(r, "ended the Lease on "+mac+" of", &k.Person, why)
		}
	}
	s.done(w, r, k.AccountID, err)
}

// leavesTeam is the sentence of Remove from M team, for a person holding
// this many Leases.
func leavesTeam(leases int) string {
	switch leases {
	case 0:
		return "Remove from M team"
	case 1:
		return "Remove: 1 Lease ends now"
	}
	return "Remove: " + strconv.Itoa(leases) + " Leases end now"
}

// done answers an action on People: back to the page with the person's row
// open, or the page itself saying why the lab refused.
func (s *Server) done(w http.ResponseWriter, r *http.Request, accountID int64, err error) {
	var no *refusal
	switch {
	case err == nil:
		to := s.basePath() + "/people"
		if accountID != 0 {
			to += "?open=" + strconv.FormatInt(accountID, 10)
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	case errors.Is(err, errUnknownPerson):
		s.notFound(w, r)
	case errors.As(err, &no):
		s.showPeople(w, r, no.code, no.msg)
	default:
		s.log.Printf("People, %s: %v", r.URL.Path, err)
		s.showPeople(w, r, http.StatusInternalServerError, "The lab couldn't do that.")
	}
}

// personAt is the person an action's address names, as the lab knows them.
func (s *Server) personAt(r *http.Request) (knownPerson, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return knownPerson{}, errUnknownPerson
	}
	return s.subject(id)
}

// onPerson is the page action that applies change to the person its
// address names.
func onPerson(change func(*Server, *http.Request, knownPerson) error) func(*Server, http.ResponseWriter, *http.Request) {
	return func(s *Server, w http.ResponseWriter, r *http.Request) {
		k, err := s.personAt(r)
		if err == nil {
			err = change(s, r, k)
		}
		s.done(w, r, k.AccountID, err)
	}
}

// pMarkHandle is the field under the table: a GitHub handle, marked as M
// team or given an Invite, signed in or not.
func (s *Server) pMarkHandle(w http.ResponseWriter, r *http.Request) {
	change := map[string]func(*Server, *http.Request, knownPerson) error{"m-team": (*Server).markMTeam, "invite": (*Server).invite}[r.PostFormValue("as")]
	if change == nil {
		s.done(w, r, 0, refuse(http.StatusUnprocessableEntity, "Mark as M team, or give an Invite."))
		return
	}
	k, err := s.pin(r.Context(), r.PostFormValue("handle"))
	if err == nil {
		err = change(s, r, k)
	}
	s.done(w, r, k.AccountID, err)
}

func (s *Server) pUnmarkMTeam(w http.ResponseWriter, r *http.Request) {
	k, err := s.personAt(r)
	if err == nil {
		err = mayUnmarkMTeam(k)
	}
	if err == nil {
		if !s.sure(w, r, leavesTeam(len(s.leasesOf(k.Person))), "/people?open="+r.PathValue("id")) {
			return
		}
		err = s.unmarkMTeam(r, k)
	}
	s.done(w, r, k.AccountID, err)
}

func (s *Server) pRemoveMaintainer(w http.ResponseWriter, r *http.Request) {
	k, err := s.personAt(r)
	if err == nil {
		err = mayRemoveMaintainer(PersonOf(r), k)
	}
	if err == nil {
		if !s.sure(w, r, "Remove: stays on the M team", "/people?open="+r.PathValue("id")) {
			return
		}
		err = s.removeMaintainer(r, k)
	}
	s.done(w, r, k.AccountID, err)
}

func (s *Server) pBlock(w http.ResponseWriter, r *http.Request) {
	k, err := s.personAt(r)
	if err == nil {
		err = mayBlock(PersonOf(r), k)
	}
	if err == nil {
		_, err = blockReason(r.PostFormValue("reason"))
	}
	if err == nil {
		if !s.sure(w, r, s.blockEnds(k), "/people?open="+r.PathValue("id")) {
			return
		}
		err = s.block(r, k, r.PostFormValue("reason"))
	}
	s.done(w, r, k.AccountID, err)
}
