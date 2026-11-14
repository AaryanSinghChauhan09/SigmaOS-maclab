// Package sqlitedb opens one of labd's SQLite files the way both stores
// need it: WAL, fullfsync, one write connection whose transactions start
// with BEGIN IMMEDIATE, and a pool of read-only connections beside it.
package sqlitedb

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

const readers = 4

// DB is one SQLite file. Exec and Begin write, one at a time; Query and
// QueryRow read from the pool and never wait for a writer. A statement that
// writes and returns rows goes through a transaction.
type DB struct {
	w, r *sql.DB

	file string
	// watch is the connection DataVersion asks, opened at its first call.
	mu      sync.Mutex
	watchDB *sql.DB
	watch   *sql.Conn
}

// Open opens the file at path, making it if it is not there.
//
// synchronous is the driver's own default, named so that it stays. fullfsync
// only does something on macOS, where a plain fsync leaves the data in the
// drive's cache: the mini has no battery. It costs milliseconds a commit, and
// the server's tests step a clock faster than that, so a test binary leaves
// it off.
func Open(path string) (*DB, error) { return open(path, !testing.Testing()) }

func open(path string, fullfsync bool) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	file := (&url.URL{Scheme: "file", Path: abs}).String()
	write := file + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_txlock=immediate"
	if fullfsync {
		write += "&_pragma=fullfsync(1)"
	}
	w, err := sql.Open("sqlite", write)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	// The write connection makes the file and turns WAL on before a reader opens it.
	if err := w.Ping(); err != nil {
		w.Close()
		return nil, err
	}
	// mode=rw: a reader never makes the file.
	r, err := sql.Open("sqlite", file+"?mode=rw&_pragma=busy_timeout(5000)&_query_only=1")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(readers)
	r.SetMaxIdleConns(readers)
	d := &DB{w: w, r: r, file: file}
	if err := d.openReaders(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// openReaders opens every read connection and has it read the file. They
// stay open, so none meets the file for the first time later.
func (d *DB) openReaders() error {
	ctx := context.Background()
	var conns []*sql.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for range readers {
		c, err := d.r.Conn(ctx)
		if err != nil {
			return err
		}
		conns = append(conns, c)
		var n int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
			return err
		}
	}
	return nil
}

// DataVersion is a number that is another one after anything committed a
// change to the file: this DB's own writer, another connection, another
// program. SQLite counts for each connection apart, so it is always asked of
// the same one, which reads and never writes.
func (d *DB) DataVersion() (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctx := context.Background()
	if d.watch == nil {
		db, err := sql.Open("sqlite", d.file+"?mode=rw&_pragma=busy_timeout(5000)&_query_only=1")
		if err != nil {
			return 0, err
		}
		db.SetMaxOpenConns(1)
		c, err := db.Conn(ctx)
		if err != nil {
			db.Close()
			return 0, err
		}
		d.watchDB, d.watch = db, c
	}
	var v int64
	if err := d.watch.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v); err != nil {
		// Another connection would count from somewhere else: the next call starts over.
		d.watch.Close()
		d.watchDB.Close()
		d.watchDB, d.watch = nil, nil
		return 0, err
	}
	return v, nil
}

func (d *DB) Close() error {
	d.mu.Lock()
	if d.watch != nil {
		d.watch.Close()
		d.watchDB.Close()
		d.watchDB, d.watch = nil, nil
	}
	d.mu.Unlock()
	err := d.r.Close()
	if werr := d.w.Close(); werr != nil {
		err = werr
	}
	return err
}

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.w.Exec(query, args...)
}

// Begin starts a write transaction. It holds the write lock from the start,
// so what it reads cannot change before it writes.
func (d *DB) Begin() (*sql.Tx, error) { return d.w.Begin() }

func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.r.Query(query, args...)
}

func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.r.QueryRow(query, args...)
}
