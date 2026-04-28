package main

import (
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
)

func (d *daemon) refreshOutboundURLProbe(state core.State, cfg *config.Config, apiPort int, timeoutMS int) (*int64, health.CheckResult) {
	d.metricsMu.Lock()
	cache := d.latency
	d.metricsMu.Unlock()

	result := rootruntime.RefreshOutboundURLProbe(rootruntime.EgressProbeInput{
		State:     state,
		Config:    cfg,
		APIPort:   apiPort,
		TimeoutMS: timeoutMS,
		Cache:     cache,
		IO:        rootProbeIO{d: d},
	})

	d.metricsMu.Lock()
	d.latency = result.Cache
	d.metricsMu.Unlock()
	return result.Latency, result.Check
}

func (d *daemon) resetRuntimeMetrics() {
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	d.latency = latencySnapshot{}
	d.healthKick = time.Time{}
}
