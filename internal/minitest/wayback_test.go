package minitest

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// breakTheSwitch makes the copy of tag's LaunchDaemon file fail, as on a
// full disk: by then labd is stopped and the mini points at tag.
func (m *mini) breakTheSwitch(tag string) {
	m.t.Helper()
	write(m.t, m.bin+"/cp", `#!/bin/bash
if [[ "$*" == *"/`+tag+`/deploy/`+labdLabel+`.plist "* ]]; then
  echo "cp: No space left on device" >&2
  exit 1
fi
exec /bin/cp "$@"
`, 0o755)
}

func TestADeployThatBreaksPartWayPutsThePreviousBuildBack(t *testing.T) {
	m, k, r := deployed(t)
	good, next := "v0.2.0-rc.1", "v0.2.0-rc.2"
	bundle(t, k, filepath.Join(r.dir, good), good, true)
	bundle(t, k, filepath.Join(r.dir, next), next, true)
	m.ok("deploy", "--from", r.from(good), good)
	m.breakTheSwitch(next)

	out := m.refused("deploy", "--from", r.from(next), next)

	if m.current() != good || !m.loaded() {
		t.Fatalf("the mini runs %q, labd loaded %v: the lab is Dark\n%s", m.current(), m.loaded(), out)
	}
	if got := read(t, m.path("Library/LaunchDaemons/"+labdLabel+".plist")); !strings.Contains(got, good) {
		t.Errorf("labd's LaunchDaemon file is not the previous build's: %q", got)
	}
	if !reflect.DeepEqual(m.builds(), []string{good}) {
		t.Errorf("builds kept: %v", m.builds())
	}
	if !strings.Contains(out, "stopped part-way") || !strings.Contains(out, good+" is back and answers") {
		t.Errorf("no word of what happened:\n%s", out)
	}
	if out, code := m.run("deploy", "--from", r.from(good), good); code != 0 || !strings.Contains(out, "already") {
		t.Errorf("the build put back does not answer: exit %d\n%s", code, out)
	}
}

func TestAFirstDeployThatBreaksPartWayLeavesLabdStopped(t *testing.T) {
	m, k, r := deployed(t)
	tag := "v0.2.0-rc.1"
	bundle(t, k, filepath.Join(r.dir, tag), tag, true)
	m.breakTheSwitch(tag)

	out := m.refused("deploy", "--from", r.from(tag), tag)

	if m.current() != "" || len(m.builds()) != 0 || m.loaded() {
		t.Errorf("left behind: current %q, builds %v, labd loaded %v\n%s", m.current(), m.builds(), m.loaded(), out)
	}
	if exists(m.path("usr/local/lab/.deploying")) {
		t.Error("the Deploy's lock was left behind")
	}
}
