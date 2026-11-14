package reports

import (
	"fmt"
	"math/big"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Numbers print as Ruby prints them: a score with a fraction in an export
// (Float#to_s), and a share of the best score to one decimal. The cases are
// Ruby's own answers (testdata/ruby-cases.json).
func TestNumbersPrintAsRubys(t *testing.T) {
	var cases struct {
		Floats [][2]string `json:"floats"`
		Shares [][3]string `json:"shares"`
	}
	decode(t, read(t, filepath.Join("testdata", "ruby-cases.json")), &cases)
	if len(cases.Floats) < 15 || len(cases.Shares) < 10 {
		t.Fatalf("the file holds %d floats and %d shares", len(cases.Floats), len(cases.Shares))
	}
	for _, c := range cases.Floats {
		f, err := strconv.ParseFloat(c[0], 64)
		if err != nil {
			t.Fatal(err)
		}
		if got := rubyFloat(f); got != c[0] {
			t.Errorf("%v prints as %q, Ruby prints %q", f, got, c[0])
		}
		// Between a ten-thousandth and 10^15, where every score lies, Ruby's JSON says the same.
		if f >= 1e-4 && f < 1e15 && c[1] != c[0] {
			t.Errorf("Ruby's JSON writes %v as %q, not as %q", f, c[1], c[0])
		}
		if f != 0 && string(JSON(f)) != c[0] {
			t.Errorf("JSON(%v) is %s", f, JSON(f))
		}
	}
	for _, c := range cases.Shares {
		a, _ := strconv.ParseFloat(c[0], 64)
		b, _ := strconv.ParseFloat(c[1], 64)
		if got := Share(a, b); got != c[2] {
			t.Errorf("%s of %s is %q, Ruby says %q", c[0], c[1], got, c[2])
		}
	}
	if Share(big.NewInt(5), 0) != "0" {
		t.Error("a share of nothing")
	}
	for value, want := range map[any]string{big.NewInt(2987): "2,987", big.NewInt(691): "691", 3136.5: "3,136.5", 1792.0: "1,792", big.NewInt(1234567): "1,234,567",
		Float(60.25): "60.3", big.NewInt(0): "0", nil: "–"} {
		if got := ScoreWords(value); got != want {
			t.Errorf("ScoreWords(%v) = %q, want %q", value, got, want)
		}
	}
}

func TestAnExportIsWrittenInTheOrderItIsPutAndEscapedAsRailsEscapes(t *testing.T) {
	export := obj("z", 1, "a", []any{"<b>&\u2028", nil, true, big.NewInt(7), 2.5, 3.0}, "tally", Tally{{"works", 2}, {"fails", 1}}, "none", Tally(nil), "nothing", (*Object)(nil), "names", []string(nil))
	if got, want := string(JSON(export)), `{"z":1,"a":["\u003cb\u003e\u0026\u2028",null,true,7,2.5,3.0],"tally":{"works":2,"fails":1},"none":{},"nothing":null,"names":[]}`; got != want {
		t.Errorf("JSON:\n  got  %s\n  want %s", got, want)
	}
	if got := string(JSON(obj("a", "", "b", nil, "c", "kept", "d", (*Object)(nil), "e", 0).compact())); got != `{"c":"kept","e":0}` {
		t.Errorf("compact: %s", got)
	}
}

func TestACSVQuotesEveryFieldAndDefusesWhatASpreadsheetWouldRun(t *testing.T) {
	got := string(csv([]string{"a", "b"}, [][]any{{"=1+1", "+1"}, {"-1", "@sum"}, {"\tx", "\ry"}, {`say "hi"`, nil}, {big.NewInt(42), true}, {"plain", 0}}))
	want := "\"a\",\"b\"\r\n\"'=1+1\",\"'+1\"\r\n\"'-1\",\"'@sum\"\r\n\"'\tx\",\"'\ry\"\r\n\"say \"\"hi\"\"\",\"\"\r\n\"42\",\"true\"\r\n\"plain\",\"0\"\r\n"
	if got != want {
		t.Errorf("the CSV:\n  got  %q\n  want %q", got, want)
	}
}

// The old site asked Postgres four things of a report's JSON. They run on
// SQLite as they were written, and find what the pages count in memory.
func TestTheFourJSONQueriesRunUnchangedOnSQLite(t *testing.T) {
	for _, query := range []string{whereNamesASet, selectSetName, whereOnSet, whereOnBoardAndStack} {
		if !strings.Contains(query, "->") {
			t.Fatalf("%q is not a query on a report's JSON", query)
		}
	}
	same := func(what string, got, want []string) {
		t.Helper()
		if len(got) == 0 && len(want) == 0 {
			return
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n  SQLite    %v\n  in memory %v", what, got, want)
		}
	}
	ids := func(runs []*Run) []string {
		var out []string
		for _, run := range runs {
			out = append(out, run.PublicID)
		}
		return out
	}
	list, _ := seededCorpus(t)
	s, _ := openStore(t)
	hidden := map[string]bool{list[3].PublicID: true, list[len(list)-4].PublicID: true}
	var shown []*Report
	for _, r := range list {
		var hiddenAt any
		if hidden[r.PublicID] {
			hiddenAt = time.Now().UnixMicro()
		} else {
			shown = append(shown, r)
		}
		if _, err := s.db.Exec(`INSERT INTO reports (public_id, deletion_token_digest, schema_version, body, created_at, updated_at, hidden_at, machine_id, verified) VALUES (?, 'digest', 1, ?, ?, ?, ?, ?, ?)`,
			r.PublicID, string(r.Body), r.CreatedAt.UnixMicro(), r.CreatedAt.UnixMicro(), hiddenAt, r.MachineID, r.Verified); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.Published(testCatalogue(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Runs()) != len(shown) || len(p.CandidateSets()) < 3 {
		t.Fatalf("%d published runs, %d candidate sets", len(p.Runs()), len(p.CandidateSets()))
	}
	var naming []*Run
	names := []string{}
	boards := map[[2]string][]*Run{}
	for _, run := range p.Runs() {
		if run.CandidateSet() != "" {
			naming = append(naming, run)
		}
		on := [2]string{run.Board(), run.Stack()}
		if _, seen := boards[on]; !seen {
			boards[on] = nil
		}
		if run.Verified {
			boards[on] = append(boards[on], run)
		}
	}
	got, err := s.NamingASet()
	if err != nil {
		t.Fatal(err)
	}
	same("the runs that name a set", got, ids(naming))
	for _, set := range p.CandidateSets() {
		names = append(names, set.Name)
		got, err := s.OnCandidateSet(set.Name)
		if err != nil {
			t.Fatal(err)
		}
		same("the runs on "+set.Name, got, ids(set.Runs))
	}
	if got, err := s.OnCandidateSet("nothing-names-this"); err != nil || len(got) != 0 {
		t.Errorf("a set nothing names: %v, %v", got, err)
	}
	got, err = s.CandidateSetNames()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(names)
	same("the sets named", got, names)
	verified := 0
	for on, runs := range boards {
		got, err := s.VerifiedOn(on[0], on[1])
		if err != nil {
			t.Fatal(err)
		}
		same(fmt.Sprintf("the Verified runs on %s, %s", on[0], on[1]), got, ids(runs))
		verified += len(got)
	}
	if verified < 5 {
		t.Errorf("only %d Verified runs were found by board and stack", verified)
	}
}

func TestAnIssuesBodyIsCutToFitALinkWithItsCodeFenceClosed(t *testing.T) {
	doc := golden(t, "m2-max-image2")
	var evidence []any
	for i := 0; i < 60; i++ {
		evidence = append(evidence, fmt.Sprintf("line %d: ``` [probe] (/sys/x) %s", i, strings.Repeat(":/[", 150)))
	}
	check(t, doc, "setup.first-boot-hardware").set("evidence", evidence)
	shelf := newShelf(t)
	shelf.add(doc, "a")
	p := shelf.published()
	run := p.Runs()[0]
	issues := p.Issues("lab.example", func(run *Run) string { return "https://lab.example/reports/" + run.PublicID })
	for _, c := range run.Checks() {
		if !WorthAnIssue(c) {
			continue
		}
		draft := issues.ForCheck(run, c)
		if len(draft.URL()) > IssueMaxURL || len([]rune(draft.Body)) > IssueMaxBody+100 || strings.Count(draft.Body, "```")%2 != 0 {
			t.Errorf("%s: a link of %d bytes, a body of %d characters, %d fences", c.ID, len(draft.URL()), len([]rune(draft.Body)), strings.Count(draft.Body, "```"))
		}
		if c.ID == "setup.first-boot-hardware" {
			if !strings.Contains(draft.Body, "cut short") || !strings.Contains(draft.Body, "Report: https://lab.example/reports/"+run.PublicID) ||
				strings.Contains(draft.Body, "line 20:") || strings.Contains(draft.Body, "` ``` [") {
				t.Errorf("the long check's body:\n%s", draft.Body[:min(len(draft.Body), 500)])
			}
			if draft.Repo != "omacom/omarchy-mac" {
				t.Errorf("the repository: %s", draft.Repo)
			}
		} else if strings.Contains(draft.Body, "cut short") || !strings.HasSuffix(draft.Body, fmt.Sprintf("From lab.example, feature catalogue v%d.", p.Catalogue().Version())) {
			t.Errorf("%s: a short body was cut, or lacks its last line", c.ID)
		}
	}
}
