// Package bench holds the lab's benchmark runner and the manifest of what it
// runs, pinned by sha256. lab-agent runs the runner; `lab bench mirror` puts
// every pinned file in labd's artifact store, from where lab-agent fetches it.
package bench

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
)

//go:embed bench.py
var Runner []byte

//go:embed benchmarks.json
var ManifestJSON []byte

// Item is one pinned file: a benchmark web app, a program, a source tree or a
// clip.
type Item struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Root    string `json:"root,omitempty"`
	Entry   string `json:"entry,omitempty"`
	Note    string `json:"note,omitempty"`
}

var reSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Manifest is the pinned items.
func Manifest() ([]Item, error) { return ParseManifest(ManifestJSON) }

// ParseManifest reads a manifest and checks every item names a sha256.
func ParseManifest(b []byte) ([]Item, error) {
	var m struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, it := range m.Items {
		switch {
		case it.Name == "" || seen[it.Name]:
			return nil, fmt.Errorf("benchmark item %q is unnamed or given twice", it.Name)
		case !reSHA.MatchString(it.SHA256):
			return nil, fmt.Errorf("benchmark item %s has no sha256", it.Name)
		case it.URL == "" || it.Version == "":
			return nil, fmt.Errorf("benchmark item %s has no url or version", it.Name)
		}
		seen[it.Name] = true
	}
	return m.Items, nil
}

// JSON is the manifest the runner reads, for these items.
func JSON(items []Item) ([]byte, error) {
	return json.MarshalIndent(struct {
		Schema int    `json:"schema"`
		Items  []Item `json:"items"`
	}{1, items}, "", "  ")
}
