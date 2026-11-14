package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	limiterTrouble = "The refused-credential limiter is refusing an address: over 10 refused credentials came from it in 10 minutes, and further bad ones get 429 for 15 minutes. The caller log names it."
	limiterQuiet   = "The refused-credential limiter refuses nobody now."
)

// limitedLab is a labd behind the tunnel, on a clock the test moves.
type limitedLab struct {
	*opsLab
	h http.Handler
}

func newLimitedLab(t *testing.T) *limitedLab {
	t.Helper()
	o := newOps(t, nil)
	seeded(t, o.s, "ada", "lab_adm_secret")
	return &limitedLab{opsLab: o, h: o.s.Handler()}
}

// ask is one request through the tunnel from addr, carrying bearer if it is
// not "".
func (l *limitedLab) ask(addr, method, target, bearer, body string) *httptest.ResponseRecorder {
	l.t.Helper()
	r := httptest.NewRequest(method, "http://"+labHost+target, strings.NewReader(body))
	r = r.WithContext(onTunnel(r.Context()))
	r.RemoteAddr = viaProxy
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("CF-Connecting-IP", addr)
	r.Header.Set("User-Agent", "lab/0.2.0")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	l.h.ServeHTTP(w, r)
	return w
}

// bad sends one Lab token labd does not know from addr.
func (l *limitedLab) bad(addr string) int {
	l.t.Helper()
	return l.ask(addr, "GET", "/api/devices", "lab_00000000000000000000000000000000", "").Code
}

// refuse sends n bad Lab tokens from addr, each of which must get 401.
func (l *limitedLab) refuse(addr string, n int) {
	l.t.Helper()
	for i := 1; i <= n; i++ {
		if code := l.bad(addr); code != http.StatusUnauthorized {
			l.t.Fatalf("bad credential %d from %s: %d, want 401", i, addr, code)
		}
	}
}

// later moves the clock on and has labd look at the limiter, as it does
// every second.
func (l *limitedLab) later(d time.Duration) {
	l.t.Helper()
	l.clock.Advance(d)
	l.s.refusedLook()
}

const house, elsewhere = "203.0.113.20", "198.51.100.7"

func TestTheEleventhRefusedCredentialFromOneAddressGets429(t *testing.T) {
	l := newLimitedLab(t)
	l.refuse(house, 10)
	w := l.ask(house, "GET", "/api/devices", "lab_00000000000000000000000000000000", "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("the eleventh bad credential: %d %s, want 429", w.Code, w.Body)
	}
	if got, want := strings.TrimSpace(w.Body.String()), "Too many refused credentials from your network: over 10 in 10 minutes. A wrong one is refused like this for 15 minutes; a valid one still works."; got != want {
		t.Errorf("the 429 says\n%s\nwant\n%s", got, want)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("the 429 says Cache-Control %q", cc)
	}
	if code := l.bad(house); code != http.StatusTooManyRequests {
		t.Errorf("the twelfth: %d, want 429", code)
	}
	// A valid credential always passes.
	if w := l.ask(house, "GET", "/api/devices", "lab_adm_secret", ""); w.Code != http.StatusOK {
		t.Errorf("a valid Lab token from the refused address: %d %s", w.Code, w.Body)
	}
	// Another address is not this one.
	l.refuse(elsewhere, 1)
}

func TestTheLimiterCountsTenMinutesAndRefusesForFifteen(t *testing.T) {
	l := newLimitedLab(t)
	// Ten in ten minutes is not past ten: the count starts again.
	l.refuse(house, 10)
	l.later(10 * time.Minute)
	l.refuse(house, 10)
	l.later(10 * time.Minute)

	l.refuse(house, 10)
	l.later(9 * time.Minute)
	if code := l.bad(house); code != http.StatusTooManyRequests {
		t.Fatalf("the eleventh in ten minutes: %d, want 429", code)
	}
	l.later(15*time.Minute - time.Second)
	if code := l.bad(house); code != http.StatusTooManyRequests {
		t.Fatalf("a bad one a second before the 15 minutes are over: %d, want 429", code)
	}
	l.later(time.Second)
	l.refuse(house, 10)
}

func TestTheLimiterTellsAgentsOnceAndWhenItRefusesNobody(t *testing.T) {
	l := newLimitedLab(t)
	l.refuse(house, 10)
	l.wantTold()
	l.bad(house)
	l.bad(house)
	l.wantTold(limiterTrouble)

	// A second address while the first is refused is no second line.
	l.later(5 * time.Minute)
	l.refuse(elsewhere, 10)
	l.bad(elsewhere)
	l.wantTold(limiterTrouble)

	// The first address is free again; the second is still refused.
	l.later(10 * time.Minute)
	l.wantTold(limiterTrouble)
	l.later(5 * time.Minute)
	l.wantTold(limiterTrouble, limiterQuiet)
	l.later(time.Hour)
	l.wantTold(limiterTrouble, limiterQuiet)
	for _, line := range l.told() {
		if strings.Contains(line, house) || strings.Contains(line, elsewhere) {
			t.Errorf("a line for \"Agents\" names an address: %s", line)
		}
	}
}

// A labd that starts refuses nobody: what it counted went with the labd
// before it.
func TestALabdThatStartsEndsTheLimitersTrouble(t *testing.T) {
	l := newLimitedLab(t)
	l.refuse(house, 10)
	l.bad(house)
	l.wantTold(limiterTrouble)
	l.stop()
	again := newOpsAt(t, l.data, l.clock, healthy(), nil)
	found := false
	for _, line := range again.told() {
		found = found || line == limiterQuiet
	}
	if !found {
		t.Errorf("the labd that started did not say the limiter refuses nobody:\n%s", strings.Join(again.told(), "\n"))
	}
}

// Every kind of credential counts in the one count: a Lab token, a Mac's
// device secret, a builder's token, an enrollment token on either of its
// routes.
func TestRefusedEnrollmentTokensCountWithTheRest(t *testing.T) {
	l := newLimitedLab(t)
	enroll := `{"token":"enr_00000000000000000000000000000000"}`
	asks := []struct {
		method, target, bearer, body string
		want                         int
	}{
		{"GET", "/api/devices", "lab_00000000000000000000000000000000", "", 401},
		{"POST", "/api/agent/checkin", "0000000000000000000000000000000000000000", "{}", 401},
		{"POST", "/api/builder/poll", "nobody", "{}", 401},
		{"GET", "/api/join/terms", "enr_00000000000000000000000000000000", "", 401},
		{"POST", "/api/agent/enroll", "", enroll, 403},
	}
	for i := 0; i < 10; i++ {
		a := asks[i%len(asks)]
		if w := l.ask(house, a.method, a.target, a.bearer, a.body); w.Code != a.want {
			t.Fatalf("refusal %d, %s %s: %d %s, want %d", i+1, a.method, a.target, w.Code, w.Body, a.want)
		}
	}
	for _, a := range asks {
		if w := l.ask(house, a.method, a.target, a.bearer, a.body); w.Code != http.StatusTooManyRequests {
			t.Errorf("past the limit, %s %s: %d %s, want 429", a.method, a.target, w.Code, w.Body)
		}
	}
	l.wantTold(limiterTrouble)
}

// Only a credential that was sent and refused counts: asking with none, or
// with a browser's cookie labd no longer knows, is not a guess at one.
func TestARequestWithNoCredentialIsNotCounted(t *testing.T) {
	l := newLimitedLab(t)
	for i := 0; i < 30; i++ {
		if w := l.ask(house, "GET", "/api/devices", "", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("no credential, request %d: %d", i+1, w.Code)
		}
		r := httptest.NewRequest("GET", "http://"+labHost+"/api/devices", nil)
		r = r.WithContext(onTunnel(r.Context()))
		r.RemoteAddr = viaProxy
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("CF-Connecting-IP", house)
		r.AddCookie(&http.Cookie{Name: sessionCookie(true), Value: "0000"})
		w := httptest.NewRecorder()
		l.h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("a cookie labd does not know, request %d: %d", i+1, w.Code)
		}
	}
	l.wantTold()
}

// Callers on the mini itself, at the loopback port, are not an address the
// limiter counts.
func TestCallersOnTheMiniAreNotCounted(t *testing.T) {
	l := newLimitedLab(t)
	for i := 0; i < 30; i++ {
		if w := edgeAt(l.h, "GET", loopback, "/api/devices", direct, map[string]string{"Authorization": "Bearer lab_00000000000000000000000000000000", "User-Agent": "lab/0.2.0"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("bad credential %d at the loopback port: %d", i+1, w.Code)
		}
	}
	l.wantTold()
}

func TestRefusedSignInsCountToo(t *testing.T) {
	l := newSignInLab(t, nil)
	b := l.browser()
	for i := 1; i <= 10; i++ {
		if w := b.do("GET", "/auth/github/callback?state=nothing&code=nothing", ""); w.Code != http.StatusBadRequest {
			t.Fatalf("stale sign-in %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := b.do("GET", "/auth/github/callback?state=nothing&code=nothing", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the eleventh stale sign-in: %d %s, want 429", w.Code, w.Body)
	}
	// A sign-in that is good still passes.
	l.browser().mustSignIn(ownerID)
}

// Refused credentials are logged: each one's line in the caller log says
// so, with the address, and never the credential.
func TestRefusedCredentialsAreLogged(t *testing.T) {
	l := newLimitedLab(t)
	l.ask(house, "GET", "/api/devices", "lab_adm_secret", "")
	l.ask(house, "GET", "/api/devices", "", "")
	l.refuse(house, 10)
	l.bad(house)
	var refused, limited int
	for _, line := range l.s.callers.linesForTest(t) {
		if !line.Refused {
			if line.Status == http.StatusTooManyRequests {
				t.Errorf("a 429 that is not marked refused: %+v", line)
			}
			continue
		}
		if line.Addr != house {
			t.Errorf("a refused credential's line names %q, want %s", line.Addr, house)
		}
		refused++
		if line.Status == http.StatusTooManyRequests {
			limited++
		}
	}
	if refused != 11 || limited != 1 {
		t.Errorf("%d lines marked refused, %d of them 429; want 11 and 1", refused, limited)
	}
	if raw := l.s.callers.rawForTest(t); strings.Contains(raw, "lab_0000") {
		t.Error("the caller log holds a refused credential")
	}
}

// Every 401 labd writes goes through the limiter's own file, so no route
// refuses a credential without counting it.
func TestNoRouteRefusesACredentialOutsideTheLimiter(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	raw := regexp.MustCompile(`httpErr\(\w+, ?(401|http\.StatusUnauthorized)|http\.Error\([^\n]*(401|StatusUnauthorized)|WriteHeader\((401|http\.StatusUnauthorized)\)`)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "refused.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if raw.MatchString(line) {
				t.Errorf("%s:%d writes a 401 itself; answer a refused credential with s.refuse, which counts it:\n%s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestTheLimiterForgetsAddressesItNoLongerCounts(t *testing.T) {
	l := newLimitedLab(t)
	for i := 0; i < 300; i++ {
		l.refuse("203.0.113."+strconv.Itoa(i%250+1), 1)
	}
	l.later(refusedWindow + time.Minute)
	l.refuse(house, 1)
	if n := l.s.refusals.addresses(); n != 1 {
		t.Errorf("the limiter still holds %d addresses, want the one it counts", n)
	}
}
