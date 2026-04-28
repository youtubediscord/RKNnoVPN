package main

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/control"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/modulecontract"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) handleDiagnosticsReport(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	request, err := control.DecodeDiagnosticsReportParams(params)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: err.Error(),
		}
	}
	lines := request.Lines

	d.mu.Lock()
	cfg := d.cfg
	cfgPath := d.cfgPath
	profilePath := d.profilePath
	dataDir := d.dataDir
	d.mu.Unlock()
	if profilePath == "" && cfgPath != "" {
		profilePath = profiledoc.Path(cfgPath)
	}

	modulePaths := modulecontract.NewPaths(dataDir)
	renderedConfigPath := filepath.Join(modulePaths.RenderedConfigDir(), "singbox.json")
	singBoxPath := filepath.Join(modulePaths.BinDir(), "sing-box")

	healthResult := d.healthMon.RunOnce()
	healthSnapshot := d.buildRuntimeV2HealthSnapshot(healthResult, true)
	var backendStatus interface{}
	var runtimeStatus runtimev2.Status
	if d.runtimeV2 != nil {
		runtimeStatus = d.runtimeV2.Status()
		backendStatus = runtimeStatus
	}
	moduleVersion := diagnostics.ReadModuleVersion()
	ports := diagnostics.PortStatuses(cfg)
	portConflicts := diagnostics.LocalPortConflicts(cfg)
	netstackReport := d.diagnosticNetstackReport(cfg)
	netstackRuntimeReport := d.diagnosticNetstackRuntimeReport(cfg)
	leftovers := netstackReport.Leftovers
	var nodeResults []runtimev2.NodeProbeResult
	if cfg != nil {
		nodeResults = d.testNodeProbesV2(cfg.Health.URL, 2500, nil)
	}
	privacy := diagnostics.Privacy(cfg, lines, core.ExecCommand)
	singBoxCheck := diagnostics.SingBoxCheck(singBoxPath, renderedConfigPath, lines, core.ExecCommand)
	releaseIntegrity := diagnostics.ReleaseIntegrityReport(dataDir)
	routingSummary := diagnostics.RoutingSummaryFromConfig(cfg)
	profileSummary := diagnostics.ProfileSummaryFromConfig(cfg, runtimeStatus)
	packageResolution := diagnostics.PackageResolutionFromConfig(cfg)
	summary := diagnostics.BuildSummaryWithCanonical(Version, controlProtocolVersion, runtimeStatus.Canonical, healthSnapshot, leftovers, netstackRuntimeReport, nodeResults, ports, privacy, moduleVersion, singBoxCheck, releaseIntegrity, profileSummary, routingSummary, packageResolution)
	versions := map[string]interface{}{
		"daemon":                   Version,
		"core":                     Version,
		"daemonctl_expected":       Version,
		"control_protocol_version": controlProtocolVersion,
		"schema_version":           config.CurrentSchemaVersion,
		"panel_min_version":        Version,
		"capabilities":             ipc.SupportedCapabilities(),
		"supported_methods":        ipc.SupportedMethods(),
		"sing_box":                 diagnostics.SingBoxVersion(singBoxPath, lines, core.ExecCommand),
		"module":                   moduleVersion,
	}

	report := map[string]interface{}{
		"generated_at":      time.Now().Format(time.RFC3339),
		"summary":           summary,
		"diagnostics_graph": summary.Graph,
		"versions":          versions,
		"device":            diagnostics.DeviceCommands(lines, core.ExecCommand),
		"paths": map[string]diagnostics.FileStatus{
			"data_dir":          diagnostics.StatFile(dataDir, false),
			"current_release":   diagnostics.StatFile(filepath.Join(dataDir, "current"), false),
			"releases_dir":      diagnostics.StatFile(filepath.Join(dataDir, "releases"), false),
			"config":            diagnostics.StatFile(cfgPath, false),
			"profile":           diagnostics.StatFile(profilePath, false),
			"rendered_singbox":  diagnostics.StatFile(renderedConfigPath, false),
			"sing_box_binary":   diagnostics.StatFile(singBoxPath, true),
			"daemon_log":        diagnostics.StatFile(filepath.Join(modulePaths.LogDir(), "daemon.log"), false),
			"sing_box_log":      diagnostics.StatFile(filepath.Join(modulePaths.LogDir(), "sing-box.log"), false),
			"daemon_socket":     diagnostics.StatFile(modulePaths.DaemonSocket(), false),
			"sing_box_pid_file": diagnostics.StatFile(modulePaths.SingBoxPIDFile(), false),
		},
		"health": map[string]interface{}{
			"snapshot": healthSnapshot,
			"raw":      healthResult,
		},
		"canonical_status":    runtimeStatus.Canonical,
		"backend_status":      backendStatus,
		"core_start_report":   d.coreMgr.LastStartReport(),
		"core_runtime_report": d.coreMgr.LastRuntimeReport(),
		"reload_report":       d.LastReloadReport(),
		"ports":               ports,
		"port_conflicts":      portConflicts,
		"routing":             routingSummary,
		"profile":             profileSummary,
		"package_resolution":  packageResolution,
		"netstack":            netstackReport,
		"netstack_runtime":    netstackRuntimeReport,
		"leftovers":           leftovers,
		"node_tests":          diagnostics.RedactNodeProbeResults(nodeResults),
		"logs":                diagnostics.ReadLogSections(diagnostics.DefaultLogFileSpecs(dataDir), lines, 512*1024, diagnostics.RedactText),
		"config": map[string]diagnostics.JSONSection{
			"daemon":           diagnostics.ReadRedactedJSONFile(cfgPath),
			"profile":          diagnostics.ReadRedactedJSONFile(profilePath),
			"rendered_singbox": diagnostics.ReadRedactedJSONFile(renderedConfigPath),
		},
		"runtime":           diagnostics.RuntimeCommands(lines, core.ExecCommand),
		"privacy":           privacy,
		"release_integrity": releaseIntegrity,
	}

	report["sing_box_check"] = singBoxCheck
	return report, nil
}

func (d *daemon) handleSelfCheck(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	summary, err := d.buildSelfCheckSummary(80)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}
	return summary, nil
}

func (d *daemon) buildSelfCheckSummary(lines int) (diagnostics.Summary, error) {
	d.mu.Lock()
	cfg := d.cfg
	dataDir := d.dataDir
	d.mu.Unlock()
	if cfg == nil {
		return diagnostics.Summary{Status: "failed", Issues: []string{"config unavailable"}, IssueCount: 1}, nil
	}
	if lines <= 0 {
		lines = 80
	}
	modulePaths := modulecontract.NewPaths(dataDir)
	renderedConfigPath := filepath.Join(modulePaths.RenderedConfigDir(), "singbox.json")
	singBoxPath := filepath.Join(modulePaths.BinDir(), "sing-box")
	healthResult := d.healthMon.RunOnce()
	healthSnapshot := d.buildRuntimeV2HealthSnapshot(healthResult, true)
	netstackReport := d.diagnosticNetstackReport(cfg)
	netstackRuntimeReport := d.diagnosticNetstackRuntimeReport(cfg)
	var nodeResults []runtimev2.NodeProbeResult
	if cfg != nil {
		nodeResults = d.testNodeProbesV2(cfg.Health.URL, 2500, nil)
	}
	var runtimeStatus runtimev2.Status
	if d.runtimeV2 != nil {
		runtimeStatus = d.runtimeV2.Status()
	}
	return diagnostics.BuildSummaryWithCanonical(
		Version,
		controlProtocolVersion,
		runtimeStatus.Canonical,
		healthSnapshot,
		netstackReport.Leftovers,
		netstackRuntimeReport,
		nodeResults,
		diagnostics.PortStatuses(cfg),
		diagnostics.Privacy(cfg, lines, core.ExecCommand),
		diagnostics.ReadModuleVersion(),
		diagnostics.SingBoxCheck(singBoxPath, renderedConfigPath, lines, core.ExecCommand),
		diagnostics.ReleaseIntegrityReport(dataDir),
		diagnostics.ProfileSummaryFromConfig(cfg, runtimeStatus),
		diagnostics.RoutingSummaryFromConfig(cfg),
		diagnostics.PackageResolutionFromConfig(cfg),
	), nil
}

func (d *daemon) diagnosticNetstackReport(cfg *config.Config) netstack.Report {
	if cfg == nil {
		return diagnostics.VerifyCleanup(d.dataDir, nil, false)
	}
	return diagnostics.VerifyCleanup(d.dataDir, buildScriptEnv(cfg, d.dataDir), true)
}

func (d *daemon) diagnosticNetstackRuntimeReport(cfg *config.Config) netstack.Report {
	if cfg == nil {
		return diagnostics.VerifyRuntime(d.dataDir, nil, false, false)
	}
	if d.coreMgr == nil {
		return diagnostics.VerifyRuntime(d.dataDir, buildScriptEnv(cfg, d.dataDir), true, false)
	}
	status := d.coreMgr.Status()
	runtimeActive := status.State == core.StateRunning.String() || status.State == core.StateDegraded.String()
	return diagnostics.VerifyRuntime(d.dataDir, buildScriptEnv(cfg, d.dataDir), true, runtimeActive)
}
