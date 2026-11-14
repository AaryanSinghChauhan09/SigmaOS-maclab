package api

import "testing"

func TestReleaseOlder(t *testing.T) {
	for _, c := range []struct {
		have, than string
		older      bool
	}{
		{"0.2.0", "0.3.0", true},
		{"v0.2.9", "0.10.0", true},
		{"0.2.0", "v0.2.1", true},
		{"0.9.9", "1.0.0", true},
		{"0.2.0-rc.1", "0.2.0", false},
		{"0.2.0", "0.2.0-rc.4", false},
		{"0.3.0-rc.1", "0.2.0", false},
		{"0.2.0", "0.2.0", false},
		{"1.0.0", "0.9.9", false},
		{"dev", "0.2.0", false},
		{"0.2.0", "dev", false},
		{"", "0.2.0", false},
		{"0.2", "0.3.0", false},
		{"0.2.0.1", "0.3.0", false},
		{"0.02.0", "0.3.0", false},
		{"0.2.x", "0.3.0", false},
	} {
		if got := ReleaseOlder(c.have, c.than); got != c.older {
			t.Errorf("ReleaseOlder(%q, %q) = %v", c.have, c.than, got)
		}
	}
}
