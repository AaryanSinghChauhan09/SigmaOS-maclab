package repotest

import (
	"strings"
	"testing"
)

// The guides the spec's repository table puts under docs/, and the README.
var guides = []string{"README.md", "docs/https.md", "docs/encrypted-macs.md", "docs/ab-installs.md", "docs/mesa-jobs.md", "docs/artifact-boot-test.md"}

// What no guide says any more: the old deployment, the ways in that are
// gone, labd's own TLS, and the tools `lab mcp` no longer has.
var goneFromGuides = []string{
	"chonkbase", "i9beef", "Kubernetes", "deploy/k8s", "ingress-nginx",
	"--tls-cert", "--tls-key", "--trusted-proxy", "--tailscale", "--allow-host",
	"admin.token", "mnt_", "lab maintainer", "--maintainer",
	"lab_serial", "lab_devices", "lab_screenshot", "lab_build_status", "`lab_log`",
	"follow=1", "--volunteer \"", "--oob-url", "--oob-token",
}

// What a guide names only to say it is retired.
var retiredInGuides = []string{"lab forget", "lab shell", "lab console", "lab serial", "lab recovery-mode", "./volunteer", "serial-assisted"}

// The guides speak of Omarchy Mac Lab at the Lab address, and of nothing
// the lab no longer has.
func TestTheGuidesNameTheLabAddressAndOmarchyMacLab(t *testing.T) {
	top := root(t)
	for _, name := range guides {
		text := read(t, top, name)
		if !strings.Contains(text, "Omarchy Mac Lab") {
			t.Errorf("%s does not name Omarchy Mac Lab", name)
		}
		for _, gone := range goneFromGuides {
			if strings.Contains(text, gone) {
				t.Errorf("%s still says %q", name, gone)
			}
		}
		for i, line := range strings.Split(text, "\n") {
			for _, word := range retiredInGuides {
				if !strings.Contains(line, word) {
					continue
				}
				around := strings.Join(strings.Split(text, "\n")[max(0, i-2):min(i+3, strings.Count(text, "\n")+1)], " ")
				if !strings.Contains(around, "retired") && !strings.Contains(around, "not part of Omarchy Mac Lab") && !strings.Contains(around, "refuse") {
					t.Errorf("%s:%d names %q without saying it is gone: %s", name, i+1, word, strings.TrimSpace(line))
				}
			}
		}
	}
	for _, name := range []string{"README.md", "docs/https.md"} {
		text := read(t, top, name)
		for _, want := range []string{"https://lab.omarchy-mac.org", "curl -fsSL https://lab.omarchy-mac.org/install/lab | bash", "lab login https://lab.omarchy-mac.org"} {
			if name == "docs/https.md" && want != "https://lab.omarchy-mac.org" {
				continue
			}
			if !strings.Contains(text, want) {
				t.Errorf("%s does not say %q", name, want)
			}
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "lab login") && strings.Contains(line, "<token>") {
				t.Errorf("%s gives lab login a token as an argument: %s", name, strings.TrimSpace(line))
			}
		}
	}
}
