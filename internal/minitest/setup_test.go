package minitest

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// me is the account the tests run as, which plays both root and lab on the
// made-up machine.
func me(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Uid
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// sealed fails unless everything under dir belongs to the account playing
// root and nobody else can write any of it.
func sealed(t *testing.T, dir string) {
	t.Helper()
	uid := me(t)
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && fmt.Sprint(st.Uid) != uid {
			t.Errorf("%s belongs to uid %d", p, st.Uid)
		}
		if info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o022 != 0 {
			t.Errorf("%s is writable by others: %v", p, info.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetupDryRunSaysWhatWouldChangeAndChangesNothing(t *testing.T) {
	m := newMini(t)
	k := newKey(t)

	out := m.ok("setup", "--dry-run", "--release-key", k.signers)

	for _, want := range []string{"would", "/Users/lab", m.path("var/lab/data"), m.path("usr/local/lab/allowed_signers")} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run doesn't mention %s:\n%s", want, out)
		}
	}
	if exists(m.path("usr/local/lab")) || exists(m.path("var/lab")) {
		t.Error("a dry run made folders")
	}
	if strings.Contains(m.calls(), "-create") {
		t.Errorf("a dry run made an account:\n%s", m.calls())
	}
}

func TestSetupMakesTheAccountItsFoldersAndThePinOnce(t *testing.T) {
	m := newMini(t)
	k := newKey(t)

	m.ok("setup", "--release-key", k.signers)

	calls := m.calls()
	for _, want := range []string{
		"-create /Users/lab UserShell /usr/bin/false",
		"-create /Users/lab NFSHomeDirectory /var/lab",
		"-create /Users/lab Password *",
		"-create /Users/lab IsHidden 1",
		"-create /Groups/lab PrimaryGroupID 300",
		"-create /Users/lab UniqueID 300",
		"-create /Users/lab PrimaryGroupID 300",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("the account: no %q in\n%s", want, calls)
		}
	}
	for path, want := range map[string]os.FileMode{
		"usr/local/lab":                 0o755,
		"usr/local/lab/builds":          0o755,
		"usr/local/lab/tools":           0o755,
		"usr/local/lab/allowed_signers": 0o644,
		"var/lab":                       0o700,
		"var/lab/data":                  0o700,
		"var/lab/secrets":               0o700,
		"var/lab/run":                   0o700,
		"var/lab/log":                   0o700,
	} {
		if got := mode(t, m.path(path)); got != want {
			t.Errorf("%s is %v, want %v", path, got, want)
		}
	}
	if read(t, m.path("usr/local/lab/allowed_signers")) != read(t, k.signers) {
		t.Error("the pinned release key is not the one given")
	}

	// Safe to run again, with or without the key: nothing changes.
	os.Remove(m.log)
	for _, args := range [][]string{{"--release-key", k.signers}, {}, {"--dry-run"}} {
		out := m.ok("setup", args...)
		if !strings.Contains(out, "nothing to change") {
			t.Errorf("a second run (%v) changed something:\n%s", args, out)
		}
	}
	if strings.Contains(m.calls(), "-create") {
		t.Errorf("a second run made the account again:\n%s", m.calls())
	}
}

func TestSetupNeedsAReleaseKeyTheFirstTime(t *testing.T) {
	m := newMini(t)
	out := m.refused("setup")
	if !strings.Contains(out, "--release-key") {
		t.Errorf("the refusal doesn't name --release-key:\n%s", out)
	}
	write(t, m.path("not-a-key"), "hello\n", 0o644)
	m.refused("setup", "--release-key", m.path("not-a-key"))
	if exists(m.path("usr/local/lab/allowed_signers")) {
		t.Error("something that is not a key was pinned")
	}
}

func TestSetupReplacesThePinOnlyWhenGivenAnotherKey(t *testing.T) {
	m := newMini(t)
	first, second := newKey(t), newKey(t)
	m.ok("setup", "--release-key", first.signers)
	out := m.ok("setup", "--release-key", second.signers)
	if read(t, m.path("usr/local/lab/allowed_signers")) != read(t, second.signers) {
		t.Errorf("the pin was not replaced:\n%s", out)
	}
	if !strings.Contains(out, "release key") {
		t.Errorf("no word that the release key changed:\n%s", out)
	}
}

// upgrade plays a Maintenance run's brew upgrade of formula to version.
func (m *mini) upgrade(formula, version string) {
	m.t.Helper()
	keg := m.path("opt/homebrew/Cellar/" + formula + "/" + version)
	write(m.t, keg+"/bin/"+formula, "#!/bin/sh\necho "+formula+" "+version+"\n", 0o775)
	os.RemoveAll(filepath.Dir(keg) + "/1.0")
	os.Remove(m.path("opt/homebrew/opt/" + formula))
	if err := os.Symlink("../Cellar/"+formula+"/"+version, m.path("opt/homebrew/opt/"+formula)); err != nil {
		m.t.Fatal(err)
	}
}

func TestSetupInstallsTheHomebrewListAndCopiesItForRoot(t *testing.T) {
	m := newMini(t)
	k := newKey(t)
	write(t, m.tree+"/deploy/homebrew-formulae", "# what the lab runs\nmadeup   # a made-up tool\n", 0o644)
	write(t, m.path("opt/homebrew/deps/madeup"), "madeup-runtime\n", 0o644)
	tools := m.path("usr/local/lab/tools")

	out := m.ok("setup", "--dry-run", "--release-key", k.signers)
	if !strings.Contains(out, "install madeup") || exists(m.path("opt/homebrew/Cellar")) {
		t.Errorf("the dry run: brew install is not announced, or ran:\n%s", out)
	}

	m.ok("setup", "--release-key", k.signers)

	if got := read(t, tools+"/current/madeup/bin/madeup"); !strings.Contains(got, "madeup 1.0") {
		t.Fatalf("the tool is not in the copy: %q", got)
	}
	if !exists(tools + "/current/madeup-runtime/bin/madeup-runtime") {
		t.Error("what the tool needs to run is not in the copy")
	}
	if got := read(t, tools+"/current/VERSIONS"); got != "madeup 1.0\nmadeup-runtime 1.0\n" {
		t.Errorf("VERSIONS: %q", got)
	}
	sealed(t, tools)
	calls := m.calls()
	if !strings.Contains(calls, "brew install madeup [auto_update=1 upgrade=1") {
		t.Errorf("brew was not told to leave itself and what is installed alone:\n%s", calls)
	}

	// Again: nothing is installed twice, nothing is copied twice.
	os.Remove(m.log)
	out = m.ok("setup")
	if !strings.Contains(out, "nothing to change") || exists(tools+"/previous") {
		t.Errorf("a second run changed something:\n%s", out)
	}
	for _, never := range []string{"brew install", "brew upgrade", "brew update"} {
		if strings.Contains(m.calls(), never) {
			t.Errorf("a second run called %s:\n%s", never, m.calls())
		}
	}

	// After an upgrade the new copy takes over and the one it replaced stays.
	m.upgrade("madeup", "2.0")
	m.ok("setup")
	if got := read(t, tools+"/current/madeup/bin/madeup"); !strings.Contains(got, "madeup 2.0") {
		t.Errorf("the copy in use: %q", got)
	}
	if got := read(t, tools+"/previous/madeup/bin/madeup"); !strings.Contains(got, "madeup 1.0") {
		t.Errorf("the copy it replaced: %q", got)
	}
	sealed(t, tools)

	// One way back is kept, not every copy ever made.
	m.upgrade("madeup", "3.0")
	m.ok("setup")
	if got := read(t, tools+"/previous/madeup/bin/madeup"); !strings.Contains(got, "madeup 2.0") {
		t.Errorf("the copy it replaced: %q", got)
	}
	entries, _ := os.ReadDir(tools)
	if len(entries) != 4 {
		t.Errorf("tools holds %d entries, want two copies and the two links", len(entries))
	}
}

func TestSetupInstallsAPinByItsChecksum(t *testing.T) {
	m := newMini(t)
	k := newKey(t)
	pkg := "a made-up package\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, pkg) }))
	t.Cleanup(srv.Close)
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(pkg)))
	pin := func(version, sum string) {
		write(t, m.tree+"/deploy/pins", "# name version sha256 url\nmadeup "+version+" "+sum+" "+srv.URL+"/madeup-"+version+".pkg\n", 0o644)
	}

	pin("1.2.3", strings.Repeat("0", 64))
	out := m.refused("setup", "--release-key", k.signers)
	if !strings.Contains(out, "checksum") || strings.Contains(m.calls(), "installer") {
		t.Errorf("a package with another checksum was not refused:\n%s\n%s", out, m.calls())
	}

	pin("1.2.3", sum)
	out = m.ok("setup", "--dry-run")
	if !strings.Contains(out, "madeup 1.2.3") || strings.Contains(m.calls(), "installer") {
		t.Errorf("the dry run: the pin is not announced, or was installed:\n%s", out)
	}
	m.ok("setup")
	if n := strings.Count(m.calls(), "installer -pkg "); n != 1 || !strings.Contains(m.calls(), "-target /") {
		t.Errorf("the package was installed %d times:\n%s", n, m.calls())
	}
	if out := m.ok("setup"); !strings.Contains(out, "nothing to change") || strings.Count(m.calls(), "installer -pkg ") != 1 {
		t.Errorf("a second run installed the pin again:\n%s", out)
	}

	pin("1.2.4", sum)
	m.ok("setup")
	if n := strings.Count(m.calls(), "installer -pkg "); n != 2 {
		t.Errorf("a changed pin was installed %d times in all, want 2", n)
	}
}
