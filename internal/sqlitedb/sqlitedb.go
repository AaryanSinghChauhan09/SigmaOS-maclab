// Package sqlitedb opens one of labd's SQLite files the way both stores
// need it: WAL, fullfsync, one write connection whose transactions start
// with BEGIN IMMEDIATE, and a pool of read-only connections beside it.
package sqlitedb

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

const readers = 4

// DB is one SQLite file. Exec and Begin write, one at a time; Query and
// QueryRow read from the pool and never wait for a writer. A statement that
// writes and returns rows goes through a transaction.
type DB struct{ w, r *sql.DB }

// Open opens the file at path, making it if it is not there.
//
// synchronous is the driver's own default, named so that it stays. fullfsync
// only does something on macOS, where a plain fsync leaves the data in the
// drive's cache: the mini has no battery.
func Open(path string) (*DB, error) {
	w, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=fullfsync(1)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	// The write connection makes the file and turns WAL on before a reader opens it.
	if err := w.Ping(); err != nil {
		w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_query_only=1")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(readers)
	r.SetMaxIdleConns(readers)
	return &DB{w, r}, nil
}

func (d *DB) Close() error {
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
