package config

import (
	"encoding/json"
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
	}

	for _, rawInbound := range rendered["inbounds"].([]any) {
		inbound := rawInbound.(map[string]any)
		if _, ok := inbound["sniff"]; ok {
			t.Fatalf("removed inbound sniff field rendered: %#v", inbound)
		}
		if _, ok := inbound["sniff_override_destination"]; ok {
			t.Fatalf("removed inbound sniff_override_destination field rendered: %#v", inbound)
		}
	}

	rules := rendered["route"].(map[string]any)["rules"].([]any)
	if rules[0].(map[string]any)["action"] != "sniff" {
		t.Fatalf("first route rule should sniff tproxy traffic: %#v", rules[0])
	}
	if rules[1].(map[string]any)["action"] != "hijack-dns" {
		t.Fatalf("DNS route rule should use hijack-dns action: %#v", rules[1])
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

func TestRenderPanelNodesAsURLTestOutbounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Node.Address = ""
	cfg.Node.UUID = ""
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
	if len(outbounds) != 4 {
		t.Fatalf("expected two nodes + urltest + direct, got %#v", outbounds)
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
	if urltest["type"] != "urltest" || urltest["tag"] != "proxy" {
		t.Fatalf("expected proxy urltest outbound, got %#v", urltest)
	}
	tags := urltest["outbounds"].([]any)
	if len(tags) != 2 || tags[0] != "node-first-node" || tags[1] != "node-second-node" {
		t.Fatalf("unexpected urltest outbounds: %#v", tags)
	}

	route := rendered["route"].(map[string]any)
	if route["final"] != "proxy" {
		t.Fatalf("route final should target urltest proxy, got %#v", route["final"])
	}
}
