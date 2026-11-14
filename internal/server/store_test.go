package server

import (
	"context"
	"database/sql"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// maclabDatabase makes a labd.db the way maclab's schema code made one at
// the fork: eight tables, then the columns tokens gained, one ALTER each.
func maclabDatabase(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS devices (id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, secret_hash TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tokens (token_hash TEXT PRIMARY KEY, name TEXT NOT NULL, label TEXT, model TEXT, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, device TEXT NOT NULL, created INTEGER NOT NULL, state TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS jobs_device ON jobs(device, created);
CREATE TABLE IF NOT EXISTS builds (id TEXT PRIMARY KEY, key TEXT NOT NULL, created INTEGER NOT NULL, state TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS builds_key ON builds(key, created);
CREATE TABLE IF NOT EXISTS maintainers (name TEXT PRIMARY KEY, token_hash TEXT UNIQUE NOT NULL, added INTEGER NOT NULL, added_by TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ended (secret_hash TEXT PRIMARY KEY, name TEXT NOT NULL, maintainer TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS configs (device TEXT NOT NULL, kernel TEXT NOT NULL, sha TEXT NOT NULL, PRIMARY KEY(device, kernel));
CREATE TABLE IF NOT EXISTS builders (name TEXT PRIMARY KEY, gen TEXT UNIQUE NOT NULL, token_hash TEXT UNIQUE NOT NULL, kinds TEXT NOT NULL, added INTEGER NOT NULL, added_by TEXT NOT NULL);
ALTER TABLE tokens ADD COLUMN volunteer INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tokens ADD COLUMN requester TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN volunteer_name TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN contact TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN activated_by TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN volunteer_login TEXT NOT NULL DEFAULT '';
INSERT INTO devices VALUES ('d-1', 'm2air', 'h1', '{"id":"d-1","name":"m2air","state":"ready"}');
INSERT INTO tokens (token_hash, name, label, model, expires, volunteer, requester) VALUES ('t1', 'm1air', 'left desk', 'laptop', 9999999999, 1, 'who@github');
INSERT INTO jobs VALUES ('j1', 'm2air', 1, 'done', '{"id":"j1","spec":{"device":"m2air"},"state":"done"}');
INSERT INTO builds VALUES ('b1', 'k', 1, 'done', '{"id":"b1","key":"k"}');
INSERT INTO maintainers VALUES ('marcelo', 'mh', 1, 'labd');
INSERT INTO ended VALUES ('eh', 'gone', 'marcelo', 2);
INSERT INTO configs VALUES ('m2air', '7.2', 'abc');
INSERT INTO builders VALUES ('mini', 'g1', 'bh', 'kernel', 3, 'marcelo');`)
	if err != nil {
		t.Fatal(err)
	}
}

func migrationFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// snapshot is everything in a database file.
type snapshot struct {
	version string // user_version
	schema  string // sqlite_master, as written
	shape   string // each table's columns, and the indexes
	rows    string // every row of every table
}

func snap(t *testing.T, path string) snapshot {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	list := func(q string, args ...any) []string {
		t.Helper()
		rows, err := db.Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var s sql.NullString
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			got = append(got, s.String)
		}
		return got
	}
	s := snapshot{
		version: list(`PRAGMA user_version`)[0],
		schema:  strings.Join(list(`SELECT type || ' ' || name || ' ' || coalesce(sql, '') FROM sqlite_master ORDER BY name`), "\n"),
	}
	shape := list(`SELECT 'index ' || name || ' on ' || tbl_name FROM sqlite_master WHERE type = 'index' ORDER BY name`)
	var rows []string
	for _, table := range list(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`) {
		cols := list(`SELECT name || ' ' || type || ' ' || "notnull" || ' ' || coalesce(dflt_value, 'NULL') || ' ' || pk FROM pragma_table_info(?) ORDER BY cid`, table)
		shape = append(shape, "table "+table+": "+strings.Join(cols, ", "))
		names := list(`SELECT '"' || name || '"' FROM pragma_table_info(?) ORDER BY cid`, table)
		// A table with no row says nothing of what is stored: later
		// migrations add tables, and maclab's rows must read the same.
		if all := list(`SELECT json_group_array(json_array(` + strings.Join(names, ",") + `)) FROM "` + table + `"`)[0]; all != "[]" {
			rows = append(rows, table+": "+all)
		}
	}
	s.shape, s.rows = strings.Join(shape, "\n"), strings.Join(rows, "\n")
	return s
}

// On a database made by maclab's schema code every migration applies once,
// in order, and a second start changes nothing.
func TestAMaclabDatabaseMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labd.db")
	maclabDatabase(t, path)
	maclab := snap(t, path)

	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	files := migrationFiles(t)
	var v int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != len(files) || v == 0 {
		t.Fatalf("version %d (%v) after %d migrations", v, err, len(files))
	}
	tok, err := st.token("t1", time.Now())
	if err != nil || tok.Name != "m1air" || !tok.Volunteer || tok.VolunteeredBy != "who@github" {
		t.Errorf("the token: %+v, %v", tok, err)
	}
	if rows, err := st.devices(); err != nil || len(rows) != 1 || rows[0].d.Name != "m2air" {
		t.Errorf("the Mac: %+v, %v", rows, err)
	}
	st.db.Close()
	first := snap(t, path)
	if first.rows != maclab.rows {
		t.Errorf("the migrations changed what maclab stored:\n%s\nwas:\n%s", first.rows, maclab.rows)
	}

	st, err = openStore(path)
	if err != nil {
		t.Fatalf("a second start: %v", err)
	}
	st.db.Close()
	if second := snap(t, path); second != first {
		t.Errorf("a second start changed the database:\n%+v\nwas:\n%+v", second, first)
	}

	// A new lab's database has the same tables, columns, indexes and version.
	fresh := filepath.Join(t.TempDir(), "labd.db")
	st, err = openStore(fresh)
	if err != nil {
		t.Fatal(err)
	}
	st.db.Close()
	if got := snap(t, fresh); got.shape != first.shape || got.version != first.version {
		t.Errorf("a new database, version %s:\n%s\na migrated one, version %s:\n%s", got.version, got.shape, first.version, first.shape)
	}
}

func TestStoreReadsGoOnWhileAWriteTransactionIsOpen(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO devices(id,name,secret_hash,data) VALUES('d1','m1','h','{"id":"d1","name":"m1"}')`); err != nil {
		t.Fatal(err)
	}
	macs := func() int {
		t.Helper()
		got := make(chan int, 1)
		go func() {
			rows, err := st.devices()
			if err != nil {
				got <- -1
				return
			}
			got <- len(rows)
		}()
		select {
		case n := <-got:
			return n
		case <-time.After(3 * time.Second):
			t.Fatal("a read waited for the open write transaction")
			return 0
		}
	}
	if n := macs(); n != 0 {
		t.Fatalf("a read saw %d Macs of a transaction not yet committed", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := macs(); n != 1 {
		t.Fatalf("after the commit a read saw %d Macs, want 1", n)
	}
}

func TestStoreWriteTransactionsStartImmediate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "labd.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	other, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = other.Exec(`INSERT INTO configs(device,kernel,sha) VALUES('m1','7.2','abc')`)
	if err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Errorf("another connection wrote while a write transaction was open: %v", err)
	}
}

// labd opens labd.db and reports.db, each in WAL and migrated.
func TestLabdOpensBothStores(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := New(ctx, Config{DataDir: dir, FirstMaintainer: harnessMaintainer, FirstMaintainerToken: "mtok"}, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"labd.db", "reports.db"} {
		db, err := sql.Open("sqlite", filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var mode string
		var v int
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		} else if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
			t.Errorf("%s: journal mode %q, %v", name, mode, err)
		} else if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v == 0 {
			t.Errorf("%s: version %d, %v", name, v, err)
		}
		db.Close()
	}
}

// removeBuilder deletes and reads in one statement, on the write connection.
func TestRemoveBuilderReturnsTheGeneration(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	if err := st.addBuilder(api.Builder{Name: "mini", Kinds: []string{"kernel"}, Added: time.Now(), AddedBy: "marcelo"}, "g1", "h1"); err != nil {
		t.Fatal(err)
	}
	if gen, ok, err := st.removeBuilder("mini", "g0"); gen != "" || ok || err != nil {
		t.Errorf("another generation: %q %v %v", gen, ok, err)
	}
	if gen, ok, err := st.removeBuilder("mini", ""); gen != "g1" || !ok || err != nil {
		t.Errorf("removing: %q %v %v", gen, ok, err)
	}
	if st.hasBuilder("mini") {
		t.Error("the builder is still there")
	}
	if gen, ok, err := st.removeBuilder("mini", ""); gen != "" || ok || err != nil {
		t.Errorf("removing again: %q %v %v", gen, ok, err)
	}
}
