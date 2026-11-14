package reports

import "sort"

// KernelGaps is what to work on next in the kernel: features Asahi supports
// that Aurora lacks or hasn't verified (from the catalogue), and what the
// published reports found missing or unclaimed on real Macs.
type KernelGaps struct {
	pub  *Published
	runs []*Run
}

// CatalogueGap is an Asahi-supported feature and the chips where Aurora lacks it.
type CatalogueGap struct {
	Feature *Object
	Chips   []string
}

// ReportedGap is one outcome of one feature on one chip, with the runs that found it.
type ReportedGap struct {
	Outcome, Feature, SoC, Chip string
	Runs                        []*Run
}

func (g *ReportedGap) Machines() int    { return MachineCount(g.Runs) }
func (g *ReportedGap) Models() []string { return uniqTexts(g.Runs, (*Run).ShortModel) }

// UnclaimedHardware is hardware no driver claimed, from the reports'
// inventory: one device-tree node type (its compatible string) with what the
// catalogue says it is. Outcome is unknown-hardware when the catalogue
// doesn't know it.
type UnclaimedHardware struct {
	Compatible, Outcome, Feature string
	Runs                         []*Run
}

func (h *UnclaimedHardware) Unknown() bool    { return h.Outcome == "unknown-hardware" }
func (h *UnclaimedHardware) Machines() int    { return MachineCount(h.Runs) }
func (h *UnclaimedHardware) Models() []string { return uniqTexts(h.Runs, (*Run).ShortModel) }

// Chips are the chips it was found on, each as its name and SoC.
func (h *UnclaimedHardware) Chips() [][2]string {
	var chips [][2]string
	seen := map[[2]string]bool{}
	for _, run := range h.Runs {
		if chip := [2]string{run.Chip(), run.SoC()}; !seen[chip] {
			seen[chip] = true
			chips = append(chips, chip)
		}
	}
	return chips
}

// ReportedOutcomes are the outcomes the page lists as found on real Macs.
var ReportedOutcomes = []string{"not-in-aurora", "not-in-asahi", "unknown-hardware"}

// Gaps reads the gaps of the published runs.
func (p *Published) Gaps() *KernelGaps { return &KernelGaps{pub: p, runs: p.runs} }

// MissingInAurora: Aurora status unsupported where Asahi supports it.
func (g *KernelGaps) MissingInAurora() []CatalogueGap { return g.catalogueGaps("unsupported") }

// UnverifiedInAurora: Aurora status unknown where Asahi supports it.
func (g *KernelGaps) UnverifiedInAurora() []CatalogueGap { return g.catalogueGaps("unknown") }

func (g *KernelGaps) catalogueGaps(aurora string) []CatalogueGap {
	cat := g.pub.cat
	var gaps []CatalogueGap
	for _, feature := range cat.features {
		var chips []string
		if all := cat.Chips(); all != nil {
			for _, chip := range all.keys {
				states := cat.Expected(feature, chip, "")
				if states != nil && oneIn(AsahiSupported, text(member(states, "asahi", "status"))) && text(member(states, "aurora", "status")) == aurora {
					chips = append(chips, chip)
				}
			}
		}
		if len(chips) > 0 {
			gaps = append(gaps, CatalogueGap{feature, chips})
		}
	}
	return gaps
}

// Reported are the gaps with that outcome, by feature name and SoC.
func (g *KernelGaps) Reported(outcome string) []*ReportedGap {
	type key struct{ feature, soc, chip string }
	found := map[key]*ReportedGap{}
	var gaps []*ReportedGap
	for _, run := range g.runs {
		for _, check := range run.Checks() {
			if check.Outcome != outcome {
				continue
			}
			k := key{check.Feature, run.SoC(), run.Chip()}
			gap := found[k]
			if gap == nil {
				gap = &ReportedGap{Outcome: outcome, Feature: check.Feature, SoC: run.SoC(), Chip: run.Chip()}
				found[k] = gap
				gaps = append(gaps, gap)
			}
			gap.Runs = appendRun(gap.Runs, run)
		}
	}
	cat := g.pub.cat
	sort.SliceStable(gaps, func(i, j int) bool {
		if a, b := cat.FeatureName(gaps[i].Feature), cat.FeatureName(gaps[j].Feature); a != b {
			return a < b
		}
		return gaps[i].SoC < gaps[j].SoC
	})
	return gaps
}

// UnclaimedHardware is the unclaimed hardware across the published reports,
// unknown hardware first.
func (g *KernelGaps) UnclaimedHardware() []*UnclaimedHardware {
	type key struct{ compatible, outcome, feature string }
	found := map[key]*UnclaimedHardware{}
	var list []*UnclaimedHardware
	for _, run := range g.runs {
		unclaimed, _ := member(run.doc, "inventory", "unclaimed").([]any)
		for _, item := range unclaimed {
			entry := asObject(item)
			compatible, ok := member(entry, "compatible").(string)
			if !ok {
				continue
			}
			k := key{compatible, text(member(entry, "outcome")), text(member(entry, "feature"))}
			hardware := found[k]
			if hardware == nil {
				hardware = &UnclaimedHardware{Compatible: k.compatible, Outcome: k.outcome, Feature: k.feature}
				found[k] = hardware
				list = append(list, hardware)
			}
			hardware.Runs = appendRun(hardware.Runs, run)
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if a, b := list[i].Unknown(), list[j].Unknown(); a != b {
			return a
		}
		return list[i].Compatible < list[j].Compatible
	})
	return list
}
