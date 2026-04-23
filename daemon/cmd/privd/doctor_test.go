package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestDoctorRedactsSensitiveJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "singbox.json")
	raw := []byte(`{
		"outbounds": [{
			"server": "proxy.example.com",
			"server_port": 443,
			"uuid": "00000000-0000-0000-0000-000000000000",
			"password": "secret",
			"tls": {"server_name": "cdn.example.com", "reality": {"public_key": "pubsecretvalue", "short_id": "sidsecretvalue"}}
		}]
	}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	section := readRedactedJSONFile(path)
	if section.Error != "" {
		t.Fatalf("unexpected parse error: %s", section.Error)
	}
	text := redactDiagnosticText(mustMarshalForTest(t, section.Value))
	for _, secret := range []string{"proxy.example.com", "00000000-0000-0000-0000-000000000000", "secret", "cdn.example.com", "pubsecretvalue", "sidsecretvalue"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q was not redacted from %s", secret, text)
		}
	}
	if !strings.Contains(text, `"server_port":443`) {
		t.Fatalf("non-secret server_port should remain available, got %s", text)
	}
}

func TestSupportedRPCMethodsAdvertiseCompatibilityAliases(t *testing.T) {
	methods := supportedRPCMethods()
	for _, method := range []string{"doctor", "config.import", "network.reset", "node.test"} {
		if !slices.Contains(methods, method) {
			t.Fatalf("supported methods missing %s: %#v", method, methods)
		}
	}
}

func TestDoctorRedactsNodeProbeServer(t *testing.T) {
	value := redactNodeProbeResults([]runtimev2.NodeProbeResult{
		{
			ID:     "node-1",
			Name:   "secret.example.com",
			Server: "secret.example.com",
			Port:   443,
		},
	})
	text := mustMarshalForTest(t, value)
	if strings.Contains(text, "secret.example.com") {
		t.Fatalf("node probe server/name was not redacted from %s", text)
	}
	if !strings.Contains(text, `"port":443`) {
		t.Fatalf("non-secret node probe fields should remain available, got %s", text)
	}
}

func mustMarshalForTest(t *testing.T, value interface{}) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
