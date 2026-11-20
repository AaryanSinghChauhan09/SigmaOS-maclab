package server

import (
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/reports"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /{$}", web.Anyone, (*Server).pHome)
	addPage("GET /matrix", web.Anyone, (*Server).pMatrix)
}

type homePage struct {
	// MovedHere puts the banner naming the old site on the page: for 30
	// days from the lab's opening.
	MovedHere  bool
	OldSite    string
	InstallURL string
	Empty      bool
	Matrix     matrixView
	Legend     legendView
}

// pHome is the home page: the install line, and each model's latest
// results with its builds under it.
func (s *Server) pHome(w http.ResponseWriter, r *http.Request) {
	p := s.published(w)
	if p == nil {
		return
	}
	matrix := p.Matrix(reports.ByModel, nil)
	s.render(w, r, "home", web.Page{Data: homePage{MovedHere: s.movedHere(), OldSite: oldSite, InstallURL: s.publicBase(r) + "/install", Empty: matrix.Empty(),
		Matrix: s.matrixTable(matrix, p.Matrix(reports.ByBuild, nil).History()), Legend: legend()}})
}

type matrixPage struct {
	Group   string
	Views   []viewLink
	Bar     filterBarView
	ByModel bool
	Card    string
	Empty   bool
	Any     bool
	Matrix  matrixView
	Legend  legendView
}

type viewLink struct {
	Value, Label, Href string
	Current            bool
}

// pMatrix is the compatibility matrix, grouped by build, by release, or by
// model and stack; the model summary keeps its build history available.
func (s *Server) pMatrix(w http.ResponseWriter, r *http.Request) {
	p := s.published(w)
	if p == nil {
		return
	}
	query := r.URL.Query()
	filter := p.Filter(query, nil)
	group, by, card := "model", reports.ByModel, "model × stack"
	switch query.Get("group") {
	case "build":
		group, by, card = "build", reports.ByBuild, "model × stack/version × build"
	case "release":
		group, by, card = "release", reports.ByRelease, "model × stack/version"
	}
	matrix := p.Matrix(by, filter)
	var history reports.History
	if by == reports.ByModel {
		// Result filters narrow summaries; history keeps all matching runs as evidence.
		history = p.Matrix(reports.ByBuild, filter.Only(reports.RunKeys)).History()
	}
	page := matrixPage{Group: group, ByModel: by == reports.ByModel, Card: card, Empty: matrix.Empty() || len(matrix.Rows()) == 0, Any: filter.Any(), Legend: legend()}
	for _, view := range [][2]string{{"model", "Model"}, {"release", "Release"}, {"build", "Build"}} {
		page.Views = append(page.Views, viewLink{view[0], view[1], s.href("/matrix") + reports.Query(filter.Params("", "group", view[0])), view[0] == group})
	}
	page.Bar = s.filterBar(filter, p.Options(nil), "/matrix", reports.FilterKeys, matrix.RunsCount(), []string{"group", group},
		linkView{"matrix.json", s.href("/api/v1/matrix.json") + reports.Query(filter.Params("", "group", group))},
		linkView{"checks.csv", s.href("/api/v1/checks.csv") + reports.Query(filter.Params(""))})
	if !page.Empty {
		page.Matrix = s.matrixTable(matrix, history)
	}
	s.render(w, r, "matrix", web.Page{Title: "Compatibility matrix", Nav: "matrix", Wide: true, Data: page})
}
