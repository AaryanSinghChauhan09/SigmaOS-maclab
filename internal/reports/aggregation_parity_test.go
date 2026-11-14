package reports

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
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
	add(parse(t, read(t, filepath.Join(goldenDir, "benchmarks", "m2-max-converged.json"))), now.Add(-30*time.Minute), "demo-a", true)
	return list, entries
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

func aggregationDump(p *Published) map[string]any {
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
	return map[string]any{"runs": runs, "matrices": matrices, "history": history, "lineup": lineup, "regressions": regressions,
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
	if s, ok := old.(string); ok {
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
	text, err := json.MarshalIndent(aggregationDump(p), "", "  ")
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
	if len(runs) != len(list) || len(list) != 17 || len(views) != len(parityViews) {
		t.Fatalf("the old site counted %d reports in %d views; the corpus has %d reports, want 17, in %d views", len(runs), len(views), len(list), len(parityViews))
	}
	var differ []string
	differences("", any(old), ours, &differ)
	for _, d := range differ {
		t.Error(d)
	}
}
