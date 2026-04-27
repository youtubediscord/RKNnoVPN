package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
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
	ipLinks := runDiagnosticCommand(maxLines, "ip", "link", "show")
	connectivity := runDiagnosticCommand(maxLines, "dumpsys", "connectivity")
	settingsProxyHost := runDiagnosticCommand(maxLines, "settings", "get", "global", "http_proxy")
	settingsProxyPort := runDiagnosticCommand(maxLines, "settings", "get", "global", "global_http_proxy_port")
	checks := map[string]interface{}{
		"no_vpn_like_interfaces":      firstVPNLikeInterfaceLine(ipLinks.Lines) == "",
		"no_transport_vpn_hint":       !diagnosticLinesContainAny(connectivity.Lines, "TRANSPORT_VPN", "VpnTransportInfo"),
		"no_loopback_dns":             !diagnosticLinesContainLoopbackDNS(connectivity.Lines),
		"system_proxy_unset":          diagnosticCommandLooksEmptySetting(settingsProxyHost) && diagnosticCommandLooksEmptySetting(settingsProxyPort),
		"localhost_proxy_ports_clear": diagnosticLocalhostProxyPortsClear(cfg),
	}
	if cfg != nil {
		profileInbounds := cfg.ResolveProfileInbounds()
		checks["clash_api_disabled"] = cfg.Proxy.APIPort == 0
		checks["helper_inbounds_disabled"] = profileInbounds.HTTPPort == 0 && profileInbounds.SocksPort == 0
		checks["helper_inbounds_local_only"] = !profileInbounds.AllowLAN
		checks["per_app_whitelist_default"] = cfg.Apps.Mode == "whitelist" || cfg.Apps.Mode == "off"
		checks["dns_hijack_per_uid"] = cfg.DNS.HijackPerUID
		checks["self_test_apps_direct"] = selfTestProtectedAppsDirect()
	}
	return map[string]interface{}{
		"checks": checks,
		"protected_packages": map[string]interface{}{
			"self_test": core.SelfTestProtectedPackages,
		},
		"commands": map[string]diagnosticCommandResult{
			"ip_link":                    ipLinks,
			"dumpsys_connectivity":       connectivity,
			"settings_global_http_proxy": settingsProxyHost,
			"settings_global_proxy_port": settingsProxyPort,
		},
	}
}

func selfTestProtectedAppsDirect() bool {
	for _, pkgName := range core.SelfTestProtectedPackages {
		if !core.IsBuiltInAlwaysDirectPackage(pkgName) {
			return false
		}
	}
	return true
}

func diagnosticLocalhostProxyPortsClear(cfg *config.Config) bool {
	ports := []int{10808, 10809, 9090}
	if cfg != nil {
		profileInbounds := cfg.ResolveProfileInbounds()
		ports = append(ports, cfg.Proxy.APIPort, profileInbounds.SocksPort, profileInbounds.HTTPPort)
	}
	seen := map[int]bool{}
	for _, port := range ports {
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		if isTCPPortListening("127.0.0.1", port, 150*time.Millisecond) {
			return false
		}
	}
	return true
}

func runDiagnosticCommand(maxLines int, name string, args ...string) diagnosticCommandResult {
	command := strings.Join(append([]string{name}, args...), " ")
	out, err := core.ExecCommand(name, args...)
	result := diagnosticCommandResult{
		Command: command,
		Lines:   limitLines(splitLines(redactDiagnosticText(out)), maxLines),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func diagnosticPortStatuses(cfg *config.Config) []diagnosticPortStatus {
	ports := effectiveLocalPorts(cfg)
	roles := diagnosticLocalPortRoles(cfg)
	result := make([]diagnosticPortStatus, 0, len(ports))
	for _, port := range ports {
		role := strings.Join(roles[port], ",")
		result = append(result, diagnosticPortStatus{
			Role:         role,
			Port:         port,
			TCPListening: isTCPPortListening("127.0.0.1", port, 150*time.Millisecond),
			Conflict:     len(roles[port]) > 1,
		})
	}
	return result
}

func diagnosticLocalPortConflicts(cfg *config.Config) []diagnosticPortConflict {
	roles := diagnosticLocalPortRoles(cfg)
	conflicts := make([]diagnosticPortConflict, 0)
	ports := make([]int, 0, len(roles))
	for port := range roles {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	for _, port := range ports {
		if len(roles[port]) <= 1 {
			continue
		}
		conflicts = append(conflicts, diagnosticPortConflict{
			Port:  port,
			Roles: append([]string(nil), roles[port]...),
		})
	}
	return conflicts
}

func diagnosticLocalPortRoles(cfg *config.Config) map[int][]string {
	if cfg == nil {
		return nil
	}
	profileInbounds := cfg.ResolveProfileInbounds()
	candidates := []struct {
		role string
		port int
	}{
		{"tproxy", valueOrDefaultInt(cfg.Proxy.TProxyPort, 10853)},
		{"dns", valueOrDefaultInt(cfg.Proxy.DNSPort, 10856)},
		{"clash_api", cfg.Proxy.APIPort},
		{"socks_helper", profileInbounds.SocksPort},
		{"http_helper", profileInbounds.HTTPPort},
	}
	roles := map[int][]string{}
	for _, candidate := range candidates {
		if candidate.port <= 0 {
			continue
		}
		roles[candidate.port] = append(roles[candidate.port], candidate.role)
	}
	for port := range roles {
		sort.Strings(roles[port])
	}
	return roles
}

func (d *daemon) diagnosticLogs(maxLines int) []diagnosticLogSection {
	specs := []logFileSpec{
		{Name: "daemon", Path: filepath.Join(d.dataDir, "logs", "daemon.log")},
		{Name: "sing-box", Path: filepath.Join(d.dataDir, "logs", "sing-box.log")},
	}
	sections := make([]diagnosticLogSection, 0, len(specs))
	for _, spec := range specs {
		section := diagnosticLogSection{Name: spec.Name, Path: spec.Path}
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

func fileStatus(path string, executable bool) diagnosticFileStatus {
	status := diagnosticFileStatus{Path: path}
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
		"/data/adb/modules/rknnovpn/module.prop",
		"/data/adb/modules_update/rknnovpn/module.prop",
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
