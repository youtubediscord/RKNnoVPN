package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

const controlProtocolVersion = 3

type doctorCommandResult struct {
	Command string   `json:"command"`
	Lines   []string `json:"lines,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type doctorFileStatus struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Executable bool   `json:"executable,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Error      string `json:"error,omitempty"`
}

type doctorLogSection struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Lines   []string `json:"lines,omitempty"`
	Missing bool     `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type doctorJSONSection struct {
	Path    string      `json:"path"`
	Value   interface{} `json:"value,omitempty"`
	Missing bool        `json:"missing,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type doctorPortStatus struct {
	Port         int  `json:"port"`
	TCPListening bool `json:"tcpListening"`
}

type doctorSummary struct {
	Status              string                `json:"status"`
	HealthCode          string                `json:"healthCode,omitempty"`
	HealthDetail        string                `json:"healthDetail,omitempty"`
	OperationalHealthy  bool                  `json:"operationalHealthy"`
	RebootRequired      bool                  `json:"rebootRequired"`
	IssueCount          int                   `json:"issueCount"`
	Issues              []string              `json:"issues,omitempty"`
	CompatibilityIssues []string              `json:"compatibilityIssues,omitempty"`
	PrivacyIssues       []string              `json:"privacyIssues,omitempty"`
	NodeTests           doctorNodeTestSummary `json:"nodeTests"`
}

type doctorNodeTestSummary struct {
	Total    int `json:"total"`
	Usable   int `json:"usable"`
	Unusable int `json:"unusable"`
	TCPOnly  int `json:"tcpOnly"`
}

func (d *daemon) handleDoctor(params *json.RawMessage) (interface{}, *ipc.RPCError) {
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
	panelPath := d.panelPath
	dataDir := d.dataDir
	d.mu.Unlock()

	renderedConfigPath := filepath.Join(dataDir, "config", "rendered", "singbox.json")
	singBoxPath := filepath.Join(dataDir, "bin", "sing-box")

	healthResult := d.healthMon.RunOnce()
	healthSnapshot := d.buildRuntimeV2HealthSnapshot(healthResult, true)
	var backendStatus interface{}
	if d.runtimeV2 != nil {
		backendStatus = d.runtimeV2.Status()
	}
	moduleVersion := readModuleVersion()
	ports := doctorPortStatuses(cfg)
	leftovers := d.collectNetworkLeftovers(cfg)
	nodeResults := d.testNodeProbesV2(cfg.Health.URL, 2500, nil)
	privacy := d.privacyDiagnostics(cfg, lines)
	singBoxCheck := d.singBoxCheck(singBoxPath, renderedConfigPath, lines)
	versions := map[string]interface{}{
		"daemon":                   Version,
		"core":                     Version,
		"privctl_expected":         Version,
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
		"summary":      buildDoctorSummary(healthSnapshot, leftovers, nodeResults, ports, privacy, moduleVersion, singBoxCheck),
		"versions":     versions,
		"device":       d.doctorDevice(lines),
		"paths": map[string]doctorFileStatus{
			"data_dir":          fileStatus(dataDir, false),
			"current_release":   fileStatus(filepath.Join(dataDir, "current"), false),
			"releases_dir":      fileStatus(filepath.Join(dataDir, "releases"), false),
			"config":            fileStatus(cfgPath, false),
			"panel":             fileStatus(panelPath, false),
			"rendered_singbox":  fileStatus(renderedConfigPath, false),
			"sing_box_binary":   fileStatus(singBoxPath, true),
			"privd_log":         fileStatus(filepath.Join(dataDir, "logs", "privd.log"), false),
			"sing_box_log":      fileStatus(filepath.Join(dataDir, "logs", "sing-box.log"), false),
			"daemon_socket":     fileStatus(filepath.Join(dataDir, "run", "daemon.sock"), false),
			"sing_box_pid_file": fileStatus(filepath.Join(dataDir, "run", "singbox.pid"), false),
		},
		"health": map[string]interface{}{
			"snapshot": healthSnapshot,
			"raw":      healthResult,
		},
		"backend_status": backendStatus,
		"ports":          ports,
		"leftovers":      leftovers,
		"node_tests":     redactNodeProbeResults(nodeResults),
		"logs":           d.doctorLogs(lines),
		"config": map[string]doctorJSONSection{
			"daemon":           readRedactedJSONFile(cfgPath),
			"panel":            readRedactedJSONFile(panelPath),
			"rendered_singbox": readRedactedJSONFile(renderedConfigPath),
		},
		"runtime": d.runtimeDiagnostics(lines),
		"privacy": privacy,
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

func (d *daemon) buildSelfCheckSummary(lines int) (doctorSummary, error) {
	d.mu.Lock()
	cfg := d.cfg
	dataDir := d.dataDir
	d.mu.Unlock()
	if cfg == nil {
		return doctorSummary{Status: "failed", Issues: []string{"config unavailable"}, IssueCount: 1}, nil
	}
	if lines <= 0 {
		lines = 80
	}
	renderedConfigPath := filepath.Join(dataDir, "config", "rendered", "singbox.json")
	singBoxPath := filepath.Join(dataDir, "bin", "sing-box")
	healthResult := d.healthMon.RunOnce()
	healthSnapshot := d.buildRuntimeV2HealthSnapshot(healthResult, true)
	return buildDoctorSummary(
		healthSnapshot,
		d.collectNetworkLeftovers(cfg),
		d.testNodeProbesV2(cfg.Health.URL, 2500, nil),
		doctorPortStatuses(cfg),
		d.privacyDiagnostics(cfg, lines),
		readModuleVersion(),
		d.singBoxCheck(singBoxPath, renderedConfigPath, lines),
	), nil
}

func buildDoctorSummary(
	healthSnapshot runtimev2.HealthSnapshot,
	leftovers []string,
	nodeResults []runtimev2.NodeProbeResult,
	ports []doctorPortStatus,
	privacy map[string]interface{},
	moduleVersion map[string]string,
	singBoxCheck doctorCommandResult,
) doctorSummary {
	summary := doctorSummary{
		Status:             "ok",
		HealthCode:         healthSnapshot.LastCode,
		HealthDetail:       healthSnapshot.LastError,
		OperationalHealthy: healthSnapshot.OperationalHealthy(),
		NodeTests:          summarizeDoctorNodeTests(nodeResults),
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
	if singBoxCheck.Error != "" {
		addCompatibility("sing-box config check failed: " + singBoxCheck.Error)
	}
	if moduleVersion["version"] == "" || moduleVersion["version"] == "unknown" {
		addCompatibility("module version is unknown")
	}
	for _, port := range ports {
		switch port.Port {
		case 10808, 10809, 9090:
			if port.TCPListening {
				addPrivacy("production localhost helper/API port is listening")
			}
		}
	}
	for _, issue := range doctorPrivacyIssues(privacy) {
		addPrivacy(issue)
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

func summarizeDoctorNodeTests(results []runtimev2.NodeProbeResult) doctorNodeTestSummary {
	summary := doctorNodeTestSummary{Total: len(results)}
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

func doctorPrivacyIssues(privacy map[string]interface{}) []string {
	rawChecks, _ := privacy["checks"].(map[string]interface{})
	if len(rawChecks) == 0 {
		return nil
	}
	labels := map[string]string{
		"no_vpn_like_interfaces":      "VPN-like network interface is visible",
		"no_transport_vpn_hint":       "Connectivity diagnostics expose VPN transport",
		"system_proxy_unset":          "system proxy setting is not empty",
		"clash_api_disabled":          "Clash/API port is enabled",
		"helper_inbounds_disabled":    "HTTP/SOCKS helper inbound is enabled",
		"helper_inbounds_local_only":  "helper inbound allows LAN access",
		"per_app_whitelist_default":   "app routing is not whitelist/off",
		"dns_hijack_per_uid":          "DNS hijack is not scoped per UID",
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

func supportedCapabilities() []string {
	return []string{
		"backend.root-tproxy",
		"backend.reset.structured",
		"config.import.v2",
		"config.schema.v4",
		"diagnostics.bundle.v2",
		"diagnostics.health.v2",
		"diagnostics.testNodes.v2",
		"node-test.tcp-direct",
		"node-test.url",
		"panel.nodes",
		"privacy.audit.v2",
		"privacy.self-check.v1",
		"runtime.logs",
	}
}

func supportedRPCMethods() []string {
	methods := []string{
		"app.list",
		"app.resolveUid",
		"audit",
		"backend.applyDesiredState",
		"backend.reset",
		"backend.restart",
		"backend.start",
		"backend.status",
		"backend.stop",
		"config-get",
		"config-import",
		"config-list",
		"config-set",
		"config-set-many",
		"config.import",
		"diagnostics.health",
		"diagnostics.testNodes",
		"doctor",
		"health",
		"logs",
		"network-reset",
		"network.reset",
		"node-test",
		"node.test",
		"panel-get",
		"panel-set",
		"self-check",
		"self.check",
		"reload",
		"start",
		"status",
		"stop",
		"subscription-fetch",
		"update-check",
		"update-download",
		"update-install",
		"version",
	}
	sort.Strings(methods)
	return methods
}

func (d *daemon) singBoxVersion(path string, maxLines int) doctorCommandResult {
	if _, err := os.Stat(path); err != nil {
		return doctorCommandResult{Command: path + " version", Error: err.Error()}
	}
	return runDoctorCommand(maxLines, path, "version")
}

func (d *daemon) singBoxCheck(path string, configPath string, maxLines int) doctorCommandResult {
	if _, err := os.Stat(path); err != nil {
		return doctorCommandResult{Command: path + " check -c " + configPath, Error: err.Error()}
	}
	if _, err := os.Stat(configPath); err != nil {
		return doctorCommandResult{Command: path + " check -c " + configPath, Error: err.Error()}
	}
	return runDoctorCommand(maxLines, path, "check", "-c", configPath)
}

func (d *daemon) runtimeDiagnostics(maxLines int) map[string]doctorCommandResult {
	return map[string]doctorCommandResult{
		"iptables_save_mangle":  runDoctorCommand(maxLines, "iptables-save", "-t", "mangle"),
		"ip6tables_save_mangle": runDoctorCommand(maxLines, "ip6tables-save", "-t", "mangle"),
		"iptables_mangle":       runDoctorCommand(maxLines, "iptables", "-w", "100", "-t", "mangle", "-S"),
		"iptables_nat":          runDoctorCommand(maxLines, "iptables", "-w", "100", "-t", "nat", "-S"),
		"ip6tables_mangle":      runDoctorCommand(maxLines, "ip6tables", "-w", "100", "-t", "mangle", "-S"),
		"ip6tables_nat":         runDoctorCommand(maxLines, "ip6tables", "-w", "100", "-t", "nat", "-S"),
		"ip_rule":               runDoctorCommand(maxLines, "ip", "rule", "show"),
		"ip_rule_v6":            runDoctorCommand(maxLines, "ip", "-6", "rule", "show"),
		"ip_route_2023":         runDoctorCommand(maxLines, "ip", "route", "show", "table", "2023"),
		"ip_route_2024_v6":      runDoctorCommand(maxLines, "ip", "-6", "route", "show", "table", "2024"),
		"listeners_ss":          runDoctorCommand(maxLines, "ss", "-lntu"),
		"listeners_netstat":     runDoctorCommand(maxLines, "netstat", "-lntu"),
	}
}

func (d *daemon) doctorDevice(maxLines int) map[string]doctorCommandResult {
	return map[string]doctorCommandResult{
		"model":           runDoctorCommand(maxLines, "getprop", "ro.product.model"),
		"manufacturer":    runDoctorCommand(maxLines, "getprop", "ro.product.manufacturer"),
		"android_release": runDoctorCommand(maxLines, "getprop", "ro.build.version.release"),
		"android_sdk":     runDoctorCommand(maxLines, "getprop", "ro.build.version.sdk"),
		"abi":             runDoctorCommand(maxLines, "getprop", "ro.product.cpu.abi"),
		"selinux":         runDoctorCommand(maxLines, "getenforce"),
		"uid":             runDoctorCommand(maxLines, "id"),
		"magisk":          runDoctorCommand(maxLines, "magisk", "-V"),
		"ksu":             runDoctorCommand(maxLines, "ksud", "-V"),
		"apatch":          runDoctorCommand(maxLines, "apd", "--version"),
	}
}

func (d *daemon) privacyDiagnostics(cfg *config.Config, maxLines int) map[string]interface{} {
	ipLinks := runDoctorCommand(maxLines, "ip", "link", "show")
	connectivity := runDoctorCommand(maxLines, "dumpsys", "connectivity")
	settingsProxyHost := runDoctorCommand(maxLines, "settings", "get", "global", "http_proxy")
	settingsProxyPort := runDoctorCommand(maxLines, "settings", "get", "global", "global_http_proxy_port")
	checks := map[string]interface{}{
		"no_vpn_like_interfaces": !doctorLinesContainAny(ipLinks.Lines, "tun0", "wg0", "tap0", "ppp0", "ipsec"),
		"no_transport_vpn_hint":  !doctorLinesContainAny(connectivity.Lines, "TRANSPORT_VPN", "VpnTransportInfo"),
		"system_proxy_unset":     doctorCommandLooksEmptySetting(settingsProxyHost) && doctorCommandLooksEmptySetting(settingsProxyPort),
	}
	if cfg != nil {
		panelInbounds := cfg.ResolvePanelInbounds()
		checks["clash_api_disabled"] = cfg.Proxy.APIPort == 0
		checks["helper_inbounds_disabled"] = panelInbounds.HTTPPort == 0 && panelInbounds.SocksPort == 0
		checks["helper_inbounds_local_only"] = !panelInbounds.AllowLAN
		checks["per_app_whitelist_default"] = cfg.Apps.Mode == "whitelist" || cfg.Apps.Mode == "off"
		checks["dns_hijack_per_uid"] = cfg.DNS.HijackPerUID
	}
	return map[string]interface{}{
		"checks": checks,
		"commands": map[string]doctorCommandResult{
			"ip_link":                    ipLinks,
			"dumpsys_connectivity":       connectivity,
			"settings_global_http_proxy": settingsProxyHost,
			"settings_global_proxy_port": settingsProxyPort,
		},
	}
}

func runDoctorCommand(maxLines int, name string, args ...string) doctorCommandResult {
	command := strings.Join(append([]string{name}, args...), " ")
	out, err := core.ExecCommand(name, args...)
	result := doctorCommandResult{
		Command: command,
		Lines:   limitLines(splitLines(redactDiagnosticText(out)), maxLines),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func doctorPortStatuses(cfg *config.Config) []doctorPortStatus {
	ports := effectiveLocalPorts(cfg)
	result := make([]doctorPortStatus, 0, len(ports))
	for _, port := range ports {
		result = append(result, doctorPortStatus{
			Port:         port,
			TCPListening: isTCPPortListening("127.0.0.1", port, 150*time.Millisecond),
		})
	}
	return result
}

func (d *daemon) doctorLogs(maxLines int) []doctorLogSection {
	specs := []logFileSpec{
		{Name: "privd", Path: filepath.Join(d.dataDir, "logs", "privd.log")},
		{Name: "sing-box", Path: filepath.Join(d.dataDir, "logs", "sing-box.log")},
	}
	sections := make([]doctorLogSection, 0, len(specs))
	for _, spec := range specs {
		section := doctorLogSection{Name: spec.Name, Path: spec.Path}
		lines, err := readLogTail(spec.Path, maxLines, 512*1024)
		switch {
		case err == nil:
			for _, line := range lines {
				section.Lines = append(section.Lines, redactDiagnosticText(line))
			}
		case os.IsNotExist(err):
			section.Missing = true
		default:
			section.Error = err.Error()
		}
		sections = append(sections, section)
	}
	return sections
}

func fileStatus(path string, executable bool) doctorFileStatus {
	status := doctorFileStatus{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			status.Error = err.Error()
		}
		return status
	}
	status.Exists = true
	status.Mode = info.Mode().Perm().String()
	if executable {
		status.Executable = info.Mode().Perm()&0111 != 0
	}
	return status
}

func readModuleVersion() map[string]string {
	paths := []string{
		"/data/adb/modules/privstack/module.prop",
		"/data/adb/modules_update/privstack/module.prop",
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		result := map[string]string{"path": path}
		for _, line := range splitLines(string(data)) {
			key, value, ok := strings.Cut(line, "=")
			if ok && (key == "version" || key == "versionCode") {
				result[key] = value
			}
		}
		return result
	}
	return map[string]string{"version": "unknown"}
}

func redactNodeProbeResults(results interface{}) interface{} {
	data, err := json.Marshal(results)
	if err != nil {
		return results
	}
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		return results
	}
	return redactDiagnosticValue("", value)
}

func readRedactedJSONFile(path string) doctorJSONSection {
	section := doctorJSONSection{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			section.Missing = true
		} else {
			section.Error = err.Error()
		}
		return section
	}
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		section.Error = err.Error()
		section.Value = limitLines(splitLines(redactDiagnosticText(string(data))), 80)
		return section
	}
	section.Value = redactDiagnosticValue("", value)
	return section
}

func redactDiagnosticValue(key string, value interface{}) interface{} {
	if isSensitiveDiagnosticKey(key) {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		redacted := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			redacted[k] = redactDiagnosticValue(k, v)
		}
		return redacted
	case []interface{}:
		redacted := make([]interface{}, len(typed))
		for i, v := range typed {
			redacted[i] = redactDiagnosticValue("", v)
		}
		return redacted
	case string:
		return redactDiagnosticText(typed)
	default:
		return value
	}
}

func isSensitiveDiagnosticKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	switch lower {
	case "uuid", "password", "private_key", "short_id", "public_key", "reality_public_key":
		return true
	}
	for _, token := range []string{"password", "private", "secret", "token", "uuid", "short_id", "public_key"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

var (
	diagnosticUUIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	diagnosticKeyPattern  = regexp.MustCompile(`(?i)("?(?:uuid|password|private_key|short_id|public_key|reality_public_key)"?\s*[:=]\s*)("[^"]*"|[^,\s}]+)`)
)

func redactDiagnosticText(text string) string {
	text = diagnosticKeyPattern.ReplaceAllString(text, `${1}"[redacted]"`)
	text = diagnosticUUIDPattern.ReplaceAllString(text, "[redacted-uuid]")
	return text
}

func doctorLinesContainAny(lines []string, needles ...string) bool {
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, needle := range needles {
			if strings.Contains(lower, strings.ToLower(needle)) {
				return true
			}
		}
	}
	return false
}

func doctorCommandLooksEmptySetting(result doctorCommandResult) bool {
	if result.Error != "" {
		return true
	}
	for _, line := range result.Lines {
		value := strings.TrimSpace(line)
		if value != "" && value != "null" && value != ":0" {
			return false
		}
	}
	return true
}

func limitLines(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return lines[len(lines)-max:]
}
