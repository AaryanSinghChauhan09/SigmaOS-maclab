package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// apiClient bounds every call, so a stream labd wrongly opens fails the
// test instead of holding it up.
var apiClient = &http.Client{Timeout: time.Minute, Transport: named{}}

// as calls labd's HTTP API with the given bearer token ("" sends none).
func (l *lab) as(token, method, path string, body any) (*http.Response, string) {
	l.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, l.url+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := l.client().Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(out)
}

// testMaintainer is a person with the Maintainer role and a Lab token of
// theirs.
type testMaintainer struct{ Name, Token string }

// addMaintainer makes name a Maintainer of the harness labd, with a Lab
// token: a person a test acts as beside the harness's own.
func (l *lab) addMaintainer(_, name string) testMaintainer {
	l.t.Helper()
	m := testMaintainer{Name: name, Token: "lab_" + sha256Hex(name)[:40]}
	seeded(l.t, l.s, m.Name, m.Token)
	return m
}

// removeMaintainer takes the Maintainer role from name.
func (l *lab) removeMaintainer(name string) {
	l.t.Helper()
	if err := l.s.setMaintainer(testAccount(name), false); err != nil {
		l.t.Fatal(err)
	}
}

// revokeTokenOf revokes the Lab token addMaintainer gave m.
func (l *lab) revokeTokenOf(m testMaintainer) {
	l.t.Helper()
	p, tok, err := l.s.store.labTokenByHash(hashSecret(m.Token))
	if err == nil {
		err = l.s.revokeLabToken(tok)
	}
	if err != nil {
		l.t.Fatalf("revoking the Lab token of %s (account %d): %v", m.Name, p.AccountID, err)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// waitLog waits for labd to log a line containing want.
func (l *lab) waitLog(want string) {
	l.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(l.logs.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	l.t.Fatalf("labd's log lacks %q:\n%s", want, l.logs.String())
}

// What ends a Maintainer's access ends their open requests: the role going,
// and the Lab token they came with being revoked.
func TestAMaintainersOpenRequestsEndWithTheRoleOrTheToken(t *testing.T) {
	for what, cut := range map[string]func(*lab, testMaintainer){
		"the role went":         func(l *lab, m testMaintainer) { l.removeMaintainer(m.Name) },
		"the token was revoked": func(l *lab, m testMaintainer) { l.revokeTokenOf(m) },
	} {
		t.Run(what, func(t *testing.T) { openRequestsEnd(t, cut) })
	}
}

func openRequestsEnd(t *testing.T, cut func(*lab, testMaintainer)) {
	l := newLab(t, true)
	l.baseline()
	// a build nobody takes, and a job waiting on it: both stay open
	src := gitRepo(t)
	resp, body := l.post("/api/builds", api.BuildRequest{Source: src, Device: l.name})
	var b api.Build
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &b) != nil || b.ID == "" {
		t.Fatalf("create build: %s %s", resp.Status, body)
	}
	j, _, err := l.s.Submit(api.JobSpec{Device: l.name, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	grace := l.addMaintainer(harnessToken, "grace")

	// cancelled before the harness closes its server, so a request labd
	// leaves open can't hold the test up
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	open := map[string]string{
		"stream":         "/api/stream",
		"job wait":       "/api/jobs/" + j.ID + "/wait?timeout=3600",
		"build-log tail": "/api/builds/" + b.ID + "/log?offset=0&wait=50",
	}
	type ended struct {
		what string
		code int
		body string
	}
	done := make(chan ended, len(open))
	for what, path := range open {
		go func() {
			req, _ := http.NewRequestWithContext(ctx, "GET", l.url+path, nil)
			req.Header.Set("Authorization", "Bearer "+grace.Token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				done <- ended{what, 0, err.Error()}
				return
			}
			out, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			done <- ended{what, resp.StatusCode, string(out)}
		}()
	}
	select {
	case e := <-done:
		t.Fatalf("%s ended before the cut: %d %s", e.what, e.code, e.body)
	case <-time.After(time.Second):
	}

	cut(l, grace)
	deadline := time.After(3 * time.Second)
	for len(open) > 0 {
		select {
		case e := <-done:
			delete(open, e.what)
			if e.what == "job wait" && e.code == 200 && strings.Contains(e.body, `"id"`) {
				var got api.Job
				if json.Unmarshal([]byte(e.body), &got) == nil && got.State != api.JobDone {
					t.Errorf("job wait answered grace with the unfinished job after the cut: %s", e.body)
				}
			}
		case <-deadline:
			t.Fatalf("grace's open requests were still open 3s after the cut: %v", open)
		}
	}
}
