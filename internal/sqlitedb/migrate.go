package sqlitedb

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
)

type migration struct{ name, sql string }

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// Migrate applies, in order, each migration the database has not had. fsys
// holds them as 0001_name.sql, 0002_name.sql and so on with no gap; the
// number of the last one applied is the file's user_version. Each is applied
// whole or not at all.
//
// A migration may only add: tables, indexes and columns. So the Lab release
// before this one still runs on the database, which is the way back, and a
// database a later release has added to is left as it is.
func (d *DB) Migrate(fsys fs.FS) error {
	ms, err := load(fsys)
	if err != nil {
		return err
	}
	for {
		done, err := d.next(ms)
		if done || err != nil {
			return err
		}
	}
}

// Version is the number of the last migration applied.
func (d *DB) Version() (int, error) {
	var v int
	err := d.r.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v, err
}

func (d *DB) next(ms []migration) (done bool, err error) {
	tx, err := d.w.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return false, err
	}
	if v >= len(ms) {
		return true, nil
	}
	m := ms[v]
	if _, err := tx.Exec(m.sql); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.Exec(`PRAGMA user_version = ` + strconv.Itoa(v+1)); err != nil {
		return false, fmt.Errorf("migration %s: %w", m.name, err)
	}
	return false, tx.Commit()
}

func load(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("no migrations")
	}
	var ms []migration
	for i, e := range entries {
		name := e.Name()
		m := migrationName.FindStringSubmatch(name)
		if m == nil || e.IsDir() {
			return nil, fmt.Errorf("%s is not a migration: want %04d_name.sql", name, i+1)
		}
		if n, _ := strconv.Atoi(m[1]); n != i+1 {
			return nil, fmt.Errorf("migration %s: want number %04d", name, i+1)
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		if err := onlyAdds(string(body)); err != nil {
			return nil, fmt.Errorf("migration %s: %w", name, err)
		}
		ms = append(ms, migration{name, string(body)})
	}
	return ms, nil
}

var adds = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^CREATE TABLE (IF NOT EXISTS )?[\w"]+ ?\(`),
	regexp.MustCompile(`(?i)^CREATE (UNIQUE )?INDEX (IF NOT EXISTS )?[\w"]+ ON `),
	regexp.MustCompile(`(?i)^ALTER TABLE [\w"]+ ADD `),
}

func onlyAdds(sql string) error {
	stmts := statements(sql)
	if len(stmts) == 0 {
		return errors.New("holds no statement")
	}
next:
	for _, s := range stmts {
		for _, re := range adds {
			if re.MatchString(s) {
				continue next
			}
		}
		if len(s) > 60 {
			s = s[:60] + "..."
		}
		return fmt.Errorf("a migration may only add tables, indexes and columns, not: %s", s)
	}
	return nil
}

// statements splits SQL at the semicolons outside strings, quoted names and
// comments, drops the comments and squeezes white space.
func statements(sql string) []string {
	var out []string
	var b strings.Builder
	end := func() {
		if s := strings.Join(strings.Fields(b.String()), " "); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(sql) && sql[j] != c {
				j++
			}
			b.WriteString(sql[i:min(j+1, len(sql))])
			i = j
		case strings.HasPrefix(sql[i:], "--"):
			j := strings.IndexByte(sql[i:], '\n')
			if j < 0 {
				j = len(sql) - i
			}
			b.WriteByte(' ')
			i += j
		case strings.HasPrefix(sql[i:], "/*"):
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				j = len(sql) - i - 4
			}
			b.WriteByte(' ')
			i += j + 3
		case c == ';':
			end()
		default:
			b.WriteByte(c)
		}
	}
	end()
	return out
}
