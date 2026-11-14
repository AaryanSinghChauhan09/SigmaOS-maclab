package server

import "github.com/omacom/omarchy-maclab/internal/web"

// terms is the text a Mac's owner accepts before the Mac joins, and its
// sha256: the Volunteer's terms as shipped with this Lab release, the same
// text /terms serves and "Lend a Mac" shows. lab-agent setup shows them, the
// owner types yes, and the Mac's record keeps the sha256 of the text
// accepted, with the person, the Mac and the time.
func (s *Server) terms() (text, sum string) {
	t := web.Terms()
	return t.Body, t.SHA256
}
