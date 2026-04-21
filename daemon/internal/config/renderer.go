package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RenderSingboxConfig generates a complete sing-box configuration JSON
// from the canonical Config and a resolved NodeProfile.
func RenderSingboxConfig(cfg *Config, profile *NodeProfile) ([]byte, error) {
	if profile.Address == "" {
		return nil, fmt.Errorf("renderer: node address is empty")
	}
	if profile.Port == 0 {
		return nil, fmt.Errorf("renderer: node port is zero")
	}

	sbCfg := map[string]interface{}{
		"log": map[string]interface{}{
			"level":     "info",
			"timestamp": true,
		},
		"experimental": map[string]interface{}{
			"clash_api": map[string]interface{}{
				"external_controller": fmt.Sprintf("127.0.0.1:%d", cfg.Proxy.APIPort),
			},
		},
		"dns":       buildDNS(cfg),
		"inbounds": buildInbounds(cfg),
		"route":     buildRoute(cfg),
	}
	outbounds, err := buildOutbounds(cfg, profile)
	if err != nil {
		return nil, err
	}
	sbCfg["outbounds"] = outbounds

	data, err := json.MarshalIndent(sbCfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("renderer: marshal: %w", err)
	}
	return data, nil
}

func buildDNS(cfg *Config) map[string]interface{} {
	servers := []map[string]interface{}{
		{
			"tag":             "remote-dns",
			"address":         cfg.DNS.ProxyDNS,
			"address_resolver": "bootstrap-dns",
			"detour":          "proxy",
		},
		{
			"tag":             "direct-dns",
			"address":         cfg.DNS.DirectDNS,
			"address_resolver": "bootstrap-dns",
			"detour":          "direct",
		},
		{
			"tag":     "bootstrap-dns",
			"address": cfg.DNS.BootstrapIP,
			"detour":  "direct",
		},
		{
			"tag":     "block-dns",
			"address": "rcode://success",
		},
	}

	rules := []map[string]interface{}{
		{
			"outbound": []string{"any"},
			"server":   "bootstrap-dns",
		},
	}

	if cfg.Routing.BlockAds {
		rules = append(rules, map[string]interface{}{
			"rule_set": []string{"geosite-ads"},
			"server":   "block-dns",
		})
	}

	if cfg.Routing.BypassRussia {
		rules = append(rules, map[string]interface{}{
			"rule_set": []string{"geosite-ru"},
			"server":   "direct-dns",
		})
	}

	if cfg.Routing.BypassChina {
		rules = append(rules, map[string]interface{}{
			"rule_set": []string{"geosite-cn"},
			"server":   "direct-dns",
		})
	}

	customDirectDomains, customDirectCIDRs := splitRuleInputs(cfg.Routing.CustomDirect)
	customBlockDomains, customBlockCIDRs := splitRuleInputs(cfg.Routing.CustomBlock)

	// Custom direct domains use direct DNS.
	if len(customDirectDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": customDirectDomains,
			"server": "direct-dns",
		})
	}

	if len(customDirectCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr": customDirectCIDRs,
			"server":  "direct-dns",
		})
	}

	// Custom blocked domains use block DNS.
	if len(customBlockDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": customBlockDomains,
			"server": "block-dns",
		})
	}

	if len(customBlockCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr": customBlockCIDRs,
			"server":  "block-dns",
		})
	}

	dns := map[string]interface{}{
		"servers": servers,
		"rules":   rules,
		"final":   "remote-dns",
		"independent_cache": true,
	}

	if cfg.DNS.FakeIP {
		dns["fakeip"] = map[string]interface{}{
			"enabled":    true,
			"inet4_range": "198.18.0.0/15",
			"inet6_range": "fc00::/18",
		}
	}

	return dns
}

func buildInbounds(cfg *Config) []map[string]interface{} {
	inbounds := []map[string]interface{}{
		{
			"type":             "tproxy",
			"tag":              "tproxy-in",
			"listen":           "::",
			"listen_port":      cfg.Proxy.TProxyPort,
			"sniff":            true,
			"sniff_override_destination": true,
		},
		{
			"type":        "direct",
			"tag":         "dns-in",
			"listen":      "::",
			"listen_port": cfg.Proxy.DNSPort,
			"override_address": "1.1.1.1",
			"override_port":    53,
		},
	}
	return inbounds
}

func buildOutbounds(cfg *Config, profile *NodeProfile) ([]map[string]interface{}, error) {
	proxyOut, err := buildProxyOutbound(profile)
	if err != nil {
		return nil, err
	}

	outbounds := []map[string]interface{}{
		proxyOut,
		{
			"type": "direct",
			"tag":  "direct",
		},
		{
			"type": "block",
			"tag":  "block",
		},
		{
			"type": "dns",
			"tag":  "dns-out",
		},
	}
	return outbounds, nil
}

func buildProxyOutbound(profile *NodeProfile) (map[string]interface{}, error) {
	out := map[string]interface{}{
		"tag":        "proxy",
		"server":     profile.Address,
		"server_port": profile.Port,
	}

	switch profile.Protocol {
		case "vless":
			out["type"] = "vless"
			if profile.UUID == "" {
				return nil, fmt.Errorf("renderer: vless uuid is empty")
			}
			out["uuid"] = profile.UUID
			if profile.Flow != "" {
				out["flow"] = profile.Flow
		}
		if tls := buildTLS(profile, false); tls != nil {
			out["tls"] = tls
		}

		case "trojan":
			out["type"] = "trojan"
			password := profile.Password
			if password == "" {
				password = profile.UUID
			}
			if password == "" {
				return nil, fmt.Errorf("renderer: trojan password is empty")
			}
			out["password"] = password
			if tls := buildTLS(profile, true); tls != nil {
				out["tls"] = tls
			}

		case "vmess":
			out["type"] = "vmess"
			if profile.UUID == "" {
				return nil, fmt.Errorf("renderer: vmess uuid is empty")
			}
			out["uuid"] = profile.UUID
		out["alter_id"] = profile.AlterID
		sec := profile.Security
		if sec == "" {
			sec = "auto"
		}
		out["security"] = sec
		if tls := buildTLS(profile, false); tls != nil {
			out["tls"] = tls
		}

		case "shadowsocks":
			out["type"] = "shadowsocks"
			password := profile.Password
			if password == "" {
				password = profile.UUID
			}
			if password == "" {
				return nil, fmt.Errorf("renderer: shadowsocks password is empty")
			}
			out["password"] = password
		method := profile.SSMethod
		if method == "" {
			method = "2022-blake3-aes-128-gcm"
		}
		out["method"] = method
		if profile.SSPlugin != "" {
			out["plugin"] = profile.SSPlugin
		}
		if profile.SSPluginOpts != "" {
			out["plugin_opts"] = profile.SSPluginOpts
		}

	case "hysteria2":
		out["type"] = "hysteria2"
		password := profile.Password
			if password == "" {
				password = profile.UUID
			}
			if password == "" {
				return nil, fmt.Errorf("renderer: hysteria2 password is empty")
			}
			out["password"] = password
		if len(profile.ServerPorts) > 0 {
			out["server_ports"] = profile.ServerPorts
		}
		if profile.ObfsType != "" || profile.ObfsPassword != "" {
			out["obfs"] = map[string]interface{}{
				"type":     valueOrDefault(profile.ObfsType, "salamander"),
				"password": profile.ObfsPassword,
			}
		}
		if tls := buildTLS(profile, true); tls != nil {
			out["tls"] = tls
		}
		if value := profile.Extra["network"]; value != "" {
			out["network"] = value
		}

	case "tuic":
			out["type"] = "tuic"
			if profile.UUID == "" || profile.Password == "" {
				return nil, fmt.Errorf("renderer: tuic uuid/password is empty")
			}
			out["uuid"] = profile.UUID
		out["password"] = profile.Password
		if value := profile.Extra["congestion_control"]; value != "" {
			out["congestion_control"] = value
		}
		if value := profile.Extra["udp_relay_mode"]; value != "" {
			out["udp_relay_mode"] = value
		}
		if value := profile.Extra["udp_over_stream"]; value != "" {
			out["udp_over_stream"] = parseBool(value)
		}
		if value := profile.Extra["zero_rtt_handshake"]; value != "" {
			out["zero_rtt_handshake"] = parseBool(value)
		}
		if value := profile.Extra["heartbeat"]; value != "" {
			out["heartbeat"] = value
		}
		if tls := buildTLS(profile, true); tls != nil {
			out["tls"] = tls
		}
		if value := profile.Extra["network"]; value != "" {
			out["network"] = value
		}
	default:
			return nil, fmt.Errorf("renderer: unsupported protocol %q", profile.Protocol)
		}

	// Transport layer (WebSocket, gRPC, HTTP/2).
	tp, err := buildTransport(profile)
	if err != nil {
		return nil, err
	}
	if tp != nil {
		out["transport"] = tp
	}

	return out, nil
}

func buildTLS(profile *NodeProfile, force bool) map[string]interface{} {
	transportSecurity := profile.Extra["security"]
	tlsEnabled := force ||
		profile.Transport == "reality" ||
		profile.RealityPubKey != "" ||
		transportSecurity == "tls" ||
		transportSecurity == "reality"
	if !tlsEnabled {
		return nil
	}

	tls := map[string]interface{}{
		"enabled": true,
	}

	if profile.TLSServer != "" {
		tls["server_name"] = profile.TLSServer
	}
	if parseBool(profile.Extra["insecure"]) {
		tls["insecure"] = true
	}
	if alpn := splitCSV(profile.Extra["alpn"]); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	if pins := splitCSV(profile.Extra["pin_sha256"]); len(pins) > 0 {
		tls["certificate_public_key_sha256"] = pins
	}

	fp := profile.Fingerprint
	if fp == "" {
		fp = "chrome"
	}
	tls["utls"] = map[string]interface{}{
		"enabled":     true,
		"fingerprint": fp,
	}

	// REALITY TLS settings.
	if profile.Transport == "reality" || profile.RealityPubKey != "" || transportSecurity == "reality" {
		publicKey := profile.RealityPubKey
		if publicKey == "" {
			publicKey = profile.Extra["public_key"]
		}
		shortID := profile.RealityShortID
		if shortID == "" {
			shortID = profile.Extra["short_id"]
		}
		tls["reality"] = map[string]interface{}{
			"enabled":    true,
			"public_key": publicKey,
			"short_id":   shortID,
		}
	}

	return tls
}

func buildTransport(profile *NodeProfile) (map[string]interface{}, error) {
	switch profile.Transport {
	case "ws":
		tp := map[string]interface{}{
			"type": "ws",
		}
		if path, ok := profile.Extra["path"]; ok {
			tp["path"] = path
		}
		if host, ok := profile.Extra["host"]; ok {
			tp["headers"] = map[string]interface{}{
				"Host": host,
			}
		}
		return tp, nil

	case "grpc":
		tp := map[string]interface{}{
			"type": "grpc",
		}
		if sn, ok := profile.Extra["service_name"]; ok {
			tp["service_name"] = sn
		}
		if mode, ok := profile.Extra["mode"]; ok {
			tp["mode"] = mode
		}
		if authority, ok := profile.Extra["authority"]; ok {
			tp["authority"] = authority
		}
		return tp, nil

	case "http", "h2":
		tp := map[string]interface{}{
			"type": "http",
		}
		if host, ok := profile.Extra["host"]; ok {
			hosts := []string{}
			for _, item := range strings.Split(host, ",") {
				item = strings.TrimSpace(item)
				if item != "" {
					hosts = append(hosts, item)
				}
			}
			if len(hosts) > 0 {
				tp["host"] = hosts
			}
		}
		if path, ok := profile.Extra["path"]; ok {
			tp["path"] = path
		}
		return tp, nil

	case "tcp":
		if headerType := profile.Extra["header_type"]; headerType != "" && headerType != "none" {
			return nil, fmt.Errorf("renderer: sing-box does not support V2Ray TCP header transport")
		}
		return nil, nil

	case "quic":
		tp := map[string]interface{}{
			"type":     "quic",
			"security": profile.Extra["quic_security"],
		}
		if tp["security"] == "" {
			tp["security"] = "none"
		}
		if key, ok := profile.Extra["key"]; ok && key != "" {
			tp["key"] = key
		}
		if headerType, ok := profile.Extra["header_type"]; ok && headerType != "" {
			tp["header_type"] = headerType
		}
		return tp, nil

	case "httpupgrade":
		tp := map[string]interface{}{
			"type": "httpupgrade",
		}
		if host, ok := profile.Extra["host"]; ok && host != "" {
			tp["host"] = host
		}
		if path, ok := profile.Extra["path"]; ok && path != "" {
			tp["path"] = path
		}
		return tp, nil
	}

	// "reality", "tcp", or empty -- no separate transport block needed.
	return nil, nil
}

func buildRoute(cfg *Config) map[string]interface{} {
	rules := []map[string]interface{}{
		{
			"protocol": []string{"dns"},
			"outbound": "dns-out",
		},
	}

	// Bypass private/LAN ranges.
	if cfg.Routing.BypassLAN {
		rules = append(rules, map[string]interface{}{
			"ip_is_private": true,
			"outbound":      "direct",
		})
	}

	// Block ads via geosite rule set.
	if cfg.Routing.BlockAds {
		rules = append(rules, map[string]interface{}{
			"rule_set": []string{"geosite-ads"},
			"outbound": "block",
		})
	}

	// Bypass Russia via geoip/geosite rule sets.
	if cfg.Routing.BypassRussia {
		rules = append(rules, map[string]interface{}{
			"rule_set": []string{"geoip-ru", "geosite-ru"},
			"outbound": "direct",
		})
	}

	if cfg.Routing.BypassChina {
		rules = append(rules, map[string]interface{}{
			"rule_set": []string{"geoip-cn", "geosite-cn"},
			"outbound": "direct",
		})
	}

	customDirectDomains, customDirectCIDRs := splitRuleInputs(cfg.Routing.CustomDirect)
	customProxyDomains, customProxyCIDRs := splitRuleInputs(cfg.Routing.CustomProxy)
	customBlockDomains, customBlockCIDRs := splitRuleInputs(cfg.Routing.CustomBlock)

	// Custom direct domains/IPs.
	if len(customDirectDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": customDirectDomains,
			"outbound": "direct",
		})
	}
	if len(customDirectCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr": customDirectCIDRs,
			"outbound": "direct",
		})
	}

	// Custom proxy domains/IPs.
	if len(customProxyDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": customProxyDomains,
			"outbound": "proxy",
		})
	}
	if len(customProxyCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr": customProxyCIDRs,
			"outbound": "proxy",
		})
	}

	// Custom block domains/IPs.
	if len(customBlockDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": customBlockDomains,
			"outbound": "block",
		})
	}
	if len(customBlockCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr": customBlockCIDRs,
			"outbound": "block",
		})
	}

	route := map[string]interface{}{
		"rules":                rules,
		"final":                "proxy",
		"default_mark":         255,
		"auto_detect_interface": true,
	}

	// Build rule sets for geo databases.
	ruleSets := buildRuleSets(cfg)
	if len(ruleSets) > 0 {
		route["rule_set"] = ruleSets
	}

	return route
}

func splitCSV(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func valueOrDefault(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func buildRuleSets(cfg *Config) []map[string]interface{} {
	var sets []map[string]interface{}

	if cfg.Routing.BypassRussia {
		if cfg.Routing.GeoIPPath != "" {
			sets = append(sets, map[string]interface{}{
				"type":   "local",
				"tag":    "geoip-ru",
				"format": "binary",
				"path":   cfg.Routing.GeoIPPath,
			})
		}
		if cfg.Routing.GeoSitePath != "" {
			sets = append(sets, map[string]interface{}{
				"type":   "local",
				"tag":    "geosite-ru",
				"format": "binary",
				"path":   cfg.Routing.GeoSitePath,
			})
		}
	}

	if cfg.Routing.BypassChina {
		if cfg.Routing.GeoIPPath != "" {
			sets = append(sets, map[string]interface{}{
				"type":   "local",
				"tag":    "geoip-cn",
				"format": "binary",
				"path":   cfg.Routing.GeoIPPath,
			})
		}
		if cfg.Routing.GeoSitePath != "" {
			sets = append(sets, map[string]interface{}{
				"type":   "local",
				"tag":    "geosite-cn",
				"format": "binary",
				"path":   cfg.Routing.GeoSitePath,
			})
		}
	}

	if cfg.Routing.BlockAds && cfg.Routing.GeoSitePath != "" {
		sets = append(sets, map[string]interface{}{
			"type":   "local",
			"tag":    "geosite-ads",
			"format": "binary",
			"path":   cfg.Routing.GeoSitePath,
		})
	}

	return sets
}

func splitRuleInputs(values []string) (domains []string, cidrs []string) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "/") {
			cidrs = append(cidrs, value)
		} else {
			domains = append(domains, value)
		}
	}
	return domains, cidrs
}
