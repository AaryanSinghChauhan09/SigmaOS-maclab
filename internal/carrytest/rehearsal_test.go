package carrytest

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/pbkdf2"

	"github.com/omacom/omarchy-maclab/internal/githubtest"
	"github.com/omacom/omarchy-maclab/internal/oldlabtest"
	"github.com/omacom/omarchy-maclab/internal/oldsite/oldsitetest"
)

// The old site's machine-id key, as the old site derives it, from a secret
// made up here.
func machineIDKey() []byte {
	return pbkdf2.Key([]byte("the old site's secret, made up"), []byte("report machine id from key"), 1000, 64, sha256.New)
}

const oldLink = "https://omarchy-m-testing.org/reports/" + oldsitetest.Verified + "/deletion?token=" + oldsitetest.VerifiedToken

// rehearsal is a mini with a stand-in for GitHub and a dump of a made-up
// old site where the lab account reads it.
func rehearsal(t *testing.T) (m *mini, dump, public string) {
	t.Helper()
	m = newMini(t, oldlabtest.OnOmarchyGPU)
	gh := githubtest.New("id", "secret")
	gh.AddUser(2741, "dhh")
	gh.AddUser(3003, "RyanRHughes")
	fake := httptest.NewServer(gh)
	t.Cleanup(fake.Close)
	m.env = append(m.env, "LAB_GITHUB_URL="+fake.URL, "STUB_LABD="+labd(t), "STUB_DATA="+m.path("var/lab/data"))
	made := oldsitetest.Made(t, machineIDKey()).Dump(t)
	dump = m.path("var/lab/import/old-site.dump")
	write(t, dump, read(t, made), 0o600)
	public = filepath.Join(m.base, "home", "m1-pro.pub")
	write(t, public, oldsitetest.PublicKey("a")+"\n", 0o644)
	return m, dump, public
}

// The machine-id key is asked for, never an argument and never printed, and
// only 128 hex digits become the file.
func TestTheMachineIDKeyIsTypedAtAPromptAndKeptForTheLabAccountAlone(t *testing.T) {
	m, _, _ := rehearsal(t)
	file := m.path("var/lab/secrets/machine-id-key")
	key := hex.EncodeToString(machineIDKey())

	if out, code := m.run(key+"\n", "rehearsal-import", "key", "--dry-run"); code != 0 || !strings.Contains(out, "would ask") || exists(file) {
		t.Fatalf("the dry run: exit %d, %s", code, out)
	}
	for _, typed := range []string{"", "\n", "not a key\n", key[:126] + "\n", key + "00\n", strings.Replace(key, key[:1], "g", 1) + "\n"} {
		if out, code := m.run(typed, "rehearsal-import", "key"); code == 0 || exists(file) || !strings.Contains(out, "Nothing was changed") {
			t.Errorf("typed %d characters that are no key: exit %d, %s", len(typed), code, out)
		}
	}
	if out, code := m.run("", "rehearsal-import", "key", key); code != 2 || exists(file) || strings.Contains(out, key) {
		t.Errorf("the key as an argument: exit %d, %s", code, out)
	}
	os.Remove(m.log)
	out, code := m.run(key+"\n", "rehearsal-import", "key")
	if code != 0 || !strings.Contains(out, "off-machine copy") {
		t.Fatalf("exit %d, %s", code, out)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 || read(t, file) != key+"\n" {
		t.Errorf("the key's file: %v", err)
	}
	if strings.Contains(out+m.calls(), key) {
		t.Error("the key was printed or passed as an argument")
	}
	if !strings.Contains(m.calls(), "launchctl kickstart -k system/"+labdLabel) {
		t.Errorf("labd was not restarted to read the key:\n%s", m.calls())
	}
	os.Remove(m.log)
	if out, code := m.run(key+"\n", "rehearsal-import", "key"); code != 0 || !strings.Contains(out, "in place already") || strings.Contains(m.calls(), "kickstart") {
		t.Errorf("the same key again: exit %d, %s", code, out)
	}

	// An open lab keeps the key it has.
	plist := m.path("Library/LaunchDaemons/" + labdLabel + ".plist")
	write(t, plist, strings.Replace(read(t, plist), "    <string>--preview</string>\n", "", 1), 0o644)
	other := strings.Repeat("ab", 64)
	if out, code := m.run(other+"\n", "rehearsal-import", "key"); code == 0 || !strings.Contains(out, "the lab is open") || read(t, file) != key+"\n" {
		t.Errorf("another key for an open lab: exit %d, %s", code, out)
	}
}

// The rehearsal: reports.db emptied, filled from the dump, the three checks
// passed, and lines for docs/acceptance that hold counts only. It can be
// run again.
func TestTheRehearsalImportFillsAnEmptiedReportsDBAndPassesItsThreeChecks(t *testing.T) {
	m, dump, public := rehearsal(t)
	key := hex.EncodeToString(machineIDKey())
	reports := m.path("var/lab/data/reports.db")

	if out, code := m.run(oldLink+"\n", "rehearsal-import", "run", "--public-key", public, "--dump", dump); code == 0 || !strings.Contains(out, "rehearsal-import key") {
		t.Fatalf("with no machine-id key: exit %d, %s", code, out)
	}
	if strings.Contains(m.calls(), "launchctl bootout") {
		t.Fatalf("a refused run stopped a daemon:\n%s", m.calls())
	}
	if out, code := m.run(key+"\n", "rehearsal-import", "key"); code != 0 {
		t.Fatalf("the key: exit %d, %s", code, out)
	}
	if out, code := m.run("", "rehearsal-import", "run", "--dry-run", "--public-key", public, "--dump", dump); code != 0 || !strings.Contains(out, "the old site is not read") || !strings.Contains(out, "would stop labd") {
		t.Fatalf("the dry run: exit %d, %s", code, out)
	}
	if out, code := m.run("", "rehearsal-import", "run", "--dry-run", "--public-key", public); code != 0 || !strings.Contains(out, "ask for the address of the old site's Postgres") {
		t.Fatalf("the dry run with no dump: exit %d, %s", code, out)
	}
	if n := count(t, reports, `SELECT count(*) FROM reports`); n != 0 {
		t.Fatalf("a dry run imported %d reports", n)
	}

	for round := 1; round <= 2; round++ {
		os.Remove(m.log)
		out, code := m.run(oldLink+"\n", "rehearsal-import", "run", "--public-key", public, "--dump", dump)
		if code != 0 {
			t.Fatalf("round %d: exit %d\n%s", round, code, out)
		}
		for _, line := range []string{"The row counts match.", "That Mac's machine id, recomputed from its public key, is on ", "The old site's link deleted its report on the lab", "the import and its three checks passed"} {
			if !strings.Contains(out, line) {
				t.Errorf("round %d did not say %q:\n%s", round, line, out)
			}
		}
		// 20 reports in the dump, and the check deleted one.
		if n := count(t, reports, `SELECT count(*) FROM reports`); n != 19 {
			t.Errorf("round %d: reports.db holds %d reports, want 19", round, n)
		}
		_, lines, ok := strings.Cut(out, "---- rehearsal import, ")
		if !ok || !strings.Contains(lines, "Brisbane") || !strings.Contains(lines, "reports: 20 at the old site, 20 in the dump, 20 in reports.db") {
			t.Fatalf("round %d: no lines for docs/acceptance:\n%s", round, out)
		}
		for _, never := range []string{oldsitetest.Verified, oldsitetest.Hidden, oldsitetest.Twice, oldsitetest.VerifiedToken, key, dump, "demo-tester"} {
			if strings.Contains(lines, never) {
				t.Errorf("round %d: the lines for docs/acceptance name %q:\n%s", round, never, lines)
			}
		}
		if strings.Contains(m.calls(), key) || strings.Contains(m.calls(), oldsitetest.VerifiedToken) {
			t.Errorf("round %d: a secret was passed as an argument", round)
		}
		calls := m.calls()
		stop, moved, start, load := strings.Index(calls, "launchctl bootout system/"+labdLabel), strings.Index(calls, "as-lab mv "+reports+" "), strings.Index(calls, "launchctl bootstrap system"), strings.Index(calls, " import load ")
		if stop < 0 || moved < stop || start < moved || load < start {
			t.Errorf("round %d: reports.db was not emptied with labd stopped, before the import:\n%s", round, calls)
		}
		if !m.loaded(labdLabel) || !m.loaded(litestreamLabel) {
			t.Errorf("round %d: labd and Litestream are not running again", round)
		}
		// What reports.db held before is kept, once.
		kept := m.path("var/lab/import/reports-before/reports.db")
		if want := map[int]int{1: 0, 2: 19}[round]; count(t, kept, `SELECT count(*) FROM reports`) != want {
			t.Errorf("round %d: the copy kept aside does not hold the %d reports of before", round, want)
		}
	}

	// An open lab's reports are never emptied.
	plist := m.path("Library/LaunchDaemons/" + labdLabel + ".plist")
	write(t, plist, strings.Replace(read(t, plist), "    <string>--preview</string>\n", "", 1), 0o644)
	os.Remove(m.log)
	if out, code := m.run(oldLink+"\n", "rehearsal-import", "run", "--public-key", public, "--dump", dump); code == 0 || !strings.Contains(out, "the lab is open") {
		t.Errorf("a rehearsal on an open lab: exit %d, %s", code, out)
	}
	if n := count(t, reports, `SELECT count(*) FROM reports`); n != 19 || strings.Contains(m.calls(), "bootout") {
		t.Errorf("an open lab's reports.db was touched: %d reports\n%s", n, m.calls())
	}
}

// A check that fails stops the run there, with a status that says so.
func TestTheRehearsalStopsAtTheFirstCheckThatFails(t *testing.T) {
	m, dump, public := rehearsal(t)
	if out, code := m.run(hex.EncodeToString(machineIDKey())+"\n", "rehearsal-import", "key"); code != 0 {
		t.Fatalf("the key: exit %d, %s", code, out)
	}
	wrong := "https://omarchy-m-testing.org/reports/" + oldsitetest.Verified + "/deletion?token=not-the-token"
	out, code := m.run(wrong+"\n", "rehearsal-import", "run", "--public-key", public, "--dump", dump)
	if code == 0 || strings.Contains(out, "three checks passed") || strings.Contains(out, "---- rehearsal import") {
		t.Errorf("with a link that deletes nothing: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "The row counts match.") {
		t.Errorf("the checks before it did not run:\n%s", out)
	}
	other := filepath.Join(m.base, "home", "another.pub")
	write(t, other, oldsitetest.PublicKey("never seen")+"\n", 0o644)
	out, code = m.run(oldLink+"\n", "rehearsal-import", "run", "--public-key", other, "--dump", dump)
	if code == 0 || strings.Contains(out, "deleted its report") {
		t.Errorf("with a Machine public key of no report: exit %d\n%s", code, out)
	}
}

// openingAsLab plays the lab account and, as the import writes the Machine
// public key down, opens the lab from a second terminal.
const openingAsLab = `#!/bin/bash
echo "as-lab $*" >> "$STUB_LOG"
if [[ "$*" == *machine.pub* && ! -f $STUB_STATE/opened ]]; then
  : > "$STUB_STATE/opened"
  curl -sS --unix-socket "$LAB_SYSROOT/var/lab/run/ops.sock" -X POST "http://labd/open?m-team=14" >> "$STUB_LOG" 2>&1
fi
exec "$@"
`

// An opening between the import's first check and its stop of labd: the
// import asks again, and the open lab's reports stay where they are.
func TestAnOpeningDuringTheImportLeavesTheReports(t *testing.T) {
	m, dump, public := rehearsal(t)
	if out, code := m.run(hex.EncodeToString(machineIDKey())+"\n", "rehearsal-import", "key"); code != 0 {
		t.Fatalf("the key: exit %d, %s", code, out)
	}
	s := openingOn(t, m, 14)
	stub := filepath.Join(m.base, "opening-as-lab")
	write(t, stub, openingAsLab, 0o755)
	for i, e := range m.env {
		if strings.HasPrefix(e, "LAB_AS_LAB=") {
			m.env[i] = "LAB_AS_LAB=" + stub
		}
	}
	reports := everyFile(t, m.path("var/lab/data"))["reports.db"]
	os.Remove(m.log)
	out, code := m.run(oldLink+"\n", "rehearsal-import", "run", "--public-key", public, "--dumped", dump)
	if _, live := s.Live(); !live {
		t.Fatalf("the stand-in did not open the lab: %s", m.calls())
	}
	if code == 0 || !strings.Contains(out, "the lab is open (live ") {
		t.Errorf("the import on a lab opened meanwhile: exit %d, %s", code, out)
	}
	if strings.Contains(m.calls(), "launchctl bootout") || everyFile(t, m.path("var/lab/data"))["reports.db"] != reports || exists(m.path("var/lab/import/reports-before/reports.db")) {
		t.Errorf("the open lab's labd was stopped or its reports.db moved:\n%s", m.calls())
	}
}

// A run that empties the reports and then fails leaves no pass from earlier
// the same day for the opening to read.
func TestAFailedImportTakesAwayTheDaysEarlierPass(t *testing.T) {
	m, dump, public := rehearsal(t)
	mark := m.path("var/lab/import/imported")
	if out, code := m.run(hex.EncodeToString(machineIDKey())+"\n", "rehearsal-import", "key"); code != 0 {
		t.Fatalf("the key: exit %d, %s", code, out)
	}
	if out, code := m.run(oldLink+"\n", "rehearsal-import", "run", "--public-key", public, "--dumped", dump); code != 0 || !exists(mark) {
		t.Fatalf("the first import: exit %d, %s", code, out)
	}
	wrong := "https://omarchy-m-testing.org/reports/" + strings.Repeat("0", 32) + "/deletion?token=not-the-one"
	if out, code := m.run(wrong+"\n", "rehearsal-import", "run", "--public-key", public, "--dumped", dump); code == 0 || !strings.Contains(out, "the third check failed") {
		t.Fatalf("the second import, with a deletion link that is not the old site's: exit %d, %s", code, out)
	}
	if exists(mark) {
		t.Errorf("the failed import left the earlier pass's mark: %q", read(t, mark))
	}
}
