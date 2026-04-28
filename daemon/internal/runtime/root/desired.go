package root

import (
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func DesiredStateFromConfig(cfg *config.Config) runtimev2.DesiredState {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	backendKind := runtimev2.BackendKind(strings.TrimSpace(cfg.RuntimeV2.BackendKind))
	if backendKind == "" {
		backendKind = runtimev2.BackendRootTProxy
	}
	fallbackPolicy := runtimev2.FallbackPolicy(strings.TrimSpace(cfg.RuntimeV2.FallbackPolicy))
	if fallbackPolicy == "" {
		fallbackPolicy = runtimev2.FallbackOfferReset
	}
	return runtimev2.DesiredState{
		BackendKind:     backendKind,
		ActiveProfileID: cfg.Profile.ActiveNodeID,
		RoutingMode:     mapRoutingMode(cfg),
		AppSelection:    mapAppSelection(cfg),
		DNSPolicy: runtimev2.DNSPolicy{
			RemoteDNS: cfg.DNS.ProxyDNS,
			DirectDNS: cfg.DNS.DirectDNS,
			FakeDNS:   cfg.DNS.FakeIP,
			IPv6Mode:  cfg.IPv6.Mode,
		},
		FallbackPolicy: fallbackPolicy,
	}
}

func CompleteDesiredState(desired runtimev2.DesiredState, defaults runtimev2.DesiredState) runtimev2.DesiredState {
	if desired.BackendKind == "" {
		desired.BackendKind = defaults.BackendKind
	}
	if desired.FallbackPolicy == "" {
		desired.FallbackPolicy = defaults.FallbackPolicy
	}
	if desired.ActiveProfileID == "" {
		desired.ActiveProfileID = defaults.ActiveProfileID
	}
	return desired
}

func ApplyDesiredStateToConfig(currentCfg *config.Config, desired runtimev2.DesiredState) (*config.Config, error) {
	doc := profiledoc.ApplyRuntimeIntent(profiledoc.FromConfig(currentCfg), profiledoc.RuntimeIntent{
		BackendKind:     string(desired.BackendKind),
		FallbackPolicy:  string(desired.FallbackPolicy),
		ActiveProfileID: desired.ActiveProfileID,
	})
	nextCfg, _, err := profiledoc.ApplyToConfig(currentCfg, doc)
	if err != nil {
		return nil, err
	}
	return nextCfg, nil
}

func mapAppSelection(cfg *config.Config) runtimev2.AppSelection {
	appSelection := runtimev2.AppSelection{
		BypassPackages: append([]string(nil), cfg.Routing.AlwaysDirectApps...),
	}
	switch cfg.Apps.Mode {
	case "whitelist":
		appSelection.ProxyPackages = append([]string(nil), cfg.Apps.Packages...)
	case "blacklist":
		appSelection.BypassPackages = append(appSelection.BypassPackages, cfg.Apps.Packages...)
	}
	return appSelection
}

func mapRoutingMode(cfg *config.Config) string {
	switch cfg.Routing.Mode {
	case "all":
		if cfg.Apps.Mode == "whitelist" || cfg.Apps.Mode == "all" {
			return "PER_APP"
		}
		if cfg.Apps.Mode == "blacklist" {
			return "PER_APP_BYPASS"
		}
		return "PROXY_ALL"
	case "whitelist":
		return "PER_APP"
	case "blacklist":
		return "PER_APP_BYPASS"
	case "rules":
		return "RULES"
	case "direct":
		return "DIRECT"
	default:
		return "PROXY_ALL"
	}
}
