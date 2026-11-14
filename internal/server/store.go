package server

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/sqlitedb"
)

//go:embed migrations/*.sql
var labdMigrations embed.FS

// store is labd.db.
type store struct{ db *sqlitedb.DB }

func openStore(path string) (*store, error) {
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	migrations, err := fs.Sub(labdMigrations, "migrations")
	if err == nil {
		err = fromMaclab(db)
	}
	if err == nil {
		err = db.Migrate(migrations)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &store{db}, nil
}

// fromMaclab gives a labd.db from before numbered migrations the columns its
// tokens table gained after maclab first made it. The first migration makes
// whole tables and leaves one that is there as it finds it.
func fromMaclab(db *sqlitedb.DB) error {
	if v, err := db.Version(); err != nil || v > 0 {
		return err
	}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('tokens')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil || len(have) == 0 {
		return err
	}
	// Tokens from before volunteer and requester read as an admin's, for a
	// lab Mac. The rest came with Volunteer enrollment, one-time activation
	// (activated_by) and a Maintainer linking a Volunteer's login.
	for _, col := range []struct{ name, def string }{
		{"volunteer", "INTEGER NOT NULL DEFAULT 0"},
		{"requester", "TEXT NOT NULL DEFAULT ''"},
		{"volunteer_name", "TEXT NOT NULL DEFAULT ''"},
		{"contact", "TEXT NOT NULL DEFAULT ''"},
		{"activated_by", "TEXT NOT NULL DEFAULT ''"},
		{"volunteer_login", "TEXT NOT NULL DEFAULT ''"},
	} {
		if have[col.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE tokens ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) saveDevice(d *api.Device, secretHash string) error {
	b, _ := json.Marshal(d)
	_, err := s.db.Exec(`INSERT INTO devices(id,name,secret_hash,data) VALUES(?,?,?,?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, secret_hash=excluded.secret_hash, data=excluded.data`, d.ID, d.Name, secretHash, string(b))
	return err
}

// deleteDevice drops a device's record. Its jobs stay in the history.
func (s *store) deleteDevice(id string) error {
	_, err := s.db.Exec(`DELETE FROM devices WHERE id=?`, id)
	return err
}

type deviceRow struct {
	d          api.Device
	secretHash string
}

func (s *store) devices() ([]deviceRow, error) {
	rows, err := s.db.Query(`SELECT secret_hash, data FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deviceRow
	for rows.Next() {
		var r deviceRow
		var data string
		if err := rows.Scan(&r.secretHash, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &r.d); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const insertToken = `INSERT INTO tokens(token_hash,name,label,model,volunteer,requester,volunteer_name,contact,volunteer_login,expires) VALUES(?,?,?,?,?,?,?,?,?,?)`

// addToken stores a Maintainer's token, with the Volunteer's login they
// linked, if any. That login is no requester: the token is outside the
// person's own limits, and their ./volunteer never replaces it.
func (s *store) addToken(hash string, req api.EnrollTokenRequest, exp time.Time) error {
	_, err := s.db.Exec(insertToken, hash, req.Name, req.Label, req.Model, req.Volunteer, "", req.VolunteerName, req.Contact, req.VolunteerLogin, exp.Unix())
	return err
}

// errVolunteerTokens turns a volunteer away while too many others hold
// unused tokens. A token stays until its Mac first checks in (see token),
// but once a Mac has enrolled with it, it is used: it counts no more.
var errVolunteerTokens = errors.New("too many volunteers hold unused enrollment tokens")

// addVolunteerToken stores a token for who, the tailnet viewer who asked for
// it, in place of any unused one they still hold: nobody holds two, and
// asking again after a setup that stopped half way just works. It refuses
// with errVolunteerTokens while max others hold unused ones.
func (s *store) addVolunteerToken(hash string, req api.EnrollTokenRequest, who string, exp, now time.Time, max int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM tokens WHERE requester != '' AND (requester=? AND name NOT IN (SELECT name FROM devices) OR expires<?)`, who, now.Unix()); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM tokens WHERE requester != '' AND name NOT IN (SELECT name FROM devices)`).Scan(&n); err != nil {
		return err
	}
	if n >= max {
		return errVolunteerTokens
	}
	if _, err := tx.Exec(insertToken, hash, req.Name, req.Label, req.Model, req.Volunteer, who, req.VolunteerName, req.Contact, "", exp.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// token looks up an enrollment token that has not expired. VolunteeredBy is
// the login that asked for a volunteer token, VolunteerLogin the one a
// Maintainer linked.
func (s *store) token(hash string, now time.Time) (*api.EnrollTokenRequest, error) {
	var r api.EnrollTokenRequest
	var exp int64
	err := s.db.QueryRow(`SELECT name,label,model,volunteer,requester,volunteer_name,contact,volunteer_login,expires FROM tokens WHERE token_hash=?`, hash).
		Scan(&r.Name, &r.Label, &r.Model, &r.Volunteer, &r.VolunteeredBy, &r.VolunteerName, &r.Contact, &r.VolunteerLogin, &exp)
	if err != nil {
		return nil, err
	}
	if now.Unix() > exp {
		return nil, sql.ErrNoRows
	}
	return &r, nil
}

// tokenState is token, and the hash of the activation key that used the
// token up, "" while it is unused.
func (s *store) tokenState(hash string, now time.Time) (*api.EnrollTokenRequest, string, error) {
	t, err := s.token(hash, now)
	if err != nil {
		return nil, "", err
	}
	var by string
	err = s.db.QueryRow(`SELECT activated_by FROM tokens WHERE token_hash=?`, hash).Scan(&by)
	return t, by, err
}

// activateAndSave uses up an unused, unexpired token for the activation
// whose key hashes to keyHash and saves the device it makes, in one
// transaction, and says whether it did: of two enrollments racing with one
// token, one wins, and a crash leaves either both or neither.
func (s *store) activateAndSave(hash, keyHash string, now time.Time, d *api.Device, secretHash string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE tokens SET activated_by=? WHERE token_hash=? AND activated_by='' AND expires>=?`, keyHash, hash, now.Unix())
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	b, _ := json.Marshal(d)
	if _, err := tx.Exec(`INSERT INTO devices(id,name,secret_hash,data) VALUES(?,?,?,?)`, d.ID, d.Name, secretHash, string(b)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// revokeTokens deletes the enrollment tokens for a name, used or not, and
// says how many there were.
func (s *store) revokeTokens(name string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM tokens WHERE name=?`, name)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// spendTokens uses up the enrollment tokens for a name: its Mac has checked in.
func (s *store) spendTokens(name string) error {
	_, err := s.db.Exec(`DELETE FROM tokens WHERE name=?`, name)
	return err
}

func (s *store) saveJob(j *api.Job) error {
	b, _ := json.Marshal(j)
	_, err := s.db.Exec(`INSERT INTO jobs(id,device,created,state,data) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET state=excluded.state, data=excluded.data`, j.ID, j.Spec.Device, j.Created.UnixNano(), string(j.State), string(b))
	return err
}

func (s *store) job(id string) (*api.Job, error) {
	var data string
	if err := s.db.QueryRow(`SELECT data FROM jobs WHERE id=?`, id).Scan(&data); err != nil {
		return nil, err
	}
	var j api.Job
	return &j, json.Unmarshal([]byte(data), &j)
}

// gateExclusion is the stored job id's GateExclusion, or nil.
func (s *store) gateExclusion(id string) *api.GateExclusion {
	var data sql.NullString
	if s.db.QueryRow(`SELECT json_extract(data, '$.gate_exclusion') FROM jobs WHERE id=?`, id).Scan(&data) != nil || !data.Valid {
		return nil
	}
	var x api.GateExclusion
	if json.Unmarshal([]byte(data.String), &x) != nil {
		return nil
	}
	return &x
}

func (s *store) jobs(device string, limit int, unfinished bool) ([]*api.Job, error) {
	q := `SELECT data FROM jobs WHERE (?='' OR device=?)`
	if unfinished {
		q += ` AND state != 'done'`
	}
	q += ` ORDER BY created DESC LIMIT ?`
	return scanJobs(s.db.Query(q, device, device, limit))
}

// jobsOn lists the newest jobs on the Macs named, up to limit.
func (s *store) jobsOn(devices []string, limit int) ([]*api.Job, error) {
	if len(devices) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(devices)+1)
	for _, d := range devices {
		args = append(args, d)
	}
	q := `SELECT data FROM jobs WHERE device IN (?` + strings.Repeat(",?", len(devices)-1) + `) ORDER BY created DESC LIMIT ?`
	return scanJobs(s.db.Query(q, append(args, limit)...))
}

// jobsOfEnrollments lists the newest jobs submitted for the enrollments
// (Device.ID) given, up to limit: a person's jobs, which a Mac's name alone
// can't pick out once a forgotten Mac's name is taken again.
func (s *store) jobsOfEnrollments(ids []string, limit int) ([]*api.Job, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	q := `SELECT data FROM jobs WHERE json_extract(data, '$.device_id') IN (?` + strings.Repeat(",?", len(ids)-1) + `) ORDER BY created DESC LIMIT ?`
	return scanJobs(s.db.Query(q, append(args, limit)...))
}

// kernelJobs lists the finished jobs that ran a kernel artifact, newest first.
func (s *store) kernelJobs(sha string) ([]*api.Job, error) {
	return scanJobs(s.db.Query(`SELECT data FROM jobs WHERE json_extract(data, '$.spec.kernel')=? AND state='done' ORDER BY created DESC`, sha))
}

func scanJobs(rows *sql.Rows, err error) ([]*api.Job, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Job
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var j api.Job
		if err := json.Unmarshal([]byte(data), &j); err != nil {
			return nil, err
		}
		out = append(out, &j)
	}
	return out, rows.Err()
}

func (s *store) saveBuild(b *api.Build) error {
	d, _ := json.Marshal(b)
	_, err := s.db.Exec(`INSERT INTO builds(id,key,created,state,data) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET state=excluded.state, data=excluded.data`, b.ID, b.Key, b.Created.UnixNano(), string(b.State), string(d))
	return err
}

func (s *store) builds(where string, args ...any) ([]*api.Build, error) {
	rows, err := s.db.Query(`SELECT data FROM builds WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Build
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var b api.Build
		if err := json.Unmarshal([]byte(data), &b); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

func (s *store) configFor(device, kernel string) string {
	var sha string
	s.db.QueryRow(`SELECT sha FROM configs WHERE device=? AND kernel=?`, device, kernel).Scan(&sha)
	return sha
}

func (s *store) setConfig(device, kernel, sha string) error {
	_, err := s.db.Exec(`INSERT INTO configs(device,kernel,sha) VALUES(?,?,?) ON CONFLICT(device,kernel) DO UPDATE SET sha=excluded.sha`, device, kernel, sha)
	return err
}

// addBuilder records a named builder: gen names this one token of it, and
// tokenHash is that token's hash.
func (s *store) addBuilder(b api.Builder, gen, tokenHash string) error {
	_, err := s.db.Exec(`INSERT INTO builders(name,gen,token_hash,kinds,added,added_by) VALUES(?,?,?,?,?,?)`,
		b.Name, gen, tokenHash, strings.Join(b.Kinds, ","), b.Added.Unix(), b.AddedBy)
	return err
}

// builderByToken is the named builder holding a token hash, and its generation.
func (s *store) builderByToken(hash string) (api.Builder, string, error) {
	var b api.Builder
	var gen, kinds string
	var added int64
	err := s.db.QueryRow(`SELECT name,gen,kinds,added,added_by FROM builders WHERE token_hash=?`, hash).Scan(&b.Name, &gen, &kinds, &added, &b.AddedBy)
	b.Kinds, b.Added = strings.Split(kinds, ","), time.Unix(added, 0)
	return b, gen, err
}

// builderGenCurrent reports whether gen is still the token of a named builder.
func (s *store) builderGenCurrent(gen string) bool {
	var n int
	s.db.QueryRow(`SELECT count(*) FROM builders WHERE gen=?`, gen).Scan(&n)
	return gen != "" && n > 0
}

func (s *store) hasBuilder(name string) bool {
	var n int
	s.db.QueryRow(`SELECT count(*) FROM builders WHERE name=?`, name).Scan(&n)
	return n > 0
}

func (s *store) builders() ([]api.Builder, error) {
	rows, err := s.db.Query(`SELECT name,kinds,added,added_by FROM builders ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Builder{}
	for rows.Next() {
		var b api.Builder
		var kinds string
		var added int64
		if err := rows.Scan(&b.Name, &kinds, &added, &b.AddedBy); err != nil {
			return nil, err
		}
		b.Kinds, b.Added = strings.Split(kinds, ","), time.Unix(added, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// removeBuilder deletes a named builder and its token, returning the
// generation that ended; only, if set, deletes it only while it is that
// generation. ok is false when nothing was deleted.
func (s *store) removeBuilder(name, only string) (gen string, ok bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(`DELETE FROM builders WHERE name=? AND (?='' OR gen=?) RETURNING gen`, name, only, only).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return "", false, err
	}
	return gen, true, nil
}
