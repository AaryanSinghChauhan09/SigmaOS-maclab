package reports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/oldsite"
)

// The import of the old site's reports. It fills an empty reports.db from
// the old site's three tables, read from a dump file: reports whole, with
// their ids, links, times and hidden mark; tester_bindings as Mac sign-ins;
// and for each report the verdict the old site shows on the day, stamped
// once. Everything is written in one transaction, so an import that was cut
// leaves the database empty and is simply started again.

// Listed is a report the import carried and has something to say about:
// one the lab's rules would refuse today, or one of the other things an
// owner should see before trusting the copy.
type Listed struct {
	PublicID string
	Reason   string
}

// Carried is what an import wrote.
type Carried struct {
	Reports, Hidden, Verified int
	SignIns                   int
	// Digests is how many digests were kept: one for each report, less the
	// ones the old site stored twice.
	Digests int
	Listed  []Listed
}

// NotEmpty is the import's refusal of a reports.db that holds something.
type NotEmpty struct{ Reports, SignIns, Digests int }

func (n *NotEmpty) Error() string {
	return fmt.Sprintf("reports.db already holds %d reports, %d Mac sign-ins and %d digests: the import fills an empty one, and changed nothing", n.Reports, n.SignIns, n.Digests)
}

// Holds counts what reports.db holds.
func (s *Store) Holds() (reports, hidden, signIns, digests int, err error) {
	err = s.db.QueryRow(`SELECT (SELECT count(*) FROM reports), (SELECT count(*) FROM reports WHERE hidden_at IS NOT NULL),
		(SELECT count(*) FROM mac_sign_ins), (SELECT count(*) FROM report_digests)`).Scan(&reports, &hidden, &signIns, &digests)
	return
}

// OfMachine counts the reports and the sign-ins of the Mac with that
// machine id.
func (s *Store) OfMachine(machineID string) (reports, signIns int, err error) {
	err = s.db.QueryRow(`SELECT (SELECT count(*) FROM reports WHERE machine_id = ?1), (SELECT count(*) FROM mac_sign_ins WHERE machine_id = ?1)`, machineID).Scan(&reports, &signIns)
	return
}

type carrying struct {
	ctx   context.Context
	tx    *sql.Tx
	rules *Rules
	out   *Carried

	allowed map[string]bool
	pinned  map[string]int64
	// signedIn is the account each Mac is signed in as, by machine id and
	// handle; accounts is every account a handle is signed in as anywhere.
	signedIn map[[2]string]int64
	accounts map[string]map[int64]bool
	digests  map[string]string
	counts   oldsite.Counts
}

func (c *carrying) Tester(row *oldsite.Tester) error {
	c.counts.Testers++
	login := strings.ToLower(row.Login)
	c.allowed[login] = true
	if row.GitHubID != nil {
		c.pinned[login] = *row.GitHubID
	}
	return nil
}

func (c *carrying) TesterBinding(row *oldsite.TesterBinding) error {
	c.counts.TesterBindings++
	login := strings.ToLower(row.GitHubLogin)
	c.signedIn[[2]string{row.MachineID, login}] = row.GitHubID
	if c.accounts[login] == nil {
		c.accounts[login] = map[int64]bool{}
	}
	c.accounts[login][row.GitHubID] = true
	c.out.SignIns++
	_, err := c.tx.ExecContext(c.ctx, `INSERT INTO mac_sign_ins (id, machine_id, account_id, handle, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		row.ID, row.MachineID, row.GitHubID, row.GitHubLogin, int64(row.CreatedAt), int64(row.UpdatedAt))
	return err
}

// accountOf is the account a report's handle was, as far as the old site's
// own tables tell: the one its Mac is signed in as under that handle, the
// one account every Mac under that handle is signed in as, or the one the
// allowlist pinned the handle to.
func (c *carrying) accountOf(machineID *string, login string) (int64, bool) {
	if machineID != nil {
		if id, ok := c.signedIn[[2]string{*machineID, login}]; ok {
			return id, true
		}
	}
	if accounts := c.accounts[login]; len(accounts) == 1 {
		for id := range accounts {
			return id, true
		}
	}
	id, ok := c.pinned[login]
	return id, ok
}

func (c *carrying) list(publicID, format string, args ...any) {
	c.out.Listed = append(c.out.Listed, Listed{publicID, fmt.Sprintf(format, args...)})
}

// judge lists what today's rules say against a carried report, in the
// order an upload is judged. The signature is not judged: the old site
// checked it and did not keep it.
func (c *carrying) judge(row *oldsite.Report, value any) {
	report, _ := value.(*Object)
	if report == nil {
		c.list(row.PublicID, "its body is not a report: a JSON object is one")
		return
	}
	if row.SchemaVersion != SchemaVersion {
		c.list(row.PublicID, "its schema_version is %d, and the lab's reports are v%d", row.SchemaVersion, SchemaVersion)
	} else if version, _ := report.members["schema_version"].(*big.Int); version == nil || !version.IsInt64() || version.Int64() != row.SchemaVersion {
		c.list(row.PublicID, "its body's schema_version is not its row's (%d)", row.SchemaVersion)
	}
	if problems := c.rules.schema.Errors(report); len(problems) > 0 {
		c.list(row.PublicID, "it does not match report schema v%d: %s", SchemaVersion, strings.Join(problems, "; "))
		return
	}
	if unknown := c.rules.catalogue.UnknownCheckIDs(report); len(unknown) > 0 {
		word := "a check"
		if len(unknown) > 1 {
			word = "checks"
		}
		c.list(row.PublicID, "it has %s the catalogue lacks: %s", word, strings.Join(unknown, ", "))
	}
	if problems := EvidenceErrors(report); len(problems) > 0 {
		c.list(row.PublicID, "its evidence breaks the rule (text, at most 64 KiB): %s", strings.Join(problems, "; "))
	}
}

func (c *carrying) Report(row *oldsite.Report) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	c.counts.Reports++
	var (
		handle    sql.NullString
		accountID sql.NullInt64
		verified  bool
	)
	if row.TesterLogin != nil {
		login := strings.ToLower(*row.TesterLogin)
		handle = sql.NullString{String: *row.TesterLogin, Valid: true}
		verified = c.allowed[login]
		if id, ok := c.accountOf(row.MachineID, login); ok {
			accountID = sql.NullInt64{Int64: id, Valid: true}
		} else {
			c.list(row.PublicID, "it names @%s, whose account the old site's tables do not tell: it keeps the handle and no account id", *row.TesterLogin)
		}
	}
	value, err := Parse([]byte(row.Body))
	if err != nil {
		return fmt.Errorf("report %s: its body is not JSON", row.PublicID)
	}
	c.judge(row, value)

	if _, err := c.tx.ExecContext(c.ctx, `INSERT INTO reports (id, public_id, deletion_token_digest, schema_version, body, created_at, updated_at, hidden_at, machine_id, handle, account_id, verified) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.PublicID, row.DeletionTokenDigest, row.SchemaVersion, row.Body, int64(row.CreatedAt), int64(row.UpdatedAt), (*int64)(row.HiddenAt), row.MachineID, handle, accountID, verified); err != nil {
		return fmt.Errorf("report %s: %w", row.PublicID, err)
	}
	c.out.Reports++
	if row.HiddenAt != nil {
		c.out.Hidden++
	}
	if verified {
		c.out.Verified++
	}

	// The digest an upload of the same report would have: of what its Mac
	// signed, however Postgres wrote it out.
	signed, err := Canonical(value)
	if err != nil {
		c.list(row.PublicID, "no digest could be made of it, so the same report sent again would be stored again: %v", err)
		return nil
	}
	machineID := ""
	if row.MachineID != nil {
		machineID = *row.MachineID
	}
	digest := Digest(machineID, signed)
	if first, twice := c.digests[digest]; twice {
		c.list(row.PublicID, "the old site stored it twice: it is %s again, and shares its digest", first)
		return nil
	}
	c.digests[digest] = row.PublicID
	c.out.Digests++
	_, err = c.tx.ExecContext(c.ctx, `INSERT INTO report_digests (digest, report_id, created_at) VALUES (?, ?, ?)`, digest, row.ID, int64(row.CreatedAt))
	return err
}

// Carry fills an empty reports.db from the old site's tables and answers
// with what it wrote. On a reports.db that holds a report, a Mac sign-in
// or a digest it writes nothing and answers *NotEmpty. It needs no key and
// no network, and the same source gives the same rows every time.
func (s *Store) Carry(ctx context.Context, src oldsite.Source) (*Carried, error) {
	rules, err := loadRules()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var full NotEmpty
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM reports), (SELECT count(*) FROM mac_sign_ins), (SELECT count(*) FROM report_digests)`).
		Scan(&full.Reports, &full.SignIns, &full.Digests); err != nil {
		return nil, err
	}
	if full != (NotEmpty{}) {
		return nil, &full
	}
	c := &carrying{ctx: ctx, tx: tx, rules: rules, out: &Carried{}, allowed: map[string]bool{}, pinned: map[string]int64{},
		signedIn: map[[2]string]int64{}, accounts: map[string]map[int64]bool{}, digests: map[string]string{}}
	said, err := src.Read(ctx, c)
	if err != nil {
		return nil, err
	}
	if said != c.counts {
		return nil, fmt.Errorf("the dump counts %v and gave %v: nothing was imported", said, c.counts)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c.out, nil
}

// CarriedPerson is a person the old site's tables name: a handle on its
// allowlist, or an account one of its Macs is signed in as. SignedIn is
// when a Mac last signed in as them there, zero for never.
type CarriedPerson struct {
	AccountID int64
	Handle    string
	SignedIn  time.Time
}

// Lookup asks GitHub whose a handle is now: the account's id, and the
// handle as the account writes it. ErrNoSuchAccount is GitHub saying it
// has no account under the handle.
type Lookup func(ctx context.Context, handle string) (accountID int64, asWritten string, err error)

var ErrNoSuchAccount = errors.New("GitHub knows no such account")

// CarriedPeople is the people an import brings: every account a carried
// Mac sign-in names, and every handle of testers, by account id. A handle
// that never signed in at the old site has no account id there, and
// lookup asks GitHub for it; one GitHub has no account for becomes nobody
// and a line of notes. Any other failure of lookup is CarriedPeople's.
func CarriedPeople(ctx context.Context, src oldsite.Source, lookup Lookup) (people []CarriedPerson, notes []string, err error) {
	var testers []oldsite.Tester
	var bindings []oldsite.TesterBinding
	if _, err := src.Read(ctx, oldsite.Funcs{
		OnTester:        func(row *oldsite.Tester) error { testers = append(testers, *row); return nil },
		OnTesterBinding: func(row *oldsite.TesterBinding) error { bindings = append(bindings, *row); return nil },
	}); err != nil {
		return nil, nil, err
	}
	byID := map[int64]*CarriedPerson{}
	byHandle := map[string]bool{}
	// Oldest sign-in first: an account that changed its handle is known by
	// the one it signed in with last.
	sort.SliceStable(bindings, func(i, j int) bool { return bindings[i].UpdatedAt < bindings[j].UpdatedAt })
	for _, row := range bindings {
		p := byID[row.GitHubID]
		if p == nil {
			p = &CarriedPerson{AccountID: row.GitHubID}
			byID[row.GitHubID] = p
		}
		p.Handle, p.SignedIn = row.GitHubLogin, row.UpdatedAt.Time()
		byHandle[strings.ToLower(row.GitHubLogin)] = true
	}
	for _, row := range testers {
		switch {
		case row.GitHubID != nil:
			if byID[*row.GitHubID] == nil {
				byID[*row.GitHubID] = &CarriedPerson{AccountID: *row.GitHubID, Handle: row.Login}
			}
		case byHandle[strings.ToLower(row.Login)]:
			// Signed in before the handle was listed: the sign-in tells the account.
		default:
			id, asWritten, err := lookup(ctx, row.Login)
			if errors.Is(err, ErrNoSuchAccount) {
				notes = append(notes, fmt.Sprintf("@%s never signed in at the old site, and GitHub knows no such account now: it becomes no person", row.Login))
				continue
			}
			if err != nil {
				return nil, nil, fmt.Errorf("asking GitHub whose @%s is: %w", row.Login, err)
			}
			if byID[id] == nil {
				byID[id] = &CarriedPerson{AccountID: id, Handle: asWritten}
			}
		}
	}
	for _, p := range byID {
		people = append(people, *p)
	}
	sort.Slice(people, func(i, j int) bool { return people[i].AccountID < people[j].AccountID })
	return people, notes, nil
}

// DeleteByOldLink deletes the report a deletion link names, whichever host
// the link was issued under: /reports/<public id>/deletion?token=<secret>.
// It fails unless the report was there, the link's secret was its own, the
// report is gone afterwards and its digest is kept. No error repeats the
// secret.
func (s *Store) DeleteByOldLink(link string) error {
	notALink := errors.New("that is not a deletion link: one reads https://<host>/reports/<id>/deletion?token=<secret>")
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return notALink
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	token := u.Query().Get("token")
	if u.Host == "" || len(parts) != 3 || parts[0] != "reports" || parts[2] != "deletion" || token == "" {
		return notALink
	}
	report, err := s.Report(parts[1])
	if errors.Is(err, ErrNoReport) {
		return fmt.Errorf("the lab has no report %s", parts[1])
	}
	if err != nil {
		return err
	}
	digest := ""
	if signed, err := Canonical(report.Document()); err == nil {
		digest = Digest(report.MachineID, signed)
	}
	if err := s.DeleteByLink(report.PublicID, token); errors.Is(err, ErrNoReport) {
		return fmt.Errorf("the link's secret is not the one of report %s: nothing was deleted", report.PublicID)
	} else if err != nil {
		return err
	}
	if _, err := s.Report(report.PublicID); !errors.Is(err, ErrNoReport) {
		return fmt.Errorf("report %s is still there", report.PublicID)
	}
	if _, held, err := s.held(digest); err != nil || !held {
		return fmt.Errorf("report %s was deleted, and its digest is not kept: the same report could be sent again", report.PublicID)
	}
	return nil
}
