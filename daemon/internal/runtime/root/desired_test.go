package root

import (
	"reflect"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestDesiredStateFromConfigUsesRootDefaults(t *testing.T) {
	desired := DesiredStateFromConfig(nil)

	if desired.BackendKind != runtimev2.BackendRootTProxy {
		t.Fatalf("backend = %q", desired.BackendKind)
	}
	if desired.FallbackPolicy != runtimev2.FallbackOfferReset {
		t.Fatalf("fallback = %q", desired.FallbackPolicy)
	}
}

func TestDesiredStateFromConfigMapsRuntimeIntent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RuntimeV2.BackendKind = " ROOT_TPROXY "
	cfg.RuntimeV2.FallbackPolicy = " AUTO_RESET_ROOTED "
	cfg.Profile.ActiveNodeID = "node-42"
	cfg.Routing.Mode = "all"
	cfg.Apps.Mode = "blacklist"
	cfg.Apps.Packages = []string{"com.example.direct"}
	cfg.Routing.AlwaysDirectApps = []string{"com.android.vending"}
	cfg.DNS.ProxyDNS = "https://proxy.example/dns-query"
	cfg.DNS.DirectDNS = "https://direct.example/dns-query"
	cfg.DNS.FakeIP = true
	cfg.IPv6.Mode = "disable"

	desired := DesiredStateFromConfig(cfg)

	if desired.BackendKind != runtimev2.BackendRootTProxy {
		t.Fatalf("backend = %q", desired.BackendKind)
	}
	if desired.FallbackPolicy != runtimev2.FallbackAutoReset {
		t.Fatalf("fallback = %q", desired.FallbackPolicy)
	}
	if desired.ActiveProfileID != "node-42" {
		t.Fatalf("active profile = %q", desired.ActiveProfileID)
	}
	if desired.RoutingMode != "PER_APP_BYPASS" {
		t.Fatalf("routing mode = %q", desired.RoutingMode)
	}
	if !reflect.DeepEqual(desired.AppSelection.BypassPackages, []string{"com.android.vending", "com.example.direct"}) {
		t.Fatalf("bypass packages = %#v", desired.AppSelection.BypassPackages)
	}
	if desired.DNSPolicy != (runtimev2.DNSPolicy{
		RemoteDNS: "https://proxy.example/dns-query",
		DirectDNS: "https://direct.example/dns-query",
		FakeDNS:   true,
		IPv6Mode:  "disable",
	}) {
		t.Fatalf("DNS policy = %#v", desired.DNSPolicy)
	}
}

func TestCompleteDesiredStateFillsRuntimeIntentDefaults(t *testing.T) {
	defaults := runtimev2.DesiredState{
		BackendKind:     runtimev2.BackendRootTProxy,
		ActiveProfileID: "node-default",
		FallbackPolicy:  runtimev2.FallbackOfferReset,
		RoutingMode:     "PROXY_ALL",
	}
	desired := CompleteDesiredState(runtimev2.DesiredState{
		RoutingMode: "DIRECT",
	}, defaults)

	if desired.BackendKind != runtimev2.BackendRootTProxy {
		t.Fatalf("backend = %q", desired.BackendKind)
	}
	if desired.ActiveProfileID != "node-default" {
		t.Fatalf("active profile = %q", desired.ActiveProfileID)
	}
	if desired.FallbackPolicy != runtimev2.FallbackOfferReset {
		t.Fatalf("fallback = %q", desired.FallbackPolicy)
	}
	if desired.RoutingMode != "DIRECT" {
		t.Fatalf("explicit routing mode was overwritten: %q", desired.RoutingMode)
	}
}

func TestApplyDesiredStateToConfigPersistsRuntimeIntent(t *testing.T) {
	cfg := config.DefaultConfig()

	next, err := ApplyDesiredStateToConfig(cfg, runtimev2.DesiredState{
		BackendKind:    runtimev2.BackendRootTProxy,
		FallbackPolicy: runtimev2.FallbackAutoReset,
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.RuntimeV2.BackendKind != string(runtimev2.BackendRootTProxy) {
		t.Fatalf("backend = %q", next.RuntimeV2.BackendKind)
	}
	if next.RuntimeV2.FallbackPolicy != string(runtimev2.FallbackAutoReset) {
		t.Fatalf("fallback = %q", next.RuntimeV2.FallbackPolicy)
	}
}
