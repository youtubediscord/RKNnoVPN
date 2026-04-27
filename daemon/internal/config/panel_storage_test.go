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

func TestNormalizeProfileNodesBackfillsLegacyStaleSource(t *testing.T) {
	profile := defaultProfileProjectionConfig()
	profile.Nodes = []json.RawMessage{
		json.RawMessage(`{"id":"node-1","protocol":"vless","server":"example.com","port":443,"stale":true}`),
	}

	normalized := normalizeProfileProjectionConfig(profile)
	if err := validateProfileProjectionConfig(normalized); err != nil {
		t.Fatalf("legacy stale node should be normalized to subscription source: %v", err)
	}

	var node map[string]json.RawMessage
	if err := json.Unmarshal(normalized.Nodes[0], &node); err != nil {
		t.Fatalf("parse normalized node: %v", err)
	}
	var source ProfileNodeSourceConfig
	if err := json.Unmarshal(node["source"], &source); err != nil {
		t.Fatalf("parse normalized source: %v", err)
	}
	if source.Type != "SUBSCRIPTION" || source.ProviderKey == "" {
		t.Fatalf("expected legacy subscription source, got %#v", source)
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
