package server

import (
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() { addPage("GET /{$}", web.Anyone, (*Server).pHome) }

func (s *Server) pHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "home", web.Page{})
}
