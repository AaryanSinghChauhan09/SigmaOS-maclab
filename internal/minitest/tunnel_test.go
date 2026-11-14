package minitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	tunnelToken = "/var/lab/secrets/cloudflared.token"
	routeFile   = "/usr/local/lab/current/deploy/cloudflared.yml"
	labdSocket  = "/var/lab/run/labd.sock"
)

// flags reads a LaunchDaemon's arguments after its program: words, and
// flags with the value that follows each.
func flags(args []string) (words []string, values map[string]string) {
	values = map[string]string{}
	for i := 1; i < len(args); i++ {
		switch {
		case !strings.HasPrefix(args[i], "--"):
			words = append(words, args[i])
		case i+1 < len(args) && strings.HasPrefix(args[i+1], "/") || i+1 < len(args) && strings.Contains(args[i+1], ":"):
			values[args[i]] = args[i+1]
			i++
		default:
			values[args[i]] = ""
		}
	}
	return words, values
}

func TestCloudflaredsLaunchDaemon(t *testing.T) {
	file := filepath.Join(repo(t), "deploy", cloudflaredLabel+".plist")
	values, arrays := plist(t, file)
	for key, want := range map[string]string{
		"Label":     cloudflaredLabel,
		"UserName":  "lab",
		"GroupName": "lab",
		"RunAtLoad": "true",
		"KeepAlive": "true",
		"Umask":     "63",
	} {
		if values[key] != want {
			t.Errorf("%s is %q, want %q", key, values[key], want)
		}
	}
	if shell(t, "CLOUDFLARED_LABEL") != cloudflaredLabel {
		t.Errorf("the mini's scripts call it %q", shell(t, "CLOUDFLARED_LABEL"))
	}
	args := arrays["ProgramArguments"]
	// The copy only root can write, never Homebrew's prefix (decision S14).
	if len(args) == 0 || args[0] != "/usr/local/lab/tools/current/cloudflared/bin/cloudflared" {
		t.Fatalf("cloudflared is not run from the root-owned copy of the Homebrew tools: %v", args)
	}
	words, values2 := flags(args)
	if strings.Join(words, " ") != "tunnel run" {
		t.Errorf("cloudflared is asked to %q, want tunnel run", strings.Join(words, " "))
	}
	if values2["--config"] != routeFile {
		t.Errorf("the routes come from %q, want the Deployed build's %s", values2["--config"], routeFile)
	}
	if values2["--token-file"] != tunnelToken || !strings.HasSuffix(shell(t, "TUNNEL_TOKEN"), strings.TrimPrefix(tunnelToken, "/var/lab")) {
		t.Errorf("the credential is read from %q; the set-up puts it at %s", values2["--token-file"], shell(t, "TUNNEL_TOKEN"))
	}
	if _, ok := values2["--no-autoupdate"]; !ok {
		t.Error("cloudflared may update itself: its version changes only in a Maintenance run")
	}
	if !strings.HasPrefix(values2["--metrics"], "127.0.0.1:") {
		t.Errorf("cloudflared's metrics are at %q, not on loopback", values2["--metrics"])
	}
	for flag := range values2 {
		switch flag {
		case "--config", "--token-file", "--no-autoupdate", "--metrics":
		default:
			t.Errorf("an argument the test does not know: %s", flag)
		}
	}
	text := read(t, file)
	for _, word := range []string{"--token\n", "<string>--token</string>", "TUNNEL_TOKEN", "SECRET", "PASSWORD", "EnvironmentVariables", "credentials-file"} {
		if strings.Contains(text, word) {
			t.Errorf("cloudflared's LaunchDaemon file holds %s", word)
		}
	}
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		if !strings.HasPrefix(values[key], "/var/lab/log/") {
			t.Errorf("%s is %q, outside the lab account's log folder", key, values[key])
		}
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("plutil", "-lint", file).CombinedOutput(); err != nil {
			t.Errorf("plutil: %v\n%s", err, out)
		}
	}
}

// route is one rule of the tunnel's route file.
type route struct{ hostname, service string }

// routes parses deploy/cloudflared.yml, which is kept to this shape: keys
// at the left margin, one level of settings under originRequest, and a list
// of rules under ingress.
func routes(t *testing.T, text string) (top map[string]map[string]string, rules []route) {
	t.Helper()
	top = map[string]map[string]string{}
	section := ""
	for n, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimRight(line, " ")
		if line == "" {
			continue
		}
		switch {
		case !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":"):
			section = strings.TrimSuffix(line, ":")
			top[section] = map[string]string{}
		case section == "ingress" && strings.HasPrefix(line, "  - "):
			rules = append(rules, route{})
			fallthrough
		case section == "ingress" && strings.HasPrefix(line, "    ") && len(rules) > 0:
			k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- ")), ": ")
			if !ok {
				t.Fatalf("route file line %d: %q", n+1, line)
			}
			switch k {
			case "hostname":
				rules[len(rules)-1].hostname = v
			case "service":
				rules[len(rules)-1].service = v
			default:
				t.Fatalf("route file line %d: a rule has %q, which the lab's routes do not use", n+1, k)
			}
		case section != "" && section != "ingress" && strings.HasPrefix(line, "  "):
			k, v, ok := strings.Cut(strings.TrimSpace(line), ": ")
			if !ok {
				t.Fatalf("route file line %d: %q", n+1, line)
			}
			top[section][k] = v
		default:
			t.Fatalf("route file line %d is outside the shape the test reads: %q", n+1, line)
		}
	}
	return top, rules
}

// Two names reach labd's socket; everything else is 404.
func TestTheTunnelsRouteFile(t *testing.T) {
	text := read(t, filepath.Join(repo(t), "deploy", "cloudflared.yml"))
	top, rules := routes(t, text)
	_, labd := plist(t, filepath.Join(repo(t), "deploy", labdLabel+".plist"))
	var alias, socket string
	for i, a := range labd["ProgramArguments"] {
		switch a {
		case "--alias":
			alias = labd["ProgramArguments"][i+1]
		case "--socket":
			socket = labd["ProgramArguments"][i+1]
		}
	}
	if socket != labdSocket {
		t.Fatalf("labd's socket is %q", socket)
	}
	want := []route{
		{shell(t, "LAB_HOST"), "unix:" + socket},
		{alias, "unix:" + socket},
		{"", "http_status:404"},
	}
	if len(rules) != len(want) {
		t.Fatalf("%d rules, want the Lab address, the Alias and 404 for the rest: %v", len(rules), rules)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d is %v, want %v", i+1, rules[i], want[i])
		}
	}
	for key := range top {
		if key != "ingress" && key != "originRequest" {
			t.Errorf("the route file sets %q", key)
		}
	}
	// The tunnel's id and its credential are in the file only lab reads.
	if regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-`).MatchString(text) || regexp.MustCompile(`(?m)^(tunnel|credentials-file|token)\b`).MatchString(text) {
		t.Error("the route file names a tunnel or a credential")
	}
	// HTTP/1.1 to the socket, and labd's idle timeout above cloudflared's pool timeout.
	for key, v := range top["originRequest"] {
		if key != "keepAliveTimeout" {
			t.Errorf("originRequest sets %s: %s", key, v)
		}
	}
	pool, err := strconv.Atoi(strings.TrimSuffix(top["originRequest"]["keepAliveTimeout"], "s"))
	if err != nil {
		t.Fatalf("keepAliveTimeout is %q, want seconds", top["originRequest"]["keepAliveTimeout"])
	}
	m := regexp.MustCompile(`idleTimeout\s*=\s*(\d+) \* time\.(Minute|Second)`).FindStringSubmatch(read(t, filepath.Join(repo(t), "internal", "server", "listen.go")))
	if m == nil {
		t.Fatal("internal/server/listen.go no longer sets idleTimeout as N * time.Minute")
	}
	idle, _ := strconv.Atoi(m[1])
	if m[2] == "Minute" {
		idle *= 60
	}
	if pool != 90 || idle <= pool {
		t.Errorf("cloudflared drops an idle connection after %d s and labd after %d s: want 90 s, and labd's above it", pool, idle)
	}
}

func formulaeList(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(read(t, filepath.Join(repo(t), "deploy", "homebrew-formulae")), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if f := strings.Fields(line); len(f) > 0 {
			// A formula of a tap is listed as tap/formula.
			out = append(out, filepath.Base(f[0]))
		}
	}
	return out
}

// A LaunchDaemon that runs a Homebrew tool runs one the set-up installs and copies.
func TestEveryToolADaemonRunsIsOnTheHomebrewList(t *testing.T) {
	list := " " + strings.Join(formulaeList(t), " ") + " "
	if !strings.Contains(list, " cloudflared ") {
		t.Errorf("cloudflared is not in deploy/homebrew-formulae: %s", list)
	}
	tool := regexp.MustCompile(`/usr/local/lab/tools/current/([^/<]+)/`)
	for _, f := range launchDaemons(t) {
		for _, m := range tool.FindAllStringSubmatch(read(t, f), -1) {
			if !strings.Contains(list, " "+m[1]+" ") {
				t.Errorf("%s runs %s, which deploy/homebrew-formulae does not name", filepath.Base(f), m[1])
			}
		}
	}
}

// withTunnel is a mini whose Homebrew list is the repository's and which has
// a Deployed build, so that only the credential is missing.
func withTunnel(t *testing.T) (*mini, key) {
	t.Helper()
	m := newMini(t)
	k := newKey(t)
	write(t, m.tree+"/deploy/homebrew-formulae", read(t, filepath.Join(repo(t), "deploy", "homebrew-formulae")), 0o644)
	m.build("v0.2.0-rc.1")
	return m, k
}

// build makes tag the Deployed build, with a route file.
func (m *mini) build(tag string) {
	m.t.Helper()
	write(m.t, m.path("usr/local/lab/builds/"+tag+"/deploy/cloudflared.yml"), "# routes\n", 0o644)
	os.Remove(m.path("usr/local/lab/current"))
	if err := os.Symlink("builds/"+tag, m.path("usr/local/lab/current")); err != nil {
		m.t.Fatal(err)
	}
}

const madeUpCredential = "eyJhIjoiYSBtYWRlLXVwIGFjY291bnQiLCJ0IjoiYSBtYWRlLXVwIHR1bm5lbCIsInMiOiJub3QgYSBzZWNyZXQifQ=="

func credentialFile(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "tunnel-credential")
	write(t, file, text, mode)
	return file
}

func TestSetupInstallsTheTunnelsCredentialAndStartsIt(t *testing.T) {
	m, k := withTunnel(t)
	cred := credentialFile(t, madeUpCredential+"\n", 0o600)
	token := m.path(strings.TrimPrefix(tunnelToken, "/"))
	daemon := m.path("Library/LaunchDaemons/" + cloudflaredLabel + ".plist")

	out := m.ok("setup", "--dry-run", "--release-key", k.signers, "--tunnel-credential", cred)
	if !strings.Contains(out, "would put the tunnel's credential") || !strings.Contains(out, "would start the tunnel") {
		t.Errorf("the dry run does not say what it would do for the tunnel:\n%s", out)
	}
	if exists(token) || exists(daemon) || !exists(cred) || m.tunnelLoaded() {
		t.Error("a dry run changed something")
	}

	out = m.ok("setup", "--release-key", k.signers, "--tunnel-credential", cred)

	if got := read(t, token); got != madeUpCredential+"\n" {
		t.Fatalf("the credential on the mini is %q", got)
	}
	if got := mode(t, token); got != 0o400 {
		t.Errorf("the credential is %v, want only the lab account to read it", got)
	}
	if exists(cred) {
		t.Error("the copy the owner brought was left behind")
	}
	if strings.Contains(out, madeUpCredential) {
		t.Errorf("the credential was printed:\n%s", out)
	}
	if read(t, daemon) != read(t, filepath.Join(repo(t), "deploy", cloudflaredLabel+".plist")) || mode(t, daemon) != 0o644 {
		t.Error("cloudflared's LaunchDaemon file is not installed as the repository has it")
	}
	if !m.tunnelLoaded() || !strings.Contains(m.calls(), "launchctl bootstrap system "+daemon) {
		t.Errorf("the tunnel was not started:\n%s", m.calls())
	}
	if !exists(m.path("usr/local/lab/tools/current/cloudflared/bin/cloudflared")) {
		t.Error("cloudflared is not in the root-owned copy")
	}
	for _, never := range []string{"brew services", "/opt/homebrew/bin/cloudflared"} {
		if strings.Contains(m.calls(), never) {
			t.Errorf("the set-up used %s:\n%s", never, m.calls())
		}
	}

	// Again: nothing changes and the tunnel is not restarted.
	os.Remove(m.log)
	out = m.ok("setup")
	if !strings.Contains(out, "nothing to change") || strings.Contains(m.calls(), "bootstrap") || strings.Contains(m.calls(), "bootout") {
		t.Errorf("a second run changed something:\n%s\n%s", out, m.calls())
	}

	// A new copy of the tools, as a Maintenance run makes, restarts it.
	m.upgrade("cloudflared", "2.0")
	out = m.ok("setup")
	if !strings.Contains(m.calls(), "launchctl bootout system/"+cloudflaredLabel) || !strings.Contains(m.calls(), "launchctl bootstrap system "+daemon) || !m.tunnelLoaded() {
		t.Errorf("the tunnel was not restarted on the new copy:\n%s\n%s", out, m.calls())
	}

	// Another credential replaces the first and restarts it.
	os.Remove(m.log)
	other := credentialFile(t, strings.Replace(madeUpCredential, "eyJh", "eyJi", 1)+"\n", 0o600)
	m.ok("setup", "--tunnel-credential", other)
	if got := read(t, token); !strings.HasPrefix(got, "eyJi") || !strings.Contains(m.calls(), "launchctl bootstrap system "+daemon) {
		t.Errorf("the new credential was not installed, or the tunnel not restarted:\n%s", m.calls())
	}
}

func TestSetupWithNoCredentialLeavesTheTunnelStopped(t *testing.T) {
	m, k := withTunnel(t)
	out := m.ok("setup", "--release-key", k.signers)
	if !strings.Contains(out, "the tunnel is not started") || !strings.Contains(out, "--tunnel-credential") {
		t.Errorf("no word about the tunnel:\n%s", out)
	}
	if m.tunnelLoaded() || exists(m.path("Library/LaunchDaemons/"+cloudflaredLabel+".plist")) {
		t.Error("a tunnel with no credential was installed")
	}
	if out := m.ok("setup"); !strings.Contains(out, "nothing to change") {
		t.Errorf("a second run changed something:\n%s", out)
	}
}

func TestSetupRefusesWhatIsNotATunnelCredential(t *testing.T) {
	m, k := withTunnel(t)
	for name, file := range map[string]string{
		"not base64":         credentialFile(t, "hello, this is no credential\n", 0o600),
		"two lines":          credentialFile(t, madeUpCredential+"\n"+madeUpCredential+"\n", 0o600),
		"readable by others": credentialFile(t, madeUpCredential+"\n", 0o644),
		"missing":            filepath.Join(t.TempDir(), "none"),
	} {
		out := m.refused("setup", "--release-key", k.signers, "--tunnel-credential", file)
		if strings.Contains(out, madeUpCredential) {
			t.Errorf("%s: the credential was printed:\n%s", name, out)
		}
		if exists(m.path(strings.TrimPrefix(tunnelToken, "/"))) || exists(m.path("var/lab")) || strings.Contains(m.calls(), "-create") {
			t.Errorf("%s: something was installed:\n%s", name, out)
		}
	}
}

// Before the first Deploy there are no routes to run: the credential is put
// away and the tunnel waits for the set-up's next run.
func TestSetupBeforeTheFirstDeployKeepsTheCredentialAndWaits(t *testing.T) {
	m, k := withTunnel(t)
	os.Remove(m.path("usr/local/lab/current"))
	cred := credentialFile(t, madeUpCredential+"\n", 0o600)

	out := m.ok("setup", "--release-key", k.signers, "--tunnel-credential", cred)
	if !exists(m.path(strings.TrimPrefix(tunnelToken, "/"))) || m.tunnelLoaded() || !strings.Contains(out, "no build is Deployed") {
		t.Errorf("want the credential installed and the tunnel waiting for a Deploy:\n%s", out)
	}
	m.build("v0.2.0-rc.1")
	m.ok("setup")
	if !m.tunnelLoaded() {
		t.Errorf("the tunnel was not started after the Deploy:\n%s", m.calls())
	}
}

func (m *mini) installTunnel(loaded bool) {
	m.t.Helper()
	write(m.t, m.path("Library/LaunchDaemons/"+cloudflaredLabel+".plist"), "<plist/>\n", 0o644)
	if loaded {
		write(m.t, filepath.Join(filepath.Dir(m.root), "state", "loaded."+cloudflaredLabel), "", 0o644)
	}
}

func TestDeployVerifiesThroughTheFrontDoorOnceTheTunnelIsInstalled(t *testing.T) {
	m, k, r := deployed(t)
	d := m.frontDoor()
	for _, tag := range []string{"v0.2.0-rc.1", "v0.2.0-rc.2", "v0.2.0-rc.3"} {
		bundle(t, k, filepath.Join(r.dir, tag), tag, true)
	}

	// The first Deploy comes before the tunnel: labd is verified on the mini only.
	out := m.ok("deploy", "--from", r.from("v0.2.0-rc.1"), "v0.2.0-rc.1")
	if d.count() != 0 || !strings.Contains(out, "the tunnel is not installed") {
		t.Errorf("with no tunnel installed the Front door was asked %d times:\n%s", d.count(), out)
	}

	m.installTunnel(true)
	out = m.ok("deploy", "--dry-run", "--from", r.from("v0.2.0-rc.2"), "v0.2.0-rc.2")
	if !strings.Contains(out, "through the Front door") || d.count() != 0 {
		t.Errorf("the dry run: no word of the Front door, or it was asked:\n%s", out)
	}
	out = m.ok("deploy", "--from", r.from("v0.2.0-rc.2"), "v0.2.0-rc.2")
	if d.count() == 0 || !strings.Contains(out, "through the Front door") {
		t.Errorf("the Front door was not asked:\n%s", out)
	}

	// Dark through the Front door: the build is not kept.
	d.setDark(true)
	out = m.refused("deploy", "--from", r.from("v0.2.0-rc.3"), "v0.2.0-rc.3")
	if m.current() != "v0.2.0-rc.2" || !strings.Contains(out, "through the Front door") {
		t.Errorf("the mini runs %q after a Deploy the Front door did not answer:\n%s", m.current(), out)
	}
}

func TestDeployRestartsTheTunnelOnlyWhenItsRoutesChange(t *testing.T) {
	m, k, r := deployed(t)
	m.frontDoor()
	m.installTunnel(true)
	bundle(t, k, filepath.Join(r.dir, "v0.2.0-rc.1"), "v0.2.0-rc.1", true)
	bundle(t, k, filepath.Join(r.dir, "v0.2.0-rc.2"), "v0.2.0-rc.2", true)
	bundle(t, k, filepath.Join(r.dir, "v0.2.0-rc.3"), "v0.2.0-rc.3", true, "other routes")
	restart := "launchctl kickstart -k system/" + cloudflaredLabel

	m.ok("deploy", "--from", r.from("v0.2.0-rc.1"), "v0.2.0-rc.1")
	os.Remove(m.log)
	m.ok("deploy", "--from", r.from("v0.2.0-rc.2"), "v0.2.0-rc.2")
	if strings.Contains(m.calls(), restart) {
		t.Errorf("the tunnel was restarted though its routes are the same:\n%s", m.calls())
	}
	out := m.ok("deploy", "--from", r.from("v0.2.0-rc.3"), "v0.2.0-rc.3")
	if !strings.Contains(m.calls(), restart) || !strings.Contains(out, "routes") {
		t.Errorf("the tunnel was not restarted on new routes:\n%s\n%s", out, m.calls())
	}
	for _, never := range []string{"bootout system/" + cloudflaredLabel, "brew"} {
		if strings.Contains(m.calls(), never) {
			t.Errorf("a Deploy called %s:\n%s", never, m.calls())
		}
	}
}
