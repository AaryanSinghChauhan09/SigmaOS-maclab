package server

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The edge answers labd's 502 and 504 with its own page, so the line labd
// wrote never reaches lab or the browser. A Mac's own refusal is the
// caller's to read (409); labd failing to reach something is 503.
func TestLabdNeverAnswers502Or504(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	gateway := regexp.MustCompile(`\b(50[24])\b|StatusBadGateway|StatusGatewayTimeout`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if code, _, _ := strings.Cut(line, "//"); gateway.MatchString(code) {
				t.Errorf("%s:%d answers a gateway status: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
	if c := statusOf(agentError{kind: "update", msg: "refused. Nothing was changed."}); c != 409 {
		t.Errorf("a Mac's refusal: %d", c)
	}
	if c := statusOf(errors.New("timed out")); c != 503 {
		t.Errorf("a Mac out of reach: %d", c)
	}
}
