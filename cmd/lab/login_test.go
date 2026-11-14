package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/client"
	"github.com/omacom/omarchy-maclab/internal/testhome"
)

// No test of lab reads or writes the config directory of whoever runs the
// tests: the package has a home of its own, and each test that logs in a
// fresh one.
func TestMain(m *testing.M) {
	testhome.Main(m)
}

// aLab answers as Omarchy Mac Lab does to the Lab token lab_good, and says
// it runs release.
func aLab(t *testing.T, release string) (url string, requests *[]string) {
	t.Helper()
	requests = new([]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Lab-Release", release)
		if r.Header.Get("Authorization") != "Bearer lab_good" || r.URL.Path != "/api/auth" {
			http.Error(w, "not a Lab token", 401)
			return
		}
		io.WriteString(w, `{"login":"ada","kind":"Maintainer"}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MACLAB_URL", "")
	t.Setenv("MACLAB_TOKEN", "")
	return srv.URL, requests
}

func stored() bool {
	_, err := os.Stat(client.LoginPath())
	return !errors.Is(err, os.ErrNotExist)
}

func TestLoginPromptsForTheTokenAndNeverTakesItAsAnArgument(t *testing.T) {
	url, requests := aLab(t, "v0.2.0")
	var out bytes.Buffer
	err := login([]string{url, "lab_good"}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "never an argument") || strings.Contains(err.Error(), "lab_good") {
		t.Fatalf("lab login <url> <token> says %v", err)
	}
	if len(*requests) != 0 || stored() {
		t.Fatalf("a token given as an argument was used: %v", *requests)
	}
	if err := login([]string{url}, strings.NewReader("\n"), &out); err == nil || stored() {
		t.Fatalf("lab login with no token: %v", err)
	}

	out.Reset()
	if err := login([]string{url}, strings.NewReader("  lab_good\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Logged in to Omarchy Mac Lab as @ada.\n" {
		t.Fatalf("lab login says %q", out.String())
	}
	if !stored() {
		t.Fatal("the login was not stored")
	}
	if b, _ := os.ReadFile(client.LoginPath()); !strings.Contains(string(b), "lab_good") || !strings.Contains(string(b), url) {
		t.Fatalf("the stored login: %s", b)
	}
}

func TestLoginThatTheLabRefusesSaysWhatTheLabSaidAndStoresNothing(t *testing.T) {
	url, _ := aLab(t, "v0.2.0")
	var out bytes.Buffer
	err := login([]string{url}, strings.NewReader("lab_other\n"), &out)
	if err == nil || err.Error() != "not a Lab token" || out.Len() != 0 || stored() {
		t.Fatalf("lab login with a token the lab does not know: %v, %q", err, out.String())
	}
}

// lab login adds one line when the lab runs a newer release than lab's own.
func TestLoginSaysWhenTheLabRunsANewerRelease(t *testing.T) {
	old := version
	t.Cleanup(func() { version, client.Release = old, old })
	for _, c := range []struct {
		own, labs string
		newer     bool
	}{
		{"v0.2.0", "v0.3.0", true},
		{"v0.2.0", "v0.2.1-rc.1", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.2.0-rc.2", "v0.2.0", false},
		{"v0.3.0", "v0.2.0", false},
		{"dev", "v0.2.0", false},
		{"v0.2.0", "", false},
	} {
		version, client.Release = c.own, c.own
		url, _ := aLab(t, c.labs)
		var out bytes.Buffer
		if err := login([]string{url}, strings.NewReader("lab_good\n"), &out); err != nil {
			t.Fatal(err)
		}
		want := "Logged in to Omarchy Mac Lab as @ada.\n"
		if c.newer {
			want += "Omarchy Mac Lab runs a newer release, " + c.labs + ". Update lab: curl -fsSL " + url + "/install/lab | bash\n"
		}
		if out.String() != want {
			t.Errorf("lab %s at a lab running %q says %q", c.own, c.labs, out.String())
		}
	}
}

func TestVersionPrintsItsReleaseAndTheLabs(t *testing.T) {
	old := version
	version = "v0.2.0"
	t.Cleanup(func() { version = old })
	url, requests := aLab(t, "v0.3.0-rc.1")
	var out bytes.Buffer
	err := showVersion(context.Background(), &out)
	if err != nil || !strings.HasPrefix(out.String(), "lab v0.2.0\nNot logged in to Omarchy Mac Lab. ") || strings.Count(out.String(), "\n") != 2 {
		t.Fatalf("lab version with no login: %q, %v", out.String(), err)
	}
	if len(*requests) != 0 {
		t.Fatalf("lab version with no login asked %v", *requests)
	}
	if err := login([]string{url}, strings.NewReader("lab_good\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := showVersion(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "lab v0.2.0\nOmarchy Mac Lab v0.3.0-rc.1\n" {
		t.Fatalf("lab version says %q", out.String())
	}
}

func TestForgetAndMaintainerAreRetired(t *testing.T) {
	for cmd, want := range map[string]string{
		"forget":     "`lab forget` is not part of Omarchy Mac Lab.",
		"maintainer": "`lab maintainer` is not part of Omarchy Mac Lab: Maintainer is a mark on People.",
	} {
		if err := retired(cmd); err == nil || err.Error() != want {
			t.Errorf("lab %s says %v", cmd, err)
		}
		if strings.Contains(usage, "lab "+cmd) {
			t.Errorf("lab help still lists lab %s", cmd)
		}
	}
	if retired("status") != nil {
		t.Error("lab status is retired")
	}
	for _, line := range strings.Split(usage, "\n") {
		if strings.Contains(line, "lab login") && (strings.Contains(line, "token>") || !strings.Contains(line, "lab login <url>")) {
			t.Errorf("lab help: %s", line)
		}
	}
	if !strings.Contains(usage, "Omarchy Mac Lab") {
		t.Error("lab help does not name Omarchy Mac Lab")
	}
}
