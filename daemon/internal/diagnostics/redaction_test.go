package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactTextMasksSensitiveInlineValues(t *testing.T) {
	text := RedactText(`uuid=00000000-0000-0000-0000-000000000000 password=secret public_key=pubsecretvalue`)
	for _, secret := range []string{"00000000-0000-0000-0000-000000000000", "secret", "pubsecretvalue"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q was not redacted from %q", secret, text)
		}
	}
	if !strings.Contains(text, `uuid="[redacted]"`) || !strings.Contains(text, `password="[redacted]"`) {
		t.Fatalf("expected redaction markers, got %q", text)
	}
}

func TestReadRedactedJSONFilePreservesEndpointMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"uuid":"00000000-0000-0000-0000-000000000000","server":"proxy.example.com","server_port":443}`), 0644); err != nil {
		t.Fatal(err)
	}

	section := ReadRedactedJSONFile(path)
	if section.Error != "" || section.Missing {
		t.Fatalf("unexpected section error: %#v", section)
	}
	value, ok := section.Value.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected redacted value: %#v", section.Value)
	}
	if value["uuid"] != "[redacted]" {
		t.Fatalf("uuid was not redacted: %#v", value)
	}
	if value["server"] != "proxy.example.com" || value["server_port"] != float64(443) {
		t.Fatalf("endpoint metadata should remain visible: %#v", value)
	}
}

func TestReadRedactedJSONFileLimitsInvalidJSONText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("a\nb\npassword=secret\n"), 0644); err != nil {
		t.Fatal(err)
	}

	section := ReadRedactedJSONFile(path)
	if section.Error == "" {
		t.Fatalf("invalid JSON should report parse error: %#v", section)
	}
	lines, ok := section.Value.([]string)
	if !ok {
		t.Fatalf("invalid JSON should return redacted lines, got %#v", section.Value)
	}
	if strings.Contains(strings.Join(lines, "\n"), "secret") {
		t.Fatalf("invalid JSON text was not redacted: %#v", lines)
	}
}

func TestLoopbackDNSAndVPNInterfaceDetection(t *testing.T) {
	if FirstLoopbackDNSLine([]string{"LinkProperties: dnses: [ /127.0.0.1 ]"}) == "" {
		t.Fatal("loopback DNS line was not detected")
	}
	if line := FirstVPNLikeInterfaceLine([]string{"7: tun0: <POINTOPOINT> mtu 1500"}); line == "" {
		t.Fatal("VPN-like interface was not detected")
	}
	if name := IPLinkInterfaceName("7: wlan0@if2: <BROADCAST> mtu 1500"); name != "wlan0" {
		t.Fatalf("unexpected interface name %q", name)
	}
}

func TestCommandLooksEmptySetting(t *testing.T) {
	if !commandLooksEmptySetting(CommandResult{Lines: []string{"null"}}) {
		t.Fatal("null setting should be empty")
	}
	if commandLooksEmptySetting(CommandResult{Lines: []string{"127.0.0.1:8080"}}) {
		t.Fatal("non-empty setting should not be empty")
	}
	if !commandLooksEmptySetting(CommandResult{Error: "settings unavailable"}) {
		t.Fatal("unavailable setting command should be treated as empty for privacy checks")
	}
}
