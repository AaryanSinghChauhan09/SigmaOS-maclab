package server

// The Lease record: for every Mac, each Lease taken, renewed, extended,
// released, expired and ended, with who and why. A line keeps the account
// id and the handle the person had then, so a rename rewrites nothing. It
// is read beside a Mac's Audit log, by whoever sees the Mac, and on the
// person's record on People.

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

func (st *store) addLeaseEvent(deviceID string, e api.LeaseEvent) error {
	_, err := st.db.Exec(`INSERT INTO lease_record (at, mac, device_id, event, account_id, handle, holder, until, by_id, by, why) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMicro(), e.Mac, deviceID, e.Event, e.AccountID, e.Handle, e.Holder, e.Until.UnixMicro(), e.ByID, e.By, e.Why)
	return err
}

const leaseEventColumns = `at, mac, event, account_id, handle, holder, until, by_id, by, why`

// leaseRecord is the newest lines of the Lease record of one enrollment of
// a Mac, newest first.
func (st *store) leaseRecord(deviceID string, limit int) ([]api.LeaseEvent, error) {
	return scanLeaseEvents(st.db.Query(`SELECT `+leaseEventColumns+` FROM lease_record WHERE device_id = ? ORDER BY id DESC LIMIT ?`, deviceID, limit))
}

// leaseRecordOf is the newest lines about a person's Leases, and those of
// Leases the person ended, newest first.
func (st *store) leaseRecordOf(accountID int64, limit int) ([]api.LeaseEvent, error) {
	return scanLeaseEvents(st.db.Query(`SELECT `+leaseEventColumns+` FROM lease_record WHERE account_id = ? OR by_id = ? ORDER BY id DESC LIMIT ?`, accountID, accountID, limit))
}

func scanLeaseEvents(rows *sql.Rows, err error) ([]api.LeaseEvent, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.LeaseEvent{}
	for rows.Next() {
		var e api.LeaseEvent
		var at, until int64
		if err := rows.Scan(&at, &e.Mac, &e.Event, &e.AccountID, &e.Handle, &e.Holder, &until, &e.ByID, &e.By, &e.Why); err != nil {
			return nil, err
		}
		e.Time, e.Until = time.UnixMicro(at), time.UnixMicro(until)
		out = append(out, e)
	}
	return out, rows.Err()
}

// recordLease writes one line of the Lease record: event happened to l,
// the Lease on the Mac called mac in enrollment deviceID. by is who did it
// when that is not its holder. A line that cannot be written is logged: the
// record never holds a Lease up.
func (s *Server) recordLease(mac, deviceID string, l *api.Lease, event string, by Person, why string) {
	e := api.LeaseEvent{Time: s.now(), Mac: mac, Event: event, AccountID: l.AccountID, Handle: l.Owner(), Holder: l.Holder, Until: l.Expires, Why: why}
	if by.Handle != "" && (by.AccountID != l.AccountID || l.AccountID == 0 && by.Handle != l.Owner()) {
		e.ByID, e.By = by.AccountID, by.Handle
	}
	if err := s.store.addLeaseEvent(deviceID, e); err != nil {
		s.log.Printf("the Lease record, %s: %v", e.Line(), err)
	}
}

// leaseRecordShown is how many lines of the Lease record a request gets
// when it asks for no number.
const leaseRecordShown = 100

// hLeaseRecord is a Mac's Lease record, for whoever sees the Mac.
func (s *Server) hLeaseRecord(w http.ResponseWriter, r *http.Request) {
	v := s.devOr404(w, r)
	if v == nil {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 1000 {
		n = leaseRecordShown
	}
	events, err := s.store.leaseRecord(v.snapshot().ID, n)
	if err != nil {
		httpErr(w, 500, "%v", err)
		return
	}
	writeJSON(w, events)
}
