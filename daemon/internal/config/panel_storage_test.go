package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigratesEmbeddedPanelToSidecar(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	initial := map[string]any{
		"proxy": map[string]any{
			"mode":        "tproxy",
			"tproxy_port": 10853,
			"dns_port":    10856,
			"gid":         23333,
			"mark":        8227,
			"api_port":    9090,
		},
		"transport": map[string]any{
			"protocol":    "reality",
			"tls_server":  "",
			"fingerprint": "chrome",
			"extra":       map[string]any{},
		},
		"node": map[string]any{
			"address":  "",
			"port":     443,
			"protocol": "vless",
			"uuid":     "",
			"flow":     "",
		},
		"panel": map[string]any{
			"id":             "default",
			"name":           "Default",
			"active_node_id": "node-1",
			"nodes": []any{
				map[string]any{
					"id":       "node-1",
					"name":     "Test",
					"protocol": "vless",
					"server":   "example.com",
					"port":     443,
					"outbound": map[string]any{
						"protocol": "vless",
						"settings": map[string]any{
							"vnext": []any{
								map[string]any{
									"address": "example.com",
									"port":    443,
									"users": []any{
										map[string]any{
											"id":         "11111111-1111-1111-1111-111111111111",
											"encryption": "none",
										},
									},
								},
							},
						},
						"streamSettings": map[string]any{
							"network":  "tcp",
							"security": "reality",
							"realitySettings": map[string]any{
								"serverName": "example.com",
								"publicKey":  "pubkey",
								"shortId":    "abcd",
							},
						},
					},
				},
			},
		},
		"runtime_v2": map[string]any{
			"backend_kind":    "ROOT_TPROXY",
			"fallback_policy": "OFFER_RESET",
		},
		"routing": map[string]any{
			"mode":         "whitelist",
			"bypass_lan":   true,
			"geoip_path":   "/data/adb/privstack/data/geoip.db",
			"geosite_path": "/data/adb/privstack/data/geosite.db",
		},
		"apps": map[string]any{
			"mode": "whitelist",
			"list": []any{},
		},
		"dns": map[string]any{
			"hijack_per_uid": true,
			"proxy_dns":      "https://1.1.1.1/dns-query",
			"direct_dns":     "https://dns.google/dns-query",
			"bootstrap_ip":   "1.1.1.1",
			"block_quic_dns": true,
			"fake_ip":        false,
		},
		"ipv6": map[string]any{
			"mode": "mirror",
		},
		"health": map[string]any{
			"enabled":      true,
			"interval_sec": 30,
			"threshold":    3,
			"check_url":    "https://www.gstatic.com/generate_204",
			"timeout_sec":  5,
		},
		"rescue": map[string]any{
			"enabled":      true,
			"max_attempts": 3,
			"cooldown_sec": 60,
		},
		"autostart": true,
	}
	data, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("marshal initial config: %v", err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Panel.ActiveNodeID != "node-1" {
		t.Fatalf("expected migrated active node, got %q", cfg.Panel.ActiveNodeID)
	}
	if cfg.Node.Address != "example.com" {
		t.Fatalf("expected synced node address from panel, got %q", cfg.Node.Address)
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("legacy config should be migrated to schema %d, got %d", CurrentSchemaVersion, cfg.SchemaVersion)
	}

	panelPath := PanelPath(configPath)
	panelData, err := os.ReadFile(panelPath)
	if err != nil {
		t.Fatalf("expected migrated panel sidecar: %v", err)
	}
	if !json.Valid(panelData) {
		t.Fatalf("panel sidecar is not valid JSON: %s", string(panelData))
	}

	savedConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	if string(savedConfig) == "" || json.Valid(savedConfig) == false {
		t.Fatalf("rewritten config is invalid JSON")
	}
	if containsPanelKey(savedConfig) {
		t.Fatalf("rewritten config still contains embedded panel: %s", string(savedConfig))
	}
}

func TestValidateRejectsNewerSchemaVersion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SchemaVersion = CurrentSchemaVersion + 1
	if err := cfg.Validate(); err == nil {
		t.Fatalf("newer schema version should be rejected")
	}
}

func TestLoadUsesAuthoritativeEmptySidecarToClearNode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	cfg := DefaultConfig()
	cfg.Node.Address = "legacy.example"
	cfg.Node.Protocol = "vless"
	cfg.Node.UUID = "11111111-1111-1111-1111-111111111111"
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := SavePanel(PanelPath(configPath), DefaultPanelConfig()); err != nil {
		t.Fatalf("save panel: %v", err)
	}

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if loaded.Node.Address != "" {
		t.Fatalf("expected authoritative empty panel to clear node address, got %q", loaded.Node.Address)
	}
}

func containsPanelKey(data []byte) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw["panel"]
	return ok
}
