package server

// The old lab's data. The labd on omarchy-gpu was maclab's: one admin token,
// nobody named, two records of a Mac that has since Joined another lab. Its
// data directory is copied to the mini and upgraded where it lies, once,
// before labd first opens it: every record, job and build keeps its id and
// what the old lab wrote in it; the Macs' records are Retired; what a
// person did there was done under the admin token, so it is the owner's;
// and nothing the old lab handed out opens anything in this one.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/sqlitedb"
)

// OldLabCounts is how much a data directory holds of what the old lab's
// labd kept: Mac records (Retired ones too), jobs and builds.
type OldLabCounts struct{ Records, Jobs, Builds int }

// OldLabUpgrade is what the upgrade of the old lab's data did, or in a dry
// run would do.
type OldLabUpgrade struct {
	Before, After OldLabCounts
	// Retired are the Mac records Retired, by name; LeasesEnded those whose
	// Lease was ended, and OutOfBand those whose out-of-band entry went.
	Retired, LeasesEnded, OutOfBand []string
	// JobsOwners and BuildsOwners are the jobs and builds now the owner's;
	// JobsEnded and BuildsEnded those the old labd left unfinished.
	JobsOwners, BuildsOwners int
	JobsEnded, BuildsEnded   int
	// What the old lab handed out and nothing honours: enrollment tokens,
	// Maintainers' tokens, builders' tokens, and the token files.
	EnrollTokens, Maintainers, Builders int
	TokenFiles                          []string
}

const oldLabRetiredWhy = "its record was Retired when the old lab's data was carried into Omarchy Mac Lab"

// oldLabTokenFiles are the old lab's credentials kept as files: the admin
// token, and the token of its own builder.
var oldLabTokenFiles = []string{"admin.token", "builder.token"}

var errOldLabDone = errors.New("already the lab's own: the old lab's data is upgraded once, before labd first opens it, and this labd.db has been opened by the lab's labd")

// CountOldLab counts the Mac records, jobs and builds of a data directory,
// before or after its upgrade. It changes nothing in it.
func CountOldLab(dataDir string) (OldLabCounts, error) {
	var c OldLabCounts
	dir, err := os.MkdirTemp("", "old-lab-count")
	if err != nil {
		return c, err
	}
	defer os.RemoveAll(dir)
	path, err := copyOfDB(dataDir, dir)
	if err != nil {
		return c, err
	}
	db, err := sqlitedb.Open(path)
	if err != nil {
		return c, err
	}
	defer db.Close()
	if err := oldLabTables(db); err != nil {
		return c, err
	}
	return countOldLab(db.QueryRow)
}

func countOldLab(row func(string, ...any) *sql.Row) (OldLabCounts, error) {
	var c OldLabCounts
	var retired int
	if err := row(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'retired_macs'`).Scan(&retired); err != nil {
		return c, err
	}
	records := `SELECT count(*) FROM devices`
	if retired > 0 {
		records = `SELECT (SELECT count(*) FROM devices) + (SELECT count(*) FROM retired_macs)`
	}
	if err := row(records).Scan(&c.Records); err != nil {
		return c, err
	}
	if err := row(`SELECT count(*) FROM jobs`).Scan(&c.Jobs); err != nil {
		return c, err
	}
	return c, row(`SELECT count(*) FROM builds`).Scan(&c.Builds)
}

// oldLabTables refuses a database that is not a labd's.
func oldLabTables(db *sqlitedb.DB) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('devices', 'jobs', 'builds')`).Scan(&n); err != nil {
		return err
	}
	if n != 3 {
		return errors.New("its labd.db is not a labd's: it lacks the tables of Mac records, jobs and builds")
	}
	return nil
}

// copyOfDB copies a data directory's labd.db, with what SQLite has not yet
// written into it, into dir, and returns the copy's path.
func copyOfDB(dataDir, dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dataDir, "labd.db")); err != nil {
		return "", fmt.Errorf("no labd.db in %s: name the old lab's data directory with --data", dataDir)
	}
	for _, name := range []string{"labd.db", "labd.db-wal"} {
		if err := copyFile(filepath.Join(dataDir, name), filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return filepath.Join(dir, "labd.db"), nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// UpgradeOldLab upgrades the old lab's data directory where it lies and
// says what it did. owner is the person everything done under the old
// lab's admin token becomes the work of. It runs once, on a labd.db no labd
// of this lab has opened: a second run, and the lab's own data, are refused
// with nothing changed. A dry run does the same to a copy of labd.db that
// it then deletes, and leaves the directory as it was.
func UpgradeOldLab(dataDir string, owner Person, now time.Time, dry bool) (*OldLabUpgrade, error) {
	if owner.AccountID <= 0 || !reHandle.MatchString(owner.Handle) {
		return nil, errors.New("name the owner by GitHub handle and account id: what was done under the old lab's admin token becomes theirs")
	}
	path := filepath.Join(dataDir, "labd.db")
	if dry {
		dir, err := os.MkdirTemp("", "old-lab-dry-run")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		if path, err = copyOfDB(dataDir, dir); err != nil {
			return nil, err
		}
	} else if _, err := UpgradeOldLab(dataDir, owner, now, true); err != nil {
		// What would fail fails on the copy, with the directory untouched.
		return nil, err
	}
	up, err := upgradeOldLabDB(path, owner, now)
	if err != nil {
		return nil, err
	}
	// The files are never read: whatever they hold opens nothing.
	for _, name := range oldLabTokenFiles {
		file := filepath.Join(dataDir, name)
		if _, err := os.Lstat(file); err != nil {
			continue
		}
		up.TokenFiles = append(up.TokenFiles, name)
		if !dry {
			if err := os.Remove(file); err != nil {
				return nil, err
			}
		}
	}
	return up, nil
}

func upgradeOldLabDB(path string, owner Person, now time.Time) (*OldLabUpgrade, error) {
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := oldLabTables(db); err != nil {
		return nil, err
	}
	if v, err := db.Version(); err != nil {
		return nil, err
	} else if v > 0 {
		return nil, errOldLabDone
	}
	up := &OldLabUpgrade{}
	if up.Before, err = countOldLab(db.QueryRow); err != nil {
		return nil, err
	}
	before, err := oldLabIDs(db.Query)
	if err != nil {
		return nil, err
	}
	if err := migrateStore(db); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO people (account_id, handle, added) VALUES (?, ?, ?)`, owner.AccountID, owner.Handle, now.UnixMicro()); err != nil {
		return nil, err
	}
	// The Maintainer the admin token became, on a labd that named them.
	var first string
	if err := tx.QueryRow(`SELECT name FROM maintainers WHERE added_by = '' ORDER BY added, rowid LIMIT 1`).Scan(&first); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	underAdminToken := func(name string) bool { return name == "" || name == first }
	stamp, _ := json.Marshal(now)

	type row struct {
		id, name, secretHash, state string
		data                        map[string]json.RawMessage
	}
	read := func(query string, withSecret bool) ([]row, error) {
		rows, err := tx.Query(query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			var data string
			dest := []any{&r.id, &r.state, &data}
			if withSecret {
				dest = []any{&r.id, &r.name, &r.secretHash, &data}
			}
			if err := rows.Scan(dest...); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(data), &r.data); err != nil {
				return nil, fmt.Errorf("%s: %v", r.id, err)
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
	text := func(raw json.RawMessage) string {
		var s string
		json.Unmarshal(raw, &s)
		return s
	}
	set := func(m map[string]json.RawMessage, key string, v any) {
		b, _ := json.Marshal(v)
		m[key] = b
	}
	held := func(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

	records, err := read(`SELECT id, name, secret_hash, data FROM devices ORDER BY name`, true)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if held(r.data["lease"]) {
			var l api.Lease
			if err := json.Unmarshal(r.data["lease"], &l); err != nil {
				return nil, fmt.Errorf("the Lease on %s: %v", r.name, err)
			}
			e := api.LeaseEvent{Time: now, Mac: r.name, Event: api.LeaseEnded, Handle: l.Owner(), Holder: l.Holder, Until: l.Expires,
				ByID: owner.AccountID, By: owner.Handle, Why: oldLabRetiredWhy}
			if underAdminToken(l.Owner()) {
				e.AccountID, e.Handle = owner.AccountID, owner.Handle
			}
			if _, err := tx.Exec(`INSERT INTO lease_record (at, mac, device_id, event, account_id, handle, holder, until, by_id, by, why) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.Time.UnixMicro(), e.Mac, r.id, e.Event, e.AccountID, e.Handle, e.Holder, e.Until.UnixMicro(), e.ByID, e.By, e.Why); err != nil {
				return nil, err
			}
			up.LeasesEnded = append(up.LeasesEnded, r.name)
		}
		if held(r.data["oob"]) {
			up.OutOfBand = append(up.OutOfBand, r.name)
		}
		// A Retired Mac holds nothing and nobody reaches it.
		for _, key := range []string{"lease", "oob", "active_job", "action"} {
			delete(r.data, key)
		}
		var volunteer int64
		json.Unmarshal(r.data["volunteer_id"], &volunteer)
		kept, _ := json.Marshal(r.data)
		if _, err := tx.Exec(`INSERT INTO retired_macs (id, name, retired, how, volunteer_id, data) VALUES (?, ?, ?, ?, ?, ?)`,
			r.id, r.name, now.UnixMicro(), retiredAtImport, volunteer, string(kept)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO mac_record (at, mac, device_id, event, line) VALUES (?, ?, ?, ?, ?)`,
			now.UnixMicro(), r.name, r.id, macLeft, "left the lab: "+oldLabRetiredWhy); err != nil {
			return nil, err
		}
		// A Mac that still has the secret learns that its enrollment ended.
		if _, err := tx.Exec(`INSERT OR REPLACE INTO ended (secret_hash, name, maintainer, at) VALUES (?, ?, ?, ?)`, r.secretHash, r.name, owner.Handle, now.Unix()); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM devices WHERE id = ?`, r.id); err != nil {
			return nil, err
		}
		up.Retired = append(up.Retired, r.name)
	}

	jobs, err := read(`SELECT id, state, data FROM jobs ORDER BY id`, false)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		changed := false
		if j.state != string(api.JobDone) {
			outcome, summary := api.OutcomeInfra, "the old lab's labd was stopped while the job was running"
			if j.state == string(api.JobQueued) {
				outcome, summary = api.OutcomeCanceled, "canceled: the old lab's labd was stopped before the job started"
			}
			set(j.data, "state", api.JobDone)
			set(j.data, "outcome", outcome)
			set(j.data, "summary", summary)
			j.data["updated"] = stamp
			j.state, changed = string(api.JobDone), true
			up.JobsEnded++
		}
		var spec struct {
			Holder string `json:"holder"`
		}
		json.Unmarshal(j.data["spec"], &spec)
		// A job with no name is a person's unless labd queued it itself.
		if underAdminToken(text(j.data["maintainer"])) && spec.Holder != omtHolder {
			set(j.data, "maintainer", owner.Handle)
			set(j.data, "account_id", owner.AccountID)
			changed = true
			up.JobsOwners++
		}
		if !changed {
			continue
		}
		b, _ := json.Marshal(j.data)
		if _, err := tx.Exec(`UPDATE jobs SET state = ?, data = ? WHERE id = ?`, j.state, string(b), j.id); err != nil {
			return nil, err
		}
	}

	builds, err := read(`SELECT id, state, data FROM builds ORDER BY id`, false)
	if err != nil {
		return nil, err
	}
	for _, b := range builds {
		changed := false
		if b.state == string(api.BuildQueued) || b.state == string(api.BuildRunning) {
			set(b.data, "state", api.BuildCanceled)
			set(b.data, "error", "the old lab's labd was stopped before the build was done")
			b.data["updated"] = stamp
			b.state, changed = string(api.BuildCanceled), true
			up.BuildsEnded++
		}
		if underAdminToken(text(b.data["asked_by"])) {
			set(b.data, "asked_by", owner.Handle)
			set(b.data, "asked_by_id", owner.AccountID)
			changed = true
			up.BuildsOwners++
		}
		if !changed {
			continue
		}
		d, _ := json.Marshal(b.data)
		if _, err := tx.Exec(`UPDATE builds SET state = ?, data = ? WHERE id = ?`, b.state, string(d), b.id); err != nil {
			return nil, err
		}
	}

	for table, n := range map[string]*int{"tokens": &up.EnrollTokens, "maintainers": &up.Maintainers, "builders": &up.Builders} {
		res, err := tx.Exec(`DELETE FROM ` + table)
		if err != nil {
			return nil, err
		}
		removed, _ := res.RowsAffected()
		*n = int(removed)
	}

	if up.After, err = countOldLab(tx.QueryRow); err != nil {
		return nil, err
	}
	after, err := oldLabIDs(tx.Query)
	if err != nil {
		return nil, err
	}
	if up.After != up.Before || !slices.Equal(after, before) {
		return nil, fmt.Errorf("the upgrade would not keep every record, job and build under its id (%+v before, %+v after): nothing was changed", up.Before, up.After)
	}
	return up, tx.Commit()
}

// oldLabIDs is every Mac record, job and build by kind and id, in order.
func oldLabIDs(query func(string, ...any) (*sql.Rows, error)) ([]string, error) {
	var retired int
	rows, err := query(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'retired_macs'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		rows.Scan(&retired)
	}
	rows.Close()
	q := `SELECT 'record ' || id FROM devices UNION ALL SELECT 'job ' || id FROM jobs UNION ALL SELECT 'build ' || id FROM builds`
	if retired > 0 {
		q += ` UNION ALL SELECT 'record ' || id FROM retired_macs`
	}
	rows, err = query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, rows.Err()
}
