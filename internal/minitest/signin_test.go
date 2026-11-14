package minitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/server"
)

// answer starts scripts/mini/sign-in and types answers at its prompts.
func (m *mini) answer(answers string, args ...string) (string, int) {
	m.t.Helper()
	cmd := exec.Command(filepath.Join(m.tree, "scripts", "mini", "sign-in"), args...)
	cmd.Env = m.env
	cmd.Dir = filepath.Dir(m.tree)
	cmd.Stdin = strings.NewReader(answers)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		m.t.Fatalf("sign-in: %v\n%s", err, out)
	}
	return string(out), 0
}

// The owner types the OAuth app's values and the Admins' account ids at a
// prompt; they land where labd reads them, the lab account's alone, and
// nowhere else: not in what the script prints, not in a command's arguments.
func TestSignInFilesAreWrittenAtAPromptForLabdToRead(t *testing.T) {
	m := newMini(t)
	const secret = "0123456789abcdef0123456789abcdef01234567"
	if out, code := m.answer("Ov23liAbCdEf\n" + secret + "\n4711\n"); code == 0 || !strings.Contains(out, "scripts/mini/setup") {
		t.Fatalf("before the set-up made the folder: exit %d\n%s", code, out)
	}
	dir := m.path("/var/lab/secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	out, code := m.answer("Ov23liAbCdEf\n" + secret + "\n4711 12\n")
	if code != 0 {
		t.Fatalf("sign-in: exit %d\n%s", code, out)
	}
	if strings.Contains(out, secret) || strings.Contains(m.callsOrNone(), secret) {
		t.Errorf("the secret was printed or handed to a command:\n%s", out)
	}
	for _, name := range []string{"github-client-id", "github-client-secret", "admins"} {
		if got := mode(t, filepath.Join(dir, name)); got != 0o600 {
			t.Errorf("%s is %o, want 600", name, got)
		}
	}
	app, admins, err := server.ReadSignIn(dir, "")
	if err != nil || app == nil || app.ClientID != "Ov23liAbCdEf" || app.ClientSecret != secret || !slices.Equal(admins, []int64{4711, 12}) {
		t.Fatalf("labd reads %+v, %v, %v", app, admins, err)
	}
	if strings.Contains(m.callsOrNone(), "kickstart") {
		t.Error("labd was restarted though it is not loaded")
	}

	// An empty answer keeps what is there; a loaded labd is restarted.
	write(t, filepath.Join(filepath.Dir(m.root), "state", "loaded"), "", 0o644)
	if out, code := m.answer("\n\n99\n"); code != 0 {
		t.Fatalf("a second run: exit %d\n%s", code, out)
	}
	app, admins, err = server.ReadSignIn(dir, "")
	if err != nil || app.ClientID != "Ov23liAbCdEf" || app.ClientSecret != secret || !slices.Equal(admins, []int64{99}) {
		t.Errorf("after empty answers labd reads %+v, %v, %v", app, admins, err)
	}
	if !strings.Contains(m.calls(), "launchctl kickstart -k system/"+shell(t, "LABD_LABEL")) {
		t.Errorf("a loaded labd was not restarted:\n%s", m.calls())
	}

	// What is no account id, client id or secret is refused, and nothing changes.
	for _, bad := range []string{"\n\nowner\n", "\n\n47 x\n", "\n\n0\n", "two words\n\n\n", "\nshort\n\n"} {
		if out, code := m.answer(bad); code == 0 {
			t.Errorf("answers %q were taken:\n%s", bad, out)
		}
	}
	if _, admins, _ := server.ReadSignIn(dir, ""); !slices.Equal(admins, []int64{99}) {
		t.Errorf("a refused run changed the Admins to %v", admins)
	}

	// A dry run asks nothing and changes nothing.
	before := read(t, m.log)
	if out, code := m.answer("", "--dry-run"); code != 0 || !strings.Contains(out, "would") {
		t.Errorf("the dry run: exit %d\n%s", code, out)
	}
	if read(t, m.log) != before {
		t.Error("the dry run called something")
	}
}

func (m *mini) callsOrNone() string {
	if !exists(m.log) {
		return ""
	}
	return m.calls()
}
