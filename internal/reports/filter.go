package reports

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Filter is the filters a reader picks on the matrix and the reports list,
// from the query string, so every filtered view is a link and the exports
// take the same parameters. Every filter narrows (they combine with AND); a
// value the lab doesn't know matches nothing, and a malformed one is dropped.
//
// Which runs:
//
//	build     a build (Build.Matches: its runtime id, label or words)
//	set       the candidate set the build is on (Build.Set)
//	release   the Omarchy release (Run.OmarchyVersion, "4.0.0", "4.0.0-dev")
//	kernel    the running kernel release (Run.Kernel, uname -r)
//	stack     the stack (Stacks)
//	model     a Mac model, by board ("j416c")
//	gen       a chip generation ("m1": M1 and its Pro, Max and Ultra)
//	chip      a chip ("M1 Pro")
//	soc       an SoC ("t6000")
//	runs      "tester" (Verified runs only) or "community"
//	tool      the omarchy-m-test version that ran it ("0.1.8")
//	from, to  uploaded on or after, on or before a day (YYYY-MM-DD, UTC)
//
// Which results:
//
//	confirmed "1": leave out results nobody confirmed, answers given by a
//	          bare Enter, and on the matrix the cells Community machines
//	          don't agree on yet
//	layer     the matrix columns and check rows of one catalogue layer
//	state     the matrix rows and columns with a cell in this state, the
//	          check rows with this result (works, partial, fails,
//	          regression, gap, untested)
type Filter struct {
	cat    *Catalogue
	keys   []string
	values map[string]string
}

var (
	// RunKeys choose runs, ResultKeys results.
	RunKeys    = []string{"build", "set", "release", "kernel", "stack", "model", "gen", "chip", "soc", "runs", "tool", "from", "to"}
	ResultKeys = []string{"confirmed", "layer", "state"}
	FilterKeys = append(append([]string(nil), RunKeys...), ResultKeys...)
	// ListKeys are what a list of runs takes: which runs, and whether their counts leave out answers by default.
	ListKeys = append(append([]string(nil), RunKeys...), "confirmed")
	// FeatureKeys: the feature catalogue narrows its result boxes to a model and/or build.
	FeatureKeys = []string{"model", "build"}
)

// filterStates is a filter state and the matrix states it picks.
var filterStates = map[string][]string{
	"works": {"works"}, "partial": {"partial"}, "fails": {"fails"}, "regression": {"regression"},
	"gap": {"missing"}, "untested": {"not-tested"},
}

// FilterStateWords are the states a reader may pick, in the order offered.
var FilterStateWords = [][2]string{{"works", "works"}, {"partial", "partial"}, {"fails", "doesn't work"}, {"regression", "regression"},
	{"gap", "expected missing (gap)"}, {"untested", "not tested"}}

// FilterLabels name each filter on its field and its chip.
var FilterLabels = map[string]string{
	"build": "build", "set": "candidate set", "release": "release", "kernel": "kernel", "stack": "stack", "model": "model", "gen": "generation",
	"chip": "chip", "soc": "SoC", "runs": "runs", "tool": "tool version", "from": "from", "to": "to",
	"confirmed": "results", "layer": "layer", "state": "state",
}

var reDate = regexp.MustCompile(`\A\d{4}-\d{2}-\d{2}\z`)

// Build words run long (an image naming its set carries its runtime, kernel, boot package, build time and digest).
const maxFilterLength = 2048

// Filter reads the filters of a query. keys are the filters the view takes;
// nil is all of them.
func (p *Published) Filter(query url.Values, keys []string) *Filter {
	return NewFilter(p.cat, query, keys)
}

func NewFilter(cat *Catalogue, query url.Values, keys []string) *Filter {
	if keys == nil {
		keys = FilterKeys
	}
	f := &Filter{cat: cat, values: map[string]string{}}
	for _, key := range keys {
		given := query[key]
		if len(given) == 0 {
			continue
		}
		if value := f.clean(key, given[len(given)-1]); value != "" {
			f.keys = append(f.keys, key)
			f.values[key] = value
		}
	}
	return f
}

// Only is the filter with only those of its filters.
func (f *Filter) Only(keys []string) *Filter {
	only := &Filter{cat: f.cat, values: map[string]string{}}
	for _, key := range f.keys {
		for _, kept := range keys {
			if key == kept {
				only.keys = append(only.keys, key)
				only.values[key] = f.values[key]
			}
		}
	}
	return only
}

// Without is the filter less one of its filters.
func (f *Filter) Without(key string) *Filter {
	var kept []string
	for _, k := range f.keys {
		if k != key {
			kept = append(kept, k)
		}
	}
	return f.Only(kept)
}

func (f *Filter) Get(key string) string { return f.values[key] }
func (f *Filter) Any() bool             { return len(f.keys) > 0 }
func (f *Filter) Confirmed() bool       { return f.values["confirmed"] == "1" }
func (f *Filter) Layer() string         { return f.values["layer"] }
func (f *Filter) State() string         { return f.values["state"] }

// Keys are the filters picked, in the order the lab lists them.
func (f *Filter) Keys() []string { return f.keys }

func (f *Filter) filtersRuns() bool {
	for _, key := range f.keys {
		for _, run := range RunKeys {
			if key == run {
				return true
			}
		}
	}
	return false
}

// Runs are the runs among runs that every run filter passes, in their order.
func (f *Filter) Runs(runs []*Run) []*Run {
	if !f.filtersRuns() {
		return runs
	}
	var passed []*Run
	for _, run := range runs {
		if f.Passes(run) {
			passed = append(passed, run)
		}
	}
	return passed
}

func (f *Filter) Passes(run *Run) bool {
	for _, key := range f.keys {
		value := f.values[key]
		ok := true
		switch key {
		case "build":
			ok = run.Build() != nil && run.Build().Matches(value)
		case "set":
			ok = run.Build() != nil && run.Build().Set() == value
		case "release":
			ok = run.OmarchyVersion() == value
		case "kernel":
			ok = run.Kernel() == value
		case "stack":
			ok = run.Stack() == value
		case "model":
			ok = run.Board() == value
		case "gen":
			ok = generation(f.cat.ChipForSoC(run.SoC())) == value
		case "chip":
			ok = run.Chip() == value
		case "soc":
			ok = run.SoC() == value
		case "runs":
			ok = run.Verified == (value == "tester")
		case "tool":
			ok = run.ToolVersion() == value
		case "from":
			ok = run.CreatedAt.UTC().Format(time.DateOnly) >= value
		case "to":
			ok = run.CreatedAt.UTC().Format(time.DateOnly) <= value
		}
		if !ok {
			return false
		}
	}
	return true
}

// Chip is one picked filter as a removable chip.
type FilterChip struct{ Key, Value, Words string }

func (f *Filter) Chips() []FilterChip {
	chips := make([]FilterChip, 0, len(f.keys))
	for _, key := range f.keys {
		chips = append(chips, FilterChip{key, f.values[key], FilterLabels[key] + ": " + f.words(key, f.values[key])})
	}
	return chips
}

// Params are the query parameters for this filter, less except, with extra
// (name, value pairs such as "group", "release"; an empty value is left out).
func (f *Filter) Params(except string, extra ...string) url.Values {
	params := url.Values{}
	for _, key := range f.keys {
		if key != except {
			params.Set(key, f.values[key])
		}
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] != "" {
			params.Set(extra[i], extra[i+1])
		}
	}
	return params
}

// Query is "?a=1&b=2", or "" for no parameters: a filtered view's link.
func Query(params url.Values) string {
	if len(params) == 0 {
		return ""
	}
	return "?" + params.Encode()
}

func generation(chip string) string {
	gen, _, _ := strings.Cut(chip, "-")
	return gen
}

// Generations are the chip generations the catalogue knows ("m1", "m2", ...), in its order.
func (c *Catalogue) Generations() []string {
	var generations []string
	seen := map[string]bool{}
	if chips := c.Chips(); chips != nil {
		for _, key := range chips.keys {
			if gen := generation(key); !seen[gen] {
				seen[gen] = true
				generations = append(generations, gen)
			}
		}
	}
	return generations
}

func (f *Filter) words(key, value string) string {
	switch key {
	case "stack":
		return StackWords(value)
	case "gen":
		return strings.ToUpper(value)
	case "runs":
		if value == "tester" {
			return "Verified runs only"
		}
		return "Community runs only"
	case "confirmed":
		return "confirmed only"
	case "layer":
		if words, ok := LayerWords[value]; ok {
			return words
		}
	case "state":
		for _, state := range FilterStateWords {
			if state[0] == value {
				return state[1]
			}
		}
	}
	return value
}

func oneOf(value string, known []string) string {
	for _, k := range known {
		if k == value {
			return value
		}
	}
	return ""
}

func (f *Filter) clean(key, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxFilterLength {
		return ""
	}
	switch key {
	case "stack":
		return oneOf(value, Stacks)
	case "runs":
		return oneOf(value, []string{"tester", "community"})
	case "confirmed":
		return oneOf(value, []string{"1"})
	case "layer":
		return oneOf(value, Layers)
	case "state":
		if _, ok := filterStates[value]; !ok {
			return ""
		}
	case "gen":
		return oneOf(strings.ToLower(value), f.cat.Generations())
	case "from", "to":
		if _, err := time.Parse(time.DateOnly, value); err != nil || !reDate.MatchString(value) {
			return ""
		}
	}
	return value
}

// Choice is one choice of a filter: its value and its words.
type Choice struct{ Value, Words string }

// Options are the choices the filter bar offers, from the published runs:
// newest first or in the catalogue's order.
type Options struct {
	pub    *Published
	runs   []*Run
	filter *Filter
}

// Options offers choices from every published run. With a filter, the
// builds offered are those its other filters pass.
func (p *Published) Options(filter *Filter) *Options {
	return &Options{pub: p, runs: p.runs, filter: filter}
}

func (o *Options) values(of func(*Run) string) []string {
	var values []string
	seen := map[string]bool{}
	for _, run := range o.runs {
		if value := of(run); value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

func same(values []string) []Choice {
	choices := make([]Choice, 0, len(values))
	for _, value := range values {
		choices = append(choices, Choice{value, value})
	}
	return choices
}

func reversed(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[len(values)-1-i] = value
	}
	return out
}

// versions puts versions newest first, as RubyGems orders them.
func versions(list []string) []Choice {
	sort.SliceStable(list, func(i, j int) bool { return newGemVersion(list[i]).compare(newGemVersion(list[j])) < 0 })
	return same(reversed(list))
}

// For gives the choices of one filter.
func (o *Options) For(key string) []Choice {
	switch key {
	case "build":
		return o.builds()
	case "set":
		sets := o.values(func(run *Run) string {
			if run.Build() == nil {
				return ""
			}
			return run.Build().Set()
		})
		sort.Strings(sets)
		return same(reversed(sets))
	case "release":
		return versions(o.values((*Run).OmarchyVersion))
	case "kernel":
		// Kernel releases contain distro suffixes; compare numeric parts as numbers.
		kernels := o.values((*Run).Kernel)
		sort.SliceStable(kernels, func(i, j int) bool { return naturalLess(kernels[i], kernels[j]) })
		return same(reversed(kernels))
	case "stack":
		choices := make([]Choice, 0, len(Stacks))
		for _, stack := range Stacks {
			choices = append(choices, Choice{stack, StackWords(stack)})
		}
		return choices
	case "model":
		var choices []Choice
		seen := map[string]bool{}
		for _, run := range o.runs {
			if !seen[run.Board()] {
				seen[run.Board()] = true
				choices = append(choices, Choice{run.Board(), run.ShortModel() + " (" + run.Board() + ")"})
			}
		}
		sort.SliceStable(choices, func(i, j int) bool { return choices[i].Words < choices[j].Words })
		return choices
	case "gen":
		tested := map[string]bool{}
		for _, gen := range o.values(func(run *Run) string { return generation(o.pub.cat.ChipForSoC(run.SoC())) }) {
			tested[gen] = true
		}
		var choices []Choice
		for _, gen := range o.pub.cat.Generations() {
			if tested[gen] {
				choices = append(choices, Choice{gen, strings.ToUpper(gen)})
			}
		}
		return choices
	case "chip":
		chips := o.values((*Run).Chip)
		sort.Strings(chips)
		return same(chips)
	case "soc":
		socs := o.values((*Run).SoC)
		sort.Strings(socs)
		return same(socs)
	case "runs":
		return []Choice{{"tester", "Verified runs only"}, {"community", "Community runs only"}}
	case "tool":
		return versions(o.values((*Run).ToolVersion))
	case "layer":
		choices := make([]Choice, 0, len(Layers))
		for _, layer := range Layers {
			choices = append(choices, Choice{layer, LayerWords[layer]})
		}
		return choices
	case "state":
		choices := make([]Choice, 0, len(FilterStateWords))
		for _, state := range FilterStateWords {
			choices = append(choices, Choice{state[0], state[1]})
		}
		return choices
	}
	return nil
}

func (o *Options) builds() []Choice {
	runs := o.runs
	if o.filter != nil {
		runs = o.filter.Without("build").Runs(runs)
	}
	builds := RunsByBuild(runs)
	labels := map[string]int{}
	for _, build := range builds {
		labels[build.Build.Label()]++
	}
	var choices []Choice
	seen := map[string]bool{}
	for _, build := range builds {
		// Variants of one runtime need their kernel/image details in the picker.
		label := build.Build.Label()
		if labels[label] > 1 {
			label = build.Build.Words()
		}
		if words := build.Build.Words(); !seen[words] {
			seen[words] = true
			choices = append(choices, Choice{words, fmt.Sprintf("%s (%d)", label, len(build.Runs))})
		}
	}
	return choices
}

type naturalPart struct {
	number bool
	text   string
}

// naturalParts splits text into its runs of digits and what lies between
// them; text that starts with a digit starts with an empty part.
func naturalParts(text string) []naturalPart {
	var parts []naturalPart
	start := 0
	digit := func(i int) bool { return text[i] >= '0' && text[i] <= '9' }
	if text != "" && digit(0) {
		parts = append(parts, naturalPart{})
	}
	for i := 1; i <= len(text); i++ {
		if i == len(text) || digit(i) != digit(start) {
			parts = append(parts, naturalPart{digit(start), text[start:i]})
			start = i
		}
	}
	return parts
}

func naturalLess(a, b string) bool {
	x, y := naturalParts(a), naturalParts(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i].number != y[i].number {
			return !x[i].number
		}
		c := strings.Compare(x[i].text, y[i].text)
		if x[i].number {
			c = compareNumbers(x[i].text, y[i].text)
		}
		if c != 0 {
			return c < 0
		}
	}
	return len(x) < len(y)
}
