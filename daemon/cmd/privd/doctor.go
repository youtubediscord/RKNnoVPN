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

	report := map[string]interface{}{
		"generated_at": time.Now().Format(time.RFC3339),
		"versions": map[string]interface{}{
			"daemon":                   Version,
			"core":                     Version,
			"privctl_expected":         Version,
			"control_protocol_version": controlProtocolVersion,
			"supported_methods":        supportedRPCMethods(),
			"sing_box":                 d.singBoxVersion(singBoxPath, lines),
			"module":                   readModuleVersion(),
		},
		"paths": map[string]doctorFileStatus{
			"data_dir":          fileStatus(dataDir, false),
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
		"ports":          doctorPortStatuses(cfg),
		"leftovers":      d.collectNetworkLeftovers(cfg),
		"node_tests":     redactNodeProbeResults(d.testNodeProbesV2(cfg.Health.URL, 2500, nil)),
		"logs":           d.doctorLogs(lines),
		"config": map[string]doctorJSONSection{
			"daemon":           readRedactedJSONFile(cfgPath),
			"panel":            readRedactedJSONFile(panelPath),
			"rendered_singbox": readRedactedJSONFile(renderedConfigPath),
		},
		"runtime": d.runtimeDiagnostics(lines),
	}

	report["sing_box_check"] = d.singBoxCheck(singBoxPath, renderedConfigPath, lines)
	return report, nil
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
		"iptables_mangle":   runDoctorCommand(maxLines, "iptables", "-w", "100", "-t", "mangle", "-S"),
		"iptables_nat":      runDoctorCommand(maxLines, "iptables", "-w", "100", "-t", "nat", "-S"),
		"ip6tables_mangle":  runDoctorCommand(maxLines, "ip6tables", "-w", "100", "-t", "mangle", "-S"),
		"ip6tables_nat":     runDoctorCommand(maxLines, "ip6tables", "-w", "100", "-t", "nat", "-S"),
		"ip_rule":           runDoctorCommand(maxLines, "ip", "rule", "show"),
		"ip_rule_v6":        runDoctorCommand(maxLines, "ip", "-6", "rule", "show"),
		"ip_route_2023":     runDoctorCommand(maxLines, "ip", "route", "show", "table", "2023"),
		"ip_route_2024_v6":  runDoctorCommand(maxLines, "ip", "-6", "route", "show", "table", "2024"),
		"listeners_ss":      runDoctorCommand(maxLines, "ss", "-lntu"),
		"listeners_netstat": runDoctorCommand(maxLines, "netstat", "-lntu"),
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
	if items, ok := value.([]interface{}); ok {
		for _, item := range items {
			probe, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			server, _ := probe["server"].(string)
			if server == "" {
				continue
			}
			if name, _ := probe["name"].(string); name == server {
				probe["name"] = "[redacted]"
			}
			probe["server"] = "[redacted]"
		}
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
	case "uuid", "password", "private_key", "short_id", "server", "address", "sni",
		"server_name", "tls_server", "public_key", "reality_public_key":
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
	diagnosticKeyPattern  = regexp.MustCompile(`(?i)("?(?:uuid|password|private_key|short_id|server|address|sni|server_name|tls_server|public_key|reality_public_key)"?\s*[:=]\s*)("[^"]*"|[^,\s}]+)`)
)

func redactDiagnosticText(text string) string {
	text = diagnosticKeyPattern.ReplaceAllString(text, `${1}"[redacted]"`)
	text = diagnosticUUIDPattern.ReplaceAllString(text, "[redacted-uuid]")
	return text
}

func limitLines(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return lines[len(lines)-max:]
}
