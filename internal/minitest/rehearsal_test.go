package minitest

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	theAlias        = "maclab.omarchy.org"
	rehearsalName   = "alias-rehearsal.omarchy-m-testing.org"
	rehearsalRoutes = "usr/local/lab/rehearsal/cloudflared.yml"
)

// rehearsalReady is a mini whose tunnel and labd run under the Deployed
// files, with the route file and labd's LaunchDaemon file as deploy/ has them.
func rehearsalReady(t *testing.T) (m *mini, labd, tunnel string) {
	t.Helper()
	m = tunnelUp(t)
	write(t, m.path("usr/local/lab/current/deploy/cloudflared.yml"), read(t, filepath.Join(repo(t), "deploy", "cloudflared.yml")), 0o644)
	write(t, m.tree+"/deploy/"+labdLabel+".plist", read(t, filepath.Join(repo(t), "deploy", labdLabel+".plist")), 0o644)
	labd = m.path("Library/LaunchDaemons/" + labdLabel + ".plist")
	tunnel = m.path("Library/LaunchDaemons/" + cloudflaredLabel + ".plist")
	write(t, labd, read(t, m.tree+"/deploy/"+labdLabel+".plist"), 0o644)
	write(t, m.path("usr/local/lab/current/bin/labd"), "#!/bin/sh\n", 0o755)
	// labd's is the one daemon the made-up launchctl keeps under a plain name.
	write(t, filepath.Join(filepath.Dir(m.root), "state", "loaded"), "", 0o644)
	m.labd()
	m.ready(true)
	os.Remove(m.log)
	return m, labd, tunnel
}

func argAfter(t *testing.T, file, flag string) string {
	t.Helper()
	_, arrays := plist(t, file)
	args := arrays["ProgramArguments"]
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("%s has no %s", file, flag)
	return ""
}

// For the Alias rehearsal labd takes the throwaway name as its Alias and
// the tunnel routes it, without a Deploy; "deployed" puts both back.
func TestRehearsalAliasPutsLabdAndTheTunnelUnderTheThrowawayNameAndBack(t *testing.T) {
	m, labd, tunnel := rehearsalReady(t)
	deployedLabd, deployedTunnel := read(t, labd), read(t, tunnel)

	if out := m.ok("rehearsal-alias"); !strings.Contains(out, theAlias) || !strings.Contains(out, "Deployed") {
		t.Errorf("asked which Alias, it said:\n%s", out)
	}
	for _, bad := range [][]string{{"lab.omarchy-mac.org"}, {theAlias}, {"example.org"}, {"www.omarchy-m-testing.org"}, {"omarchy-m-testing.org"}, {"a.b.omarchy-m-testing.org"}, {"x.omarchy-m-testing.org; id"}, {rehearsalName, "more"}, {"--alias"}} {
		os.Remove(m.log)
		m.refused("rehearsal-alias", bad...)
		if read(t, labd) != deployedLabd || read(t, tunnel) != deployedTunnel || strings.Contains(m.callsOrNone(), "launchctl boot") || exists(m.path(rehearsalRoutes)) {
			t.Fatalf("rehearsal-alias %v changed something", bad)
		}
	}

	os.Remove(m.log)
	out := m.ok("rehearsal-alias", rehearsalName)
	if got := argAfter(t, labd, "--alias"); got != rehearsalName {
		t.Errorf("labd's --alias is %q", got)
	}
	if got := argAfter(t, tunnel, "--config"); got != m.path(rehearsalRoutes) {
		t.Errorf("the tunnel's --config is %q", got)
	}
	// Nothing else of either file changed.
	if strings.Replace(read(t, labd), rehearsalName, theAlias, 1) != deployedLabd {
		t.Error("labd's file differs from the Deployed one in more than the Alias")
	}
	if strings.Replace(read(t, tunnel), m.path(rehearsalRoutes), argAfter(t, m.tree+"/deploy/"+cloudflaredLabel+".plist", "--config"), 1) != deployedTunnel {
		t.Error("the tunnel's file differs from the Deployed one in more than its route file")
	}
	routes := read(t, m.path(rehearsalRoutes))
	want := strings.Replace(read(t, filepath.Join(repo(t), "deploy", "cloudflared.yml")), "hostname: "+theAlias, "hostname: "+rehearsalName, 1)
	if routes != want || strings.Contains(routes, theAlias) || !strings.Contains(routes, "hostname: lab.omarchy-mac.org") || !strings.Contains(routes, "http_status:404") {
		t.Errorf("the rehearsal's route file:\n%s", routes)
	}
	for _, f := range []string{labd, tunnel, m.path(rehearsalRoutes)} {
		if mode(t, f) != 0o644 {
			t.Errorf("%s is %v", f, mode(t, f))
		}
	}
	calls := m.calls()
	for _, want := range []string{"launchctl bootout system/" + cloudflaredLabel, "launchctl bootstrap system " + tunnel, "launchctl bootout system/" + labdLabel, "launchctl bootstrap system " + labd} {
		if !strings.Contains(calls, want) {
			t.Errorf("no %q:\n%s", want, calls)
		}
	}
	// The tunnel first, so that labd's first look at the Alias finds a way in.
	if strings.Index(calls, "bootstrap system "+tunnel) > strings.Index(calls, "bootstrap system "+labd) {
		t.Errorf("labd was started before the tunnel:\n%s", calls)
	}
	if !m.loaded() || !m.tunnelLoaded() || !strings.Contains(out, "rehearsal-alias deployed") {
		t.Errorf("both are not running, or it does not say how to go back:\n%s", out)
	}
	if out := m.ok("rehearsal-alias"); !strings.Contains(out, rehearsalName) || !strings.Contains(out, "for the rehearsal") {
		t.Errorf("asked which Alias, it said:\n%s", out)
	}

	os.Remove(m.log)
	m.ok("rehearsal-alias", "deployed")
	if read(t, labd) != deployedLabd || read(t, tunnel) != deployedTunnel || exists(m.path(rehearsalRoutes)) || !m.loaded() || !m.tunnelLoaded() {
		t.Errorf("the Deployed files were not put back:\n%s", m.calls())
	}
}

// When labd or the tunnel does not come up under the throwaway name, both
// are put back under the Deployed files and the run fails.
func TestRehearsalAliasGoesBackWhenTheTunnelDoesNotConnect(t *testing.T) {
	m, labd, tunnel := rehearsalReady(t)
	deployedLabd, deployedTunnel := read(t, labd), read(t, tunnel)
	m.ready(false)
	out := m.refused("rehearsal-alias", rehearsalName)
	if !strings.Contains(out, "did not connect") || read(t, labd) != deployedLabd || read(t, tunnel) != deployedTunnel || !m.loaded() || !m.tunnelLoaded() || exists(m.path(rehearsalRoutes)) {
		t.Errorf("want the Deployed files back and both loaded:\n%s", out)
	}
}

// A mini with no tunnel has nothing to rehearse on.
func TestRehearsalAliasNeedsTheTunnelAndLabd(t *testing.T) {
	m, _ := withTunnel(t)
	out := m.refused("rehearsal-alias", rehearsalName)
	if !strings.Contains(out, "nothing was changed") || strings.Contains(m.callsOrNone(), "launchctl boot") {
		t.Errorf("with no tunnel:\n%s", out)
	}
}

var holdLine = regexp.MustCompile(`(?m)^(stopped|started|back) (\d{9,})$`)

// hold stops one of the two daemons a visitor's request passes, for a
// number of seconds, and starts it again: it says when, in seconds since
// 1970, for the recording to read.
func TestHoldStopsADaemonForSomeSecondsAndStartsItAgain(t *testing.T) {
	for _, part := range []string{"cloudflared", "labd"} {
		m, labd, tunnel := rehearsalReady(t)
		file, label := tunnel, cloudflaredLabel
		if part == "labd" {
			file, label = labd, labdLabel
		}
		before := read(t, file)
		out := m.ok("hold", part, "1")
		got := holdLine.FindAllStringSubmatch(out, -1)
		if len(got) != 3 || got[0][1] != "stopped" || got[1][1] != "started" || got[2][1] != "back" {
			t.Fatalf("hold %s said:\n%s", part, out)
		}
		from, _ := strconv.Atoi(got[0][2])
		to, _ := strconv.Atoi(got[1][2])
		if to-from < 1 || to-from > 30 {
			t.Errorf("hold %s 1 held it for %d s", part, to-from)
		}
		calls := m.calls()
		if strings.Count(calls, "launchctl bootout system/"+label) != 1 || strings.Count(calls, "launchctl bootstrap system "+file) != 1 || read(t, file) != before {
			t.Errorf("hold %s:\n%s", part, calls)
		}
		other := labdLabel
		if part == "labd" {
			other = cloudflaredLabel
		}
		if strings.Contains(calls, "bootout system/"+other) || !m.loaded() || !m.tunnelLoaded() {
			t.Errorf("hold %s touched the other daemon, or left one stopped:\n%s", part, calls)
		}
	}
}

// What is not one of the two daemons, or not a time under five minutes, is
// refused with nothing stopped: past five minutes "Agents" would be told
// the tunnel is gone.
func TestHoldRefusesWhatItDoesNotHold(t *testing.T) {
	m, _, _ := rehearsalReady(t)
	for _, bad := range [][]string{{}, {"cloudflared"}, {"oobd", "10"}, {"labd", "0"}, {"labd", "241"}, {"labd", "ten"}, {"labd", "10", "20"}, {"cloudflared", "-5"}, {"org.omarchy-mac.lab.labd", "10"}} {
		os.Remove(m.log)
		m.refused("hold", bad...)
		if strings.Contains(m.callsOrNone(), "launchctl boot") {
			t.Fatalf("hold %v stopped something", bad)
		}
	}
}

// A daemon that hold stopped is started again when the run is cut short.
func TestHoldStartsTheDaemonAgainWhenItIsCut(t *testing.T) {
	m, _, tunnel := rehearsalReady(t)
	// The stand-in for sleep ends the run as a hang-up of the owner's SSH would.
	write(t, m.bin+"/sleep", "#!/bin/bash\nkill -HUP $PPID\nexec /bin/sleep 5\n", 0o755)
	out, code := m.run("hold", "cloudflared", "200")
	if code == 0 || !m.tunnelLoaded() || !strings.Contains(m.calls(), "launchctl bootstrap system "+tunnel) {
		t.Errorf("cut while it held the tunnel: exit %d\n%s\n%s", code, out, m.calls())
	}
}

// bootstrapFails has the made-up launchctl refuse to start label: a number
// is how many times, anything else a text of the LaunchDaemon files it
// refuses.
func (m *mini) bootstrapFails(label, what string) {
	m.t.Helper()
	write(m.t, filepath.Join(filepath.Dir(m.root), "state", "bootstrap.fails."+label), what, 0o644)
}

// A bootstrap that fails after transport stopped the tunnel puts the tunnel
// back under the file it ran under.
func TestTransportLeavesTheTunnelStartedWhenLaunchdRefuses(t *testing.T) {
	m := tunnelUp(t)
	m.transport("quic")
	m.ready(true)
	daemon := m.path("Library/LaunchDaemons/" + cloudflaredLabel + ".plist")
	before := read(t, daemon)
	m.bootstrapFails(cloudflaredLabel, "<string>http2</string>")

	out := m.refused("transport", "http2")

	if !m.tunnelLoaded() || read(t, daemon) != before {
		t.Errorf("the tunnel is loaded %v, under its file as before %v:\n%s\n%s", m.tunnelLoaded(), read(t, daemon) == before, out, m.calls())
	}
}

// A bootstrap that fails once while hold starts the daemon again is asked
// again: the daemon is not left stopped.
func TestHoldStartsTheDaemonAgainWhenLaunchdRefusesOnce(t *testing.T) {
	for _, part := range []string{"cloudflared", "labd"} {
		m, _, _ := rehearsalReady(t)
		label := cloudflaredLabel
		if part == "labd" {
			label = labdLabel
		}
		m.bootstrapFails(label, "1")
		out, code := m.run("hold", part, "1")
		if !m.loaded() || !m.tunnelLoaded() {
			t.Errorf("hold %s: exit %d, labd loaded %v, tunnel loaded %v\n%s\n%s", part, code, m.loaded(), m.tunnelLoaded(), out, m.calls())
		}
	}
}

// A bootstrap that fails part-way through the rehearsal's switch puts both
// daemons back under the Deployed files, and going back puts labd back
// even when the tunnel cannot be started.
func TestRehearsalAliasLeavesBothStartedWhenLaunchdRefuses(t *testing.T) {
	m, labd, tunnel := rehearsalReady(t)
	deployedLabd, deployedTunnel := read(t, labd), read(t, tunnel)
	m.bootstrapFails(labdLabel, rehearsalName)

	out := m.refused("rehearsal-alias", rehearsalName)

	if read(t, labd) != deployedLabd || read(t, tunnel) != deployedTunnel || !m.loaded() || !m.tunnelLoaded() || exists(m.path(rehearsalRoutes)) {
		t.Errorf("want the Deployed files back and both loaded:\n%s\n%s", out, m.calls())
	}

	m.bootstrapFails(labdLabel, "0")
	m.ok("rehearsal-alias", rehearsalName)
	m.bootstrapFails(cloudflaredLabel, "9")
	out = m.refused("rehearsal-alias", "deployed")
	if read(t, labd) != deployedLabd || !m.loaded() {
		t.Errorf("a tunnel that would not start kept labd from going back:\n%s\n%s", out, m.calls())
	}
}
