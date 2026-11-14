package repotest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The reset tool is the lab's fork of the variant whose --rid is required
// and strictly parsed, pinned by commit and by the checksum of its source
// (spec, section 7, "Tool").
func TestTheResetToolsPin(t *testing.T) {
	pins := read(t, root(t), "deploy/pins")
	m := regexp.MustCompile(`(?m)^macvdmtool ([0-9a-f]{40}) ([0-9a-f]{64}) (\S+)$`).FindStringSubmatch(pins)
	if m == nil {
		t.Fatalf("deploy/pins has no line: macvdmtool <commit> <sha256> <url>")
	}
	if !strings.HasPrefix(m[1], "d57c485") {
		t.Errorf("the pinned commit is %s: the spec names d57c485", m[1])
	}
	if m[3] != "https://github.com/omacom/macvdmtool" {
		t.Errorf("the reset tool comes from %s, not the lab's own fork", m[3])
	}
	if n := len(regexp.MustCompile(`(?m)^macvdmtool `).FindAllString(pins, -1)); n != 1 {
		t.Errorf("%d lines for macvdmtool", n)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=A test", "-c", "user.email=test@example.org", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// release/reset-tool.sh, against a made-up repository whose make writes a
// file with a macOS arm64 program's first bytes. The real tool is built only
// by the release workflow's macOS job.
func TestTheResetToolIsBuiltOnlyFromItsPinnedSource(t *testing.T) {
	for _, tool := range []string{"git", "make", "bash", "od"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("needs %s", tool)
		}
	}
	top := root(t)
	src := t.TempDir()
	git(t, src, "init", "--quiet")
	makefile := "macvdmtool:\n\tprintf '\\317\\372\\355\\376\\014\\000\\000\\001 built from the pin\\n' > macvdmtool\n"
	if err := os.WriteFile(filepath.Join(src, "Makefile"), []byte(makefile), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, src, "add", "Makefile")
	git(t, src, "commit", "--quiet", "-m", "the pinned commit")
	commit := git(t, src, "rev-parse", "HEAD")
	tar, err := exec.Command("git", "-C", src, "-c", "tar.umask=0002", "archive", "--format=tar", commit).Output()
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(tar))
	// A later commit on the same branch: what a moved branch would bring.
	if err := os.WriteFile(filepath.Join(src, "Makefile"), []byte("macvdmtool:\n\tprintf 'something else' > macvdmtool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, src, "commit", "--quiet", "-am", "a later commit")
	later := git(t, src, "rev-parse", "HEAD")

	run := func(pin string, env ...string) (string, string, error) {
		t.Helper()
		dir := t.TempDir()
		pins := filepath.Join(dir, "pins")
		if err := os.WriteFile(pins, []byte("# the tests' pins\ncontainer 1.0 "+strings.Repeat("a", 64)+" https://example.org/c.pkg\n"+pin+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "out")
		cmd := exec.Command(filepath.Join(top, "release", "reset-tool.sh"), out)
		cmd.Env = append(append(os.Environ(), "RESET_TOOL_PINS="+pins), env...)
		b, err := cmd.CombinedOutput()
		return filepath.Join(out, "macvdmtool"), string(b), err
	}
	url := "file://" + src

	built, out, err := run("macvdmtool " + commit + " " + sum + " " + url)
	if err != nil {
		t.Fatalf("the pinned source was not built: %v\n%s", err, out)
	}
	b, err := os.ReadFile(built)
	if err != nil || !strings.Contains(string(b), "built from the pin") {
		t.Fatalf("what was built is not the pinned commit's: %q, %v", b, err)
	}
	if info, _ := os.Stat(built); info.Mode().Perm() != 0o755 {
		t.Errorf("the tool is %v, want 0755", info.Mode().Perm())
	}

	for why, pin := range map[string]string{
		"another checksum":         "macvdmtool " + commit + " " + strings.Repeat("0", 64) + " " + url,
		"the later commit's files": "macvdmtool " + later + " " + sum + " " + url,
		"a commit nobody has":      "macvdmtool " + strings.Repeat("1", 40) + " " + sum + " " + url,
		"a short commit id":        "macvdmtool " + commit[:7] + " " + sum + " " + url,
		"a branch":                 "macvdmtool main " + sum + " " + url,
		"plain http":               "macvdmtool " + commit + " " + sum + " http://example.org/macvdmtool",
		"a fifth word":             "macvdmtool " + commit + " " + sum + " " + url + " extra",
		"no line":                  "",
	} {
		built, out, err := run(pin)
		if err == nil {
			t.Errorf("%s: built", why)
		}
		if _, statErr := os.Stat(built); statErr == nil {
			t.Errorf("%s: a tool was left behind:\n%s", why, out)
		}
	}

	// Without the tests' pins file, a repository on this machine is refused:
	// the release builds from https alone.
	script := read(t, top, "release/reset-tool.sh")
	if !strings.Contains(script, `file:///*) [[ -n ${RESET_TOOL_PINS:-} ]] || die`) {
		t.Error("release/reset-tool.sh takes a file:// repository from deploy/pins")
	}
}
