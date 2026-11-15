package server

// Forgetting a person (spec section 14, "Forgetting a person"). The person
// asks on their own page, or by mail; a Maintainer carries it out with one
// button, an Admin for a Maintainer, and it cannot be undone. Everything the
// person had ends as when a Maintainer ends it (sessions, Lab tokens,
// Leases, Mac sign-ins, the Macs they lend, what was on the way to lending),
// their record goes, and everything else labd keeps loses their handle and
// account id: both databases, labd's copies of Audit logs, the frozen
// Releases' notes, the caller log, and the lines for Signal, sent or not.
// Exec record lines keep the handle until they are 12 months old. A Blocked
// person stays Blocked: the account id, the date and who blocked are kept.
// No list of forgettings is kept; the lines for one name only who carried
// it out.

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// forgetWithin is how long a Maintainer has to carry out a request.
const forgetWithin = 30 * 24 * time.Hour

// forgottenWord stands where a forgotten person's handle stood in words. A
// field that held the handle alone is left empty instead.
const forgottenWord = "someone"

// leftForgotten is the Retired record's how for a Mac whose Volunteer was
// Forgotten.
const leftForgotten = "forgotten"

// forgetting is one person being forgotten: how their handle and account
// id are found in what labd keeps.
type forgetting struct {
	id     int64
	idText string
	handle string
}

func newForgetting(id int64, handle string) forgetting {
	return forgetting{id: id, idText: strconv.FormatInt(id, 10), handle: handle}
}

// handleByte is a byte a GitHub handle may hold: a name is a mention of the
// handle only where neither side goes on with one.
func handleByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-'
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// words is text with every mention of the handle, "@" and all, in any case,
// replaced by forgottenWord.
func (f forgetting) words(s string) string {
	if f.handle == "" {
		return s
	}
	lower, h := asciiLower(s), asciiLower(f.handle)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lower[i:], h)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		j += i
		end := j + len(h)
		if (j > 0 && handleByte(s[j-1])) || (end < len(s) && handleByte(s[end])) {
			b.WriteString(s[i : j+1])
			i = j + 1
			continue
		}
		start := j
		if start > 0 && s[start-1] == '@' && (start == 1 || !handleByte(s[start-2])) {
			start--
		}
		b.WriteString(s[i:start])
		b.WriteString(forgottenWord)
		i = end
	}
}

// value is a stored value with the person taken out: one that names the
// handle alone becomes empty, and words lose its mentions.
func (f forgetting) value(s string) string {
	if f.handle != "" && strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(s), "@"), f.handle) {
		return ""
	}
	return f.words(s)
}

// keptNames are the keys of a stored object that hold a Mac's, a job's or a
// Release's name, which is left as it was typed.
var keptNames = map[string]bool{"name": true, "mac": true, "device": true, "id": true, "device_id": true, "release": true, "job": true}

// idKey is a key or a column that holds a person's account id.
func idKey(key string) bool {
	k := asciiLower(key)
	return strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "_by") || k == "accountid"
}

// json is a stored JSON document with the person taken out, and whether
// anything changed. Numbers are kept as written.
func (f forgetting) json(raw string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw, false
	}
	v, changed := f.walk("", v)
	if !changed {
		return raw, false
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return raw, false
	}
	return strings.TrimSuffix(b.String(), "\n"), true
}

func (f forgetting) walk(key string, v any) (any, bool) {
	switch x := v.(type) {
	case string:
		if keptNames[asciiLower(key)] {
			return x, false
		}
		y := f.value(x)
		return y, y != x
	case json.Number:
		if idKey(key) && x.String() == f.idText {
			return json.Number("0"), true
		}
	case map[string]any:
		did := false
		for k, e := range x {
			if n, changed := f.walk(k, e); changed {
				x[k], did = n, true
			}
		}
		return x, did
	case []any:
		did := false
		for i, e := range x {
			if n, changed := f.walk(key, e); changed {
				x[i], did = n, true
			}
		}
		return x, did
	}
	return v, false
}

// stored is a stored text with the person taken out: a JSON document by its
// keys, anything else as words.
func (f forgetting) stored(s string) string {
	if t := strings.TrimSpace(s); (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
		out, _ := f.json(s)
		return out
	}
	return f.value(s)
}

// ---- who may forget whom ----

// errForgottenAlready is a Blocked account forgotten before: all that is
// left of it is that it is Blocked.
var errForgottenAlready = refuse(http.StatusConflict, "This account is forgotten already: the lab keeps only that it is Blocked.")

func mayForget(by *Person, k knownPerson) error {
	switch {
	case k.Admin:
		return refuse(http.StatusForbidden, "An Admin is named in the server's configuration: take them out of it before forgetting them.")
	case k.Handle == "" && k.Blocked:
		return errForgottenAlready
	case k.Maintainer && !by.Admin:
		return refuse(http.StatusForbidden, "Only an Admin forgets a Maintainer.")
	}
	return nil
}

// forgetEnds is the sentence Forget states before it is pressed again.
func (s *Server) forgetEnds(k knownPerson) string {
	var ends []string
	if n, _ := s.store.countSessions(k.AccountID, s.now()); n > 0 {
		ends = append(ends, counted(n, "browser", "browsers")+" signed out")
	}
	if tokens, _ := s.store.labTokens(k.AccountID); len(tokens) > 0 && !k.Blocked {
		ends = append(ends, counted(len(tokens), "Lab token", "Lab tokens")+" stopped")
	}
	if n := len(s.leasesOf(k.Person)); n > 0 {
		ends = append(ends, counted(n, "Lease", "Leases")+" ended")
	}
	if signIns, _ := s.reports.SignInsOf(k.AccountID); len(signIns) > 0 {
		ends = append(ends, counted(len(signIns), "Mac", "Macs")+" signed out")
	}
	if n := len(s.macsLentBy(k.AccountID)); n > 0 {
		ends = append(ends, counted(n, "Mac leaves", "Macs leave")+" the lab")
	}
	out := "Forget @" + k.Handle + " now: everything the lab keeps of them goes"
	if len(ends) > 0 {
		out += ", " + strings.Join(ends, ", ")
	}
	if k.Blocked {
		out += "; the account stays Blocked"
	}
	return out + ". It cannot be undone"
}

// ---- the request ----

// askToBeForgotten records the person's request, once.
func (st *store) askToBeForgotten(accountID int64, now time.Time) (bool, error) {
	res, err := st.db.Exec(`UPDATE people SET forget_asked = ? WHERE account_id = ? AND forget_asked IS NULL`, now.UnixMicro(), accountID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// forgetAsked is when the person asked to be forgotten, zero if they did not.
func (st *store) forgetAsked(accountID int64) (time.Time, error) {
	var at sql.NullInt64
	err := st.db.QueryRow(`SELECT forget_asked FROM people WHERE account_id = ?`, accountID).Scan(&at)
	if err != nil || !at.Valid {
		return time.Time{}, err
	}
	return time.UnixMicro(at.Int64), nil
}

// forgetRequest is a request to be Forgotten, waiting on People.
type forgetRequest struct {
	AccountID         int64
	Handle, Asked, By string
	// Maintainer is a request only an Admin carries out.
	Maintainer bool
	// Sentence is Forget's for the viewer, Why why they may not.
	Sentence, Why string
}

func (st *store) forgetRequests() ([]forgetRequest, error) {
	rows, err := st.db.Query(`SELECT account_id, handle, maintainer, forget_asked FROM people WHERE forget_asked IS NOT NULL ORDER BY forget_asked, account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []forgetRequest
	for rows.Next() {
		var q forgetRequest
		var at int64
		if err := rows.Scan(&q.AccountID, &q.Handle, &q.Maintainer, &at); err != nil {
			return nil, err
		}
		q.Asked, q.By = time.UnixMicro(at).UTC().Format(pageDate), time.UnixMicro(at).Add(forgetWithin).UTC().Format(pageDate)
		out = append(out, q)
	}
	return out, rows.Err()
}

// forgetRequestsWaiting is how many requests to be Forgotten wait.
func (s *Server) forgetRequestsWaiting() int {
	var n int
	if err := s.store.db.QueryRow(`SELECT count(*) FROM people WHERE forget_asked IS NOT NULL`).Scan(&n); err != nil {
		s.log.Printf("requests to be forgotten: %v", err)
	}
	return n
}

// ---- carrying it out ----

// forgetPerson forgets k for the person signed in on r.
func (s *Server) forgetPerson(r *http.Request, k knownPerson) error {
	by := *PersonOf(r)
	if err := mayForget(&by, k); err != nil {
		return err
	}
	f := newForgetting(k.AccountID, k.Handle)

	// No line leaves for Signal until the handle is off every one.
	s.lines.sending.Lock()
	err := s.forgetHeld(by, k, f)
	s.lines.sending.Unlock()
	if err != nil {
		return err
	}
	at := s.now()
	if err := s.store.addRecord(recordLine{At: at, ActorID: by.AccountID, Actor: by.Handle, What: "carried out a forgetting"}); err != nil {
		s.log.Printf("the People record: %v", err)
	}
	s.agents.Forgot(by.Handle, at)
	return nil
}

func (s *Server) forgetHeld(by Person, k knownPerson, f forgetting) error {
	// What the person had ends as when a Maintainer ends it. Each ending
	// writes its lines with the handle, which come off below.
	if err := s.store.endAllAccess(k.AccountID); err != nil {
		return err
	}
	s.changed(k.AccountID)
	s.endLendingOf(k.Person, by)
	s.endLeasesOf(k.Person, endedByMaintainer(by, ""))
	s.endEnrollmentsOf(k.AccountID, leaving{how: leftForgotten, by: by})

	if err := s.reports.Forget(k.AccountID, k.Handle); err != nil {
		return fmt.Errorf("reports.db: %w", err)
	}
	if err := s.store.forget(f, k.Blocked); err != nil {
		return fmt.Errorf("labd.db: %w", err)
	}
	s.forgetInFleet(f)
	for _, dir := range []string{filepath.Join(s.cfg.DataDir, "audit"), filepath.Join(s.cfg.DataDir, "frozen")} {
		if err := forgetInFiles(dir, f); err != nil {
			s.log.Printf("a forgetting, %s: %v", dir, err)
		}
	}
	if err := s.callers.forget(f); err != nil {
		s.log.Printf("a forgetting, the caller log: %v", err)
	}
	return nil
}

// forgetInFleet takes the person off the Macs enrolled now, as labd holds
// them: a Lease's attribution, a recovery's holder, a login linked to a Mac.
func (s *Server) forgetInFleet(f forgetting) {
	for _, v := range s.allDevs() {
		v.mu.Lock()
		b, err := json.Marshal(v.d)
		if err == nil {
			if out, changed := f.json(string(b)); changed {
				var d api.Device
				if json.Unmarshal([]byte(out), &d) == nil {
					v.d = d
					v.saveLocked()
				}
			}
		}
		v.mu.Unlock()
	}
}

// unscrubbed are the tables a forgetting leaves alone: the Exec record keeps
// the handle until each line is 12 months old.
var unscrubbed = map[string]bool{"exec_record": true}

// forget erases the person's record, or for a Blocked person keeps only the
// Blocked state, and takes their handle and account id off every other
// table but the Exec record, in one transaction.
func (st *store) forget(f forgetting, blocked bool) error {
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		if err == nil {
			_, err = tx.Exec(q, args...)
		}
	}
	exec(`DELETE FROM browser_sessions WHERE account_id = ?`, f.id)
	exec(`DELETE FROM lab_tokens WHERE account_id = ?`, f.id)
	exec(`DELETE FROM requests_to_lend WHERE account_id = ?`, f.id)
	exec(`DELETE FROM tokens WHERE token_hash IN (SELECT token_hash FROM join_commands WHERE volunteer_id = ?)`, f.id)
	exec(`DELETE FROM join_commands WHERE volunteer_id = ?`, f.id)
	// What others typed about them goes with the handle: a Block's reason.
	exec(`UPDATE people_record SET note = '' WHERE about_id = ?`, f.id)
	if blocked {
		exec(`UPDATE people SET handle = '', blocked_reason = '', last_seen = NULL, added = blocked_at, lend_cap = NULL, forget_asked = NULL,
			maintainer = 0, m_team = 0, leave_to_lend = 0 WHERE account_id = ?`, f.id)
	} else {
		exec(`DELETE FROM people WHERE account_id = ?`, f.id)
	}
	if err != nil {
		return err
	}
	if err := forgetRetired(tx, f); err != nil {
		return err
	}
	if err := forgetEverywhere(tx, f); err != nil {
		return err
	}
	return tx.Commit()
}

// forgetRetired takes the Volunteer off the records of their Retired Macs:
// who they were, how to reach them, and the terms they accepted.
func forgetRetired(tx *sql.Tx, f forgetting) error {
	rows, err := tx.Query(`SELECT id, data FROM retired_macs WHERE volunteer_id = ?`, f.id)
	if err != nil {
		return err
	}
	type retired struct{ id, data string }
	var all []retired
	for rows.Next() {
		var m retired
		if err := rows.Scan(&m.id, &m.data); err != nil {
			rows.Close()
			return err
		}
		all = append(all, m)
	}
	rows.Close()
	for _, m := range all {
		var d api.Device
		if err := json.Unmarshal([]byte(m.data), &d); err != nil {
			return err
		}
		d.VolunteerID, d.VolunteerName, d.Contact, d.NtfyOwnURL = 0, "", "", ""
		d.TermsSHA256, d.TermsAccepted = "", time.Time{}
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE retired_macs SET volunteer_id = 0, data = ? WHERE id = ?`, string(b), m.id); err != nil {
			return err
		}
	}
	return nil
}

// forgetEverywhere goes through every table but the unscrubbed: a column
// that holds an account id loses theirs (to 0), a text loses their handle.
// The Blocked state's own account id stays, and so does every Mac's name.
// Their own row is gone or emptied already.
func forgetEverywhere(tx *sql.Tx, f forgetting) error {
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		tables = append(tables, t)
	}
	rows.Close()
	for _, table := range tables {
		if unscrubbed[table] {
			continue
		}
		cols, err := tx.Query(`SELECT name, type FROM pragma_table_info(?)`, table)
		if err != nil {
			return err
		}
		type column struct{ name, kind string }
		var columns []column
		for cols.Next() {
			var c column
			cols.Scan(&c.name, &c.kind)
			columns = append(columns, c)
		}
		cols.Close()
		for _, c := range columns {
			q := `"` + table + `"`
			name := `"` + c.name + `"`
			switch {
			// The people left are another person's each: a handle there is
			// theirs, even one the forgotten person once had too.
			case table == "people" && (c.name == "account_id" || c.name == "handle"), keptNames[asciiLower(c.name)]:
			case strings.EqualFold(c.kind, "INTEGER"):
				if idKey(c.name) {
					if _, err := tx.Exec(`UPDATE `+q+` SET `+name+` = 0 WHERE `+name+` = ?`, f.id); err != nil {
						return err
					}
				}
			case strings.EqualFold(c.kind, "TEXT") || c.kind == "":
				if err := forgetInColumn(tx, q, name, f); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func forgetInColumn(tx *sql.Tx, table, column string, f forgetting) error {
	pattern := []any{}
	where := []string{}
	if f.handle != "" {
		where, pattern = append(where, column+` LIKE '%' || ? || '%'`), append(pattern, f.handle)
	}
	where, pattern = append(where, column+` LIKE '%' || ? || '%'`), append(pattern, f.idText)
	rows, err := tx.Query(`SELECT rowid, `+column+` FROM `+table+` WHERE typeof(`+column+`) = 'text' AND (`+strings.Join(where, " OR ")+`)`, pattern...)
	if err != nil {
		return err
	}
	type change struct {
		rowid int64
		text  string
	}
	var changes []change
	for rows.Next() {
		var c change
		var text string
		if err := rows.Scan(&c.rowid, &text); err != nil {
			rows.Close()
			return err
		}
		if c.text = f.stored(text); c.text != text {
			changes = append(changes, c)
		}
	}
	rows.Close()
	for _, c := range changes {
		if _, err := tx.Exec(`UPDATE `+table+` SET `+column+` = ? WHERE rowid = ?`, c.text, c.rowid); err != nil {
			return err
		}
	}
	return nil
}

// forgetInFiles takes the person off the files under dir, a line at a
// time: labd's copies of Audit logs (JSON lines) and the frozen Releases'
// notes. A file that changes is replaced whole, keeping its mode.
func forgetInFiles(dir string, f forgetting) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out, changed := forgetInLines(b, func(line string) string { return f.stored(line) })
		if !changed {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return replaceFile(path, out, info.Mode().Perm())
	})
}

// forgetInLines is b with each line passed through change, and whether any
// line changed.
func forgetInLines(b []byte, change func(string) string) ([]byte, bool) {
	var out bytes.Buffer
	changed := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if c := change(line); c != line {
			line, changed = c, true
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), changed
}

// replaceFile puts b in path's place with this mode, all at once.
func replaceFile(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".forget"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// forget takes the person off the caller log: the name they were logged
// under, and their account id where an address names it (/people/<id>/...).
// Lines written meanwhile wait for each file.
func (l *callerLog) forget(f forgetting) error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if err := l.forgetIn(filepath.Join(l.dir, e.Name()), e.Name(), f); err != nil {
			return err
		}
	}
	return nil
}

func (l *callerLog) forgetIn(path, name string, f forgetting) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changed := forgetInLines(b, func(line string) string {
		var c callerLine
		if json.Unmarshal([]byte(line), &c) != nil {
			return line
		}
		who, path := f.words(c.Who), forgetInPath(f.words(c.Path), f.idText)
		if who == c.Who && path == c.Path {
			return line
		}
		c.Who, c.Path = who, path
		j, err := json.Marshal(c)
		if err != nil {
			return line
		}
		return string(j)
	})
	if !changed {
		return nil
	}
	if name == l.day+".log" {
		l.closeLocked()
	}
	return replaceFile(path, out, 0o600)
}

// forgetInPath is an address with every part that is the account id, and
// every mention of the person's handle, made "-".
func forgetInPath(p, id string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if part == id {
			parts[i] = "-"
		}
	}
	return strings.Join(parts, "/")
}

// notePath has the caller log keep this address for r instead of its own:
// a request that forgets someone names them in its address.
func notePath(r *http.Request, path string) {
	if c, ok := r.Context().Value(callKey{}).(*call); ok {
		c.path = path
	}
}
