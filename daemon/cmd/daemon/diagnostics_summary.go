package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func buildDiagnosticSummary(
	healthSnapshot runtimev2.HealthSnapshot,
	leftovers []string,
	netstackRuntimeReport netstack.Report,
	nodeResults []runtimev2.NodeProbeResult,
	ports []diagnosticPortStatus,
	privacy map[string]interface{},
	moduleVersion map[string]string,
	singBoxCheck diagnosticCommandResult,
	releaseIntegrity diagnosticReleaseIntegrity,
	profileSummary diagnosticProfileSummary,
	routingSummary diagnosticRoutingSummary,
	packageResolution diagnosticPackageResolution,
) diagnosticSummary {
	summary := diagnosticSummary{
		Status:             "ok",
		HealthCode:         healthSnapshot.LastCode,
		HealthDetail:       healthSnapshot.LastError,
		OperationalHealthy: healthSnapshot.OperationalHealthy(),
		Compatibility: diagnosticCompatSummary{
			DaemonVersion:          Version,
			ModuleVersion:          firstNonEmpty(moduleVersion["version"], "unknown"),
			ControlProtocolVersion: controlProtocolVersion,
			SchemaVersion:          config.CurrentSchemaVersion,
			PanelMinVersion:        Version,
			CurrentReleaseVersion:  releaseIntegrity.Version,
			CurrentReleaseOK:       releaseIntegrity.OK,
			SingBoxCheckOK:         singBoxCheck.Error == "",
		},
		Runtime:           summarizeDiagnosticRuntime(healthSnapshot),
		Profile:           profileSummary,
		Routing:           routingSummary,
		NodeTests:         summarizeDiagnosticNodeTests(nodeResults),
		PackageResolution: packageResolution,
	}
	addIssue := func(issue string) {
		if strings.TrimSpace(issue) == "" {
			return
		}
		summary.Issues = append(summary.Issues, issue)
	}
	addCompatibility := func(issue string) {
		if strings.TrimSpace(issue) == "" {
			return
		}
		summary.CompatibilityIssues = append(summary.CompatibilityIssues, issue)
		addIssue("compatibility: " + issue)
	}
	addPrivacy := func(issue string) {
		if strings.TrimSpace(issue) == "" {
			return
		}
		summary.PrivacyIssues = append(summary.PrivacyIssues, issue)
		addIssue("privacy: " + issue)
	}

	if !healthSnapshot.Healthy() {
		addIssue(firstNonEmpty(healthSnapshot.LastError, "readiness checks are failing"))
		summary.Status = "failed"
	} else if !healthSnapshot.OperationalHealthy() {
		addIssue(firstNonEmpty(healthSnapshot.LastError, "operational checks are degraded"))
		summary.Status = "degraded"
	}
	if len(leftovers) > 0 {
		summary.RebootRequired = true
		summary.Status = "partial_reset"
		addIssue("network cleanup leftovers remain")
	}
	for _, issue := range diagnosticNetstackRuntimeIssues(netstackRuntimeReport) {
		addIssue(issue)
		if summary.Status == "ok" || summary.Status == "degraded" {
			summary.Status = "failed"
		}
	}
	if singBoxCheck.Error != "" {
		addCompatibility("sing-box config check failed: " + singBoxCheck.Error)
	}
	if moduleVersion["version"] == "" || moduleVersion["version"] == "unknown" {
		addCompatibility("module version is unknown")
	}
	for _, issue := range diagnosticReleaseIntegrityIssues(releaseIntegrity) {
		addCompatibility(issue)
	}
	for _, port := range ports {
		switch port.Port {
		case 10808, 10809, 9090:
			if port.TCPListening {
				addPrivacy("production localhost helper/API port is listening")
			}
		}
		if port.Conflict {
			addIssue(fmt.Sprintf("local port %d has conflicting roles: %s", port.Port, port.Role))
		}
	}
	for _, issue := range diagnosticPrivacyIssues(privacy) {
		addPrivacy(issue)
	}
	for _, warning := range packageResolution.Warnings {
		addIssue(warning)
	}
	if summary.NodeTests.TCPOnly > 0 {
		addIssue("one or more nodes have TCP reachability but failed URL/data-plane checks")
	}

	summary.IssueCount = len(summary.Issues)
	if summary.Status == "ok" && summary.IssueCount > 0 {
		summary.Status = "degraded"
	}
	return summary
}

func summarizeDiagnosticRuntime(healthSnapshot runtimev2.HealthSnapshot) diagnosticRuntimeSummary {
	report, ok := healthSnapshot.StageReport.(core.RuntimeStageReport)
	if !ok || report.Empty() {
		return diagnosticRuntimeSummary{LastCode: healthSnapshot.LastCode}
	}
	summary := diagnosticRuntimeSummary{
		StageOperation:  report.Operation,
		StageStatus:     report.Status,
		FailedStage:     report.FailedStage,
		LastCode:        firstNonEmpty(report.LastCode, healthSnapshot.LastCode),
		RollbackApplied: report.RollbackApplied,
	}
	reportAt := report.FinishedAt
	if reportAt.IsZero() {
		reportAt = report.StartedAt
	}
	if !reportAt.IsZero() {
		summary.RuntimeReportAge = time.Since(reportAt).Truncate(time.Second).String()
	}
	return summary
}

func diagnosticNetstackRuntimeIssues(report netstack.Report) []string {
	switch report.Status {
	case "failed", "partial":
	default:
		return nil
	}
	detail := strings.Join(report.Errors, "; ")
	if detail == "" {
		for _, step := range report.Steps {
			if step.Status == "failed" && step.Detail != "" {
				detail = step.Detail
				break
			}
		}
	}
	if detail == "" {
		detail = firstNonEmpty(report.Operation, "verify")
	}
	return []string{"runtime netstack verification failed: " + detail}
}

func summarizeDiagnosticNodeTests(results []runtimev2.NodeProbeResult) diagnosticNodeTestSummary {
	summary := diagnosticNodeTestSummary{Total: len(results)}
	for _, result := range results {
		if result.Verdict == "usable" {
			summary.Usable++
		}
		if result.Verdict == "unusable" {
			summary.Unusable++
		}
		if result.TCPStatus == "ok" && result.URLStatus != "ok" {
			summary.TCPOnly++
		}
	}
	return summary
}

func diagnosticRoutingSummaryFromConfig(cfg *config.Config) diagnosticRoutingSummary {
	if cfg == nil {
		return diagnosticRoutingSummary{ActiveNodeMode: "config_unavailable"}
	}
	type nodeMeta struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
		Group    string `json:"group"`
	}
	nodes := make([]nodeMeta, 0, len(cfg.Profile.Nodes))
	groupSet := map[string]bool{}
	for _, raw := range cfg.Profile.Nodes {
		var node nodeMeta
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		nodes = append(nodes, node)
		if group := strings.TrimSpace(node.Group); group != "" {
			groupSet[group] = true
		}
	}
	groups := make([]string, 0, len(groupSet))
	for group := range groupSet {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	summary := diagnosticRoutingSummary{
		Mode:               cfg.Routing.Mode,
		ActiveNodeMode:     "none",
		NodeCount:          len(nodes),
		Groups:             groups,
		AppGroupRouteCount: len(cfg.Apps.AppGroups),
		SharingEnabled:     cfg.Sharing.Enabled,
	}
	activeID := strings.TrimSpace(cfg.Profile.ActiveNodeID)
	if activeID != "" {
		summary.ActiveNodeMode = "manual"
		summary.ActiveNodeID = activeID
		for _, node := range nodes {
			if node.ID == activeID {
				summary.ActiveNodeName = node.Name
				summary.ActiveNodeProtocol = node.Protocol
				return summary
			}
		}
		summary.ActiveNodeMode = "manual_missing"
		return summary
	}
	if len(nodes) == 1 {
		summary.ActiveNodeMode = "single_node"
		summary.ActiveNodeID = nodes[0].ID
		summary.ActiveNodeName = nodes[0].Name
		summary.ActiveNodeProtocol = nodes[0].Protocol
		return summary
	}
	if len(nodes) > 1 {
		summary.ActiveNodeMode = "auto_selector"
		summary.ActiveNodeName = "Auto"
		summary.ActiveNodeProtocol = "selector"
	}
	return summary
}

func diagnosticProfileSummaryFromConfig(cfg *config.Config, status runtimev2.Status) diagnosticProfileSummary {
	if cfg == nil {
		return diagnosticProfileSummary{ActiveNodeMode: "config_unavailable"}
	}
	doc := profiledoc.FromConfig(cfg)
	summary := diagnosticProfileSummary{
		SchemaVersion:     doc.SchemaVersion,
		DesiredGeneration: status.AppliedState.Generation,
		AppliedGeneration: status.AppliedState.Generation,
		ActiveNodeMode:    "auto",
		ActiveNodeID:      doc.ActiveNodeID,
		NodeCount:         len(doc.Nodes),
		SubscriptionCount: len(doc.Subscriptions),
	}
	if status.ActiveOperation != nil {
		summary.DesiredGeneration = status.ActiveOperation.Generation
	}
	if status.DesiredState.ActiveProfileID != "" {
		summary.ActiveNodeID = status.DesiredState.ActiveProfileID
	}
	if doc.ActiveNodeID != "" {
		summary.ActiveNodeMode = "manual"
	}
	for _, node := range doc.Nodes {
		if node.Stale {
			summary.StaleNodeCount++
			if node.Source.Type == "SUBSCRIPTION" {
				summary.StaleSubscriptionNodes++
			}
		} else {
			summary.LiveNodeCount++
		}
	}
	if status.LastOperation != nil {
		summary.LastOperation = string(status.LastOperation.Kind)
		if status.LastOperation.Succeeded {
			summary.LastOperationStatus = "ok"
		} else {
			summary.LastOperationStatus = "failed"
		}
	}
	return summary
}

func diagnosticPackageResolutionFromConfig(cfg *config.Config) diagnosticPackageResolution {
	if cfg == nil {
		return diagnosticPackageResolution{Mode: "config_unavailable"}
	}
	appMode := core.MapAppMode(cfg.Apps.Mode)
	resolution := core.BuildPackageRoutingResolution(cfg.Apps.Packages, cfg.Routing.AlwaysDirectApps)
	report := diagnosticPackageResolution{
		Mode:                         appMode,
		RequestedPackages:            append([]string(nil), resolution.Selected.RequestedPackages...),
		SelectedSource:               resolution.Selected.Source,
		ResolvedUIDCount:             len(resolution.Selected.UIDs),
		UnresolvedPackages:           append([]string(nil), resolution.Selected.UnresolvedPackages...),
		AlwaysDirectSource:           resolution.AlwaysDirect.Source,
		AlwaysDirectResolvedUIDCount: len(resolution.AlwaysDirect.UIDs),
		Sources:                      append([]core.PackageUIDSourceStatus(nil), resolution.Sources...),
		Errors:                       append([]string(nil), resolution.Errors...),
	}
	if (appMode == "whitelist" || appMode == "blacklist") &&
		len(report.RequestedPackages) > 0 &&
		report.ResolvedUIDCount == 0 {
		report.Warnings = append(report.Warnings, "per-app routing is enabled but selected packages resolved to zero UIDs")
	}
	return report
}

func diagnosticPrivacyIssues(privacy map[string]interface{}) []string {
	rawChecks, _ := privacy["checks"].(map[string]interface{})
	if len(rawChecks) == 0 {
		return nil
	}
	labels := map[string]string{
		"no_vpn_like_interfaces":      "VPN-like network interface is visible",
		"no_transport_vpn_hint":       "Connectivity diagnostics expose VPN transport",
		"no_loopback_dns":             "LinkProperties exposes loopback DNS",
		"system_proxy_unset":          "system proxy setting is not empty",
		"clash_api_disabled":          "Clash/API port is enabled",
		"helper_inbounds_disabled":    "HTTP/SOCKS helper inbound is enabled",
		"helper_inbounds_local_only":  "helper inbound allows LAN access",
		"per_app_whitelist_default":   "app routing is not whitelist/off",
		"dns_hijack_per_uid":          "DNS hijack is not scoped per UID",
		"self_test_apps_direct":       "self-test packages are not protected direct-only",
		"localhost_proxy_ports_clear": "localhost proxy port is visible",
	}
	issues := make([]string, 0)
	keys := make([]string, 0, len(rawChecks))
	for key := range rawChecks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, ok := rawChecks[key].(bool)
		if ok && !value {
			issues = append(issues, firstNonEmpty(labels[key], key))
		}
	}
	return issues
}
