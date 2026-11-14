package server

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test tool's install line, curl -fsSL https://lab.omarchy-mac.org/install | bash,
// is served by the lab: translated from the old site's site_test.rb.

const (
	latestDownload = "https://github.com/omacom/omarchy-maclab/releases/latest/download"
	realReleaseKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKHA9ywzf4hxAd5Ohzw8tRg5i2lxHAxp2mDV0PWi5o0u"
)

func installerFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "installer", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTheInstallerIsServedAsAShellScriptByteForByteAndPublicForFiveMinutes(t *testing.T) {
	l := newReportsLab(t, nil)
	want := installerFile(t)
	for _, method := range []string{"GET", "HEAD"} {
		w := l.do(method, "/install", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s /install: %d %s", method, w.Code, w.Body)
		}
		for header, value := range map[string]string{
			"Content-Type":           "text/x-shellscript; charset=utf-8",
			"Content-Disposition":    `inline; filename="install.sh"`,
			"Cache-Control":          "public, max-age=300",
			"X-Content-Type-Options": "nosniff",
		} {
			if got := w.Header().Get(header); got != value {
				t.Errorf("%s /install, %s: %q, want %q", method, header, got, value)
			}
		}
		if len(w.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s /install set a cookie", method)
		}
		if method == "GET" && w.Body.String() != want {
			t.Error("/install is not installer/install.sh byte for byte")
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Error("HEAD /install has a body")
		}
	}
	if strings.Count(want, latestDownload) != 1 || strings.Count(want, realReleaseKey) != 1 {
		t.Fatal("installer/install.sh names the latest Lab release or the release key more or less than once: labd rewrites each in one place")
	}
	// It is the same for whoever asks, since the edge keeps one copy.
	if w := l.do("GET", "/install", "", "Cookie", "lab_session=anything", "Authorization", "Bearer lab_adm_secret"); w.Body.String() != want || w.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Errorf("/install with a credential: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if w := l.do("POST", "/install", ""); w.Code != http.StatusMethodNotAllowed && w.Code != http.StatusNotFound {
		t.Errorf("POST /install: %d", w.Code)
	}
}

// Decision S4: while the lab is in Preview the install line serves the
// Release candidate the mini runs; from the lab's opening, the latest Lab
// release.
func TestInPreviewTheInstallLineServesTheReleaseTheLabRuns(t *testing.T) {
	file := installerFile(t)
	served := func(preview bool, release string) string {
		t.Helper()
		l := newReportsLab(t, func(c *Config) { c.Preview, c.Release = preview, release })
		w := l.do("GET", "/install", "")
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Fatalf("/install: %d %q", w.Code, w.Header().Get("Cache-Control"))
		}
		return w.Body.String()
	}
	for _, tag := range []string{"v0.2.0-rc.3", "v0.2.0"} {
		want := strings.Replace(file, latestDownload, "https://github.com/omacom/omarchy-maclab/releases/download/"+tag, 1)
		if got := served(true, tag); got != want {
			t.Errorf("in Preview, a lab on %s does not serve that release and nothing else changed", tag)
		}
		if got := served(false, tag); got != file {
			t.Errorf("an open lab on %s does not serve the latest Lab release", tag)
		}
	}
	// A build that is no Lab release has none of its own to serve.
	for _, release := range []string{"", "dev", "v0.2", "v0.2.0-rc.1/../../x", "v0.2.0-beta"} {
		if got := served(true, release); got != file {
			t.Errorf("in Preview, a labd built as %q does not serve the script as it is", release)
		}
	}
}

// The script checks the download against the release key of the build that
// serves it: the one key every file of a Lab release pins.
func TestTheInstallLinePinsTheReleaseKeyItsLabWasBuiltWith(t *testing.T) {
	file := installerFile(t)
	const other = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIThrowawayKeyForALocalReleaseCandidate0000000"
	served := func(key string) string {
		t.Helper()
		return newReportsLab(t, func(c *Config) { c.ReleaseKey = key }).do("GET", "/install", "").Body.String()
	}
	if got := served(realReleaseKey); got != file {
		t.Error("a lab built with the release key does not serve the script as it is")
	}
	if got, want := served(other), strings.Replace(file, realReleaseKey, other, 1); got != want || strings.Contains(got, realReleaseKey) {
		t.Error("a lab built with another key does not pin that key, in the one place")
	}
	for _, bad := range []string{"ssh-ed25519", "ssh-rsa AAAAB3Nza", "ssh-ed25519 AAAA' ; rm -rf ~ ; '", "ssh-ed25519 AAAA\nx"} {
		cfg := Config{DataDir: t.TempDir(), ReleaseKey: bad}
		if _, err := newWith("ada", "lab_adm_secret", t.Context(), cfg, log.New(os.Stderr, "", 0)); err == nil {
			t.Errorf("labd started with the release key %q", bad)
		}
	}
}

func TestTheAliasForwardsTheInstallLineToTheLabAddress(t *testing.T) {
	l := newReportsLab(t, nil)
	w := l.do("GET", "/install", "", "Host", aliasHost)
	if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != labAddress+"/install" {
		t.Errorf("the Alias: %d to %q", w.Code, w.Header().Get("Location"))
	}
	if strings.Contains(w.Body.String(), "ssh-keygen") {
		t.Error("the Alias served the script itself")
	}
}
