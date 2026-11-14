package cloudtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The scripts that run, and lib.sh, which they share.
var runnable = []string{
	"alias.sh", "cloudflare-zone.sh", "nameservers.sh", "owner-setup.sh", "register-domain.sh",
	"rehearsal-record.sh", "tunnel.sh", "verify-delegation.sh", "zone-settings.sh",
}

func source(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo(t), "scripts", "cloud", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheCloudFolderHoldsTheseScripts(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(repo(t), "scripts", "cloud"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := "alias.sh cloudflare-zone.sh edge.json lab.env.example lib.sh nameservers.sh owner-setup.sh register-domain.sh rehearsal-record.sh tunnel.sh verify-delegation.sh zone-settings.sh"
	if strings.Join(got, " ") != want {
		t.Errorf("scripts/cloud holds %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestTheCloudScriptsParseAndPassShellcheck(t *testing.T) {
	var files []string
	for _, name := range append([]string{"lib.sh"}, runnable...) {
		file := filepath.Join(repo(t), "scripts", "cloud", name)
		files = append(files, file)
		// macOS's own bash is 3.2, and is what /bin/bash is there.
		if out, err := exec.Command("/bin/bash", "-n", file).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %s", name, out)
		}
		if name == "lib.sh" {
			continue
		}
		info, _ := os.Stat(file)
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable", name)
		}
	}
	need(t, "shellcheck")
	cmd := exec.Command("shellcheck", append([]string{"--shell=bash", "--external-sources"}, files...)...)
	cmd.Dir = repo(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("shellcheck: %v\n%s", err, out)
	}
}

// A dry run is the default. It prints the calls, sends nothing and needs no
// token. What it prints is compared with internal/cloudtest/testdata;
// CLOUDTEST_UPDATE=1 writes those files afresh.
func TestADryRunPrintsTheCallsAndSendsNothing(t *testing.T) {
	for _, name := range runnable {
		name := name
		t.Run(name, func(t *testing.T) {
			f := asSpec(t)
			golden := filepath.Join(repo(t), "internal", "cloudtest", "testdata", strings.TrimSuffix(name, ".sh")+".dry-run.txt")
			out, code := script(t, f, nil, name)
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if again, _ := script(t, f, nil, name, "--dry-run"); again != out {
				t.Errorf("--dry-run prints something else than no option:\n%s", again)
			}
			if n := len(f.sent()); n != 0 {
				t.Errorf("a dry run made %d calls", n)
			}
			if os.Getenv("CLOUDTEST_UPDATE") != "" {
				if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if out != string(want) {
				t.Errorf("the dry run is not what %s holds:\n%s", filepath.Base(golden), out)
			}
			for _, id := range []string{zoneID, accountID} {
				if strings.Contains(out, id) {
					t.Errorf("a dry run names an id:\n%s", out)
				}
			}
		})
	}
}

// What the zone script's dry run would send is edge.json, whole: each setting
// with its call, each rule as written.
func TestTheZoneScriptsDryRunCoversEdgeJSON(t *testing.T) {
	out, _ := script(t, nil, nil, zoneScript)
	e := readEdge(t)
	for _, s := range e.Settings {
		method := s.Method
		if method == "" {
			method = "PATCH"
		}
		if !strings.Contains(out, s.State+": "+s.Name+"\n  GET "+s.Path+"\n  "+method+" "+s.Path+" {") {
			t.Errorf("the dry run does not show %s being read and set", s.Name)
		}
	}
	for _, r := range e.Rules {
		if !strings.Contains(out, r.Name+"\n  GET /zones/{zone}/rulesets/phases/"+r.Phase+"/entrypoint\n  PUT /zones/{zone}/rulesets/phases/"+r.Phase+"/entrypoint {\"rules\":[") {
			t.Errorf("the dry run does not show %s", r.Name)
		}
	}
	for _, o := range e.Owner {
		if !strings.Contains(out, "the owner's to set: "+o.Name+"\n  GET "+o.Path+"\n") || strings.Contains(out, "PATCH "+o.Path) || strings.Contains(out, "POST "+o.Path) {
			t.Errorf("the dry run does not show %s as read and never set", o.Name)
		}
	}
	if !strings.Contains(out, e.Mail.Address) {
		t.Error("the dry run does not show the mail forwarding rule")
	}
}

func TestEveryCloudScriptRunsTheThreeWays(t *testing.T) {
	for _, name := range runnable {
		text := source(t, name)
		for _, want := range []string{"--check", "$MODE == dry-run", "parse_mode \"$@\"", "send nothing"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q", name, want)
			}
		}
	}
}

func TestTheTokenComesFromAFileOnlyItsOwnerReads(t *testing.T) {
	f := asSpec(t)
	open := filepath.Join(t.TempDir(), "token")
	os.WriteFile(open, []byte(f.token+"\n"), 0o644)
	for _, name := range []string{zoneScript, tunnelScript, "cloudflare-zone.sh"} {
		out, code := script(t, f, nil, name, "--check", "--token-file", open)
		if code == 0 || !strings.Contains(out, "can be read by others") {
			t.Errorf("%s took a token file others can read: exit %d\n%s", name, code, out)
		}
		out, code = script(t, f, nil, name, "--check", "--token-file", filepath.Join(t.TempDir(), "none"))
		if code == 0 || !strings.Contains(out, "no token file") {
			t.Errorf("%s with no such token file: exit %d\n%s", name, code, out)
		}
		// With no file and no terminal there is nobody to ask.
		out, code = script(t, f, nil, name, "--check")
		if code == 0 || !strings.Contains(out, "--token-file") {
			t.Errorf("%s with no token: exit %d\n%s", name, code, out)
		}
		if strings.Contains(out, f.token) {
			t.Errorf("%s printed the token", name)
		}
	}
	if n := len(f.sent()); n != 0 {
		t.Errorf("%d calls were made with no token to make them with", n)
	}
}

// Traced with bash -x, a script still shows no token: lib.sh turns tracing off.
func TestTheTokenIsNotPrintedWhenTraced(t *testing.T) {
	f := asSpec(t)
	for _, name := range []string{zoneScript, tunnelScript} {
		cmd := exec.Command("bash", "-x", filepath.Join(repo(t), "scripts", "cloud", name), "--check", "--token-file", f.tokenFile())
		cmd.Env = append(os.Environ(), "LAB_CLOUD_API="+f.url())
		out, _ := cmd.CombinedOutput()
		if strings.Contains(string(out), f.token) {
			t.Errorf("%s traced prints the token", name)
		}
		if len(f.sent()) == 0 {
			t.Errorf("%s traced made no call:\n%s", name, out)
		}
	}
}

// The token is never an argument of a command: it reaches curl as a header
// read from a pipe, and curl is run only by lib.sh.
func TestTheTokenReachesCurlOnlyThroughAPipe(t *testing.T) {
	header := regexp.MustCompile(`(?i)-H\s+["']?authorization`)
	for _, name := range append([]string{"lib.sh"}, runnable...) {
		for n, line := range strings.Split(source(t, name), "\n") {
			code := line
			if i := strings.Index(code, "#"); i == 0 {
				continue
			}
			if name != "lib.sh" && regexp.MustCompile(`(^|[^\w-])curl\s`).MatchString(code) {
				t.Errorf("%s:%d runs curl itself", name, n+1)
			}
			if header.MatchString(code) {
				t.Errorf("%s:%d gives curl the token as an argument", name, n+1)
			}
			if strings.Contains(code, "$TOKEN\"") && !strings.Contains(code, `@<(printf 'Authorization: Bearer %s\n' "$TOKEN")`) &&
				!regexp.MustCompile(`^\s*(printf '%s\\n' "\$TOKEN" > |\(umask 077; .*printf '%s\\n' "\$TOKEN" > )`).MatchString(code) {
				t.Errorf("%s:%d uses the token outside the header pipe: %s", name, n+1, strings.TrimSpace(line))
			}
			if regexp.MustCompile(`\b(echo|say|die|would)\b.*\$\{?(TOKEN|secret)\b`).MatchString(code) && !strings.Contains(code, "TOKEN_FILE") {
				t.Errorf("%s:%d prints a secret: %s", name, n+1, strings.TrimSpace(line))
			}
		}
	}
	lib := source(t, "lib.sh")
	if n := strings.Count(lib, `-H @<(printf 'Authorization: Bearer %s\n' "$TOKEN")`); n != 2 {
		t.Errorf("lib.sh hands curl the token %d times, want the two calls of cf", n)
	}
	if !strings.Contains(lib, "\nset +x\n") {
		t.Error("lib.sh does not turn tracing off")
	}
	if strings.Contains(lib, "cloudflare-api-token") || strings.Contains(lib, "CLOUDFLARE_API_TOKEN") {
		t.Error("lib.sh still takes a Cloudflare token from the Keychain or the environment")
	}
}

func TestTheAPIStandInTakesOnlyLoopback(t *testing.T) {
	f := asSpec(t)
	out, code := script(t, f, []string{"LAB_CLOUD_API=https://example.org/client/v4"}, zoneScript, "--check", "--token-file", f.tokenFile())
	if code == 0 || !strings.Contains(out, "loopback") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// An address that only starts as loopback's: what follows the @ is the host.
	for _, api := range []string{"http://127.0.0.1:1@lab.invalid/client/v4", "http://127.0.0.1:1.lab.invalid/client/v4", "http://127.0.0.1:/client/v4"} {
		if out, code := script(t, f, []string{"LAB_CLOUD_API=" + api}, zoneScript, "--check", "--token-file", f.tokenFile()); code == 0 || !strings.Contains(out, "loopback") {
			t.Errorf("LAB_CLOUD_API=%s: exit %d\n%s", api, code, out)
		}
	}
	if !strings.Contains(source(t, "lib.sh"), `CLOUDFLARE_API="https://api.cloudflare.com/client/v4"`) {
		t.Error("lib.sh does not name Cloudflare's API")
	}
}

// No id of the account or the zone, no token and no mailbox is in the folder;
// lab.env, which holds the account's id, is not committed.
func TestTheCloudFolderHoldsNoIdAndNoSecret(t *testing.T) {
	hex := regexp.MustCompile(`\b[0-9a-f]{32}\b`)
	for _, name := range append([]string{"lib.sh", "edge.json", "lab.env.example"}, runnable...) {
		text := source(t, name)
		if m := hex.FindString(text); m != "" {
			t.Errorf("%s holds what looks like an id or a key: %s", name, m)
		}
		if regexp.MustCompile(`[\w.+-]+@(gmail|hey|icloud|proton)\.`).MatchString(text) {
			t.Errorf("%s holds a mailbox", name)
		}
	}
	for _, line := range strings.Split(source(t, "lab.env.example"), "\n") {
		if strings.Contains(line, "=") && !strings.HasPrefix(line, "#") && !strings.HasSuffix(line, "=") {
			t.Errorf("lab.env.example gives a value: %s", line)
		}
	}
	ignore, _ := os.ReadFile(filepath.Join(repo(t), ".gitignore"))
	if !strings.Contains(string(ignore), "\n/scripts/cloud/lab.env\n") {
		t.Error("scripts/cloud/lab.env is not ignored")
	}
}

// The names the cloud scripts set up are the names labd and the mini use.
func TestTheNamesAreTheMinis(t *testing.T) {
	lib := source(t, "lib.sh")
	mini, err := os.ReadFile(filepath.Join(repo(t), "scripts", "mini", "lib.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mini), `LAB_HOST="`+labHost+`"`) || !strings.Contains(lib, `LAB_DOMAIN="`+labDomain+`"`) || !strings.Contains(lib, `LAB_HOST="lab.$LAB_DOMAIN"`) {
		t.Error("the cloud scripts and the mini's do not name the same Lab address")
	}
	route, err := os.ReadFile(filepath.Join(repo(t), "deploy", "cloudflared.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{labHost, "maclab.omarchy.org"} {
		if !strings.Contains(string(route), "hostname: "+name+"\n") {
			t.Errorf("the tunnel's route file does not route %s", name)
		}
	}
}
