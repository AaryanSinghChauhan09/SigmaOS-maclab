package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// resetLab is a lab of people and pages beside the mini's controller: the
// Lab-owned m1-pro shows its name on the right port, the Lab-owned m2-max
// is on the left and shows none, and vol-air is linus's.
func resetLab(t *testing.T) (*signInLab, *fakeMini) {
	t.Helper()
	mini := newFakeMini(t)
	l := pagesLab(t, func(c *Config) { c.OOB, c.SightEvery = mini.client, time.Hour })
	l.gh.AddUser(lrGrace, "grace")
	l.gh.AddUser(lrLinus, "linus")
	l.lrMac("m1-pro", "j316s", nil)
	l.lrMac("m2-max", "j414c", nil)
	l.lrMac("vol-air", "j313", lrVolunteers(api.AccessFull, l.s.now().Add(2*time.Hour)))
	l.lrLend("vol-air", lrLinus)
	mini.plug(portRight, nil, "m1-pro", productM1Pro)
	mini.plug(portLeft, nil, "", productM2Max)
	l.s.sight(context.Background())
	return l, mini
}

// Macs shows each Lab-owned Mac as sighted, not sighted or no cable, and
// Overview says where it is and why no Hard reset reaches it.
func TestMacsShowsEachLabOwnedMacAsSightedNotSightedOrNoCable(t *testing.T) {
	l, mini := resetLab(t)
	ravi := l.lrMaintainer()
	kind := func(mac string) string {
		t.Helper()
		rows, _ := macRows(ravi.macsPage(""))
		return rowText(part(t, rows[mac], `<td data-label="Kind">`, `</td>`))
	}
	if got := kind("m1-pro"); got != "Lab-owned sighted" {
		t.Errorf("m1-pro: %q", got)
	}
	if got := kind("m2-max"); got != "Lab-owned not sighted" {
		t.Errorf("m2-max: %q", got)
	}
	if got := kind("vol-air"); got != "Volunteer's" {
		t.Errorf("a Volunteer's Mac: %q", got)
	}
	facts := func(mac string) string {
		t.Helper()
		return rowText(part(t, ravi.macPage(mac, ""), `<dt>Cable</dt>`, `</dd>`))
	}
	if got := facts("m1-pro"); got != "Cable sighted on the right port" {
		t.Errorf("m1-pro's Overview: %q", got)
	}
	if got := facts("m2-max"); !strings.HasPrefix(got, "Cable not sighted No Hard reset: the lab has no Sighting of m2-max. A Maintainer can prove its port: `lab reset m2-max --prove left`") {
		t.Errorf("m2-max's Overview: %q", got)
	}
	if page := ravi.macPage("vol-air", ""); strings.Contains(page, "<dt>Cable</dt>") {
		t.Error("a Volunteer's Mac's Overview speaks of a cable")
	}

	mini.unplug(portLeft)
	l.s.sight(context.Background())
	if got := kind("m2-max"); got != "Lab-owned no cable" {
		t.Errorf("m2-max with nothing on the left port: %q", got)
	}
	mini.stop(true)
	l.s.sight(context.Background())
	if got := kind("m1-pro"); got != "Lab-owned sighted no Hard reset" {
		t.Errorf("m1-pro with the controller stopped: %q", got)
	}
	if got := facts("m1-pro"); !strings.Contains(got, "sighted on the right port No Hard reset: the Out-of-band controller is not healthy") {
		t.Errorf("m1-pro's Overview with the controller stopped: %q", got)
	}
}

// Live has Hard reset for a Lab-owned Mac, asking twice, for who may send
// one: a Maintainer, and the holder of a Lease on the Mac.
func TestLiveHasHardResetForALabOwnedMacAskingTwice(t *testing.T) {
	l, mini := resetLab(t)
	grace, ravi, linus := l.lrMember(lrGrace), l.lrMaintainer(), l.signedIn(lrLinus)
	const sentence = "Hard reset: m1-pro is forced to restart now, whatever it is doing"
	sent := func() int { n, _ := mini.resetsSent(); return n }

	live := ravi.macPage("m1-pro", "live")
	if got := cards(live); got != "live-screen live-logs live-reset" {
		t.Fatalf("a Lab-owned Mac's Live: %q", got)
	}
	card := part(t, live, `<section class="card" id="live-reset">`, `</section>`)
	if !strings.Contains(card, `action="/macs/m1-pro/hard-reset"`) || !strings.Contains(card, `data-sentence="`+sentence+`"`) ||
		!strings.Contains(card, `name="back" value="live"`) || buttons(card) != "Hard reset" {
		t.Fatalf("the Hard reset button: %s", card)
	}
	// A Volunteer's Mac has none, for a Maintainer and for its Volunteer.
	for who, b := range map[string]*browser{"a Maintainer": ravi, "its Volunteer": linus} {
		if page := b.macPage("vol-air", "live"); strings.Contains(page, "live-reset") || strings.Contains(page, "Hard reset") {
			t.Errorf("%s sees a Hard reset on a Volunteer's Mac's Live", who)
		}
	}
	// A Mac the lab cannot place has the reason in place of the button.
	card = part(t, ravi.macPage("m2-max", "live"), `<section class="card" id="live-reset">`, `</section>`)
	if buttons(card) != "" || !strings.Contains(rowText(card), "No Hard reset from here now: the lab has no Sighting of m2-max") {
		t.Fatalf("Live of a Mac that is not sighted: %s", card)
	}

	// An M team member reaches it under their own Lease; a Maintainer under
	// that Lease is told who holds the Mac.
	if page := grace.macPage("m1-pro", "live"); strings.Contains(page, "live-reset") {
		t.Fatal("an M team member with no Lease sees the Hard reset")
	}
	grace.mustLease("m1-pro", "grace@laptop", 0)
	if card := part(t, grace.macPage("m1-pro", "live"), `<section class="card" id="live-reset">`, `</section>`); buttons(card) != "Hard reset" {
		t.Fatalf("the Lease holder's Live: %s", card)
	}
	card = part(t, ravi.macPage("m1-pro", "live"), `<section class="card" id="live-reset">`, `</section>`)
	if buttons(card) != "" || !strings.Contains(rowText(card), "m1-pro is leased by") {
		t.Fatalf("a Maintainer's Live under another person's Lease: %s", card)
	}

	// The first press asks; nothing is sent.
	w := grace.form("/macs/m1-pro/hard-reset", "back", "live")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), sentence) || sent() != 0 {
		t.Fatalf("the first press: %d, %d resets sent\n%s", w.Code, sent(), w.Body.String())
	}
	if w := ravi.form("/macs/m1-pro/hard-reset", "back", "live", "sure", "yes"); w.Code != http.StatusConflict || !strings.Contains(refusalIn(w.Body.String()), "m1-pro is leased by") || sent() != 0 {
		t.Fatalf("a Maintainer's press under another person's Lease: %d %q, %d resets sent", w.Code, refusalIn(w.Body.String()), sent())
	}
	if w := linus.form("/macs/m1-pro/hard-reset", "back", "live", "sure", "yes"); w.Code != http.StatusNotFound || sent() != 0 {
		t.Fatalf("a Volunteer's press: %d, %d resets sent", w.Code, sent())
	}
	// The second does it, and comes back to Live.
	w = grace.form("/macs/m1-pro/hard-reset", "back", "live", "sure", "yes")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/macs/m1-pro?tab=live" {
		t.Fatalf("the second press: %d to %q %s", w.Code, w.Header().Get("Location"), refusalIn(w.Body.String()))
	}
	if _, rids := mini.resetsSent(); len(rids) != 1 || rids[0] != portRight {
		t.Fatalf("resets went to RIDs %v, want one to the right port", rids)
	}
	// While the lab waits for the Mac it sends no other.
	if w := grace.form("/macs/m1-pro/hard-reset", "back", "live", "sure", "yes"); w.Code != http.StatusConflict || !strings.Contains(refusalIn(w.Body.String()), "m1-pro is being recovered already (Hard reset by grace)") || sent() != 1 {
		t.Fatalf("a second Hard reset at once: %d %q, %d resets sent", w.Code, refusalIn(w.Body.String()), sent())
	}

	// Refusals are said on the page: a Mac the lab cannot place, and a
	// Volunteer's Mac, which never gets one.
	if w := ravi.form("/macs/m2-max/hard-reset", "back", "live", "sure", "yes"); w.Code != http.StatusConflict || !strings.Contains(refusalIn(w.Body.String()), "no Hard reset was sent: the lab has no Sighting of m2-max") {
		t.Fatalf("a Mac that is not sighted: %d %q", w.Code, refusalIn(w.Body.String()))
	}
	if w := ravi.form("/macs/vol-air/hard-reset", "back", "live", "sure", "yes"); w.Code != http.StatusForbidden || !strings.Contains(refusalIn(w.Body.String()), "the lab never sends a Volunteer's Mac a Hard reset") {
		t.Fatalf("a Volunteer's Mac: %d %q", w.Code, refusalIn(w.Body.String()))
	}
	if sent() != 1 {
		t.Fatalf("%d resets sent, want the one", sent())
	}
}
