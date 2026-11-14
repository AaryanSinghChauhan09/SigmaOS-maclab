package reports

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/sqlitedb"
)

func publishedIDs(t *testing.T, s *Store) string {
	t.Helper()
	p, err := s.Published(testCatalogue(t))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, run := range p.Runs() {
		ids = append(ids, run.PublicID)
	}
	return strings.Join(ids, " ")
}

// The public pages ask for the published reports on every request. They are
// read from reports.db, bodies and all, once, and again only after
// something changed the file: a report stored, hidden, shown or deleted, by
// labd or by anything else that writes it.
func TestThePublishedReportsAreReadAgainOnlyAfterAChange(t *testing.T) {
	s, path := openStore(t)
	at := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	s.addReport(t, "a", `{"machine":{"model":"one"}}`, at)
	s.addReport(t, "b", `{}`, at.Add(time.Minute))
	read := func(want string, loads int, after string) {
		t.Helper()
		for range 3 {
			if got := publishedIDs(t, s); got != want {
				t.Fatalf("%s the published reports are %q, want %q", after, got, want)
			}
		}
		if s.kept.loads != loads {
			t.Fatalf("%s and three requests the reports were read %d times in all, want %d", after, s.kept.loads, loads)
		}
	}
	read("a b", 1, "at first")

	s.addReport(t, "c", `{}`, at.Add(2*time.Minute))
	read("a b c", 2, "after one was stored")
	if _, err := s.db.Exec(`UPDATE reports SET hidden_at = ? WHERE public_id = 'b'`, at.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	read("a c", 3, "after one was hidden")
	if _, err := s.db.Exec(`UPDATE reports SET hidden_at = NULL WHERE public_id = 'b'`); err != nil {
		t.Fatal(err)
	}
	read("a b c", 4, "after it was shown again")
	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	read("b c", 5, "after one was deleted")

	other, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(insertReport, "d", `{}`, at.Add(3*time.Minute).UnixMicro(), at.UnixMicro(), nil); err != nil {
		t.Fatal(err)
	}
	read("b c d", 6, "after another program stored one")
}

// What is kept is the reports as read. Each request counts them for itself,
// so two requests at once share nothing they write to.
func TestEachRequestCountsThePublishedReportsForItself(t *testing.T) {
	s, _ := openStore(t)
	s.addReport(t, "a", string(encode(t, golden(t, "m2-max-image2"))), time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC))
	cat := testCatalogue(t)
	one, err := s.Published(cat)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := s.Published(cat)
	if one == two || one.Runs()[0] == two.Runs()[0] {
		t.Fatal("two requests were given the same counting")
	}
	if one.Runs()[0].Report != two.Runs()[0].Report {
		t.Error("two requests were given two copies of the report")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.Published(cat)
			if err != nil || len(p.Runs()) != 1 || len(p.Runs()[0].Checks()) == 0 {
				t.Errorf("a request beside others: %v", err)
			}
		}()
	}
	wg.Wait()
	if s.kept.loads != 1 {
		t.Errorf("the reports were read %d times", s.kept.loads)
	}
}
