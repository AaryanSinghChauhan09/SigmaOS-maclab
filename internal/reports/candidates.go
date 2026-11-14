package reports

import (
	"fmt"
	"regexp"
	"sort"
)

// CandidateSet is the runs on one candidate set's images (the report's
// system.candidate_set, from the image's target record), and how the set
// stands: Verified runs only, per Mac model and feature. Community runs on
// the set are listed but don't count.
//
//	waiting  no Verified run yet
//	blocked  Verified runs found a feature failing where it should work (a
//	         regression, or a failure with no Verified earlier pass)
//	ready    Verified runs on at least one model, and none found a failure
//
// The three words are the old site's and stay as the pages' hooks; a page
// says Waiting, Failing or Passing.
type CandidateSet struct {
	Name string
	// Runs are the published runs whose image names the set, oldest first.
	Runs []*Run

	pub    *Published
	matrix *Matrix
}

// CandidateSetName is the shape of a set's name.
var CandidateSetName = regexp.MustCompile(`\A[A-Za-z0-9._-]{1,128}\z`)

// Finding is one feature on one row of a set's matrix.
type Finding struct {
	Row     *Row
	Feature *Object
	Cell    *Cell
}

// CandidateSets are the sets published runs name, the one with the latest run first.
func (p *Published) CandidateSets() []*CandidateSet {
	byName := map[string]*CandidateSet{}
	var sets []*CandidateSet
	for _, run := range p.runs {
		name := run.CandidateSet()
		if name == "" {
			continue
		}
		set := byName[name]
		if set == nil {
			set = &CandidateSet{Name: name, pub: p}
			byName[name] = set
			sets = append(sets, set)
		}
		set.Runs = append(set.Runs, run)
	}
	sort.SliceStable(sets, func(i, j int) bool {
		if a, b := sets[i].Latest().CreatedAt.Unix(), sets[j].Latest().CreatedAt.Unix(); a != b {
			return a > b
		}
		return sets[i].Name < sets[j].Name
	})
	return sets
}

// CandidateSet is the set with that name, or nil when no published run names it.
func (p *Published) CandidateSet(name string) *CandidateSet {
	set := &CandidateSet{Name: name, pub: p}
	for _, run := range p.runs {
		if name != "" && run.CandidateSet() == name {
			set.Runs = append(set.Runs, run)
		}
	}
	if len(set.Runs) == 0 {
		return nil
	}
	return set
}

// MatchedBy are the published runs whose image names no set but whose
// omarchy package is this set's: listed, never counted.
func (s *CandidateSet) MatchedBy() []*Run {
	var matched []*Run
	for _, run := range s.pub.runs {
		if build := run.Build(); build != nil && build.MatchedSet() == s.Name {
			matched = append(matched, run)
		}
	}
	return matched
}

func (s *CandidateSet) VerifiedRuns() []*Run {
	var verified []*Run
	for _, run := range s.Runs {
		if run.Verified {
			verified = append(verified, run)
		}
	}
	return verified
}

// Latest is the set's latest run.
func (s *CandidateSet) Latest() *Run {
	latest := s.Runs[0]
	for _, run := range s.Runs {
		if run.CreatedAt.After(latest.CreatedAt) {
			latest = run
		}
	}
	return latest
}

// Matrix is the set's runs as a matrix that counts Verified runs only.
func (s *CandidateSet) Matrix() *Matrix {
	if s.matrix == nil {
		s.matrix = s.pub.MatrixOf(s.Runs, ByBuild, nil, true)
	}
	return s.matrix
}

// TestedRows are the rows Verified runs tested.
func (s *CandidateSet) TestedRows() []*Row {
	var rows []*Row
	for _, row := range s.Matrix().Rows() {
		for _, feature := range s.pub.cat.TestedFeatures() {
			if row.Cell(id(feature)).Verified() {
				rows = append(rows, row)
				break
			}
		}
	}
	return rows
}

func (s *CandidateSet) findings(state string) []Finding {
	var found []Finding
	for _, row := range s.Matrix().Rows() {
		for _, feature := range s.Matrix().Features() {
			if cell := row.Cell(id(feature)); cell.Verified() && cell.State() == state {
				found = append(found, Finding{row, feature, cell})
			}
		}
	}
	return found
}

func (s *CandidateSet) Regressions() []Finding { return s.findings("regression") }
func (s *CandidateSet) Failures() []Finding    { return append(s.Regressions(), s.findings("fails")...) }
func (s *CandidateSet) Partials() []Finding    { return s.findings("partial") }

// Verdict is "waiting", "blocked" or "ready". Verified runs that counted
// nothing (every answer a bare Enter) leave it waiting.
func (s *CandidateSet) Verdict() string {
	switch {
	case len(s.VerifiedRuns()) == 0 || len(s.TestedRows()) == 0:
		return "waiting"
	case len(s.Failures()) > 0:
		return "blocked"
	}
	return "ready"
}

// VerdictWords is the Candidates page's own line for the set.
func (s *CandidateSet) VerdictWords() string {
	switch s.Verdict() {
	case "waiting":
		return "Waiting"
	case "blocked":
		return fmt.Sprintf("Failing: %s, %s", Plural(len(s.Failures()), "failure"), Plural(len(s.Regressions()), "regression"))
	}
	models := map[string]bool{}
	for _, row := range s.TestedRows() {
		models[row.ModelName] = true
	}
	return fmt.Sprintf("Passing: Verified runs on %s, nothing failing", Plural(len(models), "model"))
}

// The old site's four queries on a report's JSON run on SQLite as they were
// written for Postgres. The pages count from the published reports read
// once (Published); these answer the same from reports.db alone.
const (
	whereNamesASet       = `(body -> 'system' ->> 'candidate_set') IS NOT NULL`
	selectSetName        = `body -> 'system' ->> 'candidate_set'`
	whereOnSet           = `body -> 'system' ->> 'candidate_set' = ?`
	whereOnBoardAndStack = `body -> 'machine' ->> 'board' = ? AND body -> 'system' ->> 'stack' = ?`
)

func (s *Store) publicIDs(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// NamingASet are the published reports whose image names a candidate set, oldest first.
func (s *Store) NamingASet() ([]string, error) {
	return s.publicIDs(`SELECT public_id FROM reports WHERE hidden_at IS NULL AND ` + whereNamesASet + ` ORDER BY created_at, id`)
}

// CandidateSetNames are the candidate sets published reports name.
func (s *Store) CandidateSetNames() ([]string, error) {
	return s.publicIDs(`SELECT DISTINCT ` + selectSetName + ` FROM reports WHERE hidden_at IS NULL AND ` + whereNamesASet)
}

// OnCandidateSet are the published reports whose image names that set, oldest first.
func (s *Store) OnCandidateSet(name string) ([]string, error) {
	return s.publicIDs(`SELECT public_id FROM reports WHERE hidden_at IS NULL AND `+whereOnSet+` ORDER BY created_at, id`, name)
}

// VerifiedOn are the published Verified runs on one board and stack, oldest
// first: where a regression's earlier pass comes from.
func (s *Store) VerifiedOn(board, stack string) ([]string, error) {
	return s.publicIDs(`SELECT public_id FROM reports WHERE hidden_at IS NULL AND verified AND `+whereOnBoardAndStack+` ORDER BY created_at, id`, board, stack)
}
