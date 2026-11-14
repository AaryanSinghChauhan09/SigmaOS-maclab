package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

const (
	edithID = 7
	graceID = 8
	linusID = 9
)

// rolesLab is a lab whose GitHub knows the owner (its Admin), ravi, whom it
// makes a Maintainer, and four accounts the lab has not met.
func rolesLab(t *testing.T, config func(*Config)) *signInLab {
	t.Helper()
	l := pagesLab(t, config)
	l.gh.AddUser(edithID, "edith")
	l.gh.AddUser(graceID, "grace")
	l.gh.AddUser(linusID, "linus")
	l.signedIn(maintainerID)
	if err := l.s.setMaintainer(maintainerID, true); err != nil {
		t.Fatal(err)
	}
	return l
}

// post sends a form of the People page; fields are name, value pairs.
func (b *browser) post(target string, fields ...string) *httptest.ResponseRecorder {
	b.l.t.Helper()
	form := url.Values{}
	for i := 0; i+1 < len(fields); i += 2 {
		form.Set(fields[i], fields[i+1])
	}
	return b.write("POST", target, form.Encode(), "Content-Type", "application/x-www-form-urlencoded")
}

// did is post for an action that must go through.
func (b *browser) did(target string, fields ...string) {
	b.l.t.Helper()
	if w := b.post(target, fields...); w.Code != http.StatusSeeOther {
		b.l.t.Fatalf("POST %s %v: %d %s", target, fields, w.Code, refusalIn(w.Body.String()))
	}
}

// refusalIn is the line a People page says a refusal in, or the whole
// answer when it is no such page.
func refusalIn(body string) string {
	const start = `<p class="flash flash-alert" role="alert">`
	i := strings.Index(body, start)
	if i < 0 {
		return body
	}
	body = body[i+len(start):]
	return html.UnescapeString(body[:strings.Index(body, "</p>")])
}

// refused is post for an action the lab must refuse, saying why.
func (b *browser) refused(why, target string, fields ...string) {
	b.l.t.Helper()
	w := b.post(target, fields...)
	if said := refusalIn(w.Body.String()); w.Code < 400 || w.Code == http.StatusNotFound || !strings.Contains(said, why) {
		b.l.t.Errorf("POST %s %v: %d %q, want a refusal saying %q", target, fields, w.Code, said, why)
	}
}

func (l *signInLab) who(id int64) Person {
	l.t.Helper()
	p, err := l.s.person(id)
	if err != nil {
		l.t.Fatalf("account %d: %v", id, err)
	}
	return p
}

func (l *signInLab) known(id int64) bool {
	return l.count(`SELECT count(*) FROM people WHERE account_id = ?`, id) == 1
}

// recorded is the People record, oldest first, as "@actor what @about: note".
func (l *signInLab) recorded() []string {
	l.t.Helper()
	lines, err := l.s.store.recordLines(1000)
	if err != nil {
		l.t.Fatal(err)
	}
	var out []string
	for i := len(lines) - 1; i >= 0; i-- {
		out = append(out, lines[i].String())
	}
	return out
}

// addMac enrolls a Mac that the person with this account id lends; 0 is a
// Lab-owned Mac.
func (l *signInLab) addMac(name string, volunteer int64) *dev {
	l.t.Helper()
	d := api.Device{ID: "d-" + name, Name: name, Model: "MacBook Pro", State: api.StateReady}
	d.Kind = api.KindLabOwned
	if volunteer != 0 {
		d.Kind, d.Volunteer, d.VolunteerID, d.Contact = api.KindVolunteer, true, volunteer, "signal: +61 400 000 000"
	}
	if err := l.s.store.saveDevice(&d, hashSecret("dev_"+name)); err != nil {
		l.t.Fatal(err)
	}
	v := newDev(l.s, d, hashSecret("dev_"+name))
	l.s.mu.Lock()
	l.s.devs[name] = v
	l.s.mu.Unlock()
	return v
}

func (b *browser) macs() []string {
	b.l.t.Helper()
	w := b.do("GET", "/api/devices", "")
	if w.Code != http.StatusOK {
		b.l.t.Fatalf("GET /api/devices: %d %s", w.Code, w.Body)
	}
	var ds []DeviceView
	if err := json.Unmarshal(w.Body.Bytes(), &ds); err != nil {
		b.l.t.Fatal(err)
	}
	names := []string{}
	for _, d := range ds {
		names = append(names, d.Name)
	}
	return names
}

func personPath(id int64, action string) string {
	return "/people/" + strconv.FormatInt(id, 10) + "/" + action
}

// Marking a handle as M team, or giving it an Invite, asks GitHub at once
// which account the handle names and pins that account: nothing waits for a
// first sign-in, and a rename keeps the mark.
func TestMarkingAHandleLooksItUpOnGitHubAndPinsTheAccount(t *testing.T) {
	l := rolesLab(t, nil)
	ravi := l.signedIn(maintainerID)

	ravi.did("/people/mark", "handle", " @Edith ", "as", "m-team")
	if p := l.who(edithID); !p.MTeam || p.Handle != "edith" || p.Role() != web.MTeam {
		t.Fatalf("marked by handle, account %d is %+v", edithID, p)
	}
	if got := l.gh.LookedUp(); len(got) != 1 || got[0] != "Edith" {
		t.Errorf("GitHub was asked for %q, want the handle once, as typed without its @", got)
	}
	row := part(t, ravi.do("GET", "/people", "").Body.String(), `id="person-7"`, `</tr>`)
	if !strings.Contains(row, "@edith") || !strings.Contains(row, "not signed in yet") || !strings.Contains(row, "M team") {
		t.Errorf("the row of a handle marked before its first sign-in:\n%s", row)
	}

	ravi.did("/people/mark", "handle", "grace", "as", "invite")
	if p := l.who(graceID); !p.Lend || p.MTeam || p.Role() != web.NoRole || !p.MayLend() {
		t.Fatalf("invited by handle, account %d is %+v", graceID, p)
	}
	if row := part(t, ravi.do("GET", "/people", "").Body.String(), `id="person-8"`, `</tr>`); !strings.Contains(row, "not signed in yet") {
		t.Errorf("the row of a handle invited before its first sign-in:\n%s", row)
	}

	// The account is renamed on GitHub, and signs in for the first time.
	l.gh.AddUser(edithID, "edith-lovelace")
	edith := l.signedIn(edithID)
	if me := edith.whoami(); me.Kind != "M team member" || me.Login != "edith-lovelace" {
		t.Errorf("after a rename the marked account signs in as %+v", me)
	}
	if n := l.count(`SELECT count(*) FROM people WHERE account_id = ? OR handle LIKE 'edith%'`, edithID); n != 1 {
		t.Errorf("%d rows for the one account", n)
	}
	if row := part(t, ravi.do("GET", "/people", "").Body.String(), `id="person-7"`, `</tr>`); strings.Contains(row, "not signed in yet") || !strings.Contains(row, "@edith-lovelace") {
		t.Errorf("the row after the first sign-in:\n%s", row)
	}
	if grace := l.signedIn(graceID); !l.who(graceID).MayLend() || grace.whoami().Kind != "Signed in, no role" {
		t.Errorf("an invited person has leave to lend from their first sign-in, and no role: %+v", l.who(graceID))
	}

	// What is no GitHub handle, a handle GitHub does not know, and a mark
	// the person has already are refused, and make nobody known.
	before := l.count(`SELECT count(*) FROM people`)
	asked := len(l.gh.LookedUp())
	for _, handle := range []string{"not a handle", "-dash", strings.Repeat("a", 40), ""} {
		ravi.refused("isn't a GitHub handle", "/people/mark", "handle", handle, "as", "m-team")
	}
	if len(l.gh.LookedUp()) != asked {
		t.Error("GitHub was asked about something that is no handle")
	}
	ravi.refused("GitHub knows no @nobody-at-all", "/people/mark", "handle", "nobody-at-all", "as", "m-team")
	ravi.refused("already on the M team", "/people/mark", "handle", "edith-lovelace", "as", "m-team")
	ravi.refused("already has leave to lend", "/people/mark", "handle", "Grace", "as", "invite")
	ravi.refused("already has leave to lend", "/people/mark", "handle", "edith-lovelace", "as", "invite")
	if n := l.count(`SELECT count(*) FROM people`); n != before {
		t.Errorf("refused marks left %d people, want %d", n, before)
	}

	want := []string{
		"@ravi marked as M team (not signed in yet) @edith",
		"@ravi gave an Invite (not signed in yet) to @grace",
	}
	if got := l.recorded(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the People record:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// GitHub out of reach marks nobody: the lab never guesses an account.
func TestAMarkWaitsForNothingButNeedsGitHub(t *testing.T) {
	l := rolesLab(t, nil)
	ravi := l.signedIn(maintainerID)
	l.gh.Break(http.StatusServiceUnavailable)
	if w := ravi.post("/people/mark", "handle", "edith", "as", "m-team"); w.Code != http.StatusBadGateway || !strings.Contains(refusalIn(w.Body.String()), "GitHub") {
		t.Errorf("with GitHub down: %d %q", w.Code, refusalIn(w.Body.String()))
	}
	if l.known(edithID) {
		t.Error("a person was made without GitHub naming the account")
	}
}

// Only an Admin makes or removes a Maintainer, and a Maintainer is always on
// the M team.
func TestOnlyAnAdminMakesOrRemovesAMaintainer(t *testing.T) {
	l := rolesLab(t, nil)
	ravi, owner := l.signedIn(maintainerID), l.signedIn(ownerID)
	l.signedIn(edithID)

	for _, action := range []string{"maintainer", "maintainer/remove"} {
		if w := ravi.post(personPath(edithID, action), "sure", "yes"); w.Code != http.StatusNotFound {
			t.Errorf("a Maintainer on %s: %d, want it not found", action, w.Code)
		}
	}
	if l.who(edithID).Maintainer || len(l.recorded()) != 0 {
		t.Fatal("a Maintainer made a Maintainer")
	}

	owner.did(personPath(edithID, "maintainer"))
	if p := l.who(edithID); !p.Maintainer || !p.MTeam || !p.OnMTeam() || p.Role() != web.Maintainer {
		t.Fatalf("made a Maintainer, account %d is %+v", edithID, p)
	}
	owner.refused("already a Maintainer", personPath(edithID, "maintainer"))
	ravi.refused("a Maintainer is always on the M team", personPath(edithID, "m-team/remove"), "sure", "yes")
	owner.refused("a Maintainer is always on the M team", personPath(edithID, "m-team/remove"), "sure", "yes")

	// Removing asks twice, and leaves the person on the M team.
	if w := owner.post(personPath(edithID, "maintainer/remove")); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Remove: stays on the M team") || !l.who(edithID).Maintainer {
		t.Fatalf("the first click on Remove Maintainer: %d", w.Code)
	}
	owner.did(personPath(edithID, "maintainer/remove"), "sure", "yes")
	if p := l.who(edithID); p.Maintainer || !p.MTeam || p.Role() != web.MTeam {
		t.Errorf("no longer a Maintainer, account %d is %+v", edithID, p)
	}
	owner.refused("is no Maintainer", personPath(edithID, "maintainer/remove"), "sure", "yes")

	// An Admin is named in the configuration: no screen changes that.
	owner.refused("Admins are named in the server's configuration", personPath(ownerID, "maintainer"))
	owner.refused("Admins are named in the server's configuration", personPath(ownerID, "maintainer/remove"), "sure", "yes")
	owner.refused("an Admin is always on the M team", personPath(ownerID, "m-team/remove"), "sure", "yes")
	owner.refused("an Admin is always on the M team", personPath(ownerID, "m-team"))

	want := []string{"@owner made a Maintainer of @edith", "@owner removed as Maintainer @edith"}
	if got := l.recorded(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the People record:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Blocked shuts an account out: its sign-in, its browser sessions, its
// marks and its leave to lend end, and it meets one sentence.
func TestBlockedEndsSignInSessionsAndRoles(t *testing.T) {
	l := rolesLab(t, nil)
	ravi, owner := l.signedIn(maintainerID), l.signedIn(ownerID)
	edith, phone := l.signedIn(edithID), l.signedIn(edithID)
	ravi.did("/people/mark", "handle", "edith", "as", "invite")
	ravi.did(personPath(edithID, "m-team"))
	if p := l.who(edithID); !p.MTeam || !p.Lend {
		t.Fatalf("before the Block, account %d is %+v", edithID, p)
	}

	// The first click asks, counting what ends; nothing has changed.
	w := ravi.post(personPath(edithID, "block"), "reason", "uploads spam")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Block now: 2 browsers signed out, no longer M team, leave to lend taken back") {
		t.Fatalf("the first click on Block: %d\n%s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `<input type="hidden" name="reason" value="uploads spam">`) {
		t.Error("the page that asks again dropped the reason")
	}
	if l.who(edithID).Blocked || edith.whoami().Login != "edith" {
		t.Fatal("the first click blocked")
	}

	l.clock.Advance(time.Minute)
	ravi.did(personPath(edithID, "block"), "reason", "uploads spam", "sure", "yes")
	p := l.who(edithID)
	if !p.Blocked || p.MTeam || p.Maintainer || p.Lend || p.MayLend() {
		t.Errorf("Blocked, account %d is %+v", edithID, p)
	}
	if n := l.count(`SELECT count(*) FROM browser_sessions WHERE account_id = ?`, edithID); n != 0 {
		t.Errorf("%d browser sessions of a Blocked person are kept", n)
	}
	for _, b := range []*browser{edith, phone} {
		if me := b.whoami(); me.Login != "" {
			t.Errorf("a Blocked person's browser is still %+v", me)
		}
		if w := b.do("GET", "/api/devices", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("a Blocked person's old session on the API: %d", w.Code)
		}
	}
	again := l.browser().signIn(edithID, "")
	if again.Code != http.StatusForbidden || strings.TrimSpace(again.Body.String()) != "This GitHub account is blocked from Omarchy Mac Lab." {
		t.Errorf("a Blocked account signing in: %d %q", again.Code, again.Body)
	}
	if again.Header().Get("Set-Cookie") != "" && strings.Contains(again.Header().Get("Set-Cookie"), "lab_session") {
		t.Error("a Blocked account was given a session")
	}
	if n := l.count(`SELECT count(*) FROM browser_sessions WHERE account_id = ?`, edithID); n != 0 {
		t.Error("a Blocked account signed in")
	}

	// The row says who blocked, when and why; the kind is Blocked.
	page := ravi.do("GET", "/people?open=7", "").Body.String()
	row := part(t, page, `id="person-7"`, `id="person-7-end"`)
	for _, piece := range []string{`<span class="badge kind-blocked">Blocked</span>`, "by @ravi", "2026-11-14 12:01 UTC", "uploads spam", ">Unblock</button>"} {
		if !strings.Contains(row, piece) {
			t.Errorf("a Blocked person's opened row lacks %q:\n%s", piece, row)
		}
	}

	// While Blocked, the mark, Maintainer and an Invite are refused.
	ravi.refused("Unblock first", personPath(edithID, "m-team"))
	ravi.refused("Unblock first", "/people/mark", "handle", "edith", "as", "m-team")
	ravi.refused("Unblock first", "/people/mark", "handle", "edith", "as", "invite")
	owner.refused("Unblock first", personPath(edithID, "maintainer"))
	ravi.refused("already Blocked", personPath(edithID, "block"), "sure", "yes")
	if p := l.who(edithID); p.MTeam || p.Maintainer || p.Lend {
		t.Errorf("a Blocked person was given %+v", p)
	}

	// Unblock is one click by any Maintainer and gives back sign-in alone.
	ravi.did(personPath(edithID, "unblock"))
	if p := l.who(edithID); p.Blocked || p.MTeam || p.Maintainer || p.Lend || p.Role() != web.NoRole {
		t.Errorf("unblocked, account %d is %+v", edithID, p)
	}
	back := l.signedIn(edithID)
	if me := back.whoami(); me.Kind != "Signed in, no role" {
		t.Errorf("unblocked and signed in again: %+v", me)
	}
	if me := edith.whoami(); me.Login != "" {
		t.Error("Unblock gave an old browser session back")
	}
	ravi.refused("is not Blocked", personPath(edithID, "unblock"))

	want := []string{
		"@ravi gave an Invite to @edith",
		"@ravi marked as M team @edith",
		"@ravi blocked @edith: uploads spam",
		"@ravi unblocked @edith",
	}
	if got := l.recorded(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the People record:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Who may block whom: only an Admin blocks a Maintainer, an Admin cannot be
// Blocked, nobody blocks themselves, and only a person the lab knows.
func TestWhoMayBeBlockedAndByWhom(t *testing.T) {
	const secondAdmin, otherMaintainer = 11, 12
	l := rolesLab(t, func(c *Config) { c.Admins = []int64{ownerID, secondAdmin} })
	l.gh.AddUser(secondAdmin, "second-admin")
	l.gh.AddUser(otherMaintainer, "maya")
	ravi, owner := l.signedIn(maintainerID), l.signedIn(ownerID)
	l.signedIn(secondAdmin)
	maya := l.signedIn(otherMaintainer)
	l.signedIn(edithID)
	owner.did(personPath(otherMaintainer, "maintainer"))

	ravi.refused("Only an Admin blocks a Maintainer", personPath(otherMaintainer, "block"), "sure", "yes")
	ravi.refused("An Admin cannot be Blocked", personPath(ownerID, "block"), "sure", "yes")
	owner.refused("An Admin cannot be Blocked", personPath(secondAdmin, "block"), "sure", "yes")
	ravi.refused("Nobody blocks themselves", personPath(maintainerID, "block"), "sure", "yes")
	owner.refused("An Admin cannot be Blocked", personPath(ownerID, "block"), "sure", "yes")
	ravi.refused("one line of up to 200 characters", personPath(edithID, "block"), "sure", "yes", "reason", strings.Repeat("x", 201))
	for _, id := range []int64{ownerID, secondAdmin, maintainerID, otherMaintainer} {
		if l.who(id).Blocked {
			t.Errorf("account %d was Blocked", id)
		}
	}

	// GitHub knows account 9, the lab does not: there is nobody to block.
	if w := ravi.post(personPath(linusID, "block"), "sure", "yes"); w.Code != http.StatusNotFound || l.known(linusID) {
		t.Errorf("blocking an account the lab does not know: %d, known %v", w.Code, l.known(linusID))
	}

	// An Admin blocks a Maintainer, with no reason; the role goes with it,
	// and any Maintainer unblocks.
	owner.did(personPath(otherMaintainer, "block"), "sure", "yes")
	if p := l.who(otherMaintainer); !p.Blocked || p.Maintainer || p.MTeam {
		t.Errorf("a Blocked Maintainer is %+v", p)
	}
	if w := maya.do("GET", "/people", ""); w.Code == http.StatusOK {
		t.Error("a Blocked Maintainer still opens People")
	}
	ravi.did(personPath(otherMaintainer, "unblock"))
	if p := l.who(otherMaintainer); p.Blocked || p.Maintainer || p.MTeam {
		t.Errorf("unblocked, the Maintainer is %+v", p)
	}
	want := []string{"@owner made a Maintainer of @maya", "@owner blocked @maya", "@ravi unblocked @maya"}
	if got := l.recorded(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the People record:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Leaving the M team: the person's Macs stay enrolled with no new approval,
// leave to lend stays, and above the cap of 5 they keep what they have.
// Lending no Mac, they return to no role.
func TestLeavingTheMTeam(t *testing.T) {
	l := rolesLab(t, nil)
	ravi := l.signedIn(maintainerID)
	edith, grace := l.signedIn(edithID), l.signedIn(graceID)
	ravi.did(personPath(edithID, "m-team"))
	ravi.did(personPath(graceID, "m-team"))
	var macs []string
	for i := 1; i <= 6; i++ {
		name := "edith-mac-" + strconv.Itoa(i)
		l.addMac(name, edithID)
		macs = append(macs, name)
	}
	lab := l.addMac("lab-mini", 0)
	lab.mu.Lock()
	lab.d.Lease = &api.Lease{Holder: "edith", Maintainer: "edith", Expires: l.clock.Now().Add(time.Hour)}
	lab.mu.Unlock()
	if me := edith.whoami(); me.Kind != "M team member" {
		t.Fatalf("on the M team and lending: %+v", me)
	}

	if w := ravi.post(personPath(edithID, "m-team/remove")); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Remove: 1 Lease ends now") || !l.who(edithID).MTeam {
		t.Fatalf("the first click on Remove from M team: %d\n%s", w.Code, w.Body)
	}
	ravi.did(personPath(edithID, "m-team/remove"), "sure", "yes")
	p := l.who(edithID)
	if p.MTeam || !p.Lend || !p.MayLend() || p.Role() != web.External {
		t.Errorf("off the M team with Macs enrolled, account %d is %+v", edithID, p)
	}
	if me := edith.whoami(); me.Kind != "External volunteer" {
		t.Errorf("the person now reads as %+v", me)
	}
	if got := edith.macs(); fmt.Sprint(got) != fmt.Sprint(macs) {
		t.Errorf("their Macs after leaving: %v, want all six still enrolled", got)
	}
	for _, name := range macs {
		if d := l.s.dev(name).snapshot(); d.State == api.StateLeft || d.VolunteerID != edithID {
			t.Errorf("%s after its Volunteer left the M team: %+v", name, d.State)
		}
	}
	if d := lab.snapshot(); d.Lease != nil {
		t.Errorf("the Lease they held is still there: %+v", d.Lease)
	}

	// Lending no Mac, and never invited: no role, and no leave to lend.
	ravi.did(personPath(graceID, "m-team/remove"), "sure", "yes")
	if p := l.who(graceID); p.MTeam || p.Lend || p.MayLend() || p.Role() != web.NoRole {
		t.Errorf("off the M team with no Mac, account %d is %+v", graceID, p)
	}
	if me := grace.whoami(); me.Kind != "Signed in, no role" {
		t.Errorf("the person now reads as %+v", me)
	}
	ravi.refused("is not on the M team", personPath(graceID, "m-team/remove"), "sure", "yes")
}

// An External volunteer sees only their own Macs. An M team member sees the
// fleet and whose each Mac is, and changes nothing. A signed-in person with
// no role, and the public, see no Mac.
func TestWhoSeesTheFleet(t *testing.T) {
	l := rolesLab(t, nil)
	ravi := l.signedIn(maintainerID)
	edith, grace, linus := l.signedIn(edithID), l.signedIn(graceID), l.signedIn(linusID)
	l.addMac("edith-air", edithID)
	l.addMac("grace-pro", graceID)
	mini := l.addMac("lab-mini", 0)
	mini.mu.Lock()
	mini.d.Lease = &api.Lease{Holder: "ravi", Maintainer: "ravi", Expires: l.clock.Now().Add(time.Hour)}
	mini.mu.Unlock()
	ravi.did(personPath(graceID, "m-team"))

	// The External volunteer.
	if me := edith.whoami(); me.Kind != "External volunteer" {
		t.Fatalf("lending a Mac, off the M team: %+v", me)
	}
	if got := edith.macs(); fmt.Sprint(got) != "[edith-air]" {
		t.Errorf("an External volunteer sees %v, want their own Mac only", got)
	}
	if w := edith.do("GET", "/api/devices/edith-air", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "+61 400") {
		t.Errorf("their own Mac: %d", w.Code)
	}
	for _, other := range []string{"grace-pro", "lab-mini", "no-such-mac"} {
		if w := edith.do("GET", "/api/devices/"+other, ""); w.Code != http.StatusNotFound {
			t.Errorf("an External volunteer asking for %s: %d, want it not found", other, w.Code)
		}
	}
	if w := edith.write("POST", "/api/volunteer", `{}`); w.Code != http.StatusForbidden {
		t.Errorf("an External volunteer on the old ./volunteer route: %d %s", w.Code, w.Body)
	}

	// The M team member.
	if got := grace.macs(); fmt.Sprint(got) != "[edith-air grace-pro lab-mini]" {
		t.Errorf("an M team member sees %v, want the fleet", got)
	}
	fleet := grace.do("GET", "/api/devices", "").Body.String()
	for _, handle := range []string{`"volunteer_handle":"edith"`, `"volunteer_handle":"grace"`, `"holder":"ravi"`} {
		if !strings.Contains(fleet, handle) {
			t.Errorf("the fleet an M team member sees lacks %s", handle)
		}
	}
	if strings.Contains(fleet, "+61 400") {
		t.Error("an M team member sees a Volunteer's contact, which is for Maintainers")
	}
	if !strings.Contains(ravi.do("GET", "/api/devices", "").Body.String(), "+61 400") {
		t.Error("a Maintainer does not see a Volunteer's contact")
	}
	for _, c := range []struct{ method, target, body string }{
		{"POST", "/api/jobs", `{"device":"lab-mini"}`},
		{"DELETE", "/api/devices/edith-air", ""},
		{"GET", "/api/builds", ""},
		{"GET", "/api/devices/edith-air/audit", ""},
		{"POST", "/api/volunteer", `{}`},
	} {
		if w := grace.write(c.method, c.target, c.body); w.Code != http.StatusForbidden {
			t.Errorf("an M team member on %s %s: %d %s", c.method, c.target, w.Code, w.Body)
		}
	}
	for _, live := range []string{"screen", "screens", "logs"} {
		if w := grace.do("GET", "/api/devices/edith-air/"+live, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "their own Lease") {
			t.Errorf("an M team member on a Mac's %s with no Lease of their own: %d %s", live, w.Code, w.Body)
		}
	}

	// No role, and the public.
	if w := linus.do("GET", "/api/devices", ""); w.Code != http.StatusForbidden {
		t.Errorf("a signed-in person with no role: %d", w.Code)
	}
	if w := l.browser().do("GET", "/api/devices", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("the public: %d", w.Code)
	}
	for _, b := range []*browser{l.browser(), linus, edith, grace} {
		home := b.do("GET", "/", "").Body.String()
		main := part(t, home, `<main`, `</main>`)
		for _, handle := range []string{"@edith", "@grace", "@ravi", "@owner"} {
			if strings.Contains(main, handle) {
				t.Errorf("a public page shows the handle %s", handle)
			}
		}
	}

	// The mark's end takes the fleet away on the next request, and cuts
	// the stream that was open.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https")
		l.h.ServeHTTP(w, r.WithContext(onTunnel(r.Context())))
	}))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/stream", nil)
	req.Host = labHost
	req.AddCookie(grace.session())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	rd := bufio.NewReader(res.Body)
	if line, err := rd.ReadString('\n'); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("an M team member's stream: %d %q %v", res.StatusCode, line, err)
	}
	ended := make(chan error, 1)
	go func() {
		for {
			if _, err := rd.ReadString('\n'); err != nil {
				ended <- err
				return
			}
		}
	}()
	ravi.did(personPath(graceID, "m-team/remove"), "sure", "yes")
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Error("the stream stayed open after the mark went")
	}
	if got := grace.macs(); fmt.Sprint(got) != "[grace-pro]" {
		t.Errorf("off the M team, lending one Mac, they see %v", got)
	}
}

// People is for Maintainers: below that it is not there, and signed out it
// leads to sign-in. Nothing below a Maintainer changes anybody.
func TestPeopleIsNotFoundBelowMaintainer(t *testing.T) {
	l := rolesLab(t, nil)
	ravi := l.signedIn(maintainerID)
	edith, grace, linus := l.signedIn(edithID), l.signedIn(graceID), l.signedIn(linusID)
	l.addMac("edith-air", edithID)
	ravi.did(personPath(graceID, "m-team"))
	lines := len(l.recorded())

	actions := [][]string{
		{"/people/mark", "handle", "linus", "as", "m-team"},
		{personPath(linusID, "m-team")},
		{personPath(graceID, "m-team/remove"), "sure", "yes"},
		{personPath(linusID, "maintainer")},
		{personPath(linusID, "block"), "sure", "yes"},
		{personPath(linusID, "unblock")},
	}
	for kind, b := range map[string]*browser{"External volunteer": edith, "M team member": grace, "Signed in, no role": linus} {
		if me := b.whoami(); me.Kind != kind {
			t.Fatalf("%+v, want a %s", me, kind)
		}
		if w := b.do("GET", "/people", ""); w.Code != http.StatusNotFound {
			t.Errorf("%s on People: %d, want it not found", kind, w.Code)
		}
		if nav := part(t, b.do("GET", "/", "").Body.String(), `<div class="lab-row">`, `</div>`); strings.Contains(nav, "People") {
			t.Errorf("%s has People in the navigation", kind)
		}
		for _, a := range actions {
			if w := b.post(a[0], a[1:]...); w.Code != http.StatusNotFound {
				t.Errorf("%s on %s: %d, want it not found", kind, a[0], w.Code)
			}
		}
	}
	out := l.browser()
	if w := out.do("GET", "/people", ""); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/sign-in?return=") {
		t.Errorf("signed out on People: %d to %q", w.Code, w.Header().Get("Location"))
	}
	for _, a := range actions {
		if w := out.do("POST", a[0], "sure=yes", "Content-Type", "application/x-www-form-urlencoded", "Origin", labAddress); w.Code != http.StatusSeeOther {
			t.Errorf("signed out on %s: %d", a[0], w.Code)
		}
	}
	if p := l.who(linusID); p.MTeam || p.Maintainer || p.Blocked || !l.who(graceID).MTeam || len(l.recorded()) != lines {
		t.Errorf("somebody below a Maintainer changed a person: %+v", p)
	}
	w := ravi.do("GET", "/people", "")
	if nav := part(t, w.Body.String(), `<div class="lab-row">`, `</div>`); w.Code != http.StatusOK || !strings.Contains(nav, `class="current" aria-current="page">People`) {
		t.Errorf("a Maintainer on People: %d", w.Code)
	}
}

// The table of people: one row a person with their kind, chips that filter
// by kind and count each, a row that opens in place, and the record.
func TestPeopleShowsTheTableItsChipsAndTheRecord(t *testing.T) {
	l := rolesLab(t, nil)
	ravi, owner := l.signedIn(maintainerID), l.signedIn(ownerID)
	l.signedIn(edithID)
	l.signedIn(graceID)
	l.signedIn(linusID)
	l.signedIn(nobodyID)
	l.addMac("edith-air", edithID)
	l.addMac("edith-mini", edithID)
	mini := l.addMac("lab-mini", 0)
	mini.mu.Lock()
	mini.d.Lease = &api.Lease{Holder: "grace", Maintainer: "grace", Expires: l.clock.Now().Add(time.Hour)}
	mini.mu.Unlock()
	ravi.did(personPath(graceID, "m-team"))
	ravi.did(personPath(linusID, "block"), "sure", "yes")

	page := ravi.do("GET", "/people", "").Body.String()
	chips := part(t, page, `<p class="chips">`, `</p>`)
	if got, want := linkLabels(chips), "All 7, Admin 1, Maintainer 2, M team 1, External volunteer 1, No role 1, Blocked 1"; got != want {
		t.Errorf("the chips: %s\nwant:       %s", got, want)
	}
	for id, pieces := range map[int64][]string{
		ownerID:      {"@owner", `kind-admin">Admin<`},
		maintainerID: {"@ravi", `kind-maintainer">Maintainer<`, `class="you"`},
		graceID:      {"@grace", `kind-mteam">M team<`, "lab-mini"},
		edithID:      {"@edith", `kind-external">External volunteer<`, "edith-air", "edith-mini", "2026-11-14 12:00 UTC"},
		nobodyID:     {"@nobody-yet", `kind-none">no role<`},
		linusID:      {"@linus", `kind-blocked">Blocked<`},
	} {
		row := part(t, page, fmt.Sprintf(`id="person-%d"`, id), `</tr>`)
		for _, piece := range pieces {
			if !strings.Contains(row, piece) {
				t.Errorf("the row of account %d lacks %q:\n%s", id, piece, row)
			}
		}
	}
	if strings.Contains(page, `id="person-7-end"`) {
		t.Error("a row is open that nobody opened")
	}

	// A chip keeps to its kind.
	only := ravi.do("GET", "/people?kind=blocked", "").Body.String()
	if !strings.Contains(only, `id="person-9"`) || strings.Contains(only, `id="person-7"`) || !strings.Contains(only, `class="chip current" href="/people?kind=blocked">Blocked 1<`) {
		t.Error("the Blocked chip does not keep to the Blocked")
	}

	// A row opens in place. A Maintainer marks; only an Admin is offered
	// Make Maintainer; an Admin's row says where Admins come from.
	open := part(t, ravi.do("GET", "/people?open=8", "").Body.String(), `id="person-8"`, `id="person-8-end"`)
	for _, piece := range []string{"Remove from M team", `data-sentence="Remove: 1 Lease ends now"`, "Only an Admin makes a Maintainer", ">Block</button>", "@ravi marked as M team @grace"} {
		if !strings.Contains(open, piece) {
			t.Errorf("a Maintainer's view of an opened row lacks %q:\n%s", piece, open)
		}
	}
	if strings.Contains(open, "Make Maintainer</button>") {
		t.Error("a Maintainer is offered Make Maintainer")
	}
	if open := part(t, owner.do("GET", "/people?open=8", "").Body.String(), `id="person-8"`, `id="person-8-end"`); !strings.Contains(open, "Make Maintainer</button>") {
		t.Errorf("an Admin is not offered Make Maintainer:\n%s", open)
	}
	if open := part(t, owner.do("GET", "/people?open=1", "").Body.String(), `id="person-1"`, `id="person-1-end"`); !strings.Contains(open, "Admins are named in the server's configuration") || strings.Contains(open, ">Block</button>") {
		t.Errorf("an Admin's opened row:\n%s", open)
	}
	if open := part(t, ravi.do("GET", "/people?open=3003", "").Body.String(), `id="person-3003"`, `id="person-3003-end"`); strings.Contains(open, ">Block</button>") || !strings.Contains(open, "This is you") {
		t.Errorf("a Maintainer's own opened row:\n%s", open)
	}

	// The record, newest first, for the whole screen.
	record := part(t, page, `<ol class="record">`, `</ol>`)
	if a, b := strings.Index(record, "blocked"), strings.Index(record, "marked as M team"); a < 0 || b < 0 || a > b {
		t.Errorf("the record is not newest first:\n%s", record)
	}
	if !strings.Contains(record, "2026-11-14 12:00 UTC") {
		t.Errorf("a record line lacks its time:\n%s", record)
	}
	if !strings.Contains(page, "No request is waiting.") {
		t.Error("People lacks its requests, empty as they are")
	}
}

// Every change of who someone is writes one line of the People record and
// one line for the owner's group "Agents", with the handle.
func TestEveryRoleChangeIsRecordedAndToldToAgents(t *testing.T) {
	l := rolesLab(t, nil)
	ravi, owner := l.signedIn(maintainerID), l.signedIn(ownerID)
	l.signedIn(edithID)
	for _, step := range []struct {
		by           *browser
		target       string
		fields       []string
		record, told string
	}{
		{ravi, personPath(edithID, "m-team"), nil, "@ravi marked as M team @edith", "ravi marked edith as M team."},
		{owner, personPath(edithID, "maintainer"), nil, "@owner made a Maintainer of @edith", "owner made edith a Maintainer."},
		{owner, personPath(edithID, "maintainer/remove"), []string{"sure", "yes"}, "@owner removed as Maintainer @edith", "owner removed edith as a Maintainer."},
		{ravi, personPath(edithID, "m-team/remove"), []string{"sure", "yes"}, "@ravi removed from the M team @edith", "ravi removed the M team mark from edith."},
		{ravi, "/people/mark", []string{"handle", "grace", "as", "invite"}, "@ravi gave an Invite (not signed in yet) to @grace", "ravi gave grace an Invite."},
		{ravi, "/people/mark", []string{"handle", "linus", "as", "m-team"}, "@ravi marked as M team (not signed in yet) @linus", "ravi marked linus as M team."},
		{ravi, personPath(edithID, "block"), []string{"sure", "yes", "reason", "spam"}, "@ravi blocked @edith: spam", "ravi blocked edith."},
		{owner, personPath(edithID, "unblock"), nil, "@owner unblocked @edith", "owner unblocked edith."},
	} {
		records, told := len(l.recorded()), len(l.agentsLines())
		l.clock.Advance(time.Minute)
		step.by.did(step.target, step.fields...)
		got, lines := l.recorded(), l.agentsLines()
		if len(got) != records+1 || got[len(got)-1] != step.record {
			t.Errorf("%s: the People record gained %q, want one line, %q", step.target, got[records:], step.record)
		}
		if len(lines) != told+1 || lines[len(lines)-1] != step.told {
			t.Errorf("%s: \"Agents\" was told %q, want one line, %q", step.target, lines[told:], step.told)
		}
		if at := l.lastRecordedAt(); !at.Equal(l.clock.Now()) {
			t.Errorf("%s: recorded at %s, want %s", step.target, at, l.clock.Now())
		}
	}
	// A refusal writes neither.
	records, told := len(l.recorded()), len(l.agentsLines())
	ravi.refused("Nobody blocks themselves", personPath(maintainerID, "block"), "sure", "yes")
	ravi.refused("isn't a GitHub handle", "/people/mark", "handle", "-dash", "as", "m-team")
	if len(l.recorded()) != records || len(l.agentsLines()) != told {
		t.Error("a refused action was recorded or told")
	}
}

// agentsLines is every line labd has for the owner's group "Agents", oldest
// first.
func (l *signInLab) agentsLines() []string { return l.told("agents") }

func (l *signInLab) told(channel string) []string {
	l.t.Helper()
	rows, err := l.s.store.db.Query(`SELECT text FROM lines WHERE channel = ? ORDER BY id`, channel)
	if err != nil {
		l.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var text string
		rows.Scan(&text)
		out = append(out, text)
	}
	return out
}

func (l *signInLab) lastRecordedAt() time.Time {
	l.t.Helper()
	lines, err := l.s.store.recordLines(1)
	if err != nil || len(lines) != 1 {
		l.t.Fatalf("the People record: %d lines (%v)", len(lines), err)
	}
	return lines[0].At
}

// A Lab token does what its person may do at that moment, whatever their
// kind: an M team member's reads the fleet, an External volunteer's their
// own Macs, and neither changes anything.
func TestALabTokenActsWithItsPersonsKind(t *testing.T) {
	l := rolesLab(t, nil)
	ravi := l.signedIn(maintainerID)
	edith, grace, linus := l.signedIn(edithID), l.signedIn(graceID), l.signedIn(linusID)
	l.addMac("edith-air", edithID)
	l.addMac("lab-mini", 0)
	ravi.did(personPath(graceID, "m-team"))
	own, team, none := edith.makeToken("laptop", "90"), grace.makeToken("laptop", "90"), linus.makeToken("laptop", "90")

	names := func(token string) string {
		t.Helper()
		w := l.asToken(token, "GET", "/api/devices", "198.51.100.7")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/devices: %d %s", w.Code, w.Body)
		}
		var ds []DeviceView
		json.Unmarshal(w.Body.Bytes(), &ds)
		var out []string
		for _, d := range ds {
			out = append(out, d.Name)
		}
		return fmt.Sprint(out)
	}
	if got := names(own); got != "[edith-air]" {
		t.Errorf("an External volunteer's token sees %s", got)
	}
	if got := names(team); got != "[edith-air lab-mini]" {
		t.Errorf("an M team member's token sees %s", got)
	}
	if w := l.asToken(none, "GET", "/api/devices", "198.51.100.7"); w.Code != http.StatusForbidden {
		t.Errorf("the token of a person with no role: %d", w.Code)
	}
	for _, token := range []string{own, team} {
		if w := l.asToken(token, "POST", "/api/jobs", "198.51.100.7"); w.Code != http.StatusForbidden {
			t.Errorf("a token below Maintainer on a Maintainer's route: %d %s", w.Code, w.Body)
		}
	}
	// The mark goes: the same token sees no Mac on its next request.
	ravi.did(personPath(graceID, "m-team/remove"), "sure", "yes")
	if w := l.asToken(team, "GET", "/api/devices", "198.51.100.7"); w.Code != http.StatusForbidden {
		t.Errorf("off the M team, the token: %d", w.Code)
	}
}

// Blocked ends a person's Lab tokens: each opens nothing from then on, is
// answered with the one sentence, keeps no address, and is gone for good
// when the person is unblocked.
func TestBlockedEndsLabTokensAndLabIsToldTheSentence(t *testing.T) {
	l := rolesLab(t, nil)
	ravi := l.signedIn(maintainerID)
	edith := l.signedIn(edithID)
	ravi.did(personPath(edithID, "m-team"))
	token := edith.makeToken("laptop", "90")
	if w := l.asToken(token, "GET", "/api/devices", "198.51.100.7"); w.Code != http.StatusOK {
		t.Fatalf("an M team member's token: %d %s", w.Code, w.Body)
	}
	if n := l.count(`SELECT count(*) FROM lab_tokens WHERE account_id = ? AND last_used_address = '198.51.100.7'`, edithID); n != 1 {
		t.Fatal("the token's use was not written down")
	}

	// The opened row lists the token, with Revoke and "End all access".
	row := part(t, ravi.do("GET", "/people?open=7", "").Body.String(), `id="person-7"`, `id="person-7-end"`)
	for _, piece := range []string{"laptop", "from 198.51.100.7", fmt.Sprintf(`action="/people/7/tokens/%d/revoke"`, l.tokenID(edithID, "laptop")), `action="/people/7/end-all-access"`, "1 Lab token stopped"} {
		if !strings.Contains(row, piece) {
			t.Errorf("the opened row lacks %q:\n%s", piece, row)
		}
	}

	ravi.did(personPath(edithID, "block"), "sure", "yes")
	for _, target := range []string{"/api/devices", "/api/auth", "/api/jobs"} {
		w := l.asToken(token, "GET", target, "198.51.100.8")
		if w.Code != http.StatusForbidden || strings.TrimSpace(w.Body.String()) != "This GitHub account is blocked from Omarchy Mac Lab." {
			t.Errorf("a Blocked person's token on %s: %d %q", target, w.Code, w.Body)
		}
	}
	if n := l.count(`SELECT count(*) FROM lab_tokens WHERE account_id = ? AND (last_used IS NOT NULL OR last_used_address != '')`, edithID); n != 0 {
		t.Error("a Blocked person's token keeps its last use")
	}
	if row := part(t, ravi.do("GET", "/people?open=7", "").Body.String(), `id="person-7"`, `id="person-7-end"`); strings.Contains(row, "/tokens/") || strings.Contains(row, "end-all-access") {
		t.Errorf("a Blocked person's row still lists the token:\n%s", row)
	}

	ravi.did(personPath(edithID, "unblock"))
	if w := l.asToken(token, "GET", "/api/devices", "198.51.100.8"); w.Code != http.StatusUnauthorized {
		t.Errorf("unblocked, the old token: %d %s", w.Code, w.Body)
	}
	if n := l.count(`SELECT count(*) FROM lab_tokens WHERE account_id = ?`, edithID); n != 0 {
		t.Errorf("Unblock gave %d Lab tokens back", n)
	}
}

// A handle one account left and another took reads the same on two rows
// until the first signs in again: People tells the two accounts apart.
func TestPeopleTellsApartTwoAccountsUnderOneHandle(t *testing.T) {
	l := rolesLab(t, nil)
	ravi := l.signedIn(maintainerID)
	l.signedIn(edithID)
	l.gh.AddUser(edithID, "edith-lovelace")
	l.gh.AddUser(graceID, "edith")
	ravi.did("/people/mark", "handle", "edith", "as", "m-team")
	if p := l.who(graceID); !p.MTeam || l.who(edithID).MTeam {
		t.Fatalf("the mark went to the account that left the handle: %+v", p)
	}
	page := ravi.do("GET", "/people", "").Body.String()
	for id, want := range map[int64]string{edithID: "account 7<", graceID: "account 8<"} {
		if row := part(t, page, fmt.Sprintf(`id="person-%d"`, id), `</tr>`); !strings.Contains(row, "@edith<") || !strings.Contains(row, want) {
			t.Errorf("the row of account %d does not say which account it is:\n%s", id, row)
		}
	}
	if row := part(t, page, `id="person-3003"`, `</tr>`); strings.Contains(row, "account 3003") {
		t.Error("a handle only one account goes by is shown with its account id")
	}
}
