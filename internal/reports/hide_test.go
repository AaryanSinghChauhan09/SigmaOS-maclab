package reports

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

// A report is Published, Hidden or deleted. Hiding takes it off everything
// the public counts and keeps the row; a Maintainer still reads it.

func TestHidingTakesAReportOffWhatIsPublishedAndShowingPutsItBack(t *testing.T) {
	d := newDesk(t)
	first := d.post(t, run(t, 1), "a").text("id")
	d.now = d.now.Add(time.Minute)
	second := d.post(t, run(t, 2), "b").text("id")
	cat := testCatalogue(t)
	published := func() []string {
		t.Helper()
		p, err := d.store.Published(cat)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range p.Runs() {
			ids = append(ids, r.PublicID)
		}
		return ids
	}

	d.now = d.now.Add(time.Hour)
	if err := d.store.Hide(first, d.now); err != nil {
		t.Fatal(err)
	}
	if got := published(); len(got) != 1 || got[0] != second {
		t.Errorf("published after hiding: %v", got)
	}
	report, err := d.store.Report(first)
	if err != nil || !report.Hidden {
		t.Fatalf("the hidden report: %+v %v", report, err)
	}
	if got := d.store.list(t, `SELECT hidden_at || ' ' || updated_at FROM reports WHERE public_id = ?`, first); got[0] != itoa(d.now.UnixMicro())+" "+itoa(d.now.UnixMicro()) {
		t.Errorf("hidden_at and updated_at: %v", got)
	}
	if _, run, err := d.store.Around(cat, first); err != nil || run != nil {
		t.Errorf("a Hidden report has a public page: %v", err)
	}

	// Hiding it again changes nothing: it stays hidden since the first time.
	if err := d.store.Hide(first, d.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := d.store.list(t, `SELECT hidden_at FROM reports WHERE public_id = ?`, first); got[0] != itoa(d.now.UnixMicro()) {
		t.Errorf("hidden twice, hidden_at: %v", got)
	}

	d.now = d.now.Add(2 * time.Hour)
	if err := d.store.Show(first, d.now); err != nil {
		t.Fatal(err)
	}
	if got := published(); len(got) != 2 || got[0] != first {
		t.Errorf("published after showing: %v", got)
	}
	if got := d.store.list(t, `SELECT coalesce(hidden_at, 'none') || ' ' || updated_at FROM reports WHERE public_id = ?`, first); got[0] != "none "+itoa(d.now.UnixMicro()) {
		t.Errorf("after showing: %v", got)
	}

	for name, change := range map[string]func(string, time.Time) error{"Hide": d.store.Hide, "Show": d.store.Show} {
		if err := change("no-such-report", d.now); !errors.Is(err, ErrNoReport) {
			t.Errorf("%s of a report that is not there: %v", name, err)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestAMaintainerReadsEveryReportHiddenOnesToo(t *testing.T) {
	d := newDesk(t)
	cat := testCatalogue(t)
	first := d.post(t, run(t, 1), "a").text("id")
	d.now = d.now.Add(time.Minute)
	second := d.post(t, run(t, 2), "b").text("id")
	if err := d.store.Hide(first, d.now); err != nil {
		t.Fatal(err)
	}

	every, err := d.store.Every(cat)
	if err != nil {
		t.Fatal(err)
	}
	runs := every.Runs()
	if len(runs) != 2 || runs[0].PublicID != first || !runs[0].Hidden || runs[1].PublicID != second || runs[1].Hidden {
		t.Fatalf("every report, oldest first: %v", runs)
	}
	if runs[0].Model() == "" || runs[0].Build() == nil {
		t.Error("a Hidden report is not read whole")
	}

	// A Hidden report's page, for a Maintainer alone.
	p, run, err := d.store.AroundAny(cat, first)
	if err != nil || run == nil || !run.Hidden || run.PublicID != first || p.Run(first) != run {
		t.Fatalf("a Maintainer's read of a Hidden report: %v %v", run, err)
	}
	if _, run, err := d.store.AroundAny(cat, second); err != nil || run == nil || run.Hidden {
		t.Errorf("a Maintainer's read of a published report: %v %v", run, err)
	}
	if _, run, err := d.store.AroundAny(cat, "no-such-report"); err != nil || run != nil {
		t.Errorf("a report that is not there: %v %v", run, err)
	}
}

// A Hidden Verified pass is no pass to fall from: what the public counts
// never reads it, whoever asks.
func TestAHiddenReportCountsForNothingEvenWhenAMaintainerReadsAnother(t *testing.T) {
	d := newDesk(t)
	cat := testCatalogue(t)
	pass := d.post(t, run(t, 1), "a").text("id")
	if _, err := d.store.db.Exec(`UPDATE reports SET verified = 1 WHERE public_id = ?`, pass); err != nil {
		t.Fatal(err)
	}
	d.now = d.now.Add(time.Minute)
	failed := d.post(t, goldenWith(t, "m2-max-image2", "gpu.driver", "fails"), "b")
	if failed.status != 201 {
		t.Fatalf("the failing run: %d %v", failed.status, failed.body)
	}
	const feature = "gpu"
	_, run, err := d.store.AroundAny(cat, failed.text("id"))
	if err != nil || run == nil || run.RegressedFrom(feature) == nil {
		t.Fatalf("no regression from the Verified pass: %v %v", run, err)
	}
	if err := d.store.Hide(pass, d.now); err != nil {
		t.Fatal(err)
	}
	if _, run, _ := d.store.AroundAny(cat, failed.text("id")); run == nil || run.RegressedFrom(feature) != nil {
		t.Error("a Hidden pass still makes a regression")
	}
}
