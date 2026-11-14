package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/client"
)

const (
	maclabsAgent   = "Go-http-client/1.1"
	labInstallLine = "curl -fsSL " + labAddress + "/install/lab | bash"
	notOurLab      = "This `lab` was not built for Omarchy Mac Lab. Install the one that was: " + labInstallLine + "\n"
)

// asProgram asks labd through the tunnel as the program that names itself
// agent, with a Lab token unless token is "".
func (l *signInLab) asProgram(agent, token, method, target string) *httptest.ResponseRecorder {
	l.t.Helper()
	// A stream that labd opens ends with the request, so a route that should
	// have refused fails its test and does not hold it.
	ctx, cancel := context.WithTimeout(onTunnel(context.Background()), 3*time.Second)
	defer cancel()
	r := httptest.NewRequest(method, "http://"+labHost+target, strings.NewReader(`{}`)).WithContext(ctx)
	r.RemoteAddr = viaProxy
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("CF-Connecting-IP", "198.51.100.7")
	if agent != "" {
		r.Header.Set("User-Agent", agent)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	l.h.ServeHTTP(w, r)
	return w
}

var rePathValue = regexp.MustCompile(`\{[a-z]+(\.\.\.)?\}`)

// A lab built from maclab sends Go's own User-Agent. Every route a lab
// calls refuses it with the one sentence and the install line, whatever
// token it carries; a Mac, a builder, the Join command and the test tool
// are not a lab and are not asked.
func TestALabThatDoesNotNameItselfIsRefusedOnEveryCall(t *testing.T) {
	l := pagesLab(t, nil)
	var asked int
	for _, rt := range l.s.routes() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		path = rePathValue.ReplaceAllString(path, "x")
		forLab := rt.pattern == "GET /api/auth"
		switch rt.reach {
		case maintainers, team, teamBuilds, people, macNamed, jobNamed, scoped:
			forLab = true
		}
		for _, token := range []string{"lab_adm_secret", "mnt_0000000000000000000000000000000000000000", ""} {
			w := l.asProgram(maclabsAgent, token, method, path)
			switch refused := w.Body.String() == notOurLab; {
			case forLab && (!refused || w.Code != http.StatusBadRequest):
				t.Errorf("%s from a lab that does not name itself: %d %q", rt.pattern, w.Code, w.Body.String())
			case !forLab && refused:
				t.Errorf("%s refused a caller that is not a lab: %q", rt.pattern, w.Body.String())
			}
		}
		if forLab {
			asked++
		}
	}
	if asked < 60 {
		t.Fatalf("only %d routes are a lab's", asked)
	}
	if w := l.asProgram("Go-http-client/2.0", "lab_adm_secret", "GET", "/api/devices"); w.Body.String() != notOurLab {
		t.Errorf("Go's name over HTTP/2: %d %s", w.Code, w.Body.String())
	}
	for _, named := range []string{"lab/0.2.0", "lab/dev", "curl/8.7.1", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", "Python-urllib/3.13", ""} {
		if w := l.asProgram(named, "lab_adm_secret", "GET", "/api/devices"); w.Code != http.StatusOK {
			t.Errorf("a caller named %q: %d %s", named, w.Code, w.Body.String())
		}
	}
}

// The lab keeps one minimum lab release. An older lab is refused in one
// line with the install line; a Release candidate of the minimum meets it,
// and a lab newer than the lab itself is never refused.
func TestAnOlderLabIsRefusedInOneLineAndANewerOneNever(t *testing.T) {
	l := pagesLab(t, func(c *Config) { c.MinLab, c.Release = "0.3.0", "v0.3.1" })
	for agent, ok := range map[string]bool{
		"lab/0.2.0":      false,
		"lab/0.2.9-rc.4": false,
		"lab/0.3.0-rc.1": true,
		"lab/0.3.0":      true,
		"lab/0.3.1":      true,
		"lab/0.4.0":      true,
		"lab/12.0.0":     true,
		"lab/dev":        true,
	} {
		w := l.asProgram(agent, "lab_adm_secret", "POST", "/api/devices/m1/exec")
		number := strings.TrimPrefix(agent, "lab/")
		want := "lab " + number + " is too old for Omarchy Mac Lab, which needs 0.3.0 or later. Update it: " + labInstallLine + "\n"
		switch refused := strings.Contains(w.Body.String(), "too old"); {
		case ok && refused:
			t.Errorf("%s was refused: %s", agent, w.Body.String())
		case !ok && (w.Body.String() != want || w.Code != http.StatusBadRequest):
			t.Errorf("%s: %d %q", agent, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Lab-Release"); got != "v0.3.1" {
			t.Errorf("%s was told the lab runs %q", agent, got)
		}
	}

	plain := pagesLab(t, nil)
	if w := plain.asProgram("lab/"+MinLabRelease, "lab_adm_secret", "GET", "/api/devices"); w.Code != http.StatusOK {
		t.Errorf("a lab of the minimum release, %s: %d %s", MinLabRelease, w.Code, w.Body.String())
	}
	if w := plain.asProgram("lab/0.1.9", "lab_adm_secret", "GET", "/api/devices"); !strings.Contains(w.Body.String(), "lab 0.1.9 is too old for Omarchy Mac Lab, which needs "+MinLabRelease+" or later") {
		t.Errorf("a lab older than the minimum release: %d %s", w.Code, w.Body.String())
	}
}

// frontDoor is an address at which labd is reached as through the tunnel,
// asked for by the name host.
func (l *signInLab) frontDoor(host string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = host
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("CF-Connecting-IP", "198.51.100.7")
		l.h.ServeHTTP(w, r.WithContext(onTunnel(r.Context())))
	}))
	l.t.Cleanup(srv.Close)
	return srv.URL
}

// lab login against labd itself: the lab names the person and its release;
// at the Alias, with a token labd does not know and with an expired one it
// fails in one plain line and stores nothing.
func TestLabLoginAtTheLabAtTheAliasAndWithAnExpiredToken(t *testing.T) {
	l := pagesLab(t, func(c *Config) { c.Alias, c.Release = aliasHost, "v0.2.0" })
	token := l.signedIn(ownerID).makeToken("laptop", "30")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MACLAB_URL", "")
	t.Setenv("MACLAB_TOKEN", "")
	ctx := context.Background()
	stored := func() bool {
		_, err := os.Stat(client.LoginPath())
		return !errors.Is(err, os.ErrNotExist)
	}

	if who, err := client.Login(ctx, l.frontDoor(aliasHost), token, "", false); who != nil || err == nil ||
		err.Error() != "Omarchy Mac Lab is at "+labAddress+": use that address" || stored() {
		t.Fatalf("lab login at the Alias: %v, stored %v", err, stored())
	}
	if _, err := client.Login(ctx, l.frontDoor(labHost), "lab_0000000000000000000000000000000000000000", "", false); err == nil || err.Error() != "not a Lab token" || stored() {
		t.Fatalf("lab login with a token labd does not know: %v", err)
	}

	who, err := client.Login(ctx, l.frontDoor(labHost), token, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if who.Handle != "owner" || who.LabRelease != "v0.2.0" || !stored() {
		t.Fatalf("logged in as %+v, stored %v", who, stored())
	}
	c, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Devices(ctx); err != nil {
		t.Fatalf("lab status with the saved login: %v", err)
	}

	os.Remove(client.LoginPath())
	l.clock.Advance(31 * 24 * time.Hour)
	_, err = client.Login(ctx, l.frontDoor(labHost), token, "", false)
	if err == nil || !strings.Contains(err.Error(), "expired") || !strings.HasSuffix(err.Error(), "renew it on your own page, "+labAddress+"/me") || stored() {
		t.Fatalf("lab login with an expired token: %v", err)
	}
}
