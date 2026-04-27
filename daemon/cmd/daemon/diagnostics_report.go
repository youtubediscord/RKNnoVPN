package main

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) handleDiagnosticsReport(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	lines := 80
	if params != nil {
		var p struct {
			Lines int `json:"lines"`
		}
		if err := json.Unmarshal(*params, &p); err == nil && p.Lines > 0 {
			lines = p.Lines
		}
	}
	if lines > 300 {
		lines = 300
	}

	d.mu.Lock()
	cfg := d.cfg
	cfgPath := d.cfgPath
	profilePath := d.profilePath
	dataDir := d.dataDir
	d.mu.Unlock()
	if profilePath == "" && cfgPath != "" {
		profilePath = profiledoc.Path(cfgPath)
	}

	renderedConfigPath := filepath.Join(dataDir, "config", "rendered", "singbox.json")
	singBoxPath := filepath.Join(dataDir, "bin", "sing-box")

	healthResult := d.healthMon.RunOnce()
	healthSnapshot := d.buildRuntimeV2HealthSnapshot(healthResult, true)
	var backendStatus interface{}
	var runtimeStatus runtimev2.Status
	if d.runtimeV2 != nil {
		runtimeStatus = d.runtimeV2.Status()
		backendStatus = runtimeStatus
	}
	moduleVersion := readModuleVersion()
	ports := diagnosticPortStatuses(cfg)
	portConflicts := diagnosticLocalPortConflicts(cfg)
	netstackReport := d.diagnosticNetstackReport(cfg)
	netstackRuntimeReport := d.diagnosticNetstackRuntimeReport(cfg)
	leftovers := netstackReport.Leftovers
	var nodeResults []runtimev2.NodeProbeResult
	if cfg != nil {
		nodeResults = d.testNodeProbesV2(cfg.Health.URL, 2500, nil)
	}
	privacy := d.privacyDiagnostics(cfg, lines)
	singBoxCheck := d.singBoxCheck(singBoxPath, renderedConfigPath, lines)
	releaseIntegrity := diagnosticReleaseIntegrityReport(dataDir)
	routingSummary := diagnosticRoutingSummaryFromConfig(cfg)
	profileSummary := diagnosticProfileSummaryFromConfig(cfg, runtimeStatus)
	packageResolution := diagnosticPackageResolutionFromConfig(cfg)
	versions := map[string]interface{}{
		"daemon":                   Version,
		"core":                     Version,
		"daemonctl_expected":       Version,
		"control_protocol_version": controlProtocolVersion,
		"schema_version":           config.CurrentSchemaVersion,
		"panel_min_version":        Version,
		"capabilities":             supportedCapabilities(),
		"supported_methods":        supportedRPCMethods(),
		"sing_box":                 d.singBoxVersion(singBoxPath, lines),
		"module":                   moduleVersion,
	}

	report := map[string]interface{}{
		"generated_at": time.Now().Format(time.RFC3339),
		"summary":      buildDiagnosticSummary(healthSnapshot, leftovers, netstackRuntimeReport, nodeResults, ports, privacy, moduleVersion, singBoxCheck, releaseIntegrity, profileSummary, routingSummary, packageResolution),
		"versions":     versions,
		"device":       d.diagnosticDevice(lines),
		"paths": map[string]diagnosticFileStatus{
			"data_dir":          fileStatus(dataDir, false),
			"current_release":   fileStatus(filepath.Join(dataDir, "current"), false),
			"releases_dir":      fileStatus(filepath.Join(dataDir, "releases"), false),
			"config":            fileStatus(cfgPath, false),
			"profile":           fileStatus(profilePath, false),
			"rendered_singbox":  fileStatus(renderedConfigPath, false),
			"sing_box_binary":   fileStatus(singBoxPath, true),
			"daemon_log":        fileStatus(filepath.Join(dataDir, "logs", "daemon.log"), false),
			"sing_box_log":      fileStatus(filepath.Join(dataDir, "logs", "sing-box.log"), false),
			"daemon_socket":     fileStatus(filepath.Join(dataDir, "run", "daemon.sock"), false),
			"sing_box_pid_file": fileStatus(filepath.Join(dataDir, "run", "singbox.pid"), false),
		},
		"health": map[string]interface{}{
			"snapshot": healthSnapshot,
			"raw":      healthResult,
		},
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
		"node_tests":          redactNodeProbeResults(nodeResults),
		"logs":                d.diagnosticLogs(lines),
		"config": map[string]diagnosticJSONSection{
			"daemon":           readRedactedJSONFile(cfgPath),
			"profile":          readRedactedJSONFile(profilePath),
			"rendered_singbox": readRedactedJSONFile(renderedConfigPath),
		},
		"runtime":           d.runtimeDiagnostics(lines),
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

func (d *daemon) buildSelfCheckSummary(lines int) (diagnosticSummary, error) {
	d.mu.Lock()
	cfg := d.cfg
	dataDir := d.dataDir
	d.mu.Unlock()
	if cfg == nil {
		return diagnosticSummary{Status: "failed", Issues: []string{"config unavailable"}, IssueCount: 1}, nil
	}
	if lines <= 0 {
		lines = 80
	}
	renderedConfigPath := filepath.Join(dataDir, "config", "rendered", "singbox.json")
	singBoxPath := filepath.Join(dataDir, "bin", "sing-box")
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
	return buildDiagnosticSummary(
		healthSnapshot,
		netstackReport.Leftovers,
		netstackRuntimeReport,
		nodeResults,
		diagnosticPortStatuses(cfg),
		d.privacyDiagnostics(cfg, lines),
		readModuleVersion(),
		d.singBoxCheck(singBoxPath, renderedConfigPath, lines),
		diagnosticReleaseIntegrityReport(dataDir),
		diagnosticProfileSummaryFromConfig(cfg, runtimeStatus),
		diagnosticRoutingSummaryFromConfig(cfg),
		diagnosticPackageResolutionFromConfig(cfg),
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
