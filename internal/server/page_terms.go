package server

// Accept on My Macs: the one control on the site that gives. A Volunteer
// whose Macs wait for the lab's changed terms accepts them here, signed in,
// once for all their Macs (terms.go).

import (
	"errors"
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("POST /my-macs/accept-terms", web.SignedIn, (*Server).pAcceptTerms)
}

func (s *Server) pAcceptTerms(w http.ResponseWriter, r *http.Request) {
	p := PersonOf(r)
	if p == nil {
		s.notFound(w, r)
		return
	}
	_, err := s.acceptTerms(*p)
	if err == nil {
		http.Redirect(w, r, s.basePath()+myMacsPath(*p), http.StatusSeeOther)
		return
	}
	code, said := http.StatusInternalServerError, "The lab couldn't do that."
	var no *refusal
	if errors.As(err, &no) {
		code, said = no.code, no.msg
	} else {
		s.log.Printf("My Macs, accepting the terms: %v", err)
	}
	s.render(w, r, "terms_accept", web.Page{Title: "My Macs", Nav: "my-macs", Status: code, Data: said})
}
