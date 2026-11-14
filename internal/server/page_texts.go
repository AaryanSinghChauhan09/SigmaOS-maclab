package server

import (
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /terms", web.Anyone, (*Server).pTerms)
	addPage("GET /privacy", web.Anyone, (*Server).pPrivacy)
}

func (s *Server) pTerms(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "text", web.Page{Title: "Terms", Data: web.Terms()})
}

func (s *Server) pPrivacy(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "text", web.Page{Title: "Privacy", Data: web.Privacy()})
}
