package reports

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// Report is one stored report. Body is the report as its Mac signed it, less
// the signature; PublicID is the unguessable id in its link.
type Report struct {
	ID            int64
	PublicID      string
	SchemaVersion int
	Body          []byte
	CreatedAt     time.Time
	Hidden        bool
	MachineID     string
	// Handle and AccountID are the person the Mac was signed in as when the
	// report came, "" and 0 for nobody.
	Handle    string
	AccountID int64
	// Verified is the verdict stamped when the report came: a Verified run,
	// from a Mac signed in as an M team member then. Otherwise a Community run.
	Verified bool

	deletionTokenDigest string
}

// ErrNoReport is the answer for a report that is not there, and for a
// deletion link whose secret is not the report's.
var ErrNoReport = errors.New("reports: no such report")

// Document is the report's body, read. A body that cannot be read is an
// empty report.
func (r *Report) Document() *Object {
	value, _ := Parse(r.Body)
	if object, ok := value.(*Object); ok {
		return object
	}
	return &Object{members: map[string]any{}}
}

// DeletionTokenMatches reports whether token is the secret of the report's
// deletion link. Only its digest is kept.
func (r *Report) DeletionTokenMatches(token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(r.deletionTokenDigest), []byte(tokenDigest(token))) == 1
}

func tokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Digest is the digest the lab keeps of a report: of the Mac it came from
// and of what that Mac signed, so one report is one digest however it was
// written out or signed again.
func Digest(machineID string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(machineID))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

const base58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// newPublicID is 24 base58 characters, as the old site's ids are.
func newPublicID() string {
	id := make([]byte, 0, 24)
	buf := make([]byte, 48)
	for len(id) < 24 {
		rand.Read(buf)
		for _, b := range buf {
			// 232 is the largest multiple of 58 in a byte: no character is likelier than another.
			if b < 232 && len(id) < 24 {
				id = append(id, base58[b%58])
			}
		}
	}
	return string(id)
}

func newDeletionToken() string {
	secret := make([]byte, 32)
	rand.Read(secret)
	return base64.RawURLEncoding.EncodeToString(secret)
}

type scanner interface{ Scan(...any) error }

const reportColumns = `id, public_id, deletion_token_digest, schema_version, body, created_at, hidden_at, machine_id, handle, account_id, verified`

func scanReport(row scanner) (*Report, error) {
	var (
		r         Report
		body      string
		created   int64
		hidden    sql.NullInt64
		machine   sql.NullString
		handle    sql.NullString
		accountID sql.NullInt64
	)
	err := row.Scan(&r.ID, &r.PublicID, &r.deletionTokenDigest, &r.SchemaVersion, &body, &created, &hidden, &machine, &handle, &accountID, &r.Verified)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoReport
	}
	if err != nil {
		return nil, err
	}
	r.Body, r.CreatedAt, r.Hidden = []byte(body), time.UnixMicro(created).UTC(), hidden.Valid
	r.MachineID, r.Handle, r.AccountID = machine.String, handle.String, accountID.Int64
	return &r, nil
}

// Report is the report with that public id, hidden or not, or ErrNoReport.
func (s *Store) Report(publicID string) (*Report, error) {
	return scanReport(s.db.QueryRow(`SELECT `+reportColumns+` FROM reports WHERE public_id = ?`, publicID))
}

// Stored is a report just stored. DeletionToken is the secret of its
// deletion link: it is given once, here, and only its digest is kept.
type Stored struct {
	PublicID      string
	DeletionToken string
	Handle        string
	AccountID     int64
}

// held tells of the report with that digest: whether the lab has stored it,
// and its public id unless it was deleted since.
func (s *Store) held(digest string) (publicID string, held bool, err error) {
	return heldIn(s.db, digest)
}

type queryer interface {
	QueryRow(string, ...any) *sql.Row
}

func heldIn(db queryer, digest string) (publicID string, held bool, err error) {
	var id sql.NullString
	err = db.QueryRow(`SELECT r.public_id FROM report_digests d LEFT JOIN reports r ON r.id = d.report_id WHERE d.digest = ?`, digest).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id.String, err == nil, err
}

// add stores a judged report, stamped with whoever its Mac is signed in as,
// in one write transaction: a sign-out or another upload of the same report
// at the same moment waits for it. repeat is true, and nothing is stored,
// when the lab has stored the report before; heldAs is then its public id,
// "" once it was deleted.
func (s *Store) add(judged *Judged, at time.Time) (stored *Stored, heldAs string, repeat bool, err error) {
	digest := Digest(judged.MachineID, judged.Body)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", false, err
	}
	defer tx.Rollback()
	now := at.UnixMicro()
	res, err := tx.Exec(`INSERT INTO report_digests (digest, created_at) VALUES (?, ?) ON CONFLICT (digest) DO NOTHING`, digest, now)
	if err != nil {
		return nil, "", false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, "", false, err
	} else if n == 0 {
		heldAs, _, err := heldIn(tx, digest)
		return nil, heldAs, true, err
	}
	stored = &Stored{PublicID: newPublicID(), DeletionToken: newDeletionToken()}
	var handle sql.NullString
	var accountID sql.NullInt64
	err = tx.QueryRow(`SELECT handle, account_id FROM mac_sign_ins WHERE machine_id = ?`, judged.MachineID).Scan(&handle, &accountID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, err
	}
	stored.Handle, stored.AccountID = handle.String, accountID.Int64
	res, err = tx.Exec(`INSERT INTO reports (public_id, deletion_token_digest, schema_version, body, created_at, updated_at, machine_id, handle, account_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stored.PublicID, tokenDigest(stored.DeletionToken), judged.SchemaVersion, string(judged.Body), now, now, judged.MachineID, handle, accountID)
	if err != nil {
		return nil, "", false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, "", false, err
	}
	if _, err := tx.Exec(`UPDATE report_digests SET report_id = ? WHERE digest = ?`, id, digest); err != nil {
		return nil, "", false, err
	}
	return stored, "", false, tx.Commit()
}

// Delete deletes the report with that public id for good: the row, and with
// it the report and all its evidence. Its digest stays.
func (s *Store) Delete(publicID string) error {
	return s.delete(publicID, func(*Report) bool { return true })
}

// DeleteByLink deletes a report for whoever holds its deletion link: token
// is the link's secret. A wrong one deletes nothing and answers ErrNoReport,
// as a report that is not there does.
func (s *Store) DeleteByLink(publicID, token string) error {
	return s.delete(publicID, func(r *Report) bool { return r.DeletionTokenMatches(token) })
}

func (s *Store) delete(publicID string, allowed func(*Report) bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	report, err := scanReport(tx.QueryRow(`SELECT `+reportColumns+` FROM reports WHERE public_id = ?`, publicID))
	if err != nil {
		return err
	}
	if !allowed(report) {
		return ErrNoReport
	}
	if _, err := tx.Exec(`UPDATE report_digests SET report_id = NULL WHERE report_id = ?`, report.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM reports WHERE id = ?`, report.ID); err != nil {
		return err
	}
	return tx.Commit()
}
