package main

import (
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) buildRuntimeV2HealthSnapshot(result *health.HealthResult, allowEgressProbe bool) runtimev2.HealthSnapshot {
	state := d.coreMgr.GetState()
	stageReport := d.latestRuntimeStageReport()
	input := runtimeV2HealthInput{
		State:        state,
		Result:       result,
		StageReport:  stageReport,
		RecentEgress: d.hasRecentEgress(),
		CheckedAt:    time.Now(),
	}

	snapshot := classifyRuntimeV2Health(input)
	if result != nil && allowEgressProbe && snapshot.Healthy() {
		d.mu.Lock()
		cfg := d.cfg
		d.mu.Unlock()
		apiPort := cfg.Proxy.APIPort
		_, outboundURLCheck := d.refreshOutboundURLProbe(state, cfg, apiPort, 2500)
		if result.Checks == nil {
			result.Checks = make(map[string]health.CheckResult)
		}
		result.Checks["outbound_url"] = outboundURLCheck
		input.Result = result
		snapshot = classifyRuntimeV2Health(input)
	}
	return snapshot
}

func (d *daemon) latestRuntimeStageReport() core.RuntimeStageReport {
	if d == nil {
		return core.RuntimeStageReport{}
	}
	candidates := make([]core.RuntimeStageReport, 0, 3)
	if d.coreMgr != nil {
		candidates = append(candidates, d.coreMgr.LastRuntimeReport(), d.coreMgr.LastStartReport())
	}
	candidates = append(candidates, d.LastReloadReport())

	var latest core.RuntimeStageReport
	var latestAt time.Time
	for _, report := range candidates {
		if report.Empty() {
			continue
		}
		reportAt := report.FinishedAt
		if reportAt.IsZero() {
			reportAt = report.StartedAt
		}
		if latest.Empty() || reportAt.After(latestAt) {
			latest = report
			latestAt = reportAt
		}
	}
	return latest
}

func (d *daemon) hasRecentEgress() bool {
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	return d.latency.Valid && time.Since(d.latency.CheckedAt) < 30*time.Second
}
