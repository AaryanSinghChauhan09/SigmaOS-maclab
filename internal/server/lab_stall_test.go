package server

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// A loaded CI runner under -race can hold up the simulated Mac for half a
// second of wall time. On the sped-up clock that is seconds of labd's time:
// the lab still waits for its Mac as long in wall time as it did on the wall
// clock, so a stall like that is not a lost Mac.
func TestALabsMacMayStallAsLongAsOnTheWallClock(t *testing.T) {
	l := newLabWith(t, labConfig{wrap: freezeOnResults(500 * time.Millisecond)})
	l.baseline()
}

// freezeOnResults stalls the simulated Mac's lab-agent for d of wall time each
// time it reports a command's result, holding that result back: nothing it
// sends reaches labd until then.
func freezeOnResults(d time.Duration) func(http.Handler) http.Handler {
	var mu sync.Mutex
	var until time.Time
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/agent/") {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				mu.Lock()
				now := time.Now()
				if r.URL.Path == "/api/agent/report" && bytes.Contains(body, []byte(`"results":[{`)) && now.After(until) {
					until = now.Add(d)
				}
				wait := until.Sub(now)
				mu.Unlock()
				time.Sleep(wait)
			}
			h.ServeHTTP(w, r)
		})
	}
}
