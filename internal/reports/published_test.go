package reports

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/catalogue"
)

// Translated from the old site's tests of what its pages count:
// site/test/models/matrix_cell_test.rb, omarchy_release_test.rb, and the
// counting behind matrix_test.rb, model_results_test.rb, mac_lineup_test.rb,
// filters_test.rb, build_identity_test.rb, answered_by_default_test.rb and
// regressions_test.rb. The pages themselves are internal/server's to test.

func testCatalogue(t testing.TB) *Catalogue {
	t.Helper()
	cat, err := LoadCatalogue(catalogue.JSON)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// shelf is a set of reports as reports.db would hold them, each uploaded a
// minute after the one before.
type shelf struct {
	t    testing.TB
	list []*Report
	at   time.Time
}

func newShelf(t testing.TB) *shelf {
	return &shelf{t: t, at: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

// add shelves a report from a machine. A machine named "verified:x" is
// signed in as an M team member: its runs are Verified runs.
func (s *shelf) add(doc *Object, machine string) *Report {
	s.t.Helper()
	r := &Report{ID: int64(len(s.list) + 1), PublicID: fmt.Sprintf("run%02d", len(s.list)+1), SchemaVersion: 1, Body: encode(s.t, doc),
		CreatedAt: s.at, MachineID: "key:" + machine}
	if person, ok := strings.CutPrefix(machine, "verified:"); ok {
		r.Verified, r.Handle, r.AccountID = true, "team-"+person, int64(len(person))*1000+int64(person[0])
	}
	s.at = s.at.Add(time.Minute)
	s.list = append(s.list, r)
	return r
}

func (s *shelf) published() *Published { return NewPublished(testCatalogue(s.t), s.list) }

func check(t testing.TB, doc *Object, id string) *Object {
	t.Helper()
	for _, item := range doc.members["checks"].([]any) {
		if c := item.(*Object); c.members["id"] == id {
			return c
		}
	}
	t.Fatalf("the report has no check %s", id)
	return nil
}

// goldenWith is the golden report with some checks' status and outcome changed.
func goldenWith(t testing.TB, name string, outcomes ...string) *Object {
	t.Helper()
	doc := golden(t, name)
	for i := 0; i+1 < len(outcomes); i += 2 {
		c := check(t, doc, outcomes[i])
		status := "fail"
		switch outcomes[i+1] {
		case "works":
			status = "pass"
		case "not-tested":
			status = "skip"
		}
		c.set("status", status)
		c.members["classification"].(*Object).set("outcome", outcomes[i+1])
	}
	return doc
}

func setPackage(t testing.TB, doc *Object, name, version string) *Object {
	t.Helper()
	for _, item := range at(doc, "system", "packages").([]any) {
		if pkg := item.(*Object); pkg.members["name"] == name {
			pkg.set("version", version)
			return doc
		}
	}
	t.Fatalf("the report has no package %s", name)
	return nil
}

func rowOf(t testing.TB, m *Matrix, board, stack, version string) *Row {
	t.Helper()
	var found []*Row
	for _, row := range m.Rows() {
		if c := row.Configuration; c.Board == board && c.Stack == stack && c.Version == version {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d rows for %s %s %s, want 1", len(found), board, stack, version)
	}
	return found[0]
}

const m2 = "j416c"

func TestAPackageVersionNamesItsReleaseAndWhetherItIsTheReleaseItself(t *testing.T) {
	for pkg, release := range map[string]string{
		"4.0.2-2": "4.0.2",
		"0.2.3-1": "0.2.3",
		"4.0.0.alpha.quattro.r1790484851.g0052582276d9-1.362956774610001": "4.0.0-alpha",
		"4.0.0.r7547.gf2b8378-1": "4.0.0-dev",
		"4.0.4.r7099.gb8ed395-1": "4.0.4-dev",
		"4.0.3rc4-1":             "4.0.3-rc4",
		"4.5.0rc2-1":             "4.5.0-rc2",
		"4.0.4-mac.1-2":          "4.0.4-mac.1",
		"not-a-version":          "not-a-version",
	} {
		if got := ReleaseOf(pkg); got != release {
			t.Errorf("ReleaseOf(%q) = %q, want %q", pkg, got, release)
		}
	}
}

func TestPreReleasesSortBelowTheirRelease(t *testing.T) {
	releases := []string{"4.0.0-dev", "4.0.0", "4.0.0-alpha", "4.0.3-rc4"}
	sort.SliceStable(releases, func(i, j int) bool { return newGemVersion(releases[i]).compare(newGemVersion(releases[j])) < 0 })
	if got := strings.Join(releases, " "); got != "4.0.0-alpha 4.0.0-dev 4.0.0 4.0.3-rc4" {
		t.Errorf("sorted: %s", got)
	}
}

// What Ruby itself answers: Gem::Version's order and its "correct?", the
// release of each package version, and the two patterns the old site wrote
// with a lookahead (overnight/reference-16/ruby-cases.rb wrote the file).
func TestVersionsAndTheTwoLookaheadsAreRubys(t *testing.T) {
	var cases struct {
		Versions []string          `json:"versions"`
		Correct  map[string]bool   `json:"correct"`
		Order    []string          `json:"order"`
		Releases map[string]string `json:"releases"`
		Commits  map[string]string `json:"commits"`
		Handles  map[string]bool   `json:"handles"`
		Kernels  []string          `json:"kernels_sorted"`
	}
	decode(t, read(t, filepath.Join("testdata", "ruby-cases.json")), &cases)
	if len(cases.Versions) < 40 || len(cases.Commits) < 10 || len(cases.Handles) < 15 {
		t.Fatalf("the file holds %d versions, %d commits, %d handles", len(cases.Versions), len(cases.Commits), len(cases.Handles))
	}
	for version, correct := range cases.Correct {
		if got := gemVersionCorrect(version); got != correct {
			t.Errorf("correct?(%q) = %v, Ruby says %v", version, got, correct)
		}
	}
	for i, a := range cases.Versions {
		for j, b := range cases.Versions {
			want := int(cases.Order[i][j]) - '1'
			if got := newGemVersion(a).compare(newGemVersion(b)); got != want {
				t.Errorf("%q <=> %q = %d, Ruby says %d", a, b, got, want)
			}
		}
	}
	for pkg, release := range cases.Releases {
		if got := ReleaseOf(pkg); got != release {
			t.Errorf("ReleaseOf(%q) = %q, Ruby says %q", pkg, got, release)
		}
	}
	for version, commit := range cases.Commits {
		if got := first(commitOf(version), 7); got != commit {
			t.Errorf("the commit of %q = %q, Ruby says %q", version, got, commit)
		}
	}
	for handle, valid := range cases.Handles {
		if got := HandleShaped(handle); got != valid {
			t.Errorf("HandleShaped(%q) = %v, Ruby says %v", handle, got, valid)
		}
	}
	kernels := append([]string(nil), cases.Kernels...)
	sort.Strings(kernels)
	sort.SliceStable(kernels, func(i, j int) bool { return naturalLess(kernels[i], kernels[j]) })
	if !reflect.DeepEqual(kernels, cases.Kernels) {
		t.Errorf("kernels sort as %v, Ruby says %v", kernels, cases.Kernels)
	}
}

// A model summary's per-check cell: which skipped checks still count.
func TestWhichSkippedChecksStillCountInAModelSummary(t *testing.T) {
	cell := func(status, outcome string) *Cell {
		s := newShelf(t)
		doc := golden(t, "m2-max-image2")
		var only []any
		for _, item := range doc.members["checks"].([]any) {
			if c := item.(*Object); c.members["id"] == "ports.thunderbolt" {
				c.set("status", status)
				c.members["classification"].(*Object).set("outcome", outcome)
				only = append(only, c)
			}
		}
		if len(only) != 1 {
			t.Fatal("the golden report has no ports.thunderbolt check")
		}
		doc.set("checks", only)
		s.add(doc, "verified:neo")
		p := s.published()
		return cellFromChecks(p.Runs(), text(at(only[0], "classification", "feature")), false)
	}
	if got := cell("skip", "not-applicable").State(); got != "not-applicable" {
		t.Errorf("a skip on hardware the Mac doesn't have: %q, want not-applicable", got)
	}
	if got := cell("skip", "not-tested").State(); got != "not-tested" {
		t.Errorf("any other skip: %q, want not-tested", got)
	}
}

func TestCommunityRunsColourACellOnlyOnceTwoMachinesAgree(t *testing.T) {
	s := newShelf(t)
	s.add(golden(t, "m2-max-image2"), "a")
	cell := rowOf(t, s.published().Matrix(ByBuild, nil), m2, "converged", "4.0.0-alpha").Cell("gpu")
	if cell.State() != "" || cell.CSSClass() != "cell cell-unconfirmed cell-hint-works" || !strings.HasPrefix(cell.Summary(), "community, unconfirmed: 1 machine works") {
		t.Errorf("one Community run: state %q, class %q, summary %q", cell.State(), cell.CSSClass(), cell.Summary())
	}

	s.add(golden(t, "m2-max-image2"), "a")
	row := rowOf(t, s.published().Matrix(ByBuild, nil), m2, "converged", "4.0.0-alpha")
	if row.Cell("gpu").State() != "" || len(row.Runs) != 2 {
		t.Errorf("two runs of one machine: state %q over %d runs", row.Cell("gpu").State(), len(row.Runs))
	}

	s.add(golden(t, "m2-max-image2"), "b")
	row = rowOf(t, s.published().Matrix(ByBuild, nil), m2, "converged", "4.0.0-alpha")
	if cell := row.Cell("gpu"); cell.State() != "works" || cell.CSSClass() != "cell cell-works" || cell.Summary() != "works: 2 machines works" {
		t.Errorf("two machines: state %q, class %q, summary %q", cell.State(), cell.CSSClass(), cell.Summary())
	}
	// packages.hardware passes and setup.first-boot-hardware fails: partial on both machines.
	if got := row.Cell("first-boot-hardware-setup").State(); got != "partial" {
		t.Errorf("first-boot-hardware-setup: %q, want partial", got)
	}
}

func TestReportsFromBeforeMachineKeysCountAsOneMachine(t *testing.T) {
	s := newShelf(t)
	s.add(golden(t, "m2-max-image2"), "a").MachineID = ""
	s.add(golden(t, "m2-max-image2"), "b").MachineID = ""
	if got := rowOf(t, s.published().Matrix(ByBuild, nil), m2, "converged", "4.0.0-alpha").Cell("gpu").State(); got != "" {
		t.Errorf("state %q, want unconfirmed", got)
	}
}

func TestAgreeingFailuresMissingSupportAndUnknownHardwareEachHaveTheirState(t *testing.T) {
	s := newShelf(t)
	for _, machine := range []string{"a", "b"} {
		s.add(goldenWith(t, "m2-max-image2", "display.backlight", "fails", "input.ambient-light", "not-in-aurora",
			"power.battery", "unknown-hardware", "network.bluetooth", "not-applicable"), machine)
	}
	row := rowOf(t, s.published().Matrix(ByBuild, nil), m2, "converged", "4.0.0-alpha")
	for feature, want := range map[string]string{"brightness": "fails", "aop": "missing", "battery-info": "unknown", "bluetooth": "not-applicable"} {
		if got := row.Cell(feature).State(); got != want {
			t.Errorf("%s: %q, want %q", feature, got, want)
		}
	}
}

func TestMachinesThatDisagreeLeaveTheCellUnconfirmedUntilEachSideHasTwo(t *testing.T) {
	s := newShelf(t)
	brightness := func() *Cell {
		return rowOf(t, s.published().Matrix(ByBuild, nil), m2, "converged", "4.0.0-alpha").Cell("brightness")
	}
	s.add(golden(t, "m2-max-image2"), "a")
	s.add(goldenWith(t, "m2-max-image2", "display.backlight", "fails"), "b")
	cell := brightness()
	if cell.State() != "" || !strings.Contains(cell.Summary(), "1 machine works, 1 machine doesn't work") || !strings.Contains(cell.CSSClass(), "cell-hint-partial") {
		t.Errorf("a tie: state %q, summary %q, class %q", cell.State(), cell.Summary(), cell.CSSClass())
	}
	s.add(golden(t, "m2-max-image2"), "c")
	if got := brightness().State(); got != "works" {
		t.Errorf("two against one: %q, want works", got)
	}
	s.add(goldenWith(t, "m2-max-image2", "display.backlight", "fails"), "d")
	if got := brightness().State(); got != "partial" {
		t.Errorf("two against two: %q, want partial", got)
	}
}

func TestEachMachineCountsWithTheLatestStateItTested(t *testing.T) {
	s := newShelf(t)
	s.add(goldenWith(t, "m2-max-image2", "display.backlight", "fails"), "a")
	s.add(golden(t, "m2-max-image2"), "a")
	s.add(goldenWith(t, "m2-max-image2", "display.backlight", "not-tested"), "a")
	s.add(golden(t, "m2-max-image2"), "b")
	if got := rowOf(t, s.published().Matrix(ByBuild, nil), m2, "converged", "4.0.0-alpha").Cell("brightness").State(); got != "works" {
		t.Errorf("brightness: %q, want works", got)
	}
}

func TestFeaturesNoMachineTestedAreNotTested(t *testing.T) {
	s := newShelf(t)
	s.add(golden(t, "m1-pro-mx-mac"), "a")
	s.add(golden(t, "m1-pro-mx-mac"), "b")
	row := rowOf(t, s.published().Matrix(ByBuild, nil), "j314s", "mx-mac", "4.0.2-dev")
	if row.Cell("dcp").State() != "not-tested" || row.Cell("vendor-firmware").State() != "fails" {
		t.Errorf("dcp %q, vendor-firmware %q", row.Cell("dcp").State(), row.Cell("vendor-firmware").State())
	}
}

func TestRowsArePerModelAndStackVersionNewestVersionFirst(t *testing.T) {
	s := newShelf(t)
	s.add(golden(t, "m2-max-image2"), "a")
	s.add(golden(t, "m1-pro-mx-mac"), "a")
	newer := golden(t, "m2-max-image2")
	at(newer, "system", "packages", 0).(*Object).set("version", "4.1.0-1")
	s.add(newer, "a")
	p := s.published()
	m := p.Matrix(ByBuild, nil)
	var versions []string
	for _, row := range m.RowsForBoard(m2) {
		versions = append(versions, row.Configuration.Version)
	}
	if len(m.Rows()) != 3 || strings.Join(versions, " ") != "4.1.0 4.0.0-alpha" {
		t.Errorf("%d rows; the M2's versions: %v", len(m.Rows()), versions)
	}
	if len(m.Features()) != len(p.Catalogue().TestedFeatures()) {
		t.Errorf("%d columns, want every tested feature", len(m.Features()))
	}
	mx := p.Matrix(ByBuild, p.Filter(url.Values{"stack": {"mx-mac"}}, nil))
	if rows := mx.Rows(); len(rows) != 1 || rows[0].Configuration.Board != "j314s" {
		t.Errorf("stack=mx-mac: %d rows", len(rows))
	}
}

// Model summaries span releases and builds, with their evidence and history intact.
type summaries struct {
	*shelf
	t testing.TB
}

const oldKernel, newKernel = "7.1.12-2-2-ARCH", "7.1.12-2-11-ARCH"

func (s summaries) upload(outcome, kernel, release, machine, stack string) *Report {
	doc := goldenWith(s.t, "m2-max-image2", "display.backlight", outcome)
	at(doc, "machine").(*Object).set("kernel", kernel)
	at(doc, "system").(*Object).set("stack", stack)
	setPackage(s.t, doc, "omarchy", release)
	setPackage(s.t, doc, "linux-aurora", kernel)
	return s.add(doc, machine)
}

func (s summaries) cell(p *Published, feature, stack string, query url.Values) *Cell {
	return rowOf(s.t, p.Matrix(ByModel, p.Filter(query, nil)), m2, stack, "").Cell(feature)
}

func kernels(cell *Cell) string {
	var list []string
	seen := map[string]bool{}
	for _, run := range cell.Sources() {
		if !seen[run.Kernel()] {
			seen[run.Kernel()] = true
			list = append(list, run.Kernel())
		}
	}
	sort.Strings(list)
	return strings.Join(list, " ")
}

func TestTheModelSummaryReplacesAFailureWithALaterPassAndKeepsItThroughASkip(t *testing.T) {
	s := summaries{newShelf(t), t}
	s.upload("fails", oldKernel, "4.0.0-1", "verified:a", "converged")
	passed := s.upload("works", newKernel, "4.1.0-1", "verified:a", "converged")
	s.upload("not-tested", "7.1.13-1-1-ARCH", "4.2.0-1", "verified:a", "converged")
	p := s.published()
	cell := s.cell(p, "brightness", "converged", nil)
	if cell.State() != "works" || kernels(cell) != newKernel || cell.Sources()[0].PublicID != passed.PublicID {
		t.Errorf("state %q from kernels %q, source %s", cell.State(), kernels(cell), cell.Sources()[0].PublicID)
	}
	if history := p.Matrix(ByBuild, nil).History(); len(history[[2]string{m2, "converged"}]) != 3 {
		t.Errorf("the history has %d builds, want 3", len(history[[2]string{m2, "converged"}]))
	}
	if got := p.Matrix(ByModel, nil).RunsCount(); got != 3 {
		t.Errorf("%d runs behind the summary, want 3", got)
	}
}

func TestALaterFailureOnAnotherReleaseSupersedesThePassAndASkipKeepsTheRegression(t *testing.T) {
	s := summaries{newShelf(t), t}
	s.upload("works", oldKernel, "4.0.0-1", "verified:a", "converged")
	failure := s.upload("fails", newKernel, "4.1.0-1", "verified:a", "converged")
	s.upload("not-tested", "7.1.13-1-1-ARCH", "4.2.0-1", "verified:a", "converged")
	p := s.published()
	cell := s.cell(p, "brightness", "converged", url.Values{"state": {"regression"}})
	if cell.State() != "regression" || cell.Sources()[0].PublicID != failure.PublicID {
		t.Errorf("state %q, source %s", cell.State(), cell.Sources()[0].PublicID)
	}
}

func TestUploadOrderDecidesTheLatestResultEvenWhenAMachineRetestsAnOlderKernel(t *testing.T) {
	s := summaries{newShelf(t), t}
	s.upload("works", newKernel, "4.0.0-1", "verified:a", "converged")
	s.upload("fails", oldKernel, "4.1.0-1", "verified:a", "converged")
	p := s.published()
	if cell := s.cell(p, "brightness", "converged", nil); cell.State() != "regression" || kernels(cell) != oldKernel {
		t.Errorf("state %q from %q", cell.State(), kernels(cell))
	}
	query := url.Values{"kernel": {newKernel}}
	if cell := s.cell(p, "brightness", "converged", query); cell.State() != "works" || kernels(cell) != newKernel {
		t.Errorf("kernel=%s: state %q from %q", newKernel, cell.State(), kernels(cell))
	}
	if got := len(p.Matrix(ByBuild, p.Filter(query, RunKeys)).Rows()); got != 1 {
		t.Errorf("kernel=%s: %d builds in the history, want 1", newKernel, got)
	}
}

func TestACarriedResultIdentifiesItsActualSourceKernelAndDate(t *testing.T) {
	s := summaries{newShelf(t), t}
	earlier := s.upload("works", oldKernel, "4.0.0-1", "verified:a", "converged")
	s.upload("not-tested", newKernel, "4.0.0-1", "verified:a", "converged")
	cell := s.cell(s.published(), "brightness", "converged", nil)
	if kernels(cell) != oldKernel || !cell.Sources()[0].CreatedAt.Equal(earlier.CreatedAt) {
		t.Errorf("kernels %q, tested at %s", kernels(cell), cell.Sources()[0].CreatedAt)
	}
}

func TestSkippingOneCheckRetainsItsPreviousResultWhenAnotherCheckOfTheFeaturePasses(t *testing.T) {
	s := summaries{newShelf(t), t}
	s.add(goldenWith(t, "m2-max-image2", "gpu.driver", "fails", "gpu.vulkan", "works"), "a")
	later := goldenWith(t, "m2-max-image2", "gpu.driver", "not-tested", "gpu.vulkan", "works")
	at(later, "machine").(*Object).set("kernel", newKernel)
	s.add(later, "a")
	cell := s.cell(s.published(), "gpu", "converged", nil)
	if cell.DisplayState() != "partial" || kernels(cell) != newKernel+" "+oldKernel {
		t.Errorf("state %q from %q", cell.DisplayState(), kernels(cell))
	}
}

func TestConfirmedSummariesStillShowTheUnconfirmedRunsThatEstablishedAgreement(t *testing.T) {
	s := summaries{newShelf(t), t}
	s.upload("works", oldKernel, "4.0.0-1", "b", "converged")
	s.upload("works", newKernel, "4.0.0-1", "c", "converged")
	p := s.published()
	query := url.Values{"confirmed": {"1"}}
	if got := s.cell(p, "brightness", "converged", query).State(); got != "works" {
		t.Errorf("confirmed summary: %q", got)
	}
	// Result filters narrow summaries; history keeps all matching runs as evidence.
	history := p.Matrix(ByBuild, p.Filter(query, RunKeys)).Rows()
	if len(history) != 2 || history[0].Cell("brightness").State() != "" || history[1].Cell("brightness").State() != "" {
		t.Errorf("the history: %d rows", len(history))
	}
}

func TestANewerCommunityResultCannotClaimTheSourceOfAVerifiedResult(t *testing.T) {
	s := summaries{newShelf(t), t}
	s.upload("works", oldKernel, "4.0.0-1", "verified:a", "converged")
	s.upload("fails", newKernel, "4.0.0-1", "b", "converged")
	p := s.published()
	if cell := s.cell(p, "brightness", "converged", nil); cell.State() != "works" || !cell.Verified() || kernels(cell) != oldKernel {
		t.Errorf("state %q, verified %v, kernels %q", cell.State(), cell.Verified(), kernels(cell))
	}
	if cell := s.cell(p, "brightness", "converged", url.Values{"runs": {"community"}}); cell.State() != "" || kernels(cell) != newKernel {
		t.Errorf("runs=community: state %q, kernels %q", cell.State(), kernels(cell))
	}
}

func TestDifferentMachinesStillDisagreeAndDifferentStacksHaveSeparateSummaries(t *testing.T) {
	s := summaries{newShelf(t), t}
	s.upload("works", oldKernel, "4.0.0-1", "verified:a", "converged")
	s.upload("fails", newKernel, "4.0.0-1", "verified:bb", "converged")
	s.upload("works", newKernel, "4.0.0-1", "verified:a", "mx-mac")
	p := s.published()
	if got := len(p.Matrix(ByModel, nil).Rows()); got != 2 {
		t.Errorf("%d summaries, want 2", got)
	}
	if cell := s.cell(p, "brightness", "converged", nil); cell.State() != "partial" || kernels(cell) != newKernel+" "+oldKernel {
		t.Errorf("converged: %q from %q", cell.State(), kernels(cell))
	}
	if got := s.cell(p, "brightness", "mx-mac", nil).State(); got != "works" {
		t.Errorf("mx-mac: %q", got)
	}
}

// A human check answered yes by a bare Enter never colours the matrix or
// counts for a Verified run; a typed yes counts as before.
func TestAnswersByDefaultColourNothing(t *testing.T) {
	const trackpad = "input.trackpad-gestures"
	yes := func(byDefault bool) *Object {
		doc := goldenWith(t, "m2-max-image2", trackpad, "works")
		if byDefault {
			check(t, doc, trackpad).set("answered_by_default", true)
		} else {
			*check(t, doc, trackpad) = *check(t, doc, trackpad).without("answered_by_default")
		}
		return doc
	}
	touchpad := func(s *shelf) *Cell {
		return rowOf(t, s.published().Matrix(ByModel, nil), m2, "converged", "").Cell("touchpad")
	}
	s := newShelf(t)
	s.add(yes(true), "a")
	s.add(yes(true), "b")
	if got := touchpad(s).State(); got != "not-tested" {
		t.Errorf("Enter-only yeses from two machines: %q", got)
	}
	s.add(yes(false), "c")
	s.add(yes(false), "d")
	if got := touchpad(s).State(); got != "works" {
		t.Errorf("typed yeses from two machines: %q", got)
	}

	s = newShelf(t)
	s.add(yes(true), "verified:a")
	if cell := touchpad(s); cell.State() != "not-tested" || cell.Verified() {
		t.Errorf("a Verified run's Enter-only yes: %q, verified %v", cell.State(), cell.Verified())
	}
	s.add(yes(false), "verified:a")
	if cell := touchpad(s); cell.State() != "works" || !cell.Verified() {
		t.Errorf("a Verified run's typed yes: %q, verified %v", cell.State(), cell.Verified())
	}

	m3 := productionShelf(t).published().Run("xHGd4Toq7io8CSHYYFhTjZBD")
	byDefault := 0
	for _, c := range m3.Checks() {
		if c.AnsweredByDefault {
			byDefault++
		}
	}
	if all, confirmed := m3.ResultCounts(false).Pass, m3.ResultCounts(true).Pass; byDefault == 0 || all-confirmed != byDefault {
		t.Errorf("%d answers by default; %d pass, %d confirmed", byDefault, all, confirmed)
	}
}

// A failure is a regression only when an earlier Verified run on the same
// Mac model and stack found the feature working.
func TestAFailureIsARegressionOnlyAfterAVerifiedPassOnTheSameModelAndStack(t *testing.T) {
	fails := func() *Object { return goldenWith(t, "m2-max-image2", "gpu.driver", "fails", "gpu.vulkan", "fails") }
	state := func(p *Published, r *Report) string { return p.Run(r.PublicID).FeatureState("gpu") }

	s := newShelf(t)
	failing := s.add(fails(), "a")
	p := s.published()
	if state(p, failing) != "fails" || len(p.OpenRegressions(p.Runs())) != 0 {
		t.Errorf("no earlier pass: %q, %d open", state(p, failing), len(p.OpenRegressions(p.Runs())))
	}

	s = newShelf(t)
	pass := s.add(golden(t, "m2-max-image2"), "verified:t")
	failing = s.add(setPackage(t, fails(), "omarchy", "4.1.0-1"), "a")
	p = s.published()
	if state(p, failing) != "regression" || p.Run(failing.PublicID).RegressedFrom("gpu").PublicID != pass.PublicID {
		t.Errorf("after a Verified pass, on another version: %q", state(p, failing))
	}
	open := p.OpenRegressions(p.Runs())
	if len(open) != 1 || open[0].Feature != "gpu" || open[0].Board != m2 || open[0].Stack != "converged" || open[0].Pass().PublicID != pass.PublicID ||
		open[0].Latest().Configuration(ByRelease).Label() != "converged 4.1.0" {
		t.Errorf("open regressions: %+v", open)
	}

	// A Community pass or a hidden Verified pass doesn't make it a regression.
	s = newShelf(t)
	s.add(golden(t, "m2-max-image2"), "b")
	failing = s.add(fails(), "a")
	if got := state(s.published(), failing); got != "fails" {
		t.Errorf("after a Community pass: %q", got)
	}
	hidden := s.add(golden(t, "m2-max-image2"), "verified:t")
	hidden.Hidden = true
	later := s.add(fails(), "a")
	if got := state(s.published(), later); got != "fails" {
		t.Errorf("after a hidden Verified pass: %q", got)
	}
	hidden.Hidden = false
	if got := state(s.published(), later); got != "regression" {
		t.Errorf("once it is shown again: %q", got)
	}

	// Only the same model and stack, and only an earlier pass, count.
	s = newShelf(t)
	onMx := golden(t, "m2-max-image2")
	at(onMx, "system").(*Object).set("stack", "mx-mac")
	s.add(onMx, "verified:t")
	otherStack := s.add(fails(), "a")
	otherModel := s.add(goldenWith(t, "m1-pro-converged", "gpu.driver", "fails", "gpu.vulkan", "fails"), "a")
	s.add(golden(t, "m2-max-image2"), "verified:t")
	p = s.published()
	if state(p, otherStack) != "fails" || state(p, otherModel) == "regression" || p.Run(otherModel.PublicID).RegressedFrom("gpu") != nil {
		t.Errorf("a pass on mx-mac, on another Mac, or uploaded after: %q, %q", state(p, otherStack), state(p, otherModel))
	}
}

func TestARegressionStaysOpenUntilANewerRunOnThatModelAndStackWorksAgain(t *testing.T) {
	fails := func() *Object { return goldenWith(t, "m2-max-image2", "gpu.driver", "fails", "gpu.vulkan", "fails") }
	gpu := func(p *Published) []OpenRegression {
		var open []OpenRegression
		for _, o := range p.OpenRegressions(p.Runs()) {
			if o.Feature == "gpu" {
				open = append(open, o)
			}
		}
		return open
	}
	s := newShelf(t)
	s.add(golden(t, "m2-max-image2"), "verified:t")
	s.add(fails(), "a")
	s.add(fails(), "b")
	if open := gpu(s.published()); len(open) != 1 || len(open[0].Runs) != 2 {
		t.Errorf("open: %+v", open)
	}
	s.add(golden(t, "m2-max-image2"), "a")
	if open := gpu(s.published()); len(open) != 0 {
		t.Errorf("after it works again: %+v", open)
	}
}

func TestTwoMachinesAgreeingOnARegressionColourTheCell(t *testing.T) {
	s := newShelf(t)
	s.add(golden(t, "m2-max-image2"), "verified:t")
	for _, machine := range []string{"a", "b"} {
		s.add(setPackage(t, goldenWith(t, "m2-max-image2", "gpu.driver", "fails", "gpu.vulkan", "fails"), "omarchy", "4.1.0-1"), machine)
	}
	m := s.published().Matrix(ByRelease, nil)
	gpu := rowOf(t, m, m2, "converged", "4.1.0").Cell("gpu")
	if gpu.State() != "regression" || !strings.Contains(gpu.CSSClass(), "cell-regression") {
		t.Errorf("4.1.0: %q %q", gpu.State(), gpu.CSSClass())
	}
	if got := rowOf(t, m, m2, "converged", "4.0.0-alpha").Cell("gpu").State(); got != "works" {
		t.Errorf("4.0.0-alpha: %q", got)
	}
}

// The bring-up table: every Mac, tested or not, with its GPU and display state from the runs.
func lineupRow(t testing.TB, l *Lineup, board string) *LineupRow {
	t.Helper()
	for _, row := range l.Rows() {
		for _, b := range row.Boards {
			if b.Board == board {
				return row
			}
		}
	}
	t.Fatalf("no row for %s", board)
	return nil
}

func stateAndSource(t testing.TB, l *Lineup, board, area string) string {
	status := lineupRow(t, l, board).Status(area)
	return status.State + " " + status.Source
}

func TestEveryMacOfTheCatalogueShowsWithWhatTheCatalogueExpects(t *testing.T) {
	p := newShelf(t).published()
	l := p.Lineup()
	if len(l.Rows()) != len(p.Catalogue().LineupModels()) || len(l.Boards()) != len(p.Catalogue().Boards().Keys()) || len(l.UntestedBoards()) != len(l.Boards()) {
		t.Errorf("%d rows, %d boards, %d untested", len(l.Rows()), len(l.Boards()), len(l.UntestedBoards()))
	}
	if lineupRow(t, l, "j504").Tested() {
		t.Error("j504 shows as tested")
	}
	for at, want := range map[string]string{"j616s gpu": "missing catalogue", "j416c gpu": "works catalogue", "j413 display": "works catalogue",
		"j700 gpu": "missing catalogue", "j514c gpu": "missing catalogue"} {
		board, area, _ := strings.Cut(at, " ")
		if got := stateAndSource(t, l, board, area); got != want {
			t.Errorf("%s: %q, want %q", at, got, want)
		}
	}
	if got := lineupRow(t, l, "j704").Status("gpu").Note; got != "Asahi: not in Asahi's tables" {
		t.Errorf("j704's note: %q", got)
	}
	if got := lineupRow(t, l, "j514c").Status("gpu").Words(); got != "catalogue: not supported yet" {
		t.Errorf("j514c's words: %q", got)
	}
}

func TestTheRunsDecideOnceAMacIsTestedOverTheCatalogue(t *testing.T) {
	s := newShelf(t)
	s.add(goldenWith(t, "m2-max-image2", "gpu.vulkan", "fails"), "verified:one")
	l := s.published().Lineup()
	if stateAndSource(t, l, m2, "gpu") != "partial runs" || stateAndSource(t, l, m2, "display") != "works runs" || lineupRow(t, l, m2).Status("gpu").Unconfirmed() {
		t.Errorf("gpu %q, display %q", stateAndSource(t, l, m2, "gpu"), stateAndSource(t, l, m2, "display"))
	}
}

func TestAReinstalledMacsNewerVerifiedRunReplacesItsOlderOne(t *testing.T) {
	s := newShelf(t)
	older := s.add(goldenWith(t, "m2-max-image2", "gpu.driver", "not-in-asahi", "gpu.vulkan", "not-in-asahi"), "verified:one")
	newer := s.add(golden(t, "m2-max-image2"), "b")
	newer.Verified, newer.Handle, newer.AccountID = true, older.Handle, older.AccountID
	status := lineupRow(t, s.published().Lineup(), m2).Status("gpu")
	if status.State != "works" || status.Source != "runs" || !strings.Contains(status.Cell.Summary(), "1 verified machine works") {
		t.Errorf("%q %q %q", status.State, status.Source, status.Cell.Summary())
	}
}

func TestANewerPassRetiresOlderFailuresOnItsOwnBoardNotOnAnother(t *testing.T) {
	s := newShelf(t)
	older := func() *Object {
		return goldenWith(t, "m2-max-image2", "gpu.driver", "not-in-asahi", "gpu.vulkan", "not-in-asahi")
	}
	otherBoard := older()
	at(otherBoard, "machine").(*Object).set("board", "j414c")
	s.add(older(), "verified:one")
	s.add(otherBoard, "verified:three")
	s.add(golden(t, "m2-max-image2"), "verified:twoo")
	row := lineupRow(t, s.published().Lineup(), m2)
	states := map[string]string{}
	for _, board := range row.Boards {
		states[board.Board] = board.Result("gpu").DisplayState()
	}
	if states["j416c"] != "works" || states["j414c"] != "missing" || row.Status("gpu").State != "partial" {
		t.Errorf("boards %v, row %q", states, row.Status("gpu").State)
	}
}

func TestOneCommunityMachineIsUnconfirmedTwoThatAgreeConfirmIt(t *testing.T) {
	s := newShelf(t)
	s.add(golden(t, "m2-max-image2"), "a")
	row := lineupRow(t, s.published().Lineup(), m2)
	if status := row.Status("gpu"); status.State != "works" || status.Source != "runs" || !status.Unconfirmed() {
		t.Errorf("one machine: %+v", status)
	}
	if !row.Tested() || len(row.Boards[0].Runs)+len(row.Boards[1].Runs) != 1 {
		t.Errorf("the row's boards: tested %v", row.Tested())
	}
	s.add(golden(t, "m2-max-image2"), "b")
	if lineupRow(t, s.published().Lineup(), m2).Status("gpu").Unconfirmed() {
		t.Error("two machines that agree are still unconfirmed")
	}
}

// The production reports (schema/golden/production/reports.json) span four
// chips, three days and six tool versions; builds.json is the build the CLI
// derives for each.
type production struct {
	Reports []struct {
		ID         string `json:"id"`
		UploadedAt string `json:"uploaded_at"`
	} `json:"reports"`
}

func productionShelf(t testing.TB) *shelf {
	t.Helper()
	data := read(t, filepath.Join(goldenDir, "production", "reports.json"))
	var index production
	decode(t, data, &index)
	docs := at(parse(t, data), "reports").([]any)
	s := newShelf(t)
	for i, entry := range index.Reports {
		at, err := time.Parse(time.RFC3339, entry.UploadedAt)
		if err != nil {
			t.Fatal(err)
		}
		doc := docs[i].(*Object).members["report"].(*Object).without("signature")
		s.list = append(s.list, &Report{ID: int64(i + 1), PublicID: entry.ID, SchemaVersion: 1, Body: encode(t, doc), CreatedAt: at, MachineID: "key:machine-" + entry.ID})
	}
	return s
}

func ids(runs []*Run) string {
	var list []string
	for _, run := range runs {
		list = append(list, run.PublicID)
	}
	sort.Strings(list)
	return strings.Join(list, " ")
}

const (
	m3ID       = "xHGd4Toq7io8CSHYYFhTjZBD"
	m3Build    = "f60e1ba.2026092602"
	m1AirBuild = "0052582.362956774610001"
	m1AirSet   = "apple-test-0052582276d9-20260927"
)

func TestEveryOldReportIsOnTheBuildTheCLIDerives(t *testing.T) {
	var want struct {
		Builds   map[string]struct{ Build, Words string } `json:"builds"`
		Examples []struct {
			Packages     map[string]string
			CandidateSet string `json:"candidate_set"`
			Built        string `json:"built"`
			PackageSet   string `json:"package_set_sha256"`
			Words, Why   string
		} `json:"examples"`
	}
	decode(t, read(t, filepath.Join(goldenDir, "production", "builds.json")), &want)
	p := productionShelf(t).published()
	if len(want.Builds) != 11 || len(p.Runs()) != 11 {
		t.Fatalf("%d expected builds over %d production reports, want 11 of each", len(want.Builds), len(p.Runs()))
	}
	for id, build := range want.Builds {
		got := p.Run(id).Build()
		if got == nil || got.ID != build.Build || got.Words() != build.Words {
			t.Errorf("%s: build %+v, want %s, %s", id, got, build.Build, build.Words)
		}
		if label := p.Run(id).Configuration(ByRelease).Label(); label != "converged 4.0.0-alpha" {
			t.Errorf("%s is on %q", id, label)
		}
	}
	for _, example := range want.Examples {
		var image []ImageFact
		if example.Built != "" {
			image = append(image, ImageFact{"built", example.Built})
		}
		if example.PackageSet != "" {
			image = append(image, ImageFact{"package_set_sha256", example.PackageSet})
		}
		if got := NewBuild(example.Packages, example.CandidateSet, image, "").Words(); got != example.Words {
			t.Errorf("%s: %q, want %q", example.Why, got, example.Words)
		}
	}
	if len(want.Examples) == 0 {
		t.Error("builds.json has no examples")
	}
}

func TestTheMatrixHasARowPerBuildFiltersToOneAndCanMergeAReleasesBuilds(t *testing.T) {
	p := productionShelf(t).published()
	m := p.Matrix(ByBuild, nil)
	var m2Builds []string
	seen := map[string]bool{}
	for _, row := range m.Rows() {
		key := row.Configuration.Board + " " + row.Configuration.Build
		if seen[key] {
			t.Errorf("two rows for %s", key)
		}
		seen[key] = true
		if row.Configuration.Board == m2 {
			m2Builds = append(m2Builds, row.Configuration.Build)
		}
	}
	sort.Strings(m2Builds)
	// The M2 Max's runtime with a kernel installed by hand is its own row.
	if want := []string{"1937418.362376005140001 (linux-aurora 7.1.12.aurora2-10, omarchy-mac-boot 20260926-1)",
		"1937418.362376005140001 (linux-aurora 7.1.12.aurora2-10.90, omarchy-mac-boot 20260926-1)"}; !reflect.DeepEqual(m2Builds, want) {
		t.Errorf("the M2 Max's builds: %q", m2Builds)
	}
	if rows := m.RowsForBoard("j613"); len(rows) != 1 || rows[0].Configuration.BuildID != m3Build {
		t.Errorf("the M3's rows: %d", len(rows))
	}
	for _, row := range p.Matrix(ByBuild, p.Filter(url.Values{"build": {m1AirBuild}}, nil)).Rows() {
		if row.Configuration.BuildID != m1AirBuild {
			t.Errorf("build=%s shows %s", m1AirBuild, row.Configuration.BuildID)
		}
	}
	boards := map[string]int{}
	for _, row := range p.Matrix(ByRelease, nil).Rows() {
		boards[row.Configuration.Board]++
		if row.Configuration.Build != "" || row.Configuration.BuildID != "" {
			t.Errorf("a release's row names a build: %+v", row.Configuration)
		}
	}
	for board, rows := range boards {
		if rows != 1 {
			t.Errorf("%d release rows for %s", rows, board)
		}
	}
}

func imageRun(t testing.TB, change func(image *Object)) *Object {
	const set = "apple-test-1937418f520b-20260926"
	doc := golden(t, "m2-max-converged")
	image := parse(t, []byte(`{"format":"1","platform":"apple-silicon","candidate_set":"`+set+`","candidate_source_commit":"1937418f520b`+strings.Repeat("0", 28)+
		`","builder_commit":"c6fedc32c`+strings.Repeat("1", 31)+`","builder_tree_clean":"true","image_profile":"test","package_set_sha256":"`+strings.Repeat("ab", 32)+
		`","built":"2026-09-28T03:04:05Z"}`))
	if change != nil {
		change(image)
	}
	at(doc, "system").(*Object).set("candidate_set", set)
	at(doc, "system").(*Object).set("image", image)
	return doc
}

func TestARunOnAnImageNamingItsSetIsLabelledByTheSet(t *testing.T) {
	const set = "apple-test-1937418f520b-20260926"
	s := newShelf(t)
	s.add(imageRun(t, nil), "a")
	p := s.published()
	build := p.Runs()[0].Build()
	words := set + " (runtime 1937418.362376005140001, linux-aurora 7.1.12.aurora2-10, omarchy-mac-boot 20260926-1, built 2026-09-28T03:04:05Z, package set abababababab)"
	if build.Words() != words || build.Label() != set || build.SetSource() != "image" || !p.NamesSet(set) {
		t.Errorf("words %q, label %q, source %q", build.Words(), build.Label(), build.SetSource())
	}
	if want := "format 1, platform apple-silicon, candidate_set " + set + ", candidate_source_commit 1937418f520b, builder_commit c6fedc32c111, " +
		"builder_tree_clean true, image_profile test, package_set_sha256 abababababab, built 2026-09-28T03:04:05Z"; build.ImageWords() != want {
		t.Errorf("the image record reads %q", build.ImageWords())
	}
	if got := p.Options(nil).For("build"); len(got) != 1 || got[0] != (Choice{words, set + " (1)"}) {
		t.Errorf("the build choices: %+v", got)
	}

	// Two images of one set are two builds: the image's package set and build time tell them apart.
	s.add(imageRun(t, func(image *Object) { image.set("built", "2026-09-29T01:00:00Z") }), "b")
	s.add(imageRun(t, func(image *Object) { image.set("package_set_sha256", strings.Repeat("cd", 32)) }), "c")
	p = s.published()
	if got := len(p.Matrix(ByBuild, nil).RowsForBoard(m2)); got != 3 {
		t.Errorf("%d rows for three images, want 3", got)
	}
	for _, run := range p.Runs() {
		if got := p.Filter(url.Values{"build": {run.Build().Words()}}, nil).Runs(p.Runs()); len(got) != 1 || got[0] != run {
			t.Errorf("build=%s passes %d runs", run.Build().Words(), len(got))
		}
	}
	if got := p.Filter(url.Values{"build": {set}}, nil).Runs(p.Runs()); len(got) != 3 {
		t.Errorf("build=%s passes %d runs, want 3", set, len(got))
	}
}

func TestEveryRunFilterNarrowsAndTheyCombine(t *testing.T) {
	s := productionShelf(t)
	p := s.published()
	all := p.Runs()
	where := func(keep func(*Run) bool) string {
		var kept []*Run
		for _, run := range all {
			if keep(run) {
				kept = append(kept, run)
			}
		}
		return ids(kept)
	}
	passing := func(pairs ...string) string {
		query := url.Values{}
		for i := 0; i+1 < len(pairs); i += 2 {
			query.Set(pairs[i], pairs[i+1])
		}
		return ids(p.Filter(query, nil).Runs(all))
	}
	count := func(pairs ...string) int { return len(strings.Fields(passing(pairs...))) }
	day := func(run *Run) string { return run.CreatedAt.Format(time.DateOnly) }

	for name, c := range map[string]struct{ got, want string }{
		"chip":        {passing("chip", "M1 Pro"), where(func(r *Run) bool { return r.Chip() == "M1 Pro" })},
		"gen":         {passing("gen", "m1"), where(func(r *Run) bool { return strings.HasPrefix(r.Chip(), "M1") })},
		"soc":         {passing("soc", "t6021"), where(func(r *Run) bool { return r.SoC() == "t6021" })},
		"model":       {passing("model", "j613"), where(func(r *Run) bool { return r.Board() == "j613" })},
		"gen, upper":  {passing("gen", "M3"), m3ID},
		"tool":        {passing("tool", "0.1.8"), where(func(r *Run) bool { return r.ToolVersion() == "0.1.8" })},
		"stack":       {passing("stack", "converged"), ids(all)},
		"other stack": {passing("stack", "mx-mac"), ""},
		"release":     {passing("release", "4.0.0-alpha"), ids(all)},
		"no release":  {passing("release", "4.0.0"), ""},
		"build":       {passing("build", m1AirBuild), where(func(r *Run) bool { return r.Build().ID == m1AirBuild })},
		"set":         {passing("set", m1AirSet), passing("build", m1AirBuild)},
		"one day":     {passing("from", "2026-09-27", "to", "2026-09-27"), where(func(r *Run) bool { return day(r) == "2026-09-27" })},
		"from":        {passing("from", "2026-09-28"), m3ID},
		"to":          {passing("to", "2026-09-26"), where(func(r *Run) bool { return day(r) < "2026-09-27" })},
		"bad dates":   {passing("from", "yesterday", "to", "2026-02-31"), ids(all)},
		"and": {passing("gen", "m1", "tool", "0.1.8", "from", "2026-09-27"),
			where(func(r *Run) bool {
				return strings.HasPrefix(r.Chip(), "M1") && r.ToolVersion() == "0.1.8" && day(r) >= "2026-09-27"
			})},
		"nothing":       {passing("gen", "m1", "chip", "M2 Max"), ""},
		"unknown model": {passing("model", "j999"), ""},
		"unknown tool":  {passing("tool", "9.9.9"), ""},
		"not choices":   {passing("stack", "bogus", "runs", "someone", "gen", "m9", "confirmed", "yes"), ids(all)},
		"kernel":        {passing("kernel", "7.1.12-2-11-ARCH"), where(func(r *Run) bool { return r.Kernel() == "7.1.12-2-11-ARCH" })},
		"long build":    {passing("build", "apple-test-1937418f520b-20260926 ("+strings.Repeat("x", 400)+")"), ""},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", name, c.got, c.want)
		}
	}
	for name, c := range map[string][2]int{"M1 Pro": {count("chip", "M1 Pro"), 4}, "m1": {count("gen", "m1"), 6}, "0.1.8": {count("tool", "0.1.8"), 4},
		"the set": {count("set", m1AirSet), 4}, "one day": {count("from", "2026-09-27", "to", "2026-09-27"), 7},
		"and": {count("gen", "m1", "tool", "0.1.8", "from", "2026-09-27"), 2}} {
		if c[0] != c[1] {
			t.Errorf("%s: %d runs, want %d", name, c[0], c[1])
		}
	}

	// Chips, in the lab's order, each linking to the view without it.
	f := p.Filter(url.Values{"from": {"2026-09-27"}, "tool": {"0.1.8"}, "gen": {"M1"}}, nil)
	if got := strings.Join(f.Keys(), " "); got != "gen tool from" {
		t.Errorf("chips: %s", got)
	}
	if chips := f.Chips(); chips[0].Words != "generation: M1" || chips[1].Words != "tool version: 0.1.8" {
		t.Errorf("chips: %+v", chips)
	}
	if got := Query(f.Params("gen")); got != "?from=2026-09-27&tool=0.1.8" {
		t.Errorf("without gen: %s", got)
	}
	if got := Query(f.Params("", "group", "release")); got != "?from=2026-09-27&gen=m1&group=release&tool=0.1.8" {
		t.Errorf("with a group: %s", got)
	}
	if got := Query(p.Filter(nil, nil).Params("")); got != "" {
		t.Errorf("no filter: %q", got)
	}
	if !p.Filter(url.Values{"build": {"apple-test-1937418f520b-20260926 (" + strings.Repeat("x", 400) + ")"}}, nil).Any() || p.Filter(url.Values{"build": {strings.Repeat("x", 2049)}}, nil).Any() {
		t.Error("a build filter as long as an image's build words is kept, and no longer")
	}

	// Verified runs only, or Community runs only; runs=tester stays the wire name.
	for _, r := range s.list {
		r.Verified = r.PublicID == m3ID || strings.HasPrefix(r.PublicID, "Gbu7hc")
	}
	p = s.published()
	if a, b, c := len(p.Filter(url.Values{"runs": {"tester"}}, nil).Runs(p.Runs())), len(p.Filter(url.Values{"runs": {"community"}}, nil).Runs(p.Runs())),
		len(p.Filter(url.Values{"runs": {"all"}}, nil).Runs(p.Runs())); a != 2 || b != 9 || c != 11 {
		t.Errorf("runs=tester %d, community %d, all %d", a, b, c)
	}
}

func TestTheChoicesAreTheRunsOwnNewestFirst(t *testing.T) {
	s := productionShelf(t)
	extra := *s.list[0]
	doc := extra.Document()
	at(doc, "machine").(*Object).set("kernel", "7.1.12-2-100-ARCH")
	extra.ID, extra.PublicID, extra.Body = 99, "extra", encode(t, doc)
	s.list = append(s.list, &extra)
	p := s.published()
	options := p.Options(nil)
	var kernels []string
	for _, choice := range options.For("kernel") {
		kernels = append(kernels, choice.Value)
	}
	if kernels[0] != "7.1.12-2-100-ARCH" || indexOf(kernels, "7.1.12-2-11-ARCH") > indexOf(kernels, "7.1.12-2-10-ARCH") {
		t.Errorf("kernels: %v", kernels)
	}
	if got := options.For("gen"); len(got) < 3 || got[0] != (Choice{"m1", "M1"}) {
		t.Errorf("generations: %+v", got)
	}
	if got := options.For("set"); len(got) == 0 || indexOf(values(got), m1AirSet) < 0 {
		t.Errorf("sets: %+v", got)
	}
	if got := options.For("runs"); !reflect.DeepEqual(got, []Choice{{"tester", "Verified runs only"}, {"community", "Community runs only"}}) {
		t.Errorf("runs: %+v", got)
	}
	// Build choices follow the model and distinguish variants of one runtime.
	onM2 := p.Options(p.Filter(url.Values{"model": {m2}}, FeatureKeys)).For("build")
	var want []string
	for _, build := range RunsByBuild(p.Filter(url.Values{"model": {m2}}, nil).Runs(p.Runs())) {
		want = append(want, build.Build.Words())
	}
	if !reflect.DeepEqual(values(onM2), want) || len(onM2) < 2 {
		t.Errorf("the M2 Max's builds: %v, want %v", values(onM2), want)
	}
	variants := 0
	for _, choice := range onM2 {
		if strings.HasPrefix(choice.Words, choice.Value) {
			variants++
		}
	}
	if variants < 2 {
		t.Errorf("the variants of one runtime are not told apart: %+v", onM2)
	}
	if got := p.Options(nil).For("model"); indexOf(values(got), "j313") < 0 {
		t.Errorf("models: %+v", got)
	}
}

func values(choices []Choice) []string {
	var out []string
	for _, choice := range choices {
		out = append(out, choice.Value)
	}
	return out
}

func TestTheMatrixsResultFilters(t *testing.T) {
	s := productionShelf(t)
	p := s.published()
	matrix := func(pairs ...string) *Matrix {
		query := url.Values{}
		for i := 0; i+1 < len(pairs); i += 2 {
			query.Set(pairs[i], pairs[i+1])
		}
		return p.Matrix(ByBuild, p.Filter(query, nil))
	}
	if got := matrix("gen", "m1").RunsCount(); got != 6 {
		t.Errorf("gen=m1: %d runs", got)
	}
	if m := matrix("chip", "M3", "tool", "0.1.8", "to", "2026-09-28"); m.RunsCount() != 1 || m.Rows()[0].Configuration.Board != "j613" {
		t.Errorf("the M3 alone: %d runs", m.RunsCount())
	}
	// The count is of the runs behind the rows shown: result filters leave rows out.
	if m := matrix("gen", "m1", "state", "regression"); len(m.Rows()) != 0 || m.RunsCount() != 0 || m.Empty() {
		t.Errorf("no regression: %d rows, %d runs", len(m.Rows()), m.RunsCount())
	}
	if m := matrix("from", "2026-09-29"); !m.Empty() {
		t.Error("runs after the last day")
	}
	// One layer's columns.
	aurora := matrix("layer", "aurora").Features()
	tested := 0
	for _, feature := range p.Catalogue().TestedFeatures() {
		if layer(feature) == "aurora" {
			tested++
		}
	}
	if len(aurora) != tested || tested == 0 {
		t.Errorf("layer=aurora: %d columns, want %d", len(aurora), tested)
	}
	for _, feature := range aurora {
		if layer(feature) != "aurora" {
			t.Errorf("layer=aurora shows %s", id(feature))
		}
	}
	// The rows and columns with a cell in a state.
	for state, wanted := range map[string]string{"works": "works", "fails": "fails", "gap": "missing", "untested": "not-tested"} {
		m := matrix("state", state)
		for _, row := range m.Rows() {
			found := false
			for _, feature := range m.Features() {
				found = found || row.Cell(id(feature)).DisplayState() == wanted
			}
			if !found {
				t.Errorf("state=%s: a row without such a cell", state)
			}
		}
		for _, feature := range m.Features() {
			found := false
			for _, row := range m.Rows() {
				found = found || row.Cell(id(feature)).DisplayState() == wanted
			}
			if !found {
				t.Errorf("state=%s: the column %s has no such cell", state, id(feature))
			}
		}
		if len(m.Rows()) == 0 {
			t.Errorf("state=%s: no rows", state)
		}
	}
	// Confirmed only hides the cells Community machines don't agree on.
	all, confirmed := matrix(), matrix("confirmed", "1")
	if len(confirmed.Rows()) >= len(all.Rows()) || confirmed.RunsCount() >= len(p.Runs()) || len(confirmed.RowsForBoard("j613")) != 0 {
		t.Errorf("confirmed only: %d of %d rows", len(confirmed.Rows()), len(all.Rows()))
	}
	hidden := 0
	for _, row := range confirmed.Rows() {
		for _, feature := range confirmed.Features() {
			cell := row.Cell(id(feature))
			if cell.Hidden() {
				hidden++
				if cell.CSSClass() != "cell cell-not-tested cell-hidden" {
					t.Errorf("a hidden cell's class: %q", cell.CSSClass())
				}
			} else if cell.State() == "" {
				t.Error("an unconfirmed cell shows")
			}
		}
	}
	if hidden == 0 {
		t.Error("nothing was hidden")
	}
	// A Verified run colours cells on its own, so its row comes back.
	for _, r := range s.list {
		r.Verified = r.PublicID == m3ID
	}
	p = s.published()
	rows := p.Matrix(ByBuild, p.Filter(url.Values{"confirmed": {"1"}}, nil)).RowsForBoard("j613")
	if len(rows) != 1 || !rows[0].Cell("gpu").Verified() || !strings.Contains(rows[0].Cell("gpu").CSSClass(), "cell-tester") {
		t.Errorf("the M3's Verified run: %d rows", len(rows))
	}
}

// reports.db alone is enough: the store is opened in a folder that holds
// nothing else, and every count the public pages show is made from it.
func TestThePublishedReportsAreReadFromReportsDBAlone(t *testing.T) {
	store, path := openStore(t)
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	insert := `INSERT INTO reports (public_id, deletion_token_digest, schema_version, body, created_at, updated_at, hidden_at, machine_id, handle, account_id, verified) VALUES (?, 'digest', 1, ?, ?, ?, ?, ?, ?, ?, ?)`
	body := string(encode(t, golden(t, "m2-max-image2")))
	for i, r := range []struct {
		id      string
		hidden  any
		machine string
		handle  any
		account any
		v       int
	}{{"second", nil, "key:b", "ravi", 7, 1}, {"first", nil, "key:a", nil, nil, 0}, {"hidden", at.UnixMicro(), "key:c", nil, nil, 0}} {
		when := at.Add(time.Duration(1-i) * time.Hour).UnixMicro()
		if _, err := store.db.Exec(insert, r.id, body, when, when, r.hidden, r.machine, r.handle, r.account, r.v); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "reports.db") {
			t.Fatalf("the folder also holds %s", entry.Name())
		}
	}
	p, err := store.Published(testCatalogue(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Runs()) != 2 || p.Runs()[0].PublicID != "first" || p.Runs()[1].PublicID != "second" || !p.Runs()[1].Verified || p.Runs()[0].Verified {
		t.Fatalf("the published runs: %s", ids(p.Runs()))
	}
	if cell := rowOf(t, p.Matrix(ByModel, nil), m2, "converged", "").Cell("gpu"); cell.State() != "works" || !cell.Verified() {
		t.Errorf("gpu: %q, verified %v", cell.State(), cell.Verified())
	}
	if !lineupRow(t, p.Lineup(), m2).Tested() {
		t.Error("the bring-up table has no runs")
	}
}

// The copy the lab reads is the old site's file, as long as that is there.
func TestTheKnownCandidateSetsAreTheOldSitesFile(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("..", "..", "site", "config", "candidate_sets.yml"))
	if os.IsNotExist(err) {
		t.Skip("the old site is gone")
	}
	if err != nil || string(old) != candidateSetsYAML {
		t.Fatalf("candidate_sets.yml is not site/config/candidate_sets.yml (%v)", err)
	}
	if len(KnownSets()) < 6 || knownSetFor("4.0.0.alpha.quattro.r1790484851.g0052582276d9-1.362956774610001") != m1AirSet || knownSetFor("") != "" {
		t.Errorf("%d known sets", len(KnownSets()))
	}
	if _, err := parseKnownSets("a-set:\n  source_commit: abc\n  surprise: 1\n"); err == nil {
		t.Error("a key the file never had is read without complaint")
	}
}

func decode(t testing.TB, data []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatal(err)
	}
}
