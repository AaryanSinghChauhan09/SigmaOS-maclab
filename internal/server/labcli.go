package server

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// What the lab asks of a lab, the command, before it looks at its token.
//
// A lab names itself in User-Agent as lab/<release>. One that sends Go's own
// name was built from maclab and speaks differently on exec, uploads and
// Leases, so it is refused on every call with one sentence. One older than
// the lab's minimum release is refused in one line. A lab newer than the lab
// is never refused, and neither is a build from a checkout, which has no
// release to compare. A browser, curl and the test tool name themselves
// otherwise and are not a lab; a Mac and a builder have routes of their own.

// MinLabRelease is the oldest lab release the lab takes. It rises only when
// a Lab release's notes say so.
const MinLabRelease = "0.2.0"

// goDefaultAgent starts the User-Agent Go's HTTP client sends when a
// program names none.
const goDefaultAgent = "Go-http-client/"

var reLabAgent = regexp.MustCompile(`^lab/v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.]{1,32})?)(?: |$)`)

// labCalls reports whether rt is a route the lab command calls.
func labCalls(rt route) bool {
	switch rt.reach {
	case maintainers, team, teamBuilds, people, macNamed, jobNamed, scoped:
		return true
	}
	return rt.pattern == "GET /api/auth"
}

// labCall puts what the lab asks of a lab in front of a route one calls,
// and tells every caller of such a route the Lab release labd runs.
func (s *Server) labCall(rt route, h http.HandlerFunc) http.HandlerFunc {
	if !labCalls(rt) {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Release != "" {
			w.Header().Set("Lab-Release", s.cfg.Release)
		}
		agent := r.UserAgent()
		if strings.HasPrefix(agent, goDefaultAgent) {
			httpErr(w, http.StatusBadRequest, "This `lab` was not built for Omarchy Mac Lab. Install the one that was: %s", s.labInstallLine(r))
			return
		}
		if m := reLabAgent.FindStringSubmatch(agent); m != nil && api.ReleaseOlder(m[1], s.minLab()) {
			httpErr(w, http.StatusBadRequest, "lab %s is too old for Omarchy Mac Lab, which needs %s or later. Update it: %s", m[1], s.minLab(), s.labInstallLine(r))
			return
		}
		h(w, r)
	}
}

func (s *Server) minLab() string {
	if s.cfg.MinLab != "" {
		return s.cfg.MinLab
	}
	return MinLabRelease
}

// labInstallLine is the line that installs lab, and updates it.
func (s *Server) labInstallLine(r *http.Request) string {
	return "curl -fsSL " + s.publicBase(r) + "/install/lab | bash"
}
