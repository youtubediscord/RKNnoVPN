package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type nodeProbeRunner struct {
	daemon         *daemon
	cfg            *config.Config
	timeoutMS      int
	timeout        time.Duration
	requested      map[string]bool
	testURL        string
	runtimeRunning bool
	runtimeHealth  runtimev2.HealthSnapshot
	apiPort        int
	profileCount   int
}

func (r nodeProbeRunner) run(profiles []*config.NodeProfile) []runtimev2.NodeProbeResult {
	results := make([]runtimev2.NodeProbeResult, 0, len(profiles))
	for _, profile := range profiles {
		if len(r.requested) > 0 && !r.requested[profile.ID] {
			continue
		}
		results = append(results, r.probeProfile(profile))
	}
	return results
}

func (r nodeProbeRunner) probeProfile(profile *config.NodeProfile) runtimev2.NodeProbeResult {
	result := newNodeProbeResult(profile)
	r.runTCPDirectProbe(profile, &result)
	r.runDNSBootstrapProbe(profile, &result)
	r.runTunnelProbe(profile, &result)
	return finalizeNodeProbeResult(result)
}

func newNodeProbeResult(profile *config.NodeProfile) runtimev2.NodeProbeResult {
	return runtimev2.NodeProbeResult{
		ID:               profile.ID,
		Name:             firstNonEmpty(profile.Name, profile.Tag, profile.Address),
		Protocol:         profile.Protocol,
		Server:           profile.Address,
		Port:             profile.Port,
		TCPStatus:        "not_run",
		URLStatus:        "not_run",
		ThroughputStatus: "not_run",
		Verdict:          "unknown",
	}
}

func (r nodeProbeRunner) runTCPDirectProbe(profile *config.NodeProfile, result *runtimev2.NodeProbeResult) {
	tcpMS, tcpErr := testTCPConnect(profile.Address, profile.Port, r.timeout)
	if tcpErr == nil {
		result.TCPDirect = &tcpMS
		result.TCPStatus = "ok"
		return
	}
	result.TCPStatus = "fail"
	result.ErrorClass = "tcp_direct_failed"
}

func (r nodeProbeRunner) runDNSBootstrapProbe(profile *config.NodeProfile, result *runtimev2.NodeProbeResult) {
	result.DNSBootstrap = r.daemon.probeNodeBootstrapDNS(r.cfg, profile.Address, r.timeout)
	if !result.DNSBootstrap && result.ErrorClass == "" {
		result.ErrorClass = "dns_bootstrap_failed"
	}
}

func (r nodeProbeRunner) runTunnelProbe(profile *config.NodeProfile, result *runtimev2.NodeProbeResult) {
	if !r.runtimeRunning {
		result.URLStatus = "fail"
		result.Verdict = "unusable"
		if result.ErrorClass == "" {
			result.ErrorClass = "tunnel_unavailable"
		}
		return
	}

	urlMS, urlErr := r.runTunnelURLProbe(profile, result)
	if urlErr == nil {
		result.TunnelDelay = &urlMS
		result.URLStatus = "ok"
		result.Verdict = "usable"
		result.ErrorClass = "ok"
		return
	}

	result.URLStatus = "fail"
	if result.ThroughputStatus == "not_run" {
		result.ThroughputStatus = "unavailable"
	}
	result.Verdict = "unusable"
	result.ErrorDetail = urlErr.Error()
	if result.ErrorClass == "" {
		result.ErrorClass = classifyRuntimeURLTestFailure(urlErr, r.runtimeHealth)
	}
}

func (r nodeProbeRunner) runTunnelURLProbe(profile *config.NodeProfile, result *runtimev2.NodeProbeResult) (int64, error) {
	if r.apiPort > 0 {
		urlMS, _, err := testClashDelay(r.apiPort, profile.Tag, r.testURL, r.timeoutMS)
		result.ThroughputStatus = "latency_only"
		return urlMS, err
	}
	if r.profileCount == 1 {
		metrics, err := testTransparentURLProbe(r.cfg, r.testURL, r.timeoutMS)
		if metrics.ResponseBytes > 0 {
			responseBytes := metrics.ResponseBytes
			result.ResponseBytes = &responseBytes
		}
		if metrics.ThroughputBps > 0 {
			throughputBps := metrics.ThroughputBps
			result.ThroughputBps = &throughputBps
			result.ThroughputStatus = "ok"
		} else {
			result.ThroughputStatus = "latency_only"
		}
		return metrics.LatencyMS, err
	}
	result.ThroughputStatus = "unavailable"
	return 0, fmt.Errorf("api_disabled")
}

func finalizeNodeProbeResult(result runtimev2.NodeProbeResult) runtimev2.NodeProbeResult {
	if result.TCPStatus == "ok" && result.URLStatus != "ok" {
		result.Verdict = "unusable"
	}
	return result
}

func requestedNodeIDs(nodeIDs []string) map[string]bool {
	requested := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		if id = strings.TrimSpace(id); id != "" {
			requested[id] = true
		}
	}
	return requested
}

func probeNodeProfiles(cfg *config.Config) []*config.NodeProfile {
	profiles := config.ProfilesFromConfigNodes(cfg)
	if len(profiles) > 0 {
		return profiles
	}
	profile := cfg.ResolveProfile()
	if profile.Address == "" {
		return nil
	}
	profile.Tag = firstNonEmpty(profile.Tag, "proxy")
	return []*config.NodeProfile{profile}
}

func resolveNodeProbeURL(url string, cfg *config.Config) string {
	testURL := strings.TrimSpace(url)
	if testURL == "" {
		testURL = strings.TrimSpace(cfg.Health.URL)
	}
	if testURL == "" {
		testURL = "https://www.gstatic.com/generate_204"
	}
	return testURL
}
