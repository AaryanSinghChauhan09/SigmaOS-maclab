package oldsite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// tables is a Source held in memory, and a Sink that keeps what it is given.
type tables struct {
	testers  []Tester
	bindings []TesterBinding
	reports  []Report
	says     *Counts
	fail     error
}

func (m *tables) Tester(row *Tester) error {
	m.testers = append(m.testers, *row)
	return nil
}

func (m *tables) TesterBinding(row *TesterBinding) error {
	m.bindings = append(m.bindings, *row)
	return nil
}

func (m *tables) Report(row *Report) error {
	m.reports = append(m.reports, *row)
	return nil
}

func (m *tables) Read(_ context.Context, sink Sink) (Counts, error) {
	for i := range m.testers {
		if err := sink.Tester(&m.testers[i]); err != nil {
			return Counts{}, err
		}
	}
	for i := range m.bindings {
		if err := sink.TesterBinding(&m.bindings[i]); err != nil {
			return Counts{}, err
		}
	}
	for i := range m.reports {
		if i == 1 && m.fail != nil {
			return Counts{}, m.fail
		}
		if err := sink.Report(&m.reports[i]); err != nil {
			return Counts{}, err
		}
	}
	if m.says != nil {
		return *m.says, nil
	}
	return Counts{Reports: int64(len(m.reports)), TesterBindings: int64(len(m.bindings)), Testers: int64(len(m.testers))}, nil
}

func stamp(t *testing.T, text string) Stamp {
	t.Helper()
	at, err := ParseStamp(text)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func text(s string) *string { return &s }

func sample(t *testing.T) *tables {
	hidden := stamp(t, "2026-10-02T08:00:00.000001")
	pinned := int64(1001)
	return &tables{
		testers: []Tester{
			{ID: 1, Login: "maralcbr", GitHubID: &pinned, CreatedAt: stamp(t, "2026-09-27T00:00:00.000000"), UpdatedAt: stamp(t, "2026-09-27T10:11:12.131415")},
			{ID: 4, Login: "dhh", CreatedAt: stamp(t, "2026-10-28T12:00:00.654321"), UpdatedAt: stamp(t, "2026-10-28T12:00:00.654321")},
		},
		bindings: []TesterBinding{
			{ID: 2, MachineID: "key:0123456789abcdef0123", GitHubLogin: "maralcbr", GitHubID: 1001, CreatedAt: stamp(t, "2026-09-27T10:11:12.000007"), UpdatedAt: stamp(t, "2026-10-30T23:59:59.999999")},
		},
		reports: []Report{
			{ID: 3, PublicID: "h7Qm2ZpX4cVb9NkT3sWd8RfY", DeletionTokenDigest: strings.Repeat("ab", 32), SchemaVersion: 1,
				Body:      `{"tool": {"version": "0.1.8"}, "note": "a <b> & \"c\" é   \\n", "checks": [1, 2.50]}`,
				CreatedAt: stamp(t, "2026-09-26T03:04:05.123456"), UpdatedAt: stamp(t, "2026-09-26T03:04:05.123456")},
			{ID: 9, PublicID: "aaaaaaaaaaaaaaaaaaaaaaaa", DeletionTokenDigest: strings.Repeat("cd", 32), SchemaVersion: 1, Body: `{}`,
				CreatedAt: stamp(t, "2026-10-01T00:00:00.000000"), UpdatedAt: stamp(t, "2026-10-02T08:00:00.000001"), HiddenAt: &hidden,
				MachineID: text("key:0123456789abcdef0123"), TesterLogin: text("maralcbr")},
		},
	}
}

var taken = time.Date(2026, 11, 15, 1, 2, 3, 0, time.UTC)

func written(t *testing.T, src Source) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old-site.dump")
	if _, err := WriteDump(context.Background(), path, src, taken); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestADumpGivesBackTheThreeTablesAsTheyWere(t *testing.T) {
	src := sample(t)
	path := filepath.Join(t.TempDir(), "old-site.dump")
	counts, err := WriteDump(context.Background(), path, src, taken)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Counts{Reports: 2, TesterBindings: 1, Testers: 2}); counts != want {
		t.Errorf("WriteDump counted %v, want %v", counts, want)
	}
	dump, err := OpenDump(path)
	if err != nil {
		t.Fatal(err)
	}
	if dump.Rows != counts || dump.Source != counts || !dump.TakenAt.Equal(taken) {
		t.Errorf("the dump says %v rows, %v at the source, taken %v", dump.Rows, dump.Source, dump.TakenAt)
	}
	var got tables
	read, err := dump.Read(context.Background(), &got)
	if err != nil {
		t.Fatal(err)
	}
	if read != counts {
		t.Errorf("Read counted %v", read)
	}
	if !reflect.DeepEqual(got.testers, src.testers) || !reflect.DeepEqual(got.bindings, src.bindings) || !reflect.DeepEqual(got.reports, src.reports) {
		t.Errorf("the rows read back differ:\n%+v\n%+v\n%+v", got.testers, got.bindings, got.reports)
	}
	if got.reports[0].Body != src.reports[0].Body {
		t.Errorf("a body changed: %q", got.reports[0].Body)
	}
}

func TestADumpIsOneFileOnlyItsAccountReads(t *testing.T) {
	path := written(t, sample(t))
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("the dump's mode is %v", st.Mode().Perm())
	}
	names, _ := os.ReadDir(filepath.Dir(path))
	if len(names) != 1 {
		t.Errorf("beside the dump: %v", names)
	}
}

func TestTimesInADumpAreToTheMicrosecond(t *testing.T) {
	data, err := os.ReadFile(written(t, sample(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"created_at":"2026-09-27T10:11:12.000007"`, `"updated_at":"2026-10-30T23:59:59.999999"`, `"hidden_at":"2026-10-02T08:00:00.000001"`, `"hidden_at":null`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("the dump lacks %s", want)
		}
	}
	for _, bad := range []string{"2026-09-26T03:04:05", "2026-09-26T03:04:05.1234567", "2026-09-26 03:04:05.123456", "2026-09-26T03:04:05.123456Z", ""} {
		if _, err := ParseStamp(bad); err == nil {
			t.Errorf("%q was read as a time", bad)
		}
	}
}

func TestADumpThatWasCutOrChangedIsRefused(t *testing.T) {
	whole, err := os.ReadFile(written(t, sample(t)))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(whole, []byte("\n"))
	changed := bytes.Replace(whole, []byte("maralcbr"), []byte("xaralcbr"), 1)
	swapped := bytes.Join([][]byte{lines[0], lines[2], lines[1], bytes.Join(lines[3:], nil)}, nil)
	for name, data := range map[string][]byte{
		"empty":             nil,
		"header only":       lines[0],
		"cut before end":    bytes.Join(lines[:len(lines)-2], nil),
		"cut mid line":      whole[:len(whole)-40],
		"no last newline":   whole[:len(whole)-1],
		"a byte changed":    changed,
		"a row dropped":     bytes.Join(append(append([][]byte{}, lines[:2]...), lines[3:]...), nil),
		"rows out of order": swapped,
		"more after end":    append(append([]byte{}, whole...), lines[1]...),
		"another file":      []byte("{\"dump\":\"something else\",\"version\":1}\n"),
		"a later version":   bytes.Replace(whole, []byte(`"version":1`), []byte(`"version":2`), 1),
	} {
		path := filepath.Join(t.TempDir(), "d")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenDump(path); err == nil {
			t.Errorf("%s: the dump was read", name)
		}
	}
}

func TestWritingADumpNeverReplacesOne(t *testing.T) {
	path := written(t, sample(t))
	before, _ := os.ReadFile(path)
	other := sample(t)
	other.reports = other.reports[:1]
	if _, err := WriteDump(context.Background(), path, other, taken); err == nil {
		t.Fatal("a second dump replaced the first")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("the first dump changed")
	}
}

func TestADumpThatWasCutLeavesNoFileAndCanBeStartedAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-site.dump")
	src := sample(t)
	src.fail = errors.New("the line dropped")
	if _, err := WriteDump(context.Background(), path, src, taken); err == nil {
		t.Fatal("a cut read made a dump")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a cut read left %s: %v", path, err)
	}
	// What a killed run leaves behind does not stop the next.
	if err := os.WriteFile(path+".part", []byte("half a dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	src.fail = nil
	if _, err := WriteDump(context.Background(), path, src, taken); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDump(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("the part file is still there")
	}
}

func TestADumpWhoseRowsAreNotTheSourcesCountIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-site.dump")
	src := sample(t)
	src.says = &Counts{Reports: 3, TesterBindings: 1, Testers: 2}
	_, err := WriteDump(context.Background(), path, src, taken)
	if err == nil || !strings.Contains(err.Error(), "3 reports") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the dump was kept")
	}
}

// taking is a source during whose read another run takes the dump's name.
type taking struct {
	*tables
	path string
}

func (s taking) Read(ctx context.Context, sink Sink) (Counts, error) {
	if err := os.WriteFile(s.path, []byte("another run's dump"), 0o600); err != nil {
		return Counts{}, err
	}
	return s.tables.Read(ctx, sink)
}

func TestADumpDoesNotTakeANameTakenWhileItWasWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-site.dump")
	if _, err := WriteDump(context.Background(), path, taking{sample(t), path}, taken); err == nil {
		t.Fatal("the dump took the name")
	}
	if got, _ := os.ReadFile(path); string(got) != "another run's dump" {
		t.Errorf("the other run's file was replaced: %q", got)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("the part file is still there")
	}
}
