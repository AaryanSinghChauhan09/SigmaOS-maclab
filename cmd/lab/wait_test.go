package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// lab wait holds one job wait at a time and asks again when it ends, naming
// the events it has shown.
func TestWaitAsksAgainUntilTheJobIsDone(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		j := api.Job{ID: "j1", Spec: api.JobSpec{Device: "m1"}, State: api.JobTesting, Events: []api.JobEvent{{Msg: "staged"}}}
		switch len(asked) {
		case 1:
		case 2:
			j.Events = append(j.Events, api.JobEvent{Msg: "booted"})
		default:
			j.Events = append(j.Events, api.JobEvent{Msg: "booted"}, api.JobEvent{Msg: "tested"})
			j.State, j.Outcome = api.JobDone, api.OutcomePass
		}
		json.NewEncoder(w).Encode(j)
	})
	if err := dispatch(context.Background(), c, "wait", []string{"j1"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/jobs/j1/wait?timeout=50&events=0", "/api/jobs/j1/wait?timeout=50&events=1", "/api/jobs/j1/wait?timeout=50&events=2"}
	if strings.Join(asked, " ") != strings.Join(want, " ") {
		t.Fatalf("lab wait asked %q, want %q", asked, want)
	}
}
