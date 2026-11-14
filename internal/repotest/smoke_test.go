package repotest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeCurl = `#!/bin/bash
echo "curl $*" >> "$FAKE/log"
out="" post=0 url="" code=0
while (( $# )); do
  case "$1" in
    -o) out="$2"; shift ;;
    -X) [[ $2 == POST ]] && post=1; shift ;;
    -H|--data-urlencode) shift ;;
    -w) code=1; shift ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
say() { [[ -z $out || $out == /dev/null ]] || echo "$1" > "$out"; }
if [[ $url == */install ]]; then
  cat "$FAKE/install"
elif (( post )); then
  [[ $FAKE_MODE != nothing-works ]] || exit 22
  say "<h1>Report deleted</h1>"
elif (( code )); then
  printf 404
elif [[ $url != *token=* && $FAKE_MODE != ok ]]; then
  exit 22
else
  say page
fi
`

const fakeInstall = `mkdir -p "$HOME/.local/bin" "$HOME/.local/share/omarchy-m-test"
ln -s "$FAKE_REPO/cli/omarchy_m_test" "$HOME/.local/share/omarchy-m-test/omarchy_m_test"
cp "$FAKE/cli" "$HOME/.local/bin/omarchy-m-test"
`

const fakeTool = `#!/bin/bash
case "$1" in
  --version) echo "omarchy-m-test 0.2.0" ;;
  --dry-run) echo "omarchy-m-test only runs on Apple Silicon Macs"; exit 2 ;;
  --upload)
    if [[ -e "$FAKE/uploaded" ]]; then
      echo "Already uploaded. Your report: https://lab.test/reports/abc"
    else
      touch "$FAKE/uploaded"
      echo "Uploaded. Your report: https://lab.test/reports/abc"
      echo "Keep this link to delete it later: https://lab.test/reports/abc/deletion?token=s3cret"
    fi ;;
esac
`

// smoke runs release/smoke.sh against a lab that is a curl and an
// omarchy-m-test of the test's own, and returns its exit status, what it
// said and each curl it ran.
func smoke(t *testing.T, mode string) (status int, said string, curls []string) {
	t.Helper()
	for _, tool := range []string{"bash", "python3", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("needs " + tool)
		}
	}
	fake := t.TempDir()
	bin := filepath.Join(fake, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, text := range map[string]string{filepath.Join(bin, "curl"): fakeCurl, filepath.Join(fake, "install"): fakeInstall, filepath.Join(fake, "cli"): fakeTool} {
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	top := root(t)
	cmd := exec.Command("bash", filepath.Join(top, "release", "smoke.sh"))
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "SITE=https://lab.test", "FAKE="+fake, "FAKE_REPO="+top, "FAKE_MODE="+mode, "TMPDIR="+t.TempDir())
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		status = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(fake, "log"))
	return status, out.String(), strings.Split(strings.TrimSpace(string(log)), "\n")
}

func confirmations(curls []string) (sent []string) {
	for _, c := range curls {
		if strings.Contains(c, "-X POST") {
			sent = append(sent, c)
		}
	}
	return sent
}

// The smoke test sends the confirmation as a browser sends the page's form:
// the page sends no referrer, so the Origin is null and Sec-Fetch-Site says
// the form is the lab's own.
func TestTheSmokeTestConfirmsADeletionAsABrowserDoes(t *testing.T) {
	status, said, curls := smoke(t, "ok")
	if status != 0 || !strings.Contains(said, "smoke ok") {
		t.Fatalf("the smoke test against a lab that works: exit %d\n%s", status, said)
	}
	sent := confirmations(curls)
	if len(sent) != 1 {
		t.Fatalf("confirmations sent: %q", sent)
	}
	for _, want := range []string{"https://lab.test/reports/abc ", "-H Origin: null", "-H Sec-Fetch-Site: same-origin", "_method=delete", "token=s3cret"} {
		if !strings.Contains(sent[0]+" ", want) {
			t.Errorf("the confirmation lacks %q: %s", want, sent[0])
		}
	}
}

// A smoke test that fails after its upload leaves no report on the lab: it
// deletes the report on its way out, and says the deletion link when even
// that fails.
func TestAFailedSmokeTestDeletesItsReport(t *testing.T) {
	const link = "https://lab.test/reports/abc/deletion?token=s3cret"
	status, said, curls := smoke(t, "page-down")
	if status == 0 || !strings.Contains(said, "FAIL: report page not reachable") {
		t.Fatalf("the smoke test against a lab whose report page is down: exit %d\n%s", status, said)
	}
	if sent := confirmations(curls); len(sent) != 1 || !strings.Contains(sent[0], "token=s3cret") || !strings.Contains(sent[0], "_method=delete") {
		t.Fatalf("the failed run sent these confirmations: %q\n%s", sent, said)
	}
	if strings.Contains(said, link) {
		t.Errorf("a run that deleted its report still says the link:\n%s", said)
	}

	status, said, curls = smoke(t, "nothing-works")
	if status == 0 || len(confirmations(curls)) != 1 {
		t.Fatalf("the smoke test against a lab that fails and deletes nothing: exit %d, confirmations %q\n%s", status, confirmations(curls), said)
	}
	if !strings.Contains(said, link) {
		t.Errorf("the report is left on the lab and its deletion link is not said:\n%s", said)
	}
}
