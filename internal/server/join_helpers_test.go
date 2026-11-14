package server

import (
	"encoding/json"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

// termsSum is the sha256 of the lab's terms, which a test's Mac accepts
// when it enrolls.
func termsSum() string { return web.Terms().SHA256 }

// testActivationKey is the activation key of the tests' raw enrollments: one
// Mac enrolling again with it is the same activation.
const testActivationKey = "0123456789abcdef0123456789abcdef-test"

// enrollJSON is an enrollment with token, its owner having accepted the terms.
func enrollJSON(token string) string {
	b, _ := json.Marshal(api.EnrollRequest{Token: token, ActivationKey: testActivationKey, TermsSHA256: termsSum()})
	return string(b)
}

// addToken stores an enrollment token as the lab kept one before Join
// commands: with no maker, and for a Volunteer's Mac with the name and
// contact a Maintainer typed. It is how a test puts a Volunteer's Mac with
// no person behind it in the lab.
func (s *store) addToken(hash string, req api.EnrollTokenRequest, exp time.Time) error {
	req.Volunteer = req.Volunteer || req.VolunteerName != ""
	_, err := s.db.Exec(`INSERT INTO tokens(token_hash,name,label,model,volunteer,requester,volunteer_name,contact,volunteer_login,expires) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		hash, req.Name, req.Label, req.Model, req.Volunteer, req.VolunteeredBy, req.VolunteerName, req.Contact, req.VolunteerLogin, exp.Unix())
	return err
}

// volunteeredTokenForTest is the token a tailnet login's ./volunteer got in
// a lab from before sign-in: the Mac it enrolls is that login's.
func (s *store) volunteeredTokenForTest(name, login string) (string, error) {
	tok := "enr_volunteered_" + name
	return tok, s.addToken(hashSecret(tok), api.EnrollTokenRequest{Name: name, Model: api.ModelLaptop, Volunteer: true, VolunteeredBy: login, VolunteerName: login, Contact: login}, time.Now().Add(time.Hour))
}
