package cutovertest

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

const mib = 1 << 20

// local runs one of the tests with --local against the binaries built here.
func local(t *testing.T, name string, args ...string) (string, int) {
	t.Helper()
	return run(t, nil, "", frontDoor(t, name), append([]string{"--local", "--bin", bins(t)}, args...)...)
}

// The Front door's test 4 against a labd on loopback: lab upload is killed
// while it sends, the same command resumes, and the checksum matches.
func TestFD04CutsAnUploadAndResumesIt(t *testing.T) {
	records := t.TempDir()
	out, code := local(t, "fd04-upload-resume", "--size", fmt.Sprint(200*mib), "--records", records)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cut := regexp.MustCompile(`cut: lab upload killed while it sent, with (\d) of 7 parts taken by labd and [1-9]\d* bytes of another received`).FindStringSubmatch(out)
	m := regexp.MustCompile(`resume: labd held (\d) of 7 parts; the same command sent the other (\d) in`).FindStringSubmatch(out)
	if cut == nil || m == nil || cut[1] < "2" || m[1] < cut[1] || m[1] == "7" || m[1][0]-'0'+m[2][0]-'0' != 7 {
		t.Errorf("no cut with two parts held, or no resume from at least those and not all:\n%s", out)
	}
	for _, want := range []string{"file: 209715200 bytes, 7 parts of 32 MiB", "cut: lab upload killed while it sent", "checksum: the SHA-256 labd stored is the file's", ": pass"} {
		if !strings.Contains(out, want) {
			t.Errorf("the run does not say %q:\n%s", want, out)
		}
	}
	files, _ := filepath.Glob(filepath.Join(records, "*-front-door-04.md"))
	if len(files) != 1 {
		t.Fatalf("records: %v", files)
	}
	record := read(t, files[0])
	for _, want := range []string{"# Front door test 4:", "--local", "It proves the script, not the Front door.", "- resume: labd held", "Result: pass"} {
		if !strings.Contains(record, want) {
			t.Errorf("the record lacks %q:\n%s", want, record)
		}
	}
	if out, code := local(t, "fd04-upload-resume", "--size", fmt.Sprint(100*mib)); code == 0 || !strings.Contains(out, "longer than four parts") {
		t.Errorf("a file a cut cannot split was taken: exit %d\n%s", code, out)
	}
}

// With resuming turned off the test fails: the run after the cut does not
// say it resumed.
func TestFD04FailsWhenNoRunResumes(t *testing.T) {
	real := bins(t)
	dir := t.TempDir()
	for _, tool := range []string{"labd", "lab-agent"} {
		if err := os.Symlink(filepath.Join(real, tool), filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := "#!/bin/bash\nif [[ $1 == upload ]]; then exec \"" + real + "/lab\" \"$@\" 2> >(grep --line-buffered -v resuming >&2); fi\nexec \"" + real + "/lab\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "lab"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, nil, "", frontDoor(t, "fd04-upload-resume"), "--local", "--bin", dir, "--size", fmt.Sprint(200*mib))
	if code != 1 || !strings.Contains(out, ": fail") || !strings.Contains(out, "did not say it resumed") || strings.Contains(out, "checksum") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// A cut counts only inside a part: a lab that says labd takes its parts and
// sends none, so that labd never holds part of one, does not pass, though
// the run after it says what a resume says and prints the right SHA-256.
func TestFD04BetweenTwoPartsDoesNotPass(t *testing.T) {
	real := bins(t)
	dir := t.TempDir()
	for _, tool := range []string{"labd", "lab-agent"} {
		if err := os.Symlink(filepath.Join(real, tool), filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := `#!/bin/bash
[[ $1 == upload ]] || exec "` + real + `/lab" "$@"
if [[ ${MACLAB_UPLOAD_PROGRESS:-} == 1 ]]; then
  for n in 1 2 3 4 5 6; do echo "lab: labd holds $n of 7 parts" >&2; sleep 0.3; done
  exit 1
fi
echo "lab: resuming: labd holds 2 of 7 parts" >&2
shasum -a 256 "$2" | cut -d' ' -f1
`
	if err := os.WriteFile(filepath.Join(dir, "lab"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, nil, "", frontDoor(t, "fd04-upload-resume"), "--local", "--bin", dir, "--size", fmt.Sprint(200*mib))
	if code != 1 || !strings.Contains(out, ": fail") || !strings.Contains(out, "while labd held part of a part") || strings.Contains(out, "checksum") {
		t.Errorf("exit %d\n%s", code, out)
	}
	// Through the Lab address labd's upload folder is on the mini.
	if out, code := run(t, nil, "", frontDoor(t, "fd04-upload-resume"), "--lab"); code != 1 || !strings.Contains(out, "--lab needs --mini USER@HOST") {
		t.Errorf("--lab with no --mini: exit %d\n%s", code, out)
	}
}

// A part seen partly received when lab is stopped and finished after the
// kill, from bytes lab had sent before it, was not cut: FD 4 fails though
// the resume says it resumed and prints the right SHA-256.
func TestFD04APartFinishedAfterTheKillDoesNotPass(t *testing.T) {
	real := bins(t)
	dir := t.TempDir()
	for _, tool := range []string{"labd", "lab-agent"} {
		if err := os.Symlink(filepath.Join(real, tool), filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := `#!/bin/bash
[[ $1 == upload ]] || exec "` + real + `/lab" "$@"
sha="$(shasum -a 256 "$2" | cut -d' ' -f1)"
folder="$(dirname "$HOME")/root/var/lab/data/artifacts/.upload-$sha"
if [[ ${MACLAB_UPLOAD_PROGRESS:-} == 1 ]]; then
  mkdir -p "$folder"
  echo one > "$folder/1"
  echo two > "$folder/2"
  head -c 1000 /dev/zero > "$folder/.part-h3"
  me=$$
  # What lab had sent of part 3 reaches labd after the kill, which makes
  # the part whole.
  ( while kill -0 "$me" 2> /dev/null; do sleep 0.05; done; mv "$folder/.part-h3" "$folder/3" ) > /dev/null 2>&1 &
  echo "lab: labd holds 2 of 7 parts" >&2
  sleep 30
  exit 1
fi
echo "lab: resuming: labd holds 3 of 7 parts" >&2
echo "$sha"
`
	if err := os.WriteFile(filepath.Join(dir, "lab"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, nil, "", frontDoor(t, "fd04-upload-resume"), "--local", "--bin", dir, "--size", fmt.Sprint(200*mib))
	if code != 1 || !strings.Contains(out, ": fail") || !strings.Contains(out, "no part was cut mid-way") || strings.Contains(out, "checksum") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// A part labd holds whole already and receives again, a retry, is no part
// cut mid-way when the retry is cut: its file of its own goes, the counts
// match, and nothing is resent. FD 4 knows a file labd receives by its part,
// from its first bytes, and fails.
func TestFD04ARetryOfAPartHeldWholeDoesNotPass(t *testing.T) {
	real := bins(t)
	dir := t.TempDir()
	for _, tool := range []string{"labd", "lab-agent"} {
		if err := os.Symlink(filepath.Join(real, tool), filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	wrapper := `#!/bin/bash
[[ $1 == upload ]] || exec "` + real + `/lab" "$@"
sha="$(shasum -a 256 "$2" | cut -d' ' -f1)"
folder="$(dirname "$HOME")/root/var/lab/data/artifacts/.upload-$sha"
if [[ ${MACLAB_UPLOAD_PROGRESS:-} == 1 ]]; then
  mkdir -p "$folder"
  dd if="$2" of="$folder/1" bs=1048576 count=32 2> /dev/null
  dd if="$2" of="$folder/2" bs=1048576 skip=32 count=32 2> /dev/null
  # Part 2 sent again and cut after 1000 bytes: labd drops its file once
  # lab is gone.
  dd if="$2" bs=1048576 skip=32 count=1 2> /dev/null | head -c 1000 > "$folder/.part-r2"
  me=$$
  ( while kill -0 "$me" 2> /dev/null; do sleep 0.05; done; rm -f "$folder/.part-r2" ) > /dev/null 2>&1 &
  echo "lab: labd holds 2 of 7 parts" >&2
  sleep 30
  exit 1
fi
echo "lab: resuming: labd holds 2 of 7 parts" >&2
echo "$sha"
`
	if err := os.WriteFile(filepath.Join(dir, "lab"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, nil, "", frontDoor(t, "fd04-upload-resume"), "--local", "--bin", dir, "--size", fmt.Sprint(200*mib))
	if code != 1 || !strings.Contains(out, ": fail") || !strings.Contains(out, "part 2, which it held whole already") || strings.Contains(out, "checksum") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// A stop while lab is stopped for the cut (here, while labd's folder is
// read) ends the run: the stopped lab is killed, not waited for.
func TestFD04StoppedWhileLabIsStoppedEnds(t *testing.T) {
	bin := bins(t)
	stubs := t.TempDir()
	mark := filepath.Join(stubs, "looking")
	if err := os.WriteFile(filepath.Join(stubs, "find"), []byte("#!/bin/bash\ntouch \""+mark+"\"\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(frontDoor(t, "fd04-upload-resume"), "--local", "--bin", bin, "--size", fmt.Sprint(200*mib))
	cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	for end := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the run never stopped lab to look at labd's folder")
		}
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end: its cleanup waits for the stopped lab")
	}
}

// The Front door's tests 15 and 16 against a labd on loopback: the
// measuring and the decision, which is shown and written nowhere.
func TestTheTransferTestsRunAgainstALocalLabd(t *testing.T) {
	added := recordsAdded(t, "*front-door*")
	plist := read(t, filepath.Join(repo(t), "deploy", "org.omarchy-mac.lab.labd.plist"))
	routes := read(t, filepath.Join(repo(t), "deploy", "cloudflared.yml"))
	for name, wants := range map[string][]string{
		"fd15-transport":   {"quic, up: 50331648 bytes in ", "quic, down: 50331648 bytes in ", "http2, up: ", "http2, down: ", "of a core", "Mbit/s over both directions", ": pass"},
		"fd16-performance": {"target 1: pass. first byte in ", "target 2: pass. labd took ", " requests for 1", "target 3: pass.", "target 4: pass.", ": pass"},
	} {
		out, code := local(t, name)
		if code != 0 {
			t.Errorf("%s --local: exit %d\n%s", name, code, out)
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("%s --local does not say %q:\n%s", name, want, out)
			}
		}
	}
	if read(t, filepath.Join(repo(t), "deploy", "org.omarchy-mac.lab.labd.plist")) != plist || read(t, filepath.Join(repo(t), "deploy", "cloudflared.yml")) != routes {
		t.Error("a local run wrote in deploy/")
	}
	if entries := added(); len(entries) != 0 {
		t.Errorf("a local run left a record in docs/acceptance: %v", entries)
	}
}

// madeUpLab is a labd on loopback with sign-in through a stand-in for
// GitHub, an Admin (owner) with a login in a home of their own, and a
// simulated m1-pro with its baseline passed.
type madeUpLab struct {
	t         *testing.T
	url       string
	root      string
	ownerHome string
	bin       string
}

func (l *madeUpLab) env(home string) []string {
	return []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config", "XDG_STATE_HOME=" + home + "/.local/state", "XDG_DATA_HOME=" + home + "/.local/share", "XDG_CACHE_HOME=" + home + "/.cache", "TMPDIR=" + home}
}

// start runs a program until the test ends and returns the address it says
// it serves at.
func start(t *testing.T, said *regexp.Regexp, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	pipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			if m := said.FindStringSubmatch(sc.Text()); m != nil {
				select {
				case found <- m[1]:
				default:
				}
			}
		}
	}()
	select {
	case addr := <-found:
		return addr
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not say where it serves", filepath.Base(name))
		return ""
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func newMadeUpLab(t *testing.T) *madeUpLab {
	t.Helper()
	l := &madeUpLab{t: t, bin: bins(t), root: t.TempDir(), ownerHome: t.TempDir()}
	signIn := filepath.Join(l.root, "sign-in")
	os.MkdirAll(signIn, 0o700)
	github := start(t, regexp.MustCompile(`on (http://127\.0\.0\.1:\d+) with`), filepath.Join(l.bin, "fake-github"), "--listen", freePort(t), "--files", signIn, "--user", "owner:1001")
	if err := os.WriteFile(filepath.Join(signIn, "admins"), []byte("1001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(l.root, "var", "lab", "data")
	l.url = start(t, regexp.MustCompile(`labd listening on (http://127\.0\.0\.1:\d+)`), filepath.Join(l.bin, "labd"), "--listen=127.0.0.1:0", "--data="+data, "--sign-in="+signIn, "--github-url="+github, "--omt=false")

	token, code := run(t, l.env(l.ownerHome), "", "/bin/sh", "-c", `"$0" break-glass --data="$1" --account-id=1001 owner 2>/dev/null`, filepath.Join(l.bin, "labd"), data)
	if code != 0 {
		t.Fatalf("break-glass: %s", token)
	}
	l.lab(l.ownerHome, token, "login", l.url)
	enroll := l.lab(l.ownerHome, "", "enroll", "m1-pro", "--model", "desktop")
	enr := regexp.MustCompile(`enr_[0-9a-f]+`).FindString(enroll)
	if enr == "" {
		t.Fatalf("no Join command:\n%s", enroll)
	}
	sim := exec.Command(filepath.Join(l.bin, "lab-agent"), "sim", "--server", l.url, "--token", enr)
	sim.Env = append(os.Environ(), l.env(t.TempDir())...)
	if err := sim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sim.Process.Kill(); sim.Wait() })
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if out, code := run(t, l.env(l.ownerHome), "", filepath.Join(l.bin, "lab"), "status", "m1-pro"); code == 0 && strings.Contains(out, "m1-pro") {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the simulated Mac did not Join")
		}
	}
	l.lab(l.ownerHome, "", "baseline", "m1-pro")
	return l
}

// lab runs lab under a home, and fails the test when lab does.
func (l *madeUpLab) lab(home, stdin string, args ...string) string {
	l.t.Helper()
	out, code := run(l.t, l.env(home), stdin, filepath.Join(l.bin, "lab"), args...)
	if code != 0 {
		l.t.Fatalf("lab %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

// owner is the environment of the owner's own run through the Lab address,
// which here is the labd on loopback.
func (l *madeUpLab) owner(address string) []string {
	home := l.ownerHome
	if address != l.url {
		// The same lab behind another address: the owner logs in there.
		home = l.t.TempDir()
		token, code := run(l.t, l.env(home), "", "/bin/sh", "-c", `"$0" break-glass --data="$1" --account-id=1001 owner 2>/dev/null`, filepath.Join(l.bin, "labd"), filepath.Join(l.root, "var", "lab", "data"))
		if code != 0 {
			l.t.Fatalf("break-glass: %s", token)
		}
		l.lab(home, token, "login", address)
	}
	return append(l.env(home), "LAB_ADDRESS="+address, "LAB_COMMAND="+filepath.Join(l.bin, "lab"))
}

// transferJob is the owner's side of the download: the job that leaves the
// file on the Mac, run under the owner's login as the tests run it when no
// --job is given. The simulated Mac runs no script, so the test plays its
// part and writes the file among the job's.
func (l *madeUpLab) transferJob() string {
	l.t.Helper()
	out := l.lab(l.ownerHome, "", "run", "m1-pro", "--script", frontDoor(l.t, "transfer-file.sh"))
	m := regexp.MustCompile(`(?m)^job (j[0-9a-z-]+) queued`).FindStringSubmatch(out)
	if m == nil {
		l.t.Fatalf("lab run did not name its job:\n%s", out)
	}
	made := filepath.Join(l.root, "var", "lab", "data", "jobs", m[1], "test-transfer-file")
	os.MkdirAll(made, 0o755)
	if out, code := run(l.t, []string{"MACLAB_OUT=" + made, "TRANSFER_BYTES=" + fmt.Sprint(8*mib)}, "", "/bin/bash", frontDoor(l.t, "transfer-file.sh")); code != 0 {
		l.t.Fatalf("transfer-file.sh: %s", out)
	}
	return m[1]
}

// ssh plays the mini as the owner reaches it: it restarts no tunnel, counts
// CPU seconds as cloudflared's metrics would, and runs the real page-times
// against the labd of the test, after the line sudo would ask with.
const sshStub = `#!/bin/bash
echo "ssh $*" >> "$SSH_STATE/calls"
case "$*" in
  *"/transport http2") [[ ! -f $SSH_STATE/http2-fails ]] || { echo "transport: the tunnel did not connect under http2" >&2; exit 1; } ;;
  *"/transport quic"|*"/transport deployed") ;;
  *"/metrics")
    n=$(( $(cat "$SSH_STATE/cpu" 2>/dev/null || echo 0) + ${SSH_CPU_STEP:-1} ))
    echo "$n" > "$SSH_STATE/cpu"
    echo "# TYPE process_cpu_seconds_total counter"
    awk -v n="$n" 'BEGIN { printf "process_cpu_seconds_total %.2f\n", n / 100 }' ;;
  *"sudo -u lab /usr/local/lab/current/scripts/mini/page-times")
    printf 'Password:'
    LAB_SYSROOT="$SSH_ROOT" LAB_LOOPBACK="$SSH_LOOPBACK" "$SSH_REPO/scripts/mini/page-times" ;;
  *) echo "ssh: not expected: $*" >&2; exit 64 ;;
esac
`

// mini puts the stand-in for ssh first in PATH and returns the environment
// and the file its calls are written to.
func (l *madeUpLab) mini(address string) (env []string, state string) {
	l.t.Helper()
	state = l.t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "ssh"), []byte(sshStub), 0o755); err != nil {
		l.t.Fatal(err)
	}
	root, _ := filepath.Abs(repo(l.t))
	return append(l.owner(address), "PATH="+state+":"+os.Getenv("PATH"), "SSH_STATE="+state, "SSH_ROOT="+l.root,
		"SSH_LOOPBACK="+strings.TrimPrefix(l.url, "http://"), "SSH_REPO="+root), state
}

func calls(t *testing.T, state string) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(state, "calls"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// The Front door's test 15 as the owner runs it, with the mini played: the
// tunnel is put under QUIC, then HTTP/2, then back under the Deployed
// files, cloudflared's CPU is read around each transfer, and the transport
// that won is written in deploy/.
func TestFD15SwitchesTheTunnelMeasuresBothAndPutsItBack(t *testing.T) {
	l := newMadeUpLab(t)
	job := l.transferJob()
	const transport = "ssh -t owner@mini sudo /usr/local/lab/current/scripts/mini/transport "
	const metrics = "ssh owner@mini curl -fsS --max-time 10 http://127.0.0.1:20241/metrics"

	for _, lacking := range [][]string{{"--job", job}, {"--mini", "owner@mini; reboot", "--job", job}, {"--mini", "owner@mini", "--job", "../etc"}} {
		env, state := l.mini(l.url)
		if out, code := run(t, env, "", frontDoor(t, "fd15-transport"), append([]string{"--lab", "--records", t.TempDir()}, lacking...)...); code == 0 || calls(t, state)[0] != "" {
			t.Errorf("fd15 --lab %v: exit %d, calls %v\n%s", lacking, code, calls(t, state), out)
		}
	}

	env, state := l.mini(l.url)
	dir, _, routes := deployCopy(t)
	records := t.TempDir()
	out, code := run(t, env, "", frontDoor(t, "fd15-transport"), "--lab", "--mini", "owner@mini", "--job", job, "--size", fmt.Sprint(40*mib), "--deploy", dir, "--records", records)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	want := []string{transport + "quic", metrics, metrics, metrics, transport + "http2", metrics, metrics, metrics, transport + "deployed"}
	if got := calls(t, state); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the mini was asked:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	files, _ := filepath.Glob(filepath.Join(records, "*-front-door-15.md"))
	if len(files) != 1 {
		t.Fatalf("records: %v\n%s", files, out)
	}
	record := read(t, files[0])
	for _, re := range []string{`--lab\x60, through http://127\.0\.0\.1:\d+\.`, `- quic, up: 41943040 bytes in [0-9.]+ s, [0-9.]+ Mbit/s, cloudflared at 0\.\d+ of a core`, `- quic, down: 41943040 bytes in `, `- http2, up: 41943040 bytes`, `- http2, down: 41943040 bytes`, `Result: pass`} {
		if !regexp.MustCompile(re).MatchString(record) {
			t.Errorf("the record lacks %s:\n%s", re, record)
		}
	}
	chosen := setting(t, protocolLine, routes)
	if (chosen == "http2") != strings.Contains(record, "- HTTP/2: ") || (chosen == "quic") != strings.Contains(record, "- QUIC stays: ") {
		t.Errorf("deploy says %s and the record says otherwise:\n%s", chosen, record)
	}

	// A login for another lab than the one the test runs through: nothing
	// is measured and the mini is asked nothing.
	env, state = l.mini(l.url)
	env = append(env, "LAB_ADDRESS=http://127.0.0.1:9")
	if out, code := run(t, env, "", frontDoor(t, "fd15-transport"), "--lab", "--mini", "owner@mini", "--job", job, "--records", t.TempDir()); code == 0 || calls(t, state)[0] != "" || !strings.Contains(out, "not logged in to Omarchy Mac Lab at http://127.0.0.1:9") {
		t.Errorf("a login for another address: exit %d, calls %v\n%s", code, calls(t, state), out)
	}

	// A tunnel that does not come up under HTTP/2 ends the run, and the
	// tunnel is still put back under the Deployed files.
	env, state = l.mini(l.url)
	os.WriteFile(filepath.Join(state, "http2-fails"), nil, 0o644)
	dir, _, routes = deployCopy(t)
	deployed := setting(t, protocolLine, routes)
	out, code = run(t, env, "", frontDoor(t, "fd15-transport"), "--lab", "--mini", "owner@mini", "--job", job, "--size", fmt.Sprint(40*mib), "--deploy", dir, "--records", t.TempDir())
	got := calls(t, state)
	if code == 0 || got[len(got)-1] != transport+"deployed" || setting(t, protocolLine, routes) != deployed || strings.Contains(out, ": pass") {
		t.Errorf("exit %d, the mini was asked %v\n%s", code, got, out)
	}
}

// The Front door's test 16 as the owner runs it, with the edge and the
// mini played: each of the four targets is judged, and one missed fails
// the run.
func TestFD16JudgesEachOfTheFourTargets(t *testing.T) {
	l := newMadeUpLab(t)
	job := l.transferJob()
	target, _ := url.Parse(l.url)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(r *http.Response) error {
		if r.Request.URL.Path == "/" {
			r.Header.Set("CF-Cache-Status", "HIT")
		}
		return nil
	}
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = target.Host
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(edge.Close)

	fd16 := func(address string, more []string, args ...string) (string, string, int) {
		t.Helper()
		env, _ := l.mini(address)
		records := t.TempDir()
		out, code := run(t, append(env, more...), "", frontDoor(t, "fd16-performance"),
			append([]string{"--lab", "--mini", "owner@mini", "--job", job, "--size", fmt.Sprint(40 * mib), "--records", records}, args...)...)
		files, _ := filepath.Glob(filepath.Join(records, "*-front-door-16.md"))
		if len(files) != 1 {
			return out, "", code
		}
		return out, read(t, files[0]), code
	}
	verdicts := func(record string) string {
		var v []string
		for _, m := range regexp.MustCompile(`- target (\d): (pass|FAIL)\.`).FindAllStringSubmatch(record, -1) {
			v = append(v, m[1]+m[2])
		}
		return strings.Join(v, " ")
	}

	out, record, code := fd16(edge.URL, nil, "--link-up", "1", "--link-down", "1")
	if code != 0 || verdicts(record) != "1pass 2pass 3pass 4pass" || !strings.Contains(record, "Result: pass") {
		t.Fatalf("exit %d\n%s\n%s", code, out, record)
	}
	for _, re := range []string{`18 of 18 were an edge HIT`, `labd took \d+ ms at the 95th percentile over 2\d\d requests for 1\d signed-out pages`, `up [0-9.]+ Mbit/s, \d+% of a 1 Mbit/s link`, `cloudflared used 0\.\d+ of a core while uploading and 0\.\d+ while downloading`} {
		if !regexp.MustCompile(re).MatchString(record) {
			t.Errorf("the record lacks %s:\n%s", re, record)
		}
	}
	if strings.Contains(record, "Password") {
		t.Errorf("sudo's prompt is in the record:\n%s", record)
	}

	// No edge in front: the page is no HIT.
	if out, record, code := fd16(l.url, nil, "--link-up", "1", "--link-down", "1"); code != 1 || verdicts(record) != "1FAIL 2pass 3pass 4pass" || !strings.Contains(record, "0 of 18 were an edge HIT") || !strings.Contains(record, "Result: fail") {
		t.Errorf("with no edge: exit %d\n%s\n%s", code, out, record)
	}
	// A link the transfers do not fill to 80%.
	if out, record, code := fd16(edge.URL, nil, "--link-up", "1", "--link-down", "100000000"); code != 1 || verdicts(record) != "1pass 2pass 3FAIL 4pass" {
		t.Errorf("under 80%% of the link: exit %d\n%s\n%s", code, out, record)
	}
	// cloudflared over one core.
	if out, record, code := fd16(edge.URL, []string{"SSH_CPU_STEP=100000"}, "--link-up", "1", "--link-down", "1"); code != 1 || verdicts(record) != "1pass 2pass 3pass 4FAIL" {
		t.Errorf("over one core: exit %d\n%s\n%s", code, out, record)
	}
	// Without the link speeds there is nothing to compare with.
	if out, _, code := fd16(edge.URL, nil); code == 0 || !strings.Contains(out, "--link-up and --link-down") {
		t.Errorf("without link speeds: exit %d\n%s", code, out)
	}
}
