package reports

import (
	"fmt"
	"strings"
)

// MaxEvidenceBytes is how much evidence one report may carry.
const MaxEvidenceBytes = 64 * 1024

// EvidenceErrors gives the problems with a report's evidence that the schema
// can't express: evidence is text only, and at most 64 KiB a report. Empty
// when it is fine; never more than ten. The report must match the schema.
func EvidenceErrors(report *Object) []string {
	var lines []string
	checks, _ := report.members["checks"].([]any)
	for _, check := range checks {
		if check, ok := check.(*Object); ok {
			evidence, _ := check.members["evidence"].([]any)
			for _, line := range evidence {
				text, _ := line.(string)
				lines = append(lines, text)
			}
		}
	}
	var problems []string
	total := 0
	for _, line := range lines {
		total += len(line)
	}
	if total > MaxEvidenceBytes {
		problems = append(problems, fmt.Sprintf("evidence is %d bytes; a report carries at most %d", total, MaxEvidenceBytes))
	}
	for i, line := range lines {
		if len(problems) == 10 {
			break
		}
		if strings.ContainsFunc(line, notText) {
			problems = append(problems, fmt.Sprintf("evidence line %d is not text", i+1))
		}
	}
	return problems
}

func notText(r rune) bool {
	return r <= 0x08 || (r >= 0x0a && r <= 0x1f) || r == '�'
}
