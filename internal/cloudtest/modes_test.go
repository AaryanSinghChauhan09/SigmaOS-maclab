package cloudtest

import (
	"strings"
	"testing"
)

// Two modes are refused before anything is sent: the last one used to win,
// so "--dry-run --apply" changed Cloudflare.
func TestTwoModesAreRefused(t *testing.T) {
	for _, name := range runnable {
		f := asSpec(t)
		for _, modes := range [][]string{{"--dry-run", "--apply"}, {"--check", "--apply"}, {"--apply", "--dry-run"}} {
			out, code := script(t, f, nil, name, append(modes, "--token-file", f.tokenFile())...)
			if code == 0 || !strings.Contains(out, "two modes") {
				t.Errorf("%s %v: exit %d\n%s", name, modes, code, out)
			}
		}
		if n := len(f.sent()); n != 0 {
			t.Errorf("%s: a refused run made %d calls", name, n)
		}
	}
}
