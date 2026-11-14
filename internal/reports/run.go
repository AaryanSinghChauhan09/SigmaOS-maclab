package reports

import (
	"math/big"
	"sort"
	"strings"
	"sync"
)

// Published is the published reports as the public pages count them: every
// report that is not hidden, oldest upload first, counted for one request.
// It needs reports.db and the catalogue, nothing of labd.db.
type Published struct {
	cat    *Catalogue
	runs   []*Run
	passes map[[2]string]map[string][]*Run
	sets   map[string]bool
}

// kept is the published reports as last read from reports.db, each with its
// body read, for as long as nothing changes the file. Reading them is the
// costly part of a public page, and anybody can ask for one.
type kept struct {
	mu      sync.Mutex
	fresh   bool
	version int64
	reports []*Report
	docs    []*Object
	loads   int
}

// Published is the published reports for one request. The reports are read
// again only after something changed reports.db; what a request counts from
// them is its own.
func (s *Store) Published(cat *Catalogue) (*Published, error) {
	k := &s.kept
	k.mu.Lock()
	defer k.mu.Unlock()
	// Asked before the read: a change that lands between the two is read now
	// and read once more at the next request.
	version, verr := s.db.DataVersion()
	if verr != nil || !k.fresh || version != k.version {
		reports, err := s.publishedWhere(`1`)
		if err != nil {
			return nil, err
		}
		k.reports, k.docs = reports, make([]*Object, len(reports))
		for i, report := range reports {
			k.docs[i] = report.Document()
		}
		k.fresh, k.version = verr == nil, version
		k.loads++
	}
	return newPublished(cat, k.reports, k.docs), nil
}

// publishedWhere reads the published reports a condition picks, oldest first.
func (s *Store) publishedWhere(where string, args ...any) ([]*Report, error) {
	return s.reportsWhere(`hidden_at IS NULL AND `+where, args...)
}

// reportsWhere reads the reports a condition picks, Hidden or not, oldest first.
func (s *Store) reportsWhere(where string, args ...any) ([]*Report, error) {
	rows, err := s.db.Query(`SELECT `+reportColumns+` FROM reports WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Report
	for rows.Next() {
		report, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, report)
	}
	return list, rows.Err()
}

// NewPublished counts the given reports; hidden ones are left out.
func NewPublished(cat *Catalogue, list []*Report) *Published {
	var reports []*Report
	var docs []*Object
	for _, report := range list {
		if !report.Hidden {
			reports, docs = append(reports, report), append(docs, report.Document())
		}
	}
	return newPublished(cat, reports, docs)
}

// newPublished counts reports whose bodies were read into docs. It changes
// neither.
func newPublished(cat *Catalogue, reports []*Report, docs []*Object) *Published {
	p := &Published{cat: cat, passes: map[[2]string]map[string][]*Run{}}
	for i, report := range reports {
		p.runs = append(p.runs, &Run{Report: report, doc: docs[i], pub: p})
	}
	sortRuns(p.runs)
	return p
}

func (p *Published) Catalogue() *Catalogue { return p.cat }

// Runs are the published runs, oldest upload first.
func (p *Published) Runs() []*Run { return p.runs }

// Run is the published run with that public id, or nil.
func (p *Published) Run(publicID string) *Run {
	for _, run := range p.runs {
		if run.PublicID == publicID {
			return run
		}
	}
	return nil
}

// NamesSet reports whether some published run's image names that candidate set.
func (p *Published) NamesSet(name string) bool {
	if p.sets == nil {
		p.sets = map[string]bool{}
		for _, run := range p.runs {
			if set := run.CandidateSet(); set != "" {
				p.sets[set] = true
			}
		}
	}
	return p.sets[name]
}

// Run is one published report, read: the Mac, the system and each check.
type Run struct {
	*Report
	doc *Object
	pub *Published

	checks    []*Check
	read      bool
	build     *Build
	built     bool
	tested    map[string]string
	states    map[string]string
	testedIDs []string
}

// Check is one check of a run.
type Check struct {
	ID, Status string
	// Outcome, Feature and Layer are the check's classification.
	Outcome, Feature, Layer string
	// AnsweredByDefault is a human check answered yes by a bare Enter, not a
	// typed y: shown as "works (unconfirmed)" and never counted towards a
	// feature's state.
	AnsweredByDefault bool

	raw *Object
}

// Kind is how the check was run: "automatic" or "human".
func (c *Check) Kind() string { return text(member(c.raw, "kind")) }

// Expected is the state the catalogue expects of the check's feature in a layer, "" for none.
func (c *Check) Expected(layer string) string {
	return text(member(c.raw, "classification", "expected", layer))
}

// ExpectedStates are the expected states the report carries, in its order.
func (c *Check) ExpectedStates() *Object {
	expected, _ := member(c.raw, "classification", "expected").(*Object)
	return expected
}

// Classified reports whether the check carries a classification.
func (c *Check) Classified() bool {
	classification, _ := member(c.raw, "classification").(*Object)
	return classification != nil
}

// Score is a benchmark's measurement, or nil.
func (c *Check) Score() *Object {
	score, _ := member(c.raw, "score").(*Object)
	return score
}

// Evidence is the check's evidence, line by line.
func (c *Check) Evidence() []string {
	lines, _ := member(c.raw, "evidence").([]any)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, rubyText(line))
	}
	return out
}

// State is the result state the check's outcome counts as in its run: a
// failure is a regression where an earlier Verified run found the feature working.
func (r *Run) CheckState(c *Check) string {
	state := StateOfOutcome(c.Outcome)
	if state == "fails" && r.Regression(c.Feature) {
		return "regression"
	}
	return state
}

// ExpectedGap reports whether the check failed as the catalogue expects on
// this Mac: support that is not there yet, or hardware it does not have.
func (c *Check) ExpectedGap() bool {
	if c.Status != "fail" {
		return false
	}
	switch c.Outcome {
	case "not-in-aurora", "not-in-asahi", "not-in-omarchy", "not-applicable":
		return true
	}
	return false
}

func text(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case *big.Int:
		return value.String()
	}
	return ""
}

func (r *Run) text(path ...string) string { return text(member(r.doc, path...)) }

// Document is the report as it was uploaded, less its signature.
func (r *Run) Document() *Object { return r.doc }

func (r *Run) Model() string      { return r.text("machine", "model") }
func (r *Run) ShortModel() string { return strings.TrimPrefix(r.Model(), "Apple ") }
func (r *Run) Board() string      { return r.text("machine", "board") }
func (r *Run) SoC() string        { return r.text("machine", "soc") }
func (r *Run) Chip() string       { return r.text("machine", "chip") }
func (r *Run) Kernel() string     { return r.text("machine", "kernel") }
func (r *Run) Stack() string      { return r.text("system", "stack") }
func (r *Run) StackWords() string { return StackWords(r.Stack()) }

// Storage is the SSD's capacity ("1TB"), "" for a report from before it was recorded.
func (r *Run) Storage() string { return r.text("machine", "storage") }

// Unattended reports whether nobody was at the Mac: only the automatic checks ran.
func (r *Run) Unattended() bool { return member(r.doc, "unattended") == true }

// ToolVersion is the omarchy-m-test version that ran it.
func (r *Run) ToolVersion() string { return r.text("tool", "version") }

// CandidateSet is the candidate set the run's image names, or "".
func (r *Run) CandidateSet() string { return r.text("system", "candidate_set") }

// Stacks are the stacks the lab knows, in the order they are shown.
var Stacks = []string{"converged", "mx-mac", "legacy-omarchy-mac", "reference"}

// StackWords is a stack as a page says it.
func StackWords(stack string) string {
	if stack == "legacy-omarchy-mac" {
		return "legacy omarchy-mac"
	}
	return stack
}

func (r *Run) packages() []any {
	packages, _ := member(r.doc, "system", "packages").([]any)
	return packages
}

// PackageVersion is the version of that package on the Mac, or "".
func (r *Run) PackageVersion(name string) string {
	for _, pkg := range r.packages() {
		if text(member(asObject(pkg), "name")) == name {
			return text(member(asObject(pkg), "version"))
		}
	}
	return ""
}

// The packages whose version is "the Omarchy version" of a run, by preference.
var omarchyPackages = []string{"omarchy", "omarchy-dev", "omarchy-mac"}

// OmarchyVersion is the release a run is on (ReleaseOf), or the distro for a
// reference run.
func (r *Run) OmarchyVersion() string {
	if r.Stack() == "reference" {
		return r.text("system", "distro")
	}
	for _, name := range omarchyPackages {
		if version := r.PackageVersion(name); version != "" {
			return ReleaseOf(version)
		}
	}
	return "unknown"
}

// Build is the build the run is on, or nil for a reference run.
func (r *Run) Build() *Build {
	if !r.built {
		r.build, r.built = buildFor(r), true
	}
	return r.build
}

// Configuration is a Mac model (board) on an Omarchy stack, release and
// build: one row of the matrix. Build is the build's words and BuildID its
// runtime build id, both "" for a reference run or when the builds of a
// release are merged; Version is "" when releases are merged too.
type Configuration struct {
	Board, Stack, Version, Build, BuildID string
}

func (c Configuration) StackWords() string { return StackWords(c.Stack) }

func (c Configuration) ReleaseLabel() string {
	if c.Version == "" {
		return c.StackWords() + " · latest results"
	}
	return c.StackWords() + " " + c.Version
}

func (c Configuration) Label() string {
	if c.Build == "" {
		return c.ReleaseLabel()
	}
	return c.ReleaseLabel() + " · build " + c.Build
}

// Grouping is what one row of the matrix merges.
type Grouping int

const (
	// ByBuild is a row per model, stack, release and build.
	ByBuild Grouping = iota
	// ByRelease merges the builds of one release.
	ByRelease
	// ByModel merges releases too, keeping stacks apart.
	ByModel
)

func (r *Run) Configuration(by Grouping) Configuration {
	c := Configuration{Board: r.Board(), Stack: r.Stack()}
	if by != ByModel {
		c.Version = r.OmarchyVersion()
	}
	if build := r.Build(); by == ByBuild && build != nil {
		c.Build, c.BuildID = build.Words(), build.ID
	}
	return c
}

// machineKey is the Machine key the report was signed with, as the lab
// keeps it. Reports from before Machine keys all count as one machine.
func (r *Run) machineKey() string {
	if r.MachineID == "" {
		return "unknown"
	}
	return r.MachineID
}

// person is who the run's Mac was signed in as when the report came, "" for nobody.
func (r *Run) person() string {
	switch {
	case r.AccountID != 0:
		return "account:" + big.NewInt(r.AccountID).String()
	case r.Handle != "":
		return "handle:" + strings.ToLower(r.Handle)
	}
	return ""
}

// machineKeys gives the machine each run counts as, for agreement between
// machines: runs signed with one Machine key, or from Macs of one board
// signed in as one person, are one machine. Reinstalling a Mac gives it a
// new key, and its runs from before mustn't outvote the ones after as a
// second Mac.
func machineKeys(runs []*Run) map[*Run]string {
	parent := map[string]string{}
	var root func(string) string
	root = func(key string) string {
		if p, ok := parent[key]; ok && p != key {
			parent[key] = root(p)
			return parent[key]
		}
		parent[key] = key
		return key
	}
	for _, run := range runs {
		if person := run.person(); person != "" {
			parent[root(run.machineKey())] = root(person + "@" + run.Board())
		}
	}
	keys := make(map[*Run]string, len(runs))
	for _, run := range runs {
		keys[run] = root(run.machineKey())
	}
	return keys
}

// MachineCount is how many machines the runs count as.
func MachineCount(runs []*Run) int {
	seen := map[string]bool{}
	for _, key := range machineKeys(runs) {
		seen[key] = true
	}
	return len(seen)
}

// Checks are the run's checks, in the report's order.
func (r *Run) Checks() []*Check {
	if !r.read {
		r.read = true
		list, _ := member(r.doc, "checks").([]any)
		for _, item := range list {
			check := asObject(item)
			r.checks = append(r.checks, &Check{
				ID: text(member(check, "id")), Status: text(member(check, "status")),
				Outcome: text(member(check, "classification", "outcome")), Feature: text(member(check, "classification", "feature")),
				Layer:             text(member(check, "classification", "layer")),
				AnsweredByDefault: member(check, "answered_by_default") == true,
				raw:               check,
			})
		}
	}
	return r.checks
}

// CountedChecks are the checks that count towards feature states: all but
// those answered by default.
func (r *Run) CountedChecks() []*Check {
	var counted []*Check
	for _, check := range r.Checks() {
		if !check.AnsweredByDefault {
			counted = append(counted, check)
		}
	}
	return counted
}

// Counts are a run's checks by result: a gap is a failure the catalogue
// expects on this Mac.
type Counts struct{ Pass, Fail, Gap, Skip int }

// ResultCounts counts the run's checks; confirmed leaves out the answers
// given by a bare Enter.
func (r *Run) ResultCounts(confirmed bool) Counts {
	checks := r.Checks()
	if confirmed {
		checks = r.CountedChecks()
	}
	var counts Counts
	for _, check := range checks {
		switch {
		case check.ExpectedGap():
			counts.Gap++
		case check.Status == "pass":
			counts.Pass++
		case check.Status == "fail":
			counts.Fail++
		case check.Status == "skip":
			counts.Skip++
		}
	}
	return counts
}

func (r *Run) readStates() {
	if r.tested != nil {
		return
	}
	outcomes := map[string][]string{}
	for _, check := range r.CountedChecks() {
		if _, seen := outcomes[check.Feature]; !seen {
			r.testedIDs = append(r.testedIDs, check.Feature)
		}
		outcomes[check.Feature] = append(outcomes[check.Feature], check.Outcome)
	}
	r.tested, r.states = map[string]string{}, map[string]string{}
	for feature, list := range outcomes {
		r.tested[feature] = CombineOutcomes(list)
	}
}

// TestedState is what the run found for a feature from its counted checks'
// outcomes alone, a failure being "fails"; "" when no check of it ran.
func (r *Run) TestedState(feature string) string {
	r.readStates()
	return r.tested[feature]
}

// FeatureState is TestedState, with a failure a "regression" where an
// earlier Verified run on the same model and stack passed.
func (r *Run) FeatureState(feature string) string {
	r.readStates()
	state, ok := r.states[feature]
	if !ok {
		state = r.tested[feature]
		if state == "fails" && r.RegressedFrom(feature) != nil {
			state = "regression"
		}
		r.states[feature] = state
	}
	return state
}

// testedFeatures are the features the run's counted checks test, in the report's order.
func (r *Run) testedFeatures() []string {
	r.readStates()
	return r.testedIDs
}

// RegressedFrom is the earlier Verified run in which a now failing feature
// worked, or nil.
func (r *Run) RegressedFrom(feature string) *Run { return r.pub.passBefore(r, feature) }

func (r *Run) Regression(feature string) bool { return r.FeatureState(feature) == "regression" }

// AuroraVersion is the Aurora kernel package's version, or "" when the run isn't on Aurora.
func (r *Run) AuroraVersion() string { return r.PackageVersion("linux-aurora") }

// before reports whether r was uploaded before other: the order runs
// happened in, as far as the lab knows.
func (r *Run) before(other *Run) bool {
	if !r.CreatedAt.Equal(other.CreatedAt) {
		return r.CreatedAt.Before(other.CreatedAt)
	}
	return r.ID < other.ID
}

// sortRuns puts runs oldest upload first.
func sortRuns(runs []*Run) {
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].before(runs[j]) })
}

func newestFirst(runs []*Run) []*Run {
	out := make([]*Run, len(runs))
	for i, run := range runs {
		out[len(runs)-1-i] = run
	}
	return out
}

// passBefore is the latest Verified pass of the feature on the run's model
// and stack uploaded before the run, or nil. Without one a failure stays
// "fails", so a setup error, or a feature that never worked on that Mac,
// isn't taken for a regression. The Omarchy version may differ: that's what
// a regression is.
func (p *Published) passBefore(run *Run, feature string) *Run {
	on := [2]string{run.Board(), run.Stack()}
	passes, ok := p.passes[on]
	if !ok {
		passes = map[string][]*Run{}
		for _, candidate := range p.runs {
			if !candidate.Verified || candidate.Board() != on[0] || candidate.Stack() != on[1] {
				continue
			}
			for _, id := range candidate.testedFeatures() {
				if candidate.TestedState(id) == "works" {
					passes[id] = append(passes[id], candidate)
				}
			}
		}
		p.passes[on] = passes
	}
	list := passes[feature]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].before(run) {
			return list[i]
		}
	}
	return nil
}

// OpenRegression is a regression still open: the newest run testing the
// feature on this model and stack is a regression. Runs are its regressed
// runs, newest first.
type OpenRegression struct {
	Feature, Board, Stack string
	Runs                  []*Run
}

func (o OpenRegression) Latest() *Run { return o.Runs[0] }

// Pass is the Verified run the feature regressed from.
func (o OpenRegression) Pass() *Run { return o.Latest().RegressedFrom(o.Feature) }

// OpenRegressions are the regressions still open in these runs, by feature
// and model.
func (p *Published) OpenRegressions(runs []*Run) []OpenRegression {
	type on struct{ feature, board, stack string }
	type found struct {
		run   *Run
		state string
	}
	sorted := append([]*Run(nil), runs...)
	sortRuns(sorted)
	var order []on
	states := map[on][]found{}
	for _, run := range newestFirst(sorted) {
		for _, feature := range run.testedFeatures() {
			state := run.FeatureState(feature)
			if state == "not-tested" {
				continue
			}
			key := on{feature, run.Board(), run.Stack()}
			if _, seen := states[key]; !seen {
				order = append(order, key)
			}
			states[key] = append(states[key], found{run, state})
		}
	}
	var open []OpenRegression
	for _, key := range order {
		if states[key][0].state != "regression" {
			continue
		}
		regression := OpenRegression{Feature: key.feature, Board: key.board, Stack: key.stack}
		for _, f := range states[key] {
			if f.state != "regression" {
				break
			}
			regression.Runs = append(regression.Runs, f.run)
		}
		open = append(open, regression)
	}
	sort.SliceStable(open, func(i, j int) bool {
		a, b := open[i], open[j]
		if x, y := p.cat.FeatureName(a.Feature), p.cat.FeatureName(b.Feature); x != y {
			return x < y
		}
		if x, y := a.Latest().ShortModel(), b.Latest().ShortModel(); x != y {
			return x < y
		}
		return a.Stack < b.Stack
	})
	return open
}
