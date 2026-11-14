package reports

// What a feature does on a Mac, in the lab's colour code:
//
//	works           green    every tested check works
//	partial         yellow   some checks work and others don't, or machines disagree
//	regression      red      doesn't work, and an earlier Verified run on the same
//	                         model and stack found it working
//	fails           orange   doesn't work although it should on this Mac, with no
//	                         earlier Verified pass to call it a regression
//	missing         blue     expected missing: not yet in Aurora, Asahi or Omarchy
//	unknown         magenta  unknown hardware
//	not-applicable  (none)   this Mac doesn't have the hardware
//	not-tested      grey     no check was run

// StateOrder is the states, best first.
var StateOrder = []string{"works", "partial", "regression", "fails", "missing", "unknown", "not-applicable", "not-tested"}

var stateWords = map[string]string{
	"works": "works", "partial": "partial", "regression": "regression", "fails": "doesn't work",
	"missing": "expected missing", "unknown": "unknown hardware", "not-applicable": "not on this Mac", "not-tested": "not tested",
}

var stateMeanings = map[string]string{
	"works":          "works",
	"partial":        "partly works: some checks pass and others don't, or machines disagree",
	"regression":     "regression: an earlier Verified run on this model found it working",
	"fails":          "doesn't work, although it should on this Mac",
	"missing":        "expected missing: not supported yet by Aurora, Asahi or Omarchy",
	"unknown":        "unknown hardware: nothing in the catalogue drives it",
	"not-applicable": "this Mac doesn't have the hardware",
	"not-tested":     "not tested",
}

var stateGlyphs = map[string]string{
	"works": "✓", "partial": "~", "regression": "✗", "fails": "!", "missing": "·",
	"unknown": "?", "not-applicable": "–", "not-tested": " ",
}

var outcomeStates = map[string]string{
	"works": "works", "fails": "fails", "not-in-aurora": "missing", "not-in-asahi": "missing", "not-in-omarchy": "missing",
	"not-applicable": "not-applicable", "unknown-hardware": "unknown", "not-tested": "not-tested",
}

// StateOfOutcome is the state a check's outcome counts as.
func StateOfOutcome(outcome string) string {
	if state, ok := outcomeStates[outcome]; ok {
		return state
	}
	return "unknown"
}

// StateWords is a state in a few words; StateMeaning says what its colour
// means, for tooltips; StateGlyph is the mark in its square.
func StateWords(state string) string   { return stateWords[state] }
func StateMeaning(state string) string { return stateMeanings[state] }
func StateGlyph(state string) string   { return stateGlyphs[state] }

func stateRank(state string) int {
	for i, known := range StateOrder {
		if known == state {
			return i
		}
	}
	return len(StateOrder)
}

// CombineOutcomes is one run's state for a feature, from the outcomes of the
// checks that test it.
func CombineOutcomes(outcomes []string) string {
	seen := map[string]bool{}
	var states []string
	for _, outcome := range outcomes {
		if state := StateOfOutcome(outcome); state != "not-tested" && !seen[state] {
			seen[state] = true
			states = append(states, state)
		}
	}
	switch {
	case len(states) == 0:
		return "not-tested"
	case len(states) == 1:
		return states[0]
	case seen["works"]:
		return "partial"
	}
	for _, state := range []string{"regression", "fails", "unknown", "missing", "not-applicable"} {
		if seen[state] {
			return state
		}
	}
	return ""
}
