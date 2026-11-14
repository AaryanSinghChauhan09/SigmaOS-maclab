// Package installer carries the test tool's install script into labd, which
// serves it as the lab's install line.
package installer

import _ "embed"

// Script is install.sh.
//
//go:embed install.sh
var Script []byte
