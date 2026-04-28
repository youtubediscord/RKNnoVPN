package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestClassifyRuntimeURLTestFailureTable(t *testing.T) {
	healthy := runtimev2.HealthSnapshot{
		CoreReady:    true,
		RoutingReady: true,
		DNSReady:     true,
		EgressReady:  true,
	}

	tests := []struct {
		name     string
		err      error
		snapshot runtimev2.HealthSnapshot
		want     string
	}{
		{
			name:     "api disabled",
			err:      errors.New("api_disabled"),
			snapshot: healthy,
			want:     "api_disabled",
		},
		{
			name:     "localhost API unavailable",
			err:      errors.New("Get http://127.0.0.1:9090/proxies/node/delay: connect: connection refused"),
			snapshot: healthy,
			want:     "api_unavailable",
		},
		{
			name:     "outbound missing",
			err:      errors.New("clash delay HTTP 404: proxy not found"),
			snapshot: healthy,
			want:     "outbound_missing",
		},
		{
			name:     "DNS unavailable",
			err:      errors.New("lookup example.com: no such host"),
			snapshot: healthy,
			want:     "proxy_dns_unavailable",
		},
		{
			name:     "TLS handshake",
			err:      errors.New("remote error: tls: handshake failure"),
			snapshot: healthy,
			want:     "tls_handshake_failed",
		},
		{
			name:     "timeout",
			err:      errors.New("context deadline exceeded"),
			snapshot: healthy,
			want:     "tunnel_delay_failed",
		},
		{
			name: "health code runtime not ready",
			err:  errors.New("timeout"),
			snapshot: runtimev2.HealthSnapshot{
				CoreReady:    true,
				RoutingReady: true,
				LastCode:     "RULES_NOT_APPLIED",
			},
			want: "runtime_not_ready",
		},
		{
			name: "health code proxy DNS unavailable",
			err:  errors.New("timeout"),
			snapshot: runtimev2.HealthSnapshot{
				CoreReady:    true,
				RoutingReady: true,
				DNSReady:     false,
				EgressReady:  true,
				LastCode:     "DNS_LOOKUP_TIMEOUT",
			},
			want: "proxy_dns_unavailable",
		},
		{
			name: "health code outbound URL failed",
			err:  errors.New("timeout"),
			snapshot: runtimev2.HealthSnapshot{
				CoreReady:    true,
				RoutingReady: true,
				DNSReady:     true,
				EgressReady:  false,
				LastCode:     "OUTBOUND_URL_FAILED",
			},
			want: "outbound_url_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rootruntime.ClassifyURLTestFailure(tt.err, tt.snapshot); got != tt.want {
				t.Fatalf("classifyRuntimeURLTestFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveNodeProbeURLUsesRequestHealthThenFallback(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Health.URL = " https://health.example/generate_204 "

	if got := rootruntime.ResolveNodeProbeURL(" https://request.example/test ", cfg); got != "https://request.example/test" {
		t.Fatalf("explicit URL = %q", got)
	}
	if got := rootruntime.ResolveNodeProbeURL(" ", cfg); got != "https://health.example/generate_204" {
		t.Fatalf("health URL = %q", got)
	}
	cfg.Health.URL = " "
	if got := rootruntime.ResolveNodeProbeURL("", cfg); got != "https://www.gstatic.com/generate_204" {
		t.Fatalf("fallback URL = %q", got)
	}
}

func TestNodeProbeRunnerFiltersSelectedNodes(t *testing.T) {
	requested := rootruntime.RequestedNodeIDs([]string{" node-a ", "", "node-c"})

	profiles := []*config.NodeProfile{
		{ID: "node-a"},
		{ID: "node-b"},
		{ID: "node-c"},
	}
	got := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if len(requested) == 0 || requested[profile.ID] {
			got = append(got, profile.ID)
		}
	}

	if !reflect.DeepEqual(got, []string{"node-a", "node-c"}) {
		t.Fatalf("selected nodes = %#v", got)
	}
}
