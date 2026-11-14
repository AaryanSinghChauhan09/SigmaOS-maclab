package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/oldlabtest"
)

const oldLabCounted = "records 2\njobs 253\nbuilds 90\n"

// labd old-lab counts and upgrades the old lab's data directory: a dry run
// first, which changes nothing, then the upgrade, once.
func TestOldLabCountsAndUpgradesTheOldLabsDataOnce(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	old := oldlabtest.Make(t, data, oldlabtest.OnOmarchyGPU)
	run := func(args ...string) (int, string, string) {
		var out, said bytes.Buffer
		code := oldLab(args, &out, &said)
		return code, out.String(), said.String()
	}
	if code, out, said := run("counts", "--data", data); code != 0 || out != oldLabCounted {
		t.Fatalf("counts: exit %d, %q, %q", code, out, said)
	}
	db, err := os.ReadFile(filepath.Join(data, "labd.db"))
	if err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{},
		{"upgrade", "--data", data},
		{"upgrade", "--data", data, "owner"},
		{"upgrade", "--data", data, "--account-id", "7", "owner", "another"},
		{"upgrade", "--data", data, "--account-id", "7", "--admin-token", old.AdminToken, "owner"},
		{"counts", "--data", data, "more"},
		{"forget", "--data", data},
	} {
		code, out, said := run(args...)
		if code != 2 || out != "" || strings.Contains(said, old.AdminToken) {
			t.Errorf("labd old-lab %v: exit %d, %q, %q", args, code, out, said)
		}
	}
	if code, out, _ := run("upgrade", "--data", t.TempDir(), "--account-id", "7", "owner"); code != 1 || out != "" {
		t.Errorf("on a folder with no labd.db: exit %d, %q", code, out)
	}

	code, would, said := run("upgrade", "--dry-run", "--data", data, "--account-id", "7", "@owner")
	if code != 0 || !strings.Contains(would, "A dry run") {
		t.Fatalf("the dry run: exit %d, %q, %q", code, would, said)
	}
	if now, _ := os.ReadFile(filepath.Join(data, "labd.db")); !bytes.Equal(now, db) {
		t.Error("the dry run changed labd.db")
	}
	if _, err := os.Stat(filepath.Join(data, "admin.token")); err != nil {
		t.Errorf("the dry run removed admin.token: %v", err)
	}

	code, did, said := run("upgrade", "--data", data, "--account-id", "7", "@owner")
	if code != 0 {
		t.Fatalf("the upgrade: exit %d, %q, %q", code, did, said)
	}
	for _, line := range []string{
		"records 2 (2 before)\n", "jobs 253 (253 before)\n", "builds 90 (90 before)\n",
		"Retired: m1pro, m1pro2\n", "Lease ended: m1pro\n", "out-of-band entry cleared: m1pro, m1pro2\n",
		"@owner (GitHub account 7)", "admin.token", "builder.token",
	} {
		if !strings.Contains(did, line) {
			t.Errorf("the upgrade did not say %q:\n%s", line, did)
		}
	}
	// The dry run said the same, under its own first line.
	if _, rest, _ := strings.Cut(would, "\n"); !strings.HasSuffix(did, rest) {
		t.Errorf("the dry run said\n%s\nand the upgrade\n%s", would, did)
	}
	for _, secret := range []string{old.AdminToken, old.SharedBuilder, old.EnrollToken, old.Records[0].Secret} {
		if strings.Contains(would+did+said, secret) {
			t.Errorf("the command printed a secret of the old lab")
		}
	}
	if code, out, said := run("counts", "--data", data); code != 0 || out != oldLabCounted {
		t.Errorf("counts after the upgrade: exit %d, %q, %q", code, out, said)
	}
	if code, out, said := run("upgrade", "--data", data, "--account-id", "7", "owner"); code != 1 || out != "" || !strings.Contains(said, "already") || !strings.Contains(said, "Nothing was changed") {
		t.Errorf("a second upgrade: exit %d, %q, %q", code, out, said)
	}
}
