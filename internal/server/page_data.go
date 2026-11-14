package server

import (
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/reports"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /data", web.Anyone, (*Server).pData)
}

type dataPage struct {
	LicenseURL     string
	Schema         int
	UploadsPerHour int
}

// pData is the exports, each with what it holds, and the upload's address.
func (s *Server) pData(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "data", web.Page{Title: "Data", Nav: "data",
		Data: dataPage{LicenseURL: reports.LicenseURL, Schema: reports.SchemaVersion, UploadsPerHour: reports.UploadsPerHour}})
}
