package minitest

import (
	"strings"
	"testing"
)

// The port check's --seen says what the mini sees on each port, one line a
// port, left to right: it runs no tool and asks the controller nothing.
func TestThePortCheckSaysWhatTheMiniSeesForAScript(t *testing.T) {
	m, token := portsMini(t, strictTool)

	out := m.ok("ports", "--seen")

	want := "rid=1 port=left connected=no product=none plugs=27 connection=none\n" +
		"rid=0 port=middle connected=yes product=29453 plugs=42 connection=5A0E1B7C-0000-4000-8000-00000000A001\n" +
		"rid=2 port=right connected=yes product=29448 plugs=40 connection="
	if !strings.HasPrefix(out, want) || strings.Count(strings.TrimSpace(out), "\n") != 2 {
		t.Errorf("ports --seen says:\n%s\nwant it to start:\n%s", out, want)
	}
	if runs := m.toolRuns(); len(runs) != 0 {
		t.Errorf("--seen ran the reset tool: %v", runs)
	}
	if calls := m.calls(); strings.Contains(calls, "curl") || strings.Contains(calls, "launchctl") || strings.Contains(out, token) {
		t.Errorf("--seen asked the controller or launchd:\n%s", calls)
	}
}
