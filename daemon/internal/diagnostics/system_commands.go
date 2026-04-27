package diagnostics

import "os"

func SingBoxVersion(path string, maxLines int, exec ExecCommandFunc) CommandResult {
	if _, err := os.Stat(path); err != nil {
		return CommandResult{Command: path + " version", Error: err.Error()}
	}
	return RunCommand(maxLines, exec, path, "version")
}

func SingBoxCheck(path string, configPath string, maxLines int, exec ExecCommandFunc) CommandResult {
	if _, err := os.Stat(path); err != nil {
		return CommandResult{Command: path + " check -c " + configPath, Error: err.Error()}
	}
	if _, err := os.Stat(configPath); err != nil {
		return CommandResult{Command: path + " check -c " + configPath, Error: err.Error()}
	}
	return RunCommand(maxLines, exec, path, "check", "-c", configPath)
}

func RuntimeCommands(maxLines int, exec ExecCommandFunc) map[string]CommandResult {
	return map[string]CommandResult{
		"iptables_save_mangle":  RunCommand(maxLines, exec, "iptables-save", "-t", "mangle"),
		"ip6tables_save_mangle": RunCommand(maxLines, exec, "ip6tables-save", "-t", "mangle"),
		"iptables_mangle":       RunCommand(maxLines, exec, "iptables", "-w", "100", "-t", "mangle", "-S"),
		"iptables_nat":          RunCommand(maxLines, exec, "iptables", "-w", "100", "-t", "nat", "-S"),
		"ip6tables_mangle":      RunCommand(maxLines, exec, "ip6tables", "-w", "100", "-t", "mangle", "-S"),
		"ip6tables_nat":         RunCommand(maxLines, exec, "ip6tables", "-w", "100", "-t", "nat", "-S"),
		"ip_rule":               RunCommand(maxLines, exec, "ip", "rule", "show"),
		"ip_rule_v6":            RunCommand(maxLines, exec, "ip", "-6", "rule", "show"),
		"ip_route_2023":         RunCommand(maxLines, exec, "ip", "route", "show", "table", "2023"),
		"ip_route_2024_v6":      RunCommand(maxLines, exec, "ip", "-6", "route", "show", "table", "2024"),
		"listeners_ss":          RunCommand(maxLines, exec, "ss", "-lntu"),
		"listeners_netstat":     RunCommand(maxLines, exec, "netstat", "-lntu"),
	}
}

func DeviceCommands(maxLines int, exec ExecCommandFunc) map[string]CommandResult {
	return map[string]CommandResult{
		"model":           RunCommand(maxLines, exec, "getprop", "ro.product.model"),
		"manufacturer":    RunCommand(maxLines, exec, "getprop", "ro.product.manufacturer"),
		"android_release": RunCommand(maxLines, exec, "getprop", "ro.build.version.release"),
		"android_sdk":     RunCommand(maxLines, exec, "getprop", "ro.build.version.sdk"),
		"abi":             RunCommand(maxLines, exec, "getprop", "ro.product.cpu.abi"),
		"selinux":         RunCommand(maxLines, exec, "getenforce"),
		"uid":             RunCommand(maxLines, exec, "id"),
		"magisk":          RunCommand(maxLines, exec, "magisk", "-V"),
		"ksu":             RunCommand(maxLines, exec, "ksud", "-V"),
		"apatch":          RunCommand(maxLines, exec, "apd", "--version"),
	}
}
