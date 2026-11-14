package server

import (
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// nextSummary is the first 08:00 Brisbane after t.
func nextSummary(t time.Time) time.Time {
	b := t.In(brisbane)
	at := time.Date(b.Year(), b.Month(), b.Day(), 8, 0, 0, 0, brisbane)
	if !at.After(t) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// summaryLoop posts the daily summary at 08:00 Brisbane, once a day: a labd
// that restarts at that hour does not post it again, and one that was not
// running then posts none for the day.
func (s *Server) summaryLoop() {
	next := nextSummary(s.start)
	t := s.clock().NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C():
		}
		now := s.now()
		if now.Before(next) {
			continue
		}
		day := next
		next = nextSummary(now)
		if s.lines.markOnce("daily-summary", day.In(brisbane).Format(time.DateOnly)) {
			s.postDailySummary(day)
		}
	}
}

// postDailySummary records the daily summary for the Lab group now.
func (s *Server) postDailySummary(day time.Time) {
	s.labGroup.DailySummary(day, s.summaryMacs(), s.requestsToLendWaiting())
}

// summaryMacs is every enrolled Mac as the daily summary shows it.
func (s *Server) summaryMacs() []MacSummary {
	var out []MacSummary
	for _, v := range s.allDevs() {
		d := v.snapshot()
		if d.State == api.StateLeft {
			continue
		}
		m := MacSummary{Name: d.Name, State: MacFree}
		if c := s.configuration(v); c != (api.Configuration{}) {
			m.Configuration = c.String()
		}
		v.mu.Lock()
		l := v.liveLeaseLocked()
		v.mu.Unlock()
		switch {
		case l != nil:
			m.State = MacLeased(orStr(l.Owner(), l.Holder), l.Expires)
		case d.ActiveJob != "" || d.State == api.StateBusy || d.State == api.StateRecovering:
			m.State = MacRunningJob
		case v.windowClosed():
			m.State = MacOutsideWindow
		case d.State == api.StateOffline || d.State == api.StateNeedsHands:
			m.State = MacNotSeen
		}
		out = append(out, m)
	}
	return out
}

// requestsToLendWaiting is the count the daily summary gives. labd has no
// Requests to lend yet.
func (s *Server) requestsToLendWaiting() int { return 0 }
