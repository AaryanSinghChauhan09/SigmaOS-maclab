package server

// Reports from omarchy-m-test: the upload, and the two links every upload is
// answered with, which work as the old site's did with only the host
// changed. /reports/<public_id> is the report's page;
// /reports/<public_id>/deletion?token=<secret> opens a confirmation, and the
// report is deleted only when that is sent.

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"

	"github.com/omacom/omarchy-maclab/catalogue"
	"github.com/omacom/omarchy-maclab/internal/reports"
	"github.com/omacom/omarchy-maclab/internal/web"
)

// oldSite is the live site until the lab opens.
const oldSite = "omarchy-m-testing.org"

func refuseJSON(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// hUploadReport is POST /api/v1/reports. The address its limit counts on is
// the caller's: CF-Connecting-IP on the tunnel's socket and nowhere else.
func (s *Server) hUploadReport(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.Preview():
		refuseJSON(w, http.StatusForbidden, "Omarchy Mac Lab is not open yet and takes no reports. Until it opens, "+oldSite+" is the live site: upload there.")
	case s.uploads == nil:
		refuseJSON(w, http.StatusServiceUnavailable, "This lab takes no reports yet: it was given no machine-id key. The report is saved on your Mac.")
	default:
		if err := s.uploads.Handle(w, r, callerAddr(r), s.publicBase(r)); err != nil {
			s.log.Printf("report upload: %v", err)
		}
	}
}

func (s *Server) hReportPage(w http.ResponseWriter, r *http.Request) {
	report, err := s.reports.Report(r.PathValue("id"))
	if errors.Is(err, reports.ErrNoReport) || (err == nil && report.Hidden) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.pageFailed(w, err)
		return
	}
	p := s.reportPage(report)
	s.render(w, r, "reports/show", web.Page{Title: p.Title, Nav: "reports", Public: true, Data: p})
}

func (s *Server) pageFailed(w http.ResponseWriter, err error) {
	s.log.Printf("report page: %v", err)
	httpErr(w, http.StatusInternalServerError, "the lab could not show this page")
}

// deletionAsked finds the report a deletion link names: the report with that
// public id whose secret is token. Anything else is not found, whichever of
// the two is wrong.
func (s *Server) deletionAsked(w http.ResponseWriter, r *http.Request, token string) *reports.Report {
	// The answer to a deletion link is never kept and names no page it came from.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	report, err := s.reports.Report(r.PathValue("id"))
	if errors.Is(err, reports.ErrNoReport) || (err == nil && !report.DeletionTokenMatches(token)) {
		http.NotFound(w, r)
		return nil
	}
	if err != nil {
		s.pageFailed(w, err)
		return nil
	}
	return report
}

func (s *Server) hReportDeletion(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	report := s.deletionAsked(w, r, token)
	if report == nil {
		return
	}
	model, _ := member(report.Document(), "machine", "model").(string)
	s.render(w, r, "reports/deletion", web.Page{Title: "Delete report", Nav: "reports", Public: true,
		Data: struct{ Model, Uploaded, Action, Token, CSRF string }{
			model, uploadedAt(report), s.basePath() + "/reports/" + report.PublicID, token, CSRFToken(r)}})
}

// hReportDelete is the confirmation, sent: the form of the deletion page,
// which carries the link's secret in its body.
func (s *Server) hReportDelete(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, formMax)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("_method") != "delete" {
		http.NotFound(w, r)
		return
	}
	token := r.PostForm.Get("token")
	report := s.deletionAsked(w, r, token)
	if report == nil {
		return
	}
	switch err := s.reports.DeleteByLink(report.PublicID, token); {
	case errors.Is(err, reports.ErrNoReport):
		http.NotFound(w, r)
	case err != nil:
		s.pageFailed(w, err)
	default:
		s.render(w, r, "reports/deleted", web.Page{Title: "Report deleted", Nav: "reports", Public: true})
	}
}

func uploadedAt(report *reports.Report) string {
	return report.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")
}

// member follows names into a report's document; nil where it ends early.
func member(value any, path ...string) any {
	for _, key := range path {
		object, _ := value.(*reports.Object)
		if object == nil {
			return nil
		}
		value, _ = object.Get(key)
	}
	return value
}

// words is a report's value as its page shows it.
func words(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case *big.Int:
		return value.String()
	}
	return ""
}

type reportPage struct {
	Title, PublicID, Model                  string
	Chip, SoC, Board, Storage, Kernel       string
	Distro, BootLoader, Encryption          string
	ToolName, ToolVersion, CatalogueVersion string
	Uploaded                                string
	Pass, Fail, Gap, Skip                   int
	Unattended, Verified                    bool
	Packages                                []reportPackage
	Sections                                []*reportSection
}

type reportPackage struct{ Name, Version string }

type reportSection struct {
	Name   string
	Checks []reportCheck
}

type reportCheck struct {
	ID, StatusClass, StatusLabel string
	Classified, Unconfirmed      bool
	Outcome, OutcomeWords, State string
	Feature, Layer               string
	Evidence                     string
}

// outcomeStates is the result state a check's outcome counts as.
var outcomeStates = map[string]string{
	"works":            "works",
	"fails":            "fails",
	"not-in-aurora":    "missing",
	"not-in-asahi":     "missing",
	"not-in-omarchy":   "missing",
	"not-applicable":   "not-applicable",
	"unknown-hardware": "unknown",
	"not-tested":       "not-tested",
}

// expectedGap reports whether a failed check is one the catalogue expects on
// this Mac: support that is not there yet, or hardware it does not have.
func expectedGap(status, outcome string) bool {
	if status != "fail" {
		return false
	}
	switch outcome {
	case "not-in-aurora", "not-in-asahi", "not-in-omarchy", "not-applicable":
		return true
	}
	return false
}

// reportPage is what a report's page shows: the machine, and each check with
// its result in the catalogue's words.
func (s *Server) reportPage(report *reports.Report) reportPage {
	doc := report.Document()
	text := func(path ...string) string { return words(member(doc, path...)) }
	p := reportPage{
		PublicID: report.PublicID, Model: text("machine", "model"),
		Chip: text("machine", "chip"), SoC: text("machine", "soc"), Board: text("machine", "board"),
		Storage: text("machine", "storage"), Kernel: text("machine", "kernel"),
		Distro: text("system", "distro"), BootLoader: text("system", "boot_loader"), Encryption: text("system", "encryption"),
		ToolName: text("tool", "name"), ToolVersion: text("tool", "version"), CatalogueVersion: text("catalogue_version"),
		Uploaded: uploadedAt(report), Unattended: member(doc, "unattended") == true, Verified: report.Verified,
	}
	p.Title = strings.TrimPrefix(p.Model, "Apple ") + " report"
	packages, _ := member(doc, "system", "packages").([]any)
	for _, pkg := range packages {
		p.Packages = append(p.Packages, reportPackage{words(member(pkg, "name")), words(member(pkg, "version"))})
	}
	checks, _ := member(doc, "checks").([]any)
	sections := map[string]*reportSection{}
	for _, check := range checks {
		c := reportCheck{ID: words(member(check, "id"))}
		status := words(member(check, "status"))
		c.Outcome = words(member(check, "classification", "outcome"))
		c.StatusClass, c.StatusLabel = status, strings.ToUpper(status)
		switch {
		case expectedGap(status, c.Outcome) && c.Outcome == "not-applicable":
			c.StatusClass, c.StatusLabel = "gap", "N/A"
			p.Gap++
		case expectedGap(status, c.Outcome):
			c.StatusClass, c.StatusLabel = "gap", "GAP"
			p.Gap++
		case status == "pass":
			p.Pass++
		case status == "fail":
			p.Fail++
		case status == "skip":
			p.Skip++
		}
		if _, classified := member(check, "classification").(*reports.Object); classified {
			c.Classified = true
			c.Unconfirmed = member(check, "answered_by_default") == true
			c.OutcomeWords = s.catalogue.OutcomeWords(c.Outcome)
			if c.State = outcomeStates[c.Outcome]; c.State == "" {
				c.State = "unknown"
			}
			c.Feature = s.catalogue.FeatureName(words(member(check, "classification", "feature")))
			c.Layer = words(member(check, "classification", "layer"))
		}
		evidence, _ := member(check, "evidence").([]any)
		lines := make([]string, 0, len(evidence))
		for _, line := range evidence {
			lines = append(lines, words(line))
		}
		c.Evidence = strings.Join(lines, "\n")
		name, _, _ := strings.Cut(c.ID, ".")
		section := sections[name]
		if section == nil {
			section = &reportSection{Name: name}
			sections[name] = section
			p.Sections = append(p.Sections, section)
		}
		section.Checks = append(section.Checks, c)
	}
	return p
}

// loadCatalogue is the feature catalogue the pages word results with.
func loadCatalogue() (*reports.Catalogue, error) {
	return reports.LoadCatalogue(catalogue.JSON)
}
