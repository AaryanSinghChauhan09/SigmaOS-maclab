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

// token looks up an enrollment token that has not expired. VolunteerID is
// whose Mac it enrolls. VolunteeredBy and VolunteerLogin are a token's from
// before people signed in: the tailnet login that asked for it, and the one
// a Maintainer linked.
func (s *store) token(hash string, now time.Time) (*api.EnrollTokenRequest, error) {
	t, err := s.tokenState(hash, now)
	if err != nil {
		return nil, err
	}
	return &t.EnrollTokenRequest, nil
}

// tokenState is a token and what became of it.
type tokenState struct {
	api.EnrollTokenRequest
	// ActivatedBy is the hash of the activation key that used the token up,
	// "" while it is unused. BoundTo is the hash of the activation key of
	// the first Mac that used it at all, "" until one did.
	ActivatedBy, BoundTo string
	Expires              time.Time
}

func (s *store) tokenState(hash string, now time.Time) (*tokenState, error) {
	var t tokenState
	var exp int64
	err := s.db.QueryRow(`SELECT t.name,t.label,t.model,t.volunteer,t.requester,t.volunteer_name,t.contact,t.volunteer_login,t.expires,t.activated_by,
		COALESCE(j.volunteer_id,0),COALESCE(j.bound_to,'') FROM tokens t LEFT JOIN join_commands j ON j.token_hash=t.token_hash WHERE t.token_hash=?`, hash).
		Scan(&t.Name, &t.Label, &t.Model, &t.Volunteer, &t.VolunteeredBy, &t.VolunteerName, &t.Contact, &t.VolunteerLogin, &exp, &t.ActivatedBy, &t.VolunteerID, &t.BoundTo)
	if err != nil {
		return nil, err
	}
	if now.Unix() > exp {
		return nil, sql.ErrNoRows
	}
	t.Expires = time.Unix(exp, 0)
	return &t, nil
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
	return s.dropTokens(name)
}

// spendTokens uses up the enrollment tokens for a name: its Mac has checked in.
func (s *store) spendTokens(name string) error {
	_, err := s.dropTokens(name)
	return err
}

// dropTokens deletes the tokens for a name with what was kept beside them
// (see join_commands), and says how many tokens there were.
func (s *store) dropTokens(name string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM tokens WHERE name=?`, name)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if _, err := tx.Exec(dropOrphans); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
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
