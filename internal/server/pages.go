package server

// The interface's pages. A page is a template in internal/web/templates and
// a route added here with addPage, which says who may open it. A page a
// role may not open answers as one that is not there; signed out, it leads
// to sign-in. Every page is rendered inside the one header and footer, for
// whoever is looking (see web.Site).

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

type pageRoute struct {
	pattern string
	who     web.Access
	h       func(*Server, http.ResponseWriter, *http.Request)
}

var pageRoutes []pageRoute

// addPage adds a page's route, or the route of an action on a page, and who
// may reach it. A page's own file calls it from init.
func addPage(pattern string, who web.Access, h func(*Server, http.ResponseWriter, *http.Request)) {
	pageRoutes = append(pageRoutes, pageRoute{pattern, who, h})
}

type publicPageKey struct{}

func (s *Server) pageHandler(p pageRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch kind := s.viewerOf(r).Kind; {
		case p.who.Allows(kind):
			p.h(s, w, r.WithContext(context.WithValue(r.Context(), publicPageKey{}, p.who.Allows(web.SignedOut))))
		case kind == web.SignedOut && s.cfg.GitHub != nil:
			to := "/sign-in"
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				to += "?return=" + url.QueryEscape(s.basePath()+r.URL.RequestURI())
			}
			w.Header().Set("Location", to)
			w.WriteHeader(http.StatusSeeOther)
		default:
			s.notFound(w, r)
		}
	}
}

// viewerOf is who r's page is rendered for.
func (s *Server) viewerOf(r *http.Request) web.Viewer {
	p := PersonOf(r)
	if p == nil {
		return web.Viewer{}
	}
	return web.Viewer{Kind: p.Role(), Handle: p.Handle, CSRF: CSRFToken(r)}
}

// render writes the page whose template is name.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p web.Page) {
	if public, _ := r.Context().Value(publicPageKey{}).(bool); public {
		p.Public = true
	}
	// The Sign in link comes back to the page, without its query: a query
	// can carry a secret, as a report's deletion link does.
	req := web.Request{Viewer: s.viewerOf(r), Path: s.basePath() + r.URL.EscapedPath(), Preview: s.Preview()}
	if web.MTeamUp.Allows(req.Viewer.Kind) {
		req.Fleet = s.fleet()
	}
	if web.Maintainers.Allows(req.Viewer.Kind) {
		req.PendingRequests = s.pendingRequests()
	}
	if err := s.site.Render(w, name, req, p); err != nil {
		s.log.Printf("%v", err)
		httpErr(w, 500, "The lab couldn't make this page.")
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "not_found", web.Page{Title: "Not found", Status: http.StatusNotFound})
}

// fleet is the fleet status of the header's second row.
func (s *Server) fleet() *web.Fleet {
	f := &web.Fleet{}
	for _, v := range s.allDevs() {
		d := v.snapshot()
		if d.State == api.StateLeft {
			continue
		}
		f.Macs++
		switch d.State {
		case api.StateReady:
			if !s.leased(v) {
				f.Free++
			}
		case api.StateBusy:
			f.JobsRunning++
		case api.StateNeedsHands:
			f.NeedsHands++
		}
	}
	return f
}

// pendingRequests is the count People carries in the navigation. Nobody can
// ask for anything yet.
func (s *Server) pendingRequests() int { return 0 }

// sure reports whether the person asked twice for an action that takes
// something away. The lab's script asks in place: the first click turns the
// button into sentence, which states the effect, and the second posts with
// sure=yes. A post without it gets a page that asks again, with the same
// sentence as its button; back is the page to return to instead. When sure
// reports false it has answered w.
func (s *Server) sure(w http.ResponseWriter, r *http.Request, sentence, back string) bool {
	if r.PostFormValue("sure") == "yes" {
		return true
	}
	a := web.Asking{Action: s.basePath() + r.URL.EscapedPath(), Sentence: sentence, Back: s.basePath() + back}
	names := make([]string, 0, len(r.PostForm))
	for name := range r.PostForm {
		if name != "csrf_token" && name != "sure" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range r.PostForm[name] {
			a.Fields = append(a.Fields, web.Field{Name: name, Value: value})
		}
	}
	s.render(w, r, "ask_twice", web.Page{Title: "Asking twice", Data: a})
	return false
}

// withPages serves m, and for a page address m has no route for, the
// interface's own page saying so. Anything under /api/ keeps its plain
// answer.
func (s *Server) withPages(m *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/api/") {
			if h, pattern := m.Handler(r); pattern == "" {
				probe := &statusProbe{header: http.Header{}}
				h.ServeHTTP(probe, r)
				if probe.status == http.StatusNotFound {
					s.notFound(w, r)
					return
				}
			}
		}
		m.ServeHTTP(w, r)
	})
}

// statusProbe takes an answer and keeps only its status.
type statusProbe struct {
	header http.Header
	status int
}

func (p *statusProbe) Header() http.Header { return p.header }

func (p *statusProbe) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}

func (p *statusProbe) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return len(b), nil
}
