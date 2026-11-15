// Package hardening runs Hardening's fourteen tests
// (scripts/cutover/hardening/) as far as they go with no mini: their dry
// runs, and their local runs against a labd on loopback with a stand-in for
// GitHub and simulated Macs. Nothing here reaches the Lab address or the
// mini.
package hardening

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/testhome"
)

var (
	binOnce sync.Once
	binDir  string
	binErr  error
	// Each local run starts a labd and simulated Macs: four at a time.
	slots = make(chan struct{}, 4)
)

// No test here reads or writes the home of whoever runs the tests.
func TestMain(m *testing.M) {
	exit := testhome.Set()
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	exit(code)
}

var tests = []string{"h01-console", "h02-exec-lease", "h03-freeze-entry", "h04-builder-token", "h05-build-sources",
	"h06-no-secret-shown", "h07-cuts", "h08-agents-lines", "h09-notice-topic", "h10-browser", "h11-exec-record-readers",
	"h12-agent-signature", "h13-lab-own-upload", "h14-volunteers-mac"}

func repo(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func script(t *testing.T, name string) string {
	return filepath.Join(repo(t), "scripts", "cutover", "hardening", name)
}

// bins builds labd, lab, lab-agent for this machine (its sim mode is the
// simulated Mac), the lab-agent labd hands out, the stand-in for GitHub and
// the probe, once.
func bins(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and starts labd")
	}
	binOnce.Do(func() {
		binDir, binErr = os.MkdirTemp("", "hardening-bin")
		if binErr != nil {
			return
		}
		for _, build := range [][]string{
			{"", "-o", binDir + "/", "./cmd/labd", "./cmd/lab", "./cmd/lab-agent", "./tools/fake-github", "./tools/hardening-probe"},
			{"GOOS=linux GOARCH=arm64", "-o", binDir + "/lab-agent-linux-arm64", "./cmd/lab-agent"},
		} {
			cmd := exec.Command("go", append([]string{"build"}, build[1:]...)...)
			cmd.Dir = repo(t)
			cmd.Env = append(os.Environ(), strings.Fields(build[0])...)
			if out, err := cmd.CombinedOutput(); err != nil {
				binErr = fmt.Errorf("go build: %v\n%s", err, out)
				return
			}
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binDir
}

// run starts a script with env added to the test's own and stdin at its
// end, and returns what it printed and its exit status.
func run(t *testing.T, env []string, name string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	t.Fatalf("%s: %v\n%s", name, err, out)
	return "", 0
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// local runs one test against a labd on loopback and returns what it said,
// its exit status, its record and the results file.
func local(t *testing.T, env []string, name string) (out string, code int, record, results string) {
	t.Helper()
	bin := bins(t)
	slots <- struct{}{}
	defer func() { <-slots }()
	records := t.TempDir()
	out, code = run(t, env, script(t, name), "--local", "--bin", bin, "--records", records)
	if files, _ := filepath.Glob(filepath.Join(records, "*-hardening-"+name[1:3]+".md")); len(files) == 1 {
		record = read(t, files[0])
	}
	if b, err := os.ReadFile(filepath.Join(records, "hardening.results")); err == nil {
		results = string(b)
	}
	return out, code, record, results
}

// stubs is a folder of stand-ins for the programs a test may call, which
// write down how they were called and do what the test's text says.
func stubs(t *testing.T, text map[string]string) (dir, calls string) {
	t.Helper()
	dir = t.TempDir()
	calls = filepath.Join(dir, "calls")
	for _, tool := range []string{"lab", "ssh", "curl", "sqlite3", "python3", "go", "ssh-keygen", "hardening-probe"} {
		body := "#!/bin/sh\necho \"" + tool + " $*\" >> " + calls + "\n" + text[tool] + "\n"
		if text[tool] == "" {
			body += "exit 9\n"
		}
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, calls
}

// With no option each test says its steps and what each expects, names
// where the owner's hand is needed, and sends nothing.
func TestEveryDryRunSaysItsStepsAndSendsNothing(t *testing.T) {
	dir, calls := stubs(t, nil)
	env := []string{"PATH=" + dir + ":" + os.Getenv("PATH")}
	byHand := map[string]int{"h07-cuts": 1, "h09-notice-topic": 2, "h10-browser": 1}
	for _, name := range tests {
		for _, option := range [][]string{nil, {"--dry-run"}} {
			out, code := run(t, env, script(t, name), option...)
			if code != 0 {
				t.Fatalf("%s %v: exit %d\n%s", name, option, code, out)
			}
			for _, want := range []string{name + ": dry run.", "expects:", "docs/acceptance/", "-hardening-" + name[1:3] + ".md", "hardening.results", "Nothing was sent"} {
				if !strings.Contains(out, want) {
					t.Errorf("%s's dry run does not say %q:\n%s", name, want, out)
				}
			}
			if n := strings.Count(out, "STOP, by hand:"); n != byHand[name] {
				t.Errorf("%s's dry run names %d steps by hand, want %d:\n%s", name, n, byHand[name], out)
			}
		}
		if out, code := run(t, env, script(t, name), "--lab", "--open-everything"); code != 1 || !strings.Contains(out, "Nothing was done") {
			t.Errorf("%s took an option it does not know: exit %d\n%s", name, code, out)
		}
	}
	for name, why := range map[string]string{"h03-freeze-entry": "--lab needs --release R", "h06-no-secret-shown": "--lab needs --mini", "h09-notice-topic": "--lab needs --volunteers-mac NAME and --redirect URL"} {
		if out, code := run(t, env, script(t, name), "--lab"); code != 1 || !strings.Contains(out, why) {
			t.Errorf("%s --lab without what it needs: exit %d\n%s", name, code, out)
		}
	}
	out, code := run(t, env, script(t, "run"))
	if code != 0 || strings.Count(out, ": dry run.") != 14 || strings.Count(out, ": said ") != 14 {
		t.Errorf("run with no option: exit %d, %d dry runs\n%s", code, strings.Count(out, ": dry run."), out)
	}
	if out, code := run(t, env, script(t, "run"), "--only", "12,3"); code != 0 || strings.Count(out, ": dry run.") != 2 || strings.Index(out, "h12-") > strings.Index(out, "h03-") {
		t.Errorf("run --only 12,3: exit %d\n%s", code, out)
	}
	if out, code := run(t, env, script(t, "run"), "--skip", "1,2,3,4,5,6,7,8,9,10,11,12,13"); code != 0 || strings.Count(out, ": dry run.") != 1 || !strings.Contains(out, "h14-volunteers-mac: dry run.") {
		t.Errorf("run --skip all but 14: exit %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("a dry run, or a run refused for its options, called:\n%s", b)
	}
}

// Each of the fourteen against a labd on loopback: the lab's rules do what
// the test expects of them. Test 9's rule is slice 40's: until it lands the
// run says it waits for it.
func TestTheFourteenRunLocally(t *testing.T) {
	t.Parallel()
	bins(t)
	for name, wants := range map[string][]string{
		"h01-console": {"GET /api/devices/m1-pro/console: 410, The serial console is not part of Omarchy Mac Lab.", "POST /api/devices/m1-pro/console/arm: 410",
			"GET /api/devices/m1-pro/serial: 410", "serial-assisted: 410", "lab shell m1-pro: exit 1", "off the network, under the caller's Lease: 409",
			"the Exec record holds it: owner|echo through-the-console|refused|"},
		"h02-exec-lease": {"a Maintainer's lab exec on m1-pro with no Lease: 409, lab exec needs your own Lease on m1-pro, a Maintainer's too",
			"an M team member's lab exec with no Lease: 403", "a Maintainer's lab exec under the member's Lease: 409", "|ran|3|", "member|echo h02-"},
		"h03-freeze-entry": {"with no Lease: 409, freeze-entry on m1-pro needs your own Lease on it, a Maintainer's too", "the Exec record of m1-pro still holds 0 lines"},
		"h04-builder-token": {"a made-up builder token, POST /api/builder/poll: 401", "a Maintainer's Lab token, POST /api/builder/poll: 401",
			"holding no build, was refused both uploaded artifacts: 404", "the running build's config was served to its builder (200); the other artifact was refused (404",
			"a shared builder.token in labd's data folder, after a restart: 401", "its token is refused (401)"},
		"h05-build-sources": {"https://gitlab.com/hardening/linux: 400", "https://hardening:PASSWORD@github.com/hardening/linux: 400, a build's source is public: an address that carries credentials is refused",
			"http://github.com/hardening/linux: 400", "no build was made for any of them", "labd contacted nothing", "and the port does count"},
		"h06-no-secret-shown": {"no secret shows in a process or a service definition, and none is given by value"},
		"h07-cuts": {"the revoked token's next request: 401", "its stream ended: ", "a Maintainer used \"End all access\" on @member, on People",
			"the next request after \"End all access\": 401", "the stream ended: ", "@member's browser is signed out", "the Lease on m1-pro ended with it"},
		"h08-agents-lines": {"owner marked member as M team.", "owner made the Lab token \"h08-", "owner revoked member's Lab token \"h08-",
			"owner used \"End all access\" on member.", "owner removed the M team mark from member."},
		"h10-browser": {"a signed-in browser's form from a page at https://elsewhere.example: 403", "Sec-Fetch-Site: cross-site: 403", "no Lab token was made by either",
			"a signed-out browser's upload from a page at https://elsewhere.example: 403", "sign-in asked to return to https://elsewhere.example/ landed on the home page",
			"sign-in asked to return to //elsewhere.example/ landed on the home page", "it landed there", "/api/themes/default/theme.css: 404",
			"opening the deletion link: 200, no-store, no referrer, a confirmation form", "opening the link deleted nothing"},
		"h11-exec-record-readers": {"a Maintainer reading the Exec record of loaner: 200, sees: maintainer member", "the Mac's Volunteer (@lender) reading the Exec record of loaner: 200, sees: maintainer member",
			"the member who ran one (@member) reading the Exec record of loaner: 200, sees: member", "a member who ran none (@other) reading the Exec record of loaner: 200",
			"a person with no role (@nobody) reading the Exec record of loaner: 403", "on a Mac that is not theirs reading the Exec record of m1-pro: 404",
			"a signed-out browser reading the Exec record of loaner: 401", "labd deleted the line past 12 months at its start"},
		"h12-agent-signature": {"a lab-agent changed after it was signed: refused by the Mac", "a lab-agent signed by another key: refused by the Mac",
			"an older lab-agent, 0.9.0, rightly signed: refused by the Mac", "older than the 0.10.0 installed", "lab-agent 0.10.0 -> 0.10.1"},
		"h13-lab-own-upload": {"in the query, the headers and the User-Agent: 201, stored", "its page does not say \"run by the lab\"",
			"Reports, for a Maintainer, says it arrived by the Mac's own tool", "a report carrying a lab_job key: 422", "the 31st of the hour: 429"},
		"h14-volunteers-mac": {"lab run loaner --installed: refused", "lab installed promote loaner: refused", "Promote on the Release's page: refused",
			"the site's boot form has no field for an Installed run", "POST /api/jobs, an Installed run: refused", "POST /api/installed/0.0.0-hardening-14/promote: refused", "no job was made on loaner"},
		"h09-notice-topic": {"WAITS for slice 40: labd connected 1 time(s) to the Volunteer's topic at a private address"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, code, record, results := local(t, nil, name)
			want, word := 0, "pass"
			if name == "h09-notice-topic" {
				want, word = 4, "waits"
			}
			if code != want {
				t.Fatalf("exit %d, want %d\n%s", code, want, out)
			}
			for _, w := range append(wants, "It proves the script and the lab's rules, not the mini.", "Result: "+word) {
				if !strings.Contains(record, w) {
					t.Errorf("the record lacks %q:\n%s", w, record)
				}
			}
			if !strings.HasPrefix(results, name[1:3]+" "+word+" 20") || strings.Count(results, "\n") != 1 {
				t.Errorf("the results file: %q", results)
			}
			for _, secret := range []string{"enr_", "lab_0", "lab_1", "lab_2", "lab_3", "lab_4", "lab_5", "lab_6", "lab_7", "lab_8", "lab_9", "lab_a", "lab_b", "lab_c", "lab_d", "lab_e", "lab_f"} {
				if strings.Contains(out, secret) {
					t.Errorf("a token was printed (%s):\n%s", secret, out)
				}
			}
		})
	}
	t.Cleanup(func() {
		if left, _ := filepath.Glob(filepath.Join(repo(t), "docs", "acceptance", "*hardening*")); len(left) != 0 {
			t.Errorf("a local run left a record in docs/acceptance: %v", left)
		}
	})
}

// A secret put where the rule says it must not be is found: in a service
// definition, and on a command line.
func TestH06FindsASecretShown(t *testing.T) {
	t.Parallel()
	for fault, want := range map[string]string{
		"secret-in-definition": "FAILED: a secret shows in a service definition",
		"secret-in-process":    "FAILED: a secret shows in the process list (1)",
	} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			out, code, record, results := local(t, []string{"HARDENING_FAULT=" + fault}, "h06-no-secret-shown")
			if code != 1 || !strings.Contains(record, want) || !strings.HasPrefix(results, "06 fail ") {
				t.Fatalf("exit %d, results %q\n%s\n%s", code, results, out, record)
			}
			if fault == "secret-in-definition" && !strings.Contains(record, "shape=flag:org.omarchy-mac.lab.fault.plist") {
				t.Errorf("the shape of a --token on the definition's command line was not named:\n%s", record)
			}
		})
	}
}

// Through the Lab address a step that needs the owner waits for them: with
// nobody there the test ends as stopped, exit 3, and asked for nothing.
func TestAStepForTheOwnerWithNobodyThereStopsTheTest(t *testing.T) {
	bin := bins(t)
	dir, calls := stubs(t, map[string]string{"lab": `case "$1" in status) echo "NAME STATE" ;; *) exit 9 ;; esac`})
	records := t.TempDir()
	out, code := run(t, []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "LAB_ADDRESS=https://lab.example"}, script(t, "h07-cuts"),
		"--lab", "--bin", bin, "--records", records)
	if code != 3 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	files, _ := filepath.Glob(filepath.Join(records, "*-hardening-07.md"))
	if len(files) != 1 {
		t.Fatalf("records: %v\n%s", files, out)
	}
	record := read(t, files[0])
	for _, want := range []string{"h07-cuts --lab`, through https://lab.example", "STOPPED: nobody was there to type it: the value of the __Host-lab_session cookie", "Result: stopped"} {
		if !strings.Contains(record, want) {
			t.Errorf("the record lacks %q:\n%s", want, record)
		}
	}
	if !strings.HasPrefix(read(t, filepath.Join(records, "hardening.results")), "07 stopped ") {
		t.Errorf("the stop was not written in the results")
	}
	if asked := read(t, calls); strings.TrimSpace(asked) != "lab status" {
		t.Errorf("the calls:\n%s", asked)
	}
}

// Test 14 runs once, in step 14a (decision S12): through the Lab address
// with no Mac named it takes that run's pass and does not repeat it.
func TestH14TakesStep14asRun(t *testing.T) {
	dir, calls := stubs(t, nil)
	env := []string{"PATH=" + dir + ":" + os.Getenv("PATH")}
	records := t.TempDir()
	if out, code := run(t, env, script(t, "h14-volunteers-mac"), "--lab", "--records", records); code != 3 || !strings.Contains(out, "runs in step 14a (decision S12)") {
		t.Errorf("with no run of step 14a: exit %d\n%s", code, out)
	}
	record := "2026-11-20-hardening-14.md"
	if err := os.WriteFile(filepath.Join(records, record), []byte("# Hardening's test 14\n\nResult: pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(records, "hardening.results"), []byte("14 pass 2026-11-20 "+record+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, env, script(t, "h14-volunteers-mac"), "--lab", "--records", records)
	if code != 0 || !strings.Contains(out, "taken from the run of 2026-11-20 in step 14a") || !strings.Contains(out, "Not repeated.") {
		t.Errorf("with step 14a's pass: exit %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("taking step 14a's result called:\n%s", b)
	}
	if out, code := run(t, env, script(t, "h14-volunteers-mac"), "--lab", "--volunteers-mac", "m2-max"); code != 1 || !strings.Contains(out, "go together") {
		t.Errorf("--volunteers-mac without --release: exit %d\n%s", code, out)
	}
}

// run --results is what the Ready list reads: green only when all fourteen
// say pass; a test that waits for a slice is not green.
func TestTheResultsAreGreenOnlyWhenAllFourteenPassed(t *testing.T) {
	records := t.TempDir()
	write := func(lines ...string) {
		if err := os.WriteFile(filepath.Join(records, "hardening.results"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var all []string
	for n := 1; n <= 14; n++ {
		all = append(all, fmt.Sprintf("%02d pass 2026-11-20 2026-11-20-hardening-%02d.md", n, n))
	}
	if out, code := run(t, nil, script(t, "run"), "--results", "--records", records); code != 1 || strings.Count(out, ": not run") != 14 {
		t.Errorf("no results yet: exit %d\n%s", code, out)
	}
	write(all...)
	if out, code := run(t, nil, script(t, "run"), "--results", "--records", records); code != 0 || !strings.Contains(out, "Ready item 8 is green: all fourteen passed") || !strings.Contains(out, "H  9: pass    2026-11-20") {
		t.Errorf("all fourteen passed: exit %d\n%s", code, out)
	}
	for what, change := range map[string]string{"waits": "09 waits 2026-11-21 x.md", "stopped": "09 stopped 2026-11-21 x.md", "fail": "09 fail 2026-11-21 x.md", "": ""} {
		lines := append([]string{}, all[:8]...)
		if change != "" {
			lines = append(lines, change)
		}
		write(append(lines, all[9:]...)...)
		out, code := run(t, nil, script(t, "run"), "--results", "--records", records)
		if code != 1 || !strings.Contains(out, "Ready item 8 is not green") || (what != "" && !strings.Contains(out, "H  9: "+what)) || (what == "" && !strings.Contains(out, "H  9: not run")) {
			t.Errorf("test 9 %q: exit %d\n%s", what, code, out)
		}
	}
}

func TestTheScriptsPassShellcheck(t *testing.T) {
	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Skip("no shellcheck")
	}
	files := []string{script(t, "lib.sh"), script(t, "run"), filepath.Join(repo(t), "scripts", "mini", "secrets-seen")}
	for _, name := range tests {
		files = append(files, script(t, name))
	}
	cmd := exec.Command("shellcheck", append([]string{"--shell=bash", "--external-sources"}, files...)...)
	cmd.Dir = repo(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("shellcheck: %v\n%s", err, out)
	}
}
