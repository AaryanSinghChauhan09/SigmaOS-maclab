package minitest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The lab account owns /var/lab and may put anything there. What it must
// never get is what root owns: the builds root's daemons run from. victim is
// a made-up build with one program; intact fails if any of it changed.
type victim struct {
	t         *testing.T
	dir, file string
}

const victimText = "#!/bin/sh\n# root runs this\n"

func (m *mini) victim() victim {
	m.t.Helper()
	v := victim{t: m.t, dir: m.path("usr/local/lab/builds/v9.9.9/bin")}
	v.file = v.dir + "/oobd"
	write(m.t, v.file, victimText, 0o755)
	if err := os.Chmod(v.dir, 0o755); err != nil {
		m.t.Fatal(err)
	}
	return v
}

func (v victim) intact(when string) {
	v.t.Helper()
	if got := mode(v.t, v.dir); got != 0o755 {
		v.t.Errorf("%s: root's folder is now %v", when, got)
	}
	if got := mode(v.t, v.file); got != 0o755 {
		v.t.Errorf("%s: root's program is now %v", when, got)
	}
	if got := read(v.t, v.file); got != victimText {
		v.t.Errorf("%s: root's program now reads %q", when, got)
	}
	entries, _ := os.ReadDir(v.dir)
	if len(entries) != 1 {
		v.t.Errorf("%s: root's folder now holds %d entries", when, len(entries))
	}
}

// plant puts a link at path, in place of whatever is there.
func plant(t *testing.T, path, to string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(to, path); err != nil {
		t.Fatal(err)
	}
}

func saysLink(t *testing.T, out, path string) {
	t.Helper()
	if !strings.Contains(out, "symbolic link") || !strings.Contains(out, path) {
		t.Errorf("the refusal doesn't name the link %s:\n%s", path, out)
	}
}

func TestSetupRefusesALinkPlantedWhereALabFolderGoes(t *testing.T) {
	need(t, "openssl")
	for _, name := range []string{"data", "secrets", "run", "log"} {
		t.Run(name, func(t *testing.T) {
			m := newMini(t)
			k := newKey(t)
			m.ok("setup", "--release-key", k.signers)
			v := m.victim()
			link := m.path("var/lab/" + name)
			plant(t, link, v.dir)
			os.Remove(m.log)

			out := m.refused("setup")

			saysLink(t, out, link)
			v.intact("a link at " + name)
			if calls := m.callsOrNone(); strings.Contains(calls, "as-lab") || strings.Contains(calls, "bootstrap") {
				t.Errorf("the refused run changed something:\n%s", calls)
			}
			saysLink(t, m.refused("setup", "--dry-run"), link)
		})
	}
}

func TestSetupRefusesALinkPlantedWhereLabdsTokenGoes(t *testing.T) {
	need(t, "openssl")
	for _, name := range []string{"oobd.token.new", "oobd.token"} {
		t.Run(name, func(t *testing.T) {
			m := newMini(t)
			k := newKey(t)
			m.ok("setup", "--release-key", k.signers)
			v := m.victim()
			// Its own copy gone, the lab account has the next set-up write it again.
			os.Remove(m.path(labToken))
			link := m.path("var/lab/secrets/" + name)
			plant(t, link, v.file)

			out := m.refused("setup")

			saysLink(t, out, link)
			v.intact("a link at " + name)
			if to, err := os.Readlink(link); err != nil || to != v.file {
				t.Errorf("the link was touched: %q, %v", to, err)
			}
		})
	}
}

func TestSetupRefusesALinkPlantedWhereTheTunnelsCredentialGoes(t *testing.T) {
	need(t, "openssl")
	m, k := withTunnel(t)
	m.ok("setup", "--release-key", k.signers)
	v := m.victim()
	cred := credentialFile(t, madeUpCredential+"\n", 0o600)
	link := m.path(strings.TrimPrefix(tunnelToken, "/") + ".new")
	plant(t, link, v.file)

	out := m.refused("setup", "--tunnel-credential", cred)

	saysLink(t, out, link)
	v.intact("a link at the credential's temporary name")
	if !exists(cred) || m.tunnelLoaded() {
		t.Error("the owner's copy of the credential was removed, or the tunnel started")
	}
}

func TestSignInRefusesALinkPlantedAmongTheSecrets(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef01234567"
	answers := "Ov23liAbCdEf\n" + secret + "\n4711\n"
	for name, toFolder := range map[string]bool{
		"github-client-secret.new": false,
		"github-client-secret":     false,
		"admins":                   true,
		"admins.new":               true,
		"":                         true,
	} {
		t.Run("secrets/"+name, func(t *testing.T) {
			m := newMini(t)
			v := m.victim()
			dir := m.path("var/lab/secrets")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			link, to := filepath.Join(dir, name), v.file
			if toFolder {
				to = v.dir
			}
			plant(t, link, to)

			out, code := m.answer(answers)

			if code == 0 {
				t.Fatalf("sign-in wrote through a link:\n%s", out)
			}
			saysLink(t, out, link)
			v.intact("a link at secrets/" + name)
			if strings.Contains(out, secret) || strings.Contains(m.callsOrNone(), secret) {
				t.Errorf("the secret was printed or handed to a command:\n%s", out)
			}
			if name != "" && exists(dir+"/github-client-id") {
				t.Error("the refused run wrote the client id")
			}
		})
	}
}

// With no link planted, what root's scripts put below /var/lab is written by
// the lab account, which reaches only what it could already write: a link
// swapped in while a script runs, or a hard link, gets root's hand nowhere.
func TestRootWritesBelowVarLabOnlyAsTheLabAccount(t *testing.T) {
	need(t, "openssl")
	m, k := withTunnel(t)
	cred := credentialFile(t, madeUpCredential+"\n", 0o600)
	m.ok("setup", "--release-key", k.signers, "--tunnel-credential", cred)
	if out, code := m.answer("Ov23liAbCdEf\n0123456789abcdef0123456789abcdef01234567\n4711\n"); code != 0 {
		t.Fatalf("sign-in: exit %d\n%s", code, out)
	}

	calls := m.calls()
	for _, name := range []string{"data", "secrets", "run", "log"} {
		if want := "as-lab mkdir " + m.path("var/lab/"+name); !strings.Contains(calls, want) {
			t.Errorf("%s was not made by the lab account:\n%s", name, calls)
		}
	}
	for _, name := range []string{"oobd.token", "cloudflared.token", "github-client-id", "github-client-secret", "admins"} {
		file := m.path("var/lab/secrets/" + name)
		if !regexp.MustCompile(`(?m)^as-lab /bin/sh -c .* ` + regexp.QuoteMeta(file) + ` [46]00$`).MatchString(calls) {
			t.Errorf("%s was not written by the lab account:\n%s", name, calls)
		}
		if exists(file + ".new") {
			t.Errorf("%s.new was left behind", name)
		}
	}
	if mode(t, m.path("var/lab/secrets/cloudflared.token")) != 0o400 || mode(t, m.path(labToken)) != 0o600 {
		t.Error("the lab account's secrets are not its own alone")
	}

	// A file left under the temporary name by a run that was cut short is
	// replaced: only a link there is refused.
	write(t, m.path(labToken)+".new", "half a token", 0o600)
	os.Remove(m.path(labToken))
	m.ok("setup")
	if read(t, m.path(labToken)) != read(t, m.path(rootToken)) || exists(m.path(labToken)+".new") {
		t.Error("a leftover temporary file kept labd's copy from being written")
	}

	// Outside the tests that account is reached through sudo, and root gives
	// nothing away below /var/lab: the one owner it sets there is the
	// folder's own, without following a link.
	lib := read(t, filepath.Join(repo(t), "scripts", "mini", "lib.sh"))
	if !strings.Contains(lib, `AS_LAB=(sudo -n -u "$LAB_OWNER" --)`) {
		t.Error("scripts/mini/lib.sh does not run as_lab's commands as the lab account")
	}
	chown := regexp.MustCompile(`(^|\s)(as_lab )?chown ([^\n]*)`)
	for _, f := range scripts(t) {
		for _, line := range strings.Split(read(t, f), "\n") {
			c := chown.FindStringSubmatch(line)
			if c == nil || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			args := c[3]
			if strings.Contains(args, "LAB_OWNER") && !strings.HasPrefix(args, "-h ") {
				t.Errorf("%s gives something to the lab account through a link: %s", filepath.Base(f), strings.TrimSpace(line))
			}
			if strings.Contains(args, "LAB_OWNER") && c[2] == "" {
				t.Errorf("%s: root sets an owner below /var/lab: %s", filepath.Base(f), strings.TrimSpace(line))
			}
		}
	}
}
