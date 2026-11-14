package minitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	signalLabel = "org.omarchy-mac.lab.signal-cli"
	signalPlist = "Library/LaunchDaemons/" + signalLabel + ".plist"
	signalCLI   = "usr/local/lab/tools/current/signal-cli/bin/signal-cli"
	signalData  = "var/lab/secrets/signal-cli"
	groupID     = "bGFiLWdyb3VwLWlkLWxhYi1ncm91cC1pZC1sYWItZ3I="
)

// The sender is signal-cli as the lab account, from the copy of the Homebrew
// tools only root can write, on a socket of the lab account, with no number,
// no group and no secret in its file, and nothing it receives written down.
func TestTheSendersLaunchDaemon(t *testing.T) {
	file := filepath.Join(repo(t), "deploy", signalLabel+".plist")
	values, arrays := plist(t, file)
	for key, want := range map[string]string{
		"Label":     shell(t, "SIGNAL_LABEL"),
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
	if shell(t, "SIGNAL_LABEL") != signalLabel {
		t.Errorf("SIGNAL_LABEL is %q", shell(t, "SIGNAL_LABEL"))
	}
	args := arrays["ProgramArguments"]
	want := []string{
		"/" + signalCLI,
		"--data-dir", "/" + signalData,
		"--scrub-log",
		"daemon",
		"--socket", "/var/lab/run/signal-cli.sock",
		"--receive-mode", "on-start",
		"--no-receive-stdout",
		"--ignore-attachments", "--ignore-stories", "--ignore-avatars", "--ignore-stickers",
	}
	if !slices.Equal(args, want) {
		t.Errorf("signal-cli is started as %v, want %v", args, want)
	}
	text := read(t, file)
	for _, word := range []string{"/opt/homebrew", "EnvironmentVariables", "TOKEN", "SECRET", "PASSWORD", "<string>-a</string>", "--account", "--tcp", "--http", "--dbus", "--send-read-receipts"} {
		if strings.Contains(text, word) {
			t.Errorf("the sender's LaunchDaemon file holds %s", word)
		}
	}
	if regexp.MustCompile(`\+\d{6,}`).MatchString(text) {
		t.Error("the sender's LaunchDaemon file holds a phone number")
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

	// The mini's scripts and labd name the same places.
	lib := read(t, filepath.Join(repo(t), "scripts", "mini", "lib.sh"))
	for _, line := range []string{
		`SIGNAL_CLI="$LAB_TOOLS/current/signal-cli/bin/signal-cli"`,
		`SIGNAL_DATA="$LAB_HOME/secrets/signal-cli"`,
		`SIGNAL_SOCKET="$LAB_HOME/run/signal-cli.sock"`,
	} {
		if !strings.Contains(lib, line) {
			t.Errorf("scripts/mini/lib.sh lacks %s", line)
		}
	}
	_, labd := plist(t, filepath.Join(repo(t), "deploy", labdLabel+".plist"))
	flags := strings.Join(labd["ProgramArguments"], " ")
	for _, want := range []string{"--signal-socket /var/lab/run/signal-cli.sock", "--signal /var/lab/secrets"} {
		if !strings.Contains(flags, want) {
			t.Errorf("labd is not started with %s", want)
		}
	}
	if !regexp.MustCompile(`(?m)^signal-cli\s*$`).MatchString(read(t, filepath.Join(repo(t), "deploy", "homebrew-formulae"))) {
		t.Error("deploy/homebrew-formulae does not name signal-cli")
	}
	// The files the owner's script writes are the ones labd reads.
	server := read(t, filepath.Join(repo(t), "internal", "server", "lines.go"))
	script := read(t, filepath.Join(repo(t), "scripts", "mini", "signal"))
	for _, name := range []string{"signal-lab-group", "signal-agents-group"} {
		if !strings.Contains(server, `"`+name+`"`) || !strings.Contains(script, "file="+name) {
			t.Errorf("labd and scripts/mini/signal do not both name %s", name)
		}
	}
}

// withSender has the made-up machine's set-up install the sender.
func (m *mini) withSender() {
	m.t.Helper()
	write(m.t, m.tree+"/deploy/homebrew-formulae", "signal-cli\n", 0o644)
}

func (m *mini) senderLoaded() bool {
	return exists(filepath.Join(filepath.Dir(m.root), "state", "loaded."+signalLabel))
}

func TestSetupStartsTheSenderFromTheCopy(t *testing.T) {
	need(t, "openssl")
	m := newMini(t)
	k := newKey(t)
	m.withSender()

	dry := m.ok("setup", "--dry-run", "--release-key", k.signers)
	for _, want := range []string{"install signal-cli from Homebrew", m.path(signalPlist), "start " + signalLabel} {
		if !strings.Contains(dry, want) {
			t.Errorf("the dry run doesn't mention %q:\n%s", want, dry)
		}
	}
	if exists(m.path(signalPlist)) || exists(m.path(signalData)) || m.senderLoaded() {
		t.Fatal("a dry run installed or started the sender")
	}

	out := m.ok("setup", "--release-key", k.signers)
	if read(t, m.path(signalPlist)) != read(t, filepath.Join(repo(t), "deploy", signalLabel+".plist")) || mode(t, m.path(signalPlist)) != 0o644 {
		t.Error("the installed LaunchDaemon file is not deploy/'s, or not 644")
	}
	if got := mode(t, m.path(signalData)); got != 0o700 {
		t.Errorf("the account's folder is %v, want 700", got)
	}
	if !exists(m.path(signalCLI)) {
		t.Fatal("signal-cli is not in the copy of the tools")
	}
	if !m.senderLoaded() || !strings.Contains(m.calls(), "launchctl bootstrap system "+m.path(signalPlist)) {
		t.Fatalf("the sender was not started:\n%s\n%s", out, m.calls())
	}
	if !strings.Contains(out, "scripts/mini/signal register") {
		t.Errorf("the set-up doesn't say the sender has no number yet:\n%s", out)
	}
	if strings.Contains(m.calls(), "brew services") || m.loaded() {
		t.Errorf("brew started a service, or the set-up started labd:\n%s", m.calls())
	}

	os.Remove(m.log)
	if out := m.ok("setup"); !strings.Contains(out, "nothing to change") || strings.Contains(m.calls(), "bootstrap") || strings.Contains(m.calls(), "bootout") {
		t.Errorf("a run with the sender up changed something:\n%s\n%s", out, m.calls())
	}

	// A Maintenance run's newer signal-cli: copied afresh, and the sender
	// started again so that it runs the new copy.
	m.upgrade("signal-cli", "2.0")
	os.Remove(m.log)
	m.ok("setup")
	calls := m.calls()
	if out, in := strings.Index(calls, "launchctl bootout system/"+signalLabel), strings.Index(calls, "launchctl bootstrap system "+m.path(signalPlist)); out < 0 || in < out {
		t.Errorf("the sender was not started again from the new copy:\n%s", calls)
	}
	if got := read(t, m.path(signalCLI)); !strings.Contains(got, "signal-cli 2.0") {
		t.Errorf("the copy in use: %q", got)
	}
}

// signalStub plays signal-cli run by hand: it writes down its arguments,
// "receives" what a member wrote, and lists one group.
const signalStub = `#!/bin/bash
echo "signal-cli $*" >> "$STUB_LOG"
case " $* " in
  *" receive "*) echo "Envelope from: +61491570157 Body: the launch code is tangerine" ;;
  *" listGroups "*) echo "Id: ` + groupID + ` Name: Omarchy Mac Lab  Active: true Blocked: false" ;;
esac
`

// signal starts scripts/mini/signal and types answers at its prompts.
func (m *mini) signal(answers string, args ...string) (string, int) {
	m.t.Helper()
	cmd := exec.Command(filepath.Join(m.tree, "scripts", "mini", "signal"), args...)
	cmd.Env = m.env
	cmd.Dir = filepath.Dir(m.tree)
	cmd.Stdin = strings.NewReader(answers)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		m.t.Fatalf("signal: %v\n%s", err, out)
	}
	return string(out), 0
}

func TestTheOwnerRegistersTheSenderAndGivesLabdItsGroups(t *testing.T) {
	need(t, "openssl")
	m := newMini(t)
	k := newKey(t)
	m.withSender()

	if out, code := m.signal("", "groups"); code == 0 || !strings.Contains(out, "scripts/mini/setup") {
		t.Fatalf("before the set-up: exit %d\n%s", code, out)
	}
	m.ok("setup", "--release-key", k.signers)
	os.Chmod(filepath.Dir(m.path(signalCLI)), 0o755)
	write(t, m.path(signalCLI), signalStub, 0o755)
	data := m.path(signalData)

	// A dry run asks nothing and runs nothing.
	os.Remove(m.log)
	for _, verb := range [][]string{{"register"}, {"groups"}, {"accept", groupID}, {"test"}, {"group", "lab", groupID}} {
		if out, code := m.signal("", append([]string{"--dry-run"}, verb...)...); code != 0 || !strings.Contains(out, "would") {
			t.Errorf("--dry-run %v: exit %d\n%s", verb, code, out)
		}
	}
	if exists(m.log) || exists(m.path("var/lab/secrets/signal-lab-group")) {
		t.Fatalf("a dry run called something or wrote a group id:\n%s", m.callsOrNone())
	}

	// What is no number, link or code is refused before signal-cli runs.
	for _, bad := range []string{"0491570156\n", "+61491570156\nhttps://example.org/\n"} {
		if out, code := m.signal(bad, "register"); code == 0 {
			t.Errorf("answers %q were taken:\n%s", bad, out)
		}
	}
	if strings.Contains(m.callsOrNone(), "signal-cli") {
		t.Fatalf("signal-cli ran on refused answers:\n%s", m.calls())
	}

	out, code := m.signal("+61491570156\nsignalcaptcha://signal-hcaptcha.abc.registration.def\n123-456\n", "register")
	if code != 0 {
		t.Fatalf("register: exit %d\n%s", code, out)
	}
	calls := m.calls()
	var order []int
	for _, want := range []string{
		"launchctl bootout system/" + signalLabel,
		"signal-cli --data-dir " + data + " -a +61491570156 register --captcha signalcaptcha://signal-hcaptcha.abc.registration.def",
		"signal-cli --data-dir " + data + " -a +61491570156 verify 123456",
		"signal-cli --data-dir " + data + " -a +61491570156 updateProfile --given-name Omarchy Mac Lab",
		"launchctl bootstrap system " + m.path(signalPlist),
	} {
		i := strings.Index(calls, want)
		if i < 0 {
			t.Fatalf("register did not run %q:\n%s", want, calls)
		}
		order = append(order, i)
	}
	if !slices.IsSorted(order) || !m.senderLoaded() {
		t.Errorf("register ran out of order, or left the sender stopped:\n%s", calls)
	}

	// The groups are listed; what members wrote is fetched and not shown.
	out, code = m.signal("", "groups")
	if code != 0 || !strings.Contains(out, "Id: "+groupID+" Name: Omarchy Mac Lab") {
		t.Fatalf("groups: exit %d\n%s", code, out)
	}
	if strings.Contains(out, "tangerine") || strings.Contains(out, "+61491570157") {
		t.Errorf("groups showed what a member wrote:\n%s", out)
	}
	if !strings.Contains(m.calls(), "signal-cli --data-dir "+data+" receive ") || !m.senderLoaded() {
		t.Errorf("groups did not fetch what waited, or left the sender stopped:\n%s", m.calls())
	}

	// labd is given each group's id in a file of the lab account alone.
	for group, file := range map[string]string{"lab": "signal-lab-group", "agents": "signal-agents-group"} {
		if out, code := m.signal("", "group", group, groupID); code != 0 {
			t.Fatalf("group %s: exit %d\n%s", group, code, out)
		}
		path := m.path("var/lab/secrets/" + file)
		if read(t, path) != groupID+"\n" || mode(t, path) != 0o600 {
			t.Errorf("%s holds %q with mode %v", file, read(t, path), mode(t, path))
		}
	}
	for _, bad := range [][]string{{"group", "lab", "not-an-id"}, {"group", "team", groupID}, {"accept", "x"}} {
		if out, code := m.signal("", bad...); code == 0 {
			t.Errorf("%v was taken:\n%s", bad, out)
		}
	}
	if out, code := m.signal("", "accept", groupID); code != 0 || !strings.Contains(m.calls(), "signal-cli --data-dir "+data+" updateGroup -g "+groupID) {
		t.Errorf("accept: exit %d\n%s", code, out)
	}

	// The test line is labd's own sender's, told where the socket and the ids are.
	if out, code := m.signal("", "test"); code == 0 || !strings.Contains(out, "no build is Deployed") {
		t.Errorf("test with no build: exit %d\n%s", code, out)
	}
	m.deployed("v0.2.0-rc.1", "")
	write(t, m.path("usr/local/lab/current/bin/labd"), "#!/bin/bash\necho \"labd $*\" >> \"$STUB_LOG\"\necho '\"Agents\": sent'\n", 0o755)
	out, code = m.signal("", "test")
	if code != 0 || !strings.Contains(out, `"Agents": sent`) {
		t.Fatalf("test: exit %d\n%s", code, out)
	}
	if want := "labd --signal-socket " + m.path("var/lab/run/signal-cli.sock") + " --signal " + m.path("var/lab/secrets") + " --signal-test"; !strings.Contains(m.calls(), want) {
		t.Errorf("labd was not asked for the test line as %q:\n%s", want, m.calls())
	}
}
