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
	for _, secret := range []string{"00000000-0000-0000-0000-000000000000", "secret", "pubsecretvalue", "sidsecretvalue"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q was not redacted from %s", secret, text)
		}
	}
	for _, diagnostic := range []string{`"server":"proxy.example.com"`, `"server_port":443`, `"server_name":"cdn.example.com"`} {
		if !strings.Contains(text, diagnostic) {
			t.Fatalf("diagnostic endpoint field %s should remain available, got %s", diagnostic, text)
		}
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

func TestSupportedCapabilitiesAdvertiseSchemaAndDiagnostics(t *testing.T) {
	caps := supportedCapabilities()
	for _, capability := range []string{"config.schema.v4", "diagnostics.bundle.v2", "node-test.tcp-direct", "runtime.logs"} {
		if !slices.Contains(caps, capability) {
			t.Fatalf("supported capabilities missing %s: %#v", capability, caps)
		}
	}
}

func TestDoctorKeepsNodeProbeEndpointMetadata(t *testing.T) {
	value := redactNodeProbeResults([]runtimev2.NodeProbeResult{
		{
			ID:     "node-1",
			Name:   "secret.example.com",
			Server: "secret.example.com",
			Port:   443,
		},
	})
	text := mustMarshalForTest(t, value)
	for _, diagnostic := range []string{`"name":"secret.example.com"`, `"server":"secret.example.com"`, `"port":443`} {
		if !strings.Contains(text, diagnostic) {
			t.Fatalf("node probe diagnostic field %s should remain available, got %s", diagnostic, text)
		}
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
