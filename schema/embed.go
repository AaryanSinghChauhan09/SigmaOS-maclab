// Package schema carries the report schema into the lab's binaries.
package schema

import _ "embed"

// ReportV1 is report-v1.schema.json.
//
//go:embed report-v1.schema.json
var ReportV1 []byte
