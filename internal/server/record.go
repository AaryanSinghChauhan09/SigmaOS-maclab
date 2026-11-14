package server

// The People record: who did what, to whom, and when. Every action a page
// takes writes one line. A line keeps the two people by account id and by
// the handle they had then, apart from its words, so a handle can come off
// a line without the line going.

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type recordLine struct {
	At      time.Time
	ActorID int64
	Actor   string
	What    string
	AboutID int64 // 0 when the line is about nobody else
	About   string
	// Note is what the actor typed beside the action, such as the reason
	// for a Block; "" for none.
	Note string
}

// String is the line as the record reads: "@actor what @about: note".
func (l recordLine) String() string {
	out := "@" + l.Actor + " " + l.What
	if l.About != "" {
		out += " @" + l.About
	}
	if l.Note != "" {
		out += ": " + l.Note
	}
	return out
}

// record writes a line of the People record for the person signed in on r:
// they did what, to about when it is not nil. It reads "@actor what @about".
func (s *Server) record(r *http.Request, what string, about *Person) error {
	return s.recordNoted(r, what, about, "")
}

// recordNoted is record for an action the actor typed a note beside.
func (s *Server) recordNoted(r *http.Request, what string, about *Person, note string) error {
	actor := PersonOf(r)
	if actor == nil {
		return errors.New("the People record: nobody is signed in on this request")
	}
	l := recordLine{At: s.now(), ActorID: actor.AccountID, Actor: actor.Handle, What: what, Note: note}
	if about != nil {
		l.AboutID, l.About = about.AccountID, about.Handle
	}
	return s.store.addRecord(l)
}

func (st *store) addRecord(l recordLine) error {
	var about any
	if l.AboutID != 0 {
		about = l.AboutID
	}
	_, err := st.db.Exec(`INSERT INTO people_record (at, actor_id, actor, what, about_id, about, note) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.At.UnixMicro(), l.ActorID, l.Actor, l.What, about, l.About, l.Note)
	return err
}

const recordColumns = `at, actor_id, actor, what, COALESCE(about_id, 0), about, note`

// recordLines are the newest lines of the People record, newest first.
func (st *store) recordLines(limit int) ([]recordLine, error) {
	return st.scanRecord(st.db.Query(`SELECT `+recordColumns+` FROM people_record ORDER BY id DESC LIMIT ?`, limit))
}

// recordLinesOf are the newest lines a person did or that are about them.
func (st *store) recordLinesOf(accountID int64, limit int) ([]recordLine, error) {
	return st.scanRecord(st.db.Query(`SELECT `+recordColumns+` FROM people_record WHERE actor_id = ? OR about_id = ? ORDER BY id DESC LIMIT ?`, accountID, accountID, limit))
}

func (st *store) scanRecord(rows *sql.Rows, err error) ([]recordLine, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []recordLine
	for rows.Next() {
		var l recordLine
		var at int64
		if err := rows.Scan(&at, &l.ActorID, &l.Actor, &l.What, &l.AboutID, &l.About, &l.Note); err != nil {
			return nil, err
		}
		l.At = time.UnixMicro(at)
		out = append(out, l)
	}
	return out, rows.Err()
}
