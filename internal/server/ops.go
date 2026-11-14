package server

// The ops socket is how the mini's own scripts tell "Agents" a line: the
// nightly backup, the Restore drill, the watch on container's server, a
// Deploy, a Maintenance run (scripts/mini/notify). It is a second Unix
// socket, which only the account labd runs as can open, and that is the
// whole credential: no token, nothing in an argument. It takes a line for
// "Agents" and nothing else, and none of the lab's own routes is on it.
// The tunnel's socket has no such route: what arrives there comes from
// the internet.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

// opsLineMax is the longest line a script may tell, in bytes.
const opsLineMax = 2000

var reOpsKey = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ListenOps opens the ops socket at path, for its owner alone.
func ListenOps(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("--ops-socket %q: give the socket's full path", path)
	}
	return listenSocket("--ops-socket", path)
}

// OpsServer serves the ops socket.
func (s *Server) OpsServer() *http.Server {
	return &http.Server{Handler: s.OpsHandler(), ReadHeaderTimeout: headerTimeout, ReadTimeout: headerTimeout, IdleTimeout: idleTimeout, MaxHeaderBytes: maxHeaderSize}
}

// OpsHandler is what the ops socket answers:
//
//	POST /agents/line             the body is one line for "Agents"
//	POST /agents/trouble/{key}    the same, told once while the trouble lasts
//	POST /agents/recovered/{key}  told only if "Agents" was told of that trouble
//
// The answer says whether the line was recorded.
func (s *Server) OpsHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /agents/line", func(w http.ResponseWriter, r *http.Request) {
		if text, ok := opsLine(w, r); ok {
			s.lines.post(lineAgents, text)
			io.WriteString(w, "recorded\n")
		}
	})
	keyed := func(tell func(key, text string) bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			key := r.PathValue("key")
			if !reOpsKey.MatchString(key) {
				httpErr(w, http.StatusBadRequest, "a trouble's name is lower-case letters, digits and dashes, 40 at most")
				return
			}
			text, ok := opsLine(w, r)
			if !ok {
				return
			}
			if tell(key, text) {
				io.WriteString(w, "recorded\n")
			} else {
				io.WriteString(w, "nothing to tell\n")
			}
		}
	}
	m.HandleFunc("POST /agents/trouble/{key}", keyed(s.agents.Trouble))
	m.HandleFunc("POST /agents/recovered/{key}", keyed(s.agents.Recovered))
	return m
}

// opsLine reads the one line of a request; when it reports false it has
// answered w.
func opsLine(w http.ResponseWriter, r *http.Request) (string, bool) {
	b, err := io.ReadAll(io.LimitReader(r.Body, opsLineMax+1))
	if err != nil {
		httpErr(w, http.StatusBadRequest, "the line could not be read")
		return "", false
	}
	if len(b) > opsLineMax {
		httpErr(w, http.StatusRequestEntityTooLarge, "a line is %d bytes at most", opsLineMax)
		return "", false
	}
	text := oneLine(strings.ToValidUTF8(string(b), ""))
	if text == "" {
		httpErr(w, http.StatusBadRequest, "no line")
		return "", false
	}
	return text, true
}
