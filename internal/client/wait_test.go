package client

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// Wait asks again after each call, and never for more than 50 s at once.
func TestWaitAsksAgainAfterEachCall(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		n := len(asked)
		mu.Unlock()
		j := api.Job{ID: "j1", State: api.JobTesting}
		if n == 3 {
			j.State = api.JobDone
		}
		json.NewEncoder(w).Encode(j)
	})
	j, err := c.Wait(context.Background(), "j1", time.Hour)
	if err != nil || j.State != api.JobDone {
		t.Fatalf("wait: %+v %v", j, err)
	}
	if want := "/api/jobs/j1/wait?timeout=50"; len(asked) != 3 || asked[0] != want || asked[2] != want {
		t.Fatalf("lab asked %q, want %q three times", asked, want)
	}
}

// WaitPast is one such call: it names how many events the caller has seen.
func TestWaitPastNamesTheEventsSeen(t *testing.T) {
	var asked string
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path + "?" + r.URL.RawQuery
		json.NewEncoder(w).Encode(api.Job{ID: "j1", State: api.JobTesting, Events: make([]api.JobEvent, 5)})
	})
	j, err := c.WaitPast(context.Background(), "j1", 4)
	if err != nil || len(j.Events) != 5 || asked != "/api/jobs/j1/wait?timeout=50&events=4" {
		t.Fatalf("asked %q, got %+v %v", asked, j, err)
	}
}
