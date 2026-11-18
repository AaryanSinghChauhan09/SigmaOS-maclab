package server

import (
	"os"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent/tests/bench"
)

// The pinned benchmark files stay in the artifact store however old, and over
// budget: lab-agent fetches them only from labd.
func TestMirroredBenchmarkFilesAreNeverCollected(t *testing.T) {
	l := newLab(t, false)
	old := time.Now().Add(-48 * time.Hour)
	items, err := bench.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if err := os.WriteFile(l.s.artifactPath(it.SHA256), []byte("benchmark fixture "+it.Name), 0600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(l.s.artifactPath(it.SHA256), old, old)
	}
	l.s.cfg.ArtifactBudget = 1
	l.s.gcArtifacts()
	for _, it := range items {
		if _, err := os.Stat(l.s.artifactPath(it.SHA256)); err != nil {
			t.Errorf("%s collected: %v", it.Name, err)
		}
	}
}
