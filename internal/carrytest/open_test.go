package carrytest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/oldlabtest"
	"github.com/omacom/omarchy-maclab/internal/server"
)

func brisbaneDay(t time.Time) string {
	zone, err := time.LoadLocation("Australia/Brisbane")
	if err != nil {
		panic(err)
	}
	return t.In(zone).Format(time.DateOnly)
}

// opening is a mini whose labd runs in Preview with its ops socket, as on
// the day: a machine-id key, an OAuth app, marked people of the M team and
// the mark the day's import leaves.
func opening(t *testing.T, marked int) (*mini, *server.Server) {
	t.Helper()
	m := newMini(t, oldlabtest.OnOmarchyGPU)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := server.New(ctx, server.Config{DataDir: m.path("var/lab/data"), Preview: true, MachineIDKey: machineIDKey(), Release: "v0.2.0-rc.9",
		GitHub: &server.GitHubApp{ClientID: "id", ClientSecret: "secret", WebURL: "http://127.0.0.1:1", APIURL: "http://127.0.0.1:1"}}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := server.ListenOps(m.path("var/lab/run/ops.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ops := s.OpsServer()
	go ops.Serve(ln)
	t.Cleanup(func() { ops.Close() })
	db, err := sql.Open("sqlite", filepath.Join(m.path("var/lab/data"), "labd.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < marked; i++ {
		if _, err := db.Exec(`INSERT INTO people (account_id, handle, m_team, added) VALUES (?, ?, 1, 1)`, 7000+i, "member"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	write(t, m.path("var/lab/import/imported"), brisbaneDay(time.Now())+"\n", 0o600)
	return m, s
}

func asked(t *testing.T, s *server.Server) string {
	t.Helper()
	w := &answer{h: http.Header{}}
	r, _ := http.NewRequest("GET", "http://labd/stage", nil)
	s.OpsHandler().ServeHTTP(w, r)
	return strings.TrimSpace(w.b.String())
}

type answer struct {
	h http.Header
	b bytes.Buffer
}

func (a *answer) Header() http.Header         { return a.h }
func (a *answer) Write(b []byte) (int, error) { return a.b.Write(b) }
func (a *answer) WriteHeader(int)             {}

// The opening on the mini: every "must be true first" that is false stops
// it with the reason and the lab stays in Preview; then one run opens the
// lab, a second changes nothing, and what is only for a lab in Preview is
// refused from then on.
func TestTheOpeningOnTheMiniRefusesUntilEverythingIsTrueThenOpensOnce(t *testing.T) {
	m, s := opening(t, 14)
	mark := m.path("var/lab/import/imported")
	today := brisbaneDay(time.Now())
	closed := func(what string) {
		t.Helper()
		if got := asked(t, s); got != "preview" {
			t.Fatalf("%s: labd says %q, want preview", what, got)
		}
	}

	if out := m.ok("open", "stage"); strings.TrimSpace(out) != "preview" {
		t.Errorf("open stage in Preview: %q", out)
	}
	if out := m.ok("open", "--dry-run", "--m-team", "14"); !strings.Contains(out, "POST /open?m-team=14") || !strings.Contains(out, "nothing was changed") {
		t.Errorf("the dry run: %s", out)
	}
	closed("after the dry run")
	for _, args := range [][]string{nil, {"--m-team"}, {"--m-team", "all"}, {"--m-team", "14", "--now"}, {"--m-team", "14", "--imported", "yesterday"}, {"stage", "--m-team", "14"}} {
		if out, code := m.run("", "open", args...); code != 2 {
			t.Errorf("open %q: exit %d, %s", args, code, out)
		}
	}
	closed("after bad arguments")

	// No import has passed today.
	os.Remove(mark)
	if out := m.refused("open", "--m-team", "14"); !strings.Contains(out, "the day's import has not run") || !strings.Contains(out, "Nothing was changed") {
		t.Errorf("with no import: %s", out)
	}
	write(t, mark, "2026-01-02\n", 0o600)
	if out := m.refused("open", "--m-team", "14"); !strings.Contains(out, "ran on 2026-01-02, not "+today) {
		t.Errorf("with a rehearsal's import: %s", out)
	}
	closed("after an import of another day")
	// The mark as a link the lab account planted.
	os.Remove(mark)
	if err := os.Symlink(m.path("var/lab/secrets/admins"), mark); err != nil {
		t.Fatal(err)
	}
	if out := m.refused("open", "--m-team", "14"); !strings.Contains(out, "symbolic link") {
		t.Errorf("a planted link for the import's mark: %s", out)
	}
	os.Remove(mark)
	write(t, mark, today+"\n", 0o600)

	// labd counts the M team itself.
	if out := m.refused("open", "--m-team", "15"); !strings.Contains(out, "14 people are marked M team, not 15") || !strings.Contains(out, "Nothing was changed") {
		t.Errorf("with the wrong count: %s", out)
	}
	closed("after the wrong count")
	// labd not running, and its ops socket gone.
	os.Remove(m.state + "/loaded." + labdLabel)
	if out := m.refused("open", "--m-team", "14"); !strings.Contains(out, "labd is not running") {
		t.Errorf("with labd stopped: %s", out)
	}
	write(t, m.state+"/loaded."+labdLabel, "", 0o644)
	sock := m.path("var/lab/run/ops.sock")
	os.Rename(sock, sock+".away")
	if out := m.refused("open", "--m-team", "14"); !strings.Contains(out, "ops socket is not at") {
		t.Errorf("with no ops socket: %s", out)
	}
	os.Rename(sock+".away", sock)
	closed("after every refusal")

	out := m.ok("open", "--m-team", "14")
	live := asked(t, s)
	if !strings.HasPrefix(live, "live 20") || !strings.Contains(out, "the lab is open: "+live) || !strings.Contains(out, "point of no return") {
		t.Fatalf("the opening: labd says %q; the script said %s", live, out)
	}
	if s.Preview() {
		t.Fatal("labd is still in Preview")
	}
	if got := strings.TrimSpace(m.ok("open", "stage")); got != live {
		t.Errorf("open stage after the opening: %q, want %q", got, live)
	}
	if out := m.ok("open", "--m-team", "14"); !strings.Contains(out, "open already ("+live+")") {
		t.Errorf("the opening a second time: %s", out)
	}
	// An import of another day's mark no longer matters, and nothing asks labd again.
	if out := m.ok("open", "--m-team", "3", "--imported", "2020-01-01"); !strings.Contains(out, "open already") {
		t.Errorf("the opening a third time: %s", out)
	}

	// The LaunchDaemon file still says --preview, and what is for a lab in
	// Preview is refused all the same: the import would empty the open
	// lab's reports.
	if !strings.Contains(read(t, m.path("Library/LaunchDaemons/"+labdLabel+".plist")), "--preview") {
		t.Fatal("the LaunchDaemon file lost --preview: the test proves nothing")
	}
	reports := everyFile(t, m.path("var/lab/data"))["reports.db"]
	os.Remove(m.log)
	public := filepath.Join(m.base, "home", "m1-pro.pub")
	write(t, public, "ssh-ed25519 AAAA\n", 0o644)
	for _, args := range [][]string{{"run", "--public-key", public}, {"run", "--public-key", public, "--dumped", "/var/lab/import/x.dump"}, {"dump", "--dump", m.path("var/lab/import/x.dump")}, {"key"}} {
		out, code := m.run(hex.EncodeToString(machineIDKey())+"\n", "rehearsal-import", args...)
		if code == 0 || !strings.Contains(out, "the lab is open ("+live+")") {
			t.Errorf("rehearsal-import %q on an open lab: exit %d, %s", args, code, out)
		}
	}
	if strings.Contains(m.calls(), "launchctl bootout") || everyFile(t, m.path("var/lab/data"))["reports.db"] != reports {
		t.Errorf("an open lab's labd was stopped or its reports.db touched:\n%s", m.calls())
	}
}

// A Deployed labd too old to know the opening is not taken for a lab in
// Preview.
func TestALabdThatCannotSayItsStageIsNeverTakenForPreview(t *testing.T) {
	m, s := opening(t, 14)
	old := filepath.Join(m.base, "old-labd")
	write(t, old, "#!/bin/sh\necho 'labd: unknown command' >&2\nexit 2\n", 0o755)
	link := m.path("usr/local/lab/builds/v0.2.0-rc.9/bin/labd")
	os.Remove(link)
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	if out := m.refused("open", "--m-team", "14"); !strings.Contains(out, "could not say whether the lab has opened") {
		t.Errorf("the opening: %s", out)
	}
	if out := m.refused("rehearsal-import", "key"); !strings.Contains(out, "nothing is emptied on a guess") {
		t.Errorf("rehearsal-import key: %s", out)
	}
	if got := asked(t, s); got != "preview" {
		t.Errorf("labd says %q", got)
	}
}

// The day's two halves of the import: the final dump alone, never written
// over, and the import that reads a dump which must be there. A run that
// passed its three checks leaves its day for the opening to read.
func TestTheDaysDumpAndImportAreTwoStepsAndAPassLeavesItsDay(t *testing.T) {
	m, dump, public := rehearsal(t)
	mark := m.path("var/lab/import/imported")
	if out, code := m.run(hex.EncodeToString(machineIDKey())+"\n", "rehearsal-import", "key"); code != 0 {
		t.Fatalf("the key: exit %d, %s", code, out)
	}
	missing := m.path("var/lab/import/the-day.dump")
	os.Remove(m.log)
	if out := m.refused("rehearsal-import", "run", "--public-key", public, "--dumped", missing); !strings.Contains(out, "the day's final dump comes first") || !strings.Contains(out, "Nothing was changed") {
		t.Errorf("the import with no dump: %s", out)
	}
	if strings.Contains(m.calls(), "launchctl bootout") || strings.Contains(m.calls(), "import dump") || exists(mark) {
		t.Errorf("a refused import stopped labd, read the old site or left a mark:\n%s", m.calls())
	}
	if out := m.refused("rehearsal-import", "dump", "--dump", dump); !strings.Contains(out, "is there already") {
		t.Errorf("a dump over one that is there: %s", out)
	}
	if out := m.ok("rehearsal-import", "dump", "--dry-run", "--dump", missing); !strings.Contains(out, "would have labd import dump ask") || exists(missing) {
		t.Errorf("the dump's dry run: %s", out)
	}
	for _, args := range [][]string{{"dump"}, {"dump", "--dump", "relative.dump"}} {
		if out, code := m.run("", "rehearsal-import", args...); code != 2 {
			t.Errorf("rehearsal-import %q: exit %d, %s", args, code, out)
		}
	}
	if out, code := m.run(oldLink+"\n", "rehearsal-import", "run", "--public-key", public, "--dumped", dump); code != 0 || !strings.Contains(out, "the import and its three checks passed") {
		t.Fatalf("the import from the dump that is there: exit %d, %s", code, out)
	}
	if got := strings.TrimSpace(read(t, mark)); got != brisbaneDay(time.Now()) {
		t.Errorf("the mark a passed import leaves: %q", got)
	}
	if st, err := os.Stat(mark); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the mark's file: %v", err)
	}
}
