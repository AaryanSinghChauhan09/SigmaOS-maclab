// Package oldsite is the three tables of the old site, omarchy-m-testing.org
// (site/db/schema.rb): reports, tester_bindings and testers, as the import
// carries them. They are read once from the old site's Postgres into a dump
// file, and the import reads that file and nothing else.
package oldsite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Stamp is a time of the old site: microseconds since 1970, UTC, which is
// all a Postgres timestamp holds. In a dump it is written as Postgres
// writes it, to the microsecond.
type Stamp int64

const stampLayout = "2006-01-02T15:04:05.000000"

// ParseStamp reads a time written as to_char(at, 'YYYY-MM-DD"T"HH24:MI:SS.US').
func ParseStamp(text string) (Stamp, error) {
	at, err := time.Parse(stampLayout, text)
	if err != nil || at.Format(stampLayout) != text {
		return 0, fmt.Errorf("%q is not a time to the microsecond", text)
	}
	return Stamp(at.UnixMicro()), nil
}

func (s Stamp) Time() time.Time { return time.UnixMicro(int64(s)).UTC() }

func (s Stamp) String() string { return s.Time().Format(stampLayout) }

func (s Stamp) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

func (s *Stamp) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	at, err := ParseStamp(text)
	*s = at
	return err
}

// Report is a row of reports. Body is the jsonb column as text, as Postgres
// writes it out.
type Report struct {
	ID                  int64   `json:"id"`
	PublicID            string  `json:"public_id"`
	DeletionTokenDigest string  `json:"deletion_token_digest"`
	SchemaVersion       int64   `json:"schema_version"`
	Body                string  `json:"body"`
	CreatedAt           Stamp   `json:"created_at"`
	UpdatedAt           Stamp   `json:"updated_at"`
	HiddenAt            *Stamp  `json:"hidden_at"`
	MachineID           *string `json:"machine_id"`
	TesterLogin         *string `json:"tester_login"`
}

// TesterBinding is a row of tester_bindings: a Mac signed in as a GitHub
// account.
type TesterBinding struct {
	ID          int64  `json:"id"`
	MachineID   string `json:"machine_id"`
	GitHubLogin string `json:"github_login"`
	GitHubID    int64  `json:"github_id"`
	CreatedAt   Stamp  `json:"created_at"`
	UpdatedAt   Stamp  `json:"updated_at"`
}

// Tester is a row of testers, the old site's allowlist. GitHubID is nil for
// a handle that never signed in.
type Tester struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	GitHubID  *int64 `json:"github_id"`
	CreatedAt Stamp  `json:"created_at"`
	UpdatedAt Stamp  `json:"updated_at"`
}

// Counts is how many rows each table has.
type Counts struct {
	Reports        int64 `json:"reports"`
	TesterBindings int64 `json:"tester_bindings"`
	Testers        int64 `json:"testers"`
}

func (c Counts) String() string {
	return fmt.Sprintf("%d reports, %d tester_bindings, %d testers", c.Reports, c.TesterBindings, c.Testers)
}

// Sink takes the rows of the three tables.
type Sink interface {
	Tester(*Tester) error
	TesterBinding(*TesterBinding) error
	Report(*Report) error
}

// Source is the three tables at one moment: the old site's Postgres, or a
// dump file of it.
type Source interface {
	// Read hands sink every row: testers, then tester_bindings, then
	// reports, each table by id. It answers with the count of each table
	// as the source itself tells it.
	Read(ctx context.Context, sink Sink) (Counts, error)
}

// Funcs is a Sink made of functions; a nil one skips its table.
type Funcs struct {
	OnTester        func(*Tester) error
	OnTesterBinding func(*TesterBinding) error
	OnReport        func(*Report) error
}

func (f Funcs) Tester(row *Tester) error {
	if f.OnTester == nil {
		return nil
	}
	return f.OnTester(row)
}

func (f Funcs) TesterBinding(row *TesterBinding) error {
	if f.OnTesterBinding == nil {
		return nil
	}
	return f.OnTesterBinding(row)
}

func (f Funcs) Report(row *Report) error {
	if f.OnReport == nil {
		return nil
	}
	return f.OnReport(row)
}
