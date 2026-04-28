package main

import (
	"fmt"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) applyConfigWithOperation(newCfg *config.Config, reload bool, operation runtimev2.OperationKind) error {
	if operation == "" {
		operation = runtimev2.OperationReload
	}
	wasRunning := d.coreMgr.GetState() == core.StateRunning ||
		d.coreMgr.GetState() == core.StateDegraded

	if err := d.failIfRuntimeOperationActive(); err != nil {
		return err
	}

	d.mu.Lock()
	oldCfg := d.cfg
	d.mu.Unlock()
	needsFullRestart := rootruntime.ReloadNeedsFullRestart(
		rootruntime.BuildScriptEnv(oldCfg, d.dataDir),
		rootruntime.BuildScriptEnv(newCfg, d.dataDir),
	)

	if err := newCfg.Save(d.cfgPath); err != nil {
		return fmt.Errorf("persist config: %w", err)
	}

	d.mu.Lock()
	d.cfg = newCfg
	d.mu.Unlock()

	d.coreMgr.SetConfig(newCfg)
	d.rescueMgr.SetConfig(newCfg)
	healthInterval := time.Duration(newCfg.Health.IntervalSec) * time.Second
	if healthInterval <= 0 {
		healthInterval = 30 * time.Second
	}
	healthTimeout := time.Duration(newCfg.Health.TimeoutSec) * time.Second
	if healthTimeout <= 0 {
		healthTimeout = 5 * time.Second
	}
	healthThreshold := newCfg.Health.Threshold
	if healthThreshold < 1 {
		healthThreshold = 3
	}
	tproxyPort := newCfg.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	dnsPort := newCfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	routeMark := newCfg.Proxy.Mark
	if routeMark == 0 {
		routeMark = 0x2023
	}
	d.healthMon.SetConfig(healthInterval, healthThreshold, tproxyPort, dnsPort, routeMark, newCfg.Health.URL, newCfg.Health.DNSProbeDomains, newCfg.Health.DNSIsHardReadiness, healthTimeout)
	if d.netWatcher != nil {
		d.netWatcher.SetEnv(rootruntime.BuildScriptEnv(newCfg, d.dataDir))
	}
	if err := d.syncRuntimeV2DesiredState(); err != nil {
		return fmt.Errorf("config saved: sync runtime desired state: %w", err)
	}

	if reload && wasRunning {
		if _, err := d.runtimeV2.RunOperation(operation, runtimev2.PhaseStarting, func(generation int64) error {
			return d.reloadRuntimeAfterConfigChange(newCfg, "apply config", "config saved", generation, needsFullRestart)
		}); err != nil {
			return fmt.Errorf("config saved: %w", err)
		}
	}

	return nil
}

func (d *daemon) reloadRuntimeAfterConfigChange(cfg *config.Config, context string, savedLabel string, generation int64, fullRestart bool) error {
	if err := d.failIfResetInProgress(); err != nil {
		return err
	}
	return rootruntime.ReloadAfterConfigChange(
		rootruntime.ConfigReloadInput{
			Config:      cfg,
			Context:     context,
			SavedLabel:  savedLabel,
			Generation:  generation,
			FullRestart: fullRestart,
		},
		rootruntime.ConfigReloadDeps{
			StopSubsystems: func() {
				d.stopSubsystems()
			},
			FullRestart: func(generation int64) error {
				return newRootRuntimeBackend(d).RestartAfterConfigChange(generation)
			},
			LastRuntimeReport: func() core.RuntimeStageReport {
				return d.coreMgr.LastRuntimeReport()
			},
			HotSwap: func(profile *config.NodeProfile) error {
				return d.coreMgr.HotSwap(profile)
			},
			ReapplyRuntimeRules: func(cfg *config.Config) (netstack.Report, error) {
				return rootruntime.ReapplyRuntimeRules(cfg, d.dataDir, rootruntime.BuildScriptEnv(cfg, d.dataDir), core.ExecScript)
			},
			ResetNetworkState: func(generation int64) runtimev2.ResetReport {
				return d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
			},
			ResetRescueState: func() {
				d.rescueMgr.Reset()
			},
			StartSubsystems: func() {
				d.startSubsystems()
			},
			RefreshHealth: func() runtimev2.HealthSnapshot {
				return d.runtimeV2.RefreshHealth()
			},
			RuntimeErrorCode: func(err error, fallback string) string {
				return rootruntime.RuntimeErrorCode(err, fallback)
			},
			ObserveReloadReport: func(report core.RuntimeStageReport) {
				d.setLastReloadReport(report)
			},
		},
	)
}

func (d *daemon) setLastReloadReport(report core.RuntimeStageReport) {
	d.reportMu.Lock()
	defer d.reportMu.Unlock()
	d.lastReloadReport = report
}

func (d *daemon) LastReloadReport() core.RuntimeStageReport {
	d.reportMu.Lock()
	defer d.reportMu.Unlock()
	return d.lastReloadReport
}
