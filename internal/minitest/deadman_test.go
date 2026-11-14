package minitest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const (
	deadManLabel = "org.omarchy-mac.lab.dead-man"
	drillLabel   = "org.omarchy-mac.lab.restore-drill"
	checksFolder = "var/lab/secrets/checks"
)

// A check's address is its only secret. These are made up.
var checkIDs = map[string]string{
	"dead-man":      "11111111-1111-4111-8111-111111111111",
	"backup":        "22222222-2222-4222-8222-222222222222",
	"restore-drill": "33333333-3333-4333-8333-333333333333",
}

// outside is what the mini reaches beyond the house: the Lab address as
// Cloudflare answers for it, and the checks at healthchecks.io. It writes
// down, in order, what was asked of either.
type outside struct {
	mu     sync.Mutex
	events []string
	// How the Lab address answers.
	status int
	body   string
	ray    string
	cache  string
	// Whether healthchecks.io takes a ping.
	checksDown bool
	checks     *httptest.Server
	door       *httptest.Server
}

func (o *outside) set(f func(o *outside)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f(o)
}

func (o *outside) saw() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string{}, o.events...)
}

// pinged lists the checks that were told the lab is alive, in order.
func (o *outside) pinged() []string {
	var out []string
	for _, e := range o.saw() {
		if name := strings.TrimPrefix(e, "check "); name != e {
			out = append(out, name)
		}
	}
	return out
}

// withOutside gives the mini a Lab address that answers through Cloudflare,
// the three checks with their addresses in place, and a curl that writes
// down its arguments before it runs.
func (m *mini) withOutside() *outside {
	m.t.Helper()
	o := &outside{status: http.StatusOK, body: "ok\n", ray: "8e2f1a2b3c4d5e6f-BNE"}
	o.door = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.events = append(o.events, "door "+r.URL.Path)
		status, body, ray, cache := o.status, o.body, o.ray, o.cache
		o.mu.Unlock()
		if ray != "" {
			w.Header().Set("CF-RAY", ray)
			w.Header().Set("Server", "cloudflare")
		}
		if cache != "" {
			w.Header().Set("CF-Cache-Status", cache)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	m.t.Cleanup(o.door.Close)
	o.checks = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.checksDown {
			o.events = append(o.events, "check refused")
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		name := "unknown " + r.URL.Path
		for n, id := range checkIDs {
			if r.URL.Path == "/"+id {
				name = n
			}
		}
		o.events = append(o.events, "check "+name)
		io.WriteString(w, "OK")
	}))
	m.t.Cleanup(o.checks.Close)
	for name, id := range checkIDs {
		write(m.t, m.path(checksFolder+"/"+name), o.checks.URL+"/"+id+"\n", 0o400)
	}
	for _, d := range []string{"var/lab/run", "var/lab/log"} {
		if err := os.MkdirAll(m.path(d), 0o700); err != nil {
			m.t.Fatal(err)
		}
	}
	curl, err := exec.LookPath("curl")
	if err != nil {
		m.t.Skip("needs curl")
	}
	write(m.t, m.bin+"/curl", "#!/bin/bash\necho \"curl $*\" >> \"$STUB_LOG\"\nexec "+curl+" \"$@\"\n", 0o755)
	m.env = append(m.env, "LAB_FRONT_DOOR="+o.door.URL, "LAB_CHECKS_AT="+o.checks.URL)
	return o
}

// noCheckInAnArgument fails when a check's address, or any part that tells
// it from another's, was handed to a program as an argument.
func noCheckInAnArgument(t *testing.T, o *outside, calls string) {
	t.Helper()
	never := []string{strings.TrimPrefix(o.checks.URL, "http://"), "hc-ping.com"}
	for _, id := range checkIDs {
		never = append(never, id)
	}
	for _, line := range strings.Split(calls, "\n") {
		args := strings.SplitN(line, " [", 2)[0]
		for _, s := range never {
			if strings.Contains(args, s) {
				t.Errorf("a check's address is an argument: %s", args)
			}
		}
	}
}

func TestTheDeadManSwitchPingsOnceTheLabAnsweredThroughCloudflare(t *testing.T) {
	m := newMini(t)
	o := m.withOutside()

	out := m.ok("dead-man")

	if got := o.saw(); !same(got, []string{"door /healthz", "check dead-man"}) {
		t.Errorf("the mini asked %v; want the Lab address first, then its own check and no other\n%s", got, out)
	}
	noCheckInAnArgument(t, o, m.calls())
	for _, id := range checkIDs {
		if strings.Contains(out, id) {
			t.Errorf("the run printed a check's address:\n%s", out)
		}
	}
	// What it asks is the Lab address itself, as a visitor reaches it.
	if !strings.Contains(m.calls(), o.door.URL+"/healthz") {
		t.Errorf("the Lab address was not asked for /healthz:\n%s", m.calls())
	}
	if n := strings.Count(m.calls(), "curl "); n != 2 {
		t.Errorf("curl ran %d times, want the fetch and the ping:\n%s", n, m.calls())
	}
}

func TestTheDeadManSwitchSendsNothingWhenTheLabDoesNotAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		set  func(o *outside)
		want string
	}{
		"Cloudflare has no tunnel to send it to": {func(o *outside) { o.status = 530; o.body = "error code: 1033\n" }, "did not answer"},
		"labd is down behind the tunnel":         {func(o *outside) { o.status = http.StatusBadGateway; o.body = "Bad gateway\n" }, "did not answer"},
		"something else answers there":           {func(o *outside) { o.body = "<html>parked</html>\n" }, "did not answer ok"},
		"the answer did not cross Cloudflare":    {func(o *outside) { o.ray = "" }, "not through Cloudflare"},
		"Cloudflare answered from its cache":     {func(o *outside) { o.cache = "HIT" }, "from Cloudflare's cache"},
		"a redirect":                             {func(o *outside) { o.status = http.StatusMovedPermanently; o.body = "" }, "did not answer ok"},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMini(t)
			o := m.withOutside()
			o.set(c.set)

			out := m.refused("dead-man")

			if got := o.saw(); !same(got, []string{"door /healthz"}) {
				t.Errorf("the mini asked %v; want the Lab address and no check\n%s", got, out)
			}
			if !strings.Contains(out, c.want) || !strings.Contains(out, "the check was not pinged") {
				t.Errorf("it does not say %q and that nothing was sent:\n%s", c.want, out)
			}
			if !regexp.MustCompile(`(?m)^dead-man: \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ `).MatchString(out) {
				t.Errorf("the line carries no time, and launchd's log adds none:\n%s", out)
			}
		})
	}

	// The house has no uplink at all.
	m := newMini(t)
	o := m.withOutside()
	o.door.Close()
	out := m.refused("dead-man")
	if len(o.pinged()) != 0 || !strings.Contains(out, "did not answer") {
		t.Errorf("with nothing at the Lab address:\n%s", out)
	}
}

func TestTheDeadManSwitchWithoutACheckOfItsOwn(t *testing.T) {
	m := newMini(t)
	o := m.withOutside()
	os.Remove(m.path(checksFolder + "/dead-man"))
	out := m.refused("dead-man")
	if !strings.Contains(out, "--checks") || len(o.saw()) != 0 {
		t.Errorf("with no check, it asked %v and said:\n%s", o.saw(), out)
	}

	// What stands where the address should be is never handed to curl.
	for name, text := range map[string]string{
		"another site":       "https://example.com/" + checkIDs["dead-man"] + "\n",
		"an option for curl": o.checks.URL + "/" + checkIDs["dead-man"] + "\"\noutput = \"" + m.path("var/lab/log/taken") + "\n",
		"two addresses":      o.checks.URL + "/" + checkIDs["dead-man"] + "\n" + o.checks.URL + "/" + checkIDs["backup"] + "\n",
		"no id":              o.checks.URL + "/\n",
	} {
		write(t, m.path(checksFolder+"/dead-man"), text, 0o600)
		out := m.refused("dead-man")
		if !strings.Contains(out, "is not a check's address") || len(o.pinged()) != 0 || exists(m.path("var/lab/log/taken")) {
			t.Errorf("%s: pinged %v and said:\n%s", name, o.pinged(), out)
		}
	}
}

func TestTheDeadManSwitchSaysWhenItsCheckCannotBeReached(t *testing.T) {
	m := newMini(t)
	o := m.withOutside()
	o.set(func(o *outside) { o.checksDown = true })
	out := m.refused("dead-man")
	if !strings.Contains(out, "the check dead-man was not reached") || strings.Contains(out, checkIDs["dead-man"]) {
		t.Errorf("with healthchecks.io refusing:\n%s", out)
	}
}

func TestTheDeadManSwitchsDryRun(t *testing.T) {
	m := newMini(t)
	o := m.withOutside()
	out := m.ok("dead-man", "--dry-run")
	want := "dead-man: would fetch " + o.door.URL + "/healthz and, only if labd answers ok there through Cloudflare, ping the check dead-man\n" +
		"dead-man: nothing was run\n"
	if out != want {
		t.Errorf("the dry run says\n%s\nwant\n%s", out, want)
	}
	if len(o.saw()) != 0 || m.calls() != "" {
		t.Errorf("the dry run asked %v", o.saw())
	}
	if out := m.refused("dead-man", "--often"); !strings.Contains(out, "scripts/mini/dead-man [--dry-run]") {
		t.Errorf("an argument it does not know:\n%s", out)
	}
}

func TestTheBackupPingsACheckOfItsOwnOnlyAfterAGoodRun(t *testing.T) {
	b := newBackedUp(t)
	o := b.withOutside()
	out := b.ok("backup")
	if got := o.pinged(); !same(got, []string{"backup"}) {
		t.Errorf("a good run pinged %v, want its own check once\n%s", got, out)
	}
	noCheckInAnArgument(t, o, b.calls())

	b = newBackedUp(t)
	o = b.withOutside()
	write(t, filepath.Join(b.state, "restic.fails"), "offsite backup\n", 0o644)
	out = b.refused("backup")
	if got := o.saw(); len(got) != 0 {
		t.Errorf("a failed run reached %v\n%s", got, out)
	}

	// A check that cannot be reached does not make a good backup a failed one.
	b = newBackedUp(t)
	o = b.withOutside()
	o.set(func(o *outside) { o.checksDown = true })
	out = b.ok("backup")
	if !strings.Contains(out, "the check backup was not reached") || !exists(b.path("var/lab/log/backup.ok")) || b.told() != "" {
		t.Errorf("with healthchecks.io refusing:\n%s", out)
	}

	// Before the owner has made the checks, the backup runs as it did.
	b = newBackedUp(t)
	out = b.ok("backup")
	if !strings.Contains(out, "no check to ping for backup yet") || !exists(b.path("var/lab/log/backup.ok")) {
		t.Errorf("with no check:\n%s", out)
	}
}

// watched is a mini with a Deployed build that carries the three scripts.
func watched(t *testing.T) (*mini, key) {
	t.Helper()
	m, k := withBackups(t)
	for _, s := range []string{"dead-man", "restore-drill"} {
		write(t, m.path("usr/local/lab/builds/v0.2.0-rc.1/scripts/mini/"+s), "#!/bin/bash\n", 0o755)
	}
	return m, k
}

// checksCarried is the folder the owner types the three addresses into.
func checksCarried(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		write(t, filepath.Join(dir, name), "https://hc-ping.com/"+checkIDs[name]+"\n", 0o600)
	}
	return dir
}

func TestSetupPutsTheChecksInPlaceAndStartsTheDeadManSwitch(t *testing.T) {
	m, k := watched(t)
	folder := checksCarried(t, "dead-man", "backup", "restore-drill")

	out := m.ok("setup", "--dry-run", "--release-key", k.signers, "--checks", folder)
	for _, want := range []string{
		"would make " + m.path(checksFolder),
		"would put the check dead-man at " + m.path(checksFolder) + "/dead-man",
		"would put the check restore-drill at " + m.path(checksFolder) + "/restore-drill",
		"would start the Dead-man switch's ping (" + deadManLabel + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not say it %s:\n%s", want, out)
		}
	}
	if exists(m.path(checksFolder)) || !exists(folder+"/dead-man") || m.daemonLoaded(deadManLabel) {
		t.Fatal("the dry run changed something")
	}

	out = m.ok("setup", "--release-key", k.signers, "--checks", folder)
	for name, id := range checkIDs {
		if strings.Contains(out, id) || strings.Contains(m.calls(), id) {
			t.Errorf("the set-up printed the check %s, or handed it to a program", name)
		}
		file := m.path(checksFolder + "/" + name)
		if got := read(t, file); got != "https://hc-ping.com/"+id+"\n" {
			t.Errorf("the check %s is %q in place", name, got)
		}
		if mode(t, file) != 0o400 {
			t.Errorf("the check %s is %v, want it readable by its owner alone", name, mode(t, file))
		}
		if exists(filepath.Join(folder, name)) {
			t.Errorf("the check %s was left in the folder it came in", name)
		}
		if !regexp.MustCompile(`(?m)^as-lab /bin/sh -c .* ` + regexp.QuoteMeta(file) + ` 400$`).MatchString(m.calls()) {
			t.Errorf("the check %s was not written by the lab account:\n%s", name, m.calls())
		}
	}
	if mode(t, m.path(checksFolder)) != 0o700 {
		t.Errorf("the checks' folder is %v", mode(t, m.path(checksFolder)))
	}
	if !m.daemonLoaded(deadManLabel) {
		t.Errorf("the Dead-man switch's ping is not loaded:\n%s", out)
	}
	if read(t, m.path("Library/LaunchDaemons/"+deadManLabel+".plist")) != read(t, filepath.Join(repo(t), "deploy", deadManLabel+".plist")) {
		t.Error("the Dead-man switch's file is not the repository's")
	}

	if out := m.ok("setup"); !strings.Contains(out, "setup: nothing to change") {
		t.Errorf("a second run:\n%s", out)
	}

	// A check made again at healthchecks.io: its new address replaces the old.
	again := t.TempDir()
	write(t, again+"/backup", "https://hc-ping.com/44444444-4444-4444-8444-444444444444\n", 0o600)
	before := m.calls()
	m.ok("setup", "--checks", again)
	if got := read(t, m.path(checksFolder+"/backup")); !strings.Contains(got, "44444444") || exists(again+"/backup") {
		t.Errorf("after a new address the check backup is %q", got)
	}
	if strings.Contains(strings.TrimPrefix(m.calls(), before), "bootstrap") {
		t.Error("a new address restarted a daemon, and each run reads the address afresh")
	}
}

func TestSetupStartsNoDeadManSwitchBeforeItHasItsCheck(t *testing.T) {
	m, k := watched(t)
	out := m.ok("setup", "--release-key", k.signers)
	if !strings.Contains(out, "the Dead-man switch is not started: it has no check to ping yet (--checks FOLDER") || m.daemonLoaded(deadManLabel) {
		t.Errorf("with no check:\n%s", out)
	}
	// The other two alone start nothing either.
	out = m.ok("setup", "--checks", checksCarried(t, "backup", "restore-drill"))
	if m.daemonLoaded(deadManLabel) || !strings.Contains(out, "the Dead-man switch is not started") {
		t.Errorf("with the backup's and the drill's checks alone:\n%s", out)
	}

	// Its check, and no build to run from.
	m, k = withBackups(t)
	out = m.ok("setup", "--release-key", k.signers, "--checks", checksCarried(t, "dead-man"))
	if m.daemonLoaded(deadManLabel) || !strings.Contains(out, "the Dead-man switch is not started: the Deployed build has no scripts/mini/dead-man") {
		t.Errorf("with a build older than the switch:\n%s", out)
	}
}

func TestSetupRefusesChecksThatAreNotWhatTheyShouldBe(t *testing.T) {
	m, k := watched(t)
	m.ok("setup", "--release-key", k.signers)
	carried := func(name, text string, perm os.FileMode) string {
		dir := t.TempDir()
		write(t, filepath.Join(dir, name), text, perm)
		return dir
	}
	id := checkIDs["dead-man"]
	for name, c := range map[string]struct {
		folder, want string
	}{
		"others can read it":      {carried("dead-man", "https://hc-ping.com/"+id+"\n", 0o644), "can be read by others"},
		"another site":            {carried("dead-man", "https://example.com/"+id+"\n", 0o600), "is not a check's address"},
		"not https":               {carried("dead-man", "http://hc-ping.com/"+id+"\n", 0o600), "is not a check's address"},
		"a loopback address":      {carried("dead-man", "http://127.0.0.1:8000/"+id+"\n", 0o600), "is not a check's address"},
		"two lines":               {carried("backup", "https://hc-ping.com/"+id+"\nhttps://hc-ping.com/"+id+"\n", 0o600), "is not a check's address"},
		"more after the id":       {carried("backup", "https://hc-ping.com/"+id+"/fail\n", 0o600), "is not a check's address"},
		"an empty file":           {carried("restore-drill", "", 0o600), "is empty or no plain file"},
		"a name it does not know": {carried("weekly", "https://hc-ping.com/"+id+"\n", 0o600), "holds none of"},
		"no folder":               {"/nonexistent", "no folder"},
	} {
		out := m.refused("setup", "--checks", c.folder)
		if !strings.Contains(out, c.want) || strings.Contains(out, id) {
			t.Errorf("%s: %s", name, out)
		}
	}
	if exists(m.path(checksFolder)) || m.daemonLoaded(deadManLabel) {
		t.Error("a refused run changed something")
	}
}

func TestSetupRefusesALinkPlantedWhereACheckGoes(t *testing.T) {
	for name, toFolder := range map[string]bool{
		"checks":              true,
		"checks/dead-man.new": false,
		"checks/dead-man":     false,
	} {
		t.Run(name, func(t *testing.T) {
			m, k := watched(t)
			m.ok("setup", "--release-key", k.signers, "--checks", checksCarried(t, "backup"))
			v := m.victim()
			link, to := m.path("var/lab/secrets/"+name), v.file
			if toFolder {
				to = v.dir
			}
			plant(t, link, to)
			folder := checksCarried(t, "dead-man")

			out := m.refused("setup", "--checks", folder)

			saysLink(t, out, link)
			v.intact("a link at secrets/" + name)
			if !exists(folder+"/dead-man") || m.daemonLoaded(deadManLabel) {
				t.Error("the owner's copy of the check was removed, or the Dead-man switch started")
			}
		})
	}
}

func TestSetupSchedulesTheRestoreDrillWithTheNightlyBackup(t *testing.T) {
	m, k := watched(t)
	out := m.ok("setup", "--release-key", k.signers, "--backup-guest", "192.168.0.186", "--backup-keys", keysFolder(t, houseSecrets))
	if m.daemonLoaded(drillLabel) {
		t.Errorf("the Restore drill is scheduled before the backup it restores from:\n%s", out)
	}
	out = m.ok("setup", "--dry-run", "--backup-keys", keysFolder(t, offsiteSecrets))
	if !strings.Contains(out, "would schedule the Restore drill ("+drillLabel+")") {
		t.Errorf("the dry run does not say it would schedule the Restore drill:\n%s", out)
	}
	out = m.ok("setup", "--backup-keys", keysFolder(t, offsiteSecrets))
	if !m.daemonLoaded(drillLabel) || !m.daemonLoaded(backupLabel) {
		t.Errorf("with every key in place:\n%s", out)
	}
	if read(t, m.path("Library/LaunchDaemons/"+drillLabel+".plist")) != read(t, filepath.Join(repo(t), "deploy", drillLabel+".plist")) {
		t.Error("the Restore drill's file is not the repository's")
	}
	if out := m.ok("setup"); !strings.Contains(out, "setup: nothing to change") {
		t.Errorf("a second run:\n%s", out)
	}

	// A Deployed build from before the drill: the backup is scheduled, the drill waits.
	m, k = withBackups(t)
	out = m.ok("setup", "--release-key", k.signers, "--backup-guest", "192.168.0.186", "--backup-keys", keysFolder(t, houseSecrets, offsiteSecrets))
	if m.daemonLoaded(drillLabel) || !m.daemonLoaded(backupLabel) || !strings.Contains(out, "the Restore drill is not scheduled: the Deployed build has no scripts/mini/restore-drill") {
		t.Errorf("with a build older than the drill:\n%s", out)
	}
}

func TestTheDeadManSwitchsAndTheDrillsLaunchDaemons(t *testing.T) {
	values, args, text := daemon(t, deadManLabel)
	if !same(args, []string{"/usr/local/lab/current/scripts/mini/dead-man"}) {
		t.Errorf("the Dead-man switch's ping runs %v, want the Deployed build's script and no argument", args)
	}
	if values["StartInterval"] != "60" || values["RunAtLoad"] != "true" || values["KeepAlive"] != "" {
		t.Errorf("the Dead-man switch's ping does not run once a minute: %v", values)
	}
	// One address only: the property list format's own.
	if strings.Contains(text, "hc-ping") || strings.Count(text, "http") != 1 {
		t.Error("the Dead-man switch's file names an address")
	}

	values, args, text = daemon(t, drillLabel)
	if !same(args, []string{"/usr/local/lab/current/scripts/mini/restore-drill"}) {
		t.Errorf("the Restore drill runs %v, want the Deployed build's script and no argument", args)
	}
	if values["KeepAlive"] != "" || values["RunAtLoad"] != "" || values["StartInterval"] != "" {
		t.Error("the Restore drill runs more often than monthly")
	}
	if !regexp.MustCompile(`<key>StartCalendarInterval</key>\s*<dict>\s*<key>Day</key><integer>1</integer>\s*<key>Hour</key><integer>5</integer>\s*<key>Minute</key><integer>30</integer>\s*</dict>`).MatchString(text) {
		t.Error("the Restore drill is not at 05:30 on the first of each month")
	}

	for name, want := range map[string]string{
		"DEAD_MAN_LABEL": deadManLabel,
		"DRILL_LABEL":    drillLabel,
		"CHECKS":         "$LAB_HOME/secrets/checks",
		"CHECK_NAMES":    "dead-man backup restore-drill",
		"CHECKS_AT":      "https://hc-ping.com",
		"DRILL_OK":       "$LAB_HOME/log/restore-drill.ok",
	} {
		if got := shell(t, name); got != want {
			t.Errorf("scripts/mini/lib.sh: %s is %q, want %q", name, got, want)
		}
	}
}
