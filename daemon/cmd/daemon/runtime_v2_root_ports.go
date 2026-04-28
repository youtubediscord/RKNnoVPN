package main

import (
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type rootRuntimePorts struct {
	d *daemon
}

func newRootRuntimeBackend(d *daemon) *rootruntime.Backend {
	rootPorts := rootRuntimePorts{d: d}
	return rootruntime.NewBackend(rootruntime.Dependencies{
		Core:      rootPorts,
		Profiles:  rootPorts,
		Lifecycle: rootPorts,
		Health:    rootPorts,
		Netstack:  rootPorts,
		Reset:     rootPorts,
		Probes:    rootPorts,
	})
}

func (p rootRuntimePorts) GetState() core.State {
	return p.d.coreMgr.GetState()
}

func (p rootRuntimePorts) Start(profile *config.NodeProfile) error {
	return p.d.coreMgr.Start(profile)
}

func (p rootRuntimePorts) Stop() error {
	return p.d.coreMgr.Stop()
}

func (p rootRuntimePorts) RuntimeProfile() (*config.NodeProfile, bool) {
	p.d.mu.Lock()
	defer p.d.mu.Unlock()
	return p.d.cfg.ResolveProfile(), len(config.ProfilesFromConfigNodes(p.d.cfg)) > 0
}

func (p rootRuntimePorts) BeginRuntimeStartOperation() uint64 {
	return p.d.beginRuntimeStartOperation()
}

func (p rootRuntimePorts) BeginRuntimeStopOperation() uint64 {
	return p.d.beginRuntimeStopOperation()
}

func (p rootRuntimePorts) MarkRuntimeStartFailed(epoch uint64) {
	p.d.markRuntimeStartFailed(epoch)
}

func (p rootRuntimePorts) ResetRescueState() {
	p.d.rescueMgr.Reset()
}

func (p rootRuntimePorts) StartSubsystems() {
	p.d.startSubsystems()
}

func (p rootRuntimePorts) StopSubsystems() {
	p.d.stopSubsystems()
}

func (p rootRuntimePorts) CurrentRuntimeHealth() runtimev2.HealthSnapshot {
	return p.d.buildRuntimeV2HealthSnapshot(p.d.healthMon.LastResult(), false)
}

func (p rootRuntimePorts) RefreshRuntimeHealth(allowEgressProbe bool) runtimev2.HealthSnapshot {
	return p.d.buildRuntimeV2HealthSnapshot(p.d.healthMon.RunOnce(), allowEgressProbe)
}

func (p rootRuntimePorts) ReapplyRuntimeRules() error {
	p.d.mu.Lock()
	cfg := p.d.cfg
	p.d.mu.Unlock()
	_, err := rootruntime.ReapplyRuntimeRules(cfg, p.d.dataDir, buildScriptEnv(cfg, p.d.dataDir), core.ExecScript)
	return err
}

func (p rootRuntimePorts) RecoverStaleResetLock(generation int64) (*runtimev2.ResetReport, error) {
	return p.d.recoverStaleResetLock(generation)
}

func (p rootRuntimePorts) ResetNetworkStateReport(generation int64, backend runtimev2.BackendKind) runtimev2.ResetReport {
	return p.d.resetNetworkStateReport(generation, backend)
}

func (p rootRuntimePorts) ShouldSkipRootReconcile() (bool, string) {
	return p.d.shouldSkipRootReconcile()
}

func (p rootRuntimePorts) TestNodeProbes(url string, timeoutMS int, nodeIDs []string) []runtimev2.NodeProbeResult {
	return p.d.testNodeProbesV2(url, timeoutMS, nodeIDs)
}
