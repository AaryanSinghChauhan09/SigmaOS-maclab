package server

// The two install lines: curl -fsSL <Lab address>/install | bash for the
// test tool, and <Lab address>/install/lab for lab. labd serves
// installer/install.sh and installer/install-lab.sh, the same for whoever
// asks, public for five minutes.
//
// Each script as it is in the repository downloads the latest Lab release
// and checks it against the release key. A labd in Preview, built as a Lab
// release, serves both naming that release instead: a Release candidate is
// never "latest", and until the lab opens the Release candidate the mini
// runs is the one to install (decision S4). A labd built with another key
// than the repository's serves the scripts pinning its own: every file of
// one Lab release pins one key.

import (
	"bytes"
	"errors"
	"net/http"
	"regexp"
	"strconv"
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
	rePinnedKey = regexp.MustCompile(`(?m)^(\s*local allowed_signer='\S+ namespaces="[^"]+" )(ssh-ed25519 [A-Za-z0-9+/=]+)'$`)
)

// installScript is script, one of the two install scripts, as this labd
// serves it.
func installScript(cfg Config, script []byte) ([]byte, error) {
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

// hInstall is GET /install, the test tool's install line.
func (s *Server) hInstall(w http.ResponseWriter, r *http.Request) {
	serveInstall(w, r, "install.sh", s.installScript)
}

// hInstallLab is GET /install/lab, lab's install line.
func (s *Server) hInstallLab(w http.ResponseWriter, r *http.Request) {
	serveInstall(w, r, "install-lab.sh", s.installLabScript)
}

func serveInstall(w http.ResponseWriter, r *http.Request, name string, script []byte) {
	hdr := w.Header()
	hdr.Set("Content-Type", "text/x-shellscript; charset=utf-8")
	hdr.Set("Content-Disposition", `inline; filename="`+name+`"`)
	hdr.Set("Cache-Control", "public, max-age=300")
	hdr.Set("Content-Length", strconv.Itoa(len(script)))
	if r.Method != http.MethodHead {
		w.Write(script)
	}
}
