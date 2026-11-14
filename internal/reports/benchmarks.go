package reports

import (
	"math"
	"math/big"
	"sort"
	"strings"
	"time"
)

// Benchmark scores from the published reports (the score of a passed
// benchmark.* check), compared per Mac model, Omarchy stack/version and build.
//
// Scores are compared only within one benchmark: the same check id, suite
// (the CLI's fixed scenes, clip and settings) and unit. Each machine counts
// once per model and stack/version, with its latest score, so one Mac run
// ten times doesn't outweigh another; a row's score is the median of its
// machines'.

var benchmarkNames = map[string][2]string{
	"benchmark.opengl":      {"OpenGL", "glmark2, off-screen at 1920x1080"},
	"benchmark.vulkan":      {"Vulkan", "vkmark, headless at 1920x1080"},
	"benchmark.h264-decode": {"H.264 decode", "ffmpeg, VA-API hardware decode of a 1080p clip"},
	"benchmark.hevc-decode": {"HEVC decode", "ffmpeg, VA-API hardware decode of a 1080p clip"},
	"benchmark.h264-encode": {"H.264 encode", "ffmpeg, hardware encode of a 1080p test source"},
	"benchmark.storage":     {"Storage", "fio, sequential read of a 1 GiB file with direct I/O; compared within one SSD capacity"},
	"benchmark.ane":         {"Neural Engine", "omarchy-ane-smoke, runs of its small test program"},
}

var unitWords = map[string]string{"points": "points", "fps": "frames/s", "MBps": "MB/s", "ops": "runs/s"}

// UnitWords is a score's unit as a page says it.
func UnitWords(unit string) string {
	if words, ok := unitWords[unit]; ok {
		return words
	}
	return unit
}

// Storage speed depends on the SSD's size, so its rows also split by capacity.
const byCapacity = "benchmark.storage"

// Score is one measured run: a report's score for one benchmark. Value is
// the number as the report carries it.
type Score struct {
	Run                        *Run
	CheckID, Unit, Tool, Suite string
	Value                      any
}

// number is a score as a number.
func number(value any) float64 {
	switch value := value.(type) {
	case *big.Int:
		f, _ := new(big.Float).SetInt(value).Float64()
		return f
	case Float:
		return float64(value)
	case float64:
		return value
	case int:
		return float64(value)
	}
	return 0
}

// ScoreRow is one Mac model on one build (and, for storage, one SSD
// capacity) in a benchmark.
type ScoreRow struct {
	Configuration        Configuration
	ModelName, Chip, SoC string
	// Capacity is the SSD's capacity for a storage row that knows it, else "".
	Capacity string
	Scores   []Score

	latest    []Score
	reference func(string) *MacOSReference
}

// LatestPerMachine is each machine's latest score, newest first.
func (r *ScoreRow) LatestPerMachine() []Score {
	if r.latest == nil {
		var runs []*Run
		for _, score := range r.Scores {
			runs = appendRun(runs, score.Run)
		}
		keys := machineKeys(runs)
		sorted := append([]Score(nil), r.Scores...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[j].Run.before(sorted[i].Run) })
		seen := map[string]bool{}
		for _, score := range sorted {
			if !seen[keys[score.Run]] {
				seen[keys[score.Run]] = true
				r.latest = append(r.latest, score)
			}
		}
	}
	return r.latest
}

func (r *ScoreRow) Machines() int { return len(r.LatestPerMachine()) }
func (r *ScoreRow) Runs() int     { return len(r.Scores) }

// values are the machines' latest scores, lowest first.
func (r *ScoreRow) values() []any {
	var values []any
	for _, score := range r.LatestPerMachine() {
		values = append(values, score.Value)
	}
	sort.SliceStable(values, func(i, j int) bool { return number(values[i]) < number(values[j]) })
	return values
}

func (r *ScoreRow) Low() any      { return r.values()[0] }
func (r *ScoreRow) High() any     { v := r.values(); return v[len(v)-1] }
func (r *ScoreRow) Latest() Score { return r.LatestPerMachine()[0] }

// Tools are the tools that measured the machines' latest scores.
func (r *ScoreRow) Tools() []string {
	var tools []string
	for _, score := range r.LatestPerMachine() {
		if !oneIn(tools, score.Tool) {
			tools = append(tools, score.Tool)
		}
	}
	return tools
}

// Verified reports whether a machine's latest score is from a Verified run.
func (r *ScoreRow) Verified() bool {
	for _, score := range r.LatestPerMachine() {
		if score.Run.Verified {
			return true
		}
	}
	return false
}

// Median is the row's score: the median of its machines' (a number with a
// fraction when two machines straddle it).
func (r *ScoreRow) Median() any {
	values := r.values()
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (number(values[middle-1]) + number(values[middle])) / 2.0
}

// MacOS is what macOS measured on the same Mac, or nil.
func (r *ScoreRow) MacOS(checkID string) any {
	if reference := r.reference(r.Configuration.Board); reference != nil {
		return reference.Value(checkID, r.Capacity)
	}
	return nil
}

// MacOSGap says why this row has no macOS number, when the Mac has a
// reference but not at this capacity.
func (r *ScoreRow) MacOSGap(checkID string) string {
	reference := r.reference(r.Configuration.Board)
	if checkID == byCapacity && r.MacOS(checkID) == nil && reference != nil && reference.HasStorage() {
		return "no macOS reference at this capacity"
	}
	return ""
}

func (r *ScoreRow) MacOSConditions() string {
	if reference := r.reference(r.Configuration.Board); reference != nil {
		return reference.Conditions()
	}
	return ""
}

// Benchmark is one benchmark: a check, suite and unit, with its rows best first.
type Benchmark struct {
	CheckID, Suite, Unit string
	Rows                 []*ScoreRow

	suite any
}

func (b *Benchmark) ID() string {
	return strings.TrimPrefix(b.CheckID, "benchmark.") + "-suite-" + b.Suite
}

func (b *Benchmark) Name() string {
	if name, ok := benchmarkNames[b.CheckID]; ok {
		return name[0]
	}
	return b.CheckID
}

func (b *Benchmark) How() string       { return benchmarkNames[b.CheckID][1] }
func (b *Benchmark) UnitWords() string { return UnitWords(b.Unit) }

// Best is the best row's score.
func (b *Benchmark) Best() float64 {
	best := 0.0
	for _, row := range b.Rows {
		best = max(best, number(row.Median()))
	}
	return best
}

func (b *Benchmark) RowsForBoard(board string) []*ScoreRow {
	var rows []*ScoreRow
	for _, row := range b.Rows {
		if row.Configuration.Board == board {
			rows = append(rows, row)
		}
	}
	return rows
}

func (b *Benchmark) MacOS() bool      { return MacOSCompared(b.CheckID) }
func (b *Benchmark) ByCapacity() bool { return b.CheckID == byCapacity }

// Benchmarks is the comparison of every benchmark with scores.
type Benchmarks struct {
	list []*Benchmark
}

// Benchmarks compares the published runs' scores: in catalogue order, newest
// suite first; rows best first. reference gives a board's macOS numbers; nil
// is the catalogue's (MacOSReferenceFor).
func (p *Published) Benchmarks(reference func(board string) *MacOSReference) *Benchmarks {
	if reference == nil {
		reference = MacOSReferenceFor
	}
	type key struct{ check, suite, unit string }
	type rowKey struct {
		configuration Configuration
		capacity      string
	}
	var order []key
	groups := map[key][]Score{}
	suites := map[key]any{}
	for _, run := range p.runs {
		for _, check := range run.Checks() {
			score := check.Score()
			if check.Status != "pass" || score == nil || !strings.HasPrefix(check.ID, "benchmark.") {
				continue
			}
			s := Score{Run: run, CheckID: check.ID, Value: score.members["value"], Unit: rubyText(score.members["unit"]),
				Tool: rubyText(score.members["tool"]), Suite: rubyText(score.members["suite"])}
			k := key{s.CheckID, s.Suite, s.Unit}
			if _, seen := groups[k]; !seen {
				order = append(order, k)
				suites[k] = score.members["suite"]
			}
			groups[k] = append(groups[k], s)
		}
	}
	b := &Benchmarks{}
	for _, k := range order {
		var rowOrder []rowKey
		rows := map[rowKey][]Score{}
		for _, score := range groups[k] {
			rk := rowKey{configuration: score.Run.Configuration(ByBuild)}
			if k.check == byCapacity {
				rk.capacity = score.Run.Storage()
			}
			if _, seen := rows[rk]; !seen {
				rowOrder = append(rowOrder, rk)
			}
			rows[rk] = append(rows[rk], score)
		}
		benchmark := &Benchmark{CheckID: k.check, Suite: k.suite, Unit: k.unit, suite: suites[k]}
		for _, rk := range rowOrder {
			latest := rows[rk][0].Run
			for _, score := range rows[rk] {
				if latest.before(score.Run) {
					latest = score.Run
				}
			}
			benchmark.Rows = append(benchmark.Rows, &ScoreRow{Configuration: rk.configuration, ModelName: latest.ShortModel(), Chip: latest.Chip(), SoC: latest.SoC(),
				Capacity: rk.capacity, Scores: rows[rk], reference: reference})
		}
		sort.SliceStable(benchmark.Rows, func(i, j int) bool {
			a, b := benchmark.Rows[i], benchmark.Rows[j]
			if x, y := number(a.Median()), number(b.Median()); x != y {
				return x > y
			}
			return a.ModelName < b.ModelName
		})
		b.list = append(b.list, benchmark)
	}
	ids := p.cat.CheckIDs()
	rank := func(checkID string) int {
		for i, id := range ids {
			if id == checkID {
				return i
			}
		}
		return len(ids)
	}
	sort.SliceStable(b.list, func(i, j int) bool {
		if x, y := rank(b.list[i].CheckID), rank(b.list[j].CheckID); x != y {
			return x < y
		}
		return number(b.list[i].suite) > number(b.list[j].suite)
	})
	return b
}

func (b *Benchmarks) All() []*Benchmark { return b.list }
func (b *Benchmarks) Empty() bool       { return len(b.list) == 0 }

// ForBoard are the benchmarks this Mac model has scores in.
func (b *Benchmarks) ForBoard(board string) []*Benchmark {
	var list []*Benchmark
	for _, benchmark := range b.list {
		if len(benchmark.RowsForBoard(board)) > 0 {
			list = append(list, benchmark)
		}
	}
	return list
}

// ScoreWords is a benchmark score as it reads best: whole points with
// thousands apart, a fraction to one decimal.
func ScoreWords(value any) string {
	if value == nil {
		return "–"
	}
	f := number(value)
	whole := f == float64(int64(f))
	text := rubyFloat(roundTo(f, 1))
	if whole {
		text = new(big.Int).SetInt64(int64(f)).String()
	}
	digits, fraction, _ := strings.Cut(text, ".")
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	var grouped []byte
	for i := 0; i < len(digits); i++ {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped = append(grouped, ',')
		}
		grouped = append(grouped, digits[i])
	}
	if fraction != "" {
		return sign + string(grouped) + "." + fraction
	}
	return sign + string(grouped)
}

// Share is a score as a share of the best, in percent to one decimal ("57.1").
func Share(value any, best float64) string {
	if best <= 0 {
		return "0"
	}
	return rubyFloat(roundTo(100.0*number(value)/best, 1))
}

// roundTo rounds half away from zero, as Ruby's Float#round does.
func roundTo(f float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(f*scale) / scale
}

// JSON is the comparison as the export carries it.
func (b *Benchmarks) JSON() *Object {
	list := []any{}
	for _, benchmark := range b.list {
		rows := []any{}
		for _, row := range benchmark.Rows {
			macos := row.MacOS(benchmark.CheckID)
			var conditions any
			if macos != nil {
				conditions = row.MacOSConditions()
			}
			rows = append(rows, obj("model", row.ModelName, "board", row.Configuration.Board, "soc", row.SoC, "chip", row.Chip,
				"stack", row.Configuration.Stack, "version", row.Configuration.Version, "build", buildJSON(row.Latest().Run),
				"capacity", orNull(row.Capacity), "score", row.Median(), "low", row.Low(), "high", row.High(), "machines", row.Machines(), "runs", row.Runs(),
				"tools", row.Tools(), "macos", macos, "macos_conditions", conditions))
		}
		list = append(list, obj("check", benchmark.CheckID, "name", benchmark.Name(), "suite", benchmark.suite, "unit", benchmark.Unit, "rows", rows))
	}
	return obj("comparison", "scores compare only within one benchmark (check, suite and unit); each machine counts once per model and stack/version, with its latest score; score is the median over machines",
		"benchmarks", list)
}

// BenchmarksJSON is the benchmarks export.
func (p *Published) BenchmarksJSON(reference func(string) *MacOSReference, at time.Time) []byte {
	export := obj("license", license(), "generated_at", iso8601(at))
	body := p.Benchmarks(reference).JSON()
	for _, key := range body.keys {
		export.put(key, body.members[key])
	}
	return JSON(export)
}

// NewMacOSReference reads a macOS reference document, or nil when it is not one.
func NewMacOSReference(data []byte) *MacOSReference {
	document, err := Parse(data)
	if object, ok := document.(*Object); err == nil && ok {
		return &MacOSReference{object}
	}
	return nil
}
