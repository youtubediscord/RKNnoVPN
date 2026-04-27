package diagnostics

import (
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
)

func Privacy(cfg *config.Config, maxLines int, exec ExecCommandFunc) map[string]interface{} {
	ipLinks := RunCommand(maxLines, exec, "ip", "link", "show")
	connectivity := RunCommand(maxLines, exec, "dumpsys", "connectivity")
	settingsProxyHost := RunCommand(maxLines, exec, "settings", "get", "global", "http_proxy")
	settingsProxyPort := RunCommand(maxLines, exec, "settings", "get", "global", "global_http_proxy_port")
	checks := map[string]interface{}{
		"no_vpn_like_interfaces":      FirstVPNLikeInterfaceLine(ipLinks.Lines) == "",
		"no_transport_vpn_hint":       !LinesContainAny(connectivity.Lines, "TRANSPORT_VPN", "VpnTransportInfo"),
		"no_loopback_dns":             !LinesContainLoopbackDNS(connectivity.Lines),
		"system_proxy_unset":          CommandLooksEmptySetting(settingsProxyHost) && CommandLooksEmptySetting(settingsProxyPort),
		"localhost_proxy_ports_clear": LocalhostProxyPortsClear(cfg),
	}
	if cfg != nil {
		profileInbounds := cfg.ResolveProfileInbounds()
		checks["clash_api_disabled"] = cfg.Proxy.APIPort == 0
		checks["helper_inbounds_disabled"] = profileInbounds.HTTPPort == 0 && profileInbounds.SocksPort == 0
		checks["helper_inbounds_local_only"] = !profileInbounds.AllowLAN
		checks["per_app_whitelist_default"] = cfg.Apps.Mode == "whitelist" || cfg.Apps.Mode == "off"
		checks["dns_hijack_per_uid"] = cfg.DNS.HijackPerUID
		checks["self_test_apps_direct"] = SelfTestProtectedAppsDirect()
	}
	return map[string]interface{}{
		"checks": checks,
		"protected_packages": map[string]interface{}{
			"self_test": core.SelfTestProtectedPackages,
		},
		"commands": map[string]CommandResult{
			"ip_link":                    ipLinks,
			"dumpsys_connectivity":       connectivity,
			"settings_global_http_proxy": settingsProxyHost,
			"settings_global_proxy_port": settingsProxyPort,
		},
	}
}

func SelfTestProtectedAppsDirect() bool {
	for _, pkgName := range core.SelfTestProtectedPackages {
		if !core.IsBuiltInAlwaysDirectPackage(pkgName) {
			return false
		}
	}
	return true
}

func FirstVPNLikeInterfaceLine(lines []string) string {
	for _, line := range lines {
		name := IPLinkInterfaceName(line)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "tun") ||
			strings.HasPrefix(lower, "wg") ||
			strings.HasPrefix(lower, "tap") ||
			strings.HasPrefix(lower, "ppp") ||
			strings.HasPrefix(lower, "ipsec") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func IPLinkInterfaceName(line string) string {
	line = strings.TrimSpace(line)
	firstColon := strings.Index(line, ":")
	if firstColon < 0 {
		return ""
	}
	rest := strings.TrimSpace(line[firstColon+1:])
	secondColon := strings.Index(rest, ":")
	if secondColon < 0 {
		return ""
	}
	name := strings.TrimSpace(rest[:secondColon])
	name = strings.TrimSuffix(name, "@NONE")
	if at := strings.Index(name, "@"); at >= 0 {
		name = name[:at]
	}
	return name
}
