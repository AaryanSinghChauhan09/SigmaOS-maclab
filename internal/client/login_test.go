package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/testhome"
)

var execArgs = api.ExecArgs{Command: "true"}

// No test of the client reads or writes the config directory of whoever
// runs the tests: the package has a home of its own.
func TestMain(m *testing.M) {
	testhome.Main(m)
}

func TestTheTestsLoginIsKeptInTheirOwnHome(t *testing.T) {
	if rest, err := filepath.Rel(testhome.Dir(), LoginPath()); testhome.Dir() == "" || err != nil || strings.HasPrefix(rest, "..") {
		t.Fatalf("the tests keep lab's login at %s, outside their own home %q", LoginPath(), testhome.Dir())
	}
}

// home gives the test a config directory of its own and no MACLAB_*
// setting, and returns the folder lab keeps its files in.
func home(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("HOME", dir)
	for _, k := range []string{"MACLAB_URL", "MACLAB_TOKEN", "MACLAB_CA_FILE", "MACLAB_ALLOW_PLAIN_HTTP"} {
		t.Setenv(k, "")
	}
	return filepath.Dir(LoginPath())
}

// fakeLab answers /api/auth as labd does for the Lab token "lab_good",
// and lists no Mac. It notes every request's User-Agent.
type fakeLab struct {
	*httptest.Server
	agents []string
	paths  []string
}

func newFakeLab(t *testing.T, more http.HandlerFunc) *fakeLab {
	t.Helper()
	l := &fakeLab{}
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.agents = append(l.agents, r.UserAgent())
		l.paths = append(l.paths, r.Method+" "+r.URL.Path)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Lab-Release", "v0.3.0")
		switch {
		case more != nil:
			more(w, r)
		case r.Header.Get("Authorization") != "Bearer lab_good":
			http.Error(w, "not a Lab token", 401)
		case r.URL.Path == "/api/auth":
			io.WriteString(w, `{"login":"ada","kind":"Maintainer","account_id":7}`)
		case r.URL.Path == "/api/devices":
			io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(l.Close)
	return l
}

func TestLoginIsKeptInItsOwnFileAndClientJSONIsNeverReadOrWritten(t *testing.T) {
	dir := home(t)
	theirs := []byte(`{"url":"http://127.0.0.1:1","token":"mnt_for_another_lab"}` + "\n")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.json"), theirs, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.HasPrefix(err.Error(), "Not logged in to Omarchy Mac Lab") || !strings.Contains(err.Error(), LabAddress+"/me") {
		t.Fatalf("with a login for another lab only, lab says %v", err)
	}

	lab := newFakeLab(t, nil)
	who, err := Login(context.Background(), lab.URL+"/", "lab_good", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if who.Handle != "ada" || who.LabRelease != "v0.3.0" {
		t.Fatalf("logged in as %+v", who)
	}
	if filepath.Base(LoginPath()) != "omarchy-mac-lab.json" || filepath.Base(dir) != "maclab" {
		t.Fatalf("the login is kept at %s", LoginPath())
	}
	st, err := os.Stat(LoginPath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the login file: %v %v", st, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "client.json")); string(b) != string(theirs) {
		t.Fatalf("client.json was changed: %s", b)
	}
	names, _ := os.ReadDir(dir)
	if len(names) != 2 {
		t.Fatalf("lab's folder holds %v", names)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != lab.URL || c.Token != "lab_good" {
		t.Fatalf("the saved login is %s", c.URL)
	}
	if _, err := c.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A login the lab does not take stores nothing, and leaves an earlier login
// as it was. What the lab said is what the person reads, alone.
func TestALoginTheLabRefusesStoresNothing(t *testing.T) {
	const blocked = "This GitHub account is blocked from Omarchy Mac Lab."
	const expired = `this Lab token, "laptop" of @ada, expired on 2026-11-01 after 30 days unused: renew it on your own page, https://lab.omarchy-mac.org/me`
	const alias = "Omarchy Mac Lab is at https://lab.omarchy-mac.org: use that address"
	for _, c := range []struct {
		what   string
		status int
		says   string
	}{
		{"a Blocked person", 403, blocked},
		{"an expired token", 401, expired},
		{"the Alias", 421, alias},
		{"a token the lab does not know", 401, "not a Lab token"},
	} {
		t.Run(c.what, func(t *testing.T) {
			home(t)
			lab := newFakeLab(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, c.says, c.status) })
			who, err := Login(context.Background(), lab.URL, "lab_good", "", false)
			if who != nil || err == nil || err.Error() != c.says {
				t.Fatalf("lab login says %q", err)
			}
			if _, err := os.Stat(LoginPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a login was stored: %v", err)
			}
		})
	}

	home(t)
	good := newFakeLab(t, nil)
	if _, err := Login(context.Background(), good.URL, "lab_good", "", false); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(LoginPath())
	bad := newFakeLab(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, alias, 421) })
	if _, err := Login(context.Background(), bad.URL, "lab_good", "", false); err == nil {
		t.Fatal("a login at the Alias worked")
	}
	if after, _ := os.ReadFile(LoginPath()); string(after) != string(before) {
		t.Fatalf("a refused login changed the saved one: %s", after)
	}

	// A server that answers, but not as Omarchy Mac Lab does, is no login.
	home(t)
	other := newFakeLab(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"trusted":true,"role":"admin"}`) })
	if _, err := Login(context.Background(), other.URL, "lab_good", "", false); err == nil || !strings.Contains(err.Error(), "Omarchy Mac Lab") {
		t.Fatalf("a login at a server that names nobody: %v", err)
	}
	if _, err := os.Stat(LoginPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a login was stored")
	}
	if _, err := Login(context.Background(), other.URL, "mnt_0000", "", false); err == nil || !strings.Contains(err.Error(), "lab_") || len(other.paths) != 1 {
		t.Fatalf("something that is not a Lab token was sent: %v, %v", err, other.paths)
	}
}

func TestMACLABURLAndTokenTogetherOverrideOneCall(t *testing.T) {
	home(t)
	saved, other := newFakeLab(t, nil), newFakeLab(t, nil)
	if _, err := Login(context.Background(), saved.URL, "lab_good", "", false); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(LoginPath())
	t.Setenv("MACLAB_URL", other.URL)
	t.Setenv("MACLAB_TOKEN", "lab_good")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.URL != other.URL || other.paths[len(other.paths)-1] != "GET /api/devices" || len(saved.paths) != 1 {
		t.Fatalf("the call went to %s; the saved lab saw %v", c.URL, saved.paths)
	}
	if after, _ := os.ReadFile(LoginPath()); string(after) != string(before) {
		t.Fatal("an override changed the saved login")
	}

	// With no saved login at all, the two still reach a lab.
	home(t)
	t.Setenv("MACLAB_URL", other.URL)
	t.Setenv("MACLAB_TOKEN", "lab_good")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LoginPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an override stored a login")
	}
}

// MACLAB_URL alone never sends the saved token anywhere but the address it
// was saved for. An address that differs only by a trailing slash, by the
// case of its scheme or host, or by naming the scheme's own port is that
// address; a path in other letters is another one.
func TestMACLABURLAloneNamingAnotherAddressIsRefused(t *testing.T) {
	for _, c := range []struct {
		saved, given string
		same         bool
	}{
		{"https://lab.example.org", "https://lab.example.org", true},
		{"https://lab.example.org", "https://lab.example.org/", true},
		{"https://lab.example.org", "https://lab.example.org//", true},
		{"https://lab.example.org", "https://LAB.Example.ORG", true},
		{"https://lab.example.org", "HTTPS://lab.example.org", true},
		{"https://lab.example.org", "https://lab.example.org:443", true},
		{"https://lab.example.org/maclab", "https://Lab.example.org/maclab/", true},
		{"https://lab.example.org/maclab", "https://lab.example.org/MacLab", false},
		{"https://lab.example.org/maclab", "https://lab.example.org", false},
		{"https://lab.example.org", "https://lab.example.org:8443", false},
		{"https://lab.example.org", "https://lab.example.org.evil.example", false},
		{"https://lab.example.org", "https://evil.example/lab.example.org", false},
		{"https://lab.example.org", "http://lab.example.org", false},
		{"https://lab.example.org", "https://lab.example.org/?x=1", false},
	} {
		home(t)
		keep := &Client{URL: c.saved, Token: "lab_saved"}
		if err := keep.Save(); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MACLAB_URL", c.given)
		t.Setenv("MACLAB_ALLOW_PLAIN_HTTP", "1")
		got, err := Load()
		switch {
		case c.same && (err != nil || got.URL != c.saved || got.Token != "lab_saved"):
			t.Errorf("saved %s, MACLAB_URL=%s: %v %+v", c.saved, c.given, err, got)
		case !c.same && (err == nil || !strings.Contains(err.Error(), "MACLAB_URL") || !strings.Contains(err.Error(), c.saved) || strings.Contains(err.Error(), "lab_saved")):
			t.Errorf("saved %s, MACLAB_URL=%s was not refused: %v", c.saved, c.given, err)
		}
	}

	home(t)
	t.Setenv("MACLAB_URL", "https://lab.example.org")
	if _, err := Load(); err == nil || !strings.HasPrefix(err.Error(), "Not logged in to Omarchy Mac Lab") {
		t.Errorf("MACLAB_URL alone with no login: %v", err)
	}

	home(t)
	keep := &Client{URL: "https://lab.example.org", Token: "lab_saved"}
	if err := keep.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACLAB_TOKEN", "lab_other")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MACLAB_TOKEN") || strings.Contains(err.Error(), "lab_other") {
		t.Errorf("MACLAB_TOKEN alone: %v", err)
	}
}

func TestEveryCallNamesItself(t *testing.T) {
	home(t)
	old := Release
	Release = "v0.2.0-rc.3"
	t.Cleanup(func() { Release = old })
	lab := newFakeLab(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/auth":
			io.WriteString(w, `{"login":"ada"}`)
		case r.URL.Path == "/api/artifacts":
			io.WriteString(w, `{"sha256":"2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"}`)
		case r.URL.Path == "/api/devices/m1/exec":
			w.Header().Set("Content-Type", heldType)
			io.WriteString(w, "\n{\"result\":{}}\n")
		default:
			io.WriteString(w, `[]`)
		}
	})
	ctx := context.Background()
	if _, err := Login(ctx, lab.URL, "lab_good", "", false); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "foo")
	os.WriteFile(file, []byte("foo"), 0o600)
	c.Devices(ctx)
	c.ConsoleWrite(ctx, "m1", []byte("x"))
	c.ConsoleRaw(ctx, "m1", io.Discard)
	c.BuildLog(ctx, "b1", false, io.Discard)
	c.Exec(ctx, "m1", execArgs)
	if _, err := c.Upload(ctx, file); err != nil {
		t.Fatal(err)
	}
	c.BuildFile(ctx, "b1", "linux.pkg", "00", filepath.Join(t.TempDir(), "out"))
	if len(lab.agents) < 8 {
		t.Fatalf("the lab saw %v", lab.paths)
	}
	for i, a := range lab.agents {
		if a != "lab/0.2.0-rc.3" {
			t.Errorf("%s came as %q", lab.paths[i], a)
		}
	}
}

const edgePage = `<!DOCTYPE html>
<html><head><title>lab.omarchy-mac.org | 530: Origin DNS error</title></head>
<body><h1>Error 1033</h1><p>Cloudflare Tunnel error</p></body></html>`

// While the lab is Dark its edge answers with a page. lab says the lab
// cannot be reached and that no reset is possible until it returns, with the
// status and the ray id, and never the page.
func TestAnEdgeErrorPageIsNeverPrinted(t *testing.T) {
	home(t)
	status, kind := 530, "text/html; charset=UTF-8"
	lab := newFakeLab(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", kind)
		w.Header().Set("CF-Ray", "8f2c4d1e2a3b4c5d-BNE")
		w.WriteHeader(status)
		io.WriteString(w, edgePage)
	})
	t.Setenv("MACLAB_URL", lab.URL)
	t.Setenv("MACLAB_TOKEN", "lab_good")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "foo")
	os.WriteFile(file, []byte("foo"), 0o600)
	calls := map[string]func() error{
		"status":    func() error { _, err := c.Devices(ctx); return err },
		"reset":     func() error { _, err := c.Reset(ctx, "m1", "ada"); return err },
		"exec":      func() error { _, err := c.Exec(ctx, "m1", execArgs); return err },
		"console":   func() error { return c.ConsoleRaw(ctx, "m1", io.Discard) },
		"build-log": func() error { return c.BuildLog(ctx, "b1", false, io.Discard) },
		"build-get": func() error { return c.BuildFile(ctx, "b1", "linux.pkg", "00", filepath.Join(t.TempDir(), "out")) },
		"upload":    func() error { _, err := c.Upload(ctx, file); return err },
	}
	c.pause = func(context.Context, int) error { return nil }
	check := func(want string) {
		t.Helper()
		for what, call := range calls {
			err := call()
			if err == nil || !strings.Contains(err.Error(), want) || strings.ContainsAny(err.Error(), "<>") || strings.Contains(err.Error(), "1033") {
				t.Errorf("lab %s at HTTP %d (%s) says %q", what, status, kind, err)
			}
		}
	}
	check("Omarchy Mac Lab cannot be reached right now (HTTP 530, ray 8f2c4d1e2a3b4c5d-BNE). No reset is possible until it returns.")
	if _, err := c.Devices(ctx); err == nil || err.Error() != "Omarchy Mac Lab cannot be reached right now (HTTP 530, ray 8f2c4d1e2a3b4c5d-BNE). No reset is possible until it returns." {
		t.Errorf("lab status says %q", err)
	}
	if _, err := Login(ctx, lab.URL, "lab_good", "", false); err == nil || !strings.HasPrefix(err.Error(), "Omarchy Mac Lab cannot be reached right now") {
		t.Errorf("lab login says %q", err)
	}
	status = 403 // an edge challenge
	check("Omarchy Mac Lab cannot be reached right now (HTTP 403, ray 8f2c4d1e2a3b4c5d-BNE). No reset is possible until it returns.")
	status, kind = 530, "text/plain" // the edge's page under another type
	check("Omarchy Mac Lab cannot be reached right now (HTTP 530, ray 8f2c4d1e2a3b4c5d-BNE).")
	status, kind = 502, ""
	check("Omarchy Mac Lab cannot be reached right now (HTTP 502, ray 8f2c4d1e2a3b4c5d-BNE).")
}

// What labd itself says in refusing is read as it is.
func TestLabdsOwnRefusalIsReadAsItIs(t *testing.T) {
	home(t)
	lab := newFakeLab(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "m1 is leased by @grace until 18:00", 409)
	})
	t.Setenv("MACLAB_URL", lab.URL)
	t.Setenv("MACLAB_TOKEN", "lab_good")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Devices(context.Background())
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 409 || fmt.Sprint(err) != "m1 is leased by @grace until 18:00" {
		t.Fatalf("lab says %v", err)
	}
	if c.LabRelease() != "v0.3.0" {
		t.Fatalf("the lab's release is read as %q", c.LabRelease())
	}
}
