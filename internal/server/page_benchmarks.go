package server

import (
	"net/http"
	"strings"

	"github.com/omacom/omarchy-maclab/internal/reports"
	"github.com/omacom/omarchy-maclab/internal/web"
)

func init() {
	addPage("GET /benchmarks", web.Anyone, (*Server).pBenchmarks)
}

// scoreRowView is one row of a benchmark's table: a Mac model on a build,
// its score as a bar against the best Mac on the same benchmark.
type scoreRowView struct {
	Benchmark, BenchmarkHref, BenchmarkName, Suite string
	Board, Stack, Version, BuildID                 string
	ModelHref, Model, Chip                         string
	Capacity, Label                                string
	Share, Score, ScoreHref, Range, Unit           string
	MacOS, MacOSConditions, MacOSGap               string
	Machines                                       int
	Runs                                           string
	Verified                                       bool
	Tools                                          string
}

type benchmarkView struct {
	ID, Title, How, Unit string
	ByCapacity, MacOS    bool
	Rows                 []scoreRowView
}

type benchmarksPage struct {
	Benchmarks []benchmarkView
}

func (s *Server) scoreRow(benchmark *reports.Benchmark, row *reports.ScoreRow) scoreRowView {
	c := row.Configuration
	v := scoreRowView{Benchmark: benchmark.ID(), BenchmarkHref: s.href("/benchmarks") + "#benchmark-" + benchmark.ID(), BenchmarkName: benchmark.Name(), Suite: benchmark.Suite,
		Board: c.Board, Stack: c.Stack, Version: c.Version, BuildID: c.BuildID, ModelHref: s.href("/models/" + c.Board), Model: row.ModelName, Chip: row.Chip,
		Capacity: row.Capacity, Label: c.Label(), Share: reports.Share(row.Median(), benchmark.Best()), Score: reports.ScoreWords(row.Median()),
		ScoreHref: s.href(reportPath(row.Latest().Run)), Unit: benchmark.UnitWords(), Machines: row.Machines(), Verified: row.Verified(), Tools: strings.Join(row.Tools(), ", ")}
	if v.Capacity == "" {
		v.Capacity = "unknown"
	}
	if row.Machines() > 1 {
		v.Range = reports.ScoreWords(row.Low()) + "–" + reports.ScoreWords(row.High())
	}
	if row.Runs() > row.Machines() {
		v.Runs = reports.Plural(row.Runs(), "run")
	}
	if macos := row.MacOS(benchmark.CheckID); macos != nil {
		v.MacOS, v.MacOSConditions = reports.ScoreWords(macos), row.MacOSConditions()
	} else if v.MacOSGap = row.MacOSGap(benchmark.CheckID); v.MacOSGap == "" {
		v.MacOSGap = "–"
	}
	return v
}

// pBenchmarks is each benchmark's scores, compared per Mac model and build.
func (s *Server) pBenchmarks(w http.ResponseWriter, r *http.Request) {
	p := s.published(w)
	if p == nil {
		return
	}
	var page benchmarksPage
	for _, benchmark := range p.Benchmarks(s.macOSReference).All() {
		v := benchmarkView{ID: benchmark.ID(), Title: strings.ToLower(benchmark.Name()) + " · suite " + benchmark.Suite, How: benchmark.How(), Unit: benchmark.UnitWords(),
			ByCapacity: benchmark.ByCapacity(), MacOS: benchmark.MacOS()}
		for _, row := range benchmark.Rows {
			v.Rows = append(v.Rows, s.scoreRow(benchmark, row))
		}
		page.Benchmarks = append(page.Benchmarks, v)
	}
	s.render(w, r, "benchmarks", web.Page{Title: "Benchmarks", Nav: "benchmarks", Data: page})
}

// boardScores are a Mac model's scores, each against the best Mac on the same benchmark.
func (s *Server) boardScores(p *reports.Published, board string) []scoreRowView {
	var rows []scoreRowView
	for _, benchmark := range p.Benchmarks(s.macOSReference).ForBoard(board) {
		for _, row := range benchmark.RowsForBoard(board) {
			rows = append(rows, s.scoreRow(benchmark, row))
		}
	}
	return rows
}
