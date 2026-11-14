package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// What may be published at all is TestOnlyAKnownGoodRunOnAPackagedKernelIsPublished
// (publish_test.go): the report goes in inside labd, so its cases are real
// reports. This is the guard those cases meet.
func TestPackagedOMTReportRejectsMalformedOrWrongKernel(t *testing.T) {
	for _, body := range []string{`no JSON`, `{}`, `{"machine":{"kernel":"wrong"},"checks":[{"id":"boot.kernel-package","status":"pass"}]}`} {
		if err := packagedOMTReport([]byte(body), "current"); err == nil {
			t.Fatal(body)
		}
	}
	// Guard consumes exactly the report identity used by the existing reader.
	var rep omtReport
	if err := json.Unmarshal([]byte(`{"machine":{"kernel":"current"},"checks":[{"id":"boot.kernel-package","status":"pass"}]}`), &rep); err != nil || !strings.Contains(rep.Machine.Kernel, "current") {
		t.Fatal(err)
	}
}
