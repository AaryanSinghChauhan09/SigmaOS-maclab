package reports

import "sort"

// Matrix is the compatibility matrix: one row per Mac model, Omarchy
// stack/version and build, one cell per catalogue feature some check tests,
// from the published runs. ByRelease merges a release's builds into one row
// ("converged 4.0.0"); ByModel merges releases too, with each machine's
// latest actual check results per stack.
type Matrix struct {
	pub          *Published
	runs         []*Run
	by           Grouping
	onlyVerified bool
	filter       *Filter

	all      []*Row
	allDone  bool
	rows     []*Row
	rowsDone bool
	features []*Object
}

// Row is one row of the matrix. Runs are its runs, newest first.
type Row struct {
	Configuration        Configuration
	ModelName, Chip, SoC string
	Runs                 []*Run

	cells map[string]*Cell
}

// Cell is the row's cell for a feature.
func (r *Row) Cell(feature string) *Cell {
	if cell := r.cells[feature]; cell != nil {
		return cell
	}
	return &Cell{}
}

// Matrix is the matrix of the published runs the filter's run filters pass,
// with its result filters applied: confirmed cells only, one layer's
// columns, the rows and columns with a state. A nil filter passes everything.
func (p *Published) Matrix(by Grouping, filter *Filter) *Matrix {
	if filter == nil {
		filter = p.Filter(nil, nil)
	}
	return p.MatrixOf(filter.Runs(p.runs), by, filter, false)
}

// MatrixOf is the matrix of runs already chosen. onlyVerified counts cells
// from Verified runs only (a candidate set's view).
func (p *Published) MatrixOf(runs []*Run, by Grouping, filter *Filter, onlyVerified bool) *Matrix {
	if filter == nil {
		filter = p.Filter(nil, nil)
	}
	sorted := append([]*Run(nil), runs...)
	sortRuns(sorted)
	return &Matrix{pub: p, runs: sorted, by: by, onlyVerified: onlyVerified, filter: filter}
}

// Runs are the matrix's runs, newest first.
func (m *Matrix) Runs() []*Run { return newestFirst(m.runs) }

// RunsCount is the runs behind the rows shown (fewer than Runs when result filters leave rows out).
func (m *Matrix) RunsCount() int {
	count := 0
	for _, row := range m.Rows() {
		count += len(row.Runs)
	}
	return count
}

func (m *Matrix) Empty() bool { return len(m.runs) == 0 }

func id(feature *Object) string    { return text(member(feature, "id")) }
func layer(feature *Object) string { return text(member(feature, "layer")) }

// Features are the columns: the catalogue's tested features, of the filter's
// layer, and with its state in some row.
func (m *Matrix) Features() []*Object {
	if m.features != nil {
		return m.features
	}
	m.features = []*Object{}
	for _, feature := range m.pub.cat.TestedFeatures() {
		if l := m.filter.Layer(); l != "" && layer(feature) != l {
			continue
		}
		if m.filter.State() != "" {
			found := false
			for _, row := range m.allRows() {
				found = found || m.inState(row.Cell(id(feature)))
			}
			if !found {
				continue
			}
		}
		m.features = append(m.features, feature)
	}
	return m.features
}

// Rows are the rows: with confirmed only, those with a confirmed cell; with
// a state, those with a cell in it.
func (m *Matrix) Rows() []*Row {
	if m.rowsDone {
		return m.rows
	}
	m.rowsDone = true
	for _, row := range m.allRows() {
		confirmed, inState := false, false
		for _, feature := range m.Features() {
			cell := row.Cell(id(feature))
			confirmed = confirmed || (!cell.Hidden() && len(cell.Tallies) > 0)
			inState = inState || m.inState(cell)
		}
		if (!m.filter.Confirmed() || confirmed) && (m.filter.State() == "" || inState) {
			m.rows = append(m.rows, row)
		}
	}
	return m.rows
}

func (m *Matrix) RowsForBoard(board string) []*Row {
	var rows []*Row
	for _, row := range m.Rows() {
		if row.Configuration.Board == board {
			rows = append(rows, row)
		}
	}
	return rows
}

// RowsForFeature are the rows with at least one run that tested the feature.
func (m *Matrix) RowsForFeature(feature string) []*Row {
	var rows []*Row
	for _, row := range m.Rows() {
		if len(row.Cell(feature).Runs) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// History groups a matrix's rows by model and stack: the builds behind each
// row of a model summary.
type History map[[2]string][]*Row

// History is the rows by board and stack.
func (m *Matrix) History() History {
	history := History{}
	for _, row := range m.Rows() {
		key := [2]string{row.Configuration.Board, row.Configuration.Stack}
		history[key] = append(history[key], row)
	}
	return history
}

// Of are the rows behind a summary row.
func (h History) Of(row *Row) []*Row {
	return h[[2]string{row.Configuration.Board, row.Configuration.Stack}]
}

// allRows is every row of the runs, each cell counted from them (with
// confirmed only, unconfirmed cells hidden).
func (m *Matrix) allRows() []*Row {
	if m.allDone {
		return m.all
	}
	m.allDone = true
	var order []Configuration
	groups := map[Configuration][]*Run{}
	for _, run := range m.runs {
		configuration := run.Configuration(m.by)
		if _, seen := groups[configuration]; !seen {
			order = append(order, configuration)
		}
		groups[configuration] = append(groups[configuration], run)
	}
	for _, configuration := range order {
		runs := groups[configuration]
		latest := runs[len(runs)-1]
		row := &Row{Configuration: configuration, ModelName: latest.ShortModel(), Chip: latest.Chip(), SoC: latest.SoC(),
			Runs: newestFirst(runs), cells: map[string]*Cell{}}
		for _, feature := range m.pub.cat.TestedFeatures() {
			var cell *Cell
			if m.by == ByModel {
				cell = cellFromChecks(runs, id(feature), m.onlyVerified)
			} else {
				cell = cellFrom(runs, id(feature), m.onlyVerified)
			}
			if m.filter.Confirmed() {
				cell = cell.confirmedOnly()
			}
			row.cells[id(feature)] = cell
		}
		m.all = append(m.all, row)
	}
	// By chip generation, model and stack; within those, newest version first, then the newest build.
	sort.SliceStable(m.all, func(i, j int) bool { return m.compare(m.all[i], m.all[j]) < 0 })
	return m.all
}

func (m *Matrix) inState(cell *Cell) bool {
	if cell.Hidden() {
		return false
	}
	for _, state := range filterStates[m.filter.State()] {
		if state == cell.DisplayState() {
			return true
		}
	}
	return false
}

func (m *Matrix) compare(a, b *Row) int {
	if c := cmpInt(m.pub.cat.chipRank(a.SoC), m.pub.cat.chipRank(b.SoC)); c != 0 {
		return c
	}
	if a.ModelName != b.ModelName {
		if a.ModelName < b.ModelName {
			return -1
		}
		return 1
	}
	if c := cmpInt(stackRank(a.Configuration.Stack), stackRank(b.Configuration.Stack)); c != 0 {
		return c
	}
	if c := newGemVersion(b.Configuration.Version).compare(newGemVersion(a.Configuration.Version)); c != 0 {
		return c
	}
	switch {
	case b.Runs[0].before(a.Runs[0]):
		return -1
	case a.Runs[0].before(b.Runs[0]):
		return 1
	}
	return 0
}

func stackRank(stack string) int {
	for i, known := range Stacks {
		if known == stack {
			return i
		}
	}
	return len(Stacks)
}

// chipRank is where a SoC's chip generation comes in the catalogue; a SoC it
// doesn't know comes last.
func (c *Catalogue) chipRank(soc string) int {
	return c.generationRank(c.ChipForSoC(soc))
}

func (c *Catalogue) generationRank(chip string) int {
	chips := c.Chips()
	if chips == nil {
		return 0
	}
	for i, key := range chips.keys {
		if key == chip {
			return i
		}
	}
	return len(chips.keys)
}
