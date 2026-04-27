package diagnostics

import (
	"errors"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func TestPrivacyFlagsVisibleProxyArtifacts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.APIPort = 9090
	cfg.Apps.Mode = "all"
	cfg.DNS.HijackPerUID = false

	report := Privacy(cfg, 20, func(name string, args ...string) (string, error) {
		command := name + " " + strings.Join(args, " ")
		switch command {
		case "ip link show":
			return "9: tun0: <POINTOPOINT> mtu 1500\n", nil
		case "dumpsys connectivity":
			return "NetworkCapabilities: TRANSPORT_VPN\nLinkProperties: dnses: [ /127.0.0.1 ]\n", nil
		case "settings get global http_proxy":
			return "127.0.0.1:8080\n", nil
		case "settings get global global_http_proxy_port":
			return "8080\n", nil
		default:
			return "", errors.New("unexpected command")
		}
	})

	checks, ok := report["checks"].(map[string]interface{})
	if !ok {
		t.Fatalf("privacy checks missing: %#v", report)
	}
	for _, key := range []string{
		"no_vpn_like_interfaces",
		"no_transport_vpn_hint",
		"no_loopback_dns",
		"system_proxy_unset",
		"clash_api_disabled",
		"per_app_whitelist_default",
		"dns_hijack_per_uid",
	} {
		if checks[key] != false {
			t.Fatalf("expected %s=false, got %#v", key, checks[key])
		}
	}
	if checks["self_test_apps_direct"] != true {
		t.Fatalf("self-test packages should remain direct-only: %#v", checks)
	}
}
