package reports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Translated from site/test/integration/tester_sign_in_test.rb,
// tester_requests_test.rb and the cases of tester_gating_test.rb that say
// which run is a Verified run. Where the lab differs from the old site by
// the spec, the test says so: the verdict is stamped at upload and never
// recomputed, the M team's mark hangs on the account id, a handle keeps
// GitHub's case, and a Blocked person is told one sentence.

const (
	goldenToken = "gho_goldenTesterToken0123456789"
	// goldenAt is the clock the tool's golden status and sign-out are dated at.
	goldenAt = 1790553600
	// goldenSignIn names the sign-in made five minutes before goldenAt.
	goldenSignIn = "1790553300000000"
)

type account struct {
	id     int64
	handle string
}

// fakeGitHub knows the tokens the lab's app issued, and those another did.
type fakeGitHub struct {
	issued  map[string]account
	other   map[string]account
	revoked []string
}

func (g *fakeGitHub) TokenUser(_ context.Context, token string) (int64, string, error) {
	who, ok := g.issued[token]
	if !ok {
		return 0, "", errors.New("GitHub answered HTTP 404")
	}
	return who.id, who.handle, nil
}

func (g *fakeGitHub) Revoke(_ context.Context, token string) { g.revoked = append(g.revoked, token) }

// fakePeople is labd.db's side: who is on the M team, who is Blocked, and
// whom a Mac sign-in made known.
type fakePeople struct {
	team, blocked map[int64]bool
	known         map[int64]string
	// blockAfter blocks the account once the lab has met it, as a Block
	// landing while a sign-in is on its way does.
	blockAfter int64
}

func (p *fakePeople) Know(id int64, handle string) (bool, error) {
	if p.blocked[id] {
		return true, nil
	}
	p.known[id] = handle
	if id == p.blockAfter {
		p.blocked[id] = true
	}
	return false, nil
}

func (p *fakePeople) Blocked(id int64) bool { return p.blocked[id] }
func (p *fakePeople) OnMTeam(id int64) bool { return p.team[id] && !p.blocked[id] }

// signInDesk is the upload desk with Mac sign-in beside it. Account 4242,
// @MaralcBR, is on the M team and holds the golden token.
type signInDesk struct {
	*desk
	github  *fakeGitHub
	people  *fakePeople
	signIns *MacSignIns
}

func newSignInDesk(t *testing.T) *signInDesk {
	t.Helper()
	d := &signInDesk{desk: newDesk(t),
		github: &fakeGitHub{issued: map[string]account{goldenToken: {4242, "MaralcBR"}}, other: map[string]account{}},
		people: &fakePeople{team: map[int64]bool{4242: true}, blocked: map[int64]bool{}, known: map[int64]string{}}}
	d.now = time.Unix(goldenAt, 0).UTC()
	d.uploads.OnMTeam = d.people.OnMTeam
	d.signIns = NewMacSignIns(d.store, testRules(t), func() time.Time { return d.now }, d.github, d.people)
	return d
}

func goldenSignInText(t testing.TB, name string) []byte {
	t.Helper()
	return read(t, filepath.Join(goldenDir, "sign-in", "tester-"+name+".json"))
}

// fixtureReport is the golden report the tool signed with its fixture key,
// the key of the golden sign-in.
func fixtureReport(t testing.TB) []byte {
	t.Helper()
	return read(t, filepath.Join(goldenDir, "signed", "m2-max-image2.json"))
}

func (d *signInDesk) ask(t testing.TB, path string, body any, ip string) answer {
	t.Helper()
	var text []byte
	switch body := body.(type) {
	case []byte:
		text = body
	case string:
		text = []byte(body)
	default:
		text = encode(t, body)
	}
	r := httptest.NewRequest("POST", site+path, bytes.NewReader(text))
	r.Header.Set("Content-Type", "application/json")
	return d.answered(t, r, path, ip)
}

func (d *signInDesk) answered(t testing.TB, r *http.Request, path, ip string) answer {
	t.Helper()
	w := httptest.NewRecorder()
	var err error
	if path == "/api/v1/tester_bindings" {
		err = d.signIns.HandleSignIn(w, r, ip)
	} else {
		err = d.signIns.HandleRequest(w, r, ip)
	}
	if err != nil {
		t.Fatal(err)
	}
	a := answer{status: w.Code, header: w.Header()}
	if err := json.Unmarshal(w.Body.Bytes(), &a.body); err != nil {
		t.Fatalf("the answer is not a JSON object: %d %q", w.Code, w.Body)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
	return a
}

// signIn sends a sign-in: the golden one unless body is given.
func (d *signInDesk) signIn(t testing.TB, body ...any) answer {
	t.Helper()
	if len(body) == 0 {
		return d.ask(t, "/api/v1/tester_bindings", goldenSignInText(t, "sign-in"), "10.0.0.1")
	}
	return d.ask(t, "/api/v1/tester_bindings", body[0], "10.0.0.1")
}

// signInOn is a sign-in on a test machine with a token issued to who.
func (d *signInDesk) signInOn(t testing.TB, machine string, who account) answer {
	t.Helper()
	token := "gho_" + machine + "_" + who.handle + "_" + strconv.Itoa(len(d.github.revoked))
	d.github.issued[token] = who
	return d.signIn(t, signedAs(t, object("binding_version", integer(1), "github_token", token), machine))
}

func signedAs(t testing.TB, document *Object, machine string) *Object {
	t.Helper()
	return signed(t, document, machine, NamespaceSignIn, nil, nil)
}

// request sends a status or a sign-out with the clock at at.
func (d *signInDesk) request(t testing.TB, body any, at time.Time) answer {
	t.Helper()
	was := d.now
	d.now = at
	defer func() { d.now = was }()
	return d.ask(t, "/api/v1/tester_requests", body, "10.0.0.1")
}

func (d *signInDesk) golden(t testing.TB, name string) answer {
	t.Helper()
	return d.request(t, goldenSignInText(t, name), time.Unix(goldenAt, 0))
}

// signInBefore is the golden sign-in, made five minutes before the golden
// requests are dated.
func (d *signInDesk) signInBefore(t testing.TB) {
	t.Helper()
	d.signInAt(t, time.Unix(goldenAt-300, 0))
}

func (d *signInDesk) signInAt(t testing.TB, at time.Time) {
	t.Helper()
	was := d.now
	d.now = at
	defer func() { d.now = was }()
	if a := d.signIn(t); a.status != http.StatusCreated {
		t.Fatalf("the golden sign-in: %d %v", a.status, a.body)
	}
}

func testRequest(t testing.TB, machine, request string, more ...any) *Object {
	t.Helper()
	document := object("request_version", integer(1), "request", request, "requested_at", integer(goldenAt))
	for i := 0; i+1 < len(more); i += 2 {
		document.set(more[i].(string), more[i+1])
	}
	return signedAs(t, document, machine)
}

func (d *signInDesk) signedIn(t *testing.T) []string {
	t.Helper()
	return d.store.list(t, `SELECT handle || ' ' || account_id FROM mac_sign_ins ORDER BY id`)
}

func (d *signInDesk) report(t testing.TB, a answer) *Report {
	t.Helper()
	if a.status != http.StatusCreated {
		t.Fatalf("upload: %d %v", a.status, a.body)
	}
	report, err := d.store.Report(a.text("id"))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func wantBody(t *testing.T, a answer, status int, want map[string]any) {
	t.Helper()
	if a.status != status || !reflect.DeepEqual(a.body, want) {
		t.Errorf("answered %d %v, want %d %v", a.status, a.body, status, want)
	}
}

// ---- tester_sign_in_test.rb ----

func TestTheGoldenSignInBindsTheGitHubHandleToTheMachineKeyAndTheTokenIsRevokedNeverKept(t *testing.T) {
	d := newSignInDesk(t)
	a := d.signIn(t)
	if a.status != http.StatusCreated || a.body["login"] != "MaralcBR" || a.body["tester"] != true {
		t.Fatalf("answered %d %v", a.status, a.body)
	}
	if got := a.text("message"); got != "Signed in as @MaralcBR. This Mac's runs are published as Verified runs." {
		t.Errorf("message %q", got)
	}
	// The old site kept the handle in lower case; the lab keeps it as GitHub writes it.
	if got := d.signedIn(t); !reflect.DeepEqual(got, []string{"MaralcBR 4242"}) {
		t.Errorf("sign-ins %q", got)
	}
	if !reflect.DeepEqual(d.github.revoked, []string{goldenToken}) {
		t.Errorf("revoked %q", d.github.revoked)
	}
	if columns := d.store.list(t, `SELECT name FROM pragma_table_info('mac_sign_ins')`); strings.Contains(strings.Join(columns, " "), "token") {
		t.Errorf("mac_sign_ins keeps a token: %q", columns)
	}
	if got := d.people.known[4242]; got != "MaralcBR" {
		t.Errorf("the lab knows account 4242 as %q", got)
	}

	report := d.report(t, d.post(t, fixtureReport(t)))
	if machines := d.store.list(t, `SELECT machine_id FROM mac_sign_ins`); len(machines) != 1 || machines[0] != report.MachineID {
		t.Errorf("the sign-in is of %q and the report of %q: one machine key signed both", machines, report.MachineID)
	}
}

func TestAfterSigningInTheMachinesRunsAreVerifiedRunsAndTheUploadSaysSo(t *testing.T) {
	d := newSignInDesk(t)
	before := d.post(t, run(t, 1))
	if before.body["tester"] != false {
		t.Errorf("an upload before any sign-in answered tester %v", before.body["tester"])
	}
	if a := d.signInOn(t, "a", account{4242, "MaralcBR"}); a.status != http.StatusCreated {
		t.Fatalf("sign-in: %d %v", a.status, a.body)
	}

	after := d.post(t, run(t, 2))
	if after.body["tester"] != true {
		t.Errorf("an upload from a Mac signed in as an M team member answered tester %v", after.body["tester"])
	}
	if report := d.report(t, after); !report.Verified || report.Handle != "MaralcBR" || report.AccountID != 4242 {
		t.Errorf("stamped %+v", report)
	}
	if report := d.report(t, before); report.Verified || report.Handle != "" {
		t.Errorf("the earlier report became %+v", report)
	}
	// Whoever uploads: the same Mac's report from another network counts the same.
	if elsewhere := d.post(t, run(t, 3), "a", "198.51.100.7"); elsewhere.body["tester"] != true {
		t.Error("the Mac's report sent from another network is no Verified run")
	}
	if other := d.post(t, run(t, 4), "b"); other.body["tester"] != false {
		t.Error("another Mac's report is a Verified run")
	}
}

// The old site looked the allowlist up each time a page was drawn, so adding
// a handle turned its earlier runs into tester runs. The lab stamps the
// verdict at upload: the earlier run stays a Community run.
func TestASignInByAPersonOutsideTheMTeamStaysAndItsRunsAreCommunityRuns(t *testing.T) {
	d := newSignInDesk(t)
	d.people.team = map[int64]bool{}
	a := d.signIn(t)
	if a.status != http.StatusCreated || a.body["tester"] != false {
		t.Fatalf("answered %d %v", a.status, a.body)
	}
	if got := a.text("message"); got != "Signed in as @MaralcBR. This Mac's runs are published as Community runs; Verified runs come from Macs of the Omarchy M team." {
		t.Errorf("message %q", got)
	}
	if got := d.signedIn(t); len(got) != 1 {
		t.Fatalf("sign-ins %q", got)
	}
	first := d.post(t, fixtureReport(t))
	if first.body["tester"] != false {
		t.Error("the run of a Mac signed in from outside the M team is a Verified run")
	}

	d.people.team[4242] = true
	if report := d.report(t, first); report.Verified || report.Handle != "MaralcBR" {
		t.Errorf("after the mark the stored report is %+v: the verdict is stamped once", report)
	}
	if next := d.post(t, indented(t, fixtureReport(t))); next.status != http.StatusConflict {
		t.Fatalf("the same report again: %d", next.status)
	}
	if status := d.golden(t, "status"); status.body["tester"] != true {
		t.Errorf("the status after the mark: %v", status.body)
	}
}

func indented(t testing.TB, report []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, report, "", "\t"); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestATokenAnotherAppIssuedOrAnUnknownOneBindsNothing(t *testing.T) {
	d := newSignInDesk(t)
	d.github.issued = map[string]account{}
	d.github.other[goldenToken] = account{1, "maralcbr"}

	a := d.signIn(t)
	if a.status != http.StatusUnauthorized || a.text("error") != "GitHub didn't confirm the sign-in (GitHub answered HTTP 404). Run omarchy-m-test --sign-in again." {
		t.Errorf("answered %d %v", a.status, a.body)
	}
	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}
	if len(d.people.known) != 0 {
		t.Errorf("the lab met %v", d.people.known)
	}
}

func TestASignInChangedAfterItWasSignedOrSignedAsAReportIsRefused(t *testing.T) {
	d := newSignInDesk(t)
	d.github.issued["gho_someoneElsesToken"] = account{66, "attacker"}
	forged := parse(t, goldenSignInText(t, "sign-in")).set("github_token", "gho_someoneElsesToken")
	a := d.signIn(t, forged)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(a.text("error"), "the report was changed after it was signed") {
		t.Errorf("a forged sign-in: %d %v", a.status, a.body)
	}

	asReport := sign(t, object("binding_version", integer(1), "github_token", goldenToken), "a")
	a = d.signIn(t, asReport)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(a.text("error"), "it isn't a sign-in signature (namespace)") {
		t.Errorf("a sign-in signed as a report: %d %v", a.status, a.body)
	}
	if !strings.HasPrefix(a.text("error"), "The sign-in's signature is not valid, so it was refused (") {
		t.Errorf("worded %q", a.text("error"))
	}

	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}
	if len(d.github.revoked) != 0 {
		t.Errorf("revoked %q", d.github.revoked)
	}
}

func TestAnythingButASignInIsRefused(t *testing.T) {
	d := newSignInDesk(t)
	golden := func() *Object { return parse(t, goldenSignInText(t, "sign-in")) }
	const malformed = "The sign-in isn't what omarchy-m-test sends. Update omarchy-m-test and sign in again."
	for name, c := range map[string]struct {
		body any
		says string
	}{
		"no signature":        {golden().without("signature"), malformed},
		"a member more":       {golden().set("extra", integer(1)), malformed},
		"another version":     {golden().set("binding_version", integer(2)), malformed},
		"a version in words":  {golden().set("binding_version", "1"), malformed},
		"a token with spaces": {golden().set("github_token", "gho two"), malformed},
		"a token too long":    {golden().set("github_token", strings.Repeat("a", 256)), malformed},
		"no token":            {golden().set("github_token", ""), malformed},
		"a token in a list":   {golden().set("github_token", []any{goldenToken}), malformed},
		"a signature of text": {golden().set("signature", "nope"), "The sign-in's signature is not valid, so it was refused (it isn't a signature object)."},
		"a list":              {[]any{integer(1)}, malformed},
		"a request":           {goldenSignInText(t, "status"), malformed},
	} {
		// Each from its own address: ten an hour is all one address has.
		a := d.ask(t, "/api/v1/tester_bindings", c.body, "10.0.1."+strconv.Itoa(len(name)))
		if a.status != http.StatusUnprocessableEntity || a.text("error") != c.says {
			t.Errorf("%s: %d %v", name, a.status, a.body)
		}
	}
	if a := d.signIn(t, "{"); a.status != http.StatusBadRequest || a.text("error") != "The sign-in is not valid JSON." {
		t.Errorf("not JSON: %d %v", a.status, a.body)
	}
	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}
	if len(d.github.revoked) != 0 {
		t.Errorf("GitHub was asked to revoke %q", d.github.revoked)
	}
}

func TestWithoutTheGitHubAppThereIsNoMacSignIn(t *testing.T) {
	d := newSignInDesk(t)
	d.signIns = NewMacSignIns(d.store, testRules(t), func() time.Time { return d.now }, nil, d.people)

	a := d.signIn(t)
	wantBody(t, a, http.StatusServiceUnavailable, map[string]any{"error": "Mac sign-in isn't set up on this lab yet."})
	// What a Mac asks under a sign-in needs no GitHub.
	wantBody(t, d.golden(t, "status"), http.StatusOK, map[string]any{"signed_in": false})
}

func TestSigningInAgainOnTheMachineRebindsItAndEarlierRunsKeepTheHandleTheyWereUploadedUnder(t *testing.T) {
	d := newSignInDesk(t)
	d.signIn(t)
	first := d.post(t, fixtureReport(t))
	d.github.issued[goldenToken] = account{7, "someone-else"}

	if a := d.signIn(t); a.status != http.StatusCreated || a.body["tester"] != false {
		t.Fatalf("the second sign-in: %d %v", a.status, a.body)
	}
	if got := d.signedIn(t); !reflect.DeepEqual(got, []string{"someone-else 7"}) {
		t.Errorf("sign-ins %q", got)
	}
	if report := d.report(t, first); report.Handle != "MaralcBR" || report.AccountID != 4242 || !report.Verified {
		t.Errorf("the earlier report is now %+v", report)
	}
}

// The old site pinned an allowlisted handle to the first account that signed
// in with it and refused another account under that handle. The lab's mark
// hangs on the account id from the start: the other account signs in as
// itself, and nothing of the member's is its.
func TestAnotherAccountUnderAMembersHandleIsNotThatMember(t *testing.T) {
	d := newSignInDesk(t)
	d.signIn(t)
	d.github.issued[goldenToken] = account{999, "MaralcBR"} // the handle, renamed away and registered by someone else

	a := d.signIn(t)
	if a.status != http.StatusCreated || a.body["tester"] != false {
		t.Fatalf("answered %d %v", a.status, a.body)
	}
	if got := d.signedIn(t); !reflect.DeepEqual(got, []string{"MaralcBR 999"}) {
		t.Errorf("sign-ins %q", got)
	}
	if report := d.report(t, d.post(t, fixtureReport(t))); report.Verified || report.AccountID != 999 {
		t.Errorf("stamped %+v", report)
	}
}

func TestAnOversizedSignInIsRefusedAlsoWhenSentChunked(t *testing.T) {
	d := newSignInDesk(t)
	large := append(goldenSignInText(t, "sign-in"), bytes.Repeat([]byte(" "), 9<<10)...)

	a := d.ask(t, "/api/v1/tester_bindings", large, "10.0.0.1")
	wantBody(t, a, http.StatusRequestEntityTooLarge, map[string]any{"error": "The sign-in is larger than 8 KiB."})

	chunked := httptest.NewRequest("POST", site+"/api/v1/tester_bindings", io.NopCloser(bytes.NewReader(large)))
	chunked.ContentLength = -1
	a = d.answered(t, chunked, "/api/v1/tester_bindings", "10.0.0.1")
	if a.status != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked: %d %v", a.status, a.body)
	}
	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}

	// Exactly 8 KiB is read.
	padded := append(goldenSignInText(t, "sign-in"), bytes.Repeat([]byte(" "), 8<<10)...)[:8<<10]
	if a := d.ask(t, "/api/v1/tester_bindings", padded, "10.0.0.2"); a.status != http.StatusCreated {
		t.Errorf("a sign-in of 8 KiB: %d %v", a.status, a.body)
	}
	a = d.request(t, append(goldenSignInText(t, "status"), bytes.Repeat([]byte(" "), 9<<10)...), time.Unix(goldenAt, 0))
	wantBody(t, a, http.StatusRequestEntityTooLarge, map[string]any{"error": "The request is larger than 8 KiB."})
}

func TestSignInsAreLimitedToTenAnHourPerAddress(t *testing.T) {
	d := newSignInDesk(t)
	for range SignInsPerHour {
		if a := d.signIn(t, "{"); a.status != http.StatusBadRequest {
			t.Fatalf("answered %d", a.status)
		}
	}
	a := d.signIn(t)
	wantBody(t, a, http.StatusTooManyRequests, map[string]any{"error": "Too many sign-ins from your network: at most 10 an hour. Try again later."})
	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}
	if a := d.ask(t, "/api/v1/tester_bindings", goldenSignInText(t, "sign-in"), "10.0.0.2"); a.status != http.StatusCreated {
		t.Errorf("another address is held by the first one's count: %d", a.status)
	}
	// What a Mac asks under its sign-in is counted apart.
	if a := d.golden(t, "status"); a.status != http.StatusOK {
		t.Errorf("a status after ten sign-ins: %d", a.status)
	}
	d.now = d.now.Add(time.Hour)
	if a := d.signIn(t); a.status != http.StatusCreated {
		t.Errorf("an hour later: %d %v", a.status, a.body)
	}
}

func TestStatusAndSignOutAreLimitedToSixtyAnHourPerAddress(t *testing.T) {
	d := newSignInDesk(t)
	for range SignInRequestsPerHour {
		if a := d.golden(t, "status"); a.status != http.StatusOK {
			t.Fatalf("answered %d", a.status)
		}
	}
	wantBody(t, d.golden(t, "status"), http.StatusTooManyRequests, map[string]any{"error": "Too many requests from your network: at most 60 an hour. Try again later."})
	wantBody(t, d.golden(t, "sign-out"), http.StatusTooManyRequests, map[string]any{"error": "Too many requests from your network: at most 60 an hour. Try again later."})
	if a := d.signIn(t); a.status != http.StatusCreated {
		t.Errorf("a sign-in after sixty requests: %d", a.status)
	}
}

// ---- the lab's own: Blocked ----

func TestABlockedPersonGetsTheBlockedSentenceAndTheirMacsReportsAreCommunityRuns(t *testing.T) {
	d := newSignInDesk(t)
	d.people.blocked[4242] = true

	a := d.signIn(t)
	wantBody(t, a, http.StatusForbidden, map[string]any{"error": "This GitHub account is blocked from Omarchy Mac Lab."})
	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}
	if !reflect.DeepEqual(d.github.revoked, []string{goldenToken}) {
		t.Errorf("revoked %q: the token is let go whoever it names", d.github.revoked)
	}
	if len(d.people.known) != 0 {
		t.Errorf("the lab changed what it keeps of a Blocked person: %v", d.people.known)
	}
	wantBody(t, d.golden(t, "status"), http.StatusOK, map[string]any{"signed_in": false})
	if report := d.report(t, d.post(t, fixtureReport(t))); report.Verified || report.Handle != "" {
		t.Errorf("a Blocked person's Mac uploaded %+v", report)
	}
}

// A Block that lands while the sign-in is on its way leaves no sign-in.
func TestABlockDuringASignInLeavesNoSignIn(t *testing.T) {
	d := newSignInDesk(t)
	d.people.blockAfter = 4242

	a := d.signIn(t)
	wantBody(t, a, http.StatusForbidden, map[string]any{"error": BlockedSentence})
	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}
}

// A sign-in of a Blocked member, should one be left, makes no Verified run.
func TestTheVerdictAsksWhoIsOnTheMTeamAtUpload(t *testing.T) {
	d := newSignInDesk(t)
	d.signIn(t)
	d.people.blocked[4242] = true
	if report := d.report(t, d.post(t, fixtureReport(t))); report.Verified {
		t.Error("a Blocked person's sign-in made a Verified run")
	}
}

// ---- tester_requests_test.rb ----

func TestStatusAMachineThatNeverSignedInIsNotSignedIn(t *testing.T) {
	d := newSignInDesk(t)
	wantBody(t, d.golden(t, "status"), http.StatusOK, map[string]any{"signed_in": false})
}

func TestStatusAfterTheGoldenSignInTheMachineIsSignedInAsItsHandleVerifiedWhileOnTheMTeam(t *testing.T) {
	d := newSignInDesk(t)
	d.signInBefore(t)
	wantBody(t, d.golden(t, "status"), http.StatusOK, map[string]any{"signed_in": true, "login": "MaralcBR", "tester": true, "sign_in": goldenSignIn})
	if named := at(parse(t, goldenSignInText(t, "sign-out")), "sign_in"); named != goldenSignIn {
		t.Fatalf("the golden sign-out names %v, not this sign-in", named)
	}

	d.people.team = map[int64]bool{}
	wantBody(t, d.golden(t, "status"), http.StatusOK, map[string]any{"signed_in": true, "login": "MaralcBR", "tester": false, "sign_in": goldenSignIn})
}

func TestSignOutUnbindsTheMachineItsLaterRunsAreCommunityRunsEarlierOnesKeepTheirHandle(t *testing.T) {
	d := newSignInDesk(t)
	d.signInBefore(t)
	first := d.post(t, fixtureReport(t))
	if report := d.report(t, first); report.Handle != "MaralcBR" || !report.Verified {
		t.Fatalf("stamped %+v", report)
	}

	wantBody(t, d.golden(t, "sign-out"), http.StatusOK, map[string]any{"signed_in": false, "signed_out": "MaralcBR"})
	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}
	wantBody(t, d.golden(t, "status"), http.StatusOK, map[string]any{"signed_in": false})

	// The lab refuses the same report twice, so the later run is another.
	awkward := read(t, filepath.Join(goldenDir, "signed", "m2-max-image2-awkward-kernel.json"))
	later := d.post(t, awkward)
	if later.body["tester"] != false {
		t.Error("a run after the sign-out is a Verified run")
	}
	if report := d.report(t, later); report.Handle != "" || report.Verified {
		t.Errorf("the later report is %+v", report)
	}
	if report := d.report(t, first); report.Handle != "MaralcBR" || !report.Verified {
		t.Errorf("the earlier report is now %+v", report)
	}
}

func TestSignOutOfAMachineThatIsNotBoundSaysSoAndChangesNothingElse(t *testing.T) {
	d := newSignInDesk(t)
	d.signInOn(t, "b", account{5, "someone"})
	wantBody(t, d.golden(t, "sign-out"), http.StatusOK, map[string]any{"signed_in": false, "signed_out": nil})
	if got := d.signedIn(t); len(got) != 1 {
		t.Errorf("sign-ins %q", got)
	}
}

func TestAMachineOnlyEverSignsItselfOut(t *testing.T) {
	d := newSignInDesk(t)
	d.now = time.Unix(goldenAt-300, 0)
	d.signInOn(t, "a", account{4242, "MaralcBR"})
	d.signInOn(t, "b", account{5, "someone"})
	d.now = time.Unix(goldenAt, 0)

	a := d.request(t, testRequest(t, "b", "sign-out", "sign_in", goldenSignIn), d.now)
	wantBody(t, a, http.StatusOK, map[string]any{"signed_in": false, "signed_out": "someone"})
	want := []string{testRules(t).MachineID(testKeyBlob("a"))}
	if got := d.store.list(t, `SELECT machine_id FROM mac_sign_ins`); !reflect.DeepEqual(got, want) {
		t.Errorf("signed in: %q, want %q", got, want)
	}
}

func TestARequestDatedMoreThanADayFromTheLabsClockIsRefused(t *testing.T) {
	d := newSignInDesk(t)
	d.signInBefore(t)
	a := d.request(t, goldenSignInText(t, "sign-out"), time.Unix(goldenAt, 0).Add(25*time.Hour))
	wantBody(t, a, http.StatusUnprocessableEntity, map[string]any{"error": "The request is dated more than a day from the lab's clock. Check this Mac's date and time, then try again."})
	if got := d.signedIn(t); len(got) != 1 {
		t.Errorf("sign-ins %q", got)
	}

	if a := d.request(t, goldenSignInText(t, "sign-out"), time.Unix(goldenAt, 0).Add(-25*time.Hour)); a.status != http.StatusUnprocessableEntity {
		t.Errorf("a request from a day ahead: %d", a.status)
	}
	if a := d.request(t, goldenSignInText(t, "sign-out"), time.Unix(goldenAt, 0).Add(23*time.Hour)); a.status != http.StatusOK {
		t.Errorf("a request 23 hours old: %d %v", a.status, a.body)
	}
	if got := d.signedIn(t); len(got) != 0 {
		t.Errorf("sign-ins %q", got)
	}
	huge, _ := Parse([]byte("123456789012345678901234567890"))
	if a := d.request(t, testRequest(t, "a", "status").set("requested_at", huge), d.now); a.status != http.StatusUnprocessableEntity {
		t.Errorf("a date past any clock: %d %v", a.status, a.body)
	}
}

func TestACopyOfASignOutReplayedAfterTheMachineSignedInAgainUnbindsNothing(t *testing.T) {
	d := newSignInDesk(t)
	d.signInBefore(t)
	d.golden(t, "sign-out")
	if got := d.signedIn(t); len(got) != 0 {
		t.Fatalf("sign-ins %q", got)
	}

	d.signInAt(t, time.Unix(goldenAt-60, 0)) // dated before the copied sign-out: still a later sign-in
	a := d.request(t, goldenSignInText(t, "sign-out"), time.Unix(goldenAt+180, 0))
	wantBody(t, a, http.StatusConflict, map[string]any{"error": "This Mac signed in again after this sign-out was made, so it was refused. Run omarchy-m-test --sign-out again."})
	if got := d.signedIn(t); !reflect.DeepEqual(got, []string{"MaralcBR 4242"}) {
		t.Errorf("sign-ins %q", got)
	}
}

func TestACopyOfASignOutAfterTheSameHandleSignedInAgainOnTheMachineUnbindsNothing(t *testing.T) {
	d := newSignInDesk(t)
	d.signInBefore(t)
	d.signInAt(t, time.Unix(goldenAt-60, 0)) // the same row, signed in again

	if a := d.golden(t, "sign-out"); a.status != http.StatusConflict {
		t.Errorf("answered %d %v", a.status, a.body)
	}
	if got := d.signedIn(t); len(got) != 1 {
		t.Errorf("sign-ins %q", got)
	}
}

// Two sign-ins within one tick of the clock are still two sign-ins.
func TestASignInAtTheSameInstantIsStillALaterSignIn(t *testing.T) {
	d := newSignInDesk(t)
	d.signInBefore(t)
	d.signInBefore(t)
	if a := d.golden(t, "sign-out"); a.status != http.StatusConflict {
		t.Errorf("a sign-out naming the first of two sign-ins made at one instant: %d %v", a.status, a.body)
	}
}

func TestASignInRacingASignOutIsNeverLost(t *testing.T) {
	d := newSignInDesk(t)
	d.signInBefore(t)
	machine := d.store.list(t, `SELECT machine_id FROM mac_sign_ins`)[0]
	looked, err := d.store.SignInOf(machine)
	if err != nil || looked == nil {
		t.Fatalf("the sign-in: %v %v", looked, err)
	}
	// The sign-in lands between the sign-out's lookup and its delete: the delete names the sign-in it looked up.
	d.signInAt(t, time.Unix(goldenAt-60, 0))
	if ended, err := d.store.endSignIn(*looked); err != nil || ended {
		t.Errorf("the sign-out ended the later sign-in: %v %v", ended, err)
	}
	if got := d.signedIn(t); len(got) != 1 {
		t.Errorf("sign-ins %q", got)
	}
}

func TestARequestChangedAfterItWasSignedOrSignedAsAReportIsRefused(t *testing.T) {
	d := newSignInDesk(t)
	d.signInBefore(t)
	changed := parse(t, goldenSignInText(t, "sign-out")).set("sign_in", "1")
	a := d.request(t, changed, time.Unix(goldenAt, 0))
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(a.text("error"), "changed after it was signed") {
		t.Errorf("a changed sign-out: %d %v", a.status, a.body)
	}
	if !strings.HasPrefix(a.text("error"), "The request's signature is not valid, so it was refused (") {
		t.Errorf("worded %q", a.text("error"))
	}

	asReport := sign(t, object("request_version", integer(1), "request", "sign-out", "requested_at", integer(goldenAt), "sign_in", goldenSignIn), "a")
	a = d.request(t, asReport, time.Unix(goldenAt, 0))
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(a.text("error"), "namespace") {
		t.Errorf("a sign-out signed as a report: %d %v", a.status, a.body)
	}
	if got := d.signedIn(t); len(got) != 1 {
		t.Errorf("sign-ins %q", got)
	}
}

func TestASignInCannotPassForARequestNorAnythingElse(t *testing.T) {
	d := newSignInDesk(t)
	status := func() *Object { return parse(t, goldenSignInText(t, "status")) }
	signOut := func() *Object { return parse(t, goldenSignInText(t, "sign-out")) }
	for name, body := range map[string]any{
		"a sign-in":                   goldenSignInText(t, "sign-in"),
		"no signature":                status().without("signature"),
		"a member more":               status().set("extra", integer(1)),
		"another request":             status().set("request", "delete-everything"),
		"a date in words":             status().set("requested_at", "now"),
		"a date with a fraction":      []byte(strings.Replace(string(goldenSignInText(t, "status")), "1790553600", "1790553600.5", 1)),
		"another version":             status().set("request_version", integer(2)),
		"a status naming a sign-in":   status().set("sign_in", "1"),
		"a sign-out naming none":      signOut().without("sign_in"),
		"a sign-out naming words":     signOut().set("sign_in", "x"),
		"a sign-out naming 21 digits": signOut().set("sign_in", strings.Repeat("1", 21)),
		"a sign-out naming a list":    signOut().set("sign_in", []any{"1"}),
		"a list":                      []any{integer(1)},
	} {
		a := d.request(t, body, time.Unix(goldenAt, 0))
		wantBody(t, a, http.StatusUnprocessableEntity, map[string]any{"error": "The request isn't what omarchy-m-test sends. Update omarchy-m-test and try again."})
		if t.Failed() {
			t.Fatalf("%s", name)
		}
	}
	a := d.request(t, "{", time.Unix(goldenAt, 0))
	wantBody(t, a, http.StatusBadRequest, map[string]any{"error": "The request is not valid JSON."})
}

// The old site read a sign-out's sign_in with to_s: a number of digits is
// what the tool sends too, and names no sign-in, since a sign-in's name is
// text.
func TestASignOutNamingItsSignInAsANumberEndsNothing(t *testing.T) {
	d := newSignInDesk(t)
	d.now = time.Unix(goldenAt-300, 0)
	d.signInOn(t, "a", account{4242, "MaralcBR"})
	d.now = time.Unix(goldenAt, 0)
	named, _ := Parse([]byte(goldenSignIn))
	if a := d.request(t, testRequest(t, "a", "sign-out", "sign_in", named), d.now); a.status != http.StatusConflict {
		t.Errorf("answered %d %v", a.status, a.body)
	}
	if a := d.request(t, testRequest(t, "a", "sign-out", "sign_in", goldenSignIn), d.now); a.status != http.StatusOK {
		t.Errorf("answered %d %v", a.status, a.body)
	}
}

// ---- tester_gating_test.rb: which run is a Verified run ----

func TestAMachinesLatestSignInDecidesEachRunAsItIsUploaded(t *testing.T) {
	d := newSignInDesk(t)
	d.now = time.Unix(goldenAt-300, 0)
	d.signInOn(t, "a", account{4242, "MaralcBR"})
	d.signInOn(t, "b", account{77, "not-on-the-team"})
	d.now = time.Unix(goldenAt, 0)
	verified := d.post(t, run(t, 1), "a")
	community := d.post(t, run(t, 1), "b")
	if !d.report(t, verified).Verified || d.report(t, community).Verified {
		t.Fatalf("stamped %+v and %+v", d.report(t, verified), d.report(t, community))
	}

	// A community run after its Verified run: the Mac signed out between them.
	if a := d.request(t, testRequest(t, "a", "sign-out", "sign_in", goldenSignIn), d.now); a.status != http.StatusOK {
		t.Fatalf("sign-out: %d %v", a.status, a.body)
	}
	if later := d.post(t, run(t, 2), "a"); d.report(t, later).Verified {
		t.Error("a run after the sign-out is a Verified run")
	}
	if !d.report(t, verified).Verified {
		t.Error("the earlier Verified run changed")
	}
}

// The old site took a removed tester's runs out of the matrix colours. The
// lab never recomputes: removing the mark later changes no stored report.
func TestRemovingTheMarkLaterChangesNoStoredReport(t *testing.T) {
	d := newSignInDesk(t)
	d.signIn(t)
	first := d.post(t, fixtureReport(t))
	before := d.store.list(t, `SELECT verified || ' ' || handle || ' ' || account_id || ' ' || updated_at FROM reports`)

	d.people.team = map[int64]bool{}
	if after := d.store.list(t, `SELECT verified || ' ' || handle || ' ' || account_id || ' ' || updated_at FROM reports`); !reflect.DeepEqual(after, before) || before[0][:1] != "1" {
		t.Errorf("the stored report went from %q to %q", before, after)
	}
	if report := d.report(t, first); !report.Verified || report.Handle != "MaralcBR" || report.AccountID != 4242 {
		t.Errorf("the report reads %+v", report)
	}
	awkward := read(t, filepath.Join(goldenDir, "signed", "m2-max-image2-awkward-kernel.json"))
	if later := d.post(t, awkward); later.body["tester"] != false || d.report(t, later).Verified {
		t.Error("a run uploaded after the mark was removed is a Verified run")
	}
	if got := d.signedIn(t); len(got) != 1 {
		t.Errorf("the sign-in went with the mark: %q", got)
	}
}

// ---- what the pages ask ----

func TestThePagesListAPersonsMacsAndEndASignInByItsRow(t *testing.T) {
	d := newSignInDesk(t)
	d.now = time.Unix(goldenAt-300, 0)
	d.signIn(t)
	d.signInOn(t, "b", account{4242, "MaralcBR"})
	d.signInOn(t, "c", account{5, "someone"})
	d.now = time.Unix(goldenAt, 0)
	d.post(t, fixtureReport(t))

	mine, err := d.store.SignInsOf(4242)
	if err != nil || len(mine) != 2 {
		t.Fatalf("the sign-ins of 4242: %v %v", mine, err)
	}
	if model, n, err := d.store.MacOf(mine[0].MachineID); err != nil || n != 1 || !strings.Contains(model, "MacBook Pro") {
		t.Errorf("the Mac of the first sign-in: %q, %d reports, %v", model, n, err)
	}
	if model, n, err := d.store.MacOf(mine[1].MachineID); err != nil || n != 0 || model != "" {
		t.Errorf("a Mac that sent no report: %q, %d reports, %v", model, n, err)
	}
	if all, err := d.store.SignIns(); err != nil || len(all) != 3 {
		t.Errorf("every sign-in: %v %v", all, err)
	}

	// Only the person's own row ends.
	if ended, err := d.store.EndSignInOf(5, mine[0].ID); err != nil || ended != nil {
		t.Errorf("a sign-out by its row under another account: %v %v", ended, err)
	}
	if ended, err := d.store.EndSignInOf(4242, mine[0].ID); err != nil || ended == nil || ended.MachineID != mine[0].MachineID {
		t.Errorf("a sign-out by its row: %v %v", ended, err)
	}
	wantBody(t, d.golden(t, "status"), http.StatusOK, map[string]any{"signed_in": false})
	if n, err := d.store.EndSignInsOf(4242); err != nil || n != 1 {
		t.Errorf("ending every sign-in of 4242: %d %v", n, err)
	}
	if got := d.signedIn(t); !reflect.DeepEqual(got, []string{"someone 5"}) {
		t.Errorf("sign-ins %q", got)
	}
}
