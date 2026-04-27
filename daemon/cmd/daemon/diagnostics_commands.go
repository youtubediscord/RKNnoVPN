package main

import (
	"os"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
)

func (d *daemon) singBoxVersion(path string, maxLines int) diagnosticCommandResult {
	if _, err := os.Stat(path); err != nil {
		return diagnosticCommandResult{Command: path + " version", Error: err.Error()}
	}
	return runDiagnosticCommand(maxLines, path, "version")
}

func (d *daemon) singBoxCheck(path string, configPath string, maxLines int) diagnosticCommandResult {
	if _, err := os.Stat(path); err != nil {
		return diagnosticCommandResult{Command: path + " check -c " + configPath, Error: err.Error()}
	}
	if _, err := os.Stat(configPath); err != nil {
		return diagnosticCommandResult{Command: path + " check -c " + configPath, Error: err.Error()}
	}
	return runDiagnosticCommand(maxLines, path, "check", "-c", configPath)
}

func (d *daemon) runtimeDiagnostics(maxLines int) map[string]diagnosticCommandResult {
	return map[string]diagnosticCommandResult{
		"iptables_save_mangle":  runDiagnosticCommand(maxLines, "iptables-save", "-t", "mangle"),
		"ip6tables_save_mangle": runDiagnosticCommand(maxLines, "ip6tables-save", "-t", "mangle"),
		"iptables_mangle":       runDiagnosticCommand(maxLines, "iptables", "-w", "100", "-t", "mangle", "-S"),
		"iptables_nat":          runDiagnosticCommand(maxLines, "iptables", "-w", "100", "-t", "nat", "-S"),
		"ip6tables_mangle":      runDiagnosticCommand(maxLines, "ip6tables", "-w", "100", "-t", "mangle", "-S"),
		"ip6tables_nat":         runDiagnosticCommand(maxLines, "ip6tables", "-w", "100", "-t", "nat", "-S"),
		"ip_rule":               runDiagnosticCommand(maxLines, "ip", "rule", "show"),
		"ip_rule_v6":            runDiagnosticCommand(maxLines, "ip", "-6", "rule", "show"),
		"ip_route_2023":         runDiagnosticCommand(maxLines, "ip", "route", "show", "table", "2023"),
		"ip_route_2024_v6":      runDiagnosticCommand(maxLines, "ip", "-6", "route", "show", "table", "2024"),
		"listeners_ss":          runDiagnosticCommand(maxLines, "ss", "-lntu"),
		"listeners_netstat":     runDiagnosticCommand(maxLines, "netstat", "-lntu"),
	}
}

func (d *daemon) diagnosticDevice(maxLines int) map[string]diagnosticCommandResult {
	return map[string]diagnosticCommandResult{
		"model":           runDiagnosticCommand(maxLines, "getprop", "ro.product.model"),
		"manufacturer":    runDiagnosticCommand(maxLines, "getprop", "ro.product.manufacturer"),
		"android_release": runDiagnosticCommand(maxLines, "getprop", "ro.build.version.release"),
		"android_sdk":     runDiagnosticCommand(maxLines, "getprop", "ro.build.version.sdk"),
		"abi":             runDiagnosticCommand(maxLines, "getprop", "ro.product.cpu.abi"),
		"selinux":         runDiagnosticCommand(maxLines, "getenforce"),
		"uid":             runDiagnosticCommand(maxLines, "id"),
		"magisk":          runDiagnosticCommand(maxLines, "magisk", "-V"),
		"ksu":             runDiagnosticCommand(maxLines, "ksud", "-V"),
		"apatch":          runDiagnosticCommand(maxLines, "apd", "--version"),
	}
}

func (d *daemon) privacyDiagnostics(cfg *config.Config, maxLines int) map[string]interface{} {
	return diagnostics.Privacy(cfg, maxLines, core.ExecCommand)
}

func selfTestProtectedAppsDirect() bool {
	return diagnostics.SelfTestProtectedAppsDirect()
}

func diagnosticLocalhostProxyPortsClear(cfg *config.Config) bool {
	return diagnostics.LocalhostProxyPortsClear(cfg)
}

func runDiagnosticCommand(maxLines int, name string, args ...string) diagnosticCommandResult {
	return diagnostics.RunCommand(maxLines, core.ExecCommand, name, args...)
}

func diagnosticPortStatuses(cfg *config.Config) []diagnosticPortStatus {
	return diagnostics.PortStatuses(cfg)
}

func diagnosticLocalPortConflicts(cfg *config.Config) []diagnosticPortConflict {
	return diagnostics.LocalPortConflicts(cfg)
}

func diagnosticLocalPortRoles(cfg *config.Config) map[int][]string {
	return diagnostics.LocalPortRoles(cfg)
}

func (d *daemon) diagnosticLogs(maxLines int) []diagnosticLogSection {
	return diagnostics.ReadLogSections(
		diagnostics.DefaultLogFileSpecs(d.dataDir),
		maxLines,
		512*1024,
		redactDiagnosticText,
	)
}

func fileStatus(path string, executable bool) diagnosticFileStatus {
	return diagnostics.StatFile(path, executable)
}

func readModuleVersion() map[string]string {
	return diagnostics.ReadModuleVersion()
}
