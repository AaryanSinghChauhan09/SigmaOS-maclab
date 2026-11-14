package carrytest

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The lab account owns /var/lab, so whoever has its rights can put a link
// there. Root's steps write nothing through one: each is refused before
// anything is stopped, moved or written, and what the link points at stays
// as it was.
func TestALinkPlantedBelowVarLabIsRefusedBeforeAnythingIsChanged(t *testing.T) {
	m, dump, public := rehearsal(t)
	m.ok("old-lab", "stop")
	m.ok("old-lab", "copy", "--stage", m.stage)
	key := hex.EncodeToString(machineIDKey())
	if out, code := m.run(key+"\n", "rehearsal-import", "key"); code != 0 {
		t.Fatalf("the key: exit %d, %s", code, out)
	}
	elsewhere := filepath.Join(m.base, "elsewhere")
	write(t, elsewhere+"/kept", "not the lab's\n", 0o644)
	theirs := everyFile(t, elsewhere)

	upgrade := []string{"old-lab", "upgrade", "--stage", m.stage, "--account-id", "7", "owner"}
	run := []string{"rehearsal-import", "run", "--public-key", public, "--dump", dump}
	for _, c := range []struct {
		link    string
		script  []string
		typed   string
		restore bool
	}{
		{"var/lab/data.old-lab", upgrade, "", false},
		{"var/lab/data.before-old-lab", upgrade, "", false},
		{"var/lab/data", upgrade, "", true},
		{"var/lab/import/reports-before", run, oldLink + "\n", false},
		{"var/lab/import", run, oldLink + "\n", true},
		{"var/lab/secrets/machine-id-key", []string{"rehearsal-import", "key"}, strings.Repeat("cd", 64) + "\n", true},
		{"var/lab/secrets", []string{"rehearsal-import", "key"}, strings.Repeat("cd", 64) + "\n", true},
	} {
		link := m.path(c.link)
		aside := link + ".aside"
		if c.restore {
			if err := os.Rename(link, aside); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		os.Remove(m.log)
		out, code := m.run(c.typed, c.script[0], c.script[1:]...)
		if code == 0 || !strings.Contains(out, "is a symbolic link") {
			t.Errorf("%s with %s a link: exit %d\n%s", strings.Join(c.script[:2], " "), c.link, code, out)
		}
		if got := everyFile(t, elsewhere); !reflect.DeepEqual(got, theirs) {
			t.Errorf("%s wrote through the link at %s: %v", c.script[0], c.link, changed(theirs, got))
		}
		for _, never := range []string{"launchctl bootout", "as-lab mv", "as-lab rm", "as-lab tar", "old-lab upgrade", "import load"} {
			if strings.Contains(m.calls(), never) {
				t.Errorf("with %s a link, %s still ran %q:\n%s", c.link, c.script[0], never, m.calls())
			}
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if c.restore {
			if err := os.Rename(aside, link); err != nil {
				t.Fatal(err)
			}
		}
	}
	if read(t, m.path("var/lab/secrets/machine-id-key")) != key+"\n" {
		t.Error("the machine-id key changed")
	}
	// With the links gone both go through.
	m.ok("old-lab", "upgrade", "--stage", m.stage, "--account-id", "7", "owner")
	if out, code := m.run(oldLink+"\n", "rehearsal-import", "run", "--public-key", public, "--dump", dump); code != 0 {
		t.Errorf("the rehearsal after the old lab's data went in: exit %d\n%s", code, out)
	}
}
