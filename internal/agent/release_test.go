package agent

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

func TestCheckinCarriesBothNumbers(t *testing.T) {
	was := LabRelease
	LabRelease = "v0.2.0-rc.1"
	t.Cleanup(func() { LabRelease = was })
	checkins := make(chan api.Checkin, 1)
	a := factsAgent(t, func(w http.ResponseWriter, ci api.Checkin) {
		checkins <- ci
		io.WriteString(w, "{}")
	})
	if err := a.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	ci := <-checkins
	if ci.Facts == nil || ci.Facts.AgentVersion != Version || ci.Facts.LabRelease != "v0.2.0-rc.1" {
		t.Fatalf("first check-in's facts: %+v, want lab-agent %s of Lab release v0.2.0-rc.1", ci.Facts, Version)
	}
}

func TestReleaseSigners(t *testing.T) {
	was := ReleaseKey
	t.Cleanup(func() { ReleaseKey = was })
	ReleaseKey = ""
	if got := ReleaseSigners(); got != "" {
		t.Errorf("a build with no release key pins %q", got)
	}
	ReleaseKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKHA9ywzf4hxAd5Ohzw8tRg5i2lxHAxp2mDV0PWi5o0u"
	want := `release@omarchy-m-testing.org namespaces="omarchy-m-test-release" ` + ReleaseKey + "\n"
	if got := ReleaseSigners(); got != want {
		t.Errorf("allowed signers: %q, want %q", got, want)
	}
}
