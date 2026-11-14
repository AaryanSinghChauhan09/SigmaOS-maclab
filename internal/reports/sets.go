package reports

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"
)

// The candidate sets the lab knows by their packages (candidate_sets.yml),
// so a run on a set's exact omarchy package maps to the set even when its
// image doesn't name it.

//go:embed candidate_sets.yml
var candidateSetsYAML string

// KnownSet is one candidate set of candidate_sets.yml.
type KnownSet struct{ Name, SourceCommit, Created, Omarchy string }

var knownSets = mustKnownSets(candidateSetsYAML)

// KnownSets are the candidate sets of candidate_sets.yml, in its order.
func KnownSets() []KnownSet { return knownSets }

func mustKnownSets(text string) []KnownSet {
	sets, err := parseKnownSets(text)
	if err != nil {
		panic(err)
	}
	return sets
}

// parseKnownSets reads the one shape the file has: a name at the margin,
// then its three values indented under it. Anything else is an error, so a
// change of shape is noticed.
func parseKnownSets(text string) ([]KnownSet, error) {
	var sets []KnownSet
	for n, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			return nil, fmt.Errorf("reports: candidate_sets.yml line %d: no key", n+1)
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("reports: candidate_sets.yml line %d: %v", n+1, err)
			}
			value = unquoted
		}
		indented := strings.HasPrefix(line, " ")
		switch {
		case !indented && value == "":
			sets = append(sets, KnownSet{Name: key})
		case indented && len(sets) > 0 && key == "source_commit":
			sets[len(sets)-1].SourceCommit = value
		case indented && len(sets) > 0 && key == "created":
			sets[len(sets)-1].Created = value
		case indented && len(sets) > 0 && key == "omarchy":
			sets[len(sets)-1].Omarchy = value
		default:
			return nil, fmt.Errorf("reports: candidate_sets.yml line %d: %q is not a set's name, source_commit, created or omarchy", n+1, key)
		}
	}
	for _, set := range sets {
		if set.SourceCommit == "" || set.Created == "" || set.Omarchy == "" {
			return nil, fmt.Errorf("reports: candidate_sets.yml: %s lacks a value", set.Name)
		}
	}
	return sets, nil
}

// knownSetFor is the set built with exactly this omarchy package version, or "".
func knownSetFor(omarchy string) string {
	if omarchy == "" {
		return ""
	}
	for _, set := range knownSets {
		if set.Omarchy == omarchy {
			return set.Name
		}
	}
	return ""
}
