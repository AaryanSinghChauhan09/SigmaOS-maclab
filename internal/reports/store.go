// Package reports keeps the lab's public record, reports.db: test reports
// and Mac sign-ins.
package reports

import (
	"embed"
	"io/fs"

	"github.com/omacom/omarchy-maclab/internal/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is reports.db. It holds no credential, and what it holds is enough
// to render the public site without labd.db.
type Store struct{ db *sqlitedb.DB }

// Open opens reports.db at path, making it or bringing it up to date.
func Open(path string) (*Store, error) {
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	files, err := fs.Sub(migrations, "migrations")
	if err == nil {
		err = db.Migrate(files)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
