package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderSingboxConfigAvoidsRemovedSingBox113Fields(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	cfg.Routing.BlockAds = true
	cfg.Routing.CustomBlock = []string{"ads.example"}

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	for _, rawOutbound := range rendered["outbounds"].([]any) {
		outbound := rawOutbound.(map[string]any)
		switch outbound["type"] {
		case "dns", "block":
			t.Fatalf("removed sing-box 1.13 outbound rendered: %v", outbound["type"])
		}
		if outbound["tag"] == "proxy" && outbound["domain_resolver"] != "direct-dns" {
			t.Fatalf("proxy outbound with domain server must set domain_resolver: %#v", outbound)
		}
	}

	dns := rendered["dns"].(map[string]any)
	if _, ok := dns["independent_cache"]; ok {
		t.Fatal("deprecated independent_cache field rendered")
	}
	for _, rawServer := range dns["servers"].([]any) {
		server := rawServer.(map[string]any)
		if _, ok := server["type"]; !ok {
			t.Fatalf("legacy DNS server without type rendered: %#v", server)
		}
		if _, ok := server["address"]; ok {
			t.Fatalf("legacy DNS server address field rendered: %#v", server)
		}
		if _, ok := server["address_resolver"]; ok {
			t.Fatalf("legacy DNS server address_resolver field rendered: %#v", server)
		}
		if tag := server["tag"]; (tag == "direct-dns" || tag == "bootstrap-dns") && server["detour"] == "direct" {
			t.Fatalf("DNS server must not detour through empty direct outbound: %#v", server)
		}
	}

	var dnsInbound map[string]any
	for _, rawInbound := range rendered["inbounds"].([]any) {
		inbound := rawInbound.(map[string]any)
		if _, ok := inbound["sniff"]; ok {
			t.Fatalf("removed inbound sniff field rendered: %#v", inbound)
		}
		if _, ok := inbound["sniff_override_destination"]; ok {
			t.Fatalf("removed inbound sniff_override_destination field rendered: %#v", inbound)
		}
		if inbound["tag"] == "dns-in" {
			dnsInbound = inbound
		}
	}
	if dnsInbound == nil {
		t.Fatal("DNS direct inbound was not rendered")
	}
	if dnsInbound["type"] != "direct" {
		t.Fatalf("DNS inbound must use direct override listener, got %#v", dnsInbound)
	}
	if dnsInbound["override_address"] != "1.1.1.1" {
		t.Fatalf("DNS direct inbound must override destination away from itself: %#v", dnsInbound)
	}
	if dnsInbound["override_port"] != float64(53) {
		t.Fatalf("DNS direct inbound must override destination port 53: %#v", dnsInbound)
	}

	rules := rendered["route"].(map[string]any)["rules"].([]any)
	if rendered["route"].(map[string]any)["default_domain_resolver"] != "direct-dns" {
		t.Fatalf("route should define default_domain_resolver: %#v", rendered["route"])
	}
	if _, ok := rendered["route"].(map[string]any)["default_mark"]; ok {
		t.Fatalf("route must not mark sing-box's own outbound sockets on Android: %#v", rendered["route"])
	}
	if _, ok := rendered["route"].(map[string]any)["auto_detect_interface"]; ok {
		t.Fatalf("route should not require default interface during service start: %#v", rendered["route"])
	}
	if rules[0].(map[string]any)["action"] != "sniff" {
		t.Fatalf("first route rule should sniff tproxy traffic: %#v", rules[0])
	}
	if got := rules[0].(map[string]any)["inbound"].([]any); len(got) != 2 || got[0] != "tproxy-in" || got[1] != "dns-in" {
		t.Fatalf("sniff rule should cover tproxy and DNS redirect inbounds: %#v", rules[0])
	}
	if rules[1].(map[string]any)["action"] != "hijack-dns" {
		t.Fatalf("DNS route rule should use hijack-dns action: %#v", rules[1])
	}
	localRule := rules[2].(map[string]any)
	if localRule["action"] != "reject" {
		t.Fatalf("local TPROXY route should reject instead of looping: %#v", localRule)
	}
}

func TestRenderOmitsClashAPIByDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if experimental, ok := rendered["experimental"]; ok {
		t.Fatalf("clash API must be production-off by default, got experimental=%#v", experimental)
	}
}

func TestRenderAppliesDNSIPv6Mode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	cfg.IPv6.Mode = "disable"

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	dns := rendered["dns"].(map[string]any)
	if dns["strategy"] != "ipv4_only" {
		t.Fatalf("IPv6 disabled should render ipv4_only DNS strategy: %#v", dns)
	}
	firstRule := dns["rules"].([]any)[0].(map[string]any)
	if firstRule["action"] != "predefined" {
		t.Fatalf("IPv6 disabled should synthesize empty AAAA responses: %#v", firstRule)
	}
	queryTypes := firstRule["query_type"].([]any)
	if len(queryTypes) != 1 || queryTypes[0] != "AAAA" {
		t.Fatalf("unexpected IPv6 DNS suppression rule: %#v", firstRule)
	}

	cfg.IPv6.Mode = "prefer_ipv4"
	data, err = RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	dns = rendered["dns"].(map[string]any)
	if dns["strategy"] != "prefer_ipv4" {
		t.Fatalf("prefer_ipv4 should render DNS strategy: %#v", dns)
	}
}

func TestRenderPreservesDNSIPv6ModeWithFakeIP(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	cfg.DNS.FakeIP = true
	cfg.IPv6.Mode = "disable"

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	dns := rendered["dns"].(map[string]any)
	if dns["strategy"] != "ipv4_only" {
		t.Fatalf("FakeIP should preserve IPv6 DNS strategy: %#v", dns)
	}
	rules := dns["rules"].([]any)
	if rules[0].(map[string]any)["action"] != "predefined" {
		t.Fatalf("AAAA suppression should run before FakeIP: %#v", rules)
	}
	if rules[1].(map[string]any)["server"] != "fakeip" {
		t.Fatalf("FakeIP DNS rule should remain after IPv6 suppression: %#v", rules)
	}
}

func TestRenderAddsClashAPIWhenExplicitlyEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Proxy.APIPort = 9090
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	experimental := rendered["experimental"].(map[string]any)
	clashAPI := experimental["clash_api"].(map[string]any)
	if clashAPI["external_controller"] != "127.0.0.1:9090" {
		t.Fatalf("unexpected clash API controller: %#v", clashAPI)
	}
}

func TestRenderSocksOutboundDoesNotInheritTransport(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "127.0.0.1"
	cfg.Node.Port = 1080
	cfg.Node.Protocol = "socks"
	cfg.Node.Username = "user"
	cfg.Node.Password = "pass"
	cfg.Node.SocksVersion = "5"
	cfg.Transport.Protocol = "reality"
	cfg.Transport.Extra = map[string]string{"security": "reality", "public_key": "bad"}

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	outbound := rendered["outbounds"].([]any)[0].(map[string]any)
	if outbound["type"] != "socks" {
		t.Fatalf("expected socks outbound, got %#v", outbound["type"])
	}
	if outbound["server"] != "127.0.0.1" || outbound["server_port"].(float64) != 1080 {
		t.Fatalf("unexpected socks server fields: %#v", outbound)
	}
	if outbound["version"] != "5" || outbound["username"] != "user" || outbound["password"] != "pass" {
		t.Fatalf("unexpected socks auth fields: %#v", outbound)
	}
	if _, ok := outbound["transport"]; ok {
		t.Fatalf("socks outbound must not inherit V2Ray transport: %#v", outbound)
	}
}

func TestRenderWireGuardOutboundWithoutKernelInterface(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "203.0.113.1"
	cfg.Node.Port = 51820
	cfg.Node.Protocol = "wireguard"
	cfg.Node.WGPrivateKey = "private-key"
	cfg.Node.WGPeerPublicKey = "peer-public-key"
	cfg.Node.WGPresharedKey = "psk"
	cfg.Node.WGLocalAddress = []string{"10.7.0.2/32", "fd42::2/128"}
	cfg.Node.WGMTU = 1280
	cfg.Node.WGReserved = []int{1, 2, 3}

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	outbound := rendered["outbounds"].([]any)[0].(map[string]any)
	if outbound["type"] != "wireguard" {
		t.Fatalf("expected wireguard outbound, got %#v", outbound)
	}
	if outbound["server"] != "203.0.113.1" || outbound["server_port"].(float64) != 51820 {
		t.Fatalf("unexpected wireguard endpoint: %#v", outbound)
	}
	if outbound["private_key"] != "private-key" || outbound["peer_public_key"] != "peer-public-key" {
		t.Fatalf("wireguard keys were not rendered: %#v", outbound)
	}
	if _, ok := outbound["transport"]; ok {
		t.Fatalf("wireguard outbound must not render v2ray transport: %#v", outbound)
	}
	if _, ok := outbound["interface_name"]; ok {
		t.Fatalf("wireguard outbound must not request a kernel interface: %#v", outbound)
	}
}

func TestRenderPanelNodesAsURLTestOutbounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = ""
	cfg.Node.UUID = ""
	cfg.Panel.ActiveNodeID = "second-node"
	cfg.Panel.Nodes = []json.RawMessage{
		json.RawMessage(`{
			"id":"first-node",
			"name":"First",
			"protocol":"VLESS",
			"server":"one.example",
			"port":443,
			"outbound":{
				"protocol":"vless",
				"settings":{
					"vnext":[{
						"address":"one.example",
						"port":443,
						"users":[{
							"id":"00000000-0000-0000-0000-000000000001",
							"encryption":"none"
						}]
					}]
				},
				"streamSettings":{
					"network":"tcp",
					"security":"tls",
					"tlsSettings":{"serverName":"one.example","fingerprint":"chrome"}
				}
			}
		}`),
		json.RawMessage(`{
			"id":"second-node",
			"name":"Second",
			"protocol":"SOCKS",
			"server":"127.0.0.1",
			"port":1080,
			"outbound":{
				"protocol":"socks",
				"settings":{
					"address":"127.0.0.1",
					"port":1080,
					"version":"5"
				}
			}
		}`),
	}

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	outbounds := rendered["outbounds"].([]any)
	if len(outbounds) != 5 {
		t.Fatalf("expected two nodes + urltest + selector + direct, got %#v", outbounds)
	}

	firstNode := outbounds[0].(map[string]any)
	if firstNode["domain_resolver"] != "direct-dns" {
		t.Fatalf("domain node should use direct-dns resolver: %#v", firstNode)
	}
	secondNode := outbounds[1].(map[string]any)
	if _, ok := secondNode["domain_resolver"]; ok {
		t.Fatalf("IP node should not set domain_resolver: %#v", secondNode)
	}

	urltest := outbounds[2].(map[string]any)
	if urltest["type"] != "urltest" || urltest["tag"] != "auto" {
		t.Fatalf("expected auto urltest outbound, got %#v", urltest)
	}
	tags := urltest["outbounds"].([]any)
	if len(tags) != 2 || tags[0] != "node-first-node" || tags[1] != "node-second-node" {
		t.Fatalf("unexpected urltest outbounds: %#v", tags)
	}
	selector := outbounds[3].(map[string]any)
	if selector["type"] != "selector" || selector["tag"] != "proxy" {
		t.Fatalf("expected proxy selector outbound, got %#v", selector)
	}
	selectorTags := selector["outbounds"].([]any)
	if len(selectorTags) != 3 || selectorTags[0] != "auto" || selectorTags[1] != "node-first-node" || selectorTags[2] != "node-second-node" {
		t.Fatalf("unexpected selector outbounds: %#v", selectorTags)
	}
	if selector["default"] != "node-second-node" {
		t.Fatalf("expected active node to be selector default, got %#v", selector["default"])
	}

	route := rendered["route"].(map[string]any)
	if route["final"] != "proxy" {
		t.Fatalf("route final should target selector proxy, got %#v", route["final"])
	}
}

func TestRenderPanelShadowsocksFallsBackToShareLinkSecret(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = ""
	cfg.Node.UUID = ""
	cfg.Panel.Nodes = []json.RawMessage{
		json.RawMessage(`{
			"id":"ss-node",
			"name":"SS",
			"protocol":"SHADOWSOCKS",
			"server":"127.0.0.1",
			"port":8388,
			"link":"ss://aes-128-gcm:secret@127.0.0.1:8388#SS",
			"outbound":{
				"protocol":"shadowsocks",
				"settings":{
					"servers":[{
						"address":"127.0.0.1",
						"port":8388,
						"method":"aes-128-gcm",
						"password":""
					}]
				}
			}
		}`),
	}

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	outbounds := rendered["outbounds"].([]any)
	proxy := outbounds[0].(map[string]any)
	if proxy["type"] != "shadowsocks" || proxy["password"] != "secret" {
		t.Fatalf("expected shadowsocks password from share link, got %#v", proxy)
	}
}

func TestRenderAutoSkipsInvalidPanelNode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = ""
	cfg.Node.UUID = ""
	cfg.Panel.Nodes = []json.RawMessage{
		json.RawMessage(`{
			"id":"bad-ss",
			"name":"Bad SS",
			"protocol":"SHADOWSOCKS",
			"server":"127.0.0.1",
			"port":8388,
			"outbound":{
				"protocol":"shadowsocks",
				"settings":{
					"servers":[{"address":"127.0.0.1","port":8388,"method":"aes-128-gcm","password":""}]
				}
			}
		}`),
		json.RawMessage(`{
			"id":"good-vless",
			"name":"Good VLESS",
			"protocol":"VLESS",
			"server":"one.example",
			"port":443,
			"outbound":{
				"protocol":"vless",
				"settings":{
					"vnext":[{
						"address":"one.example",
						"port":443,
						"users":[{"id":"00000000-0000-0000-0000-000000000001","encryption":"none"}]
					}]
				}
			}
		}`),
	}

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	outbounds := rendered["outbounds"].([]any)
	if len(outbounds) != 2 {
		t.Fatalf("expected one usable proxy plus direct, got %#v", outbounds)
	}
	proxy := outbounds[0].(map[string]any)
	if proxy["type"] != "vless" || proxy["tag"] != "proxy" {
		t.Fatalf("expected valid VLESS proxy after skipping invalid SS, got %#v", proxy)
	}
}

func TestRenderActiveInvalidPanelNodeFails(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = ""
	cfg.Node.UUID = ""
	cfg.Panel.ActiveNodeID = "bad-ss"
	cfg.Panel.Nodes = []json.RawMessage{
		json.RawMessage(`{
			"id":"bad-ss",
			"name":"Bad SS",
			"protocol":"SHADOWSOCKS",
			"server":"127.0.0.1",
			"port":8388,
			"outbound":{
				"protocol":"shadowsocks",
				"settings":{
					"servers":[{"address":"127.0.0.1","port":8388,"method":"aes-128-gcm","password":""}]
				}
			}
		}`),
		json.RawMessage(`{
			"id":"good-vless",
			"name":"Good VLESS",
			"protocol":"VLESS",
			"server":"one.example",
			"port":443,
			"outbound":{
				"protocol":"vless",
				"settings":{
					"vnext":[{
						"address":"one.example",
						"port":443,
						"users":[{"id":"00000000-0000-0000-0000-000000000001","encryption":"none"}]
					}]
				}
			}
		}`),
	}

	_, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err == nil || !strings.Contains(err.Error(), "active node") {
		t.Fatalf("expected active invalid node error, got %v", err)
	}
}

func TestRenderPanelNodeGroupsAsSelectorOutbounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = ""
	cfg.Node.UUID = ""
	cfg.Panel.Nodes = []json.RawMessage{
		json.RawMessage(`{
			"id":"first-node",
			"name":"First",
			"group":"Europe",
			"protocol":"SOCKS",
			"server":"127.0.0.1",
			"port":1081,
			"outbound":{
				"protocol":"socks",
				"settings":{"address":"127.0.0.1","port":1081,"version":"5"}
			}
		}`),
		json.RawMessage(`{
			"id":"second-node",
			"name":"Second",
			"group":"Europe",
			"protocol":"SOCKS",
			"server":"127.0.0.1",
			"port":1082,
			"outbound":{
				"protocol":"socks",
				"settings":{"address":"127.0.0.1","port":1082,"version":"5"}
			}
		}`),
	}

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	byTag := map[string]map[string]any{}
	for _, rawOutbound := range rendered["outbounds"].([]any) {
		outbound := rawOutbound.(map[string]any)
		if tag, ok := outbound["tag"].(string); ok {
			byTag[tag] = outbound
		}
	}

	globalSelector := byTag["proxy"]
	globalTags := globalSelector["outbounds"].([]any)
	if len(globalTags) != 4 || globalTags[0] != "auto" || globalTags[1] != "node-first-node" || globalTags[2] != "node-second-node" || globalTags[3] != "group-europe" {
		t.Fatalf("global selector should expose group selector after raw node tags: %#v", globalTags)
	}

	groupAuto := byTag["group-europe-auto"]
	if groupAuto["type"] != "urltest" {
		t.Fatalf("expected group urltest outbound, got %#v", groupAuto)
	}
	groupAutoTags := groupAuto["outbounds"].([]any)
	if len(groupAutoTags) != 2 || groupAutoTags[0] != "node-first-node" || groupAutoTags[1] != "node-second-node" {
		t.Fatalf("unexpected group urltest members: %#v", groupAutoTags)
	}

	groupSelector := byTag["group-europe"]
	if groupSelector["type"] != "selector" || groupSelector["default"] != "group-europe-auto" {
		t.Fatalf("unexpected group selector: %#v", groupSelector)
	}
	groupSelectorTags := groupSelector["outbounds"].([]any)
	if len(groupSelectorTags) != 3 || groupSelectorTags[0] != "group-europe-auto" || groupSelectorTags[1] != "node-first-node" || groupSelectorTags[2] != "node-second-node" {
		t.Fatalf("unexpected group selector members: %#v", groupSelectorTags)
	}
}

func TestRenderAppGroupRouteRules(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = ""
	cfg.Node.UUID = ""
	cfg.Routing.CustomDirect = []string{"example.org"}
	cfg.Routing.AlwaysDirectApps = []string{"com.privstack.panel"}
	cfg.Apps.AppGroups = map[string]string{
		"com.chat.app":  "Europe",
		"com.video.app": "Europe",
		"com.unknown":   "Missing",
	}
	cfg.Panel.Nodes = []json.RawMessage{
		json.RawMessage(`{
			"id":"first-node",
			"name":"First",
			"group":"Europe",
			"protocol":"SOCKS",
			"server":"127.0.0.1",
			"port":1081,
			"outbound":{
				"protocol":"socks",
				"settings":{"address":"127.0.0.1","port":1081,"version":"5"}
			}
		}`),
		json.RawMessage(`{
			"id":"second-node",
			"name":"Second",
			"group":"Europe",
			"protocol":"SOCKS",
			"server":"127.0.0.1",
			"port":1082,
			"outbound":{
				"protocol":"socks",
				"settings":{"address":"127.0.0.1","port":1082,"version":"5"}
			}
		}`),
	}

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	rules := rendered["route"].(map[string]any)["rules"].([]any)
	var directRule map[string]any
	var groupRule map[string]any
	for _, rawRule := range rules {
		rule := rawRule.(map[string]any)
		switch rule["outbound"] {
		case "direct":
			if packages, ok := rule["package_name"].([]any); ok && len(packages) == 1 && packages[0] == "com.privstack.panel" {
				directRule = rule
			}
		case "group-europe":
			groupRule = rule
		}
	}
	if directRule == nil {
		t.Fatalf("expected always-direct package route, got %#v", rules)
	}
	if groupRule == nil {
		t.Fatalf("expected app group route, got %#v", rules)
	}
	packages := groupRule["package_name"].([]any)
	if len(packages) != 2 || packages[0] != "com.chat.app" || packages[1] != "com.video.app" {
		t.Fatalf("unexpected app group packages: %#v", packages)
	}
}

func TestRenderOmitsInternalStatusHTTPInboundByDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	inbounds := rendered["inbounds"].([]any)
	for _, rawInbound := range inbounds {
		inbound := rawInbound.(map[string]any)
		if inbound["tag"] == "status-http-in" {
			t.Fatalf("status-http-in inbound must be disabled by default: %#v", inbound)
		}
	}
}

func TestRenderAddsInternalStatusHTTPInboundWhenExplicitlyEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	cfg.Panel.Inbounds = json.RawMessage(`{"httpPort":10809}`)

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	inbounds := rendered["inbounds"].([]any)
	found := false
	for _, rawInbound := range inbounds {
		inbound := rawInbound.(map[string]any)
		if inbound["tag"] == "status-http-in" {
			found = true
			if inbound["type"] != "http" {
				t.Fatalf("unexpected helper inbound type: %#v", inbound)
			}
			if inbound["listen"] != "127.0.0.1" {
				t.Fatalf("helper inbound must stay localhost-only: %#v", inbound)
			}
			if inbound["listen_port"].(float64) != 10809 {
				t.Fatalf("unexpected helper inbound port: %#v", inbound)
			}
		}
	}
	if !found {
		t.Fatal("status-http-in inbound was not rendered")
	}
}

func TestRenderAddsInternalStatusSOCKSInboundWhenExplicitlyEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	cfg.Panel.Inbounds = json.RawMessage(`{"socksPort":10808}`)

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	inbounds := rendered["inbounds"].([]any)
	found := false
	for _, rawInbound := range inbounds {
		inbound := rawInbound.(map[string]any)
		if inbound["tag"] == "status-socks-in" {
			found = true
			if inbound["type"] != "socks" {
				t.Fatalf("unexpected helper inbound type: %#v", inbound)
			}
			if inbound["listen"] != "127.0.0.1" {
				t.Fatalf("helper inbound must stay localhost-only by default: %#v", inbound)
			}
			if inbound["listen_port"].(float64) != 10808 {
				t.Fatalf("unexpected helper inbound port: %#v", inbound)
			}
		}
	}
	if !found {
		t.Fatal("status-socks-in inbound was not rendered")
	}
}

func TestRenderHelperInboundsHonorAllowLAN(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	cfg.Panel.Inbounds = json.RawMessage(`{"httpPort":10809,"socksPort":10808,"allowLan":true}`)

	var rendered map[string]any
	data, err := RenderSingboxConfig(cfg, cfg.ResolveProfile())
	if err != nil {
		t.Fatalf("render config: %v", err)
	}
	if err := json.Unmarshal(data, &rendered); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	inbounds := rendered["inbounds"].([]any)
	for _, wantTag := range []string{"status-http-in", "status-socks-in"} {
		found := false
		for _, rawInbound := range inbounds {
			inbound := rawInbound.(map[string]any)
			if inbound["tag"] != wantTag {
				continue
			}
			found = true
			if inbound["listen"] != "0.0.0.0" {
				t.Fatalf("%s did not honor allowLan: %#v", wantTag, inbound)
			}
		}
		if !found {
			t.Fatalf("%s inbound was not rendered", wantTag)
		}
	}
}

func TestRenderSkipsStalePanelNodes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = "fallback.example"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	cfg.Panel.ActiveNodeID = "stale-node"
	cfg.Panel.Nodes = []json.RawMessage{json.RawMessage(`{
		"id":"stale-node",
		"name":"Removed by subscription",
		"protocol":"VLESS",
		"server":"stale.example",
		"port":443,
		"stale":true,
		"outbound":{
			"protocol":"vless",
			"settings":{"vnext":[{"address":"stale.example","port":443,"users":[{"id":"11111111-1111-1111-1111-111111111111","encryption":"none"}]}]}
		}
	}`)}

	profiles := ProfilesFromPanelNodes(cfg)
	if len(profiles) != 0 {
		t.Fatalf("stale panel nodes must not become runtime profiles: %#v", profiles)
	}
}
