package server

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/omacom/omarchy-maclab/internal/reports"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /candidates", web.Anyone, (*Server).pCandidates)
	addPage("GET /candidates/{set}", web.Anyone, (*Server).pCandidate)
}

type candidateRow struct {
	Name, Href, Verdict, Words string
	// Lab is the lab's verdict on the Release whose packages are the set's:
	// its state and date, "" when no Release with a verdict matches.
	Lab            string
	Latest         string
	Runs, Verified int
	Models         string
}

type buildRow struct {
	BuildID, Set, SetSource      string
	Label, Words, Href           string
	Runtime, Built, Kernel, Boot string
	SetHref                      string
	Latest                       string
	Runs, Verified               int
	Models, Tools                string
}

type candidatesPage struct {
	Sets   []candidateRow
	Builds []buildRow
}

// sortedUniq is what of gives for each run, each value once, sorted.
func sortedUniq(runs []*reports.Run, of func(*reports.Run) string) string {
	seen := map[string]bool{}
	var values []string
	for _, run := range runs {
		if value := of(run); value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}

func verifiedAmong(runs []*reports.Run) int {
	n := 0
	for _, run := range runs {
		if run.Verified {
			n++
		}
	}
	return n
}

// pCandidates is the candidate sets published runs name, each with how it
// stands by its Verified runs, and every build a run was on.
func (s *Server) pCandidates(w http.ResponseWriter, r *http.Request) {
	p := s.published(w)
	if p == nil {
		return
	}
	var page candidatesPage
	lab := s.labVerdicts(p)
	for _, set := range p.CandidateSets() {
		page.Sets = append(page.Sets, candidateRow{Name: set.Name, Href: s.href("/candidates/" + set.Name), Verdict: set.Verdict(), Words: set.VerdictWords(), Lab: lab[set.Name],
			Latest: uploadedAtRun(set.Latest()), Runs: len(set.Runs), Verified: len(set.VerifiedRuns()), Models: sortedUniq(set.Runs, (*reports.Run).ShortModel)})
	}
	for _, on := range reports.RunsByBuild(p.Runs()) {
		build := on.Build
		row := buildRow{BuildID: build.ID, Set: build.Set(), SetSource: build.SetSource(), Label: build.Label(), Words: build.Words(),
			Href: s.href("/reports") + reports.Query(url.Values{"build": {build.Words()}}), Built: build.Built(), Kernel: build.Kernel, Boot: build.Boot,
			Latest: uploadedAtRun(on.Runs[0]), Runs: len(on.Runs), Verified: verifiedAmong(on.Runs),
			Models: sortedUniq(on.Runs, (*reports.Run).ShortModel), Tools: sortedUniq(on.Runs, (*reports.Run).ToolVersion)}
		if build.CandidateSet != "" {
			row.Runtime = build.ID
		}
		if row.Set != "" && p.NamesSet(row.Set) {
			row.SetHref = s.href("/candidates/" + row.Set)
		}
		page.Builds = append(page.Builds, row)
	}
	s.render(w, r, "candidates/index", web.Page{Title: "Candidate sets", Nav: "candidates", Data: page})
}

type findingView struct {
	State                                           string
	Badge                                           badgeView
	Model, Label, FeatureHref, FeatureName, Summary string
}

type candidatePage struct {
	Name, Runs, VerifiedRuns string
	Verdict, Words, Lab      string
	Failures, Partials       []findingView
	Tested                   bool
	Matrix                   matrixView
	Legend                   legendView
	Reports, Matched         []runView
}

func (s *Server) findings(found []reports.Finding) []findingView {
	var list []findingView
	for _, finding := range found {
		id, state := words(member(finding.Feature, "id")), finding.Cell.State()
		list = append(list, findingView{State: state, Badge: badgeView{state, reports.StateWords(state)}, Model: finding.Row.ModelName, Label: finding.Row.Configuration.Label(),
			FeatureHref: s.href("/features/" + id), FeatureName: words(member(finding.Feature, "name")), Summary: finding.Cell.Summary()})
	}
	return list
}

func newestRuns(runs []*reports.Run) []*reports.Run {
	out := make([]*reports.Run, len(runs))
	for i, run := range runs {
		out[len(runs)-1-i] = run
	}
	return out
}

// pCandidate is one candidate set: how it stands by its Verified runs, what
// they found per model and feature, and every run on it.
func (s *Server) pCandidate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("set")
	if !reports.CandidateSetName.MatchString(name) {
		s.notFound(w, r)
		return
	}
	// A set nothing names is not found before every report is read.
	if runs, err := s.reports.OnCandidateSet(name); err != nil {
		s.pageFailed(w, err)
		return
	} else if len(runs) == 0 {
		s.notFound(w, r)
		return
	}
	p := s.published(w)
	if p == nil {
		return
	}
	set := p.CandidateSet(name)
	if set == nil {
		s.notFound(w, r)
		return
	}
	tested := set.TestedRows()
	page := candidatePage{Name: set.Name, Runs: reports.Plural(len(set.Runs), "run"), VerifiedRuns: reports.Plural(len(set.VerifiedRuns()), "Verified run"),
		Verdict: set.Verdict(), Words: set.VerdictWords(), Lab: s.labVerdicts(p)[set.Name], Failures: s.findings(set.Failures()), Partials: s.findings(set.Partials()), Tested: len(tested) > 0,
		Legend: legend(), Reports: s.runList(p, newestRuns(set.Runs), false), Matched: s.runList(p, newestRuns(set.MatchedBy()), false)}
	if page.Tested {
		page.Matrix = s.matrixOf(set.Matrix().Features(), tested)
	}
	s.render(w, r, "candidates/show", web.Page{Title: "Candidate set " + set.Name, Nav: "candidates", Data: page})
}
