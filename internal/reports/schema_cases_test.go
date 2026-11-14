package reports

import (
	"net/http"
	"strings"
	"testing"
)

// Upload rules the old site's page tests held, translated here so each has
// its own test: an image record, an inventory entry and a benchmark score
// take only what the schema names, each in its shape, and the golden
// benchmark report is stored with its scores.

func refusedByTheSchema(t *testing.T, d *desk, name string, report *Object) {
	t.Helper()
	a := d.post(t, report)
	if a.status != http.StatusUnprocessableEntity || !strings.Contains(a.text("error"), "does not match report schema v1") {
		t.Errorf("%s: %d %v", name, a.status, a.body)
	}
}

// From build_identity_test.rb.
func TestTheImageRecordTakesOnlyTheBuildersKeysEachInItsShape(t *testing.T) {
	d := newDesk(t)
	const set = "apple-test-1937418f520b-20260926"
	onNewImage := func(key string, value any) *Object {
		report := golden(t, "m2-max-converged")
		put(report, set, "system", "candidate_set")
		image := object("format", "1", "platform", "apple-silicon", "candidate_set", set,
			"candidate_source_commit", "1937418f520b"+strings.Repeat("0", 28), "builder_commit", "c6fedc32c"+strings.Repeat("1", 31),
			"builder_tree_clean", "true", "image_profile", "test", "package_set_sha256", strings.Repeat("ab", 32), "built", "2026-09-28T03:04:05Z")
		if key != "" {
			image.set(key, value)
		}
		return put(report, image, "system", "image")
	}
	refusedByTheSchema(t, d, "a key the builder does not write", onNewImage("hostname", "omarchy-marcelo"))
	refusedByTheSchema(t, d, "a build time in another shape", onNewImage("built", "Sep 28 2026"))
	refusedByTheSchema(t, d, "a package set that is no SHA-256", onNewImage("package_set_sha256", "marcelo"))
	// A second line can't ride on a field's pattern.
	refusedByTheSchema(t, d, "a second line in a field", onNewImage("image_profile", "test\nhostname=omarchy-marcelo"))
	if d.count(t) != 0 {
		t.Errorf("%d reports stored", d.count(t))
	}
	if a := d.post(t, onNewImage("", nil)); a.status != http.StatusCreated {
		t.Errorf("the image record as the builder writes it: %d %v", a.status, a.body)
	}
}

// From report_inventory_test.rb.
func TestInventoryFieldsOutsideTheSchemaAreRejected(t *testing.T) {
	d := newDesk(t)
	g := func() *Object { return golden(t, "m2-max-image2") }
	refusedByTheSchema(t, d, "a node's path", put(g(), "/soc/gpu@406400000", "inventory", "nodes", 0, "path"))
	refusedByTheSchema(t, d, "a compatible that is no compatible", put(g(), "serial-number = C02XXXXXXXXX", "inventory", "nodes", 0, "compatible"))
	if d.count(t) != 0 {
		t.Errorf("%d reports stored", d.count(t))
	}
}

// From benchmarks_test.rb.
func TestAScoreOutsideTheSchemaIsRefused(t *testing.T) {
	d := newDesk(t)
	score := func(change func(*Object)) *Object {
		report := golden(t, "benchmarks/m2-max-converged")
		change(at(report, "checks", 0, "score").(*Object))
		return report
	}
	for name, change := range map[string]func(*Object){
		"a unit the schema lacks": func(s *Object) { s.set("unit", "percent") },
		"a negative value":        func(s *Object) { s.set("value", integer(-1)) },
		"a value as text":         func(s *Object) { s.set("value", "2987") },
		"markup in the tool":      func(s *Object) { s.set("tool", "glmark2 <script>") },
		"a member of its own":     func(s *Object) { s.set("serial", "C02XYZ") },
	} {
		refusedByTheSchema(t, d, name, score(change))
	}
	noSuite := golden(t, "benchmarks/m2-max-converged")
	put(noSuite, at(noSuite, "checks", 0, "score").(*Object).without("suite"), "checks", 0, "score")
	refusedByTheSchema(t, d, "no suite", noSuite)
	if d.count(t) != 0 {
		t.Errorf("%d reports stored", d.count(t))
	}
}

// From benchmarks_test.rb.
func TestTheGoldenBenchmarkReportIsStoredWithItsScores(t *testing.T) {
	d := newDesk(t)
	a := d.post(t, golden(t, "benchmarks/m2-max-converged"))
	if a.status != http.StatusCreated {
		t.Fatalf("%d %v", a.status, a.body)
	}
	stored, err := d.store.Report(a.text("id"))
	if err != nil {
		t.Fatal(err)
	}
	checks := at(stored.Document(), "checks").([]any)
	for _, check := range checks {
		if text(at(check, "id")) != "benchmark.opengl" {
			continue
		}
		got, err := Canonical(at(check, "score"))
		if err != nil || string(got) != `{"suite":1,"tool":"glmark2 2023.01","unit":"points","value":2987}` {
			t.Errorf("the stored score: %s %v", got, err)
		}
		return
	}
	t.Error("the stored report has no benchmark.opengl")
}

// From answered_by_default_test.rb.
func TestAnsweredByDefaultCanOnlyBeTrue(t *testing.T) {
	d := newDesk(t)
	report := golden(t, "m2-max-image2")
	checks := at(report, "checks").([]any)
	for i, check := range checks {
		if text(at(check, "kind")) == "human" {
			put(report, false, "checks", i, "answered_by_default")
			refusedByTheSchema(t, d, "answered_by_default: false", report)
			return
		}
	}
	t.Fatal("the golden report has no check a person answers")
}
