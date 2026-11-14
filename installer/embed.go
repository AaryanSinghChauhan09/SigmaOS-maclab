// Package installer carries the two install scripts into labd, which serves
// them as the lab's install lines: /install for the test tool, /install/lab
// for lab.
package installer

import _ "embed"

// Script is install.sh.
//
//go:embed install.sh
var Script []byte

// LabScript is install-lab.sh.
//
//go:embed install-lab.sh
var LabScript []byte
