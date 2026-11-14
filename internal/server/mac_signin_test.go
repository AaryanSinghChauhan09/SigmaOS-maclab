package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/reports"
	"github.com/omacom/omarchy-maclab/internal/web"
)

// Mac sign-in through labd's own front: the tunnel's socket, the Lab
// address, the fake GitHub, People and the own page. What a sign-in or a
// request is refused for, rule by rule, is internal/reports' to test.

const (
	signInPath  = "/api/v1/tester_bindings"
	requestPath = "/api/v1/tester_requests"
)

// macLab is rolesLab with the machine-id key, so it takes reports.
func macLab(t *testing.T, config func(*Config)) *signInLab {
	t.Helper()
	return rolesLab(t, func(c *Config) {
		c.MachineIDKey = testMachineIDKey()
		if config != nil {
			config(c)
		}
	})
}

// A test Mac is an ed25519 Machine key made from its name.
func macKey(name string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("server test mac " + name))
	return ed25519.NewKeyFromSeed(seed[:])
}

func sshPacked(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = binary.BigEndian.AppendUint32(out, uint32(len(part)))
		out = append(out, part...)
	}
	return out
}

// macSigned is document, a JSON object, with the signature the Mac's key
// makes over it under namespace, as `ssh-keygen -Y sign` would.
func macSigned(t testing.TB, mac, namespace, document string) string {
	t.Helper()
	parsed, err := reports.Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	message, err := reports.Canonical(parsed)
	if err != nil {
		t.Fatal(err)
	}
	key := macKey(mac)
	keyBlob := sshPacked([]byte("ssh-ed25519"), key.Public().(ed25519.PublicKey))
	digest := sha512.Sum512(message)
	signedData := append([]byte("SSHSIG"), sshPacked([]byte(namespace), nil, []byte("sha512"), digest[:])...)
	blob := binary.BigEndian.AppendUint32([]byte("SSHSIG"), 1)
	blob = append(blob, sshPacked(keyBlob, []byte(namespace), nil, []byte("sha512"), sshPacked([]byte("ssh-ed25519"), ed25519.Sign(key, signedData)))...)
	armored := "-----BEGIN SSH SIGNATURE-----\n" + base64.StdEncoding.EncodeToString(blob) + "\n-----END SSH SIGNATURE-----"
	signature, _ := json.Marshal(map[string]string{"public_key": "ssh-ed25519 " + base64.StdEncoding.EncodeToString(keyBlob), "signature": armored})
	return string(message[:len(message)-1]) + `,"signature":` + string(signature) + "}"
}

type macAnswer struct {
	status int
	header http.Header
	raw    string
	body   map[string]any
}

// fromMac asks labd as a Mac behind the edge at address does.
func (l *signInLab) fromMac(target, body, address string) macAnswer {
	l.t.Helper()
	r := httptest.NewRequest("POST", "http://"+labHost+target, strings.NewReader(body))
	r = r.WithContext(onTunnel(r.Context()))
	r.RemoteAddr = "@"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("CF-Connecting-IP", address)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", "omarchy-m-test/0.2.0")
	w := httptest.NewRecorder()
	l.h.ServeHTTP(w, r)
	a := macAnswer{status: w.Code, header: w.Header(), raw: w.Body.String()}
	json.Unmarshal(w.Body.Bytes(), &a.body)
	return a
}

const macAddress = "198.51.100.20"

var macTokens int

// macSignIn runs `omarchy-m-test --sign-in` on the Mac for the account: the
// device flow's token, as the fake GitHub's app issued it, in a signed
// sign-in.
func (l *signInLab) macSignIn(mac string, account int64) macAnswer {
	l.t.Helper()
	macTokens++
	token := fmt.Sprintf("gho_%sToken%d", mac, macTokens)
	l.gh.Issue(token, account)
	return l.fromMac(signInPath, macSigned(l.t, mac, reports.NamespaceSignIn, `{"binding_version":1,"github_token":"`+token+`"}`), macAddress)
}

func (l *signInLab) mustMacSignIn(mac string, account int64) {
	l.t.Helper()
	if a := l.macSignIn(mac, account); a.status != http.StatusCreated {
		l.t.Fatalf("sign-in of %s as account %d: %d %s", mac, account, a.status, a.raw)
	}
}

func (l *signInLab) macRequest(mac, request string, more ...string) macAnswer {
	l.t.Helper()
	document := fmt.Sprintf(`{"request_version":1,"request":%q,"requested_at":%d`, request, l.clock.Now().Unix())
	for i := 0; i+1 < len(more); i += 2 {
		document += fmt.Sprintf(`,%q:%q`, more[i], more[i+1])
	}
	return l.fromMac(requestPath, macSigned(l.t, mac, reports.NamespaceSignIn, document+"}"), macAddress)
}

func (l *signInLab) macStatus(mac string) map[string]any {
	l.t.Helper()
	a := l.macRequest(mac, "status")
	if a.status != http.StatusOK {
		l.t.Fatalf("status of %s: %d %s", mac, a.status, a.raw)
	}
	return a.body
}

// macUpload sends the golden report called run from the Mac.
func (l *signInLab) macUpload(mac, run string) macAnswer {
	l.t.Helper()
	golden, err := os.ReadFile(filepath.Join("..", "..", "schema", "golden", run+".json"))
	if err != nil {
		l.t.Fatal(err)
	}
	a := l.fromMac("/api/v1/reports", macSigned(l.t, mac, reports.NamespaceReport, string(golden)), macAddress)
	if a.status != http.StatusCreated {
		l.t.Fatalf("upload of %s from %s: %d %s", run, mac, a.status, a.raw)
	}
	return a
}

func (l *signInLab) storedReport(a macAnswer) *reports.Report {
	l.t.Helper()
	report, err := l.s.reports.Report(a.body["id"].(string))
	if err != nil {
		l.t.Fatal(err)
	}
	return report
}

func (l *signInLab) macSignIns(account int64) []reports.SignIn {
	l.t.Helper()
	signIns, err := l.s.reports.SignInsOf(account)
	if err != nil {
		l.t.Fatal(err)
	}
	return signIns
}

func (b *browser) page(target string) string {
	b.l.t.Helper()
	w := b.do("GET", target, "")
	if w.Code != http.StatusOK {
		b.l.t.Fatalf("GET %s: %d", target, w.Code)
	}
	return w.Body.String()
}

func macPath(account, signIn int64) string {
	return personPath(account, "macs/"+strconv.FormatInt(signIn, 10)+"/sign-out")
}

// The wire names are the old site's: /api/v1/tester_bindings,
// /api/v1/tester_requests and the tester key. GitHub's token is used once
// and kept nowhere.
func TestMacSignInKeepsTheOldSitesWireNames(t *testing.T) {
	l := macLab(t, nil)
	token := "gho_wireNamesToken0123456789"
	l.gh.Issue(token, ownerID)

	a := l.fromMac(signInPath, macSigned(t, "a", reports.NamespaceSignIn, `{"binding_version":1,"github_token":"`+token+`"}`), macAddress)
	if a.status != http.StatusCreated || a.body["login"] != "owner" || a.body["tester"] != true {
		t.Fatalf("sign-in: %d %s", a.status, a.raw)
	}
	if got := a.header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type %q", got)
	}
	if got := l.gh.Revoked(); len(got) != 1 || got[0] != token {
		t.Errorf("revoked at GitHub: %q", got)
	}
	if bytes.Contains(l.onDisk(), []byte(token)) {
		t.Error("the lab kept GitHub's token")
	}

	status := l.macStatus("a")
	name, _ := status["sign_in"].(string)
	if status["signed_in"] != true || status["login"] != "owner" || status["tester"] != true || name != strconv.FormatInt(l.clock.Now().UnixMicro(), 10) {
		t.Errorf("status: %v", status)
	}
	if up := l.macUpload("a", "m2-max-image2"); up.body["tester"] != true {
		t.Errorf("the upload answered tester %v", up.body["tester"])
	}
	if up := l.macUpload("b", "m2-max-image2"); up.body["tester"] != false {
		t.Errorf("another Mac's upload answered tester %v", up.body["tester"])
	}

	out := l.macRequest("a", "sign-out", "sign_in", name)
	if out.status != http.StatusOK || out.body["signed_in"] != false || out.body["signed_out"] != "owner" {
		t.Errorf("sign-out: %d %s", out.status, out.raw)
	}
	if status := l.macStatus("a"); len(status) != 1 || status["signed_in"] != false {
		t.Errorf("status after the sign-out: %v", status)
	}
	if again := l.macRequest("a", "sign-out", "sign_in", name); again.status != http.StatusOK || again.raw != `{"signed_in":false,"signed_out":null}`+"\n" {
		t.Errorf("a sign-out of a Mac that is not signed in: %d %s", again.status, again.raw)
	}

	// A token GitHub does not know as this app's signs nobody in.
	l.gh.IssueElsewhere("gho_fromAnotherApp", ownerID)
	no := l.fromMac(signInPath, macSigned(t, "a", reports.NamespaceSignIn, `{"binding_version":1,"github_token":"gho_fromAnotherApp"}`), macAddress)
	if no.status != http.StatusUnauthorized || no.body["error"] != "GitHub didn't confirm the sign-in (GitHub answered HTTP 404). Run omarchy-m-test --sign-in again." {
		t.Errorf("a token of another app: %d %s", no.status, no.raw)
	}
	if n := len(l.macSignIns(ownerID)); n != 0 {
		t.Errorf("%d Macs signed in", n)
	}
}

// One rule: a run is a Verified run when its Mac's Machine key is signed in
// as an M team member at upload, whoever uploads it. The verdict, the
// account id and the handle are stamped then, and removing the mark later
// changes no stored report.
func TestARunIsVerifiedOnlyWhenItsMacIsSignedInAsAnMTeamMemberAtUpload(t *testing.T) {
	l := macLab(t, nil)
	ravi := l.signedIn(maintainerID)
	ravi.did("/people/mark", "handle", "edith", "as", "m-team")

	if a := l.macSignIn("a", edithID); a.status != http.StatusCreated || a.body["tester"] != true || a.body["login"] != "edith" {
		t.Fatalf("an M team member's sign-in: %d %s", a.status, a.raw)
	}
	l.mustMacSignIn("m", maintainerID)
	l.mustMacSignIn("o", ownerID)
	for mac, who := range map[string]string{"a": "edith", "m": "ravi", "o": "owner"} {
		up := l.macUpload(mac, "m2-max-image2")
		if report := l.storedReport(up); up.body["tester"] != true || !report.Verified || report.Handle != who {
			t.Errorf("the run of a Mac signed in as @%s: tester %v, stored %+v", who, up.body["tester"], report)
		}
	}
	// The report came from another network than the sign-in did: whoever uploads.
	golden, _ := os.ReadFile(filepath.Join("..", "..", "schema", "golden", "m1-pro-mx-mac.json"))
	if up := l.fromMac("/api/v1/reports", macSigned(t, "a", reports.NamespaceReport, string(golden)), "203.0.113.77"); up.status != http.StatusCreated || up.body["tester"] != true {
		t.Errorf("the Mac's report from another network: %d %s", up.status, up.raw)
	}

	verified := l.macUpload("a", "m2-max-converged")
	before := l.storedReport(verified)
	page := l.browser().page(pathOf(t, verified.body["report_url"].(string)))
	if !strings.Contains(page, `<span class="badge badge-tester"`) || !strings.Contains(page, ">Verified run</span>") {
		t.Errorf("a Verified run's page carries no badge saying so")
	}
	if strings.Contains(strings.ToLower(page), "edith") {
		t.Error("a report's page names who its Mac was signed in as")
	}

	ravi.did(personPath(edithID, "m-team/remove"), "sure", "yes")
	after := l.storedReport(verified)
	if !after.Verified || after.Handle != "edith" || after.AccountID != edithID || !bytes.Equal(after.Body, before.Body) {
		t.Errorf("removing the mark changed the stored report to %+v", after)
	}
	if page := l.browser().page(pathOf(t, verified.body["report_url"].(string))); !strings.Contains(page, ">Verified run</span>") {
		t.Error("the earlier run's page stopped saying Verified run")
	}
	later := l.macUpload("a", "m1-pro-converged")
	if report := l.storedReport(later); later.body["tester"] != false || report.Verified || report.Handle != "edith" || report.AccountID != edithID {
		t.Errorf("a run after the mark was removed: tester %v, stored %+v", later.body["tester"], report)
	}
	if page := l.browser().page(pathOf(t, later.body["report_url"].(string))); !strings.Contains(page, `<span class="badge badge-community"`) || !strings.Contains(page, ">Community run</span>") {
		t.Error("a Community run's page carries no badge saying so")
	}
	if status := l.macStatus("a"); status["signed_in"] != true || status["tester"] != false {
		t.Errorf("the sign-in after the mark was removed: %v", status)
	}
}

// A sign-in by someone outside the M team stays, its runs are Community
// runs, and People shows it as "not counted".
func TestASignInFromOutsideTheMTeamStaysAndPeopleShowsItNotCounted(t *testing.T) {
	l := macLab(t, nil)
	ravi := l.signedIn(maintainerID)
	ravi.did("/people/mark", "handle", "edith", "as", "m-team")

	a := l.macSignIn("g", graceID)
	if a.status != http.StatusCreated || a.body["tester"] != false {
		t.Fatalf("sign-in: %d %s", a.status, a.raw)
	}
	if want := "Signed in as @grace. This Mac's runs are published as Community runs; Verified runs come from Macs of the Omarchy M team."; a.body["message"] != want {
		t.Errorf("message %q", a.body["message"])
	}
	l.mustMacSignIn("e1", edithID)
	l.mustMacSignIn("e2", edithID)
	up := l.macUpload("g", "m2-max-image2")
	if report := l.storedReport(up); up.body["tester"] != false || report.Verified || report.Handle != "grace" || report.AccountID != graceID {
		t.Errorf("the run: tester %v, stored %+v", up.body["tester"], report)
	}
	if n := len(l.macSignIns(graceID)); n != 1 {
		t.Errorf("%d Macs signed in as grace", n)
	}

	people := ravi.page("/people")
	cell := func(id int) string {
		row := part(t, people, `id="person-`+strconv.Itoa(id)+`"`, `</tr>`)
		_, inside, _ := strings.Cut(part(t, row, `data-label="Signed in for Verified runs">`, `</td>`), ">")
		return strings.Join(strings.Fields(reTag.ReplaceAllString(inside, " ")), " ")
	}
	if got := cell(graceID); got != "1 not counted" {
		t.Errorf("grace's cell reads %q", got)
	}
	if got := cell(edithID); got != "2" {
		t.Errorf("edith's cell reads %q", got)
	}
	if got := cell(maintainerID); got != "0" {
		t.Errorf("the cell of someone with no Mac signed in reads %q", got)
	}
	open := ravi.page("/people?open=" + strconv.Itoa(graceID))
	block := part(t, open, "<h3>Signed in for Verified runs</h3>", "</div>")
	for _, want := range []string{"MacBook Pro", "1 report", "not counted", "Sign out"} {
		if !strings.Contains(block, want) {
			t.Errorf("the opened row lacks %q:\n%s", want, block)
		}
	}
}

// A Blocked person gets the Blocked sentence; their Macs are signed out by
// the Block and their reports upload as Community runs.
func TestABlockEndsMacSignInsAndABlockedPersonGetsTheSentence(t *testing.T) {
	l := macLab(t, nil)
	ravi := l.signedIn(maintainerID)
	ravi.did("/people/mark", "handle", "edith", "as", "m-team")
	l.mustMacSignIn("a", edithID)
	l.mustMacSignIn("b", edithID)
	l.mustMacSignIn("g", graceID)
	if up := l.macUpload("a", "m2-max-image2"); up.body["tester"] != true {
		t.Fatalf("before the Block: %s", up.raw)
	}

	open := ravi.page("/people?open=" + strconv.Itoa(edithID))
	if !strings.Contains(open, "2 Macs signed out") {
		t.Errorf("the Block's sentence does not count the Mac sign-ins it ends:\n%s", part(t, open, "<h3>Block</h3>", "</div>"))
	}
	ravi.did(personPath(edithID, "block"), "sure", "yes")
	if n := len(l.macSignIns(edithID)); n != 0 {
		t.Errorf("%d Macs still signed in as a Blocked person", n)
	}
	if n := len(l.macSignIns(graceID)); n != 1 {
		t.Errorf("the Block ended someone else's sign-in: %d left", n)
	}
	if status := l.macStatus("a"); len(status) != 1 || status["signed_in"] != false {
		t.Errorf("the status of a Blocked person's Mac: %v", status)
	}

	revoked := len(l.gh.Revoked())
	a := l.macSignIn("a", edithID)
	if a.status != http.StatusForbidden || a.raw != `{"error":"This GitHub account is blocked from Omarchy Mac Lab."}`+"\n" || a.body["error"] != blockedSentence {
		t.Errorf("a Blocked person's sign-in: %d %s", a.status, a.raw)
	}
	if len(l.gh.Revoked()) != revoked+1 {
		t.Error("the Blocked person's token was not let go")
	}
	if n := len(l.macSignIns(edithID)); n != 0 {
		t.Errorf("%d Macs signed in", n)
	}
	up := l.macUpload("a", "m2-max-converged")
	if report := l.storedReport(up); up.body["tester"] != false || report.Verified || report.Handle != "" || report.AccountID != 0 {
		t.Errorf("a Blocked person's Mac uploaded: tester %v, stored %+v", up.body["tester"], report)
	}
	earlier, err := l.s.reports.SignIns()
	if err != nil || len(earlier) != 1 {
		t.Errorf("sign-ins left: %v %v", earlier, err)
	}

	// Unblock gives back sign-in and nothing else: no Mac comes back.
	ravi.did(personPath(edithID, "unblock"))
	if n := len(l.macSignIns(edithID)); n != 0 {
		t.Errorf("Unblock gave back %d Mac sign-ins", n)
	}
	if a := l.macSignIn("a", edithID); a.status != http.StatusCreated || a.body["tester"] != false {
		t.Errorf("a sign-in after Unblock: %d %s", a.status, a.raw)
	}
}

// A first sign-in makes the lab know the person, and no more: no browser
// session, no Lab token, no leave to lend.
func TestAFirstMacSignInMakesTheLabKnowThePersonAndGivesNothing(t *testing.T) {
	l := macLab(t, nil)
	if l.known(linusID) {
		t.Fatal("the lab knows linus before any sign-in")
	}
	a := l.macSignIn("a", linusID)
	if a.status != http.StatusCreated {
		t.Fatalf("sign-in: %d %s", a.status, a.raw)
	}
	if !l.known(linusID) {
		t.Fatal("the lab does not know linus after his Mac signed in")
	}
	if p := l.who(linusID); p.Handle != "linus" || p.Role() != web.NoRole || p.MayLend() || p.OnMTeam() || p.Maintainer || p.Lend {
		t.Errorf("the first Mac sign-in made %+v", p)
	}
	if n := l.count(`SELECT count(*) FROM browser_sessions WHERE account_id = ?`, linusID); n != 0 {
		t.Errorf("%d browser sessions", n)
	}
	if n := l.count(`SELECT count(*) FROM lab_tokens WHERE account_id = ?`, linusID); n != 0 {
		t.Errorf("%d Lab tokens", n)
	}
	if cookies := a.header.Values("Set-Cookie"); len(cookies) != 0 {
		t.Errorf("the sign-in set %q", cookies)
	}
	for key := range a.body {
		if key != "login" && key != "tester" && key != "message" {
			t.Errorf("the sign-in answered %q", key)
		}
	}

	// A rename on GitHub follows at the next sign-in: one person, one row.
	l.gh.AddUser(linusID, "linus-t")
	l.mustMacSignIn("b", linusID)
	if p := l.who(linusID); p.Handle != "linus-t" || l.count(`SELECT count(*) FROM people WHERE handle LIKE 'linus%'`) != 1 {
		t.Errorf("after a rename the lab keeps %+v", p)
	}
	// A mark the person had is kept by a Mac sign-in.
	ravi := l.signedIn(maintainerID)
	ravi.did("/people/mark", "handle", "grace", "as", "invite")
	l.mustMacSignIn("g", graceID)
	if p := l.who(graceID); !p.Lend {
		t.Errorf("a Mac sign-in took an Invite away: %+v", p)
	}
}

// Sign out from the own page and by a Maintainer both end a Mac sign-in.
func TestThePersonAndAMaintainerEachEndAMacSignIn(t *testing.T) {
	l := macLab(t, nil)
	ravi := l.signedIn(maintainerID)
	ravi.did("/people/mark", "handle", "edith", "as", "m-team")
	l.mustMacSignIn("a", edithID)
	l.macUpload("a", "m2-max-image2")
	l.clock.Advance(time.Minute)
	l.mustMacSignIn("b", edithID)
	l.mustMacSignIn("g", graceID)
	edith, grace := l.signedIn(edithID), l.signedIn(graceID)
	macs := l.macSignIns(edithID)
	if len(macs) != 2 {
		t.Fatalf("%d Macs signed in as edith", len(macs))
	}

	own := part(t, edith.page("/me"), "macs signed in as you", "</section>")
	for _, want := range []string{"MacBook Pro", "1 report", "no report yet", "2026-11-14 12:00 UTC", "2026-11-14 12:01 UTC", "Verified runs", "Sign out"} {
		if !strings.Contains(own, want) {
			t.Errorf("the own page lacks %q:\n%s", want, own)
		}
	}
	if strings.Contains(own, macs[0].MachineID) || strings.Contains(own, "key:") {
		t.Error("the own page shows a machine id")
	}
	if theirs := part(t, grace.page("/me"), "macs signed in as you", "</section>"); !strings.Contains(theirs, "Community runs") || strings.Count(theirs, "<tr>") != 2 {
		t.Errorf("the own page of someone outside the M team:\n%s", theirs)
	}
	if none := part(t, ravi.page("/me"), "macs signed in as you", "</section>"); !strings.Contains(none, "No Mac is signed in as you.") {
		t.Errorf("the own page of someone with no Mac:\n%s", none)
	}

	// The first click asks; nothing ends until the second.
	mine := "/me/macs/" + strconv.FormatInt(macs[0].ID, 10) + "/sign-out"
	if w := edith.post(mine); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Sign out: this Mac&#39;s runs are Community runs from now on") {
		t.Errorf("the first click: %d", w.Code)
	}
	if len(l.macSignIns(edithID)) != 2 {
		t.Fatal("a Mac was signed out at the first click")
	}
	// Nobody ends another person's sign-in from their own page.
	if w := grace.post(mine, "sure", "yes"); w.Code != http.StatusNotFound || len(l.macSignIns(edithID)) != 2 {
		t.Errorf("grace signing edith's Mac out from her own page: %d", w.Code)
	}
	if w := l.browser().do("POST", mine, "sure=yes", "Content-Type", "application/x-www-form-urlencoded"); w.Code == http.StatusSeeOther && len(l.macSignIns(edithID)) != 2 {
		t.Error("a signed-out browser signed a Mac out")
	}
	edith.did(mine, "sure", "yes")
	if left := l.macSignIns(edithID); len(left) != 1 || left[0].ID != macs[1].ID {
		t.Fatalf("after the own page's Sign out: %+v", left)
	}
	if status := l.macStatus("a"); status["signed_in"] != false {
		t.Errorf("the Mac's status after the own page's Sign out: %v", status)
	}
	if up := l.macUpload("a", "m2-max-converged"); up.body["tester"] != false {
		t.Error("a run after the own page's Sign out is a Verified run")
	}

	// A Maintainer, from People. Anyone else gets the page that is not there.
	theirs := macPath(edithID, macs[1].ID)
	if w := grace.post(theirs, "sure", "yes"); w.Code != http.StatusNotFound || len(l.macSignIns(edithID)) != 1 {
		t.Errorf("someone who is no Maintainer signing a Mac out from People: %d", w.Code)
	}
	if w := ravi.post(macPath(graceID, macs[1].ID), "sure", "yes"); w.Code != http.StatusNotFound || len(l.macSignIns(edithID)) != 1 {
		t.Errorf("a sign-in named under the wrong person: %d", w.Code)
	}
	if w := ravi.post(theirs); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Sign this Mac of @edith out: its runs are Community runs from now on") {
		t.Errorf("the Maintainer's first click: %d", w.Code)
	}
	ravi.did(theirs, "sure", "yes")
	if left := l.macSignIns(edithID); len(left) != 0 {
		t.Fatalf("after the Maintainer's Sign out: %+v", left)
	}
	if status := l.macStatus("b"); status["signed_in"] != false {
		t.Errorf("the Mac's status after the Maintainer's Sign out: %v", status)
	}
	if n := len(l.macSignIns(graceID)); n != 1 {
		t.Errorf("grace's Mac went too: %d left", n)
	}

	lines := strings.Join(l.recorded(), "\n")
	for _, want := range []string{"@edith signed a Mac out", "@ravi signed a Mac out of @edith"} {
		if !strings.Contains(lines, want) {
			t.Errorf("the record lacks %q:\n%s", want, lines)
		}
	}
}

// "End all access" and "Sign out everywhere" leave Mac sign-ins alone.
func TestEndAllAccessLeavesMacSignInsUntouched(t *testing.T) {
	l := macLab(t, nil)
	l.mustMacSignIn("a", graceID)
	grace := l.signedIn(graceID)
	grace.did("/me/sign-out-everywhere", "sure", "yes")
	grace = l.signedIn(graceID)
	grace.did("/me/end-all-access", "sure", "yes")
	if n := len(l.macSignIns(graceID)); n != 1 {
		t.Errorf("%d Macs signed in after the two cuts", n)
	}
	if status := l.macStatus("a"); status["signed_in"] != true {
		t.Errorf("status %v", status)
	}
}

// The old site's limits, on the address the edge names: 10 sign-ins an hour
// and 60 requests.
func TestMacSignInIsLimitedOnTheCallersAddress(t *testing.T) {
	l := macLab(t, nil)
	for i := range reports.SignInsPerHour {
		if a := l.fromMac(signInPath, "{", macAddress); a.status != http.StatusBadRequest {
			t.Fatalf("sign-in %d: %d %s", i+1, a.status, a.raw)
		}
	}
	a := l.macSignIn("a", ownerID)
	if a.status != http.StatusTooManyRequests || a.body["error"] != "Too many sign-ins from your network: at most 10 an hour. Try again later." {
		t.Errorf("the eleventh sign-in: %d %s", a.status, a.raw)
	}
	token := "gho_fromElsewhere0123"
	l.gh.Issue(token, ownerID)
	signIn := macSigned(t, "a", reports.NamespaceSignIn, `{"binding_version":1,"github_token":"`+token+`"}`)
	if a := l.fromMac(signInPath, signIn, "198.51.100.21"); a.status != http.StatusCreated {
		t.Errorf("a sign-in from another address: %d %s", a.status, a.raw)
	}

	for i := range reports.SignInRequestsPerHour {
		if a := l.macRequest("a", "status"); a.status != http.StatusOK {
			t.Fatalf("request %d: %d %s", i+1, a.status, a.raw)
		}
	}
	if a := l.macRequest("a", "status"); a.status != http.StatusTooManyRequests || a.body["error"] != "Too many requests from your network: at most 60 an hour. Try again later." {
		t.Errorf("the sixty-first request: %d %s", a.status, a.raw)
	}
	l.clock.Advance(time.Hour + time.Second)
	if a := l.macRequest("a", "status"); a.status != http.StatusOK {
		t.Errorf("an hour later: %d %s", a.status, a.raw)
	}
	if a := l.macSignIn("b", ownerID); a.status != http.StatusCreated {
		t.Errorf("a sign-in an hour later: %d %s", a.status, a.raw)
	}
}

// With Preview on, Mac sign-in is refused with the line pointing at the old
// site, and nothing is counted or kept.
func TestInPreviewMacSignInIsRefusedPointingAtTheOldSite(t *testing.T) {
	l := macLab(t, func(c *Config) { c.Preview = true })
	const line = "Omarchy Mac Lab is not open yet and signs no Mac in. Until it opens, omarchy-m-testing.org is the live site: sign in there."
	for i := range reports.SignInsPerHour + 5 {
		a := l.macSignIn("a", ownerID)
		if a.status != http.StatusForbidden || a.body["error"] != line {
			t.Fatalf("sign-in %d in Preview: %d %s", i+1, a.status, a.raw)
		}
	}
	for _, request := range []string{"status", "sign-out"} {
		if a := l.macRequest("a", request, "sign_in", "1"); a.status != http.StatusForbidden || a.body["error"] != line {
			t.Errorf("%s in Preview: %d %s", request, a.status, a.raw)
		}
	}
	if n := len(l.macSignIns(ownerID)); n != 0 {
		t.Errorf("%d Macs signed in while in Preview", n)
	}
	if got := l.gh.Revoked(); len(got) != 0 {
		t.Errorf("GitHub was asked about %d tokens while in Preview", len(got))
	}
}

// A lab with no OAuth app, or with no machine-id key, signs no Mac in, and
// says so as the lab: a 5xx of labd's own carries "error", so the tool does
// not take the lab for Dark.
func TestALabThatCannotSignAMacInSaysSoItself(t *testing.T) {
	noApp := newReportsLab(t, nil)
	w := noApp.do("POST", signInPath, macSigned(t, "a", reports.NamespaceSignIn, `{"binding_version":1,"github_token":"gho_x"}`), "Content-Type", "application/json")
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != `{"error":"Mac sign-in isn't set up on this lab yet."}`+"\n" {
		t.Errorf("no OAuth app: %d %s", w.Code, w.Body)
	}
	status := macSigned(t, "a", reports.NamespaceSignIn, fmt.Sprintf(`{"request_version":1,"request":"status","requested_at":%d}`, time.Now().Unix()))
	if w := noApp.do("POST", requestPath, status, "Content-Type", "application/json"); w.Code != http.StatusOK || w.Body.String() != `{"signed_in":false}`+"\n" {
		t.Errorf("status without the OAuth app: %d %s", w.Code, w.Body)
	}

	noKey := rolesLab(t, nil)
	for _, target := range []string{signInPath, requestPath} {
		a := noKey.fromMac(target, "{}", macAddress)
		if a.status != http.StatusServiceUnavailable || a.body["error"] != "This lab signs no Mac in yet: it was given no machine-id key." {
			t.Errorf("%s with no machine-id key: %d %s", target, a.status, a.raw)
		}
	}

	// GitHub out of reach: nothing changed, and the lab's own words.
	l := macLab(t, nil)
	l.gh.Break(http.StatusBadGateway)
	a := l.macSignIn("a", ownerID)
	if a.status != http.StatusUnauthorized || a.body["error"] != "GitHub didn't confirm the sign-in (GitHub answered HTTP 502). Run omarchy-m-test --sign-in again." {
		t.Errorf("GitHub down: %d %s", a.status, a.raw)
	}
	l.gh.Break(0)
	if n := len(l.macSignIns(ownerID)); n != 0 {
		t.Errorf("%d Macs signed in", n)
	}
}

// The two addresses are the Mac's alone: at the Alias they are refused as
// every API address is, and a browser gets no page from them.
func TestMacSignInAtTheAliasNamesTheLabAddress(t *testing.T) {
	l := macLab(t, func(c *Config) { c.Alias = aliasHost })
	r := httptest.NewRequest("POST", "http://"+aliasHost+signInPath, strings.NewReader("{}"))
	r = r.WithContext(onTunnel(r.Context()))
	r.RemoteAddr = "@"
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	l.h.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest || !strings.Contains(w.Body.String(), "use that address") {
		t.Errorf("at the Alias: %d %s", w.Code, w.Body)
	}
	if w := l.browser().do("GET", signInPath, ""); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s: %d", signInPath, w.Code)
	}
}
