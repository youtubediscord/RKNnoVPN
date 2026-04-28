package main

import (
	"reflect"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestDesiredStateFromConfigUsesRootRuntimeDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "nil config",
		},
		{
			name: "blank runtime config",
			cfg: func() *config.Config {
				cfg := config.DefaultConfig()
				cfg.RuntimeV2.BackendKind = " "
				cfg.RuntimeV2.FallbackPolicy = ""
				return cfg
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desired := rootruntime.DesiredStateFromConfig(tt.cfg)

			if desired.BackendKind != runtimev2.BackendRootTProxy {
				t.Fatalf("default backend = %q, want %q", desired.BackendKind, runtimev2.BackendRootTProxy)
			}
			if desired.FallbackPolicy != runtimev2.FallbackOfferReset {
				t.Fatalf("default fallback = %q, want %q", desired.FallbackPolicy, runtimev2.FallbackOfferReset)
			}
		})
	}
}

func TestDesiredStateFromConfigMapsActiveProfileID(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Profile.ActiveNodeID = "node-active"

	desired := rootruntime.DesiredStateFromConfig(cfg)

	if desired.ActiveProfileID != "node-active" {
		t.Fatalf("active profile ID = %q", desired.ActiveProfileID)
	}
}

func TestDesiredStateFromConfigMapsRoutingMode(t *testing.T) {
	tests := []struct {
		name        string
		routingMode string
		appMode     string
		want        string
	}{
		{name: "all routes everything when apps are not constrained", routingMode: "all", appMode: "off", want: "PROXY_ALL"},
		{name: "all with app whitelist is per-app proxy", routingMode: "all", appMode: "whitelist", want: "PER_APP"},
		{name: "all with app all mode is per-app proxy", routingMode: "all", appMode: "all", want: "PER_APP"},
		{name: "all with app blacklist is per-app bypass", routingMode: "all", appMode: "blacklist", want: "PER_APP_BYPASS"},
		{name: "whitelist", routingMode: "whitelist", appMode: "off", want: "PER_APP"},
		{name: "blacklist", routingMode: "blacklist", appMode: "off", want: "PER_APP_BYPASS"},
		{name: "rules", routingMode: "rules", appMode: "off", want: "RULES"},
		{name: "direct", routingMode: "direct", appMode: "off", want: "DIRECT"},
		{name: "unknown falls back to proxy all", routingMode: "mystery", appMode: "off", want: "PROXY_ALL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Routing.Mode = tt.routingMode
			cfg.Apps.Mode = tt.appMode

			desired := rootruntime.DesiredStateFromConfig(cfg)

			if desired.RoutingMode != tt.want {
				t.Fatalf("routing mode = %q, want %q", desired.RoutingMode, tt.want)
			}
		})
	}
}

func TestDesiredStateFromConfigMapsAppSelection(t *testing.T) {
	tests := []struct {
		name       string
		appMode    string
		apps       []string
		always     []string
		wantProxy  []string
		wantBypass []string
	}{
		{
			name:       "whitelist proxies selected packages and keeps always-direct bypass",
			appMode:    "whitelist",
			apps:       []string{"com.example.proxy"},
			always:     []string{"com.android.vending"},
			wantProxy:  []string{"com.example.proxy"},
			wantBypass: []string{"com.android.vending"},
		},
		{
			name:       "blacklist bypasses selected packages after always-direct packages",
			appMode:    "blacklist",
			apps:       []string{"com.example.direct"},
			always:     []string{"com.android.vending"},
			wantBypass: []string{"com.android.vending", "com.example.direct"},
		},
		{
			name:       "all only carries always-direct bypass packages",
			appMode:    "all",
			apps:       []string{"com.example.ignored"},
			always:     []string{"com.android.vending"},
			wantBypass: []string{"com.android.vending"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Apps.Mode = tt.appMode
			cfg.Apps.Packages = append([]string(nil), tt.apps...)
			cfg.Routing.AlwaysDirectApps = append([]string(nil), tt.always...)

			desired := rootruntime.DesiredStateFromConfig(cfg)

			if !reflect.DeepEqual(desired.AppSelection.ProxyPackages, tt.wantProxy) {
				t.Fatalf("proxy packages = %#v, want %#v", desired.AppSelection.ProxyPackages, tt.wantProxy)
			}
			if !reflect.DeepEqual(desired.AppSelection.BypassPackages, tt.wantBypass) {
				t.Fatalf("bypass packages = %#v, want %#v", desired.AppSelection.BypassPackages, tt.wantBypass)
			}
		})
	}
}

func TestDesiredStateFromConfigMapsDNSPolicy(t *testing.T) {
	tests := []struct {
		name      string
		proxyDNS  string
		directDNS string
		fakeDNS   bool
		ipv6Mode  string
		want      runtimev2.DNSPolicy
	}{
		{
			name:      "proxy direct fake DNS and IPv6 mode",
			proxyDNS:  "https://proxy.example/dns-query",
			directDNS: "https://direct.example/dns-query",
			fakeDNS:   true,
			ipv6Mode:  "disable",
			want: runtimev2.DNSPolicy{
				RemoteDNS: "https://proxy.example/dns-query",
				DirectDNS: "https://direct.example/dns-query",
				FakeDNS:   true,
				IPv6Mode:  "disable",
			},
		},
		{
			name:      "classic direct DNS without fake DNS",
			proxyDNS:  "https://cloudflare-dns.com/dns-query",
			directDNS: "https://dns.google/dns-query",
			ipv6Mode:  "mirror",
			want: runtimev2.DNSPolicy{
				RemoteDNS: "https://cloudflare-dns.com/dns-query",
				DirectDNS: "https://dns.google/dns-query",
				IPv6Mode:  "mirror",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.DNS.ProxyDNS = tt.proxyDNS
			cfg.DNS.DirectDNS = tt.directDNS
			cfg.DNS.FakeIP = tt.fakeDNS
			cfg.IPv6.Mode = tt.ipv6Mode

			desired := rootruntime.DesiredStateFromConfig(cfg)

			if desired.DNSPolicy != tt.want {
				t.Fatalf("DNS policy = %#v, want %#v", desired.DNSPolicy, tt.want)
			}
		})
	}
}

func TestDesiredStateFromConfigMapsRuntimeIntentFieldsTogether(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RuntimeV2.BackendKind = " ROOT_TPROXY "
	cfg.RuntimeV2.FallbackPolicy = " AUTO_RESET_ROOTED "
	cfg.Profile.ActiveNodeID = "node-42"
	cfg.Routing.Mode = "rules"
	cfg.Apps.Mode = "whitelist"
	cfg.Apps.Packages = []string{"com.example.proxy"}
	cfg.Routing.AlwaysDirectApps = []string{"com.android.vending"}
	cfg.DNS.ProxyDNS = "https://proxy.example/dns-query"
	cfg.DNS.DirectDNS = "https://direct.example/dns-query"
	cfg.DNS.FakeIP = true
	cfg.IPv6.Mode = "disable"

	desired := rootruntime.DesiredStateFromConfig(cfg)

	if desired.BackendKind != runtimev2.BackendRootTProxy {
		t.Fatalf("backend = %q", desired.BackendKind)
	}
	if desired.FallbackPolicy != runtimev2.FallbackAutoReset {
		t.Fatalf("fallback = %q", desired.FallbackPolicy)
	}
	if desired.ActiveProfileID != "node-42" {
		t.Fatalf("active profile = %q", desired.ActiveProfileID)
	}
	if desired.RoutingMode != "RULES" {
		t.Fatalf("routing mode = %q", desired.RoutingMode)
	}
	if !reflect.DeepEqual(desired.AppSelection.ProxyPackages, []string{"com.example.proxy"}) {
		t.Fatalf("proxy packages = %#v", desired.AppSelection.ProxyPackages)
	}
	if !reflect.DeepEqual(desired.AppSelection.BypassPackages, []string{"com.android.vending"}) {
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

func TestDesiredStateFromConfigDoesNotAliasPackageSlices(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Apps.Mode = "whitelist"
	cfg.Apps.Packages = []string{"com.example.proxy"}
	cfg.Routing.AlwaysDirectApps = []string{"com.example.direct"}

	desired := rootruntime.DesiredStateFromConfig(cfg)
	cfg.Apps.Packages[0] = "mutated.proxy"
	cfg.Routing.AlwaysDirectApps[0] = "mutated.direct"

	if got := desired.AppSelection.ProxyPackages; !reflect.DeepEqual(got, []string{"com.example.proxy"}) {
		t.Fatalf("proxy packages aliased config slice: %#v", got)
	}
	if got := desired.AppSelection.BypassPackages; !reflect.DeepEqual(got, []string{"com.example.direct"}) {
		t.Fatalf("bypass packages aliased config slice: %#v", got)
	}
}
