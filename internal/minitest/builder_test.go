package minitest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	containerLabel = "org.omarchy-mac.lab.container"
	builderLabel   = "org.omarchy-mac.lab.builder"
	builderToken   = "usr/local/lab/secrets/builder.token"
	madeUpToken    = "bldr_0123456789abcdef0123456789abcdef01234567"
	builderIO      = "usr/local/lab/io"
)

// container plays Apple's container: its server is up while the file "up"
// exists, and cannot be started while "broken" does. It writes down what it
// was asked.
const containerStub = `#!/bin/bash
echo "container $*" >> "$STUB_LOG"
s="$STUB_STATE/container"
mkdir -p "$s"
case "$1 $2" in
  "system status") [[ -f $s/up ]] || { echo "apiserver is not running and not registered with launchd" >&2; exit 1; } ;;
  "system start") [[ ! -f $s/broken ]] || { echo "could not start" >&2; exit 1; }; touch "$s/up" ;;
  "volume inspect") [[ -f $s/up && -f $s/volume.$3 ]] || exit 1 ;;
  "volume create") [[ -f $s/up ]] || exit 1; echo "$4" > "$s/volume.$5" ;;
  "image inspect") [[ -f $s/up && -f $s/image.$3 ]] || exit 1 ;;
  build\ *) [[ -f $s/up && -f $4/Containerfile ]] || exit 1; touch "$s/image.$3" ;;
  *) echo "container $*: not expected" >&2; exit 64 ;;
esac
`

// sudo -n -u USER COMMAND: written down, and COMMAND run.
const sudoStub = `#!/bin/bash
echo "sudo $*" >> "$STUB_LOG"
while [[ $1 == -* ]]; do
  case "$1" in -u) shift 2 ;; *) shift ;; esac
done
exec "$@"
`

// launchctl asuser UID COMMAND: written down, and COMMAND run; anything
// else is the made-up launchctl of every other test.
const asuserStub = `#!/bin/bash
if [[ $1 == asuser ]]; then
  echo "launchctl $*" >> "$STUB_LOG"
  shift 2
  exec "$@"
fi
exec "$(dirname "$0")/launchctl.real" "$@"
`

// builderMini is a made-up mini with container installed, stopped, and the
// Builder's files in the bundle the set-up runs from.
func builderMini(t *testing.T) *mini {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("container never runs as root, and these tests name the account running them")
	}
	m := newMini(t)
	if err := os.Rename(m.bin+"/launchctl", m.bin+"/launchctl.real"); err != nil {
		t.Fatal(err)
	}
	write(t, m.bin+"/launchctl", asuserStub, 0o755)
	write(t, m.bin+"/sudo", sudoStub, 0o755)
	write(t, m.path("usr/local/bin/container"), containerStub, 0o755)
	for _, label := range []string{containerLabel, builderLabel} {
		write(t, m.tree+"/deploy/"+label+".plist", read(t, filepath.Join(repo(t), "deploy", label+".plist")), 0o644)
	}
	if err := os.MkdirAll(m.path("var/lab/log"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.path("usr/local/lab"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.env = append(m.env, "LAB_BOOT=1", "LAB_START_TRIES=1", "LAB_WAIT_STEP=0.1")
	return m
}

// account is the name of the account running the tests, which stands in
// for the one container runs under.
func account(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func (m *mini) state(name string) string {
	return filepath.Join(filepath.Dir(m.root), "state", "container", name)
}

func (m *mini) serverUp() bool { return exists(m.state("up")) }

func (m *mini) kill() { os.Remove(m.state("up")) }

func (m *mini) touch(name string) { write(m.t, m.state(name), "", 0o644) }

// told is the lines "Agents" was told, without their times.
func (m *mini) told() []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(read(m.t, m.path("var/lab/log/notify.log"))), "\n") {
		if f := strings.SplitN(line, " ", 3); len(f) == 3 {
			out = append(out, f[2])
		}
	}
	return out
}

// builderBuild makes tag the Deployed build, with the Builder in it: the
// watch script beside what it sources, lab-builder, and the two images'
// folders.
func (m *mini) builderBuild(tag string) {
	m.t.Helper()
	m.deployed(tag, "")
	build := m.path("usr/local/lab/builds/" + tag)
	for _, name := range []string{"container", "lib.sh", "notify"} {
		write(m.t, build+"/scripts/mini/"+name, read(m.t, filepath.Join(m.tree, "scripts", "mini", name)), 0o755)
	}
	write(m.t, build+"/bin/lab-builder", "#!/bin/sh\n", 0o755)
	write(m.t, build+"/builder/Containerfile", "FROM scratch\n", 0o644)
	write(m.t, build+"/builder/pkg/Containerfile", "FROM scratch\n", 0o644)
}

// The two LaunchDaemon files: root's, from the Deployed build, with no
// secret. The one starts container's server at boot and looks every minute;
// the other waits for it and becomes lab-builder, with its token in a file.
func TestTheBuildersLaunchDaemons(t *testing.T) {
	watch := filepath.Join(repo(t), "deploy", containerLabel+".plist")
	values, arrays := plist(t, watch)
	for key, want := range map[string]string{"Label": containerLabel, "RunAtLoad": "true", "StartInterval": "60", "UserName": "", "KeepAlive": ""} {
		if values[key] != want {
			t.Errorf("%s: %s is %q, want %q", containerLabel, key, values[key], want)
		}
	}
	if got := strings.Join(arrays["ProgramArguments"], " "); got != "/usr/local/lab/current/scripts/mini/container watch CONTAINER_USER" {
		t.Errorf("container's server is watched by: %s", got)
	}

	file := filepath.Join(repo(t), "deploy", builderLabel+".plist")
	values, arrays = plist(t, file)
	for key, want := range map[string]string{"Label": builderLabel, "RunAtLoad": "true", "KeepAlive": "true", "Umask": "63", "UserName": ""} {
		if values[key] != want {
			t.Errorf("%s: %s is %q, want %q", builderLabel, key, values[key], want)
		}
	}
	want := "/usr/local/lab/current/scripts/mini/container wait CONTAINER_USER -- /usr/local/lab/current/bin/lab-builder" +
		" --server http://" + shell(t, "LABD_LISTEN") + " --token-file /usr/local/lab/secrets/builder.token --as-user CONTAINER_USER --io /usr/local/lab/io --cpus BUILDER_CPUS"
	if got := strings.Join(arrays["ProgramArguments"], " "); got != want {
		t.Errorf("the Builder is started as:\n %s\nwant\n %s", got, want)
	}
	main := read(t, filepath.Join(repo(t), "cmd", "lab-builder", "main.go"))
	for _, flag := range []string{"server", "token-file", "as-user", "io", "cpus"} {
		if !regexp.MustCompile(`fl\.\w+\((&[\w.]+, )?"` + flag + `"`).MatchString(main) {
			t.Errorf("lab-builder has no flag --%s", flag)
		}
	}
	// The scratch folder is the one the set-up makes: root's, in no home.
	if shell(t, "BUILDER_IO") != "$LAB_ROOT/io" || shell(t, "LAB_ROOT") != "$SYSROOT/usr/local/lab" || !strings.Contains(main, `miniScratch = "/usr/local/lab/io"`) {
		t.Errorf("the set-up's scratch folder is %s, and lab-builder's is another", shell(t, "BUILDER_IO"))
	}
	for _, f := range []string{watch, file} {
		text := read(t, f)
		for _, word := range []string{"EnvironmentVariables", "MACLAB_", "bldr_", "<string>--token</string>", "PASSWORD", "/opt/homebrew", "/Users/"} {
			if strings.Contains(text, word) {
				t.Errorf("%s holds %s", filepath.Base(f), word)
			}
		}
		if runtime.GOOS == "darwin" {
			if out, err := exec.Command("plutil", "-lint", f).CombinedOutput(); err != nil {
				t.Errorf("plutil: %v\n%s", err, out)
			}
		}
	}
	// Only the watch starts the server.
	if strings.Contains(main, `"system"`) || strings.Contains(main, "system start") {
		t.Error("lab-builder knows how to start container's server")
	}
	// The volumes the set-up makes are the sizes lab-builder says, and the
	// artifact store is labd's own 20 GB.
	if shell(t, "BUILDER_VOLUMES") != "maclab-cache:40G maclab-pkgcache:120G" ||
		!strings.Contains(main, `kernelVolumeSize  = "40G"`) || !strings.Contains(main, `packageVolumeSize = "120G"`) {
		t.Errorf("the volumes are %s", shell(t, "BUILDER_VOLUMES"))
	}
	if strings.Contains(read(t, filepath.Join(repo(t), "deploy", labdLabel+".plist")), "artifact-budget") ||
		!strings.Contains(read(t, filepath.Join(repo(t), "cmd", "labd", "main.go")), `"artifact-budget-gb", 20,`) {
		t.Error("the artifact store is not labd's own 20 GB")
	}
	// container is pinned by version and checksum.
	if !regexp.MustCompile(`(?m)^container [0-9]+\.[0-9]+\.[0-9]+ [0-9a-f]{64} https://github\.com/apple/container/releases/download/\S+\.pkg$`).MatchString(read(t, filepath.Join(repo(t), "deploy", "pins"))) {
		t.Error("deploy/pins has no line for container with its version, checksum and package")
	}
}

// At boot the server is started and nothing is said. Found stopped, it is
// started again through the wrapper in the same look. Each change is one
// line for "Agents", and no line is said twice in a row.
func TestTheWatchStartsContainerAgainAndTellsEachChangeOnce(t *testing.T) {
	m := builderMini(t)
	user := account(t)
	uid, _ := exec.Command("id", "-u").Output()
	wrapper := "launchctl asuser " + strings.TrimSpace(string(uid)) + " sudo -n -u " + user + " " + m.path("usr/local/bin/container")
	look := func(wantOK bool, wantUp bool, wantTold ...string) {
		t.Helper()
		out, code := m.run("container", "watch", user)
		if (code == 0) != wantOK || m.serverUp() != wantUp {
			t.Fatalf("exit %d, server up %v; want ok %v, up %v\n%s", code, m.serverUp(), wantOK, wantUp, out)
		}
		told := m.told()
		if len(told) != len(wantTold) {
			t.Fatalf("\"Agents\" was told %d lines, want %d:\n%s", len(told), len(wantTold), strings.Join(told, "\n"))
		}
		for i, want := range wantTold {
			if !strings.Contains(told[i], want) {
				t.Fatalf("line %d is %q, want it to say %q", i+1, told[i], want)
			}
		}
	}
	const (
		stopped = "found stopped"
		again   = "started again"
		failed  = "could not be started"
		back    = "running again"
	)

	look(true, true)
	calls := m.calls()
	if !strings.Contains(calls, wrapper+" system status") || !strings.Contains(calls, wrapper+" system start --enable-kernel-install") {
		t.Fatalf("the server was not started through the wrapper:\n%s", calls)
	}
	for _, line := range strings.Split(calls, "\n") {
		if strings.HasPrefix(line, "container ") && !strings.Contains(calls, "sudo -n -u "+user+" "+m.path("usr/local/bin/container")+strings.TrimPrefix(line, "container")) {
			t.Errorf("container was run outside the wrapper: %s", line)
		}
	}
	look(true, true)

	m.kill()
	look(true, true, stopped, again)
	look(true, true, stopped, again)

	m.kill()
	m.touch("broken")
	look(false, false, stopped, again, stopped, failed)
	look(false, false, stopped, again, stopped, failed)
	look(false, false, stopped, again, stopped, failed)
	os.Remove(m.state("broken"))
	look(true, true, stopped, again, stopped, failed, again)

	// Stopped, not startable, then started by someone else.
	m.kill()
	m.touch("broken")
	look(false, false, stopped, again, stopped, failed, again, stopped, failed)
	m.touch("up")
	look(true, true, stopped, again, stopped, failed, again, stopped, failed, back)
	look(true, true, stopped, again, stopped, failed, again, stopped, failed, back)

	// Each line went out as the lab account, whose log it is.
	if n := strings.Count(m.calls(), "as-lab "+filepath.Join(m.tree, "scripts", "mini", "notify")+" agents "); n != 8 {
		t.Errorf("%d lines were handed to the lab account, want 8", n)
	}

	// After a restart of the mini the server is stopped, and starting it is
	// no news; a server that cannot be started then is.
	m.kill()
	os.Remove(m.state("broken"))
	m.env = append(m.env, "LAB_BOOT=2")
	look(true, true, stopped, again, stopped, failed, again, stopped, failed, back)
	m.kill()
	m.touch("broken")
	m.env = append(m.env, "LAB_BOOT=3")
	look(false, false, stopped, again, stopped, failed, again, stopped, failed, back, failed)
}

// The Builder's LaunchDaemon waits until the server answers and starts
// nothing itself.
func TestTheBuilderWaitsForContainerAndNeverStartsIt(t *testing.T) {
	m := builderMini(t)
	cmd := exec.Command(filepath.Join(m.tree, "scripts", "mini", "container"), "wait", account(t), "--", "/bin/echo", "lab-builder would poll now")
	cmd.Env = m.env
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("it did not wait for the server: %v\n%s", err, out.String())
	case <-time.After(time.Second):
	}
	if strings.Contains(m.calls(), "system start") || m.serverUp() {
		t.Fatalf("waiting started the server:\n%s", m.calls())
	}
	m.touch("up")
	select {
	case err := <-done:
		if err != nil || !strings.Contains(out.String(), "lab-builder would poll now") {
			t.Fatalf("after the server came up: %v\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("it went on waiting with the server up")
	}
	if strings.Contains(m.calls(), "system start") {
		t.Fatalf("waiting started the server:\n%s", m.calls())
	}
}

func tokenFile(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "mini.token")
	write(t, file, text, mode)
	return file
}

func TestSetupRefusesWhatIsNoAccountNoTokenAndNoLimit(t *testing.T) {
	m := builderMini(t)
	k := newKey(t)
	good := tokenFile(t, madeUpToken, 0o600)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	for what, args := range map[string][]string{
		"an account that is not there":    {"--container-user", "nobody-of-that-name"},
		"root":                            {"--container-user", "root"},
		"the lab account":                 {"--container-user", "lab"},
		"no name":                         {"--container-user", "../x"},
		"twelve cores":                    {"--builder-cpus", "12"},
		"a token others read":             {"--builder-token", tokenFile(t, madeUpToken, 0o644)},
		"a token that is a link":          {"--builder-token", link},
		"a file that holds no token":      {"--builder-token", tokenFile(t, "lab_0123456789abcdef0123456789abcdef01234567", 0o600)},
		"a file that holds two":           {"--builder-token", tokenFile(t, madeUpToken+"\n"+madeUpToken+"\n", 0o600)},
		"a token file that is not there":  {"--builder-token", filepath.Join(t.TempDir(), "none")},
		"a token with something after it": {"--builder-token", tokenFile(t, madeUpToken+" x", 0o600)},
	} {
		out := m.refused("setup", append([]string{"--release-key", k.signers}, args...)...)
		if strings.Contains(out, madeUpToken) {
			t.Errorf("%s: the refusal shows the token:\n%s", what, out)
		}
	}
	if exists(m.path(builderToken)) || exists(m.path("usr/local/lab/container-user")) || strings.Contains(m.calls(), "container ") {
		t.Errorf("a refused run changed something:\n%s", m.calls())
	}
	if !exists(good) {
		t.Error("a refused run removed a token file")
	}
}

// The set-up names the account once, starts the watch, makes the volumes at
// their sizes and the images, puts the token where only root reads it and
// starts the Builder; a second run changes nothing.
func TestSetupBringsTheBuilderUp(t *testing.T) {
	need(t, "openssl")
	m := builderMini(t)
	k := newKey(t)
	user := account(t)
	containerPlist := m.path("Library/LaunchDaemons/" + containerLabel + ".plist")
	builderPlist := m.path("Library/LaunchDaemons/" + builderLabel + ".plist")
	loaded := func(label string) bool { return exists(filepath.Join(filepath.Dir(m.root), "state", "loaded."+label)) }

	// Before anything is named, and before a Deploy.
	out := m.ok("setup", "--release-key", k.signers)
	if !strings.Contains(out, "--container-user") || exists(containerPlist) {
		t.Fatalf("with no account named:\n%s", out)
	}
	out = m.ok("setup", "--container-user", user)
	if !strings.Contains(out, "once a build is Deployed") || !exists(containerPlist) || loaded(containerLabel) || strings.Contains(m.calls(), "container ") {
		t.Fatalf("before a Deploy:\n%s", out)
	}
	if info, err := os.Lstat(m.path(builderIO)); err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
		t.Fatalf("the Builder's scratch folder was not made for the account to reach: %v %v", info, err)
	}
	m.builderBuild("v0.2.0")

	token := tokenFile(t, madeUpToken+"\n", 0o600)
	dry := m.ok("setup", "--dry-run", "--builder-token", token)
	if !strings.Contains(dry, "would") || loaded(containerLabel) || m.serverUp() || !exists(token) || exists(m.path(builderToken)) {
		t.Fatalf("the dry run:\n%s", dry)
	}

	out = m.ok("setup")
	if !loaded(containerLabel) || !m.serverUp() || loaded(builderLabel) || !strings.Contains(out, "lab builder add mini --kinds kernel,package") {
		t.Fatalf("without a token the Builder waits, and the rest is up:\n%s", out)
	}
	calls := m.calls()
	for _, want := range []string{
		"container volume create -s 40G maclab-cache",
		"container volume create -s 120G maclab-pkgcache",
		"container build -t maclab-kbuild:1 " + m.path("usr/local/lab/current/builder"),
		"container build -t maclab-pkgbuild:1 " + m.path("usr/local/lab/current/builder/pkg"),
		"sudo -n -u " + user + " " + m.path("usr/local/bin/container") + " system start --enable-kernel-install",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("the set-up did not run: %s", want)
		}
	}
	if len(m.told()) != 0 {
		t.Errorf("the set-up's own start told \"Agents\": %v", m.told())
	}

	out = m.ok("setup", "--builder-token", token)
	if strings.Contains(out, madeUpToken) || strings.Contains(m.calls(), madeUpToken) {
		t.Fatal("the token was shown or handed to a command")
	}
	if exists(token) || strings.TrimSpace(read(t, m.path(builderToken))) != madeUpToken || mode(t, m.path(builderToken)) != 0o600 {
		t.Fatalf("the token is not root's alone, or its first file is still there:\n%s", out)
	}
	if !loaded(builderLabel) {
		t.Fatalf("the Builder was not started:\n%s", out)
	}
	for file, wants := range map[string][]string{
		containerPlist: {"<string>watch</string>\n    <string>" + user + "</string>"},
		builderPlist:   {"<string>--as-user</string><string>" + user + "</string>", "<string>--io</string><string>/usr/local/lab/io</string>", "<string>--cpus</string><string>10</string>", "<string>wait</string>\n    <string>" + user + "</string>"},
	} {
		text := read(t, file)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %s", filepath.Base(file), want)
			}
		}
		if strings.Contains(text, "CONTAINER_USER") || strings.Contains(text, "BUILDER_CPUS") || mode(t, file) != 0o644 {
			t.Errorf("%s was not filled in, or is not 0644", filepath.Base(file))
		}
	}

	os.Remove(m.log)
	if out := m.ok("setup"); !strings.Contains(out, "nothing to change") {
		t.Fatalf("a second run:\n%s", out)
	}
	if calls := m.calls(); strings.Contains(calls, "bootstrap") || strings.Contains(calls, "create") || strings.Contains(calls, "container build") || strings.Contains(calls, "system start") {
		t.Fatalf("a second run did something:\n%s", calls)
	}

	// Fewer cores after a failed gate test: the Builder alone starts again,
	// and the setting stays.
	os.Remove(m.log)
	out = m.ok("setup", "--builder-cpus", "8")
	if !strings.Contains(read(t, builderPlist), "<string>--cpus</string><string>8</string>") ||
		strings.Count(m.calls(), "launchctl bootstrap") != 1 || !strings.Contains(m.calls(), "bootstrap system "+builderPlist) {
		t.Fatalf("at 8 cores:\n%s\n%s", out, m.calls())
	}
	if out := m.ok("setup"); !strings.Contains(out, "nothing to change") || !strings.Contains(read(t, builderPlist), "<string>8</string>") {
		t.Fatalf("after the change of cores:\n%s", out)
	}

	// The server killed by hand: the set-up starts it again too.
	m.kill()
	m.ok("setup")
	if !m.serverUp() {
		t.Fatal("the set-up left the server stopped")
	}
}

// Given the installed token as its own source, the set-up would find it
// "already" in place and remove it: the Builder could not start again until
// a Maintainer adds a builder anew.
func TestSetupRefusesItsOwnInstalledBuilderTokenAsTheSource(t *testing.T) {
	need(t, "openssl")
	m := builderMini(t)
	k := newKey(t)
	m.ok("setup", "--release-key", k.signers, "--container-user", account(t), "--builder-token", tokenFile(t, madeUpToken+"\n", 0o600))
	installed := m.path(builderToken)
	if strings.TrimSpace(read(t, installed)) != madeUpToken {
		t.Fatalf("the token was not installed at %s", installed)
	}
	os.Remove(m.log)

	for _, args := range [][]string{{"--builder-token", installed}, {"--dry-run", "--builder-token", installed}} {
		out := m.refused("setup", args...)
		if !strings.Contains(out, "is where the set-up keeps the Builder's token") {
			t.Errorf("setup %v does not say why:\n%s", args, out)
		}
		if strings.Contains(out, "setup: remove ") || strings.Contains(out, "would remove ") {
			t.Errorf("setup %v went on to remove it:\n%s", args, out)
		}
		if strings.Contains(out, madeUpToken) {
			t.Errorf("setup %v shows the token:\n%s", args, out)
		}
	}
	if got := read(t, installed); got != madeUpToken+"\n" || mode(t, installed) != 0o600 {
		t.Fatalf("the Builder's token is %d bytes at %v after the set-up was given it as its own source", len(got), mode(t, installed))
	}
	if strings.Contains(m.callsOrNone(), "bootout") {
		t.Errorf("a refused run stopped something:\n%s", m.callsOrNone())
	}
}

// The scratch folder is root's own. A link at its name is refused: root
// would hand every build's folder to whatever the link names.
func TestSetupRefusesALinkAtTheBuildersScratchFolder(t *testing.T) {
	need(t, "openssl")
	m := builderMini(t)
	k := newKey(t)
	v := m.victim()
	plant(t, m.path(builderIO), v.dir)

	out := m.refused("setup", "--release-key", k.signers, "--container-user", account(t))

	v.intact("a link at the Builder's scratch folder")
	saysLink(t, out, m.path(builderIO))
	if exists(m.path("Library/LaunchDaemons/" + builderLabel + ".plist")) {
		t.Errorf("the Builder's LaunchDaemon file was installed all the same:\n%s", out)
	}
}

// The watch runs as root and tells "Agents" through a log the lab account
// owns: a link planted there is never written through.
func TestTheWatchWritesThroughNoLinkPlantedAtTheLog(t *testing.T) {
	m := builderMini(t)
	v := m.victim()
	user := account(t)
	m.ok("container", "watch", user)
	plant(t, m.path("var/lab/log/notify.log"), v.file)
	m.kill()

	out := m.ok("container", "watch", user)

	v.intact("a link at the log of \"Agents\"")
	if !m.serverUp() {
		t.Error("the server was not started again")
	}
	saysLink(t, out, m.path("var/lab/log/notify.log"))
	if !strings.Contains(out, "found stopped") || !strings.Contains(out, "that line was not sent") {
		t.Errorf("the watch's own log does not carry the lines:\n%s", out)
	}
	if n := strings.Count(m.calls(), "as-lab "); n != 2 {
		t.Errorf("%d lines were handed to the lab account, want 2:\n%s", n, m.calls())
	}
}
