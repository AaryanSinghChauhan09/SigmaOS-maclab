package repotest

import (
	"regexp"
	"strings"
	"testing"
)

// Everything an enrolled Mac or a stored record carries keeps its maclab
// name (spec, section 5, "Kept names"). A name leaves these lists only when
// the spec says it goes: MACLAB_BUILDER_TOKEN went with the shared builder
// token (section 12), which leaves 37 of the 38 variables.

const keptVariables = `
MACLAB_ALLOW_PLAIN_HTTP MACLAB_ARCHIVE_TEST_PLAN MACLAB_ARCHIVE_TEST_RECEIPT MACLAB_ARCHIVE_TEST_STATE
MACLAB_BODY MACLAB_BOOT_TEST_ENTRY_CONTROL MACLAB_BOOT_TEST_PROFILE_CONTROL MACLAB_BROWSER
MACLAB_CA_FILE MACLAB_CHAOS_CODES MACLAB_COLLECTOR_EOF MACLAB_DEVICE
MACLAB_FREEZE_EOF MACLAB_GATE MACLAB_GATE_SAMPLE MACLAB_HOLDER MACLAB_IN MACLAB_INSTALLED_SAMPLE
MACLAB_JOB MACLAB_LISTEN MACLAB_LIVE_GUI_USER MACLAB_LIVE_OUTPUT MACLAB_NATIVE_PYTHON
MACLAB_NATIVE_SCRIPT MACLAB_NODE MACLAB_NOTIFY_CMD MACLAB_NTFY MACLAB_OUT MACLAB_PRIORITY
MACLAB_SKIP_ON_SCREEN MACLAB_TEST MACLAB_TEST_LOG MACLAB_TITLE MACLAB_TOKEN
MACLAB_URL MACLAB_WORKFLOW_CPU_CRASH
`

const keptSchemaIDs = `
maclab.bind-output-archive-cpu-calibration/1 maclab.dmesg-compare/2 maclab.installed-identity/2
maclab.installed-manifest/2 maclab.installed-observed/1 maclab.installer-entry/1
maclab.m1-full-checkpoint-cpu-replay/1 maclab.m1-submit-markers/1 maclab.module-build/1
maclab.native-job-supervisor/1 maclab.omt-compare/2 maclab.preinstall-identity/1
maclab.workflow-authorization/1 maclab.workflow-reconciliation/1 maclab.workflow-state/1
maclab.workflow-status/1 maclab.workflow/1
`

const keptUnits = `
maclab-lab-boot-session.service maclab-lab-boot-lock.service maclab-lab-boot-deadlines.service
90-maclab.conf 90-maclab-watchdog.conf zz-maclab-autologin.conf zz-maclab-lab-boot-autologin.conf
maclab.conf
`

// Paths, the launchd label, the kernel command-line keys and the names on
// the ESP and in /boot.
const keptStrings = `
/var/lib/maclab
/etc/maclab
.config/maclab
.local/share/maclab
/run/maclab-lab-boot-lock
com.maclab.oobd
maclab.job=
maclab.lab_boot=
maclab.cfg
maclab.env
<esp>/maclab/
maclab_esp
maclab_next
maclab_chaos
`

const binaries = "lab lab-agent lab-builder lab-esp lab-loop lab-recovery-default lab-workflow labd oobd"

var (
	variableRE = regexp.MustCompile(`\bMACLAB_[A-Z0-9_]+\b`)
	schemaIDRE = regexp.MustCompile(`\bmaclab\.[a-z0-9-]+/[0-9]+\b`)
	unitRE     = regexp.MustCompile(`[A-Za-z0-9@_-]*maclab[A-Za-z0-9@_-]*\.(?:service|conf)\b`)
)

func TestKeptNames(t *testing.T) {
	src := sources(t)
	found := map[*regexp.Regexp]map[string]bool{variableRE: {}, schemaIDRE: {}, unitRE: {}}
	for _, text := range src {
		for re, set := range found {
			for _, m := range re.FindAllString(text, -1) {
				set[m] = true
			}
		}
	}
	for _, class := range []struct {
		what string
		list string
		re   *regexp.Regexp
		want int
	}{
		// MACLAB_SERVER went with ./volunteer, the one script that read it.
		{"variable", keptVariables, variableRE, 36},
		{"schema id", keptSchemaIDs, schemaIDRE, 17},
		{"systemd unit or drop-in", keptUnits, unitRE, 8},
	} {
		names := strings.Fields(class.list)
		if len(names) != class.want {
			t.Errorf("%d %ss listed, want %d", len(names), class.what, class.want)
		}
		for _, name := range names {
			if !found[class.re][name] {
				t.Errorf("the %s %s is gone or renamed", class.what, name)
			}
		}
	}
	for _, name := range lines(keptStrings) {
		ok := false
		for _, text := range src {
			if strings.Contains(text, name) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("%s is gone or renamed", name)
		}
	}
}

func TestBinariesKeepTheirNames(t *testing.T) {
	top := root(t)
	for _, name := range strings.Fields(binaries) {
		if !exists(top, "cmd/"+name+"/main.go") {
			t.Errorf("cmd/%s is gone or renamed", name)
		}
		if strings.Contains(name, "maclab") {
			t.Errorf("%s carries the name maclab", name)
		}
	}
}
