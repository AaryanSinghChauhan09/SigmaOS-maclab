package repotest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const realPostgresTest = "TestTheReaderReadsARealPostgres"

// parityGate runs scripts/parity-gate in a tree whose Go, Ruby, Rails and
// Postgres are stand-ins that pass and write down how they were called, and
// returns what it said, its exit status and each call of go and rails.
func parityGate(t *testing.T, env ...string) (said string, status int, calls []string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	top, tree := root(t), t.TempDir()
	log := filepath.Join(tree, "calls")
	gate, err := os.ReadFile(filepath.Join(top, "scripts", "parity-gate"))
	if err != nil {
		t.Fatal(err)
	}
	const pass = "#!/bin/bash\nexit 0\n"
	for path, text := range map[string]string{
		"scripts/parity-gate":    string(gate),
		"site/.ruby-version":     "ruby-3.4.1\n",
		"site/bin/rails":         "#!/bin/bash\necho \"rails $*\" >> \"$CALLS\"\n",
		"installer/test.sh":      pass,
		"release/smoke-local.sh": pass,
		"bin/pg_isready":         pass,
		"bin/ruby":               "#!/bin/bash\nprintf 3.4.1\n",
		"bin/go":                 "#!/bin/bash\nif [[ $1 == env ]]; then echo /nowhere; exit 0; fi\necho \"go postgres=${LAB_TEST_POSTGRES:+named} schema=${LAB_TEST_POSTGRES_SCHEMA:-} $*\" >> \"$CALLS\"\n",
		"internal/oldsite/pg/testdata/old-site.sql.gz": "",
	} {
		path = filepath.Join(tree, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", filepath.Join(tree, "scripts", "parity-gate"))
	cmd.Env = append([]string{"PATH=" + filepath.Join(tree, "bin") + ":/usr/bin:/bin", "TMPDIR=" + t.TempDir(), "HOME=" + t.TempDir(), "CALLS=" + log}, env...)
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		status = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(log)
	return string(out), status, strings.Split(strings.TrimSpace(string(written)), "\n")
}

// The gate's check that the import reads the tables Rails made runs on
// tables Rails has just made: before it, nothing the gate starts can reach
// the database and make the three tables again from the committed SQL.
func TestTheParityGateReadsRailsTablesBeforeAnythingMakesThemAgain(t *testing.T) {
	said, status, calls := parityGate(t, "LAB_TEST_POSTGRES=postgres://localhost/omarchy_m_testing_test", "LAB_TEST_POSTGRES_SCHEMA=rails")
	if status != 0 || !strings.Contains(said, "parity gate: GREEN") {
		t.Fatalf("with everything passing: exit %d\n%s", status, said)
	}
	rails, made, committed := -1, -1, -1
	for i, call := range calls {
		real := strings.Contains(call, "-run "+realPostgresTest) || strings.Contains(call, "-run ^"+realPostgresTest+"$")
		switch {
		case call == "rails db:schema:load":
			made = i
		case real && strings.Contains(call, " schema=rails "):
			if rails >= 0 {
				t.Errorf("the gate reads Rails' tables twice:\n%s", strings.Join(calls, "\n"))
			}
			rails = i
		case real:
			committed = i
		case strings.HasPrefix(call, "go postgres=named ") && strings.Contains(call, "./internal/oldsite") && !strings.Contains(call, "-skip"):
			t.Errorf("a run that is not the comparison can reach the database and make the tables again: %s", call)
		}
		if strings.HasPrefix(call, "go postgres=named ") && strings.Contains(call, "./internal/oldsite") && !strings.Contains(call, " schema=rails ") && rails < 0 {
			t.Errorf("before Rails' tables were read, the gate ran: %s", call)
		}
	}
	if rails < 0 || committed < rails {
		t.Fatalf("the gate reads Rails' tables (call %d), then the committed SQL's (call %d):\n%s", rails, committed, strings.Join(calls, "\n"))
	}
	if made < 0 || made != rails-1 {
		t.Errorf("Rails makes its tables anew (call %d) right before they are read (call %d):\n%s", made, rails, strings.Join(calls, "\n"))
	}
	if !strings.Contains(calls[committed], " schema= ") {
		t.Errorf("the committed SQL's run still reads as Rails': %s", calls[committed])
	}
}

// With no database named, the gate says which one to name and is not green.
func TestTheParityGateSaysWhichDatabaseToName(t *testing.T) {
	said, status, calls := parityGate(t)
	if status != 2 || !strings.Contains(said, "NOT JUDGED") {
		t.Fatalf("with no database named: exit %d\n%s", status, said)
	}
	if !strings.Contains(said, "LAB_TEST_POSTGRES naming the database Rails prepared (omarchy_m_testing_test") {
		t.Errorf("the gate does not say which database to name:\n%s", said)
	}
	for _, call := range calls {
		if strings.Contains(call, "-run "+realPostgresTest) || call == "rails db:schema:load" {
			t.Errorf("with no database named the gate ran: %s", call)
		}
	}
}
