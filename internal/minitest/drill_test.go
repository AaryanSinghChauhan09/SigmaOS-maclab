package minitest

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// drilled is a backed-up mini the morning after: an artifact as labd keeps
// it (a file named by its SHA-256), last night's backup in both targets, the
// checks in place, and a live database that has gone on since.
type drilled struct {
	*backedUp
	out      *outside
	artifact string
	data     string
}

func artifactOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func newDrilled(t *testing.T, more ...string) *drilled {
	t.Helper()
	need(t, "shasum", "cmp")
	b := newBackedUp(t)
	d := &drilled{backedUp: b, data: b.path("var/lab/data"), artifact: artifactOf("a kernel image\n")}
	write(t, d.data+"/artifacts/"+d.artifact, "a kernel image\n", 0o600)
	for _, text := range more {
		write(t, d.data+"/artifacts/"+artifactOf(text), text, 0o600)
	}
	d.out = b.withOutside()
	b.ok("backup")
	if _, err := b.labd.Exec("INSERT INTO t VALUES ('this morning')"); err != nil {
		t.Fatal(err)
	}
	os.Remove(b.log)
	d.out.set(func(o *outside) { o.events = nil })
	return d
}

// kept is where a target's snapshot holds a file of the data directory.
func (d *drilled) kept(target, id, file string) string {
	return filepath.Join(d.state, "restic", target, "snapshots", id, "files", d.data, file)
}

func (d *drilled) scratch() []string {
	left, _ := filepath.Glob(d.path("var/lab/run/restore-drill*"))
	return left
}

// database writes a small database of its own over file.
func database(t *testing.T, file string, queries ...string) {
	t.Helper()
	os.Remove(file)
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestTheDrillRestoresBothDatabasesAndOneArtifactAndChecksThem(t *testing.T) {
	d := newDrilled(t)

	out := d.ok("restore-drill")

	for _, want := range []string{
		"restore-drill: the house copy: snapshot 00000001 of lab-mini, made 0 hours ago",
		"restore-drill: labd.db from the house copy: intact, tables: 1, rows: 1, rows in the live database now: 2",
		"restore-drill: reports.db from the house copy: intact, tables: 1, rows: 1, rows in the live database now: 1",
		"restore-drill: the artifact " + d.artifact + " from the house copy: is what its name says, and the same as the live file",
		"restore-drill: the off-site copy: snapshot 00000001 of lab-mini, made 0 hours ago",
		"restore-drill: labd.db from the off-site copy: intact, tables: 1, rows: 1, rows in the live database now: 2",
		"restore-drill: reports.db from the off-site copy: intact, tables: 1, rows: 1, rows in the live database now: 1",
		"restore-drill: done",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("the drill does not say %q:\n%s", want, out)
		}
	}

	// What was asked of each target: its newest snapshot, by id, into a
	// folder of the lab account's own, and no more of the house copy than
	// the two databases and the one artifact.
	house, offsite := resticCalls(d.backedUp)
	scratch := d.path("var/lab/run") + "/restore-drill."
	copies := " --include " + d.data + "/snapshots/labd.db --include " + d.data + "/snapshots/reports.db"
	var restored int
	for _, line := range append(house, offsite...) {
		args := strings.SplitN(line, " [", 2)[0]
		switch {
		case args == "restic snapshots --json --host lab-mini":
		case args == "restic ls --json 00000001 "+d.data+"/artifacts" && strings.Contains(line, "repository=rest:"):
		case strings.HasPrefix(args, "restic restore 00000001 --target "+scratch) && strings.HasSuffix(args, "/house"+copies+" --include "+d.data+"/artifacts/"+d.artifact):
			restored++
		case strings.HasPrefix(args, "restic restore 00000001 --target "+scratch) && strings.HasSuffix(args, "/offsite"+copies):
			restored++
		default:
			t.Errorf("the drill asked restic for something else: %s", args)
		}
	}
	if restored != 2 || len(house) != 3 || len(offsite) != 2 {
		t.Errorf("restic ran %d times for the house and %d for R2, with %d restores:\n%s", len(house), len(offsite), restored, d.calls())
	}

	if left := d.scratch(); len(left) != 0 {
		t.Errorf("the drill left what it restored: %v", left)
	}
	if d.told() != "" {
		t.Errorf("a good drill told the owner: %s", d.told())
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(read(t, d.path("var/lab/log/restore-drill.ok")))); err != nil {
		t.Errorf("a good drill did not write when it ended: %v", err)
	}
	if got := d.out.pinged(); !same(got, []string{"restore-drill"}) {
		t.Errorf("a good drill pinged %v, want its own check once", got)
	}

	// Nothing that opens a target or names a check is an argument.
	noCheckInAnArgument(t, d.out, d.calls())
	for _, line := range strings.Split(d.calls(), "\n") {
		args := strings.SplitN(line, " [", 2)[0]
		for _, s := range []string{"the-minis-restic-key", "the-rest-servers-password", "the-r2-key-id", "the-r2-secret", "0123456789abcdef"} {
			if strings.Contains(args, s) || strings.Contains(out, s) {
				t.Errorf("%q is an argument, or was printed: %s", s, args)
			}
		}
	}
}

func TestWhatTheDrillRestoresIsReadByTheLabAccountAlone(t *testing.T) {
	d := newDrilled(t)
	// A restic that stops to look at where it was told to restore.
	real := read(t, d.path(resticCopy))
	spy := strings.Replace(real, "  restore)\n", "  restore)\n    ls -ld \"$(dirname \"$3\")\" >> \"$STUB_STATE/scratch\"\n", 1)
	if spy == real {
		t.Fatal("the stand-in for restic has no restore to look into")
	}
	write(t, d.path(resticCopy), spy, 0o755)
	d.ok("restore-drill")
	seen := read(t, filepath.Join(d.state, "scratch"))
	if strings.Count(seen, "drwx------") != 2 || !strings.Contains(seen, d.path("var/lab/run")+"/restore-drill.") {
		t.Errorf("the scratch folder is not the lab account's alone, below /var/lab/run:\n%s", seen)
	}

	// What a drill that was cut short left behind goes with the next one.
	write(t, d.path("var/lab/run/restore-drill.Xa1b2c/house/labd.db"), "half a restore\n", 0o600)
	d.ok("restore-drill")
	if left := d.scratch(); len(left) != 0 {
		t.Errorf("an earlier drill's leftovers stay: %v", left)
	}
}

func TestADamagedCopyFailsTheDrill(t *testing.T) {
	old := time.Now().UTC().Add(-40 * time.Hour).Format("2006-01-02T15:04:05.000000000Z")
	for name, c := range map[string]struct {
		set  func(d *drilled)
		want string
	}{
		"a database damaged in the house copy": {
			func(d *drilled) { damage(t, d.kept("house", "00000001", "snapshots/labd.db")) },
			"labd.db from the house copy is damaged",
		},
		"a database damaged off-site": {
			func(d *drilled) { damage(t, d.kept("offsite", "00000001", "snapshots/reports.db")) },
			"reports.db from the off-site copy is damaged",
		},
		"a database cut short": {
			func(d *drilled) { os.Truncate(d.kept("house", "00000001", "snapshots/reports.db"), 4096) },
			"reports.db from the house copy is damaged",
		},
		"a database that is not in the copy": {
			func(d *drilled) { os.Remove(d.kept("offsite", "00000001", "snapshots/labd.db")) },
			"labd.db did not come back from the off-site copy",
		},
		"an artifact damaged in the copy": {
			func(d *drilled) {
				write(t, d.kept("house", "00000001", "artifacts/"+d.artifact), "a kernel imagE\n", 0o600)
			},
			"the artifact " + artifactOf("a kernel image\n") + " from the house copy is not what its name says",
		},
		"a copy older than 36 hours": {
			func(d *drilled) {
				write(t, filepath.Join(d.state, "restic/house/snapshots/00000001/time"), old+"\n", 0o644)
			},
			"the house copy's newest snapshot is 40 hours old",
		},
		"another lab's database": {
			func(d *drilled) {
				database(t, d.kept("offsite", "00000001", "snapshots/labd.db"), "CREATE TABLE t (x TEXT)", "CREATE TABLE elsewhere (x TEXT)", "INSERT INTO t VALUES ('theirs')")
			},
			"labd.db from the off-site copy has a table, elsewhere, that the live database lacks",
		},
		"an empty database": {
			func(d *drilled) {
				database(t, d.kept("house", "00000001", "snapshots/labd.db"), "CREATE TABLE t (x TEXT)")
			},
			"labd.db from the house copy is empty, and the live database holds 2 rows",
		},
		"a file that is no database": {
			func(d *drilled) {
				write(t, d.kept("house", "00000001", "snapshots/reports.db"), "not a database at all\n", 0o600)
			},
			"reports.db from the house copy is damaged",
		},
		"a target with no snapshot": {
			func(d *drilled) { os.RemoveAll(filepath.Join(d.state, "restic/offsite/snapshots")) },
			"the off-site copy holds no snapshot of lab-mini",
		},
		"a target that cannot be read": {
			func(d *drilled) { write(t, filepath.Join(d.state, "restic.fails"), "house snapshots\n", 0o644) },
			"the house copy's snapshots could not be listed",
		},
		"a restore that fails": {
			func(d *drilled) { write(t, filepath.Join(d.state, "restic.fails"), "offsite restore\n", 0o644) },
			"the off-site copy could not be restored",
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDrilled(t)
			c.set(d)

			out := d.refused("restore-drill")

			if got := d.told(); !strings.Contains(got, " agents The Restore drill failed: ") || !strings.Contains(got, c.want) || strings.Count(got, "\n") != 1 {
				t.Errorf("the owner is told %q, want one line with %q\n%s", got, c.want, out)
			}
			if exists(d.path("var/lab/log/restore-drill.ok")) {
				t.Error("a failed drill wrote that it went well")
			}
			if got := d.out.saw(); len(got) != 0 {
				t.Errorf("a failed drill reached %v", got)
			}
			if left := d.scratch(); len(left) != 0 {
				t.Errorf("a failed drill left what it restored: %v", left)
			}
			// The other target is still drilled.
			other := "from the off-site copy: intact"
			if strings.Contains(c.want, "off-site") {
				other = "from the house copy: intact"
			}
			if !strings.Contains(out, other) {
				t.Errorf("one target's failure kept the other from being drilled:\n%s", out)
			}
		})
	}
}

// damage writes over the second page of a database, where its one table is.
func damage(t *testing.T, file string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte(strings.Repeat("\xff", 64)), 4096); err != nil {
		t.Fatal(err)
	}
}

func TestTheDrillComparesWhatItRestoredWithTheLiveLab(t *testing.T) {
	// A table made since the backup, by a Deploy's migration, fails nothing:
	// the live database has gone on.
	d := newDrilled(t)
	if _, err := d.labd.Exec("CREATE TABLE since (x TEXT)"); err != nil {
		t.Fatal(err)
	}
	if out := d.ok("restore-drill"); !strings.Contains(out, "labd.db from the house copy: intact, tables: 1, rows: 1, rows in the live database now: 2") {
		t.Errorf("after a migration:\n%s", out)
	}

	// The live artifact is not what the copy holds.
	d = newDrilled(t)
	os.Chmod(d.data+"/artifacts/"+d.artifact, 0o600)
	write(t, d.data+"/artifacts/"+d.artifact, "another image\n", 0o600)
	out := d.refused("restore-drill")
	if !strings.Contains(d.told(), "the artifact "+d.artifact+" from the house copy differs from the live file") {
		t.Errorf("with the live artifact changed, the owner is told %q\n%s", d.told(), out)
	}

	// The live database cannot be read.
	d = newDrilled(t)
	d.labd.Close()
	for _, f := range []string{"labd.db", "labd.db-wal", "labd.db-shm"} {
		os.Remove(d.data + "/" + f)
	}
	out = d.refused("restore-drill")
	if !strings.Contains(d.told(), "the live labd.db could not be read") {
		t.Errorf("with no live labd.db, the owner is told %q\n%s", d.told(), out)
	}
}

func TestTheDrillTakesAnArtifactTheMiniStillHolds(t *testing.T) {
	// Of two in the copy, one was removed on the mini since: the other is drilled.
	d := newDrilled(t, "a second image\n")
	second := artifactOf("a second image\n")
	os.Remove(d.data + "/artifacts/" + d.artifact)
	out := d.ok("restore-drill")
	if !strings.Contains(out, "the artifact "+second+" from the house copy: is what its name says") || strings.Contains(d.calls(), "--include "+d.data+"/artifacts/"+d.artifact) {
		t.Errorf("with one artifact gone from the mini:\n%s", out)
	}

	// Every artifact of the copy is gone from the mini: said, and no failure.
	os.Remove(d.data + "/artifacts/" + second)
	out = d.ok("restore-drill")
	if !strings.Contains(out, "no artifact of the house copy is still on the mini: none was restored") {
		t.Errorf("with every artifact gone from the mini:\n%s", out)
	}

	// A lab that has no artifact yet.
	b := newBackedUp(t)
	os.RemoveAll(b.path("var/lab/data/artifacts"))
	b.ok("backup")
	out = b.ok("restore-drill")
	if !strings.Contains(out, "the house copy holds no artifact, and neither does the mini: none was restored") {
		t.Errorf("with no artifact anywhere:\n%s", out)
	}

	// The mini holds artifacts and the copy none: the backup leaves them out.
	d = newDrilled(t)
	os.RemoveAll(filepath.Dir(d.kept("house", "00000001", "artifacts/"+d.artifact)))
	out = d.refused("restore-drill")
	if !strings.Contains(d.told(), "the house copy holds no artifact, and the mini holds 1") {
		t.Errorf("with the artifacts left out of the copy, the owner is told %q\n%s", d.told(), out)
	}

	// Only what labd keeps as an artifact is one: a file named by a SHA-256.
	d = newDrilled(t)
	write(t, d.kept("house", "00000001", "artifacts/$(reboot)"), "x\n", 0o600)
	write(t, d.data+"/artifacts/$(reboot)", "x\n", 0o600)
	out = d.ok("restore-drill")
	if strings.Contains(d.calls(), "reboot") {
		t.Errorf("a file that is no artifact was drilled:\n%s", out)
	}
}

func TestTheDrillsDryRunSaysWhatWouldRunAndRunsNothing(t *testing.T) {
	d := newDrilled(t)
	out := d.ok("restore-drill", "--dry-run")
	want := strings.Join([]string{
		"restore-drill: would restore, from the newest snapshot of lab-mini in the house copy: " + d.data + "/snapshots/labd.db, " + d.data + "/snapshots/reports.db and one artifact of " + d.data + "/artifacts",
		"restore-drill: would restore, from the newest snapshot of lab-mini in the off-site copy: " + d.data + "/snapshots/labd.db, " + d.data + "/snapshots/reports.db",
		"restore-drill: would restore into a new folder under " + d.path("var/lab/run") + ", and remove it afterwards",
		"restore-drill: would check each snapshot is no older than 36 hours, each database for damage and against the live one (its tables, its rows), the artifact against its name and the live file",
		"restore-drill: would ping the check restore-drill",
		"restore-drill: nothing was run",
		"",
	}, "\n")
	if out != want {
		t.Errorf("the dry run says\n%s\nwant\n%s", out, want)
	}
	if d.calls() != "" || len(d.scratch()) != 0 || exists(d.path("var/lab/log/restore-drill.ok")) || len(d.out.saw()) != 0 {
		t.Errorf("the dry run did something:\n%s", d.calls())
	}
	if out := d.refused("restore-drill", "--target", "/tmp"); !strings.Contains(out, "scripts/mini/restore-drill [--dry-run]") {
		t.Errorf("an argument it does not know:\n%s", out)
	}
}

func TestOneDrillAtATime(t *testing.T) {
	d := newDrilled(t)
	// A drill that is running: this test's own process holds the lock.
	write(t, d.path("var/lab/run/restore-drill.lock/pid"), strconv.Itoa(os.Getpid())+"\n", 0o600)
	if out := d.refused("restore-drill"); !strings.Contains(out, "another Restore drill is running") {
		t.Errorf("with a drill running:\n%s", out)
	}
	if d.told() != "" || len(d.out.saw()) != 0 {
		t.Error("the second drill told the owner, or pinged")
	}
	// A lock left by a drill that died is taken over: no process has that number.
	write(t, d.path("var/lab/run/restore-drill.lock/pid"), "99999999\n", 0o600)
	d.ok("restore-drill")
	if exists(d.path("var/lab/run/restore-drill.lock")) {
		t.Error("the drill left its lock")
	}
}
