package minitest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run the same scripts against the real restic, rest-server and
// Litestream. They are skipped on a machine without the tools.

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func must(t *testing.T, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestRealToolsTheNightlyRunAgainstRestServer(t *testing.T) {
	need(t, "restic", "rest-server", "htpasswd", "openssl")
	b := newBackedUp(t)
	real, _ := exec.LookPath("restic")
	os.Remove(b.path(resticCopy))
	if err := os.Symlink(real, b.path(resticCopy)); err != nil {
		t.Fatal(err)
	}

	// The guest: a repository made with the guest's key, the mini's key
	// added, and rest-server in append-only mode in front of it.
	dir := t.TempDir()
	write(t, dir+"/guest-key", "the-guests-restic-key\n", 0o600)
	keeper := []string{"RESTIC_REPOSITORY=" + dir + "/restic/mini", "RESTIC_PASSWORD_FILE=" + dir + "/guest-key", "RESTIC_CACHE_DIR=" + dir + "/cache"}
	must(t, keeper, "restic", "init")
	must(t, keeper, "restic", "key", "add", "--new-password-file", b.path(backupKeys+"/restic-password"), "--user", "mini", "--host", "lab-mini")
	must(t, nil, "htpasswd", "-B", "-b", "-c", dir+"/htpasswd", "mini", strings.TrimSpace(houseSecrets["rest-password"]))
	must(t, nil, "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-days", "2", "-subj", "/CN=lab-backup", "-keyout", dir+"/tls.key", "-out", b.path(backupKeys+"/guest.crt"))
	addr := freePort(t)
	server := exec.Command("rest-server", "--path", dir+"/restic", "--listen", addr, "--append-only", "--private-repos", "--htpasswd-file", dir+"/htpasswd")
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.env = append(b.env, "LAB_BACKUP_HOUSE=rest:http://"+addr+"/mini/")

	// R2 is played by a second repository in a folder, made with the mini's key.
	offsite := []string{"RESTIC_REPOSITORY=" + dir + "/offsite", "RESTIC_PASSWORD_FILE=" + b.path(backupKeys+"/restic-password"), "RESTIC_CACHE_DIR=" + dir + "/cache"}
	must(t, offsite, "restic", "init")
	os.Chmod(b.path(backupKeys+"/r2-repository"), 0o600)
	write(t, b.path(backupKeys+"/r2-repository"), dir+"/offsite\n", 0o400)

	// An artifact as labd keeps it, for the Restore drill below.
	image := "a kernel image\n"
	write(t, b.path("var/lab/data/artifacts/"+artifactOf(image)), image, 0o600)

	b.ok("backup")
	data := b.path("var/lab/data")
	listed := b.ok("backup", "restic", "house", "ls", "latest")
	for _, want := range []string{data + "/snapshots/labd.db\n", data + "/snapshots/reports.db\n", data + "/artifacts/ab/kernel.img\n"} {
		if !strings.Contains(listed, want) {
			t.Errorf("the house copy lacks %s", want)
		}
	}
	for _, never := range []string{data + "/labd.db\n", data + "/labd.db-wal\n", data + "/reports.db\n", data + "/callers", "litestream", ".upload-"} {
		if strings.Contains(listed, never) {
			t.Errorf("the house copy carries %s", never)
		}
	}
	if out := b.ok("backup", "restic", "offsite", "ls", "latest"); strings.Contains(out, "artifacts") || strings.Contains(out, "admin.token") || !strings.Contains(out, data+"/snapshots/labd.db\n") {
		t.Errorf("the off-site copy holds more, or less, than the databases' copies:\n%s", out)
	}

	// The mini's key and password cannot take a snapshot away.
	if out, code := b.run("backup", "restic", "house", "forget", "--prune", "latest"); code == 0 {
		t.Errorf("the mini deleted a snapshot from the house copy:\n%s", out)
	}
	var snaps []struct{ ID string }
	json.Unmarshal([]byte(must(t, keeper, "restic", "snapshots", "--json")), &snaps)
	if len(snaps) != 1 {
		t.Errorf("after the mini's forget the guest holds %d snapshots, want 1", len(snaps))
	}

	back := t.TempDir()
	b.ok("backup", "restic", "house", "restore", "latest", "--target", back)
	if got := rows(t, filepath.Join(back, data, "snapshots/labd.db")); !same(got, []string{"in labd.db"}) {
		t.Errorf("labd.db restored from the house copy holds %v", got)
	}

	// The Restore drill, from the same two repositories: restic's own
	// listing, its times and what --include restores.
	drill := b.ok("restore-drill")
	for _, want := range []string{
		"labd.db from the house copy: intact, tables: 1, rows: 1",
		"reports.db from the house copy: intact",
		"the artifact " + artifactOf(image) + " from the house copy: is what its name says, and the same as the live file",
		"labd.db from the off-site copy: intact",
		"reports.db from the off-site copy: intact",
		"made 0 hours ago",
	} {
		if !strings.Contains(drill, want) {
			t.Errorf("the Restore drill against restic does not say %q:\n%s", want, drill)
		}
	}
	if left, _ := filepath.Glob(b.path("var/lab/run/restore-drill*")); len(left) != 0 {
		t.Errorf("the Restore drill left %v", left)
	}

	// The guest's pruning: of 400 nights, 7 daily, 4 weekly, 6 monthly stay,
	// and none older than six months.
	for ago := 2; ago <= 400; ago++ {
		at := time.Now().AddDate(0, 0, -ago).Format("2006-01-02 15:04:05")
		must(t, keeper, "restic", "backup", "--host", "lab-mini", "--time", at, data+"/snapshots")
	}
	g := &guest{t: t, root: filepath.Join(dir, "g"), dir: filepath.Join(repo(t), "scripts", "mini", "guest")}
	for _, d := range []string{"srv/lab-backup/sftp/db", "srv/lab-backup/sftp/status", "etc/lab-backup"} {
		os.MkdirAll(g.path(d), 0o755)
	}
	os.Rename(dir+"/restic", g.path("srv/lab-backup/restic"))
	write(t, g.path("etc/lab-backup/restic-password"), "the-guests-restic-key\n", 0o400)
	g.env = append(os.Environ(), "LAB_GUEST_ROOT="+g.root)
	g.ok("prune")
	moved := []string{"RESTIC_REPOSITORY=" + g.path("srv/lab-backup/restic/mini"), keeper[1], keeper[2]}
	var kept []struct{ Time time.Time }
	json.Unmarshal([]byte(must(t, moved, "restic", "snapshots", "--json")), &kept)
	week := 0
	for _, s := range kept {
		if age := time.Since(s.Time); age > 183*24*time.Hour {
			t.Errorf("a snapshot %d days old stays", int(age.Hours()/24))
		} else if age < 7*24*time.Hour {
			week++
		}
	}
	if len(kept) < 12 || len(kept) > 17 || week < 6 {
		t.Errorf("%d snapshots stay, %d of them from the last week; want 7 daily, 4 weekly, 6 monthly", len(kept), week)
	}
}

func TestRealToolsLitestreamIsASecondBehind(t *testing.T) {
	need(t, "litestream")
	dir := t.TempDir()
	// The lab's own configuration parses as this Litestream reads it.
	config := strings.ReplaceAll(read(t, filepath.Join(repo(t), "deploy", "litestream.yml")), "GUEST_HOST_KEY", strings.TrimSpace(houseSecrets["host-key"]))
	write(t, dir+"/lab.yml", config, 0o644)
	if out := must(t, nil, "litestream", "databases", "-config", dir+"/lab.yml"); !strings.Contains(out, "/var/lab/data/labd.db") || !strings.Contains(out, "/var/lab/data/reports.db") {
		t.Errorf("Litestream reads the lab's configuration as:\n%s", out)
	}

	// The same settings against a folder in place of the guest.
	write(t, dir+"/local.yml", fmt.Sprintf("snapshot:\n  interval: 24h\n  retention: 720h\ndbs:\n  - path: %s/labd.db\n    replica:\n      type: file\n      path: %s/replica\n      sync-interval: 1s\n", dir, dir), 0o644)
	db, err := sql.Open("sqlite", dir+"/labd.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "CREATE TABLE t (x TEXT)", "INSERT INTO t VALUES ('first')"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	replicate := exec.Command("litestream", "replicate", "-config", dir+"/local.yml")
	if err := replicate.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { replicate.Process.Kill(); replicate.Wait() })
	time.Sleep(3 * time.Second)
	if _, err := db.Exec("INSERT INTO t VALUES ('a second ago')"); err != nil {
		t.Fatal(err)
	}
	wrote := time.Now()
	for {
		out := filepath.Join(dir, fmt.Sprintf("restored-%d.db", time.Since(wrote).Milliseconds()))
		if exec.Command("litestream", "restore", "-config", dir+"/local.yml", "-o", out, dir+"/labd.db").Run() == nil && same(rows(t, out), []string{"first", "a second ago"}) {
			break
		}
		if time.Since(wrote) > 10*time.Second {
			t.Fatal("ten seconds on, the replica does not hold a row written to the database")
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("the replica held the row within %s", time.Since(wrote).Round(100*time.Millisecond))
}
