package profile

import (
	"encoding/json"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func TestProfileDocumentMigratesConfigAndPanelWithoutDroppingState(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Profile.ID = "main"
	cfg.Profile.Name = "Primary"
	cfg.Profile.ActiveNodeID = "node-a"
	cfg.Profile.Nodes = []json.RawMessage{
		json.RawMessage(`{"id":"node-a","name":"A","protocol":"VLESS","server":"example.com","port":443,"outbound":{"protocol":"vless","settings":{"vnext":[{"address":"example.com","port":443,"users":[{"id":"uuid-a"}]}]}},"source":{"type":"MANUAL"}}`),
		json.RawMessage(`{"id":"node-old","name":"Old","protocol":"TROJAN","server":"old.example","port":443,"stale":true,"outbound":{"protocol":"trojan","settings":{"servers":[{"address":"old.example","port":443,"password":"p"}]}},"source":{"type":"SUBSCRIPTION","url":"https://sub.example/list","providerKey":"https://sub.example/list","lastSeenAt":1}}`),
	}
	cfg.Profile.Subscriptions = []json.RawMessage{
		json.RawMessage(`{"providerKey":"https://sub.example/list","url":"https://sub.example/list","lastFetchedAt":1,"lastSeenNodeCount":1,"staleNodeCount":1}`),
	}
	cfg.Profile.Inbounds = json.RawMessage(`{"socksPort":10808,"httpPort":10809,"allowLan":false}`)

	doc := FromConfig(cfg)
	if doc.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("profile schema version not exposed: got %d want %d", doc.SchemaVersion, CurrentSchemaVersion)
	}
	if doc.ID != "main" || doc.Name != "Primary" || doc.ActiveNodeID != "node-a" {
		t.Fatalf("profile identity not migrated: %#v", doc)
	}
	if len(doc.Nodes) != 2 || len(doc.Subscriptions) != 1 {
		t.Fatalf("profile collections not migrated: nodes=%d subscriptions=%d", len(doc.Nodes), len(doc.Subscriptions))
	}
	if !doc.Nodes[1].Stale || doc.Nodes[1].Source.Type != "SUBSCRIPTION" {
		t.Fatalf("stale subscription metadata was not preserved: %#v", doc.Nodes[1])
	}
	if doc.Inbounds.SocksPort != 10808 || doc.Inbounds.HTTPPort != 10809 || doc.Inbounds.AllowLAN {
		t.Fatalf("inbounds not migrated safely: %#v", doc.Inbounds)
	}
}

func TestFromConfigDoesNotCreateNodeFromLegacyConfigOnlyState(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Node.Address = "legacy.example"
	cfg.Node.Port = 443
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	cfg.Profile.Nodes = nil
	cfg.Profile.ActiveNodeID = ""

	doc := FromConfig(cfg)
	if len(doc.Nodes) != 0 || doc.ActiveNodeID != "" {
		t.Fatalf("legacy config-only node must not be projected into profile: %#v", doc)
	}
}

func TestDecodeStrictDocumentRejectsUnknownProfileFields(t *testing.T) {
	raw := []byte(`{
		"profileSchemaVersion": 2,
		"id": "main",
		"name": "Primary",
		"nodes": [],
		"runtime": {},
		"routing": {"mode":"PER_APP"},
		"dns": {"remoteDns":"https://1.1.1.1/dns-query","directDns":"https://dns.google/dns-query","bootstrapIp":"1.1.1.1","ipv6Mode":"MIRROR","blockQuic":true,"fakeDns":false},
		"health": {"enabled":true,"intervalSec":30,"threshold":3,"checkUrl":"https://www.gstatic.com/generate_204","timeoutSec":5,"dnsIsHardReadiness":false},
		"sharing": {"enabled":false},
		"tun": {"enabled":false,"mtu":9000,"ipv4Address":"172.19.0.1/30","ipv6":false,"autoRoute":true,"strictRoute":true},
		"inbounds": {"socksPort":0,"httpPort":0,"allowLan":false},
		"unexpected": true
	}`)

	if _, err := DecodeStrictDocument(raw); err == nil {
		t.Fatalf("unknown profile field should be rejected")
	}
}

func TestDecodeStrictDocumentRejectsUnknownNodeFieldsButAllowsOutboundAndExtra(t *testing.T) {
	raw := []byte(`{
		"profileSchemaVersion": 2,
		"id": "main",
		"name": "Primary",
		"activeNodeId": "node-1",
		"nodes": [{
			"id":"node-1",
			"name":"Node",
			"protocol":"vless",
			"server":"example.com",
			"port":443,
			"outbound":{"protocol":"vless","settings":{"x":1}},
			"source":{"type":"MANUAL"},
			"extra":{"ui":"ok"},
			"legacyField":"nope"
		}],
		"runtime": {},
		"routing": {"mode":"PER_APP"},
		"dns": {"remoteDns":"https://1.1.1.1/dns-query","directDns":"https://dns.google/dns-query","bootstrapIp":"1.1.1.1","ipv6Mode":"MIRROR","blockQuic":true,"fakeDns":false},
		"health": {"enabled":true,"intervalSec":30,"threshold":3,"checkUrl":"https://www.gstatic.com/generate_204","timeoutSec":5,"dnsIsHardReadiness":false},
		"sharing": {"enabled":false},
		"tun": {"enabled":false,"mtu":9000,"ipv4Address":"172.19.0.1/30","ipv6":false,"autoRoute":true,"strictRoute":true},
		"inbounds": {"socksPort":0,"httpPort":0,"allowLan":false}
	}`)

	if _, err := DecodeStrictDocument(raw); err == nil {
		t.Fatalf("unknown node field should be rejected")
	}
}

func TestProfileValidationRejectsAllowLanAndRepairsStaleActive(t *testing.T) {
	doc := Document{
		ID:           "main",
		Name:         "Primary",
		ActiveNodeID: "stale",
		Inbounds:     InboundsConfig{AllowLAN: true},
		Nodes: []Node{
			{ID: "stale", Name: "Stale", Protocol: "vless", Server: "old.example", Port: 443, Stale: true, Outbound: json.RawMessage(`{}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "sub"}},
			{ID: "live", Name: "Live", Protocol: "vless", Server: "live.example", Port: 443, Outbound: json.RawMessage(`{}`), Source: NodeSource{Type: "MANUAL"}},
		},
	}
	if _, _, err := Normalize(doc); err == nil {
		t.Fatal("expected allowLan validation error")
	}
	doc.Inbounds.AllowLAN = false
	normalized, warnings, err := Normalize(doc)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if normalized.ActiveNodeID != "live" {
		t.Fatalf("active stale node was not repaired: %#v", normalized.ActiveNodeID)
	}
	if len(warnings) == 0 || warnings[0].Code != "active_node_repaired" {
		t.Fatalf("active repair warning missing: %#v", warnings)
	}
}

func TestSubscriptionMergePreservesManualAndMarksRemovedStale(t *testing.T) {
	current := Document{
		ID:   "main",
		Name: "Primary",
		Nodes: []Node{
			{ID: "manual", Name: "Manual", Protocol: "vless", Server: "shared.example", Port: 443, Outbound: json.RawMessage(`{"id":"manual"}`), Source: NodeSource{Type: "MANUAL"}},
			{ID: "old", Name: "Old", Protocol: "vless", Server: "old.example", Port: 443, Outbound: json.RawMessage(`{"id":"old"}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "sub"}},
			{ID: "same", Name: "Same", Protocol: "vless", Server: "same.example", Port: 443, Outbound: json.RawMessage(`{"id":"old-outbound"}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "sub"}},
		},
	}
	incoming := []Node{
		{ID: "new", Name: "New", Protocol: "vless", Server: "new.example", Port: 443, Outbound: json.RawMessage(`{"id":"new"}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "sub"}},
		{ID: "same", Name: "Same updated", Protocol: "vless", Server: "same.example", Port: 443, Outbound: json.RawMessage(`{"id":"new-outbound"}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "sub"}},
		{ID: "manual-lookalike", Name: "Subscription twin", Protocol: "vless", Server: "shared.example", Port: 443, Outbound: json.RawMessage(`{"id":"manual"}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "sub"}},
	}
	next, stats := MergeNodes(current, incoming, true)
	if len(next.Nodes) != 5 {
		t.Fatalf("unexpected node count after merge: %#v", next.Nodes)
	}
	if next.Nodes[0].ID != "manual" || next.Nodes[0].Stale {
		t.Fatalf("manual node was not preserved: %#v", next.Nodes[0])
	}
	if !next.Nodes[1].Stale {
		t.Fatalf("removed subscription node was not marked stale: %#v", next.Nodes[1])
	}
	if next.Nodes[2].ID != "same" || string(next.Nodes[2].Outbound) != `{"id":"new-outbound"}` {
		t.Fatalf("subscription node was not updated in place: %#v", next.Nodes[2])
	}
	if stats["added"] != 2 || stats["updated"] != 1 || stats["stale"] != 1 {
		t.Fatalf("unexpected merge stats: %#v", stats)
	}
}

func TestNormalizeSubscriptionsRecomputesProviderCounts(t *testing.T) {
	doc := Document{
		ID: "main",
		Subscriptions: []Subscription{
			{ProviderKey: "sub", URL: "https://sub.example/list", LastSeenNodeCount: 99, StaleNodeCount: 99},
		},
		Nodes: []Node{
			{ID: "live", Name: "Live", Protocol: "vless", Server: "live.example", Port: 443, Outbound: json.RawMessage(`{}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "sub", URL: "https://sub.example/list"}},
			{ID: "stale", Name: "Stale", Protocol: "vless", Server: "old.example", Port: 443, Stale: true, Outbound: json.RawMessage(`{}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "sub", URL: "https://sub.example/list"}},
		},
	}

	normalized, _, err := Normalize(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Subscriptions) != 1 {
		t.Fatalf("expected one subscription, got %#v", normalized.Subscriptions)
	}
	if normalized.Subscriptions[0].LastSeenNodeCount != 1 || normalized.Subscriptions[0].StaleNodeCount != 1 {
		t.Fatalf("subscription counts were not recomputed: %#v", normalized.Subscriptions[0])
	}
}

func TestMergeSubscriptionNodesCanMarkProviderEmptyRefreshStale(t *testing.T) {
	current := Document{
		ID:           "main",
		Name:         "Primary",
		ActiveNodeID: "sub-live",
		Nodes: []Node{
			{ID: "manual", Name: "Manual", Protocol: "vless", Server: "manual.example", Port: 443, Outbound: json.RawMessage(`{}`), Source: NodeSource{Type: "MANUAL"}},
			{ID: "sub-live", Name: "Subscription", Protocol: "vless", Server: "sub.example", Port: 443, Outbound: json.RawMessage(`{}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "https://sub.example/list"}},
			{ID: "other-provider", Name: "Other", Protocol: "vless", Server: "other.example", Port: 443, Outbound: json.RawMessage(`{}`), Source: NodeSource{Type: "SUBSCRIPTION", ProviderKey: "https://other.example/list"}},
		},
	}

	next, stats := MergeSubscriptionNodes(current, Subscription{
		ProviderKey: "https://sub.example/list",
		URL:         "https://sub.example/list",
	}, nil)

	if stats["stale"] != 1 {
		t.Fatalf("expected one stale node, got stats %#v", stats)
	}
	if !next.Nodes[1].Stale {
		t.Fatalf("provider node was not marked stale: %#v", next.Nodes[1])
	}
	if next.Nodes[0].Stale || next.Nodes[2].Stale {
		t.Fatalf("empty refresh touched nodes outside provider scope: %#v", next.Nodes)
	}
	if next.ActiveNodeID != "manual" {
		t.Fatalf("active stale node was not repaired to live manual node: %q", next.ActiveNodeID)
	}
}
