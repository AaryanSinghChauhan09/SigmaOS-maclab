package server

// "Ask to be forgotten" on a person's own page, and Forget on People: the
// request waits on People until a Maintainer carries it out (forgetting.go).

import (
	"errors"
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("POST /me/forget", web.SignedIn, (*Server).pAskToBeForgotten)
	addPage("POST /people/{id}/forget", web.Maintainers, (*Server).pForget)
}

const askToBeForgotten = "Ask to be forgotten: a Maintainer erases what the lab keeps of you within 30 days. It cannot be undone"

func (s *Server) pAskToBeForgotten(w http.ResponseWriter, r *http.Request) {
	p := PersonOf(r)
	if asked, err := s.store.forgetAsked(p.AccountID); err != nil {
		s.pageFailed(w, err)
		return
	} else if !asked.IsZero() {
		s.renderMe(w, r, http.StatusConflict, func(page *mePage) {
			page.ForgetRefused = "You asked already, on " + asked.UTC().Format(pageDate) + "."
		})
		return
	}
	if !s.sure(w, r, askToBeForgotten, "/me") {
		return
	}
	if _, err := s.store.askToBeForgotten(p.AccountID, s.now()); err != nil {
		s.pageFailed(w, err)
		return
	}
	if err := s.record(r, "asked to be forgotten", nil); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	http.Redirect(w, r, s.basePath()+"/me", http.StatusSeeOther)
}

// pForget is Forget, on a request or in a person's row. Its address names
// the person, so the caller log keeps it without their account id.
func (s *Server) pForget(w http.ResponseWriter, r *http.Request) {
	notePath(r, "/people/-/forget")
	k, err := s.personAt(r)
	if err == nil {
		err = mayForget(PersonOf(r), k)
	}
	if err == nil {
		if !s.sure(w, r, s.forgetEnds(k), "/people?open="+r.PathValue("id")) {
			return
		}
		err = s.forgetPerson(r, k)
	}
	s.done(w, r, 0, err)
}

// forgetControl is what a person's row on People offers: the sentence of
// Forget, or why the viewer may not.
func (s *Server) forgetControl(me *Person, k knownPerson) (sentence, why string) {
	err := mayForget(me, k)
	var no *refusal
	switch {
	case err == nil:
		return s.forgetEnds(k), ""
	case errors.As(err, &no):
		return "", no.msg
	}
	return "", ""
}

// forgetRequestsFor are the waiting requests as the viewer may act on them.
func (s *Server) forgetRequestsFor(me *Person) ([]forgetRequest, error) {
	waiting, err := s.store.forgetRequests()
	if err != nil {
		return nil, err
	}
	for i := range waiting {
		q := &waiting[i]
		if k, err := s.subject(q.AccountID); err == nil {
			q.Sentence, q.Why = s.forgetControl(me, k)
		}
	}
	return waiting, nil
}
