package minitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const hostsBefore = "127.0.0.1\tlocalhost\n255.255.255.255\tbroadcasthost\n::1             localhost\n"

// withBackups is a mini whose Homebrew list is the repository's, with a
// Deployed build that carries the backup script and an /etc/hosts.
func withBackups(t *testing.T) (*mini, key) {
	t.Helper()
	m, k := withTunnel(t)
	write(t, m.path("usr/local/lab/builds/v0.2.0-rc.1/scripts/mini/backup"), "#!/bin/bash\n", 0o755)
	write(t, m.path("etc/hosts"), hostsBefore, 0o644)
	return m, k
}

// keysFolder is what the owner carries to the mini: the guest's five files, R2's three, or both.
func keysFolder(t *testing.T, sets ...map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, set := range sets {
		for name, text := range set {
			write(t, filepath.Join(dir, name), text, 0o600)
		}
	}
	return dir
}

func (m *mini) daemonLoaded(label string) bool {
	return exists(filepath.Join(filepath.Dir(m.root), "state", "loaded."+label))
}

func TestSetupPutsTheBackupsKeysInPlaceAndStartsBoth(t *testing.T) {
	m, k := withBackups(t)
	folder := keysFolder(t, houseSecrets, offsiteSecrets)

	out := m.ok("setup", "--dry-run", "--release-key", k.signers, "--backup-guest", "192.168.0.186", "--backup-keys", folder)
	for _, want := range []string{
		"would install benbjohnson/litestream/litestream from Homebrew",
		"would install restic from Homebrew",
		"would name the backup guest in " + m.path("etc/hosts") + ": 192.168.0.186 lab-backup",
		"would put restic-password at " + m.path(backupKeys) + "/restic-password",
		"would write Litestream's configuration",
		"would start Litestream (" + litestreamLabel + ")",
		"would schedule the nightly backup (" + backupLabel + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not say it %s:\n%s", want, out)
		}
	}
	if strings.Contains(m.calls(), "brew deps") || strings.Contains(m.calls(), "brew --prefix") {
		t.Errorf("the dry run asked brew about a formula that is not installed, which fetches its tap:\n%s", m.calls())
	}
	if read(t, m.path("etc/hosts")) != hostsBefore || exists(m.path(backupKeys)) || !exists(folder+"/restic-password") || m.daemonLoaded(litestreamLabel) {
		t.Fatal("the dry run changed something")
	}

	out = m.ok("setup", "--release-key", k.signers, "--backup-guest", "192.168.0.186", "--backup-keys", folder)
	for _, set := range []map[string]string{houseSecrets, offsiteSecrets} {
		for name, text := range set {
			if strings.Contains(out, strings.TrimSpace(text)) && name != "host-key" {
				t.Errorf("the set-up printed %s", name)
			}
			if got := read(t, m.path(backupKeys+"/"+name)); got != text {
				t.Errorf("%s is %q in place", name, got)
			}
			if info, err := os.Stat(m.path(backupKeys + "/" + name)); err != nil || info.Mode().Perm() != 0o400 {
				t.Errorf("%s: %v, want it readable by its owner alone", name, info.Mode())
			}
			if exists(filepath.Join(folder, name)) {
				t.Errorf("%s was left in the folder it came in", name)
			}
		}
	}
	if info, err := os.Stat(m.path(backupKeys)); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the keys' folder: %v", info.Mode())
	}
	hostKey := strings.TrimSpace(houseSecrets["host-key"])
	if got := read(t, m.path(backupKeys+"/known_hosts")); got != "lab-backup "+hostKey+"\n" {
		t.Errorf("known_hosts is %q", got)
	}
	if got := read(t, m.path("etc/hosts")); got != hostsBefore+"192.168.0.186\tlab-backup\n" {
		t.Errorf("/etc/hosts is %q", got)
	}
	config := read(t, m.path("usr/local/lab/litestream.yml"))
	if strings.Count(config, "host-key: "+hostKey+"\n") != 2 || strings.Contains(config, "GUEST_HOST_KEY\n") {
		t.Errorf("Litestream's configuration does not pin the guest's host key:\n%s", config)
	}
	for _, tool := range []string{"litestream", "restic", "cloudflared"} {
		if !exists(m.path("usr/local/lab/tools/current/" + tool + "/bin/" + tool)) {
			t.Errorf("%s is not in the root-owned copy under its own name", tool)
		}
	}
	if !strings.Contains(m.calls(), "brew install benbjohnson/litestream/litestream [auto_update=1 upgrade=1") {
		t.Errorf("Litestream was not asked of its tap:\n%s", m.calls())
	}
	for _, label := range []string{litestreamLabel, backupLabel} {
		if !m.daemonLoaded(label) {
			t.Errorf("%s is not loaded\n%s", label, out)
		}
		if read(t, m.path("Library/LaunchDaemons/"+label+".plist")) != read(t, filepath.Join(repo(t), "deploy", label+".plist")) {
			t.Errorf("%s's file is not the repository's", label)
		}
	}

	if out := m.ok("setup"); !strings.Contains(out, "setup: nothing to change") {
		t.Errorf("a second run:\n%s", out)
	}

	// The guest moved: one line changes, and nothing restarts for it.
	before := m.calls()
	out = m.ok("setup", "--backup-guest", "192.168.0.187")
	if got := read(t, m.path("etc/hosts")); got != hostsBefore+"192.168.0.187\tlab-backup\n" {
		t.Errorf("/etc/hosts after the guest moved is %q", got)
	}
	if strings.Contains(strings.TrimPrefix(m.calls(), before), "bootstrap") {
		t.Errorf("a new address restarted a daemon:\n%s", out)
	}

	// A new host key reaches the configuration, and Litestream starts again.
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherOtherOtherOtherOtherOtherOtherOtherOtherOth"
	out = m.ok("setup", "--backup-keys", keysFolder(t, map[string]string{"host-key": other + "\n"}))
	if !strings.Contains(read(t, m.path("usr/local/lab/litestream.yml")), "host-key: "+other+"\n") || !strings.Contains(out, "start Litestream") || strings.Contains(out, "schedule the nightly backup") {
		t.Errorf("after a new host key:\n%s", out)
	}
	if got := read(t, m.path(backupKeys+"/known_hosts")); got != "lab-backup "+other+"\n" {
		t.Errorf("known_hosts after a new host key is %q", got)
	}
}

func TestSetupStartsNoBackupBeforeItsKeysAreThere(t *testing.T) {
	m, k := withBackups(t)
	out := m.ok("setup", "--release-key", k.signers)
	for _, want := range []string{
		"Litestream is not started: it has no litestream-sftp-key yet",
		"the nightly backup is not scheduled: it has no restic-password yet",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the set-up does not say %q:\n%s", want, out)
		}
	}

	// The guest's keys alone: Litestream replicates, the nightly run waits for R2.
	out = m.ok("setup", "--backup-keys", keysFolder(t, houseSecrets))
	if !strings.Contains(out, "Litestream is not started: "+m.path("etc/hosts")+" does not name lab-backup yet") {
		t.Errorf("with keys and no address:\n%s", out)
	}
	out = m.ok("setup", "--backup-guest", "192.168.0.186")
	if !m.daemonLoaded(litestreamLabel) || m.daemonLoaded(backupLabel) || !strings.Contains(out, "the nightly backup is not scheduled: it has no r2-repository yet") {
		t.Errorf("with the guest's keys alone:\n%s", out)
	}
	out = m.ok("setup", "--backup-keys", keysFolder(t, offsiteSecrets))
	if !m.daemonLoaded(backupLabel) {
		t.Errorf("with R2's keys too:\n%s", out)
	}
}

func TestSetupRefusesBackupKeysThatAreNotWhatTheyShouldBe(t *testing.T) {
	m, k := withBackups(t)
	m.ok("setup", "--release-key", k.signers)
	open := keysFolder(t, houseSecrets)
	os.Chmod(open+"/rest-password", 0o644)
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"a name for an address":      {[]string{"--backup-guest", "lab-backup.local"}, "is not an address"},
		"a key others can read":      {[]string{"--backup-keys", open}, "rest-password can be read by others"},
		"a host key with more in it": {[]string{"--backup-keys", keysFolder(t, map[string]string{"host-key": "ssh-ed25519 AAAA|x\n      exec: reboot\n"})}, "is not one SSH host key"},
		"a host key that is no key":  {[]string{"--backup-keys", keysFolder(t, map[string]string{"host-key": "ssh-ed25519 AAAA|x\n"})}, "is not one SSH host key"},
		"a bucket that is no bucket": {[]string{"--backup-keys", keysFolder(t, map[string]string{"r2-repository": "s3:http://example.com/x y\n"})}, "is not s3:https://"},
		"a certificate that is none": {[]string{"--backup-keys", keysFolder(t, map[string]string{"guest.crt": "hello\n"})}, "is no certificate"},
		"an empty folder":            {[]string{"--backup-keys", t.TempDir()}, "holds none of"},
		"no folder":                  {[]string{"--backup-keys", "/nonexistent"}, "no folder"},
	} {
		if out := m.refused("setup", c.args...); !strings.Contains(out, c.want) {
			t.Errorf("%s: %s", name, out)
		}
	}
	if exists(m.path(backupKeys+"/rest-password")) || read(t, m.path("etc/hosts")) != hostsBefore {
		t.Error("a refused run changed something")
	}

	// What the lab account keeps as the host key is written into a file
	// everyone reads: only a host key is taken from there, and never through a link.
	m.ok("setup", "--backup-guest", "192.168.0.186", "--backup-keys", keysFolder(t, houseSecrets))
	config := read(t, m.path("usr/local/lab/litestream.yml"))
	os.Chmod(m.path(backupKeys+"/host-key"), 0o600)
	write(t, m.path(backupKeys+"/host-key"), "ssh-ed25519 AAAA\n      exec: reboot\n", 0o400)
	if out := m.refused("setup"); !strings.Contains(out, "host-key is not one SSH host key") {
		t.Errorf("a host key changed in place: %s", out)
	}
	os.Remove(m.path(backupKeys + "/host-key"))
	write(t, m.path("usr/local/lab/secrets/oobd.token"), "roots-own-token\n", 0o600)
	if err := os.Symlink(m.path("usr/local/lab/secrets/oobd.token"), m.path(backupKeys+"/host-key")); err != nil {
		t.Fatal(err)
	}
	if out := m.refused("setup"); !strings.Contains(out, "is a symbolic link") {
		t.Errorf("a link planted for the host key: %s", out)
	}
	if read(t, m.path("usr/local/lab/litestream.yml")) != config {
		t.Error("a refused host key reached Litestream's configuration")
	}
}

func daemon(t *testing.T, label string) (map[string]string, []string, string) {
	t.Helper()
	file := filepath.Join(repo(t), "deploy", label+".plist")
	values, arrays := plist(t, file)
	for key, want := range map[string]string{"Label": label, "UserName": "lab", "GroupName": "lab", "Umask": "63"} {
		if values[key] != want {
			t.Errorf("%s: %s is %q, want %q", label, key, values[key], want)
		}
	}
	text := read(t, file)
	for _, word := range []string{"TOKEN", "SECRET", "PASSWORD", "EnvironmentVariables", "/opt/homebrew"} {
		if strings.Contains(text, word) {
			t.Errorf("%s's LaunchDaemon file holds %s", label, word)
		}
	}
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		if !strings.HasPrefix(values[key], "/var/lab/log/") {
			t.Errorf("%s: %s is %q, outside the lab account's log folder", label, key, values[key])
		}
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("plutil", "-lint", file).CombinedOutput(); err != nil {
			t.Errorf("plutil: %v\n%s", err, out)
		}
	}
	return values, arrays["ProgramArguments"], text
}

func TestLitestreamsAndTheBackupJobsLaunchDaemons(t *testing.T) {
	values, args, _ := daemon(t, litestreamLabel)
	if want := []string{"/usr/local/lab/tools/current/litestream/bin/litestream", "replicate", "-config", "/usr/local/lab/litestream.yml"}; !same(args, want) {
		t.Errorf("Litestream runs as %v, want %v", args, want)
	}
	if values["KeepAlive"] != "true" || values["RunAtLoad"] != "true" {
		t.Error("Litestream is not kept running")
	}

	values, args, text := daemon(t, backupLabel)
	if !same(args, []string{"/usr/local/lab/current/scripts/mini/backup"}) {
		t.Errorf("the backup job runs %v, want the Deployed build's script and no argument", args)
	}
	if values["KeepAlive"] != "" || values["RunAtLoad"] != "" {
		t.Error("the backup job runs more often than nightly")
	}
	if !regexp.MustCompile(`<key>StartCalendarInterval</key>\s*<dict>\s*<key>Hour</key><integer>2</integer>\s*<key>Minute</key><integer>30</integer>\s*</dict>`).MatchString(text) {
		t.Error("the backup job is not at 02:30 each night")
	}

	for name, want := range map[string]string{
		"LITESTREAM_LABEL":  litestreamLabel,
		"BACKUP_LABEL":      backupLabel,
		"BACKUP_GUEST":      "lab-backup",
		"LITESTREAM_CONFIG": "$LAB_ROOT/litestream.yml",
		"LITESTREAM":        "$LAB_TOOLS/current/litestream/bin/litestream",
		"RESTIC":            "$LAB_TOOLS/current/" + filepath.FromSlash("restic/bin/restic"),
		"BACKUP_KEYS":       "$LAB_HOME/secrets/backup",
		"LAB_DATABASES":     "labd.db reports.db",
	} {
		if got := shell(t, name); got != want {
			t.Errorf("scripts/mini/lib.sh: %s is %q, want %q", name, got, want)
		}
	}
	list := " " + strings.Join(formulaeList(t), " ") + " "
	if !strings.Contains(list, " litestream ") || !strings.Contains(list, " restic ") {
		t.Errorf("deploy/homebrew-formulae names %s", list)
	}
	// Homebrew core has no Litestream: it is asked of its tap by its full name.
	if !regexp.MustCompile(`(?m)^benbjohnson/litestream/litestream\s`).MatchString(read(t, filepath.Join(repo(t), "deploy", "homebrew-formulae"))) {
		t.Error("Litestream is not listed by its tap's name")
	}
}

// litestreamConfig reads deploy/litestream.yml, which is kept to the shape
// Litestream 0.5 reads: "snapshot" with two settings, and under "dbs" one
// database after another, each a path and a replica's settings.
func litestreamConfig(t *testing.T, text string) (snapshot map[string]string, dbs []map[string]string) {
	t.Helper()
	snapshot = map[string]string{}
	section := ""
	for n, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, "\t") {
			t.Fatalf("line %d has a tab", n+1)
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		k, v, _ := strings.Cut(strings.TrimSpace(line), ":")
		v = strings.TrimSpace(v)
		switch {
		case indent == 0 && v == "" && (k == "snapshot" || k == "dbs"):
			section = k
		case section == "snapshot" && indent == 2 && v != "":
			snapshot[k] = v
		case section == "dbs" && indent == 2 && strings.HasPrefix(k, "- path") && v != "":
			dbs = append(dbs, map[string]string{"db": v})
		case section == "dbs" && indent == 4 && k == "replica" && v == "" && len(dbs) > 0:
		case section == "dbs" && indent == 6 && v != "" && len(dbs) > 0:
			if _, twice := dbs[len(dbs)-1][k]; twice {
				t.Fatalf("line %d sets %s twice", n+1, k)
			}
			dbs[len(dbs)-1][k] = v
		default:
			t.Fatalf("line %d is outside the shape the test reads: %q", n+1, line)
		}
	}
	return snapshot, dbs
}

func TestLitestreamsConfiguration(t *testing.T) {
	snapshot, dbs := litestreamConfig(t, read(t, filepath.Join(repo(t), "deploy", "litestream.yml")))
	// Thirty days back: Litestream drops what is older than its oldest snapshot.
	if len(snapshot) != 2 || snapshot["retention"] != "720h" || snapshot["interval"] != "24h" {
		t.Errorf("snapshot is %v, want one a day, kept 720h", snapshot)
	}
	want := map[string]string{"/var/lab/data/labd.db": "/db/labd", "/var/lab/data/reports.db": "/db/reports"}
	if len(dbs) != len(want) || len(strings.Fields(shell(t, "LAB_DATABASES"))) != len(want) {
		t.Fatalf("%d databases are replicated, want both", len(dbs))
	}
	for _, db := range dbs {
		for k, v := range map[string]string{
			"type":          "sftp",
			"host":          "lab-backup:22",
			"user":          "litestream",
			"path":          want[db["db"]],
			"key-path":      "/var/lab/secrets/backup/litestream-sftp-key",
			"host-key":      "GUEST_HOST_KEY",
			"sync-interval": "1s",
		} {
			if db[k] != v {
				t.Errorf("%s: %s is %q, want %q", db["db"], k, db[k], v)
			}
		}
		if want[db["db"]] == "" {
			t.Errorf("%s is not one of the lab's two databases", db["db"])
		}
		// The database and seven settings; among them no password and no key itself.
		if len(db) != 8 {
			t.Errorf("%s has settings the lab does not use: %v", db["db"], db)
		}
	}
}
