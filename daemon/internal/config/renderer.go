package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type panelNode struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Protocol string          `json:"protocol"`
	Server   string          `json:"server"`
	Port     int             `json:"port"`
	Outbound json.RawMessage `json:"outbound"`
}

// RenderSingboxConfig generates a complete sing-box configuration JSON
// from the canonical Config and a resolved NodeProfile.
func RenderSingboxConfig(cfg *Config, profile *NodeProfile) ([]byte, error) {
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
		"dns":      buildDNS(cfg),
		"inbounds": buildInbounds(cfg),
		"route":    buildRoute(cfg),
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
		buildDNSServer("remote-dns", cfg.DNS.ProxyDNS, "proxy"),
		buildDNSServer("direct-dns", cfg.DNS.DirectDNS, ""),
		{
			"type":   "udp",
			"tag":    "bootstrap-dns",
			"server": cfg.DNS.BootstrapIP,
		},
	}

	rules := []map[string]interface{}{}

	if cfg.Routing.BlockAds {
		rules = append(rules, map[string]interface{}{
			"rule_set": []string{"geosite-ads"},
			"action":   "predefined",
			"rcode":    "NOERROR",
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

	customDirectDomains, _ := splitRuleInputs(cfg.Routing.CustomDirect)
	customBlockDomains, _ := splitRuleInputs(cfg.Routing.CustomBlock)

	// Custom direct domains use direct DNS.
	if len(customDirectDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": customDirectDomains,
			"server": "direct-dns",
		})
	}

	// Custom blocked domains use block DNS.
	if len(customBlockDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": customBlockDomains,
			"action": "predefined",
			"rcode":  "NOERROR",
		})
	}

	dns := map[string]interface{}{
		"servers": servers,
		"rules":   rules,
		"final":   "remote-dns",
	}

	if cfg.DNS.FakeIP {
		servers = append(servers, map[string]interface{}{
			"type":        "fakeip",
			"tag":         "fakeip",
			"inet4_range": "198.18.0.0/15",
			"inet6_range": "fc00::/18",
		})
		dns["servers"] = servers
		dns["rules"] = append(rules, map[string]interface{}{
			"query_type": []string{"A", "AAAA"},
			"server":     "fakeip",
		})
	}

	return dns
}

func buildDNSServer(tag, address, detour string) map[string]interface{} {
	server := map[string]interface{}{
		"tag": tag,
	}
	if detour != "" {
		server["detour"] = detour
	}

	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme == "" {
		server["type"] = "udp"
		server["server"] = address
		return server
	}

	switch parsed.Scheme {
	case "https", "h3":
		server["type"] = parsed.Scheme
		host := parsed.Hostname()
		if host == "" {
			host = parsed.Host
		}
		server["server"] = host
		if port := parsed.Port(); port != "" {
			if parsedPort, err := strconv.Atoi(port); err == nil {
				server["server_port"] = parsedPort
			}
		}
		if parsed.Path != "" && parsed.Path != "/dns-query" {
			server["path"] = parsed.Path
		}
		if net.ParseIP(host) == nil {
			server["domain_resolver"] = "bootstrap-dns"
		}
	case "tls", "quic":
		server["type"] = parsed.Scheme
		host := parsed.Hostname()
		if host == "" {
			host = parsed.Host
		}
		server["server"] = host
		if port := parsed.Port(); port != "" {
			if parsedPort, err := strconv.Atoi(port); err == nil {
				server["server_port"] = parsedPort
			}
		}
		if net.ParseIP(host) == nil {
			server["domain_resolver"] = "bootstrap-dns"
		}
	case "tcp", "udp":
		server["type"] = parsed.Scheme
		server["server"] = parsed.Hostname()
		if server["server"] == "" {
			server["server"] = parsed.Host
		}
		if port := parsed.Port(); port != "" {
			if parsedPort, err := strconv.Atoi(port); err == nil {
				server["server_port"] = parsedPort
			}
		}
	default:
		server["type"] = "udp"
		server["server"] = address
	}

	return server
}

func buildInbounds(cfg *Config) []map[string]interface{} {
	inbounds := []map[string]interface{}{
		{
			"type":        "tproxy",
			"tag":         "tproxy-in",
			"listen":      "::",
			"listen_port": cfg.Proxy.TProxyPort,
		},
		{
			"type":             "direct",
			"tag":              "dns-in",
			"listen":           "::",
			"listen_port":      cfg.Proxy.DNSPort,
			"override_address": "1.1.1.1",
			"override_port":    53,
		},
	}
	return inbounds
}

func buildOutbounds(cfg *Config, profile *NodeProfile) ([]map[string]interface{}, error) {
	nodeProfiles := ProfilesFromPanelNodes(cfg)
	if len(nodeProfiles) == 0 {
		if profile.Address == "" {
			return nil, fmt.Errorf("renderer: node address is empty")
		}
		if profile.Port == 0 {
			return nil, fmt.Errorf("renderer: node port is zero")
		}
		profile.Tag = "proxy"
		nodeProfiles = []*NodeProfile{profile}
	}

	outbounds := []map[string]interface{}{}
	nodeTags := make([]string, 0, len(nodeProfiles))
	for index, nodeProfile := range nodeProfiles {
		if len(nodeProfiles) == 1 {
			nodeProfile.Tag = "proxy"
		} else if nodeProfile.Tag == "" {
			nodeProfile.Tag = fmt.Sprintf("node-%d", index+1)
		}
		proxyOut, err := buildProxyOutbound(nodeProfile)
		if err != nil {
			return nil, err
		}
		outbounds = append(outbounds, proxyOut)
		nodeTags = append(nodeTags, nodeProfile.Tag)
	}

	if len(nodeTags) > 1 {
		testURL := cfg.Health.URL
		if testURL == "" {
			testURL = "https://www.gstatic.com/generate_204"
		}
		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "urltest",
			"tag":                         "proxy",
			"outbounds":                   nodeTags,
			"url":                         testURL,
			"interval":                    "3m",
			"tolerance":                   50,
			"interrupt_exist_connections": true,
		})
	}

	outbounds = append(outbounds,
		map[string]interface{}{
			"type": "direct",
			"tag":  "direct",
		},
	)
	return outbounds, nil
}

func buildProxyOutbound(profile *NodeProfile) (map[string]interface{}, error) {
	tag := profile.Tag
	if tag == "" {
		tag = "proxy"
	}
	out := map[string]interface{}{
		"tag":         tag,
		"server":      profile.Address,
		"server_port": profile.Port,
	}
	if isDomainAddress(profile.Address) {
		out["domain_resolver"] = "direct-dns"
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

	case "socks":
		out["type"] = "socks"
		out["version"] = valueOrDefault(profile.SocksVersion, "5")
		if profile.Username != "" {
			out["username"] = profile.Username
		}
		if profile.Password != "" {
			out["password"] = profile.Password
		}
		if profile.Network != "" {
			out["network"] = profile.Network
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

	// Transport layer (WebSocket, gRPC, HTTP/2). SOCKS is already a complete
	// outbound and must not inherit VLESS/Trojan transport settings.
	if profile.Protocol != "socks" {
		tp, err := buildTransport(profile)
		if err != nil {
			return nil, err
		}
		if tp != nil {
			out["transport"] = tp
		}
	}

	return out, nil
}

func ProfilesFromPanelNodes(cfg *Config) []*NodeProfile {
	profiles := make([]*NodeProfile, 0, len(cfg.Panel.Nodes))
	for index, raw := range cfg.Panel.Nodes {
		profile, err := profileFromPanelNode(raw, index)
		if err != nil {
			continue
		}
		if profile.Address == "" || profile.Port == 0 || profile.Protocol == "" {
			continue
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

func isDomainAddress(address string) bool {
	host := strings.Trim(address, "[]")
	return host != "" && net.ParseIP(host) == nil
}

func profileFromPanelNode(raw json.RawMessage, index int) (*NodeProfile, error) {
	var node panelNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, err
	}

	var outbound map[string]interface{}
	if len(node.Outbound) > 0 {
		_ = json.Unmarshal(node.Outbound, &outbound)
	}

	protocol := normalizeProtocol(node.Protocol)
	if value := stringFromMap(outbound, "protocol"); protocol == "" && value != "" {
		protocol = normalizeProtocol(value)
	}
	profile := &NodeProfile{
		ID:          node.ID,
		Name:        node.Name,
		Tag:         panelOutboundTag(node, index),
		Protocol:    protocol,
		Address:     node.Server,
		Port:        node.Port,
		Transport:   "tcp",
		Fingerprint: "chrome",
		Extra:       map[string]string{},
	}

	settings := mapFromMap(outbound, "settings")
	switch protocol {
	case "vless", "vmess":
		vnext := firstMapFromArray(settings, "vnext")
		user := firstMapFromArray(vnext, "users")
		if profile.Address == "" {
			profile.Address = stringFromMap(vnext, "address")
		}
		if profile.Port == 0 {
			profile.Port = intFromMap(vnext, "port")
		}
		profile.UUID = stringFromMap(user, "id")
		profile.Flow = stringFromMap(user, "flow")
		if protocol == "vmess" {
			profile.AlterID = intFromMap(user, "alterId")
			profile.Security = valueOrDefault(stringFromMap(user, "security"), "auto")
		}
	case "trojan", "shadowsocks":
		server := firstMapFromArray(settings, "servers")
		if profile.Address == "" {
			profile.Address = stringFromMap(server, "address")
		}
		if profile.Port == 0 {
			profile.Port = intFromMap(server, "port")
		}
		profile.Password = stringFromMap(server, "password")
		profile.UUID = profile.Password
		if protocol == "shadowsocks" {
			profile.SSMethod = stringFromMap(server, "method")
			profile.SSPlugin = stringFromMap(server, "plugin")
			profile.SSPluginOpts = stringFromMap(server, "plugin_opts")
		}
	case "socks":
		if profile.Address == "" {
			profile.Address = stringFromMap(settings, "address")
		}
		if profile.Port == 0 {
			profile.Port = intFromMap(settings, "port")
		}
		profile.Username = stringFromMap(settings, "username")
		profile.Password = stringFromMap(settings, "password")
		profile.SocksVersion = valueOrDefault(stringFromMap(settings, "version"), "5")
		profile.Network = stringFromMap(settings, "network")
	case "hysteria2":
		if profile.Address == "" {
			profile.Address = stringFromMap(settings, "address")
		}
		if profile.Port == 0 {
			profile.Port = intFromMap(settings, "port")
		}
		profile.Password = stringFromMap(settings, "password")
		profile.UUID = profile.Password
		profile.ServerPorts = stringSliceFromMap(settings, "server_ports")
		obfs := mapFromMap(settings, "obfs")
		profile.ObfsType = stringFromMap(obfs, "type")
		profile.ObfsPassword = stringFromMap(obfs, "password")
	case "tuic":
		if profile.Address == "" {
			profile.Address = stringFromMap(settings, "address")
		}
		if profile.Port == 0 {
			profile.Port = intFromMap(settings, "port")
		}
		profile.UUID = stringFromMap(settings, "uuid")
		profile.Password = stringFromMap(settings, "password")
		copyStringExtra(profile.Extra, settings, "congestion_control")
		copyStringExtra(profile.Extra, settings, "udp_relay_mode")
		copyStringExtra(profile.Extra, settings, "udp_over_stream")
		copyStringExtra(profile.Extra, settings, "zero_rtt_handshake")
		copyStringExtra(profile.Extra, settings, "heartbeat")
	}

	applyStreamSettings(profile, mapFromMap(outbound, "streamSettings"))
	return profile, nil
}

func applyStreamSettings(profile *NodeProfile, stream map[string]interface{}) {
	if len(stream) == 0 || profile.Protocol == "socks" {
		return
	}
	network := stringFromMap(stream, "network")
	if network == "" {
		network = "tcp"
	}
	security := stringFromMap(stream, "security")
	if security == "reality" {
		profile.Transport = "reality"
	} else {
		profile.Transport = network
	}
	if security == "tls" {
		profile.Extra["security"] = "tls"
	}

	if tls := mapFromMap(stream, "tlsSettings"); len(tls) > 0 {
		applyTLSSettings(profile, tls)
	}
	if reality := mapFromMap(stream, "realitySettings"); len(reality) > 0 {
		profile.Transport = "reality"
		applyTLSSettings(profile, reality)
		profile.RealityPubKey = stringFromMap(reality, "publicKey")
		profile.RealityShortID = stringFromMap(reality, "shortId")
		profile.Extra["public_key"] = profile.RealityPubKey
		profile.Extra["short_id"] = profile.RealityShortID
	}

	switch network {
	case "ws":
		ws := mapFromMap(stream, "wsSettings")
		profile.Extra["path"] = stringFromMap(ws, "path")
		headers := mapFromMap(ws, "headers")
		profile.Extra["host"] = stringFromMap(headers, "Host")
	case "grpc":
		grpc := mapFromMap(stream, "grpcSettings")
		profile.Extra["service_name"] = stringFromMap(grpc, "serviceName")
		profile.Extra["mode"] = stringFromMap(grpc, "mode")
		profile.Extra["authority"] = stringFromMap(grpc, "authority")
	case "http", "h2":
		httpSettings := mapFromMap(stream, "httpSettings")
		profile.Extra["path"] = stringFromMap(httpSettings, "path")
		profile.Extra["host"] = strings.Join(stringSliceFromMap(httpSettings, "host"), ",")
	case "httpupgrade":
		httpUpgrade := mapFromMap(stream, "httpupgradeSettings")
		profile.Extra["path"] = stringFromMap(httpUpgrade, "path")
		profile.Extra["host"] = stringFromMap(httpUpgrade, "host")
	case "quic":
		quic := mapFromMap(stream, "quicSettings")
		profile.Extra["quic_security"] = stringFromMap(quic, "security")
		profile.Extra["key"] = stringFromMap(quic, "key")
		profile.Extra["header_type"] = stringFromMap(quic, "header_type")
	}
	if profile.Protocol == "hysteria2" || profile.Protocol == "tuic" {
		profile.Extra["network"] = network
	}
}

func applyTLSSettings(profile *NodeProfile, tls map[string]interface{}) {
	profile.TLSServer = firstNonEmpty(
		stringFromMap(tls, "serverName"),
		stringFromMap(tls, "server_name"),
	)
	profile.Fingerprint = firstNonEmpty(
		stringFromMap(tls, "fingerprint"),
		profile.Fingerprint,
	)
	if alpn := stringSliceFromMap(tls, "alpn"); len(alpn) > 0 {
		profile.Extra["alpn"] = strings.Join(alpn, ",")
	}
	if boolFromMap(tls, "allowInsecure") {
		profile.Extra["insecure"] = "true"
	}
}

func panelOutboundTag(node panelNode, index int) string {
	base := node.ID
	if base == "" {
		base = node.Name
	}
	if base == "" {
		base = fmt.Sprintf("node-%d", index+1)
	}
	base = strings.ToLower(base)
	base = regexp.MustCompile(`[^a-z0-9_-]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = fmt.Sprintf("node-%d", index+1)
	}
	return "node-" + base
}

func normalizeProtocol(value string) string {
	switch strings.ToLower(value) {
	case "ss", "shadowsocks":
		return "shadowsocks"
	case "socks", "socks4", "socks4a", "socks5":
		return "socks"
	case "hy2", "hysteria2":
		return "hysteria2"
	default:
		return strings.ToLower(value)
	}
}

func mapFromMap(source map[string]interface{}, key string) map[string]interface{} {
	if source == nil {
		return nil
	}
	if value, ok := source[key].(map[string]interface{}); ok {
		return value
	}
	return nil
}

func firstMapFromArray(source map[string]interface{}, key string) map[string]interface{} {
	if source == nil {
		return nil
	}
	values, ok := source[key].([]interface{})
	if !ok || len(values) == 0 {
		return nil
	}
	value, _ := values[0].(map[string]interface{})
	return value
}

func stringFromMap(source map[string]interface{}, key string) string {
	if source == nil {
		return ""
	}
	switch value := source[key].(type) {
	case string:
		return value
	case float64:
		return strconv.Itoa(int(value))
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func intFromMap(source map[string]interface{}, key string) int {
	if source == nil {
		return 0
	}
	switch value := source[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	default:
		return 0
	}
}

func boolFromMap(source map[string]interface{}, key string) bool {
	if source == nil {
		return false
	}
	switch value := source[key].(type) {
	case bool:
		return value
	case string:
		return parseBool(value)
	default:
		return false
	}
}

func stringSliceFromMap(source map[string]interface{}, key string) []string {
	if source == nil {
		return nil
	}
	values, ok := source[key].([]interface{})
	if !ok {
		if value := stringFromMap(source, key); value != "" {
			return []string{value}
		}
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if str, ok := value.(string); ok && str != "" {
			result = append(result, str)
		}
	}
	return result
}

func copyStringExtra(extra map[string]string, source map[string]interface{}, key string) {
	if value := stringFromMap(source, key); value != "" {
		extra[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
			"inbound": []string{"tproxy-in"},
			"action":  "sniff",
		},
		{
			"protocol": []string{"dns"},
			"action":   "hijack-dns",
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
			"action":   "reject",
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
			"domain":   customDirectDomains,
			"outbound": "direct",
		})
	}
	if len(customDirectCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr":  customDirectCIDRs,
			"outbound": "direct",
		})
	}

	// Custom proxy domains/IPs.
	if len(customProxyDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain":   customProxyDomains,
			"outbound": "proxy",
		})
	}
	if len(customProxyCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr":  customProxyCIDRs,
			"outbound": "proxy",
		})
	}

	// Custom block domains/IPs.
	if len(customBlockDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": customBlockDomains,
			"action": "reject",
		})
	}
	if len(customBlockCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr": customBlockCIDRs,
			"action":  "reject",
		})
	}

	route := map[string]interface{}{
		"rules":                   rules,
		"final":                   "proxy",
		"default_domain_resolver": "direct-dns",
		"default_mark":            255,
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
