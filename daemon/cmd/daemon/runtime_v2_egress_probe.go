package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
)

func (d *daemon) refreshOutboundURLProbe(state core.State, cfg *config.Config, apiPort int, timeoutMS int) (*int64, health.CheckResult) {
	if state != core.StateRunning && state != core.StateDegraded {
		return nil, health.CheckResult{
			Pass:   false,
			Detail: "runtime is not running",
			Code:   "OUTBOUND_NOT_RUNNING",
		}
	}
	if timeoutMS <= 0 {
		timeoutMS = 2500
	}

	now := time.Now()
	d.metricsMu.Lock()
	if d.latency.Valid && now.Sub(d.latency.CheckedAt) < 30*time.Second {
		value := d.latency.Ms
		d.metricsMu.Unlock()
		return &value, health.CheckResult{
			Pass:   true,
			Detail: fmt.Sprintf("recent outbound URL probe ok: %d ms", value),
		}
	}
	if !d.latency.Valid && !d.latency.CheckedAt.IsZero() && now.Sub(d.latency.CheckedAt) < 10*time.Second {
		d.metricsMu.Unlock()
		return nil, health.CheckResult{
			Pass:   false,
			Detail: "recent outbound URL probe failed",
			Code:   "OUTBOUND_URL_FAILED",
		}
	}
	d.metricsMu.Unlock()

	var latency int64
	var err error
	var lastURL string
	for _, testURL := range healthEgressURLs(cfg) {
		lastURL = testURL
		if apiPort > 0 {
			latency, _, err = testClashDelay(apiPort, "proxy", testURL, timeoutMS)
		} else {
			metrics, probeErr := testTransparentURLProbe(cfg, testURL, timeoutMS)
			latency = metrics.LatencyMS
			err = probeErr
		}
		if err == nil {
			break
		}
	}

	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	d.latency.CheckedAt = now
	if err != nil {
		d.latency.Valid = false
		return nil, health.CheckResult{
			Pass:   false,
			Detail: fmt.Sprintf("outbound URL probe failed for %s: %v", lastURL, err),
			Code:   "OUTBOUND_URL_FAILED",
		}
	}
	d.latency.Valid = true
	d.latency.Ms = latency
	value := latency
	return &value, health.CheckResult{
		Pass:   true,
		Detail: fmt.Sprintf("outbound URL probe ok: %d ms", latency),
	}
}

func healthEgressURLs(cfg *config.Config) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, 3)
	add := func(raw string) {
		url := strings.TrimSpace(raw)
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		result = append(result, url)
	}
	if cfg != nil {
		for _, url := range cfg.Health.EgressURLs {
			add(url)
		}
		add(cfg.Health.URL)
	}
	add("https://www.gstatic.com/generate_204")
	add("https://cp.cloudflare.com/generate_204")
	return result
}

func (d *daemon) resetRuntimeMetrics() {
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	d.latency = latencySnapshot{}
	d.healthKick = time.Time{}
}
