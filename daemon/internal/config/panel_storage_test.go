package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRejectsNewerSchemaVersion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SchemaVersion = CurrentSchemaVersion + 1
	if err := cfg.Validate(); err == nil {
		t.Fatalf("newer schema version should be rejected")
	}
}

func TestValidateChecksProfileProjectionSchema(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profile.Nodes = []json.RawMessage{
		json.RawMessage(`{"id":"node-1","protocol":"vless","server":"example.com","port":443,"stale":true,"source":{"type":"MANUAL"}}`),
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("config validation should reject invalid profile projection schema")
	}
}

func TestNormalizeProfileNodesAddsManualSource(t *testing.T) {
	profile := defaultProfileProjectionConfig()
	profile.Nodes = []json.RawMessage{
		json.RawMessage(`{"id":"node-1","protocol":"vless","server":"example.com","port":443}`),
	}

	normalized := normalizeProfileProjectionConfig(profile)
	if err := validateProfileProjectionConfig(normalized); err != nil {
		t.Fatalf("normalized profile projection should validate: %v", err)
	}

	var node map[string]json.RawMessage
	if err := json.Unmarshal(normalized.Nodes[0], &node); err != nil {
		t.Fatalf("parse normalized node: %v", err)
	}
	var source ProfileNodeSourceConfig
	if err := json.Unmarshal(node["source"], &source); err != nil {
		t.Fatalf("parse normalized source: %v", err)
	}
	if source.Type != "MANUAL" {
		t.Fatalf("expected manual source default, got %#v", source)
	}
}

func TestNormalizeProfileNodesDoesNotBackfillLegacyStaleProvider(t *testing.T) {
	profile := defaultProfileProjectionConfig()
	profile.Nodes = []json.RawMessage{
		json.RawMessage(`{"id":"node-1","protocol":"vless","server":"example.com","port":443,"stale":true}`),
	}

	normalized := normalizeProfileProjectionConfig(profile)
	if err := validateProfileProjectionConfig(normalized); err == nil {
		t.Fatalf("legacy stale node without provider key should be rejected")
	}
}

func TestValidateProfileProjectionConfigRejectsManualStaleNode(t *testing.T) {
	profile := defaultProfileProjectionConfig()
	profile.Nodes = []json.RawMessage{
		json.RawMessage(`{"id":"node-1","protocol":"vless","server":"example.com","port":443,"stale":true,"source":{"type":"MANUAL"}}`),
	}

	if err := validateProfileProjectionConfig(profile); err == nil {
		t.Fatalf("manual stale node should be rejected")
	}
}

func TestNormalizeProfileSubscriptionsBackfillsProviderKey(t *testing.T) {
	profile := defaultProfileProjectionConfig()
	profile.Subscriptions = []json.RawMessage{
		json.RawMessage(`{"url":"HTTPS://Example.com/Sub","lastFetchedAt":1000,"lastSeenNodeCount":2}`),
	}

	normalized := normalizeProfileProjectionConfig(profile)
	if err := validateProfileProjectionConfig(normalized); err != nil {
		t.Fatalf("normalized subscriptions should validate: %v", err)
	}

	var subscription ProfileSubscriptionConfig
	if err := json.Unmarshal(normalized.Subscriptions[0], &subscription); err != nil {
		t.Fatalf("parse normalized subscription: %v", err)
	}
	if subscription.ProviderKey != "https://example.com/sub" {
		t.Fatalf("expected provider key from URL, got %#v", subscription)
	}
}

func TestNormalizeProfileBackfillsSubscriptionsFromNodes(t *testing.T) {
	profile := defaultProfileProjectionConfig()
	profile.Nodes = []json.RawMessage{
		json.RawMessage(`{"id":"node-1","protocol":"vless","server":"example.com","port":443,"source":{"type":"SUBSCRIPTION","url":"https://example.com/sub","providerKey":"https://example.com/sub","lastSeenAt":1000}}`),
		json.RawMessage(`{"id":"node-2","protocol":"vless","server":"old.example","port":443,"stale":true,"source":{"type":"SUBSCRIPTION","url":"https://example.com/sub","providerKey":"https://example.com/sub","lastSeenAt":900}}`),
	}

	normalized := normalizeProfileProjectionConfig(profile)
	if err := validateProfileProjectionConfig(normalized); err != nil {
		t.Fatalf("backfilled subscription should validate: %v", err)
	}
	if len(normalized.Subscriptions) != 1 {
		t.Fatalf("expected one backfilled subscription, got %d", len(normalized.Subscriptions))
	}

	var subscription ProfileSubscriptionConfig
	if err := json.Unmarshal(normalized.Subscriptions[0], &subscription); err != nil {
		t.Fatalf("parse backfilled subscription: %v", err)
	}
	if subscription.ProviderKey != "https://example.com/sub" ||
		subscription.LastSeenNodeCount != 2 ||
		subscription.StaleNodeCount != 1 ||
		subscription.LastFetchedAt != 1000 {
		t.Fatalf("unexpected backfilled subscription: %#v", subscription)
	}
}

func TestValidateProfileProjectionConfigRejectsSubscriptionWithoutProviderKey(t *testing.T) {
	profile := defaultProfileProjectionConfig()
	profile.Subscriptions = []json.RawMessage{
		json.RawMessage(`{"url":"","lastFetchedAt":1000}`),
	}

	if err := validateProfileProjectionConfig(profile); err == nil {
		t.Fatalf("subscription without provider key should be rejected")
	}
}

func TestWriteFileAtomicReplacesFileWithoutLeavingTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, []byte("new\n"), 0600, "config"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("atomic replacement wrote %q", string(data))
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file should be gone after atomic write, stat err=%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("atomic replacement mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestLoadRejectsLegacyNodeArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := []byte(`{
		"schema_version": 5,
		"proxy": {"mode":"tproxy","tproxy_port":10853,"dns_port":10856,"gid":23333,"mark":8227,"api_port":0},
		"transport": {"protocol":"reality","tls_server":"","fingerprint":"chrome","extra":{}},
		"node": [],
		"runtime_v2": {"backend_kind":"ROOT_TPROXY","fallback_policy":"OFFER_RESET"},
		"routing": {"mode":"whitelist","bypass_lan":true,"bypass_china":false,"bypass_russia":false,"block_ads":false,"custom_direct":[],"custom_proxy":[],"custom_block":[],"geoip_path":"/data/adb/privstack/data/geoip.db","geosite_path":"/data/adb/privstack/data/geosite.db"},
		"apps": {"mode":"whitelist","list":[],"app_groups":{}},
		"dns": {"hijack_per_uid":true,"proxy_dns":"https://1.1.1.1/dns-query","direct_dns":"https://dns.google/dns-query","bootstrap_ip":"1.1.1.1","block_quic_dns":true,"fake_ip":false},
		"ipv6": {"mode":"mirror"},
		"sharing": {"enabled":false},
		"health": {"enabled":true,"interval_sec":30,"threshold":3,"check_url":"https://www.gstatic.com/generate_204","timeout_sec":5,"dns_is_hard_readiness":false},
		"rescue": {"enabled":true,"max_attempts":3,"cooldown_sec":60},
		"autostart": false
	}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatalf("legacy node array must not be normalized")
	}
}
