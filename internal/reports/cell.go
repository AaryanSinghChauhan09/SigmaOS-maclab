package reports

import (
	"fmt"
	"sort"
	"strings"
)

// Agreement is how many distinct machines must agree before Community runs
// colour a cell.
const Agreement = 2

// Tally counts machines by state, in the order the states were first seen.
type Tally []StateCount

type StateCount struct {
	State string
	N     int
}

func (t Tally) add(state string) Tally {
	for i := range t {
		if t[i].State == state {
			t[i].N++
			return t
		}
	}
	return append(t, StateCount{state, 1})
}

// Sum is the number of machines counted.
func (t Tally) Sum() int {
	sum := 0
	for _, c := range t {
		sum += c.N
	}
	return sum
}

// Cell is one feature on one configuration, counted over the published
// runs. Each machine counts once, with the latest state it tested. Verified
// runs colour the cell on their own: the state their machines agree on,
// partial when they disagree. Without Verified runs, the cell only takes a
// colour when two or more distinct machines agree; until then it shows the
// Community runs, labelled as unconfirmed. Machines agreeing on different
// states make it partial. Across builds, a check a newer run on the board
// found working retires older results that it didn't.
type Cell struct {
	// Tallies counts every machine, VerifiedTallies those of Verified runs.
	Tallies, VerifiedTallies Tally
	// Runs are the runs that tested the feature, newest first.
	Runs []*Run

	hidden  bool
	sources []*Run
	carried bool
}

type vote struct {
	state    string
	verified bool
}

// cellFrom counts runs, oldest first and all on one configuration.
// onlyVerified counts Verified runs only.
func cellFrom(runs []*Run, feature string, onlyVerified bool) *Cell {
	latest := map[string]vote{}
	var order []string
	var tested []*Run
	keys := machineKeys(runs)
	for _, run := range runs {
		if onlyVerified && !run.Verified {
			continue
		}
		state := run.FeatureState(feature)
		if state == "" || state == "not-tested" {
			continue
		}
		tested = append(tested, run)
		if _, seen := latest[keys[run]]; !seen {
			order = append(order, keys[run])
		}
		latest[keys[run]] = vote{state, run.Verified}
	}
	cell := &Cell{Runs: newestFirst(tested)}
	for _, machine := range order {
		cell.Tallies = cell.Tallies.add(latest[machine].state)
		if latest[machine].verified {
			cell.VerifiedTallies = cell.VerifiedTallies.add(latest[machine].state)
		}
	}
	return cell
}

type carried struct {
	id      string
	outcome string
	run     *Run
}

// cellFromChecks counts check by check, for a summary across builds: a
// partial retest must retain the outcomes of the checks it skipped; a skip on
// hardware the Mac doesn't have counts, as not-applicable.
func cellFromChecks(runs []*Run, feature string, onlyVerified bool) *Cell {
	latest := map[string][]carried{}
	var order []string
	var tested []*Run
	keys := machineKeys(runs)
	for _, run := range runs {
		if onlyVerified && !run.Verified {
			continue
		}
		var checks []*Check
		for _, check := range run.CountedChecks() {
			if check.Feature == feature && check.Outcome != "not-tested" && (check.Status != "skip" || check.Outcome == "not-applicable") {
				checks = append(checks, check)
			}
		}
		if len(checks) == 0 {
			continue
		}
		tested = append(tested, run)
		machine := keys[run]
		if _, seen := latest[machine]; !seen {
			order = append(order, machine)
		}
	next:
		for _, check := range checks {
			for i := range latest[machine] {
				if latest[machine][i].id == check.ID {
					latest[machine][i].outcome, latest[machine][i].run = check.Outcome, run
					continue next
				}
			}
			latest[machine] = append(latest[machine], carried{check.ID, check.Outcome, run})
		}
	}
	retireSuperseded(latest)
	cell := &Cell{Runs: newestFirst(tested), carried: true}
	type machineVote struct {
		vote
		sources []*Run
	}
	var votes, verifiedVotes []machineVote
	for _, machine := range order {
		checks := latest[machine]
		if len(checks) == 0 {
			continue
		}
		var outcomes []string
		var sources []*Run
		verified := true
		for _, c := range checks {
			outcomes = append(outcomes, c.outcome)
			sources = appendRun(sources, c.run)
		}
		sortRuns(sources)
		sources = newestFirst(sources)
		for _, run := range sources {
			verified = verified && run.Verified
		}
		state := CombineOutcomes(outcomes)
		if state == "fails" && sources[0].RegressedFrom(feature) != nil {
			state = "regression"
		}
		v := machineVote{vote{state, verified}, sources}
		votes = append(votes, v)
		cell.Tallies = cell.Tallies.add(state)
		if verified {
			verifiedVotes = append(verifiedVotes, v)
			cell.VerifiedTallies = cell.VerifiedTallies.add(state)
		}
	}
	contributing := votes
	if len(verifiedVotes) > 0 {
		contributing = verifiedVotes
	}
	for _, v := range contributing {
		for _, run := range v.sources {
			cell.sources = appendRun(cell.sources, run)
		}
	}
	sortRuns(cell.sources)
	cell.sources = newestFirst(cell.sources)
	return cell
}

func appendRun(runs []*Run, run *Run) []*Run {
	for _, known := range runs {
		if known == run {
			return runs
		}
	}
	return append(runs, run)
}

// retireSuperseded drops a check that didn't work once a newer run on the
// same board found it working: that Mac has since been brought up. A
// Community run can't retire a Verified run's result, and an older pass
// never retires a newer failure.
func retireSuperseded(latest map[string][]carried) {
	type on struct{ board, id string }
	passes := map[on][]*Run{}
	for _, checks := range latest {
		for _, c := range checks {
			if c.outcome == "works" {
				passes[on{c.run.Board(), c.id}] = append(passes[on{c.run.Board(), c.id}], c.run)
			}
		}
	}
	for machine, checks := range latest {
		var kept []carried
		for _, c := range checks {
			retired := false
			if c.outcome != "works" && c.outcome != "not-applicable" {
				for _, pass := range passes[on{c.run.Board(), c.id}] {
					if c.run.before(pass) && (pass.Verified || !c.run.Verified) {
						retired = true
					}
				}
			}
			if !retired {
				kept = append(kept, c)
			}
		}
		latest[machine] = kept
	}
}

// Hidden reports whether the confirmed-only filter hides the cell: what
// Community machines don't agree on yet.
func (c *Cell) Hidden() bool { return c.hidden }

// confirmedOnly is the cell with confirmed results only: an unconfirmed one is hidden.
func (c *Cell) confirmedOnly() *Cell {
	if c.Unconfirmed() {
		return &Cell{hidden: true}
	}
	return c
}

func (c *Cell) Machines() int         { return c.Tallies.Sum() }
func (c *Cell) VerifiedMachines() int { return c.VerifiedTallies.Sum() }

// Verified reports whether Verified runs decide the cell.
func (c *Cell) Verified() bool { return len(c.VerifiedTallies) > 0 }

// Sources are the contributing runs, newest first, including checks retained
// from earlier runs in a model summary. When Verified runs decide the
// state, Community runs cannot claim its provenance.
func (c *Cell) Sources() []*Run {
	if c.carried {
		return c.sources
	}
	keys := machineKeys(c.Runs)
	seen := map[string]bool{}
	var latest []*Run
	for _, run := range c.Runs {
		if !seen[keys[run]] {
			seen[keys[run]] = true
			if !c.Verified() || run.Verified {
				latest = append(latest, run)
			}
		}
	}
	return latest
}

// State is the colour state, or "" while the Community runs don't agree yet.
func (c *Cell) State() string {
	switch {
	case c.hidden:
		return ""
	case len(c.Tallies) == 0:
		return "not-tested"
	case c.Verified():
		if len(c.VerifiedTallies) == 1 {
			return c.VerifiedTallies[0].State
		}
		return "partial"
	}
	var agreed []string
	for _, count := range c.Tallies {
		if count.N >= Agreement {
			agreed = append(agreed, count.State)
		}
	}
	switch len(agreed) {
	case 0:
		return ""
	case 1:
		return agreed[0]
	}
	return "partial"
}

func (c *Cell) Confirmed() bool   { return c.State() != "" && len(c.Tallies) > 0 }
func (c *Cell) Unconfirmed() bool { return c.State() == "" }

// Tentative is what the Community runs say before machines agree; partial on a tie.
func (c *Cell) Tentative() string {
	if len(c.Tallies) == 0 {
		return "not-tested"
	}
	most := 0
	for _, count := range c.Tallies {
		most = max(most, count.N)
	}
	var leaders []string
	for _, count := range c.Tallies {
		if count.N == most {
			leaders = append(leaders, count.State)
		}
	}
	if len(leaders) == 1 {
		return leaders[0]
	}
	return "partial"
}

func (c *Cell) DisplayState() string {
	if state := c.State(); state != "" {
		return state
	}
	return c.Tentative()
}

func (c *Cell) CSSClass() string {
	switch {
	case c.hidden:
		return "cell cell-not-tested cell-hidden"
	case c.Unconfirmed():
		return "cell cell-unconfirmed cell-hint-" + c.Tentative()
	case c.Verified():
		return "cell cell-" + c.State() + " cell-tester"
	}
	return "cell cell-" + c.State()
}

func (c *Cell) Summary() string {
	switch {
	case c.hidden:
		return "unconfirmed community results, hidden (confirmed only)"
	case len(c.Tallies) == 0:
		return "not tested"
	case c.Verified():
		summary := StateWords(c.State()) + ": verified, " + describe(c.VerifiedTallies, "verified machine")
		if c.Machines() > c.VerifiedMachines() {
			summary += fmt.Sprintf("; %d machines in all", c.Machines())
		}
		return summary
	case c.Confirmed():
		return StateWords(c.State()) + ": " + describe(c.Tallies, "machine")
	}
	return fmt.Sprintf("community, unconfirmed: %s; colours once %d machines agree or a Verified run confirms", describe(c.Tallies, "machine"), Agreement)
}

// Counts is each state's machine count, without the verdict ("2 machines works, 1 machine fails").
func (c *Cell) Counts() string {
	if c.Verified() {
		return describe(c.VerifiedTallies, "verified machine")
	}
	return describe(c.Tallies, "machine")
}

func describe(tally Tally, noun string) string {
	sorted := append(Tally(nil), tally...)
	sort.SliceStable(sorted, func(i, j int) bool { return stateRank(sorted[i].State) < stateRank(sorted[j].State) })
	parts := make([]string, 0, len(sorted))
	for _, count := range sorted {
		parts = append(parts, fmt.Sprintf("%s %s", Plural(count.N, noun), StateWords(count.State)))
	}
	return strings.Join(parts, ", ")
}

// Plural is "1 machine", "2 machines".
func Plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
