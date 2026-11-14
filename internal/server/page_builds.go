package server

// Builds, for Maintainers: the kernel and package builds of the lab's
// Builder, who asked for each, and a build's own page with the end of its
// log.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /builds", web.Maintainers, (*Server).pBuilds)
	addPage("GET /builds/{id}", web.Maintainers, (*Server).pBuild)
	addPage("POST /builds/{id}/cancel", web.Maintainers, (*Server).pBuildCancel)
}

const (
	// buildsShown is how many builds Builds lists, newest first.
	buildsShown = 100
	// buildLogShown is how many lines of its log a build's page ends with.
	buildLogShown = 40
)

type builderBuild struct {
	ID string
	// What is what was built; Source where from, and For whose config.
	What, Source, For string
	whoAsked
	State, StateClass string
	// Doing is a running build's stage and how far it is.
	Doing      string
	When, Took string
}

// whoAsked is who asked for a build, as a page says it.
type whoAsked struct {
	// AskedBy is the handle; "" with ByLab is the lab itself, and "" alone a
	// build from before the lab recorded who asked.
	AskedBy string
	ByLab   bool
	// ByTeam marks a build an M team member asked for.
	ByTeam bool
}

func askedOf(b *api.Build) whoAsked {
	if b.AskedBy == "labd" {
		return whoAsked{ByLab: true}
	}
	return whoAsked{AskedBy: b.AskedBy, ByTeam: b.ByTeam}
}

func buildOpen(b *api.Build) bool {
	return b.State == api.BuildQueued || b.State == api.BuildRunning
}

func builderBuildOf(b *api.Build) builderBuild {
	row := builderBuild{ID: b.ID, What: orStr(b.Release, "a kernel"), For: orStr(b.Device, b.ConfigName), whoAsked: askedOf(b),
		State: string(b.State), StateClass: "job-live", When: whenDay(b.Created)}
	if b.Kind == api.BuildPackage {
		row.What = "packages from " + orStr(b.RecipeDir, "recipe "+short(b.Recipe))
		if b.Pkgrel != "" {
			row.What += ", pkgrel " + b.Pkgrel
		}
	}
	if b.Source.Repo != "" {
		row.Source = shortRepo(b.Source.Repo)
		if b.Source.Ref != "" {
			row.Source += "@" + b.Source.Ref
		}
		if sha := b.Source.SHA; sha != "" {
			row.Source += " " + sha[:min(12, len(sha))]
		}
	}
	switch b.State {
	case api.BuildDone:
		row.StateClass = "job-ok"
	case api.BuildFailed:
		row.StateClass = "job-bad"
	case api.BuildCanceled:
		row.StateClass = "job-dim"
	case api.BuildRunning:
		row.Doing = b.Stage
		if b.Progress > 0 {
			row.Doing += fmt.Sprintf(" %.0f%%", b.Progress*100)
		}
	}
	if b.Seconds > 0 {
		row.Took = took(time.Duration(b.Seconds * float64(time.Second)))
	}
	return row
}

type buildsData struct{ Builds []builderBuild }

func (s *Server) pBuilds(w http.ResponseWriter, r *http.Request) {
	builds, err := s.store.builds(`1=1 ORDER BY created DESC LIMIT ?`, buildsShown)
	if err != nil {
		s.log.Printf("builds: %v", err)
		httpErr(w, 500, "The lab couldn't list its builds.")
		return
	}
	rows := make([]builderBuild, 0, len(builds))
	for _, b := range builds {
		rows = append(rows, builderBuildOf(b))
	}
	s.render(w, r, "builds/index", web.Page{Title: "Builds", Nav: "builds", Data: buildsData{rows}})
}

type buildData struct {
	builderBuild
	Error, Builder string
	Config         string
	Started        string
	Reused         bool
	Artifact, Size string
	Files          []buildFile
	// Log is the end of the build's log.
	Log            []string
	CanCancel      bool
	CancelSentence string
	Reloads        bool
}

type buildFile struct{ Name, SHA, Size string }

func megabytes(n int64) string { return fmt.Sprintf("%d MB", n>>20) }

func buildCancelSentence(b *api.Build) string {
	return "Cancel: build " + b.ID + " stops, and a job waiting for it fails"
}

// buildOr404 is the build a page names.
func (s *Server) buildOr404(w http.ResponseWriter, r *http.Request) *api.Build {
	b, err := s.getBuild(r.PathValue("id"))
	if err != nil {
		s.notFound(w, r)
		return nil
	}
	return b
}

func (s *Server) pBuild(w http.ResponseWriter, r *http.Request) {
	b := s.buildOr404(w, r)
	if b == nil {
		return
	}
	data := buildData{builderBuild: builderBuildOf(b), Error: b.Error, Builder: b.Builder, Config: short(b.ConfigSHA), Reused: b.Reused,
		Artifact: b.Artifact, Log: s.buildLogTail(b.ID, buildLogShown), CanCancel: buildOpen(b), CancelSentence: buildCancelSentence(b), Reloads: buildOpen(b)}
	if !b.Started.IsZero() {
		data.Started = whenDay(b.Started)
	}
	if b.Size > 0 {
		data.Size = megabytes(b.Size)
	}
	for _, f := range b.Files {
		data.Files = append(data.Files, buildFile{f.Name, f.SHA256, megabytes(f.Size)})
	}
	s.render(w, r, "builds/show", web.Page{Title: "Build " + b.ID, Nav: "builds", Data: data})
}

func (s *Server) pBuildCancel(w http.ResponseWriter, r *http.Request) {
	b := s.buildOr404(w, r)
	if b == nil {
		return
	}
	if buildOpen(b) {
		if !s.sure(w, r, buildCancelSentence(b), "/builds/"+b.ID) {
			return
		}
		s.cancelBuild(b)
	}
	w.Header().Set("Location", s.basePath()+"/builds/"+b.ID)
	w.WriteHeader(http.StatusSeeOther)
}
