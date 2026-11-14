package sqlitedb

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// openDB opens a database as labd does outside a test, fullfsync on.
func openDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

func pragma(t *testing.T, q interface{ QueryRow(string, ...any) *sql.Row }, name string) string {
	t.Helper()
	var v string
	if err := q.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

// The settings read back as set, on the write connection and on every
// connection of the read pool.
func TestSettingsReadBackAsSet(t *testing.T) {
	d, _ := openDB(t)
	for name, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "fullfsync": "1", "busy_timeout": "5000", "query_only": "0"} {
		if got := pragma(t, d.w, name); got != want {
			t.Errorf("write connection: %s is %s, want %s", name, got, want)
		}
	}
	var held []*sql.Conn
	for i := 0; i < readers; i++ {
		c, err := d.r.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
		for name, want := range map[string]string{"journal_mode": "wal", "busy_timeout": "5000", "query_only": "1"} {
			var got string
			if err := c.QueryRowContext(t.Context(), "PRAGMA "+name).Scan(&got); err != nil || got != want {
				t.Errorf("read connection %d: %s is %s (%v), want %s", i, name, got, err, want)
			}
		}
	}
	for _, c := range held {
		c.Close()
	}
}

// A test binary leaves fullfsync off and changes nothing else.
func TestATestBinaryLeavesFullfsyncOff(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for name, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "fullfsync": "0", "busy_timeout": "5000"} {
		if got := pragma(t, d.w, name); got != want {
			t.Errorf("%s is %s, want %s", name, got, want)
		}
	}
}

// The driver's own level, with nothing set: the answer section 17 of the
// spec asks for. Open sets the same level by name, so a later driver that
// changes its default changes nothing here.
func TestTheDriversDefaultSynchronousLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.db")
	for _, mode := range []string{"", "?_pragma=journal_mode(WAL)"} {
		db, err := sql.Open("sqlite", path+mode)
		if err != nil {
			t.Fatal(err)
		}
		if got := pragma(t, db, "synchronous"); got != "2" {
			t.Errorf("with %q the driver's synchronous is %s, want 2 (FULL)", mode, got)
		}
		if got := pragma(t, db, "fullfsync"); got != "0" {
			t.Errorf("with %q the driver's fullfsync is %s, want 0", mode, got)
		}
		db.Close()
	}
}

func TestReadsGoOnWhileAWriteTransactionIsOpen(t *testing.T) {
	d, _ := openDB(t)
	if _, err := d.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		got := make(chan int, 1)
		go func() {
			var n int
			if err := d.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
				n = -1
			}
			got <- n
		}()
		select {
		case n := <-got:
			return n
		case <-time.After(3 * time.Second):
			t.Fatal("a read waited for the open write transaction")
			return 0
		}
	}
	if n := count(); n != 0 {
		t.Fatalf("a read saw %d rows of a transaction not yet committed", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("after the commit a read saw %d rows, want 1", n)
	}
}

// A write transaction holds the write lock from its first statement, BEGIN
// IMMEDIATE: another connection cannot write until it ends.
func TestWriteTransactionsStartImmediate(t *testing.T) {
	d, path := openDB(t)
	if _, err := d.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Exec(`INSERT INTO t VALUES (2)`)
	if err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Errorf("another connection wrote while a write transaction was open: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`INSERT INTO t VALUES (2)`); err != nil {
		t.Errorf("after the transaction ended another connection could not write: %v", err)
	}
}

func TestTheReadPoolCannotWrite(t *testing.T) {
	d, _ := openDB(t)
	if _, err := d.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`INSERT INTO t VALUES (1) RETURNING n`).Scan(&n); err == nil {
		t.Error("a write went through the read pool")
	}
	if err := d.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d rows, %v", n, err)
	}
}

func TestCloseClosesBoth(t *testing.T) {
	d, _ := openDB(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`CREATE TABLE t (n INTEGER)`); err == nil {
		t.Error("a write worked after Close")
	}
	var n int
	if err := d.QueryRow(`SELECT 1`).Scan(&n); err == nil {
		t.Error("a read worked after Close")
	}
}

func files(m map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range m {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func schema(t *testing.T, d *DB) string {
	t.Helper()
	rows, err := d.Query(`SELECT type || ' ' || name || ' ' || coalesce(sql, '') FROM sqlite_master ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

// Each file depends on the one before, and none can run twice: the second
// would add a column that is there.
func TestMigrationsApplyOnceInOrder(t *testing.T) {
	d, path := openDB(t)
	one := files(map[string]string{"0001_first.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY);`})
	if err := d.Migrate(one); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Version(); err != nil || v != 1 {
		t.Fatalf("version %d, %v", v, err)
	}
	if _, err := d.Exec(`INSERT INTO a (id) VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	three := files(map[string]string{
		"0003_third.sql":  `CREATE INDEX a_third ON a (second, tenth);`,
		"0001_first.sql":  `CREATE TABLE a (id INTEGER PRIMARY KEY);`,
		"0002_second.sql": "ALTER TABLE a ADD COLUMN second TEXT NOT NULL DEFAULT '';\n-- a comment; with a semicolon\nALTER TABLE a ADD COLUMN tenth INTEGER;",
	})
	if err := d.Migrate(three); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Version(); v != 3 {
		t.Fatalf("version %d, want 3", v)
	}
	before := schema(t, d)
	if !strings.Contains(before, "a_third") || !strings.Contains(before, "tenth") {
		t.Fatalf("schema:\n%s", before)
	}

	// A second start.
	d.Close()
	d, err := open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Migrate(three); err != nil {
		t.Fatalf("a second start: %v", err)
	}
	if v, _ := d.Version(); v != 3 {
		t.Errorf("version %d after a second start", v)
	}
	if after := schema(t, d); after != before {
		t.Errorf("a second start changed the schema:\n%s\nwas:\n%s", after, before)
	}
	var id int
	if err := d.QueryRow(`SELECT id FROM a`).Scan(&id); err != nil || id != 7 {
		t.Errorf("the row: %d, %v", id, err)
	}
}

// A migration that fails leaves nothing of itself behind and is tried again
// on the next start.
func TestAFailedMigrationLeavesNothing(t *testing.T) {
	d, _ := openDB(t)
	bad := files(map[string]string{
		"0001_first.sql":  `CREATE TABLE a (id INTEGER PRIMARY KEY);`,
		"0002_second.sql": `CREATE TABLE b (id INTEGER PRIMARY KEY); ALTER TABLE missing ADD COLUMN c TEXT;`,
	})
	err := d.Migrate(bad)
	if err == nil || !strings.Contains(err.Error(), "0002_second.sql") {
		t.Fatalf("want an error naming the file, got %v", err)
	}
	if v, _ := d.Version(); v != 1 {
		t.Errorf("version %d, want 1", v)
	}
	if s := schema(t, d); strings.Contains(s, "table b") {
		t.Errorf("half of the failed migration stayed:\n%s", s)
	}
}

// The way back is the previous Lab release on a database the newer one has
// added to: it starts and leaves the database as it is.
func TestAnOlderReleaseStartsOnANewerDatabase(t *testing.T) {
	d, _ := openDB(t)
	newer := files(map[string]string{
		"0001_first.sql":  `CREATE TABLE a (id INTEGER PRIMARY KEY);`,
		"0002_second.sql": `ALTER TABLE a ADD COLUMN second TEXT;`,
	})
	if err := d.Migrate(newer); err != nil {
		t.Fatal(err)
	}
	before := schema(t, d)
	older := files(map[string]string{"0001_first.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY);`})
	if err := d.Migrate(older); err != nil {
		t.Fatalf("the older release refused the database: %v", err)
	}
	if v, _ := d.Version(); v != 2 {
		t.Errorf("version %d, want 2", v)
	}
	if schema(t, d) != before {
		t.Error("the older release changed the schema")
	}
}

func TestMigrationsAreNumberedWithoutGaps(t *testing.T) {
	for name, set := range map[string]map[string]string{
		"a gap":            {"0001_a.sql": `CREATE TABLE a (n);`, "0003_c.sql": `CREATE TABLE c (n);`},
		"no first":         {"0002_b.sql": `CREATE TABLE b (n);`},
		"a number twice":   {"0001_a.sql": `CREATE TABLE a (n);`, "0001_b.sql": `CREATE TABLE b (n);`},
		"a stray file":     {"0001_a.sql": `CREATE TABLE a (n);`, "notes.txt": `x`},
		"a short number":   {"1_a.sql": `CREATE TABLE a (n);`},
		"an unnamed file":  {"0001.sql": `CREATE TABLE a (n);`},
		"an empty file":    {"0001_a.sql": "-- nothing\n"},
		"no files at all":  {},
		"an upper-case id": {"0001_A.sql": `CREATE TABLE a (n);`},
	} {
		d, _ := openDB(t)
		if err := d.Migrate(files(set)); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if v, _ := d.Version(); v != 0 {
			t.Errorf("%s: version %d", name, v)
		}
	}
}

// A Lab release only adds to a database (spec, section 11).
func TestMigrationsOnlyAdd(t *testing.T) {
	for _, ok := range []string{
		`CREATE TABLE a (n INTEGER);`,
		`create table if not exists a (n integer);`,
		"CREATE TABLE a (\n  n INTEGER PRIMARY KEY AUTOINCREMENT,\n  body TEXT NOT NULL CHECK (json_valid(body))\n) STRICT;\nCREATE UNIQUE INDEX a_n ON a (n);",
		`CREATE TABLE a (n INTEGER); CREATE INDEX IF NOT EXISTS a_n ON a (n);`,
		`CREATE TABLE a (n INTEGER); ALTER TABLE a ADD COLUMN m TEXT NOT NULL DEFAULT '';`,
		`CREATE TABLE a (n INTEGER); ALTER TABLE a ADD m TEXT;`,
	} {
		d, _ := openDB(t)
		if err := d.Migrate(files(map[string]string{"0001_a.sql": ok})); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		`CREATE TABLE a (n INTEGER); DROP TABLE a;`,
		`CREATE TABLE a (n INTEGER); ALTER TABLE a RENAME TO b;`,
		`CREATE TABLE a (n INTEGER, m TEXT); ALTER TABLE a DROP COLUMN m;`,
		`CREATE TABLE a (n INTEGER, m TEXT); ALTER TABLE a RENAME COLUMN m TO k;`,
		`CREATE TABLE a (n INTEGER); DELETE FROM a;`,
		`CREATE TABLE a (n INTEGER); UPDATE a SET n = 1;`,
		`CREATE TABLE a (n INTEGER); INSERT INTO a VALUES (1);`,
		`CREATE TABLE a (n INTEGER); DROP INDEX IF EXISTS a_n;`,
		`CREATE TABLE a (n INTEGER); PRAGMA user_version = 9;`,
		`CREATE TABLE a AS SELECT 1 AS n;`,
		`CREATE TEMP TABLE a (n INTEGER);`,
		`CREATE TABLE a (n INTEGER); CREATE TRIGGER t AFTER INSERT ON a BEGIN DELETE FROM a; END;`,
		`CREATE TABLE a (n INTEGER); VACUUM;`,
	} {
		d, _ := openDB(t)
		err := d.Migrate(files(map[string]string{"0001_a.sql": bad}))
		if err == nil || !strings.Contains(err.Error(), "only add") {
			t.Errorf("%q accepted: %v", bad, err)
		}
		if s := schema(t, d); s != "" {
			t.Errorf("%q left something behind: %s", bad, s)
		}
	}
}

// One commit a time, with the drive told to write it out (F_FULLFSYNC, what
// Open sets) and with a plain fsync. On macOS the difference is the proof
// that fullfsync reaches the drive in this build; elsewhere there is none.
func BenchmarkCommit(b *testing.B) {
	for _, full := range []string{"1", "0"} {
		b.Run("fullfsync="+full, func(b *testing.B) {
			d, err := open(filepath.Join(b.TempDir(), "bench.db"), true)
			if err != nil {
				b.Fatal(err)
			}
			defer d.Close()
			if _, err := d.Exec(`PRAGMA fullfsync = ` + full + `; CREATE TABLE t (n INTEGER)`); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := d.Exec(`INSERT INTO t VALUES (?)`, i); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Every read connection is open from the start and stays: a reader that
// opened the file later could find it gone and would make an empty one.
func TestReadersOpenAtTheStartAndNeverMakeTheFile(t *testing.T) {
	d, path := openDB(t)
	if n := d.r.Stats().OpenConnections; n != readers {
		t.Errorf("%d read connections open after Open, want %d", n, readers)
	}
	d.r.SetMaxIdleConns(0)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err == nil {
		t.Error("a reader opened a database that is not there")
	}
	if left, _ := filepath.Glob(path + "*"); len(left) != 0 {
		t.Errorf("a reader made %v", left)
	}
}

func TestAPathIsTakenAsItIs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b?c#d%20e&_pragma=x")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, path := range []string{filepath.Join(dir, "whole.db"), "bare.db"} {
		d, err := open(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`CREATE TABLE t (n INTEGER)`); err != nil {
			t.Error(err)
		}
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
			t.Error(err)
		}
		d.Close()
		if _, err := os.Stat(filepath.Join(dir, filepath.Base(path))); err != nil {
			t.Error(err)
		}
	}
	if all, _ := os.ReadDir(filepath.Dir(dir)); len(all) != 1 {
		t.Errorf("files outside the folder: %v", all)
	}
}

// The data version stays while nothing is written and is another number
// after each commit, by this DB or by another that has the file open. A
// read changes nothing, and neither does a write that was rolled back.
func TestTheDataVersionChangesWithEachCommitAndNoRead(t *testing.T) {
	d, path := openDB(t)
	version := func() int64 {
		t.Helper()
		v, err := d.DataVersion()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if _, err := d.Exec(`CREATE TABLE a (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	first := version()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM a`).Scan(&n); err != nil || version() != first || version() != first {
		t.Fatalf("the data version moved with nothing written: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO a DEFAULT VALUES`); err != nil {
		t.Fatal(err)
	}
	second := version()
	if second == first {
		t.Fatal("a commit left the data version as it was")
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO a DEFAULT VALUES`); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if version() != second {
		t.Error("a write that was rolled back moved the data version")
	}
	other, err := open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(`INSERT INTO a DEFAULT VALUES`); err != nil {
		t.Fatal(err)
	}
	if version() == second {
		t.Error("another program's commit left the data version as it was")
	}
}
