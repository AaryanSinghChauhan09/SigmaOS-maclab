package reports

import (
	"sort"
	"strings"
	"time"
)

// Lineup is the bring-up table of the Models page: every Mac in the
// catalogue's boards, with or without runs, so the Macs that still need
// testing or bringing online show. Each row is a group of boards bought as
// one Mac (the M3 Air's J613 and J615) with its GPU and built-in display
// state, taken from the published runs on those boards: each machine's
// latest actual outcomes, Verified runs first, Community runs once machines
// agree. An untested Mac shows what the catalogue expects from Asahi and
// Aurora instead. Boards with runs that the catalogue doesn't list are added
// from the runs.
type Lineup struct {
	pub  *Published
	runs map[string][]*Run
	seen []string
	rows []*LineupRow
}

// LineupAreas are the areas of the table and the catalogue feature each shows.
var LineupAreas = []struct{ Area, Feature string }{{"gpu", "gpu"}, {"display", "dcp"}}

// LineupStatus is a row's state for one area, in the lab's colour code: from
// the runs (Cell) when any board was tested, else from the catalogue (Note).
type LineupStatus struct {
	State, Source string
	Cell          *Cell
	Note          string
}

func (s LineupStatus) Tested() bool      { return s.Source == "runs" }
func (s LineupStatus) Unconfirmed() bool { return s.Tested() && s.Cell.Unconfirmed() }

func (s LineupStatus) Words() string {
	if s.Tested() {
		return LineupWords(s.State)
	}
	return "catalogue: " + LineupWords(s.State)
}

// LineupWords are a state's words in the bring-up table.
func LineupWords(state string) string {
	if state == "missing" {
		return "not supported yet"
	}
	return StateWords(state)
}

// LineupBoard is one board's runs, oldest first, and what they found per area.
type LineupBoard struct {
	Board   string
	Runs    []*Run
	results map[string]*Cell
}

func (b *LineupBoard) Tested() bool { return len(b.Runs) > 0 }

// VerifiedRuns is how many of the board's runs are Verified runs.
func (b *LineupBoard) VerifiedRuns() int {
	count := 0
	for _, run := range b.Runs {
		if run.Verified {
			count++
		}
	}
	return count
}

func (b *LineupBoard) LastTestedAt() time.Time  { return b.Runs[len(b.Runs)-1].CreatedAt }
func (b *LineupBoard) Result(area string) *Cell { return b.results[area] }

type LineupRow struct {
	Name, Chip string
	Boards     []*LineupBoard
	statuses   map[string]LineupStatus
}

func (r *LineupRow) Status(area string) LineupStatus { return r.statuses[area] }

func (r *LineupRow) Tested() bool {
	for _, board := range r.Boards {
		if board.Tested() {
			return true
		}
	}
	return false
}

// LineupModel is one Mac of the catalogue: the boards sold under one name.
type LineupModel struct {
	Name, Chip string
	Boards     []string
}

// LineupModels are the catalogue's boards grouped by lineup, in the catalogue's order.
func (c *Catalogue) LineupModels() []LineupModel {
	var models []LineupModel
	at := map[string]int{}
	boards := c.Boards()
	if boards == nil {
		return nil
	}
	for _, board := range boards.keys {
		name := text(member(boards, board, "lineup"))
		i, seen := at[name]
		if !seen {
			i = len(models)
			at[name] = i
			models = append(models, LineupModel{Name: name, Chip: text(member(boards, board, "chip"))})
		}
		models[i].Boards = append(models[i].Boards, board)
	}
	return models
}

// Lineup is the bring-up table over all the published runs.
func (p *Published) Lineup() *Lineup {
	l := &Lineup{pub: p, runs: map[string][]*Run{}}
	for _, run := range p.runs {
		if _, seen := l.runs[run.Board()]; !seen {
			l.seen = append(l.seen, run.Board())
		}
		l.runs[run.Board()] = append(l.runs[run.Board()], run)
	}
	return l
}

func (l *Lineup) Rows() []*LineupRow {
	if l.rows != nil {
		return l.rows
	}
	known := map[string]bool{}
	for _, model := range l.pub.cat.LineupModels() {
		l.rows = append(l.rows, l.row(model.Name, model.Chip, model.Boards))
		for _, board := range model.Boards {
			known[board] = true
		}
	}
	var unlisted []*LineupRow
	for _, board := range l.seen {
		if !known[board] {
			latest := l.runs[board][len(l.runs[board])-1]
			unlisted = append(unlisted, l.row(latest.ShortModel(), l.pub.cat.ChipForSoC(latest.SoC()), []string{board}))
		}
	}
	sort.SliceStable(unlisted, func(i, j int) bool {
		a, b := unlisted[i], unlisted[j]
		if x, y := l.pub.cat.generationRank(a.Chip), l.pub.cat.generationRank(b.Chip); x != y {
			return x < y
		}
		return a.Name < b.Name
	})
	l.rows = append(l.rows, unlisted...)
	return l.rows
}

func (l *Lineup) Boards() []*LineupBoard {
	var boards []*LineupBoard
	for _, row := range l.Rows() {
		boards = append(boards, row.Boards...)
	}
	return boards
}

func (l *Lineup) UntestedBoards() []*LineupBoard {
	var boards []*LineupBoard
	for _, board := range l.Boards() {
		if !board.Tested() {
			boards = append(boards, board)
		}
	}
	return boards
}

func (l *Lineup) row(name, chip string, boards []string) *LineupRow {
	row := &LineupRow{Name: name, Chip: chip, statuses: map[string]LineupStatus{}}
	var all []*Run
	for _, board := range boards {
		b := &LineupBoard{Board: board, Runs: l.runs[board], results: map[string]*Cell{}}
		for _, area := range LineupAreas {
			b.results[area.Area] = cellFromChecks(b.Runs, area.Feature, false)
		}
		row.Boards = append(row.Boards, b)
		all = append(all, b.Runs...)
	}
	sortRuns(all)
	first := ""
	if len(boards) > 0 {
		first = boards[0]
	}
	for _, area := range LineupAreas {
		// What the runs on the row's boards found, or the catalogue's view when none tested the feature.
		if cell := cellFromChecks(all, area.Feature, false); len(cell.Tallies) > 0 {
			row.statuses[area.Area] = LineupStatus{State: cell.DisplayState(), Source: "runs", Cell: cell}
		} else {
			row.statuses[area.Area] = l.expected(area.Feature, chip, first)
		}
	}
	return row
}

// expected is the catalogue's view: Aurora supports it, or matches Asahi where Asahi has it.
func (l *Lineup) expected(feature, chip, board string) LineupStatus {
	states := l.pub.cat.Expected(l.pub.cat.Feature(feature), chip, board)
	if states == nil {
		return LineupStatus{State: "unknown", Source: "catalogue", Note: "not in the catalogue"}
	}
	asahi, aurora := text(member(states, "asahi", "status")), text(member(states, "aurora", "status"))
	state := "missing"
	switch {
	case asahi == "absent":
		state = "not-applicable"
	case aurora == "supported" || (aurora == "asahi" && asahiSupports(asahi)):
		state = "works"
	}
	status := LineupStatus{State: state, Source: "catalogue"}
	switch {
	case state == "not-applicable":
	case aurora == "supported":
		status.Note = strings.TrimSpace("Aurora " + text(member(states, "aurora", "version")))
	default:
		status.Note = "Asahi: " + text(member(states, "asahi", "cell"))
		if _, ok := member(states, "asahi", "cell").(string); !ok {
			status.Note = "Asahi: " + asahi
		}
	}
	return status
}

func asahiSupports(status string) bool {
	for _, supported := range AsahiSupported {
		if supported == status {
			return true
		}
	}
	return false
}
