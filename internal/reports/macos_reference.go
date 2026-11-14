package reports

import (
	"fmt"
	"math/big"
	"regexp"

	"github.com/omacom/omarchy-maclab/catalogue"
)

// MacOSReference is what macOS measures on a Mac
// (catalogue/macos-reference/<board>.json): shown beside Linux's benchmark
// scores as a description, never a verdict. Storage numbers are keyed by
// capacity, because an SSD's speed depends on its size.
type MacOSReference struct{ document *Object }

// Benchmark check => the metric to show. No entry: macOS has no counterpart.
var macOSMetrics = map[string]string{"benchmark.h264-decode": "h264_decode_fps", "benchmark.hevc-decode": "hevc_decode_fps"}

const (
	macOSStorageCheck  = "benchmark.storage"
	macOSStorageMetric = "read_mbps"
)

var boardRE = regexp.MustCompile(`\Aj[0-9]{3}[a-z]{0,2}\z`)

// MacOSReferenceFor gives the reference of a board, or nil without one.
func MacOSReferenceFor(board string) *MacOSReference {
	if !boardRE.MatchString(board) {
		return nil
	}
	data, err := catalogue.MacOSReference.ReadFile("macos-reference/" + board + ".json")
	if err != nil {
		return nil
	}
	document, err := Parse(data)
	if object, ok := document.(*Object); err == nil && ok {
		return &MacOSReference{object}
	}
	return nil
}

// MacOSCompared says whether macOS has a number for this benchmark check.
func MacOSCompared(checkID string) bool {
	return macOSMetrics[checkID] != "" || checkID == macOSStorageCheck
}

// Value gives the macOS number for a benchmark (a *big.Int or a Float), or
// nil. capacity matters for storage only.
func (m *MacOSReference) Value(checkID, capacity string) any {
	if checkID == macOSStorageCheck {
		return member(m.document, "storage", capacity, macOSStorageMetric)
	}
	if metric := macOSMetrics[checkID]; metric != "" {
		return member(m.document, "metrics", metric)
	}
	return nil
}

func (m *MacOSReference) HasStorage() bool {
	storage, _ := m.document.members["storage"].(*Object)
	return storage != nil && len(storage.keys) > 0
}

// Conditions says how the numbers were taken.
func (m *MacOSReference) Conditions() string {
	power := "on battery"
	if member(m.document, "conditions", "power") == "ac" {
		power = "on AC"
	}
	displays := "displays"
	if count, ok := member(m.document, "conditions", "displays").(*big.Int); ok && count.IsInt64() && count.Int64() == 1 {
		displays = "display"
	}
	return fmt.Sprintf("macOS %s, %s, brightness %s, %s %s, %s", rubyToS(member(m.document, "macos", "version")), power,
		rubyToS(member(m.document, "conditions", "brightness")), rubyToS(member(m.document, "conditions", "displays")), displays, rubyToS(m.document.members["captured"]))
}
