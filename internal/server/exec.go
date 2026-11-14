package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// hExec runs a shell command on a Mac, as root, through lab-agent. This is
// what agents use instead of ssh. Nobody runs it without their own Lease on
// the Mac, a Maintainer included, and the holder's is cut when the Lease
// ends. Every one, and every refused attempt, is in the Exec record. The
// answer is held until the command ends (see held): cutting it does not
// stop the command on the Mac.
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
	err := s.fence(v, p, fenceExec)
	if err == nil {
		if e := s.mayDo(callerOf(r), v, actExec); e != nil {
			err = refuse(403, "%v", e)
		}
	}
	if err == nil && !v.alive() {
		err = refuse(409, "%s is offline, and lab exec reaches a Mac only through lab-agent. %s", v.snapshot().Name, api.NoSerialConsole)
	}
	if err != nil {
		s.execRefused(v, p, a.Command, err)
		httpErr(w, statusOf(err), "%v", err)
		return
	}
	ended, err := s.execSent(v, p, a.Command)
	if err != nil {
		httpErr(w, statusOf(err), "%v", err)
		return
	}
	session, done := s.underLease(r, v, p)
	defer done()
	s.held(w, r, func() (any, error) {
		res := api.ExecResult{Via: "agent"}
		if err := v.call(session.Context(), api.CmdExec, "", a, time.Duration(a.TimeoutSec+30)*time.Second, &res); err != nil {
			err = leaseCut(session, r, v, err)
			if r.Context().Err() != nil {
				ended(nil, errors.New("its caller left before the Mac answered"))
			} else {
				ended(nil, err)
			}
			return nil, err
		}
		res.Via = "agent"
		ended(&res, nil)
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
