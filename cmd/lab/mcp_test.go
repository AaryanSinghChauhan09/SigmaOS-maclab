package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lab_run is lab run one for one: its uploads name the Mac they are for, so
// an M team member under their own Lease boots their own kernel as `lab run`
// lets them.
func TestAnMTeamMembersLabRunUploadsForTheMacTheyLease(t *testing.T) {
	var uploads []string
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/artifacts":
			uploads = append(uploads, r.URL.RawQuery)
			if r.URL.Query().Get("device") != "m1" {
				http.Error(w, "an M team member uploads only for a job on a Mac they lease", http.StatusForbidden)
				return
			}
			io.WriteString(w, `{"sha256":"`+strings.Repeat("ab", 32)+`"}`)
		case "POST /api/jobs":
			json.NewEncoder(w).Encode(map[string]any{"id": "j-1", "state": "queued", "spec": map[string]any{"device": "m1"}})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := newMCP(c).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	kernel := filepath.Join(t.TempDir(), "Image")
	os.WriteFile(kernel, []byte("a kernel"), 0o644)
	script := filepath.Join(t.TempDir(), "check.sh")
	os.WriteFile(script, []byte("#!/bin/sh\ntrue\n"), 0o755)
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "lab_run", Arguments: map[string]any{"device": "m1", "kernel_path": kernel, "scripts": []string{script}}})
	if err != nil {
		t.Fatal(err)
	}
	var said string
	for _, part := range res.Content {
		if text, ok := part.(*mcp.TextContent); ok {
			said += text.Text
		}
	}
	if res.IsError || len(uploads) != 2 {
		t.Fatalf("lab_run with a kernel_path under the member's Lease (error %v, uploads %q):\n%s", res.IsError, uploads, said)
	}
}
