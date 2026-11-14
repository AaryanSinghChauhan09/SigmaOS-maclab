package reports

import (
	"errors"
	"time"
)

// A report is Published, Hidden or deleted. Hide and Show are a
// Maintainer's: a Hidden report keeps its row and its evidence, and nothing
// the public reads counts it.

// Hide hides the report with that public id, at at. One that is hidden
// already stays hidden since the first time.
func (s *Store) Hide(publicID string, at time.Time) error {
	return s.setHidden(`UPDATE reports SET hidden_at = coalesce(hidden_at, ?1), updated_at = CASE WHEN hidden_at IS NULL THEN ?1 ELSE updated_at END WHERE public_id = ?2`, publicID, at)
}

// Show publishes a Hidden report again.
func (s *Store) Show(publicID string, at time.Time) error {
	return s.setHidden(`UPDATE reports SET updated_at = CASE WHEN hidden_at IS NULL THEN updated_at ELSE ?1 END, hidden_at = NULL WHERE public_id = ?2`, publicID, at)
}

func (s *Store) setHidden(update, publicID string, at time.Time) error {
	res, err := s.db.Exec(update, at.UnixMicro(), publicID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNoReport
	}
	return nil
}

// Every reads every report, Hidden ones too, oldest first: what a
// Maintainer lists. Nothing the public reads is counted from it.
func (s *Store) Every(cat *Catalogue) (*Published, error) {
	list, err := s.reportsWhere(`1`)
	if err != nil {
		return nil, err
	}
	p := NewPublished(cat, nil)
	for _, report := range list {
		p.runs = append(p.runs, &Run{Report: report, doc: report.Document(), pub: p})
	}
	sortRuns(p.runs)
	return p, nil
}

// AroundAny is Around for a Maintainer, who reads a Hidden report's page
// too. What is said of the report still counts published reports alone.
func (s *Store) AroundAny(cat *Catalogue, publicID string) (*Published, *Run, error) {
	report, err := s.Report(publicID)
	if errors.Is(err, ErrNoReport) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := s.around(cat, report)
	if err != nil {
		return nil, nil, err
	}
	if report.Hidden {
		run := &Run{Report: report, doc: report.Document(), pub: p}
		p.runs = append(p.runs, run)
		sortRuns(p.runs)
	}
	return p, p.Run(publicID), nil
}
