package diagnostics

import (
	"fmt"
	"strings"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

type Fact struct {
	Key      string      `json:"key"`
	Value    interface{} `json:"value,omitempty"`
	Redacted bool        `json:"redacted,omitempty"`
}

type Check struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Severity Severity `json:"severity"`
	Detail   string   `json:"detail,omitempty"`
}

type Graph struct {
	Facts            []Fact  `json:"facts,omitempty"`
	Checks           []Check `json:"checks,omitempty"`
	SupportRendering string  `json:"supportRendering,omitempty"`
}

func BuildGraphFromSummary(summary Summary) Graph {
	graph := Graph{}
	graph.addFact("summary.status", summary.Status, false)
	graph.addFact("summary.issue_count", summary.IssueCount, false)
	graph.addFact("runtime.backend_kind", summary.Runtime.BackendKind, false)
	graph.addFact("runtime.phase", summary.Runtime.Phase, false)
	graph.addFact("runtime.generation", summary.Runtime.Generation, false)
	graph.addFact("profile.active_node_mode", summary.Profile.ActiveNodeMode, false)
	graph.addFact("profile.active_node_id", summary.Profile.ActiveNodeID, true)
	graph.addFact("routing.mode", summary.Routing.Mode, false)
	graph.addFact("compatibility.daemon_version", summary.Compatibility.DaemonVersion, false)
	graph.addFact("compatibility.module_version", summary.Compatibility.ModuleVersion, false)

	graph.addCheck(Check{
		ID:       "runtime.readiness",
		Category: "runtime",
		Title:    "Runtime readiness",
		Status:   passFail(summary.Runtime.Ready),
		Severity: severityFor(summary.Runtime.Ready, SeverityCritical),
		Detail:   firstNonEmptyString(summary.HealthDetail, summary.Runtime.LastCode),
	})
	graph.addCheck(Check{
		ID:       "runtime.operational_health",
		Category: "runtime",
		Title:    "Operational health",
		Status:   passFail(summary.Runtime.OperationalReady),
		Severity: severityFor(summary.Runtime.OperationalReady, SeverityError),
		Detail:   summary.Runtime.LastCode,
	})
	graph.addCheck(Check{
		ID:       "reset.cleanup_leftovers",
		Category: "reset",
		Title:    "Network cleanup leftovers",
		Status:   passFail(!summary.RebootRequired),
		Severity: severityFor(!summary.RebootRequired, SeverityCritical),
	})
	graph.addCheck(Check{
		ID:       "compatibility.sing_box_config",
		Category: "compatibility",
		Title:    "sing-box config check",
		Status:   passFail(summary.Compatibility.SingBoxCheckOK),
		Severity: severityFor(summary.Compatibility.SingBoxCheckOK, SeverityError),
	})
	graph.addCheck(Check{
		ID:       "compatibility.module_version",
		Category: "compatibility",
		Title:    "Module version known",
		Status:   passFail(summary.Compatibility.ModuleVersion != "" && summary.Compatibility.ModuleVersion != "unknown"),
		Severity: severityFor(summary.Compatibility.ModuleVersion != "" && summary.Compatibility.ModuleVersion != "unknown", SeverityWarning),
	})
	graph.addCheck(Check{
		ID:       "compatibility.release_integrity",
		Category: "compatibility",
		Title:    "Current release integrity",
		Status:   passFail(summary.Compatibility.CurrentReleaseOK),
		Severity: severityFor(summary.Compatibility.CurrentReleaseOK, SeverityError),
		Detail:   summary.Compatibility.CurrentReleaseVersion,
	})
	graph.addCheck(Check{
		ID:       "nodes.url_dataplane",
		Category: "nodes",
		Title:    "Node data-plane probes",
		Status:   passFail(summary.NodeTests.TCPOnly == 0 && summary.NodeTests.Unusable == 0),
		Severity: severityFor(summary.NodeTests.TCPOnly == 0 && summary.NodeTests.Unusable == 0, SeverityWarning),
		Detail:   fmt.Sprintf("total=%d usable=%d tcp_only=%d unusable=%d", summary.NodeTests.Total, summary.NodeTests.Usable, summary.NodeTests.TCPOnly, summary.NodeTests.Unusable),
	})
	for idx, issue := range summary.PrivacyIssues {
		graph.addCheck(Check{
			ID:       fmt.Sprintf("privacy.issue.%d", idx+1),
			Category: "privacy",
			Title:    "Privacy check",
			Status:   "fail",
			Severity: SeverityWarning,
			Detail:   issue,
		})
	}
	for idx, issue := range summary.CompatibilityIssues {
		graph.addCheck(Check{
			ID:       fmt.Sprintf("compatibility.issue.%d", idx+1),
			Category: "compatibility",
			Title:    "Compatibility check",
			Status:   "fail",
			Severity: SeverityError,
			Detail:   issue,
		})
	}
	graph.SupportRendering = graph.renderSupport()
	return graph
}

func (g *Graph) addFact(key string, value interface{}, redacted bool) {
	if key == "" || value == nil || value == "" {
		return
	}
	if redacted {
		value = RedactText(fmt.Sprint(value))
	}
	g.Facts = append(g.Facts, Fact{Key: key, Value: value, Redacted: redacted})
}

func (g *Graph) addCheck(check Check) {
	if check.ID == "" {
		return
	}
	if check.Status == "" {
		check.Status = "pass"
	}
	if check.Severity == "" {
		check.Severity = SeverityInfo
	}
	check.Detail = RedactText(strings.TrimSpace(check.Detail))
	g.Checks = append(g.Checks, check)
}

func (g Graph) renderSupport() string {
	lines := make([]string, 0, len(g.Checks)+len(g.Facts)+2)
	lines = append(lines, "Diagnostics summary")
	for _, fact := range g.Facts {
		lines = append(lines, fmt.Sprintf("- %s: %v", fact.Key, fact.Value))
	}
	for _, check := range g.Checks {
		if check.Status == "pass" {
			continue
		}
		detail := check.Detail
		if detail != "" {
			detail = ": " + detail
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s %s%s", check.Severity, check.ID, check.Status, detail))
	}
	return RedactText(strings.Join(lines, "\n"))
}

func passFail(pass bool) string {
	if pass {
		return "pass"
	}
	return "fail"
}

func severityFor(pass bool, failure Severity) Severity {
	if pass {
		return SeverityInfo
	}
	return failure
}
