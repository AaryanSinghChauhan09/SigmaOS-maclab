// Package catalogue carries the feature catalogue and the macOS reference
// files into the lab's binaries.
package catalogue

import "embed"

// JSON is catalogue.json.
//
//go:embed catalogue.json
var JSON []byte

// MacOSReference holds macos-reference/<board>.json.
//
//go:embed macos-reference/*.json
var MacOSReference embed.FS
