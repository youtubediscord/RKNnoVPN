package diagnostics

import (
	"strings"
	"testing"
)

func TestBuildGraphFromSummaryRedactsSupportRendering(t *testing.T) {
	summary := Summary{
		Status:         "failed",
		IssueCount:     1,
		HealthDetail:   `uuid="11111111-1111-1111-1111-111111111111" failed`,
		RebootRequired: true,
		Compatibility:  CompatSummary{DaemonVersion: "v1.8.0", ModuleVersion: "unknown"},
		Runtime:        RuntimeSummary{Ready: false, LastCode: "CORE_FAILED"},
		Profile:        ProfileSummary{ActiveNodeID: "11111111-1111-1111-1111-111111111111"},
		CompatibilityIssues: []string{
			`public_key="secret-key" mismatch`,
		},
	}

	graph := BuildGraphFromSummary(summary)
	rendered := graph.SupportRendering
	if strings.Contains(rendered, "11111111-1111-1111-1111-111111111111") || strings.Contains(rendered, "secret-key") {
		t.Fatalf("support rendering was not redacted: %s", rendered)
	}
	if !strings.Contains(rendered, "[redacted-uuid]") || !strings.Contains(rendered, `[redacted]`) {
		t.Fatalf("support rendering missing redaction markers: %s", rendered)
	}
	if len(graph.Checks) == 0 {
		t.Fatalf("expected checks in graph")
	}
}
