package main

import (
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) testNodeProbesV2(url string, timeoutMS int, nodeIDs []string) []runtimev2.NodeProbeResult {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	profiles := probeNodeProfiles(cfg)
	var runtimeHealth runtimev2.HealthSnapshot
	runtimeRunning := d.coreMgr.GetState() == core.StateRunning || d.coreMgr.GetState() == core.StateDegraded
	if runtimeRunning {
		runtimeHealth = d.buildRuntimeV2HealthSnapshot(d.healthMon.RunOnce(), false)
	}

	runner := nodeProbeRunner{
		daemon:         d,
		cfg:            cfg,
		timeoutMS:      timeoutMS,
		timeout:        time.Duration(timeoutMS) * time.Millisecond,
		requested:      requestedNodeIDs(nodeIDs),
		testURL:        resolveNodeProbeURL(url, cfg),
		runtimeRunning: runtimeRunning,
		runtimeHealth:  runtimeHealth,
		apiPort:        cfg.Proxy.APIPort,
		profileCount:   len(profiles),
	}
	return runner.run(profiles)
}
