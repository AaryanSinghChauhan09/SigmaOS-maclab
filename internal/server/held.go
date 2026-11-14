package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// The Front door cuts an answer that sends no body byte for 125 s, and the
// status and headers do not count. A held response therefore sends a
// Keepalive at once and then every keepaliveEvery, which survives three lost
// in a row. A call that only waits (a job wait, a followed build log) sends
// nothing and ends within maxWait instead.
const (
	keepaliveEvery = 30 * time.Second
	maxWait        = 50 * time.Second
	heldType       = "application/x-ndjson"
)

// refusal is an error with the status labd answers it with.
type refusal struct {
	code int
	msg  string
}

func (e *refusal) Error() string { return e.msg }

func refuse(code int, format string, args ...any) error {
	return &refusal{code, fmt.Sprintf(format, args...)}
}

// streamed is whether r is answered as a stream: always through the tunnel,
// and at the loopback port when the caller asks for one.
func streamed(r *http.Request) bool {
	return viaTunnel(r) || strings.Contains(r.Header.Get("Accept"), heldType)
}

// held answers r with what work returns, however long work takes. As a
// stream that is a Keepalive (an empty line) at once and every
// keepaliveEvery, then one last line, an api.HeldLine: the status is 200 by
// then, so an error is in that line. A caller on the mini that did not ask
// for the stream gets the silent form: one JSON answer, or the error's
// status, when work ends. work must not write to w; when r is cut its
// context ends, and the answer is dropped.
func (s *Server) held(w http.ResponseWriter, r *http.Request, work func() (any, error)) {
	if !streamed(r) {
		v, err := work()
		if err != nil {
			httpErr(w, statusOf(err), "%v", err)
			return
		}
		writeJSON(w, v)
		return
	}
	w.Header().Set("Content-Type", heldType)
	w.Header().Set("X-Accel-Buffering", "no")
	fl := http.NewResponseController(w)
	keep := s.clock().NewTicker(keepaliveEvery)
	defer keep.Stop()
	last := make(chan api.HeldLine, 1)
	go func() {
		v, err := work()
		if err != nil {
			last <- api.HeldLine{Error: err.Error(), Status: statusOf(err)}
			return
		}
		b, err := json.Marshal(v)
		if err != nil {
			last <- api.HeldLine{Error: err.Error(), Status: 500}
			return
		}
		last <- api.HeldLine{Result: b}
	}()
	for {
		io.WriteString(w, "\n")
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-keep.C():
		case l := <-last:
			json.NewEncoder(w).Encode(l)
			return
		}
	}
}

func statusOf(err error) int {
	var r *refusal
	if errors.As(err, &r) {
		return r.code
	}
	return 502
}

// waitFor is how long a call that only waits may be held: what the caller
// asks for in the query's key, in seconds, and never more than maxWait.
// def is for a caller that names no time.
func waitFor(r *http.Request, key string, def time.Duration) time.Duration {
	sec, _ := strconv.Atoi(r.URL.Query().Get(key))
	if sec <= 0 {
		return def
	}
	return min(time.Duration(sec)*time.Second, maxWait)
}
