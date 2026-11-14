package server

import (
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/web"
)

// /status shows the lab's parts as labd last saw them (watch.go). It is
// open to anyone, so it carries states and times only: no reason, address
// or path. It asks nothing when it is opened.
func init() { addPage("GET /status", web.Anyone, (*Server).pStatus) }

func (s *Server) pStatus(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "status", web.Page{Title: "Status", Data: s.statusNow()})
}
