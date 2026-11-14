package server

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// liveLab is a labd in Preview with sign-in, a machine-id key and marks made
// by the store, as the lab is on the day before it opens.
func liveLab(t *testing.T, mTeam int, config func(*Config)) *signInLab {
	t.Helper()
	l := newSignInLab(t, func(c *Config) {
		c.Preview, c.MachineIDKey, c.Release = true, testMachineIDKey(), "v0.2.0-rc.4"
		if config != nil {
			config(c)
		}
	})
	for i := 0; i < mTeam; i++ {
		id := int64(7000 + i)
		if _, err := l.s.store.db.Exec(`INSERT INTO people (account_id, handle, m_team, added) VALUES (?, ?, 1, ?)`, id, "member-"+string(rune('a'+i)), l.clock.Now().UnixMicro()); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// ops asks labd on its ops socket's routes.
func (l *signInLab) ops(method, target, body string) *httptest.ResponseRecorder {
	l.t.Helper()
	w := httptest.NewRecorder()
	l.s.OpsHandler().ServeHTTP(w, httptest.NewRequest(method, "http://labd"+target, strings.NewReader(body)))
	return w
}

const (
	previewBanner = `The live site is <a href="https://omarchy-m-testing.org">omarchy-m-testing.org</a>.`
	movedBanner   = `omarchy-m-testing.org is now here.`
)

func sendsAReport(l *signInLab) *httptest.ResponseRecorder {
	return l.browser().do("POST", "/api/v1/reports", "{}", "Content-Type", "application/json")
}

// The opening, in one step: uploads and Mac sign-in accepted, the Preview
// banner and noindex off, the banner naming the old site on the home page,
// the install line serving the latest Lab release.
func TestTheOpeningTurnsPreviewOffInOneStep(t *testing.T) {
	l := liveLab(t, 14, nil)
	closed := func(what string) {
		t.Helper()
		if !l.s.Preview() {
			t.Fatalf("%s: the lab is not in Preview", what)
		}
		if w := sendsAReport(l); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "not open yet") {
			t.Errorf("%s: an upload: %d %s", what, w.Code, w.Body)
		}
		if w := l.browser().do("POST", "/api/v1/tester_bindings", "{}", "Content-Type", "application/json"); w.Code != http.StatusForbidden {
			t.Errorf("%s: a Mac sign-in: %d %s", what, w.Code, w.Body)
		}
		w := l.browser().do("GET", "/", "")
		if body := w.Body.String(); !strings.Contains(body, previewBanner) || strings.Contains(body, movedBanner) || !strings.Contains(body, `<meta name="robots" content="noindex">`) {
			t.Errorf("%s: the home page lacks Preview's banner or its noindex, or says the old site moved here", what)
		}
		if w.Header().Get("X-Robots-Tag") != "noindex" {
			t.Errorf("%s: X-Robots-Tag %q", what, w.Header().Get("X-Robots-Tag"))
		}
		if body := l.browser().do("GET", "/install", "").Body.String(); !strings.Contains(body, "/releases/download/v0.2.0-rc.4") {
			t.Errorf("%s: the install line does not serve the Release candidate the lab runs", what)
		}
		if w := l.ops("GET", "/stage", ""); strings.TrimSpace(w.Body.String()) != "preview" {
			t.Errorf("%s: GET /stage: %d %q", what, w.Code, w.Body)
		}
	}
	closed("before the opening")

	l.clock.Advance(time.Minute)
	at := l.clock.Now()
	w := l.ops("POST", "/open?m-team=14", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "live "+at.UTC().Format(time.RFC3339) {
		t.Fatalf("POST /open: %d %q", w.Code, w.Body)
	}
	if l.s.Preview() {
		t.Fatal("the lab is still in Preview after the opening")
	}
	if w := sendsAReport(l); w.Code == http.StatusForbidden || w.Code == http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "not open yet") {
		t.Errorf("an upload after the opening is still refused as closed: %d %s", w.Code, w.Body)
	}
	if w := l.browser().do("POST", "/api/v1/tester_bindings", "{}", "Content-Type", "application/json"); w.Code == http.StatusForbidden {
		t.Errorf("a Mac sign-in after the opening: %d %s", w.Code, w.Body)
	}
	home := l.browser().do("GET", "/", "")
	if body := home.Body.String(); strings.Contains(body, previewBanner) || strings.Contains(body, "noindex") || !strings.Contains(body, movedBanner) {
		t.Errorf("the home page after the opening: Preview's banner or noindex is there, or the banner naming the old site is not")
	}
	if got := home.Header().Get("X-Robots-Tag"); got != "" {
		t.Errorf("X-Robots-Tag after the opening: %q", got)
	}
	// Only the home page names the old site.
	for _, target := range []string{"/terms", "/privacy", "/reports"} {
		if body := l.browser().do("GET", target, "").Body.String(); strings.Contains(body, movedBanner) || strings.Contains(body, previewBanner) {
			t.Errorf("GET %s after the opening carries a banner", target)
		}
	}
	if body := l.browser().do("GET", "/install", "").Body.String(); !strings.Contains(body, "/releases/latest/download") || strings.Contains(body, "rc.4") {
		t.Errorf("the install line after the opening does not serve the latest Lab release")
	}
	if w := l.ops("GET", "/stage", ""); strings.TrimSpace(w.Body.String()) != "live "+at.UTC().Format(time.RFC3339) {
		t.Errorf("GET /stage after the opening: %q", w.Body)
	}

	// Asked again it changes nothing and says when the lab opened.
	l.clock.Advance(time.Hour)
	if w := l.ops("POST", "/open?m-team=14", ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "live "+at.UTC().Format(time.RFC3339) {
		t.Errorf("POST /open a second time: %d %q", w.Code, w.Body)
	}
}

// The lab stays open over a restart, and over a Deploy whose LaunchDaemon
// file still says --preview: there is no way back into Preview.
func TestALiveLabStaysOpenWhenLabdStartsWithPreviewAgain(t *testing.T) {
	l := liveLab(t, 14, nil)
	at := l.clock.Now()
	if w := l.ops("POST", "/open?m-team=14", ""); w.Code != http.StatusOK {
		t.Fatalf("POST /open: %d %s", w.Code, w.Body)
	}
	cfg := l.s.cfg
	out := &bytes.Buffer{}
	again, err := newWith("ada", "lab_adm_other", t.Context(), cfg, log.New(out, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Preview || again.Preview() {
		t.Fatalf("a labd started with --preview on a Live lab's data: Preview() is %v", again.Preview())
	}
	if got, ok := again.Live(); !ok || !got.Equal(at) {
		t.Errorf("the restarted labd says the lab opened at %v (%v), want %v", got, ok, at)
	}
	if !strings.Contains(out.String(), "the lab is Live") {
		t.Errorf("the restarted labd does not log that the lab is Live and --preview is not obeyed: %q", out)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://"+labHost+"/install", nil)
	again.Handler().ServeHTTP(w, r.WithContext(onTunnel(r.Context())))
	if body, _ := io.ReadAll(w.Body); !strings.Contains(string(body), "/releases/latest/download") {
		t.Errorf("the restarted labd's install line does not serve the latest Lab release")
	}
}

// The opening refuses to run when what must be true first is not, and
// changes nothing.
func TestTheOpeningRefusesWhenWhatMustBeTrueFirstIsNot(t *testing.T) {
	for _, c := range []struct {
		name   string
		marked int
		config func(*Config)
		query  string
		status int
		says   string
	}{
		{"13 of the 14 are marked", 13, nil, "?m-team=14", http.StatusConflict, "13 people are marked M team, not 14"},
		{"15 are marked", 15, nil, "?m-team=14", http.StatusConflict, "15 people are marked M team, not 14"},
		{"no number is given", 14, nil, "", http.StatusBadRequest, "how many M team members"},
		{"the number is no number", 14, nil, "?m-team=all", http.StatusBadRequest, "how many M team members"},
		{"no machine-id key", 14, func(c *Config) { c.MachineIDKey = nil }, "?m-team=14", http.StatusConflict, "machine-id key"},
		{"no OAuth app", 14, func(c *Config) { c.GitHub = nil }, "?m-team=14", http.StatusConflict, "sign-in is off"},
		{"a labd that is not in Preview", 14, func(c *Config) { c.Preview = false }, "?m-team=14", http.StatusConflict, "not in Preview"},
	} {
		l := liveLab(t, c.marked, c.config)
		w := l.ops("POST", "/open"+c.query, "")
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.says) {
			t.Errorf("%s: POST /open: %d %q, want %d naming %q", c.name, w.Code, w.Body, c.status, c.says)
		}
		if _, ok := l.s.Live(); ok {
			t.Errorf("%s: the lab opened all the same", c.name)
		}
		if l.s.cfg.Preview && !l.s.Preview() {
			t.Errorf("%s: the lab left Preview", c.name)
		}
	}
}

// The opening is not on the tunnel's socket or the loopback port: only the
// ops socket, which the lab account alone can open, has it.
func TestTheOpeningIsOnTheOpsSocketOnly(t *testing.T) {
	l := liveLab(t, 14, nil)
	admin := l.signedIn(ownerID)
	for _, target := range []string{"/open", "/api/open", "/stage"} {
		if w := admin.do("POST", target, "m-team=14", "Content-Type", "application/x-www-form-urlencoded", "Origin", labAddress); w.Code == http.StatusOK {
			t.Errorf("POST %s through the lab's own routes: %d", target, w.Code)
		}
	}
	if _, ok := l.s.Live(); ok {
		t.Error("the lab opened from a route that is not the ops socket's")
	}
}

// Step 34: the banner naming the old site is on the home page for 30 days
// after the lab opens, and comes off by itself.
func TestTheMovedHereBannerEnds30DaysAfterLive(t *testing.T) {
	l := liveLab(t, 14, nil)
	if w := l.ops("POST", "/open?m-team=14", ""); w.Code != http.StatusOK {
		t.Fatalf("POST /open: %d %s", w.Code, w.Body)
	}
	shown := func() bool {
		return strings.Contains(l.browser().do("GET", "/", "").Body.String(), movedBanner)
	}
	l.clock.Advance(30*24*time.Hour - time.Second)
	if !shown() {
		t.Error("a second before 30 days after Live the banner is gone")
	}
	l.clock.Advance(time.Second)
	if shown() {
		t.Error("30 days after Live the banner is still on the home page")
	}
	// A labd that never was in Preview has no old site to name.
	open := newSignInLab(t, nil)
	if strings.Contains(open.browser().do("GET", "/", "").Body.String(), movedBanner) {
		t.Error("a lab that was never opened carries the banner")
	}
}

// The daily summary, once by hand (the Confirmed checklist).
func TestTheDailySummaryCanBeSentOnceByHand(t *testing.T) {
	l := liveLab(t, 0, nil)
	lines := func() (out []string) {
		rows, err := l.s.store.db.Query(`SELECT text FROM lines WHERE channel = 'lab' ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var text string
			rows.Scan(&text)
			out = append(out, text)
		}
		return out
	}
	before := lines()
	if w := l.ops("POST", "/summary", ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "recorded" {
		t.Fatalf("POST /summary: %d %q", w.Code, w.Body)
	}
	after := lines()
	if len(after) != len(before)+1 || !strings.HasPrefix(after[len(after)-1], "Omarchy Mac Lab, ") {
		t.Errorf("the Lab group's lines after a summary by hand: %q", after)
	}
}
