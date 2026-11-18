package server

// Package sets, for Maintainers, under Builds: every set, read only, and a
// set's own page with each package's source, version and sha256. Sets are
// made with lab set create or lab set capture.

import (
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /builds/sets", web.Maintainers, (*Server).pSets)
	addPage("GET /builds/sets/{name}", web.Maintainers, (*Server).pSet)
}

type setRow struct {
	Name, CreatedBy, When, From string
	Packages                    int
}

type setsData struct{ Sets []setRow }

func (s *Server) pSets(w http.ResponseWriter, r *http.Request) {
	sets, err := s.store.sets(`1=1 ORDER BY created DESC`)
	if err != nil {
		s.log.Printf("package sets: %v", err)
		httpErr(w, 500, "The lab couldn't list its package sets.")
		return
	}
	rows := make([]setRow, 0, len(sets))
	for _, set := range sets {
		rows = append(rows, setRow{Name: set.Name, CreatedBy: set.CreatedBy, When: whenDay(set.Created), From: set.CapturedFrom, Packages: len(set.Items)})
	}
	s.render(w, r, "builds/sets", web.Page{Title: "Package sets", Nav: "builds", Data: setsData{rows}})
}

type setData struct {
	setRow
	Items []api.SetItem
}

func (s *Server) pSet(w http.ResponseWriter, r *http.Request) {
	set, err := s.packageSet(r.PathValue("name"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	data := setData{setRow{Name: set.Name, CreatedBy: set.CreatedBy, When: whenDay(set.Created), From: set.CapturedFrom, Packages: len(set.Items)}, set.Items}
	s.render(w, r, "builds/set", web.Page{Title: "Package set " + set.Name, Nav: "builds", Data: data})
}
