package server

// Joining a Mac with a Join command. The person who lends the Mac makes it,
// signed in, on "Lend a Mac"; a Maintainer makes one for a Lab-owned Mac
// with lab enroll (POST /api/enroll-tokens). It is one line to run on the
// Mac. It downloads lab-agent with the enrollment token
// (GET /api/join/lab-agent), which tests the token before anything changes:
// a refusal is said once, by its case, and nothing is tried again. It checks
// the download's sha256 against the one written into it, and runs
// `sudo lab-agent setup`, the token going to it on stdin. Setup binds the
// token to the Mac (POST /api/join/check), shows the lab's terms
// (GET /api/join/terms), which the owner accepts by typing yes, before it
// changes anything; then the Mac enrolls, using the token up (see hEnroll).
//
// The token travels in the Authorization header and request bodies only,
// never in a URL, so neither labd's nor a proxy's access log records it. It
// reaches these three routes and the enrollment itself, nothing else. Every
// address in the line is the Lab address.

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

var reEnrollToken = regexp.MustCompile(`^enr_[0-9a-f]{32}$`)

// joinToken admits a request carrying an unexpired enrollment token, used or
// not: setup reads the terms and downloads lab-agent again when it runs
// again after an interruption.
func (s *Server) joinToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if !strings.HasPrefix(tok, "enr_") || len(tok) > 200 || !s.tokenValid(hashSecret(tok)) {
			if tok != "" {
				s.refusedToken(w, r, 401, "enrollment token unknown, used or expired")
				return
			}
			httpErr(w, 401, "enrollment token unknown, used or expired")
			return
		}
		h(w, r)
	}
}

func (s *Server) hJoinTerms(w http.ResponseWriter, r *http.Request) {
	t := web.Terms()
	writeJSON(w, api.JoinTerms{SHA256: t.SHA256, Text: t.Body, Date: t.Date})
}

// hJoinCheck is the Join command testing its token on the Mac, after the
// pre-checks and before anything changes. The first Mac to ask binds the
// token: from then on it serves the Mac with that activation key and no
// other. What would refuse the enrollment (Blocked, cap reached, name
// taken) is said here already, so that a Mac leaves no other lab for a
// lab that will not take it.
func (s *Server) hJoinCheck(w http.ResponseWriter, r *http.Request) {
	var req api.JoinCheckRequest
	if !decodeBody(w, r, enrollBodyMax, &req) {
		return
	}
	if len(req.ActivationKey) < 32 || len(req.ActivationKey) > 128 {
		httpErr(w, 400, "activation key: want 32 to 128 characters")
		return
	}
	tokHash, keyHash := hashSecret(bearer(r)), hashSecret(req.ActivationKey)
	t, err := s.store.tokenState(tokHash, s.now())
	if err != nil {
		s.refusedToken(w, r, 401, tokenRefused)
		return
	}
	if mine, err := s.store.bindToken(tokHash, keyHash); err != nil || !mine {
		s.refusedToken(w, r, 403, tokenOtherMacs)
		return
	}
	if t.ActivatedBy == "" {
		if err := s.mayEnroll(&t.EnrollTokenRequest); err != nil {
			s.refusedEnrollment(w, t.Name, err)
			return
		}
	}
	kind := api.KindLabOwned
	if t.Volunteer {
		kind = api.KindVolunteer
	}
	terms := web.Terms()
	writeJSON(w, api.JoinCheck{Name: t.Name, Kind: kind, Expires: t.Expires, Enrolled: t.ActivatedBy != "", TermsSHA256: terms.SHA256, TermsDate: terms.Date})
}

// agentSum caches the sha256 of the lab-agent labd hands out, by its size and
// modification time.
type agentSum struct {
	mu   sync.Mutex
	size int64
	mod  time.Time
	sum  string
}

var agentSums agentSum

// agentSHA256 is the sha256 of the lab-agent labd hands out, "" when it has
// none.
func (s *Server) agentSHA256() string {
	f, err := os.Open(s.cfg.AgentBinary)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	agentSums.mu.Lock()
	defer agentSums.mu.Unlock()
	if agentSums.sum != "" && agentSums.size == st.Size() && agentSums.mod.Equal(st.ModTime()) {
		return agentSums.sum
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	agentSums.size, agentSums.mod, agentSums.sum = st.Size(), st.ModTime(), hex.EncodeToString(h.Sum(nil))
	return agentSums.sum
}

// publicBase is labd's URL as a Mac reaches it: --public-url, else the
// origin r reached labd at.
func (s *Server) publicBase(r *http.Request) string {
	if s.cfg.PublicURL != nil {
		return s.cfg.PublicURL.String()
	}
	origin, _ := s.externalOrigin(r)
	return origin
}

// reShellSafe is what joinCommand puts in single quotes: URL and token
// characters, nothing a shell gives meaning to inside them.
var reShellSafe = regexp.MustCompile(`^[A-Za-z0-9:/._~\[\]-]+$`)

// What the Join command says, once, when the lab does not hand it the
// agent: the case, and what to do.
const (
	joinSaysUsed       = "This Join command has expired or was used: make a new one on Lend a Mac."
	joinSaysTooMany    = "Too many refused attempts from this network: wait 15 minutes, then make a new Join command."
	joinSaysChallenged = "The front door of the lab challenged this request instead of answering it: try again later or from another network."
	joinSaysOther      = "The lab did not answer as expected"
)

// joinCommand is the one line that joins a Mac with token, "" when labd has
// no lab-agent to hand out. Over https curl may use nothing else; over plain
// http setup must be told it is allowed (and only loopback is, by default).
// The download is the token's test: an answer other than the agent stops
// the line there, named by its case, before anything on the Mac changes.
func (s *Server) joinCommand(r *http.Request, token string) string {
	base, sum := s.publicBase(r), s.agentSHA256()
	if sum == "" || !reShellSafe.MatchString(base) || !reEnrollToken.MatchString(token) {
		return ""
	}
	curl, plain := "curl --proto '=https' --tlsv1.2 -sS", ""
	if !strings.HasPrefix(base, "https://") {
		curl, plain = "curl -sS", " --allow-plain-http"
	}
	// The token is in no process's arguments: printf is the shell's own, and
	// curl and setup read it on stdin. The leading space keeps the line out
	// of the shell's history where it ignores such lines.
	return " T='" + token + "' U='" + base + "' D=\"$(mktemp -d)\" && " +
		"C=\"$(printf 'Authorization: Bearer %s\\n' \"$T\" | " + curl + " -A lab-join -H @- -o \"$D/lab-agent\" -w '%{http_code}%header{cf-mitigated}' \"$U/api/join/lab-agent\")\" && " +
		"case \"$C\" in 200) ;; 401) echo '" + joinSaysUsed + "' >&2; false ;; 429) echo '" + joinSaysTooMany + "' >&2; false ;; " +
		"*challenge) echo '" + joinSaysChallenged + "' >&2; false ;; *) echo \"" + joinSaysOther + " ($C).\" >&2; false ;; esac && " +
		"echo '" + sum + "  '\"$D/lab-agent\" | sha256sum -c --quiet && chmod +x \"$D/lab-agent\" && " +
		"printf '%s\\n' \"$T\" | sudo \"$D/lab-agent\" setup --server \"$U\" --token-stdin --gui-user \"$USER\"" + plain
}
