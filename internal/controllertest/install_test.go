// Package controllertest tests scripts/controller/install, the Mac lab
// controller's install, on a temporary root: nothing is started.
package controllertest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// bundle is the unpacked lab-controller tarball: the install script and
// stand-ins for oobd and the reset tool.
func bundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.ReadFile("../../scripts/controller/install")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "install"), src, 0o755); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	for _, f := range []string{"oobd", "macvdmtool"} {
		os.WriteFile(filepath.Join(dir, "bin", f), []byte("#!/bin/sh\necho "+f+"\n"), 0o755)
	}
	os.WriteFile(filepath.Join(dir, "VERSION"), []byte("v0.0.0-test\n"), 0o644)
	return dir
}

func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(dir, "install")}, args...)...)
	cmd.Env = append(os.Environ(), "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestADryRunChangesNothing(t *testing.T) {
	dir, root := bundle(t), t.TempDir()
	out, err := run(t, dir, "--root", root, "--listen", "100.64.0.5", "--rids", "0,1", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"would: install", "making the controller's token", "https://100.64.0.5:7780", "ports 0,1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run lacks %q:\n%s", want, out)
		}
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("a dry run wrote %v", ents)
	}
}

func TestTheInstallMakesTheControllerOnOneAddressWithTLSAndAToken(t *testing.T) {
	dir, root := bundle(t), t.TempDir()
	out, err := run(t, dir, "--root", root, "--listen", "100.64.0.5:7781", "--rids", "0,1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	prefix := filepath.Join(root, "usr/local/lab-controller")
	for f, mode := range map[string]os.FileMode{"bin/oobd": 0o755, "bin/macvdmtool": 0o755, "secrets/oobd.token": 0o600, "secrets/tls.key": 0o600, "secrets/tls.crt": 0o644, "secrets": 0o700 | os.ModeDir} {
		info, err := os.Stat(filepath.Join(prefix, f))
		if err != nil || info.Mode() != mode {
			t.Errorf("%s: %v %v, want %v", f, info.Mode(), err, mode)
		}
	}
	token, _ := os.ReadFile(filepath.Join(prefix, "secrets/oobd.token"))
	if !regexp.MustCompile(`^[0-9a-f]{64}\n$`).Match(token) {
		t.Fatalf("the token %q", token)
	}
	plist, err := os.ReadFile(filepath.Join(root, "Library/LaunchDaemons/org.omarchy-mac.lab.controller.plist"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>100.64.0.5:7781</string>", "<string>--tls-cert</string>", "<string>/usr/local/lab-controller/secrets/oobd.token</string>", "<string>0,1</string>"} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("the LaunchDaemon lacks %s", want)
		}
	}
	if strings.Contains(string(plist), string(token[:16])) {
		t.Fatal("the LaunchDaemon carries the token")
	}
	m := regexp.MustCompile(`lab controller add \S+ --url https://100\.64\.0\.5:7781 --fingerprint ([0-9a-f]{64}) --token-file FILE`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no lab controller add line:\n%s", out)
	}
	if strings.Contains(out, strings.TrimSpace(string(token))) {
		t.Fatal("the install printed the token")
	}

	// Again: the token and certificate are kept.
	out, err = run(t, dir, "--root", root, "--listen", "100.64.0.5:7781", "--rids", "0,1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	again, _ := os.ReadFile(filepath.Join(prefix, "secrets/oobd.token"))
	if string(again) != string(token) || !strings.Contains(out, m[1]) {
		t.Fatal("a second install made a new token or certificate")
	}
}

func TestTheInstallNeverListensEverywhere(t *testing.T) {
	dir := bundle(t)
	for _, addr := range []string{"0.0.0.0", "0.0.0.0:7780", "[::]:7780", "::", "[::1]:7780", "127.0.0.1", "localhost", "lab.example"} {
		root := t.TempDir()
		if out, err := run(t, dir, "--root", root, "--listen", addr); err == nil {
			t.Errorf("--listen %s was taken:\n%s", addr, out)
		}
		if ents, _ := os.ReadDir(root); len(ents) != 0 {
			t.Errorf("--listen %s wrote %v", addr, ents)
		}
	}
	if out, err := run(t, dir, "--root", t.TempDir(), "--listen", "100.64.0.5", "--rids", "all"); err == nil {
		t.Errorf("--rids all was taken:\n%s", out)
	}
}
