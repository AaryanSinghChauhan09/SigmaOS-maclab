package reports

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/oldsite"
	"github.com/omacom/omarchy-maclab/internal/oldsite/oldsitetest"
)

// The import of the old site's reports, from a dump of a made-up old site
// (internal/oldsite/oldsitetest): what reports.db holds afterwards.

func oldSite(t testing.TB) *oldsitetest.Corpus {
	t.Helper()
	return oldsitetest.Made(t, railsMachineIDKey(t, paritySecretKeyBase))
}

// dumped is the corpus as the import reads it: from a dump file.
func dumped(t testing.TB, c *oldsitetest.Corpus) *oldsite.Dump {
	t.Helper()
	dump, err := oldsite.OpenDump(c.Dump(t))
	if err != nil {
		t.Fatal(err)
	}
	return dump
}

func imported(t *testing.T) (*desk, *oldsitetest.Corpus, *Carried) {
	t.Helper()
	d, c := newDesk(t), oldSite(t)
	result, err := d.store.Carry(context.Background(), dumped(t, c))
	if err != nil {
		t.Fatal(err)
	}
	return d, c, result
}

// everything is every row of every table of reports.db, as text.
func (s *Store) everything(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, q := range []string{
		`SELECT 'report ' || id || ' ' || public_id || ' ' || deletion_token_digest || ' ' || schema_version || ' ' || created_at || ' ' || updated_at || ' ' || coalesce(hidden_at, 'shown') || ' ' || coalesce(machine_id, 'no machine') || ' ' || coalesce(handle, 'nobody') || ' ' || coalesce(account_id, 0) || ' ' || verified || ' ' || body FROM reports ORDER BY id`,
		`SELECT 'sign-in ' || id || ' ' || machine_id || ' ' || account_id || ' ' || handle || ' ' || created_at || ' ' || updated_at FROM mac_sign_ins ORDER BY id`,
		`SELECT 'digest ' || digest || ' ' || coalesce(report_id, 0) || ' ' || created_at FROM report_digests ORDER BY id`,
		`SELECT 'sequence ' || name || ' ' || seq FROM sqlite_sequence ORDER BY name`,
	} {
		out = append(out, s.list(t, q)...)
	}
	return out
}

func (s *Store) one(t *testing.T, q string, args ...any) string {
	t.Helper()
	rows := s.list(t, q, args...)
	if len(rows) != 1 {
		t.Fatalf("%s: %d rows", q, len(rows))
	}
	return rows[0]
}

func TestTheImportCarriesEveryRowOfReportsHiddenOnesIncluded(t *testing.T) {
	d, c, result := imported(t)
	if got := d.store.one(t, `SELECT count(*) FROM reports`); got != fmt.Sprint(len(c.Reports)) {
		t.Fatalf("%s reports, the dump has %d", got, len(c.Reports))
	}
	if result.Reports != len(c.Reports) || result.Hidden != 2 || result.SignIns != len(c.Bindings) {
		t.Errorf("the import says %+v", result)
	}
	if got := d.store.one(t, `SELECT count(*) FROM reports WHERE hidden_at IS NOT NULL`); got != "2" {
		t.Errorf("%s hidden reports", got)
	}
	for _, row := range c.Reports {
		var (
			id, version, created, updated int64
			hidden                        *int64
			digest, body                  string
			machine, handle               *string
		)
		err := d.store.db.QueryRow(`SELECT id, deletion_token_digest, schema_version, body, created_at, updated_at, hidden_at, machine_id, handle FROM reports WHERE public_id = ?`, row.PublicID).
			Scan(&id, &digest, &version, &body, &created, &updated, &hidden, &machine, &handle)
		if err != nil {
			t.Fatalf("%s: %v", row.PublicID, err)
		}
		if id != row.ID || digest != row.DeletionTokenDigest || version != row.SchemaVersion || created != int64(row.CreatedAt) || updated != int64(row.UpdatedAt) {
			t.Errorf("%s: id %d, digest %s, schema %d, created %d, updated %d", row.PublicID, id, digest, version, created, updated)
		}
		if body != row.Body {
			t.Errorf("%s: its body is not the old site's text", row.PublicID)
		}
		if (hidden == nil) != (row.HiddenAt == nil) || hidden != nil && *hidden != int64(*row.HiddenAt) {
			t.Errorf("%s: hidden_at %v", row.PublicID, hidden)
		}
		if !reflect.DeepEqual(machine, row.MachineID) || !reflect.DeepEqual(handle, row.TesterLogin) {
			t.Errorf("%s: machine id or handle differ", row.PublicID)
		}
	}
	report, err := d.store.Report(oldsitetest.Hidden)
	if err != nil || !report.Hidden || !report.CreatedAt.Equal(time.Date(2026, 10, 2, 12, 0, 0, 999999000, time.UTC)) {
		t.Errorf("the hidden report: %+v %v", report, err)
	}
	if model := at(report.Document(), "machine", "board"); model == nil {
		t.Error("a carried body does not read as a report")
	}
}

func TestTesterBindingsBecomeMacSignInsToTheMicrosecond(t *testing.T) {
	d, c, _ := imported(t)
	want := []string{}
	for _, row := range c.Bindings {
		want = append(want, fmt.Sprintf("sign-in %d %s %d %s %d %d", row.ID, row.MachineID, row.GitHubID, row.GitHubLogin, int64(row.CreatedAt), int64(row.UpdatedAt)))
	}
	got := d.store.list(t, `SELECT 'sign-in ' || id || ' ' || machine_id || ' ' || account_id || ' ' || handle || ' ' || created_at || ' ' || updated_at FROM mac_sign_ins ORDER BY id`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mac_sign_ins:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A Mac's saved status names its sign-in by that time, as the old site did.
	signIn, err := d.store.SignInOf(c.Bindings[0].MachineID)
	if err != nil || signIn.At.UnixMicro() != int64(c.Bindings[0].UpdatedAt) || signIn.At.Nanosecond() != 999999000 {
		t.Errorf("the carried sign-in: %+v %v", signIn, err)
	}
}

func TestEachReportKeepsTheVerificationTheOldSiteShows(t *testing.T) {
	d, c, result := imported(t)
	allowed := map[string]bool{}
	for _, tester := range c.Testers {
		allowed[tester.Login] = true
	}
	verified := 0
	for _, row := range c.Reports {
		report, err := d.store.Report(row.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		want := row.TesterLogin != nil && allowed[*row.TesterLogin]
		if report.Verified != want {
			t.Errorf("%s by %v: verified %v", row.PublicID, row.TesterLogin, report.Verified)
		}
		if want {
			verified++
		}
	}
	if result.Verified != verified || verified != 4+4 {
		t.Errorf("%d Verified runs, the import says %d", verified, result.Verified)
	}
	for id, want := range map[string]struct {
		verified bool
		handle   string
		account  int64
	}{
		oldsitetest.Verified:     {true, "maralcbr", 1001},
		oldsitetest.UnknownCheck: {true, "scottjones", 1002},
		oldsitetest.Outsider:     {false, "outsider", 5005},
		oldsitetest.LeftTheTeam:  {false, "left-the-team", 0},
		oldsitetest.Hidden:       {false, "", 0},
		c.Reports[0].PublicID:    {true, "demo-tester", 4242},
		c.Reports[1].PublicID:    {false, "", 0},
	} {
		report, err := d.store.Report(id)
		if err != nil {
			t.Fatal(err)
		}
		if report.Verified != want.verified || report.Handle != want.handle || report.AccountID != want.account {
			t.Errorf("%s: verified %v, @%s, account %d", id, report.Verified, report.Handle, report.AccountID)
		}
	}
}

func TestEveryImportedReportPassesTheRulesOrIsListedWithItsReason(t *testing.T) {
	d, c, result := imported(t)
	listed := map[string][]string{}
	for _, l := range result.Listed {
		listed[l.PublicID] = append(listed[l.PublicID], l.Reason)
	}
	want := map[string][]string{
		oldsitetest.UnknownCheck: {"it has a check the catalogue lacks: " + oldsitetest.GoneCheck},
		oldsitetest.Twice:        {"the old site stored it twice: it is " + oldsitetest.Verified + " again, and shares its digest"},
		oldsitetest.LeftTheTeam:  {"it names @left-the-team, whose account the old site's tables do not tell: it keeps the handle and no account id"},
	}
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("listed:\n%v\nwant\n%v", listed, want)
	}
	rules := testRules(t)
	for _, row := range c.Reports {
		if row.PublicID == oldsitetest.UnknownCheck {
			continue
		}
		report, err := d.store.Report(row.PublicID)
		if err != nil {
			t.Fatal(err)
		}
		doc := report.Document()
		if problems := rules.SchemaErrors(doc); len(problems) > 0 {
			t.Errorf("%s is not listed and fails the schema: %v", row.PublicID, problems)
		}
		if unknown := rules.catalogue.UnknownCheckIDs(doc); len(unknown) > 0 {
			t.Errorf("%s is not listed and has unknown checks: %v", row.PublicID, unknown)
		}
		if problems := EvidenceErrors(doc); len(problems) > 0 {
			t.Errorf("%s is not listed and fails the evidence rule: %v", row.PublicID, problems)
		}
	}
}

func TestABodyTheRulesRefuseIsCarriedAndListed(t *testing.T) {
	c := oldSite(t)
	c.Reports = c.Reports[:3]
	c.Reports[0].Body = `{"machine": {"board": "j416c"}, "schema_version": 1}`
	c.Reports[1].Body = `[1, 2]`
	c.Reports[2].SchemaVersion = 0
	d := newDesk(t)
	result, err := d.store.Carry(context.Background(), dumped(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if d.count(t) != 3 || result.Reports != 3 {
		t.Fatalf("%d reports carried", d.count(t))
	}
	reasons := map[string]string{}
	for _, l := range result.Listed {
		reasons[l.PublicID] += l.Reason + "\n"
	}
	for i, want := range []string{"it does not match report schema v1: ", "its body is not a report: a JSON object", "its schema_version is 0, and the lab's reports are v1"} {
		if !strings.Contains(reasons[c.Reports[i].PublicID], want) {
			t.Errorf("report %d is listed with %q", i, reasons[c.Reports[i].PublicID])
		}
	}
}

func TestDigestsAreKeptSoADeletedReportCannotReturn(t *testing.T) {
	d, c, result := imported(t)
	digests := d.store.one(t, `SELECT count(*) FROM report_digests`)
	if digests != fmt.Sprint(len(c.Reports)-1) || result.Digests != len(c.Reports)-1 {
		t.Errorf("%s digests for %d reports, one of them stored twice; the import says %d", digests, len(c.Reports), result.Digests)
	}
	if orphans := d.store.one(t, `SELECT count(*) FROM reports r WHERE r.public_id != ? AND NOT EXISTS (SELECT 1 FROM report_digests d WHERE d.report_id = r.id)`, oldsitetest.Twice); orphans != "0" {
		t.Errorf("%s reports have no digest", orphans)
	}
	// The Mac sends the report the old site already had.
	again := d.post(t, golden(t, "m2-max-image2"), "a")
	if again.status != http.StatusConflict || again.text("report_url") != site+"/reports/"+oldsitetest.Verified {
		t.Fatalf("the carried report sent again: %d %v", again.status, again.body)
	}
	before := d.count(t)
	// Both copies the old site stored are deleted, the first by its old link.
	if err := d.store.DeleteByLink(oldsitetest.Verified, oldsitetest.VerifiedToken); err != nil {
		t.Fatal(err)
	}
	if err := d.store.Delete(oldsitetest.Twice); err != nil {
		t.Fatal(err)
	}
	if d.count(t) != before-2 {
		t.Fatal("the old link deleted nothing")
	}
	back := d.post(t, golden(t, "m2-max-image2"), "a")
	if back.status != http.StatusConflict || back.text("error") != "Already uploaded" || back.text("report_url") != "" || d.count(t) != before-2 {
		t.Errorf("a deleted report came back: %d %v", back.status, back.body)
	}
	// Another run on that Mac is a new report, and its id goes on from the carried ones.
	next := d.post(t, run(t, 7), "a")
	if next.status != http.StatusCreated {
		t.Fatalf("a new run: %d %v", next.status, next.body)
	}
	if report, err := d.store.Report(next.text("id")); err != nil || report.ID != 61 {
		t.Errorf("the next report: %+v %v", report, err)
	}
}

func TestASecondImportRefusesAndChangesNothing(t *testing.T) {
	d, c, _ := imported(t)
	before := d.store.everything(t)
	_, err := d.store.Carry(context.Background(), dumped(t, c))
	var full *NotEmpty
	if !errors.As(err, &full) || full.Reports != len(c.Reports) || full.SignIns != len(c.Bindings) {
		t.Fatalf("a second import: %v", err)
	}
	if after := d.store.everything(t); !reflect.DeepEqual(before, after) {
		t.Error("the second import changed reports.db")
	}
}

func TestTheImportFillsOnlyAnEmptyDatabase(t *testing.T) {
	c := oldSite(t)
	for name, fill := range map[string]func(d *desk){
		"a report": func(d *desk) { d.post(t, golden(t, "m2-max-image2")) },
		"a deleted report's digest": func(d *desk) {
			links := d.post(t, golden(t, "m2-max-image2"))
			if err := d.store.Delete(links.text("id")); err != nil {
				t.Fatal(err)
			}
		},
		"a Mac sign-in": func(d *desk) {
			if _, err := d.store.db.Exec(`INSERT INTO mac_sign_ins (machine_id, account_id, handle, created_at, updated_at) VALUES ('key:x', 1, 'ada', ?, ?)`, d.now.UnixMicro(), d.now.UnixMicro()); err != nil {
				t.Fatal(err)
			}
		},
	} {
		d := newDesk(t)
		fill(d)
		before := d.store.everything(t)
		var full *NotEmpty
		if _, err := d.store.Carry(context.Background(), dumped(t, c)); !errors.As(err, &full) {
			t.Errorf("with %s in it the import ran: %v", name, err)
		}
		if after := d.store.everything(t); !reflect.DeepEqual(before, after) {
			t.Errorf("with %s in it the import changed something", name)
		}
	}
}

// cut is a source that stops part way through reports.
type cut struct {
	oldsite.Source
	after int
	stop  func()
}

func (c cut) Read(ctx context.Context, sink oldsite.Sink) (oldsite.Counts, error) {
	n := 0
	return c.Source.Read(ctx, oldsite.Funcs{OnTester: sink.Tester, OnTesterBinding: sink.TesterBinding, OnReport: func(row *oldsite.Report) error {
		if n++; n > c.after {
			if c.stop != nil {
				c.stop()
				return ctx.Err()
			}
			return errors.New("the file ended")
		}
		return sink.Report(row)
	}})
}

func TestAnImportThatWasCutLeavesNothingAndCanBeStartedAgain(t *testing.T) {
	whole, c, _ := imported(t)
	want := whole.store.everything(t)
	d := newDesk(t)
	empty := d.store.everything(t)
	for _, after := range []int{0, 1, len(c.Reports) - 1} {
		if _, err := d.store.Carry(context.Background(), cut{Source: dumped(t, c), after: after}); err == nil {
			t.Fatalf("an import cut after %d reports was kept", after)
		}
		if got := d.store.everything(t); !reflect.DeepEqual(got, empty) {
			t.Fatalf("an import cut after %d reports left %d rows", after, len(got))
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	if _, err := d.store.Carry(ctx, cut{Source: dumped(t, c), after: 5, stop: stop}); err == nil {
		t.Fatal("a stopped import was kept")
	}
	if got := d.store.everything(t); !reflect.DeepEqual(got, empty) {
		t.Fatal("a stopped import left rows")
	}
	if _, err := d.store.Carry(context.Background(), dumped(t, c)); err != nil {
		t.Fatal(err)
	}
	if got := d.store.everything(t); !reflect.DeepEqual(got, want) {
		t.Error("the import started again is not the import that was never cut")
	}
}

func TestADumpWhoseCountsAreNotItsRowsIsNotImported(t *testing.T) {
	c := oldSite(t)
	d := newDesk(t)
	short := cut{Source: c, after: len(c.Reports)}
	lying := oldsite.Source(sourceFunc(func(ctx context.Context, sink oldsite.Sink) (oldsite.Counts, error) {
		counts, err := short.Read(ctx, sink)
		counts.Reports++
		return counts, err
	}))
	if _, err := d.store.Carry(context.Background(), lying); err == nil || d.count(t) != 0 {
		t.Errorf("an import of fewer rows than its source counts was kept: %v", err)
	}
}

type sourceFunc func(context.Context, oldsite.Sink) (oldsite.Counts, error)

func (f sourceFunc) Read(ctx context.Context, sink oldsite.Sink) (oldsite.Counts, error) {
	return f(ctx, sink)
}

func TestAnEmptiedDatabaseGivesTheSameResultAgain(t *testing.T) {
	d, c, first := imported(t)
	want := d.store.everything(t)
	// Life goes on, then the rehearsal copy is emptied for the final import.
	d.post(t, run(t, 3), "a")
	for _, table := range []string{"reports", "mac_sign_ins", "report_digests"} {
		if _, err := d.store.db.Exec(`DELETE FROM ` + table); err != nil {
			t.Fatal(err)
		}
	}
	second, err := d.store.Carry(context.Background(), dumped(t, c))
	if err != nil {
		t.Fatal(err)
	}
	got := d.store.everything(t)
	// The ids given since are never given again: only the sequences moved.
	keep := func(rows []string) (out []string) {
		for _, row := range rows {
			if !strings.HasPrefix(row, "sequence ") {
				out = append(out, row)
			}
		}
		return out
	}
	if !reflect.DeepEqual(keep(got), keep(want)) {
		t.Error("the second import's rows are not the first's")
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("the second import says %+v, the first %+v", second, first)
	}
}

func lookupIn(accounts map[string]int64, asked *[]string) Lookup {
	return func(_ context.Context, handle string) (int64, string, error) {
		*asked = append(*asked, handle)
		for name, id := range accounts {
			if strings.EqualFold(name, handle) {
				return id, name, nil
			}
		}
		return 0, "", ErrNoSuchAccount
	}
}

func TestTestersAndCarriedSignInsBecomePeople(t *testing.T) {
	c := oldSite(t)
	var asked []string
	people, notes, err := CarriedPeople(context.Background(), dumped(t, c), lookupIn(map[string]int64{"dhh": 2741, "RyanRHughes": 3003}, &asked))
	if err != nil {
		t.Fatal(err)
	}
	latest := time.Date(2026, 11, 2, 0, 0, 0, 600000, time.UTC)
	want := []CarriedPerson{
		{AccountID: 1001, Handle: "maralcbr", SignedIn: latest},
		{AccountID: 1002, Handle: "scottjones", SignedIn: time.Date(2026, 10, 29, 7, 8, 9, 101112000, time.UTC)},
		{AccountID: 2741, Handle: "dhh"},
		{AccountID: 3003, Handle: "RyanRHughes"},
		{AccountID: 4242, Handle: "demo-tester"},
		{AccountID: 5005, Handle: "outsider", SignedIn: time.Date(2026, 10, 1, 1, 1, 1, 10101000, time.UTC)},
	}
	if !reflect.DeepEqual(people, want) {
		t.Errorf("people:\n%+v\nwant\n%+v", people, want)
	}
	// Only the handles that never signed in are asked of GitHub.
	sort.Strings(asked)
	if !reflect.DeepEqual(asked, []string{"dhh", "gone-handle", "ryanrhughes"}) {
		t.Errorf("GitHub was asked for %v", asked)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "@gone-handle") {
		t.Errorf("notes: %v", notes)
	}
}

func TestWithGitHubOutOfReachNobodyIsMadeUp(t *testing.T) {
	down := func(context.Context, string) (int64, string, error) {
		return 0, "", errors.New("GitHub answered HTTP 503")
	}
	people, _, err := CarriedPeople(context.Background(), dumped(t, oldSite(t)), down)
	if err == nil || people != nil {
		t.Errorf("people %v, err %v", people, err)
	}
}

func TestAHandleThatSignedInBeforeItWasListedNeedsNoLookup(t *testing.T) {
	c := oldSite(t)
	c.Testers = append(c.Testers, oldsite.Tester{ID: 9, Login: "outsider", CreatedAt: c.Testers[0].CreatedAt, UpdatedAt: c.Testers[0].UpdatedAt})
	var asked []string
	people, _, err := CarriedPeople(context.Background(), dumped(t, c), lookupIn(map[string]int64{"dhh": 2741, "ryanrhughes": 3003, "outsider": 9999}, &asked))
	if err != nil {
		t.Fatal(err)
	}
	for _, handle := range asked {
		if handle == "outsider" {
			t.Error("GitHub was asked for a handle a Mac is signed in as")
		}
	}
	for _, p := range people {
		if p.AccountID == 9999 {
			t.Error("the handle's account came from GitHub, not from its sign-in")
		}
	}
}

func TestOneKnownMachineIDIsRecomputedFromItsPublicKey(t *testing.T) {
	d, _, _ := imported(t)
	rules := testRules(t)
	id, err := rules.MachineIDOf(oldsitetest.PublicKey("a") + "\n")
	if err != nil {
		t.Fatal(err)
	}
	reports, signIns, err := d.store.OfMachine(id)
	if err != nil || reports != 4 || signIns != 1 {
		t.Errorf("machine a: %d reports, %d sign-ins, %v", reports, signIns, err)
	}
	other, err := rules.MachineIDOf(oldsitetest.PublicKey("never-seen"))
	if err != nil {
		t.Fatal(err)
	}
	if reports, signIns, _ := d.store.OfMachine(other); reports+signIns != 0 {
		t.Error("a Mac the old site never saw has rows")
	}
	// Under another key the same Mac is a stranger.
	wrong, _ := NewRules(railsMachineIDKey(t, "another secret"))
	id, _ = wrong.MachineIDOf(oldsitetest.PublicKey("a"))
	if reports, signIns, _ := d.store.OfMachine(id); reports+signIns != 0 {
		t.Error("the wrong machine-id key finds the Mac")
	}
	for _, bad := range []string{"", "ssh-rsa AAAA", "not a key at all"} {
		if _, err := rules.MachineIDOf(bad); err == nil {
			t.Errorf("%q was read as a Machine public key", bad)
		}
	}
}

func TestAnOldDeletionLinkDeletesItsReportAndKeepsItsDigest(t *testing.T) {
	d, _, _ := imported(t)
	before := d.count(t)
	for _, link := range []string{
		"https://omarchy-m-testing.org/reports/" + oldsitetest.Outsider + "/deletion?token=" + oldsitetest.VerifiedToken,
		"https://omarchy-m-testing.org/reports/" + oldsitetest.Verified + "/deletion?token=wrong",
		"https://omarchy-m-testing.org/reports/" + oldsitetest.Verified + "/deletion",
		"https://omarchy-m-testing.org/reports/" + oldsitetest.Verified,
		"https://omarchy-m-testing.org/reports/nosuchreport000000000000/deletion?token=" + oldsitetest.VerifiedToken,
		"not a link",
	} {
		if err := d.store.DeleteByOldLink(link); err == nil || d.count(t) != before {
			t.Errorf("%s deleted something: %v", strings.ReplaceAll(link, oldsitetest.VerifiedToken, "…"), err)
		} else if strings.Contains(err.Error(), oldsitetest.VerifiedToken) {
			t.Errorf("the refusal repeats the link's secret: %v", err)
		}
	}
	for _, host := range []string{"https://omarchy-m-testing.org", "https://lab.omarchy-mac.org"} {
		d, _, _ := imported(t)
		if err := d.store.DeleteByOldLink(" " + host + "/reports/" + oldsitetest.Verified + "/deletion?token=" + oldsitetest.VerifiedToken + "\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.store.Report(oldsitetest.Verified); !errors.Is(err, ErrNoReport) {
			t.Errorf("the report is still there: %v", err)
		}
		if d.count(t) != before-1 {
			t.Error("the link deleted another report too")
		}
	}
}
