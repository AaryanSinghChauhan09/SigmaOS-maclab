package server

import (
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/api"
)

func TestNextSummaryIsTheNext0800InBrisbane(t *testing.T) {
	for at, want := range map[string]string{
		"2026-11-14T21:59:59Z": "2026-11-14T22:00:00Z", // 07:59:59 on the 15th in Brisbane
		"2026-11-14T22:00:00Z": "2026-11-15T22:00:00Z",
		"2026-11-15T03:00:00Z": "2026-11-15T22:00:00Z",
		"2026-01-01T00:00:00Z": "2026-01-01T22:00:00Z", // no summer time in Brisbane
	} {
		from, _ := time.Parse(time.RFC3339, at)
		if got := nextSummary(from).UTC().Format(time.RFC3339); got != want {
			t.Errorf("after %s the summary goes at %s, want %s", at, got, want)
		}
	}
}

// The summary goes at 08:00 Brisbane and not before, once a day, with each
// Mac of the lab.
func TestTheDailySummaryGoesAt0800Brisbane(t *testing.T) {
	// The last 08:00 Brisbane that has passed, less two minutes: the harness
	// enrolls its Mac by the wall clock, which labd's must not be ahead of.
	day := nextSummary(time.Now()).AddDate(0, 0, -1)
	clock := agent.NewFakeClock(day.Add(-2 * time.Minute))
	sig := &fakeSignal{}
	dir := signalDir(t)
	l := newLabWith(t, labConfig{clock: clock, config: func(c *Config) { c.Signal, c.SignalDir = sig, dir }})
	if r, body := l.post("/api/devices/sim-mac/lease", api.LeaseRequest{Holder: "ada", TTLSec: 4 * 3600}); r.StatusCode != 200 {
		t.Fatalf("lease: %s %s", r.Status, body)
	}
	summaries := func() []string {
		var out []string
		for _, s := range sig.lines() {
			if s.group == testLabGroup && strings.HasPrefix(s.text, "Omarchy Mac Lab, ") {
				out = append(out, s.text)
			}
		}
		return out
	}
	// until moves the clock on in the summary loop's own steps until n summaries went.
	until := func(n int, limit time.Duration) {
		t.Helper()
		for moved := time.Duration(0); len(summaries()) < n; moved += 30 * time.Second {
			if moved > limit {
				t.Fatalf("%d summaries after %s, want %d", len(summaries()), moved, n)
			}
			clock.Advance(30 * time.Second)
			time.Sleep(20 * time.Millisecond)
		}
	}

	for i := 0; i < 3; i++ { // to 07:59:30
		clock.Advance(30 * time.Second)
		time.Sleep(50 * time.Millisecond)
	}
	if got := summaries(); len(got) != 0 {
		t.Fatalf("a summary went before 08:00: %q", got)
	}
	until(1, 10*time.Minute)
	if at := clock.Now().In(brisbane).Format("15:04"); at != "08:00" {
		t.Errorf("the summary went at %s Brisbane, want 08:00", at)
	}
	want := "Omarchy Mac Lab, " + day.In(brisbane).Format("Mon 2 Jan") + "\nsim-mac (Configuration unknown): leased by ada until " + day.In(brisbane).Format("Mon") + " 11:58 AEST\nRequests to lend waiting: 0"
	if got := summaries()[0]; got != want {
		t.Errorf("the summary is\n%s\nwant\n%s", got, want)
	}

	// The rest of the day, to 07:59 the next morning: no second one.
	for i := 0; i < 47; i++ {
		clock.Advance(30 * time.Minute)
		time.Sleep(5 * time.Millisecond)
	}
	clock.Advance(29 * time.Minute)
	time.Sleep(50 * time.Millisecond)
	if got := summaries(); len(got) != 1 {
		t.Fatalf("%d summaries in one day: %q", len(got), got)
	}
	until(2, 10*time.Minute)
	if got := summaries()[1]; !strings.HasPrefix(got, "Omarchy Mac Lab, "+day.AddDate(0, 0, 1).In(brisbane).Format("Mon 2 Jan")+"\nsim-mac (Configuration unknown): ") {
		t.Errorf("the next day's summary is\n%s", got)
	}
}

// A labd that starts again in the minute of the summary does not post the
// day's summary twice.
func TestTheDaysSummaryIsMarkedOnce(t *testing.T) {
	h := newLines4Test(t, nil, "")
	if !h.l.markOnce("daily-summary", "2026-11-15") || h.l.markOnce("daily-summary", "2026-11-15") {
		t.Fatal("the day's summary can be marked twice, or not at all")
	}
	if !h.l.markOnce("daily-summary", "2026-11-16") {
		t.Fatal("the next day's summary is refused")
	}
}
