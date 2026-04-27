package main

import (
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
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
	return diagnostics.BuildSummary(
		Version,
		controlProtocolVersion,
		healthSnapshot,
		leftovers,
		netstackRuntimeReport,
		nodeResults,
		ports,
		privacy,
		moduleVersion,
		singBoxCheck,
		releaseIntegrity,
		profileSummary,
		routingSummary,
		packageResolution,
	)
}

func summarizeDiagnosticRuntime(healthSnapshot runtimev2.HealthSnapshot) diagnosticRuntimeSummary {
	return diagnostics.RuntimeSummaryFromHealth(healthSnapshot)
}

func diagnosticNetstackRuntimeIssues(report netstack.Report) []string {
	return diagnostics.NetstackRuntimeIssues(report)
}

func summarizeDiagnosticNodeTests(results []runtimev2.NodeProbeResult) diagnosticNodeTestSummary {
	return diagnostics.NodeTestSummaryFromResults(results)
}

func diagnosticRoutingSummaryFromConfig(cfg *config.Config) diagnosticRoutingSummary {
	return diagnostics.RoutingSummaryFromConfig(cfg)
}

func diagnosticProfileSummaryFromConfig(cfg *config.Config, status runtimev2.Status) diagnosticProfileSummary {
	return diagnostics.ProfileSummaryFromConfig(cfg, status)
}

func diagnosticPackageResolutionFromConfig(cfg *config.Config) diagnosticPackageResolution {
	return diagnostics.PackageResolutionFromConfig(cfg)
}

func diagnosticPrivacyIssues(privacy map[string]interface{}) []string {
	return diagnostics.PrivacyIssues(privacy)
}
