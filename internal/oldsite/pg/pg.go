// Package pg reads the old site's three tables from its Postgres, for the
// import command and nothing else: the Postgres client is linked into no
// other part of the lab.
//
// The address of that Postgres carries its password. It comes from the
// command's prompt, is kept in memory only, and no error from here repeats
// any part of it or anything the server said.
package pg

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/omacom/omarchy-maclab/internal/oldsite"
)

// tables is the old site's schema (site/db/schema.rb) as Postgres tells it:
// each table's columns and their types. A table with a column more or less
// is not read, so nothing the old site kept is left behind unnoticed.
var tables = map[string]map[string]string{
	"reports": {
		"id": "bigint", "public_id": "character varying", "deletion_token_digest": "character varying", "schema_version": "integer", "body": "jsonb",
		"created_at": "timestamp without time zone", "updated_at": "timestamp without time zone", "hidden_at": "timestamp without time zone",
		"machine_id": "character varying", "tester_login": "character varying",
	},
	"tester_bindings": {
		"id": "bigint", "machine_id": "character varying", "github_login": "character varying", "github_id": "bigint",
		"created_at": "timestamp without time zone", "updated_at": "timestamp without time zone",
	},
	"testers": {
		"id": "bigint", "login": "character varying", "github_id": "bigint",
		"created_at": "timestamp without time zone", "updated_at": "timestamp without time zone",
	},
}

// said is an error in this package's own words: it repeats nothing of the
// address and nothing the server sent.
type said string

func (s said) Error() string { return string(s) }

func sayf(format string, args ...any) error { return said(fmt.Sprintf(format, args...)) }

const shape = "postgresql://user:password@host:port/database"

// Postgres is the old site's database.
type Postgres struct {
	db  *sql.DB
	cfg *pgx.ConnConfig
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Open takes the address of the old site's Postgres, whole, as Railway
// shows it: postgresql://user:password@host:port/database. Nothing is read
// from the environment or from a file in its place. Beyond this machine
// the connection is made over TLS or not at all. Open connects to nothing.
func Open(address string) (*Postgres, error) {
	notOne := sayf("that is not a Postgres address: one reads %s", shape)
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.Opaque != "" || u.Fragment != "" {
		return nil, notOne
	}
	password, _ := u.User.Password()
	database := strings.TrimPrefix(u.Path, "/")
	if u.User.Username() == "" || password == "" || u.Hostname() == "" || strings.Contains(u.Host, ",") || database == "" || strings.Contains(database, "/") {
		return nil, notOne
	}
	query := u.Query()
	for name := range query {
		if name != "sslmode" {
			return nil, sayf("the address has an option this command does not take (%q): only sslmode is", name)
		}
	}
	mode := query.Get("sslmode")
	local := loopback(u.Hostname())
	switch mode {
	case "", "prefer", "require", "verify-ca", "verify-full":
	case "disable", "allow":
		if !local {
			return nil, sayf("the address turns TLS off (sslmode=%s): beyond this machine the old site's Postgres is read over TLS only", mode)
		}
	default:
		return nil, notOne
	}
	for _, pair := range os.Environ() {
		if name, _, _ := strings.Cut(pair, "="); strings.HasPrefix(name, "PG") {
			os.Unsetenv(name)
		}
	}
	cfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, notOne
	}
	if !local {
		// The modes that give none were refused above; this holds whatever the client makes of the rest.
		if cfg.TLSConfig == nil {
			return nil, sayf("the address turns TLS off: beyond this machine the old site's Postgres is read over TLS only")
		}
		var overTLS []*pgconn.FallbackConfig
		for _, fallback := range cfg.Fallbacks {
			if fallback.TLSConfig != nil {
				overTLS = append(overTLS, fallback)
			}
		}
		cfg.Fallbacks = overTLS
	}
	cfg.ConnectTimeout = 20 * time.Second
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(1)
	return &Postgres{db: db, cfg: cfg}, nil
}

func (p *Postgres) Close() error { return p.db.Close() }

// Read reads the three tables inside one read-only transaction, so that
// they are the tables of one moment, and writes nothing.
func (p *Postgres) Read(ctx context.Context, sink oldsite.Sink) (oldsite.Counts, error) {
	return read(ctx, p.db, sink)
}

// plain is a failure in words that are safe to print: a driver's own error
// names the host, the user and the database, and the server's may quote
// more.
func plain(err error) error {
	var own said
	var server *pgconn.PgError
	var dns *net.DNSError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var alert tls.AlertError
	var connect *pgconn.ConnectError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &own):
		return own
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return said("the old site's Postgres did not answer in time")
	case errors.Is(err, context.Canceled):
		return said("the read was stopped")
	case errors.As(err, &server):
		switch server.Code {
		case "28P01", "28000":
			return said("the old site's Postgres did not accept that user and password")
		case "3D000":
			return said("the old site's Postgres has no database of that name")
		case "42P01":
			return said("the old site's Postgres lacks one of the three tables")
		case "42501":
			return said("that user may not read the old site's tables")
		}
		return sayf("the old site's Postgres refused (SQLSTATE %s)", server.Code)
	case errors.As(err, &dns):
		return said("the address's host name was not found")
	case errors.Is(err, syscall.ECONNREFUSED):
		return said("the connection was refused at that host and port")
	case errors.As(err, &unknown), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &record), errors.As(err, &alert):
		return said("TLS with the old site's Postgres failed")
	case errors.As(err, &connect):
		return said("no connection could be made to the old site's Postgres")
	}
	return said("reading the old site's Postgres failed")
}

const stampFormat = `'YYYY-MM-DD"T"HH24:MI:SS.US'`

func stampOf(table, column, text string, id int64) (oldsite.Stamp, error) {
	at, err := oldsite.ParseStamp(text)
	if err != nil {
		return 0, sayf("%s row %d: its %s is not a time to the microsecond", table, id, column)
	}
	return at, nil
}

func read(ctx context.Context, db *sql.DB, sink oldsite.Sink) (oldsite.Counts, error) {
	var counts oldsite.Counts
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return counts, plain(err)
	}
	defer tx.Rollback()

	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, table := range names {
		if err := sameColumns(ctx, tx, table); err != nil {
			return counts, err
		}
	}
	for table, into := range map[string]*int64{"testers": &counts.Testers, "tester_bindings": &counts.TesterBindings, "reports": &counts.Reports} {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(into); err != nil {
			return counts, plain(err)
		}
	}

	rows, err := tx.QueryContext(ctx, "SELECT id, login, github_id, to_char(created_at, "+stampFormat+"), to_char(updated_at, "+stampFormat+") FROM testers ORDER BY id")
	if err != nil {
		return counts, plain(err)
	}
	err = each(rows, "testers", func() (int64, error) {
		var row oldsite.Tester
		var githubID sql.NullInt64
		var created, updated string
		if err := rows.Scan(&row.ID, &row.Login, &githubID, &created, &updated); err != nil {
			return row.ID, err
		}
		if githubID.Valid {
			row.GitHubID = &githubID.Int64
		}
		if row.CreatedAt, err = stampOf("testers", "created_at", created, row.ID); err != nil {
			return row.ID, err
		}
		if row.UpdatedAt, err = stampOf("testers", "updated_at", updated, row.ID); err != nil {
			return row.ID, err
		}
		return row.ID, sink.Tester(&row)
	})
	if err != nil {
		return counts, err
	}

	rows, err = tx.QueryContext(ctx, "SELECT id, machine_id, github_login, github_id, to_char(created_at, "+stampFormat+"), to_char(updated_at, "+stampFormat+") FROM tester_bindings ORDER BY id")
	if err != nil {
		return counts, plain(err)
	}
	err = each(rows, "tester_bindings", func() (int64, error) {
		var row oldsite.TesterBinding
		var created, updated string
		if err := rows.Scan(&row.ID, &row.MachineID, &row.GitHubLogin, &row.GitHubID, &created, &updated); err != nil {
			return row.ID, err
		}
		if row.CreatedAt, err = stampOf("tester_bindings", "created_at", created, row.ID); err != nil {
			return row.ID, err
		}
		if row.UpdatedAt, err = stampOf("tester_bindings", "updated_at", updated, row.ID); err != nil {
			return row.ID, err
		}
		return row.ID, sink.TesterBinding(&row)
	})
	if err != nil {
		return counts, err
	}

	rows, err = tx.QueryContext(ctx, "SELECT id, public_id, deletion_token_digest, schema_version, body::text, to_char(created_at, "+stampFormat+"), to_char(updated_at, "+stampFormat+
		"), to_char(hidden_at, "+stampFormat+"), machine_id, tester_login FROM reports ORDER BY id")
	if err != nil {
		return counts, plain(err)
	}
	err = each(rows, "reports", func() (int64, error) {
		var row oldsite.Report
		var created, updated string
		var hidden, machine, login sql.NullString
		if err := rows.Scan(&row.ID, &row.PublicID, &row.DeletionTokenDigest, &row.SchemaVersion, &row.Body, &created, &updated, &hidden, &machine, &login); err != nil {
			return row.ID, err
		}
		if row.CreatedAt, err = stampOf("reports", "created_at", created, row.ID); err != nil {
			return row.ID, err
		}
		if row.UpdatedAt, err = stampOf("reports", "updated_at", updated, row.ID); err != nil {
			return row.ID, err
		}
		if hidden.Valid {
			at, err := stampOf("reports", "hidden_at", hidden.String, row.ID)
			if err != nil {
				return row.ID, err
			}
			row.HiddenAt = &at
		}
		if machine.Valid {
			row.MachineID = &machine.String
		}
		if login.Valid {
			row.TesterLogin = &login.String
		}
		return row.ID, sink.Report(&row)
	})
	return counts, err
}

// each takes the rows of one table. A row that does not read as the old
// site's is told of by its table and the id before it, never by its text.
func each(rows *sql.Rows, table string, row func() (int64, error)) error {
	defer rows.Close()
	var last int64
	for rows.Next() {
		id, err := row()
		var own said
		switch {
		case err == nil:
			last = id
		case errors.As(err, &own):
			return own
		case strings.HasPrefix(err.Error(), "sql: Scan error"):
			return sayf("the row of %s after id %d does not read as the old site's", table, last)
		default:
			// The sink's own failure: a file that cannot be written.
			return err
		}
	}
	return plain(rows.Err())
}

func sameColumns(ctx context.Context, tx *sql.Tx, table string) error {
	rows, err := tx.QueryContext(ctx, "SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY column_name", table)
	if err != nil {
		return plain(err)
	}
	defer rows.Close()
	want := tables[table]
	seen := 0
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return plain(err)
		}
		switch expected, known := want[name]; {
		case !known:
			return sayf("the old site's %s has a column this command does not carry: %s. Nothing was read", table, name)
		case expected != kind:
			return sayf("the old site's %s.%s is %s, not %s. Nothing was read", table, name, kind, expected)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		return plain(err)
	}
	if seen == 0 {
		return sayf("the old site's Postgres has no table %s where that address looks", table)
	}
	if seen != len(want) {
		return sayf("the old site's %s lacks a column this command carries. Nothing was read", table)
	}
	return nil
}
