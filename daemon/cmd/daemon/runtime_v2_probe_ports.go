package main

import (
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
)

type rootProbeIO struct {
	d *daemon
}

func (p rootProbeIO) TCPConnect(host string, port int, timeout time.Duration) (int64, error) {
	return testTCPConnect(host, port, timeout)
}

func (p rootProbeIO) BootstrapDNS(cfg *config.Config, host string, timeout time.Duration) bool {
	return p.d.probeNodeBootstrapDNS(cfg, host, timeout)
}

func (p rootProbeIO) ClashDelay(apiPort int, outboundTag string, testURL string, timeoutMS int) (int64, int, error) {
	return testClashDelay(apiPort, outboundTag, testURL, timeoutMS)
}

func (p rootProbeIO) TransparentURLProbe(cfg *config.Config, testURL string, timeoutMS int) (rootruntime.URLProbeMetrics, error) {
	metrics, err := testTransparentURLProbe(cfg, testURL, timeoutMS)
	return rootruntime.URLProbeMetrics{
		LatencyMS:     metrics.LatencyMS,
		StatusCode:    metrics.StatusCode,
		ResponseBytes: metrics.ResponseBytes,
		ThroughputBps: metrics.ThroughputBps,
	}, err
}
