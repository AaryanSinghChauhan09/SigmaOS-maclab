package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/oob"
)

// hExec runs a shell command on a Mac, as root: through the agent when the
// Mac is on the network, otherwise through the serial shell. This is what
// agents use instead of ssh. The answer is held until the command ends (see
// held): cutting it does not stop the command on the Mac. A Lease fences
// it: an M team member runs it on a Mac they lease, nobody under another
// person's Lease, and the holder's is cut when their Lease ends.
func (s *Server) hExec(w http.ResponseWriter, r *http.Request) {
	v := s.devOr404(w, r)
	if v == nil {
		return
	}
	var a api.ExecArgs
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil || a.Command == "" {
		httpErr(w, 400, "send {\"command\": \"...\"}")
		return
	}
	if a.TimeoutSec <= 0 || a.TimeoutSec > 3600 {
		a.TimeoutSec = 120
	}
	p := leaser(r)
	if err := s.fence(v, p, fenceExec); err != nil {
		httpErr(w, statusOf(err), "%v", err)
		return
	}
	if err := s.mayDo(callerOf(r), v, actExec); err != nil {
		httpErr(w, 403, "%v", err)
		return
	}
	v.mu.Lock()
	alive, ctl := v.aliveLocked(), v.effectiveOOBLocked()
	v.mu.Unlock()
	if !alive && ctl == nil {
		httpErr(w, 409, "%s is offline and has no serial console", v.d.Name)
		return
	}
	session, done := s.underLease(r, v, p)
	defer done()
	s.held(w, r, func() (any, error) {
		var res api.ExecResult
		var err error
		if alive {
			err = v.call(session.Context(), api.CmdExec, "", a, time.Duration(a.TimeoutSec+30)*time.Second, &res)
			res.Via = "agent"
		} else {
			res, err = v.execOOB(session.Context(), a)
			res.Via = "serial"
		}
		if err != nil {
			return nil, leaseCut(session, r, v, err)
		}
		return res, nil
	})
}

// leaseCut is err, or, when the session ended because its Lease did and
// not because its caller left, the refusal that says so.
func leaseCut(session, r *http.Request, v *dev, err error) error {
	if session.Context().Err() != nil && r.Context().Err() == nil {
		return refuse(409, "the Lease on %s ended, which cut this session; a command it started may still be running on the Mac", v.snapshot().Name)
	}
	return err
}

var (
	reOSC = regexp.MustCompile("\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)")
	reCSI = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")
	seq   struct {
		sync.Mutex
		n int
	}
)

// serialExec types the command into the serial shell between two markers and
// reads the output back. stdout and stderr arrive merged.
func serialExec(ctx context.Context, ctl oob.Controller, a api.ExecArgs) (api.ExecResult, error) {
	var res api.ExecResult
	seq.Lock()
	seq.n++
	tag := fmt.Sprintf("%d%d", time.Now().Unix()%100000, seq.n)
	seq.Unlock()
	start := regexp.MustCompile(`(?m)^__MLS_` + tag + `__\r?$`)
	end := regexp.MustCompile(`(?m)^__MLE_` + tag + `_(\d+)__`)

	if err := ctl.Arm(ctx, 0); err != nil {
		return res, fmt.Errorf("arming serial: %w", err)
	}
	time.Sleep(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(a.TimeoutSec)*time.Second)
	defer cancel()
	var mu sync.Mutex
	var buf bytes.Buffer
	go ctl.Raw(ctx, writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	time.Sleep(300 * time.Millisecond)
	t0 := time.Now()
	line := "\x15stty -echo; echo __MLS_" + tag + "__; " + a.Command + "; echo __MLE_" + tag + "_$?__; stty echo\r"
	if err := ctl.Write(ctx, []byte(line)); err != nil {
		return res, err
	}
	for {
		mu.Lock()
		out := reCSI.ReplaceAllString(reOSC.ReplaceAllString(buf.String(), ""), "")
		mu.Unlock()
		if si := start.FindStringIndex(out); si != nil {
			if m := end.FindStringSubmatchIndex(out[si[1]:]); m != nil {
				body := out[si[1] : si[1]+m[0]]
				res.ExitCode, _ = strconv.Atoi(out[si[1]+m[2] : si[1]+m[3]])
				res.Stdout = string(bytes.TrimLeft(bytes.ReplaceAll([]byte(body), []byte("\r\n"), []byte("\n")), "\n"))
				res.Seconds = time.Since(t0).Seconds()
				return res, nil
			}
		}
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("no result over serial within %ds", a.TimeoutSec)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
