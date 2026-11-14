package server

// The test tool's install line: curl -fsSL <Lab address>/install | bash.
// labd serves installer/install.sh, the same for whoever asks, public for
// five minutes.
//
// The script as it is in the repository downloads the latest Lab release
// and checks it against the release key. A labd in Preview, built as a Lab
// release, serves it naming that release instead: a Release candidate is
// never "latest", and until the lab opens the Release candidate the mini
// runs is the one to install. A labd built with another key than the
// repository's serves the script pinning its own: every file of one Lab
// release pins one key.

import (
	"bytes"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/omacom/omarchy-maclab/installer"
)

const (
	releaseDownloads = "https://github.com/omacom/omarchy-maclab/releases"
	latestRelease    = releaseDownloads + "/latest/download"
)

var (
	// reLabRelease is a Lab release's tag (release/lib.sh).
	reLabRelease = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`)
	reReleaseKey = regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/]+=*$`)
	// rePinnedKey is the key inside the script's allowed_signer line.
	rePinnedKey = regexp.MustCompile(`(?m)^(\s*local allowed_signer='\S+ namespaces="[^"]+" )ssh-ed25519 [A-Za-z0-9+/=]+'$`)
)

// installScript is the script this labd serves at /install.
func installScript(cfg Config) ([]byte, error) {
	script := installer.Script
	if cfg.Preview && reLabRelease.MatchString(cfg.Release) {
		if bytes.Count(script, []byte(latestRelease)) != 1 {
			return nil, errors.New("the install script does not name the latest Lab release in exactly one place")
		}
		script = bytes.Replace(script, []byte(latestRelease), []byte(releaseDownloads+"/download/"+cfg.Release), 1)
	}
	if cfg.ReleaseKey != "" {
		if !reReleaseKey.MatchString(cfg.ReleaseKey) {
			return nil, errors.New("the release key this labd was built with is not an ed25519 public key")
		}
		if len(rePinnedKey.FindAll(script, -1)) != 1 {
			return nil, errors.New("the install script does not pin the release key in exactly one place")
		}
		script = rePinnedKey.ReplaceAll(script, []byte("${1}"+cfg.ReleaseKey+"'"))
	}
	return script, nil
}

// hInstall is GET /install.
func (s *Server) hInstall(w http.ResponseWriter, r *http.Request) {
	hdr := w.Header()
	hdr.Set("Content-Type", "text/x-shellscript; charset=utf-8")
	hdr.Set("Content-Disposition", `inline; filename="install.sh"`)
	hdr.Set("Cache-Control", "public, max-age=300")
	hdr.Set("Content-Length", strconv.Itoa(len(s.installScript)))
	if r.Method != http.MethodHead {
		w.Write(s.installScript)
	}
}
