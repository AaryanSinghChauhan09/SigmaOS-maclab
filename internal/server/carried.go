package server

// What the import of the old site's reports brings into labd.db: people.
// The reports and the Mac sign-ins go to reports.db (internal/reports).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/reports"
)

// GitHubLookup asks GitHub whose a handle is now, without a token.
// githubURL is "" for GitHub itself, or a stand-in on this machine for a
// lab under development, such as http://127.0.0.1:7791.
func GitHubLookup(githubURL string) (reports.Lookup, error) {
	app := &GitHubApp{APIURL: githubAPI}
	if githubURL != "" {
		u, err := url.Parse(strings.TrimSuffix(githubURL, "/"))
		if err != nil || u.Scheme != "http" || !loopbackName(u.Host) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, fmt.Errorf("--github-url %q: only a stand-in for GitHub on this machine, such as http://127.0.0.1:7791", githubURL)
		}
		app.APIURL = u.String()
	}
	return func(ctx context.Context, handle string) (int64, string, error) {
		id, err := app.lookup(ctx, handle)
		if errors.Is(err, errNoSuchAccount) {
			return 0, "", reports.ErrNoSuchAccount
		}
		return id.ID, id.Login, err
	}, nil
}

// CarryPeople makes the lab know the people an import brings, in the
// labd.db in dataDir: each as a person with no mark, as a first sign-in
// would leave them, last seen when a Mac last signed in as them at the old
// site and never otherwise. A person the lab knows already keeps their
// handle, marks and times. All are kept or none; added is how many were
// new. Run again, it adds nobody.
func CarryPeople(dataDir string, people []reports.CarriedPerson, now time.Time) (added int, err error) {
	path := filepath.Join(dataDir, "labd.db")
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("no labd.db in %s: name labd's data directory with --data", dataDir)
	}
	for _, p := range people {
		if p.AccountID <= 0 || !reHandle.MatchString(p.Handle) {
			return 0, fmt.Errorf("account %d, %q: not a GitHub account and its handle", p.AccountID, p.Handle)
		}
	}
	st, err := openStore(path)
	if err != nil {
		return 0, err
	}
	defer st.db.Close()
	tx, err := st.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, p := range people {
		var seen sql.NullInt64
		if !p.SignedIn.IsZero() {
			seen = sql.NullInt64{Int64: p.SignedIn.UnixMicro(), Valid: true}
		}
		res, err := tx.Exec(`INSERT INTO people (account_id, handle, added, last_seen) VALUES (?, ?, ?, ?) ON CONFLICT (account_id) DO NOTHING`,
			p.AccountID, p.Handle, now.UnixMicro(), seen)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		added += int(n)
	}
	return added, tx.Commit()
}

// KnownAccounts counts how many of those accounts the labd.db in dataDir
// knows as people.
func KnownAccounts(dataDir string, accountIDs []int64) (known int, err error) {
	path := filepath.Join(dataDir, "labd.db")
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("no labd.db in %s: name labd's data directory with --data", dataDir)
	}
	st, err := openStore(path)
	if err != nil {
		return 0, err
	}
	defer st.db.Close()
	seen := map[int64]bool{}
	for _, id := range accountIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := st.person(id); err == nil {
			known++
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	return known, nil
}
