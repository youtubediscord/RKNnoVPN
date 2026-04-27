package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type storedProfileNode struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Group    string          `json:"group"`
	Protocol string          `json:"protocol"`
	Server   string          `json:"server"`
	Port     int             `json:"port"`
	Link     string          `json:"link"`
	Outbound json.RawMessage `json:"outbound"`
	Stale    bool            `json:"stale"`
}

// RenderSingboxConfig generates a complete sing-box configuration JSON
// from the canonical Config and a resolved NodeProfile.
func RenderSingboxConfig(cfg *Config, profile *NodeProfile) ([]byte, error) {
	sbCfg := map[string]interface{}{
		"log": map[string]interface{}{
			"level":     "info",
			"timestamp": true,
		},
		"dns":      buildDNS(cfg),
		"inbounds": buildInbounds(cfg),
		"route":    buildRoute(cfg),
	}
	if cfg.Proxy.APIPort > 0 {
		sbCfg["experimental"] = map[string]interface{}{
			"clash_api": map[string]interface{}{
				"external_controller": fmt.Sprintf("127.0.0.1:%d", cfg.Proxy.APIPort),
				"secret":              "",
			},
		}
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

	applyDNSIPv6Mode(dns, cfg.IPv6.Mode)
	return dns
}

func applyDNSIPv6Mode(dns map[string]interface{}, mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "prefer_ipv4":
		dns["strategy"] = "prefer_ipv4"
	case "prefer_ipv6":
		dns["strategy"] = "prefer_ipv6"
	case "ipv6_only":
		dns["strategy"] = "ipv6_only"
	case "disable", "disabled", "off", "ipv4_only":
		dns["strategy"] = "ipv4_only"
		rules, _ := dns["rules"].([]map[string]interface{})
		dns["rules"] = append([]map[string]interface{}{
			{
				"query_type": []string{"AAAA"},
				"action":     "predefined",
				"rcode":      "NOERROR",
			},
		}, rules...)
	}
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
	profileInbounds := cfg.ResolveProfileInbounds()
	helperListen := "127.0.0.1"
	if profileInbounds.AllowLAN {
		helperListen = "0.0.0.0"
	}
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
			"override_address": firstNonEmpty(cfg.DNS.BootstrapIP, "1.1.1.1"),
			"override_port":    53,
		},
	}
	if profileInbounds.HTTPPort > 0 {
		inbounds = append(inbounds, map[string]interface{}{
			"type":        "http",
			"tag":         "status-http-in",
			"listen":      helperListen,
			"listen_port": profileInbounds.HTTPPort,
		})
	}
	if profileInbounds.SocksPort > 0 {
		inbounds = append(inbounds, map[string]interface{}{
			"type":        "socks",
			"tag":         "status-socks-in",
			"listen":      helperListen,
			"listen_port": profileInbounds.SocksPort,
		})
	}
	return inbounds
}

func buildOutbounds(cfg *Config, profile *NodeProfile) ([]map[string]interface{}, error) {
	nodeProfiles := ProfilesFromConfigNodes(cfg)
	if len(nodeProfiles) > 0 {
		var err error
		nodeProfiles, err = renderableProfileNodes(cfg, nodeProfiles)
		if err != nil {
			return nil, err
		}
	}
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
	groupPlans := buildGroupOutboundPlans(nodeProfiles)
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
			"tag":                         "auto",
			"outbounds":                   nodeTags,
			"url":                         testURL,
			"interval":                    "3m",
			"tolerance":                   50,
			"interrupt_exist_connections": true,
		})
		selectorOutbounds := append([]string{"auto"}, nodeTags...)
		for _, groupPlan := range groupPlans {
			selectorOutbounds = append(selectorOutbounds, groupPlan.selectorTag())
		}
		selectorDefault := "auto"
		if activeTag := activeProfileNodeTag(cfg, nodeProfiles); activeTag != "" {
			selectorDefault = activeTag
		}
		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "selector",
			"tag":                         "proxy",
			"outbounds":                   selectorOutbounds,
			"default":                     selectorDefault,
			"interrupt_exist_connections": true,
		})
	}
	outbounds = append(outbounds, buildGroupOutbounds(cfg, groupPlans)...)

	outbounds = append(outbounds,
		map[string]interface{}{
			"type": "direct",
			"tag":  "direct",
		},
	)
	return outbounds, nil
}

type groupOutboundPlan struct {
	name string
	base string
	tags []string
}

func (plan groupOutboundPlan) selectorTag() string {
	return "group-" + plan.base
}

func (plan groupOutboundPlan) autoTag() string {
	return "group-" + plan.base + "-auto"
}

func buildGroupOutboundPlans(profiles []*NodeProfile) []groupOutboundPlan {
	plans := []groupOutboundPlan{}
	byName := map[string]int{}
	usedBases := map[string]int{}

	for _, profile := range profiles {
		group := strings.TrimSpace(profile.Group)
		if group == "" || profile.Tag == "" {
			continue
		}
		index, ok := byName[group]
		if !ok {
			base := sanitizeOutboundTagSuffix(group)
			if base == "" {
				base = "default"
			}
			if count := usedBases[base]; count > 0 {
				base = fmt.Sprintf("%s-%d", base, count+1)
			}
			usedBases[base]++
			index = len(plans)
			byName[group] = index
			plans = append(plans, groupOutboundPlan{name: group, base: base})
		}
		plans[index].tags = append(plans[index].tags, profile.Tag)
	}

	return plans
}

func buildGroupOutbounds(cfg *Config, plans []groupOutboundPlan) []map[string]interface{} {
	if len(plans) == 0 {
		return nil
	}
	testURL := cfg.Health.URL
	if testURL == "" {
		testURL = "https://www.gstatic.com/generate_204"
	}

	outbounds := make([]map[string]interface{}, 0, len(plans)*2)
	for _, plan := range plans {
		if len(plan.tags) == 0 {
			continue
		}
		selectorOutbounds := append([]string{}, plan.tags...)
		selectorDefault := plan.tags[0]
		if len(plan.tags) > 1 {
			outbounds = append(outbounds, map[string]interface{}{
				"type":                        "urltest",
				"tag":                         plan.autoTag(),
				"outbounds":                   plan.tags,
				"url":                         testURL,
				"interval":                    "3m",
				"tolerance":                   50,
				"interrupt_exist_connections": true,
			})
			selectorOutbounds = append([]string{plan.autoTag()}, plan.tags...)
			selectorDefault = plan.autoTag()
		}
		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "selector",
			"tag":                         plan.selectorTag(),
			"outbounds":                   selectorOutbounds,
			"default":                     selectorDefault,
			"interrupt_exist_connections": true,
		})
	}
	return outbounds
}

func activeProfileNodeTag(cfg *Config, profiles []*NodeProfile) string {
	activeID := strings.TrimSpace(cfg.Profile.ActiveNodeID)
	if activeID == "" {
		return ""
	}
	for _, profile := range profiles {
		if profile.ID == activeID && profile.Tag != "" {
			return profile.Tag
		}
	}
	return ""
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
	case "wireguard":
		out["type"] = "wireguard"
		if profile.WGPrivateKey == "" {
			return nil, fmt.Errorf("renderer: wireguard private key is empty")
		}
		if profile.WGPeerPublicKey == "" {
			return nil, fmt.Errorf("renderer: wireguard peer public key is empty")
		}
		if len(profile.WGLocalAddress) == 0 {
			return nil, fmt.Errorf("renderer: wireguard local address is empty")
		}
		out["private_key"] = profile.WGPrivateKey
		out["peer_public_key"] = profile.WGPeerPublicKey
		out["local_address"] = profile.WGLocalAddress
		if profile.WGPresharedKey != "" {
			out["pre_shared_key"] = profile.WGPresharedKey
		}
		if profile.WGMTU > 0 {
			out["mtu"] = profile.WGMTU
		}
		if len(profile.WGReserved) > 0 {
			out["reserved"] = profile.WGReserved
		}
	default:
		return nil, fmt.Errorf("renderer: unsupported protocol %q", profile.Protocol)
	}

	// Transport layer (WebSocket, gRPC, HTTP/2). SOCKS is already a complete
	// outbound and must not inherit VLESS/Trojan transport settings.
	if profile.Protocol != "socks" && profile.Protocol != "wireguard" {
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

func ProfilesFromConfigNodes(cfg *Config) []*NodeProfile {
	profiles := make([]*NodeProfile, 0, len(cfg.Profile.Nodes))
	for index, raw := range cfg.Profile.Nodes {
		profile, err := profileFromStoredNode(raw, index)
		if err != nil {
			continue
		}
		if profile.Stale || profile.Address == "" || profile.Port == 0 || profile.Protocol == "" {
			continue
		}
		profiles = append(profiles, profile)
	}
	return profiles
}

// ResolveActiveProfile returns the selected profile node profile, or the
// first valid profile node when the selected one is absent. It returns nil when
// profile storage projection is empty or malformed.
func ResolveActiveProfile(cfg *Config) *NodeProfile {
	profiles := ProfilesFromConfigNodes(cfg)
	if len(profiles) == 0 {
		return nil
	}

	activeID := cfg.Profile.ActiveNodeID
	for _, profile := range profiles {
		if profile.ID == activeID {
			return profile
		}
	}
	return profiles[0]
}

func isDomainAddress(address string) bool {
	host := strings.Trim(address, "[]")
	return host != "" && net.ParseIP(host) == nil
}

func profileFromStoredNode(raw json.RawMessage, index int) (*NodeProfile, error) {
	var node storedProfileNode
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
		Group:       strings.TrimSpace(node.Group),
		Tag:         profileOutboundTag(node, index),
		Protocol:    protocol,
		Address:     node.Server,
		Port:        node.Port,
		Transport:   "tcp",
		Fingerprint: "chrome",
		Extra:       map[string]string{},
		Stale:       node.Stale,
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
	case "wireguard":
		if profile.Address == "" {
			profile.Address = stringFromMap(settings, "address")
		}
		if profile.Port == 0 {
			profile.Port = intFromMap(settings, "port")
		}
		profile.WGPrivateKey = stringFromMap(settings, "private_key")
		profile.WGPeerPublicKey = stringFromMap(settings, "peer_public_key")
		profile.WGPresharedKey = stringFromMap(settings, "pre_shared_key")
		profile.WGLocalAddress = stringSliceFromMap(settings, "local_address")
		profile.WGAllowedIPs = stringFromMap(settings, "allowed_ips")
		profile.WGMTU = intFromMap(settings, "mtu")
		profile.WGReserved = intSliceFromMap(settings, "reserved")
	}

	applyStreamSettings(profile, mapFromMap(outbound, "streamSettings"))
	applyProfileLinkFallback(profile, node.Link)
	return profile, nil
}

func renderableProfileNodes(cfg *Config, profiles []*NodeProfile) ([]*NodeProfile, error) {
	activeID := strings.TrimSpace(cfg.Profile.ActiveNodeID)
	renderable := make([]*NodeProfile, 0, len(profiles))
	skipped := 0
	for _, profile := range profiles {
		if _, err := buildProxyOutbound(profile); err != nil {
			if activeID != "" && profile.ID == activeID {
				return nil, fmt.Errorf("renderer: active node %q is invalid: %w", nodeDisplayName(profile), err)
			}
			skipped++
			continue
		}
		renderable = append(renderable, profile)
	}
	if len(renderable) == 0 && len(profiles) > 0 {
		return nil, fmt.Errorf("renderer: no usable profile nodes; skipped %d invalid node(s)", skipped)
	}
	return renderable, nil
}

func nodeDisplayName(profile *NodeProfile) string {
	return firstNonEmpty(profile.Name, profile.ID, profile.Address, profile.Tag)
}

func applyProfileLinkFallback(profile *NodeProfile, link string) {
	link = strings.TrimSpace(link)
	if link == "" {
		return
	}
	switch profile.Protocol {
	case "shadowsocks":
		method, password, host, port, ok := parseShadowsocksLink(link)
		if !ok {
			return
		}
		if profile.Address == "" {
			profile.Address = host
		}
		if profile.Port == 0 {
			profile.Port = port
		}
		if profile.Password == "" {
			profile.Password = password
			profile.UUID = password
		}
		if profile.SSMethod == "" {
			profile.SSMethod = method
		}
	case "trojan":
		password, host, port, ok := parseUserInfoProxyLink(link, "trojan")
		if !ok {
			return
		}
		if profile.Address == "" {
			profile.Address = host
		}
		if profile.Port == 0 {
			profile.Port = port
		}
		if profile.Password == "" && profile.UUID == "" {
			profile.Password = password
			profile.UUID = password
		}
	}
}

func parseShadowsocksLink(link string) (method string, password string, host string, port int, ok bool) {
	body, found := strings.CutPrefix(strings.TrimSpace(link), "ss://")
	if !found {
		return "", "", "", 0, false
	}
	body = strings.SplitN(body, "#", 2)[0]

	if at := strings.LastIndex(body, "@"); at >= 0 {
		userInfo := body[:at]
		hostPort := strings.SplitN(body[at+1:], "?", 2)[0]
		host, port, ok := parseHostPortValue(hostPort)
		if !ok {
			return "", "", "", 0, false
		}
		decoded := decodeShadowsocksUserInfo(userInfo)
		if decoded == "" {
			decoded, _ = url.QueryUnescape(userInfo)
		}
		method, password, ok := splitMethodPassword(decoded)
		if !ok {
			return "", "", "", 0, false
		}
		return method, password, host, port, true
	}

	decoded := decodeShadowsocksUserInfo(strings.SplitN(body, "?", 2)[0])
	if decoded == "" {
		return "", "", "", 0, false
	}
	at := strings.LastIndex(decoded, "@")
	if at < 0 {
		return "", "", "", 0, false
	}
	method, password, ok = splitMethodPassword(decoded[:at])
	if !ok {
		return "", "", "", 0, false
	}
	host, port, ok = parseHostPortValue(decoded[at+1:])
	if !ok {
		return "", "", "", 0, false
	}
	return method, password, host, port, true
}

func parseUserInfoProxyLink(link string, scheme string) (secret string, host string, port int, ok bool) {
	parsed, err := url.Parse(link)
	if err != nil || !strings.EqualFold(parsed.Scheme, scheme) || parsed.User == nil {
		return "", "", 0, false
	}
	secret = parsed.User.Username()
	if secret == "" {
		return "", "", 0, false
	}
	portValue, err := strconv.Atoi(parsed.Port())
	if err != nil || portValue <= 0 {
		return "", "", 0, false
	}
	return secret, parsed.Hostname(), portValue, parsed.Hostname() != ""
}

func decodeShadowsocksUserInfo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	decoders := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	for _, decoder := range decoders {
		if decoded, err := decoder.DecodeString(value); err == nil {
			return string(decoded)
		}
	}
	return ""
}

func splitMethodPassword(value string) (string, string, bool) {
	colon := strings.Index(value, ":")
	if colon <= 0 || colon == len(value)-1 {
		return "", "", false
	}
	return value[:colon], value[colon+1:], true
}

func parseHostPortValue(value string) (string, int, bool) {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		lastColon := strings.LastIndex(value, ":")
		if lastColon <= 0 || lastColon == len(value)-1 {
			return "", 0, false
		}
		host = strings.Trim(value[:lastColon], "[]")
		portText = value[lastColon+1:]
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 || host == "" {
		return "", 0, false
	}
	return host, port, true
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

func profileOutboundTag(node storedProfileNode, index int) string {
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

func sanitizeOutboundTagSuffix(value string) string {
	base := strings.ToLower(strings.TrimSpace(value))
	base = regexp.MustCompile(`[^a-z0-9_-]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	return base
}

func normalizeProtocol(value string) string {
	switch strings.ToLower(value) {
	case "ss", "shadowsocks":
		return "shadowsocks"
	case "socks", "socks4", "socks4a", "socks5":
		return "socks"
	case "hy2", "hysteria2":
		return "hysteria2"
	case "wg", "wireguard":
		return "wireguard"
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

func intSliceFromMap(source map[string]interface{}, key string) []int {
	if source == nil {
		return nil
	}
	values, ok := source[key].([]interface{})
	if !ok {
		value := intFromMap(source, key)
		if value != 0 {
			return []int{value}
		}
		return nil
	}
	result := make([]int, 0, len(values))
	for _, value := range values {
		switch v := value.(type) {
		case int:
			result = append(result, v)
		case float64:
			result = append(result, int(v))
		case json.Number:
			if parsed, err := strconv.Atoi(v.String()); err == nil {
				result = append(result, parsed)
			}
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				result = append(result, parsed)
			}
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
			"inbound": []string{"tproxy-in", "dns-in"},
			"action":  "sniff",
		},
		{
			"protocol": []string{"dns"},
			"action":   "hijack-dns",
		},
		{
			"inbound": []string{"tproxy-in"},
			"ip_cidr": []string{"127.0.0.0/8", "::1/128"},
			"action":  "reject",
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

	if len(cfg.Routing.AlwaysDirectApps) > 0 {
		rules = append(rules, map[string]interface{}{
			"package_name": cfg.Routing.AlwaysDirectApps,
			"outbound":     "direct",
		})
	}

	for _, rule := range buildAppGroupRouteRules(cfg) {
		rules = append(rules, rule)
	}

	finalOutbound := "proxy"
	if cfg.Routing.Mode == "direct" {
		finalOutbound = "direct"
	}

	route := map[string]interface{}{
		"rules":                   rules,
		"final":                   finalOutbound,
		"default_domain_resolver": "direct-dns",
	}

	// Build rule sets for geo databases.
	ruleSets := buildRuleSets(cfg)
	if len(ruleSets) > 0 {
		route["rule_set"] = ruleSets
	}

	return route
}

func buildAppGroupRouteRules(cfg *Config) []map[string]interface{} {
	if cfg == nil || len(cfg.Apps.AppGroups) == 0 {
		return nil
	}

	profiles := ProfilesFromConfigNodes(cfg)
	if len(profiles) == 0 {
		return nil
	}
	plans := buildGroupOutboundPlans(profiles)
	if len(plans) == 0 {
		return nil
	}

	groupOutbounds := make(map[string]string, len(plans))
	for _, plan := range plans {
		groupOutbounds[plan.name] = plan.selectorTag()
	}

	packagesByOutbound := map[string][]string{}
	for packageName, groupName := range cfg.Apps.AppGroups {
		packageName = strings.TrimSpace(packageName)
		groupName = strings.TrimSpace(groupName)
		if packageName == "" || groupName == "" {
			continue
		}
		outbound := groupOutbounds[groupName]
		if outbound == "" {
			continue
		}
		packagesByOutbound[outbound] = append(packagesByOutbound[outbound], packageName)
	}

	outbounds := make([]string, 0, len(packagesByOutbound))
	for outbound := range packagesByOutbound {
		outbounds = append(outbounds, outbound)
	}
	sort.Strings(outbounds)

	rules := make([]map[string]interface{}, 0, len(outbounds))
	for _, outbound := range outbounds {
		packages := packagesByOutbound[outbound]
		sort.Strings(packages)
		rules = append(rules, map[string]interface{}{
			"package_name": packages,
			"outbound":     outbound,
		})
	}
	return rules
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
