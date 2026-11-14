package reports

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// The seeded corpus (site/db/preview_seeds.rb's reports, at fixed times) and
// what the public pages count from it, compared with what the old site's own
// models count: testdata/rails-aggregation.json, as
// site/test/support/aggregation_dump.rb writes it. With PAGE_PARITY_DIR set,
// the corpus and this port's counts are written there, for
// site/test/integration/page_parity_test.rb to read; with
// PAGE_PARITY_AGGREGATION set, the counts compared with are that file's,
// which is how CI holds the two side by side.

const railsAggregation = "testdata/rails-aggregation.json.gz"

type seeded struct {
	PublicID    string          `json:"public_id"`
	CreatedAt   string          `json:"created_at"`
	MachineID   string          `json:"machine_id"`
	TesterLogin *string         `json:"tester_login"`
	Verified    bool            `json:"verified"`
	Body        json.RawMessage `json:"body"`
}

// seededCorpus is the preview seeds: the production reports, every third a
// Verified run; two Verified machines that retest one feature, then one
// skips it; and a benchmark run.
func seededCorpus(t testing.TB) ([]*Report, []seeded) {
	t.Helper()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var list []*Report
	var entries []seeded
	add := func(doc *Object, at time.Time, machine string, verified bool) {
		sum := sha256.Sum256([]byte(machine))
		r := &Report{ID: int64(len(list) + 1), PublicID: fmt.Sprintf("preview-%016d", len(list)), SchemaVersion: 1, Body: encode(t, doc.without("signature")),
			CreatedAt: at, MachineID: "preview:" + hex.EncodeToString(sum[:])[:20], Verified: verified}
		entry := seeded{PublicID: r.PublicID, CreatedAt: at.Format(time.RFC3339), MachineID: r.MachineID, Verified: verified, Body: r.Body}
		if verified {
			login := "demo-tester"
			r.Handle, r.AccountID, entry.TesterLogin = login, 4242, &login
		}
		list, entries = append(list, r), append(entries, entry)
	}
	for i, r := range productionShelf(t).list {
		add(r.Document(), r.CreatedAt, fmt.Sprintf("snapshot-%d", i), i%3 == 0)
	}
	for index, outcome := range []string{"works", "fails", "not-tested"} {
		machines := []string{"demo-a", "demo-b"}
		if outcome == "not-tested" {
			machines = machines[:1]
		}
		for n, machine := range machines {
			doc := goldenWith(t, "m2-max-image2", "display.backlight", outcome)
			c := check(t, doc, "display.backlight")
			*c = *c.without("answered_by_default")
			c.set("evidence", []any{"Local preview: synthetic " + outcome + " example."})
			add(doc, now.Add(-time.Duration(3-index)*time.Hour+time.Duration(n)*time.Second), machine, true)
		}
	}
	benchmark := func() *Object {
		return parse(t, read(t, filepath.Join(goldenDir, "benchmarks", "m2-max-converged.json")))
	}
	add(benchmark(), now.Add(-30*time.Minute), "demo-a", true)

	// Past the seeds, what the later pages count: runs on images that name
	// their candidate set (two failing on a regression, one waiting, one
	// passing), a second machine's benchmark
	// scores, and a storage score with its SSD's capacity.
	onSet := func(doc *Object, set string) *Object {
		system := asObject(member(doc, "system"))
		system.set("candidate_set", set)
		system.set("image", obj("format", "1", "platform", "apple-silicon", "candidate_set", set, "image_profile", "test", "built", "2026-10-05T01:02:03Z"))
		return doc
	}
	working := func(outcomes ...string) *Object {
		doc := golden(t, "m2-max-converged")
		for _, item := range member(doc, "checks").([]any) {
			if c := asObject(item); text(member(c, "classification", "outcome")) == "fails" {
				c.set("status", "pass")
				asObject(member(c, "classification")).set("outcome", "works")
			}
		}
		for i := 0; i+1 < len(outcomes); i += 2 {
			c := check(t, doc, outcomes[i])
			c.set("status", "fail")
			asObject(member(c, "classification")).set("outcome", outcomes[i+1])
		}
		return doc
	}
	scaled := func(percent int64) *Object {
		doc := benchmark()
		for _, item := range member(doc, "checks").([]any) {
			if value, ok := member(asObject(item), "score", "value").(*big.Int); ok {
				asObject(member(asObject(item), "score")).set("value", new(big.Int).Div(new(big.Int).Mul(value, big.NewInt(percent)), big.NewInt(100)))
			}
		}
		return doc
	}
	add(onSet(working(), "edge-demo"), now.Add(-25*time.Minute), "demo-a", true)
	add(onSet(golden(t, "m2-max-converged"), "edge-demo"), now.Add(-24*time.Minute), "demo-c", false)
	add(onSet(working("gpu.driver", "fails", "gpu.vulkan", "fails"), "edge-demo"), now.Add(-23*time.Minute), "demo-b", true)
	add(onSet(working("gpu.driver", "fails", "gpu.vulkan", "fails"), "edge-failing"), now.Add(-22*time.Minute), "demo-b", true)
	add(onSet(golden(t, "m2-max-converged"), "edge-waiting"), now.Add(-21*time.Minute), "demo-c", false)
	add(onSet(working(), "edge-passing"), now.Add(-21*time.Minute+time.Second), "demo-a", true)
	add(scaled(110), now.Add(-20*time.Minute), "demo-c", false)
	storage := scaled(90)
	asObject(member(storage, "machine")).set("storage", "1TB")
	storage.set("checks", append(member(storage, "checks").([]any), obj("id", "benchmark.storage", "kind", "automatic", "status", "pass", "evidence", []any{"sequential read: 3000 MB/s"},
		"classification", obj("outcome", "works", "feature", "nvme", "layer", "asahi", "expected", obj("asahi", "upstream")),
		"score", obj("value", big.NewInt(3000), "unit", "MBps", "tool", "fio 3.38", "suite", big.NewInt(1)))))
	add(storage, now.Add(-19*time.Minute), "demo-d", false)
	return list, entries
}

// seededReports is how many reports the corpus holds.
const seededReports = 25

// The exports both sides write: the same list as AggregationDump::EXPORTS.
var parityExports = []struct {
	file  string
	query [][2]string
}{
	{"reports.json", [][2]string{{"model", "j613"}}}, {"reports.json", [][2]string{{"gen", "m1"}, {"tool", "0.1.8"}, {"confirmed", "1"}}}, {"reports.json", [][2]string{{"set", "edge-demo"}}},
	{"reports.csv", nil}, {"reports.csv", [][2]string{{"confirmed", "1"}, {"runs", "tester"}}},
	{"checks.csv", nil}, {"checks.csv", [][2]string{{"model", "j416c"}, {"state", "regression"}}}, {"checks.csv", [][2]string{{"layer", "aurora"}, {"confirmed", "1"}, {"state", "gap"}}},
	{"matrix.json", nil}, {"matrix.json", [][2]string{{"group", "release"}}}, {"matrix.json", [][2]string{{"group", "model"}}},
	{"matrix.json", [][2]string{{"group", "model"}, {"confirmed", "1"}, {"gen", "m2"}}}, {"matrix.json", [][2]string{{"layer", "asahi"}, {"state", "works"}}},
	{"benchmarks.json", nil}, {"aurora.json", nil},
}

var reGeneratedAt = regexp.MustCompile(`"generated_at":"[^"]*"`)

// dumpExports is each export as it is served, less the time it was made;
// reports.json as data, the others as their text.
func dumpExports(t testing.TB, p *Published) map[string]any {
	urlFor := func(run *Run) string { return "http://www.example.com/reports/" + run.PublicID }
	timeless := func(export []byte) string {
		return reGeneratedAt.ReplaceAllString(string(export), `"generated_at":"-"`)
	}
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	out := map[string]any{}
	for _, export := range parityExports {
		name, query := export.file, url.Values{}
		for _, pair := range export.query {
			name += " " + pair[0] + "=" + pair[1]
			query.Set(pair[0], pair[1])
		}
		switch export.file {
		case "reports.json":
			var body map[string]any
			decode(t, p.ReportsJSON(p.Filter(query, ListKeys), urlFor, at), &body)
			delete(body, "generated_at")
			out[name] = body
		case "reports.csv":
			out[name] = string(p.ReportsCSV(p.Filter(query, ListKeys), urlFor))
		case "checks.csv":
			out[name] = string(p.ChecksCSV(p.Filter(query, nil)))
		case "matrix.json":
			out[name] = timeless(p.MatrixJSON(query.Get("group"), p.Filter(query, nil), at))
		case "benchmarks.json":
			out[name] = timeless(p.BenchmarksJSON(nil, at))
		case "aurora.json":
			out[name] = timeless(p.AuroraJSON(urlFor, at))
		}
	}
	return out
}

func dumpGaps(p *Published) map[string]any {
	gaps := p.Gaps()
	reported := map[string]any{}
	for _, outcome := range ReportedOutcomes {
		list := []any{}
		for _, gap := range gaps.Reported(outcome) {
			list = append(list, map[string]any{"feature": gap.Feature, "soc": gap.SoC, "chip": gap.Chip, "runs": publicIDs(gap.Runs), "machines": gap.Machines(), "models": gap.Models()})
		}
		reported[outcome] = list
	}
	unclaimed := []any{}
	for _, hardware := range gaps.UnclaimedHardware() {
		chips := []any{}
		for _, chip := range hardware.Chips() {
			chips = append(chips, []any{chip[0], chip[1]})
		}
		unclaimed = append(unclaimed, map[string]any{"compatible": hardware.Compatible, "outcome": hardware.Outcome, "feature": hardware.Feature, "runs": publicIDs(hardware.Runs),
			"machines": hardware.Machines(), "models": hardware.Models(), "chips": chips})
	}
	catalogue := func(found []CatalogueGap) []any {
		list := []any{}
		for _, gap := range found {
			list = append(list, []any{id(gap.Feature), gap.Chips})
		}
		return list
	}
	return map[string]any{"reported": reported, "unclaimed": unclaimed, "missing": catalogue(gaps.MissingInAurora()), "unverified": catalogue(gaps.UnverifiedInAurora())}
}

func dumpCandidates(p *Published) map[string]any {
	findings := func(found []Finding) []any {
		list := []any{}
		for _, finding := range found {
			list = append(list, []any{finding.Row.Configuration.Label(), id(finding.Feature), finding.Cell.State()})
		}
		return list
	}
	names, sets := []string{}, []any{}
	for _, set := range p.CandidateSets() {
		names = append(names, set.Name)
		rows, models := []any{}, []any{}
		for _, row := range set.TestedRows() {
			rows = append(rows, row.Configuration.Label())
			known := false
			for _, model := range models {
				known = known || model == row.ModelName
			}
			if !known {
				models = append(models, row.ModelName)
			}
		}
		sets = append(sets, map[string]any{"name": set.Name, "runs": publicIDs(set.Runs), "tester_runs": len(set.VerifiedRuns()), "latest": set.Latest().PublicID,
			"verdict": set.Verdict(), "failures": findings(set.Failures()), "regressions": len(set.Regressions()), "partials": findings(set.Partials()),
			"tested_rows": rows, "models": models, "found": publicIDs(p.CandidateSet(set.Name).Runs), "matched": publicIDs(set.MatchedBy())})
	}
	sort.Strings(names)
	return map[string]any{"names": names, "sets": sets}
}

// The views both sides count: the same list as AggregationDump::VIEWS.
var parityViews = []struct {
	group string
	query [][2]string
}{
	{"model", nil}, {"release", nil}, {"build", nil},
	{"build", [][2]string{{"confirmed", "1"}}}, {"model", [][2]string{{"confirmed", "1"}}}, {"release", [][2]string{{"confirmed", "1"}}},
	{"build", [][2]string{{"state", "works"}}}, {"build", [][2]string{{"state", "gap"}}}, {"build", [][2]string{{"state", "untested"}}}, {"build", [][2]string{{"state", "fails"}}},
	{"model", [][2]string{{"state", "regression"}}}, {"model", [][2]string{{"state", "partial"}}},
	{"build", [][2]string{{"layer", "aurora"}}}, {"model", [][2]string{{"layer", "omarchy"}, {"state", "works"}}},
	{"build", [][2]string{{"runs", "tester"}}}, {"model", [][2]string{{"runs", "tester"}}}, {"model", [][2]string{{"runs", "community"}}},
	{"build", [][2]string{{"gen", "m1"}}}, {"model", [][2]string{{"gen", "M2"}}}, {"model", [][2]string{{"kernel", "7.1.12-2-11-ARCH"}}},
	{"build", [][2]string{{"from", "2026-09-27"}, {"to", "2026-09-27"}}}, {"release", [][2]string{{"tool", "0.1.8"}}}, {"build", [][2]string{{"chip", "M1 Pro"}, {"soc", "t6000"}}},
	{"build", [][2]string{{"model", "j416c"}}}, {"model", [][2]string{{"model", "j416c"}, {"confirmed", "1"}}},
	{"build", [][2]string{{"build", "1937418.362376005140001"}}}, {"build", [][2]string{{"set", "apple-test-0052582276d9-20260927"}}},
	{"build", [][2]string{{"release", "4.0.0-alpha"}, {"stack", "converged"}}}, {"build", [][2]string{{"stack", "mx-mac"}}}, {"model", [][2]string{{"model", "j999"}}},
}

func publicIDs(runs []*Run) []any {
	list := []any{}
	for _, run := range runs {
		list = append(list, run.PublicID)
	}
	return list
}

func tallyPairs(tally Tally) []any {
	list := []any{}
	for _, count := range tally {
		list = append(list, []any{count.State, count.N})
	}
	return list
}

func dumpCell(cell *Cell, summary bool) map[string]any {
	sources := cell.Sources()
	out := map[string]any{"state": cell.State(), "tentative": cell.Tentative(), "display": cell.DisplayState(), "class": cell.CSSClass(), "summary": cell.Summary(),
		"counts": cell.Counts(), "machines": cell.Machines(), "tester_machines": cell.VerifiedMachines(), "hidden": cell.Hidden(),
		"tallies": tallyPairs(cell.Tallies), "tester_tallies": tallyPairs(cell.VerifiedTallies), "runs": publicIDs(cell.Runs), "sources": publicIDs(sources), "kernels": nil}
	if summary {
		kernels := []any{}
		seen := map[string]bool{}
		for _, run := range sources {
			if !seen[run.Kernel()] {
				seen[run.Kernel()] = true
				kernels = append(kernels, run.Kernel())
			}
		}
		out["kernels"] = kernels
	}
	return out
}

func dumpRows(rows []*Row, features []*Object, summary bool) []any {
	list := []any{}
	for _, row := range rows {
		c := row.Configuration
		cells := map[string]any{}
		for _, feature := range features {
			cells[id(feature)] = dumpCell(row.Cell(id(feature)), summary)
		}
		list = append(list, map[string]any{"board": c.Board, "stack": c.Stack, "version": c.Version, "build": c.Build, "build_id": c.BuildID, "label": c.Label(),
			"model": row.ModelName, "chip": row.Chip, "soc": row.SoC, "runs": publicIDs(row.Runs), "cells": cells})
	}
	return list
}

func dumpOptions(options *Options, keys []string) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		if key == "from" || key == "to" || key == "confirmed" {
			continue
		}
		choices := []any{}
		for _, choice := range options.For(key) {
			choices = append(choices, []any{choice.Value, choice.Words})
		}
		out[key] = choices
	}
	return out
}

func aggregationDump(t testing.TB, p *Published) map[string]any {
	runs := []any{}
	for _, run := range p.Runs() {
		counts := func(confirmed bool) map[string]any {
			c := run.ResultCounts(confirmed)
			return map[string]any{"pass": c.Pass, "fail": c.Fail, "gap": c.Gap, "skip": c.Skip}
		}
		states := map[string]any{}
		for _, feature := range run.testedFeatures() {
			states[feature] = run.FeatureState(feature)
		}
		entry := map[string]any{"id": run.PublicID, "release": run.OmarchyVersion(), "kernel": run.Kernel(), "tester": run.Verified, "build": nil,
			"labels": []any{run.Configuration(ByBuild).Label(), run.Configuration(ByRelease).Label(), run.Configuration(ByModel).Label()},
			"counts": counts(false), "confirmed_counts": counts(true), "states": states}
		if b := run.Build(); b != nil {
			entry["build"] = map[string]any{"id": b.ID, "label": b.Label(), "words": b.Words(), "set": b.Set(), "set_source": b.SetSource(),
				"commit": b.Commit, "stamp": b.Stamp, "kernel": b.Kernel, "boot": b.Boot, "image": b.ImageWords()}
		}
		runs = append(runs, entry)
	}
	matrices, history := map[string]any{}, map[string]any{}
	for _, view := range parityViews {
		name, query := view.group, url.Values{}
		for _, pair := range view.query {
			name += " " + pair[0] + "=" + pair[1]
			query.Set(pair[0], pair[1])
		}
		by := map[string]Grouping{"build": ByBuild, "release": ByRelease, "model": ByModel}[view.group]
		filter := p.Filter(query, nil)
		m := p.Matrix(by, filter)
		features, chips := []any{}, []any{}
		for _, feature := range m.Features() {
			features = append(features, id(feature))
		}
		for _, chip := range filter.Chips() {
			chips = append(chips, []any{chip.Key, chip.Value})
		}
		matrices[name] = map[string]any{"features": features, "runs": m.RunsCount(), "empty": m.Empty(), "chips": chips, "rows": dumpRows(m.Rows(), m.Features(), by == ByModel)}
		if by == ByModel {
			builds := p.Matrix(ByBuild, filter.Only(RunKeys))
			history[name] = dumpRows(builds.Rows(), builds.Features(), false)
		}
	}
	lineup := []any{}
	for _, row := range p.Lineup().Rows() {
		boards, statuses := []any{}, map[string]any{}
		for _, board := range row.Boards {
			results := map[string]any{}
			for _, area := range LineupAreas {
				results[area.Area] = dumpCell(board.Result(area.Area), false)
			}
			boards = append(boards, map[string]any{"board": board.Board, "runs": publicIDs(board.Runs), "tester_runs": board.VerifiedRuns(), "results": results})
		}
		for _, area := range LineupAreas {
			status := row.Status(area.Area)
			entry := map[string]any{"state": status.State, "source": status.Source, "note": status.Note, "words": status.Words(), "unconfirmed": status.Unconfirmed(), "summary": nil}
			if status.Cell != nil {
				entry["summary"] = status.Cell.Summary()
			}
			statuses[area.Area] = entry
		}
		lineup = append(lineup, map[string]any{"name": row.Name, "chip": row.Chip, "tested": row.Tested(), "boards": boards, "statuses": statuses})
	}
	regressions := []any{}
	for _, open := range p.OpenRegressions(p.Runs()) {
		entry := map[string]any{"feature": open.Feature, "board": open.Board, "stack": open.Stack, "runs": publicIDs(open.Runs), "pass": nil}
		if pass := open.Pass(); pass != nil {
			entry["pass"] = pass.PublicID
		}
		regressions = append(regressions, entry)
	}
	builds := []any{}
	for _, on := range RunsByBuild(p.Runs()) {
		builds = append(builds, []any{on.Build.Words(), publicIDs(on.Runs)})
	}
	return map[string]any{"runs": runs, "matrices": matrices, "history": history, "lineup": lineup, "regressions": regressions,
		"exports": dumpExports(t, p), "gaps": dumpGaps(p), "candidates": dumpCandidates(p), "builds": builds,
		"options": map[string]any{"all": dumpOptions(p.Options(nil), FilterKeys),
			"features model=j416c": dumpOptions(p.Options(p.Filter(url.Values{"model": {"j416c"}}, FeatureKeys)), FeatureKeys)}}
}

// oldWords are the old site's words for what the lab calls a Verified run
// or a Community run; nothing else in what the two count differs.
var oldWords = strings.NewReplacer("tester-verified, ", "verified, ", "tester machine", "verified machine", "a tester confirms", "a Verified run confirms",
	"tester runs only", "Verified runs only", "community runs only", "Community runs only")

// differences lists where two counts differ. The old site's nothing is the
// port's empty text.
func differences(path string, old, ours any, out *[]string) {
	if len(*out) >= 40 {
		return
	}
	// The exports keep the old site's words: what reads them must keep working.
	if s, ok := old.(string); ok && !strings.HasPrefix(path, ".exports") {
		old = oldWords.Replace(s)
	}
	if old == nil {
		old = ""
	}
	if ours == nil {
		ours = ""
	}
	switch a := old.(type) {
	case map[string]any:
		b, ok := ours.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: the old site has an object, the port %v", path, ours))
			return
		}
		keys := map[string]bool{}
		for key := range a {
			keys[key] = true
		}
		for key := range b {
			keys[key] = true
		}
		sorted := make([]string, 0, len(keys))
		for key := range keys {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		for _, key := range sorted {
			x, inOld := a[key]
			y, inOurs := b[key]
			if !inOld || !inOurs {
				*out = append(*out, fmt.Sprintf("%s.%s: in the old site's %v, in the port's %v", path, key, inOld, inOurs))
				continue
			}
			differences(path+"."+key, x, y, out)
		}
	case []any:
		b, ok := ours.([]any)
		if !ok || len(a) != len(b) {
			*out = append(*out, fmt.Sprintf("%s: the old site has %v, the port %v", path, old, ours))
			return
		}
		for i := range a {
			differences(fmt.Sprintf("%s[%d]", path, i), a[i], b[i], out)
		}
	default:
		if old != ours {
			*out = append(*out, fmt.Sprintf("%s: the old site has %#v, the port %#v", path, old, ours))
		}
	}
}

func TestTheCountsAreTheOldSites(t *testing.T) {
	list, entries := seededCorpus(t)
	p := NewPublished(testCatalogue(t), list)
	// Through JSON, so both sides are read alike.
	var ours any
	text, err := json.MarshalIndent(aggregationDump(t, p), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	decode(t, text, &ours)
	if dir := os.Getenv("PAGE_PARITY_DIR"); dir != "" {
		corpus, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"corpus.json": corpus, "go-aggregation.json": text} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	file := railsAggregation
	if given := os.Getenv("PAGE_PARITY_AGGREGATION"); given != "" {
		file = given
	}
	data := read(t, file)
	if strings.HasSuffix(file, ".gz") {
		unzipped, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if data, err = io.ReadAll(unzipped); err != nil {
			t.Fatal(err)
		}
	}
	var old map[string]any
	decode(t, data, &old)
	runs, _ := old["runs"].([]any)
	views, _ := old["matrices"].(map[string]any)
	exports, _ := old["exports"].(map[string]any)
	if len(runs) != len(list) || len(list) != seededReports || len(views) != len(parityViews) || len(exports) != len(parityExports) {
		t.Fatalf("the old site counted %d reports in %d views and wrote %d exports; the corpus has %d reports, want %d, in %d views, with %d exports",
			len(runs), len(views), len(exports), len(list), seededReports, len(parityViews), len(parityExports))
	}
	var differ []string
	differences("", any(old), ours, &differ)
	for _, d := range differ {
		t.Error(d)
	}
}
