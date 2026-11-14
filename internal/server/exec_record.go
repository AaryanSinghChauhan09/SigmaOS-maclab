package server

// The Exec record: every lab exec on every Mac, and every refused attempt:
// the person and the handle they had then, the Mac, when, the command's
// text, its exit code and how long it ran. Never its output. A line is
// deleted when it is 12 months old. It is shown to Maintainers, to the
// Mac's Volunteer, and to each person for the commands they ran.

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

func (st *store) addExec(deviceID string, e api.ExecEvent) (int64, error) {
	res, err := st.db.Exec(`INSERT INTO exec_record (at, mac, device_id, account_id, handle, command, state, seconds, why) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMicro(), e.Mac, deviceID, e.AccountID, e.Handle, e.Command, e.State, e.Seconds, e.Why)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// endExec says how the command of line id ended. Only one that ran has an
// exit code.
func (st *store) endExec(id int64, state string, exit int, seconds float64, why string) error {
	var code any
	if state == api.ExecRan {
		code = exit
	}
	_, err := st.db.Exec(`UPDATE exec_record SET state = ?, exit_code = ?, seconds = ?, why = ? WHERE id = ?`, state, code, seconds, why, id)
	return err
}

// execRecord is the newest lines of the Exec record of one enrollment of a
// Mac, newest first: all of them, or with only set, those that account ran.
func (st *store) execRecord(deviceID string, only int64, limit int) ([]api.ExecEvent, error) {
	rows, err := st.db.Query(`SELECT at, mac, account_id, handle, command, state, exit_code, seconds, why FROM exec_record
		WHERE device_id = ? AND (? = 0 OR account_id = ?) ORDER BY id DESC LIMIT ?`, deviceID, only, only, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.ExecEvent{}
	for rows.Next() {
		var e api.ExecEvent
		var at int64
		var exit sql.NullInt64
		if err := rows.Scan(&at, &e.Mac, &e.AccountID, &e.Handle, &e.Command, &e.State, &exit, &e.Seconds, &e.Why); err != nil {
			return nil, err
		}
		e.Time = time.UnixMicro(at)
		if exit.Valid {
			code := int(exit.Int64)
			e.ExitCode = &code
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// execKeptMonths is how long a line of the Exec record is kept.
const execKeptMonths = 12

// dropOldExecLines deletes every line of the Exec record that is 12 months
// old.
func (s *Server) dropOldExecLines() {
	if _, err := s.store.db.Exec(`DELETE FROM exec_record WHERE at < ?`, s.now().AddDate(0, -execKeptMonths, 0).UnixMicro()); err != nil {
		s.log.Printf("the Exec record, deleting lines %d months old: %v", execKeptMonths, err)
	}
}

// execRecordLoop keeps the Exec record to its 12 months, at start and every
// hour. A command labd was still waiting on when it last stopped has no
// answer to record.
func (s *Server) execRecordLoop() {
	if _, err := s.store.db.Exec(`UPDATE exec_record SET state = ?, why = ? WHERE state = ?`, api.ExecLost, "labd stopped before the Mac answered", api.ExecRunning); err != nil {
		s.log.Printf("the Exec record, closing lines left open: %v", err)
	}
	s.dropOldExecLines()
	t := s.clock().NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C():
			s.dropOldExecLines()
		}
	}
}

// execRefused records an attempt at lab exec that labd refused. A line that
// cannot be written is logged: the refusal stands either way.
func (s *Server) execRefused(v *dev, p Person, command string, why error) {
	d := v.snapshot()
	e := api.ExecEvent{Time: s.now(), Mac: d.Name, AccountID: p.AccountID, Handle: p.Handle, Command: command, State: api.ExecRefused, Why: why.Error()}
	if _, err := s.store.addExec(d.ID, e); err != nil {
		s.log.Printf("the Exec record, %s: %v", e.Line(), err)
	}
}

// errNotSent is a command whose line the Exec record already had when its
// Lease was found ended: it never went to the Mac, and the line ends as
// refused.
type errNotSent struct{ error }

func (e errNotSent) Unwrap() error { return e.error }

// execSent records p's command as it goes to v, and returns what records
// how it ended. A command whose line cannot be written is not sent: no
// lab exec runs outside the record.
func (s *Server) execSent(v *dev, p Person, command string) (ended func(*api.ExecResult, error), err error) {
	d := v.snapshot()
	e := api.ExecEvent{Time: s.now(), Mac: d.Name, AccountID: p.AccountID, Handle: p.Handle, Command: command, State: api.ExecRunning}
	id, err := s.store.addExec(d.ID, e)
	if err != nil {
		s.log.Printf("the Exec record, %s: %v", e.Line(), err)
		return nil, refuse(500, "the Exec record could not be written, so the command was not sent to %s: %v", d.Name, err)
	}
	return func(res *api.ExecResult, failed error) {
		var err error
		switch {
		case errors.As(failed, new(errNotSent)):
			err = s.store.endExec(id, api.ExecRefused, 0, 0, failed.Error())
		case failed != nil:
			err = s.store.endExec(id, api.ExecLost, 0, s.since(e.Time).Seconds(), failed.Error())
		default:
			err = s.store.endExec(id, api.ExecRan, res.ExitCode, res.Seconds, "")
		}
		if err != nil {
			s.log.Printf("the Exec record, how %s ended: %v", e.Line(), err)
		}
	}, nil
}

// execRecordOf is the newest lines of v's Exec record that p may read,
// newest first: all of them for a Maintainer and for the Mac's Volunteer,
// and for anyone else the commands they ran themselves.
func (s *Server) execRecordOf(p Person, v *dev, limit int) ([]api.ExecEvent, error) {
	d := v.snapshot()
	switch {
	case p.isMaintainer() || p.AccountID != 0 && p.AccountID == d.VolunteerID:
		return s.store.execRecord(d.ID, 0, limit)
	case p.AccountID == 0:
		return []api.ExecEvent{}, nil
	}
	return s.store.execRecord(d.ID, p.AccountID, limit)
}

// execRecordShown is how many lines of the Exec record a request gets when
// it asks for no number.
const execRecordShown = 100

// hExecRecord is a Mac's Exec record, as far as its caller may read it.
func (s *Server) hExecRecord(w http.ResponseWriter, r *http.Request) {
	v := s.devOr404(w, r)
	if v == nil {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 1000 {
		n = execRecordShown
	}
	var events []api.ExecEvent
	var err error
	if c := callerOf(r); c.maintainer != "" || c.team == "" {
		// A Maintainer, or someone the route let in as the Mac's own
		// person: its Volunteer.
		events, err = s.store.execRecord(v.snapshot().ID, 0, n)
	} else {
		events, err = s.execRecordOf(leaser(r), v, n)
	}
	if err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, events)
}
