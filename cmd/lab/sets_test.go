package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// lab set create, capture, list and show are the API's calls, one each.
func TestLabSetCallsTheAPI(t *testing.T) {
	var got []string
	var bodies []any
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/sets":
			if r.Method == "GET" {
				io.WriteString(w, `[{"name":"7.2","created_by":"ada","items":[]}]`)
				return
			}
			var req api.SetRequest
			json.Unmarshal(b, &req)
			bodies = append(bodies, req)
		case "/api/sets/capture":
			var req api.SetCapture
			json.Unmarshal(b, &req)
			bodies = append(bodies, req)
		}
		io.WriteString(w, `{"name":"7.2","created_by":"ada","items":[{"package":"mesa","source":"repo","ref":"1","version":"1"}]}`)
	})
	ctx := context.Background()
	for _, args := range [][]string{
		{"create", "7.3", "--pkg", "linux-aurora=build:b-1", "--pkg", "m1n1-aurora=repo:1.5.2-1"},
		{"capture", "7.2", "--from", "m2-max"},
		{"list"},
		{"show", "7.2"},
	} {
		if err := dispatch(ctx, c, "set", args); err != nil {
			t.Fatalf("lab set %v: %v", args, err)
		}
	}
	want := []string{"POST /api/sets", "POST /api/sets/capture", "GET /api/sets", "GET /api/sets/7.2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("asked %v, want %v", got, want)
	}
	wantBodies := []any{
		api.SetRequest{Name: "7.3", Items: []api.SetItem{{Package: "linux-aurora", Source: "build", Ref: "b-1"}, {Package: "m1n1-aurora", Source: "repo", Ref: "1.5.2-1", Version: "1.5.2-1"}}},
		api.SetCapture{Name: "7.2", Mac: "m2-max"},
	}
	if !reflect.DeepEqual(bodies, wantBodies) {
		t.Fatalf("sent %+v, want %+v", bodies, wantBodies)
	}
	for _, bad := range [][]string{{"create", "x"}, {"create", "x", "--pkg", "nonsense"}, {"capture", "x"}, {"show"}, {"delete", "x"}} {
		if err := dispatch(ctx, c, "set", bad); err == nil {
			t.Errorf("lab set %v did something", bad)
		}
	}
}
