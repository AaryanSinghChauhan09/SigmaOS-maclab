package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// compareFacts runs compare-facts on a Mac made of the files under root: the
// kernel log is the lines in kernel, the device tree and sysfs are under root.
func compareFacts(t *testing.T, root, kernel string) map[string]any {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	script, err := builtinTests.ReadFile("tests/compare-facts.sh")
	if err != nil {
		t.Fatal(err)
	}
	bin, out := t.TempDir(), t.TempDir()
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("journalctl", "cat "+kernel)
	stub("systemctl", "true")
	stub("systemd-analyze", "echo 'Startup finished in 4.000s (kernel) + 6.000s (userspace) = 10.000s'")
	cmd := exec.Command("bash", "-c", string(script))
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "MACLAB_OUT="+out, "MACLAB_FACTS_ROOT="+root)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compare-facts: %v\n%s", err, b)
	}
	b, err := os.ReadFile(filepath.Join(out, "facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(b, &facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

// m2-max's kernel log (2026-11-18) names no GPU firmware version: the device
// tree holds it. Its SIO lines follow the reserved-memory lines for SIO's
// firmware, which say nothing of whether SIO came up.
func TestCompareFactsOnM2Max(t *testing.T) {
	root := filepath.Join("testdata", "compare-facts", "m2-max")
	facts := compareFacts(t, root, filepath.Join(root, "kernel.txt"))
	if g := facts["gpu_firmware"]; g != "13.5.0" {
		t.Errorf("gpu_firmware %q", g)
	}
	if s := facts["sio"]; s != "up, SIO protocol v9" {
		t.Errorf("sio %q", s)
	}
}

// A SIO or GPU that fails says so, with the kernel's line. These lines are
// made up in the drivers' form: m2-max's SIO and GPU came up.
func TestCompareFactsSayAFailure(t *testing.T) {
	root := filepath.Join("testdata", "compare-facts", "m2-max")
	kernel := filepath.Join(t.TempDir(), "kernel.txt")
	os.WriteFile(kernel, []byte(`OF: reserved mem: 0x00000107ca498000..0x00000107ca49bfff (16 KiB) nomap non-reusable sio-firmware-data@107ca498000
apple-sio 39bc00000.sio: RTKit: Initializing (protocol version 12)
apple-sio 39bc00000.sio: probe with driver apple-sio failed with error -110
asahi 406400000.gpu: Probing...
asahi 406400000.gpu: probe with driver asahi failed with error -22
`), 0o644)
	facts := compareFacts(t, root, kernel)
	if g := facts["gpu_firmware"]; g != "13.5.0; the GPU failed: asahi 406400000.gpu: probe with driver asahi failed with error -22" {
		t.Errorf("gpu_firmware %q", g)
	}
	if s := facts["sio"]; s != "failed: apple-sio 39bc00000.sio: probe with driver apple-sio failed with error -110" {
		t.Errorf("sio %q", s)
	}

	// No SIO line at all, and no device tree: nothing is guessed.
	os.WriteFile(kernel, []byte("OF: reserved mem: 0x00000107ca498000..0x00000107ca49bfff (16 KiB) nomap non-reusable sio-firmware-data@107ca498000\n"), 0o644)
	facts = compareFacts(t, t.TempDir(), kernel)
	if g, s := facts["gpu_firmware"], facts["sio"]; g != "" || s != "no SIO driver line" {
		t.Errorf("gpu_firmware %q, sio %q", g, s)
	}
}
