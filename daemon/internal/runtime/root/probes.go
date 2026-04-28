package root

import (
	"fmt"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type URLProbeMetrics struct {
	LatencyMS     int64
	StatusCode    int
	ResponseBytes int64
	ThroughputBps int64
}

type ProbeIO interface {
	TCPConnect(host string, port int, timeout time.Duration) (int64, error)
	BootstrapDNS(cfg *config.Config, host string, timeout time.Duration) bool
	ClashDelay(apiPort int, outboundTag string, testURL string, timeoutMS int) (int64, int, error)
	TransparentURLProbe(cfg *config.Config, testURL string, timeoutMS int) (URLProbeMetrics, error)
}

type NodeProbeRunner struct {
	Config         *config.Config
	TimeoutMS      int
	Timeout        time.Duration
	Requested      map[string]bool
	TestURL        string
	RuntimeRunning bool
	RuntimeHealth  runtimev2.HealthSnapshot
	APIPort        int
	ProfileCount   int
	IO             ProbeIO
}

type NodeProbeInput struct {
	Config        *config.Config
	State         core.State
	RuntimeHealth runtimev2.HealthSnapshot
	URL           string
	TimeoutMS     int
	NodeIDs       []string
	APIPort       int
	IO            ProbeIO
}

func RunNodeProbes(input NodeProbeInput) []runtimev2.NodeProbeResult {
	profiles := ProbeNodeProfiles(input.Config)
	timeout := time.Duration(input.TimeoutMS) * time.Millisecond
	runtimeRunning := input.State == core.StateRunning || input.State == core.StateDegraded
	runner := NodeProbeRunner{
		Config:         input.Config,
		TimeoutMS:      input.TimeoutMS,
		Timeout:        timeout,
		Requested:      RequestedNodeIDs(input.NodeIDs),
		TestURL:        ResolveNodeProbeURL(input.URL, input.Config),
		RuntimeRunning: runtimeRunning,
		RuntimeHealth:  input.RuntimeHealth,
		APIPort:        input.APIPort,
		ProfileCount:   len(profiles),
		IO:             input.IO,
	}
	return runner.Run(profiles)
}

func (r NodeProbeRunner) Run(profiles []*config.NodeProfile) []runtimev2.NodeProbeResult {
	results := make([]runtimev2.NodeProbeResult, 0, len(profiles))
	for _, profile := range profiles {
		if len(r.Requested) > 0 && !r.Requested[profile.ID] {
			continue
		}
		results = append(results, r.probeProfile(profile))
	}
	return results
}

func (r NodeProbeRunner) probeProfile(profile *config.NodeProfile) runtimev2.NodeProbeResult {
	result := NewNodeProbeResult(profile)
	r.runTCPDirectProbe(profile, &result)
	r.runDNSBootstrapProbe(profile, &result)
	r.runTunnelProbe(profile, &result)
	return FinalizeNodeProbeResult(result)
}

func NewNodeProbeResult(profile *config.NodeProfile) runtimev2.NodeProbeResult {
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

func (r NodeProbeRunner) runTCPDirectProbe(profile *config.NodeProfile, result *runtimev2.NodeProbeResult) {
	tcpMS, tcpErr := r.IO.TCPConnect(profile.Address, profile.Port, r.Timeout)
	if tcpErr == nil {
		result.TCPDirect = &tcpMS
		result.TCPStatus = "ok"
		return
	}
	result.TCPStatus = "fail"
	result.ErrorClass = "tcp_direct_failed"
}

func (r NodeProbeRunner) runDNSBootstrapProbe(profile *config.NodeProfile, result *runtimev2.NodeProbeResult) {
	result.DNSBootstrap = r.IO.BootstrapDNS(r.Config, profile.Address, r.Timeout)
	if !result.DNSBootstrap && result.ErrorClass == "" {
		result.ErrorClass = "dns_bootstrap_failed"
	}
}

func (r NodeProbeRunner) runTunnelProbe(profile *config.NodeProfile, result *runtimev2.NodeProbeResult) {
	if !r.RuntimeRunning {
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
		result.ErrorClass = ClassifyURLTestFailure(urlErr, r.RuntimeHealth)
	}
}

func (r NodeProbeRunner) runTunnelURLProbe(profile *config.NodeProfile, result *runtimev2.NodeProbeResult) (int64, error) {
	if r.APIPort > 0 {
		urlMS, _, err := r.IO.ClashDelay(r.APIPort, profile.Tag, r.TestURL, r.TimeoutMS)
		result.ThroughputStatus = "latency_only"
		return urlMS, err
	}
	if r.ProfileCount == 1 {
		metrics, err := r.IO.TransparentURLProbe(r.Config, r.TestURL, r.TimeoutMS)
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

func FinalizeNodeProbeResult(result runtimev2.NodeProbeResult) runtimev2.NodeProbeResult {
	if result.TCPStatus == "ok" && result.URLStatus != "ok" {
		result.Verdict = "unusable"
	}
	return result
}

func RequestedNodeIDs(nodeIDs []string) map[string]bool {
	requested := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		if id = strings.TrimSpace(id); id != "" {
			requested[id] = true
		}
	}
	return requested
}

func ProbeNodeProfiles(cfg *config.Config) []*config.NodeProfile {
	if cfg == nil {
		return nil
	}
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

func ResolveNodeProbeURL(url string, cfg *config.Config) string {
	testURL := strings.TrimSpace(url)
	if testURL == "" && cfg != nil {
		testURL = strings.TrimSpace(cfg.Health.URL)
	}
	if testURL == "" {
		testURL = "https://www.gstatic.com/generate_204"
	}
	return testURL
}
