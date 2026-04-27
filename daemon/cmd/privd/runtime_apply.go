package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) applyConfig(newCfg *config.Config, reload bool) error {
	wasRunning := d.coreMgr.GetState() == core.StateRunning ||
		d.coreMgr.GetState() == core.StateDegraded

	if err := d.failIfRuntimeOperationActive(); err != nil {
		return err
	}

	d.mu.Lock()
	oldCfg := d.cfg
	d.mu.Unlock()
	needsFullRestart := runtimeReloadNeedsFullRestart(oldCfg, newCfg, d.dataDir)

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
		d.netWatcher.SetEnv(buildScriptEnv(newCfg, d.dataDir))
	}
	if err := d.syncRuntimeV2DesiredState(); err != nil {
		return fmt.Errorf("config saved: sync runtime desired state: %w", err)
	}

	if reload && wasRunning {
		if _, err := d.runtimeV2.RunOperation(runtimev2.OperationReload, runtimev2.PhaseStarting, func(generation int64) error {
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

	report := core.NewRuntimeStageReport(context)
	d.setLastReloadReport(report)
	recordStage := func(name string, status string, code string, detail string, rollbackApplied bool) {
		report.AddStage(name, status, code, detail, rollbackApplied)
		d.setLastReloadReport(report)
	}
	failStage := func(name string, code string, err error, rollbackApplied bool) error {
		recordStage(name, "failed", code, err.Error(), rollbackApplied)
		return err
	}

	d.stopSubsystems()
	recordStage("stop-subsystems", "ok", "", "", false)
	if fullRestart {
		if err := d.restartRootBackendV2(generation); err != nil {
			if resetReport := resetReportFromRuntimeError(err); resetReport != nil {
				recordStage("reset-after-full-restart-failure", resetReport.Status, "", fmt.Sprintf("errors=%d leftovers=%d", len(resetReport.Errors), len(resetReport.Leftovers)), resetReport.Status != "ok")
			}
			err = failStage("full-restart", runtimeErrorCode(err, "RUNTIME_RESTART_FAILED"), err, resetReportFromRuntimeError(err) != nil)
			return fmt.Errorf("%s full restart failed; %s: %w", context, savedLabel, err)
		}
		recordStage("full-restart", "ok", "", d.coreMgr.LastRuntimeReport().Status, false)
		report.FinishOK()
		d.setLastReloadReport(report)
		return nil
	}
	profile := cfg.ResolveProfile()
	if err := d.coreMgr.HotSwap(profile); err != nil {
		resetReport := d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
		recordStage("reset-after-hot-swap-failure", resetReport.Status, "", fmt.Sprintf("errors=%d leftovers=%d", len(resetReport.Errors), len(resetReport.Leftovers)), resetReport.Status != "ok")
		err = failStage("hot-swap", runtimeErrorCode(err, "CORE_SPAWN_FAILED"), err, resetReport.Status != "ok")
		return runtimeErrorWithResetReport(
			fmt.Errorf("%s hot-swap failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	recordStage("hot-swap", "ok", "", d.coreMgr.LastRuntimeReport().Status, false)
	netReport, err := d.reapplyRuntimeRulesReport(cfg)
	if err != nil {
		resetReport := d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
		recordStage("reset-after-netstack-failure", resetReport.Status, "", fmt.Sprintf("errors=%d leftovers=%d", len(resetReport.Errors), len(resetReport.Leftovers)), resetReport.Status != "ok")
		err = failStage("netstack-reapply", runtimeErrorCode(err, "RULES_NOT_APPLIED"), err, resetReport.Status != "ok")
		return runtimeErrorWithResetReport(
			fmt.Errorf("%s rules failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	recordStage("netstack-reapply", "ok", "", fmt.Sprintf("steps=%d", len(netReport.Steps)), false)
	d.rescueMgr.Reset()
	recordStage("rescue-reset", "ok", "", "", false)
	d.startSubsystems()
	recordStage("start-subsystems", "ok", "", "", false)
	snapshot := d.runtimeV2.RefreshHealth()
	if !snapshot.Healthy() {
		resetReport := d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
		recordStage("reset-after-health-failure", resetReport.Status, "", fmt.Sprintf("errors=%d leftovers=%d", len(resetReport.Errors), len(resetReport.Leftovers)), resetReport.Status != "ok")
		err := fmt.Errorf("%s", firstNonEmpty(snapshot.LastError, "readiness gates failed"))
		err = failStage("health-refresh", firstNonEmpty(snapshot.LastCode, "READINESS_GATE_FAILED"), err, resetReport.Status != "ok")
		return runtimeErrorWithResetReport(
			fmt.Errorf("%s readiness gates failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	recordStage("health-refresh", "ok", "", firstNonEmpty(snapshot.LastCode, "healthy"), false)
	report.FinishOK()
	d.setLastReloadReport(report)
	return nil
}

func (d *daemon) resetNetworkState(cfg *config.Config) []string {
	report := netstack.New(d.dataDir, buildScriptEnv(cfg, d.dataDir), core.ExecScript).Cleanup()
	errors := append([]string(nil), report.Errors...)

	_, _ = core.ExecCommand("killall", "-TERM", "sing-box")
	_, _ = core.ExecCommand("killall", "-KILL", "sing-box")
	d.rescueMgr.Reset()
	d.coreMgr.ResetState()
	d.healthMon.Clear()
	d.resetRuntimeMetrics()
	return errors
}

func (d *daemon) reapplyRuntimeRules(cfg *config.Config) error {
	_, err := d.reapplyRuntimeRulesReport(cfg)
	return err
}

func (d *daemon) reapplyRuntimeRulesReport(cfg *config.Config) (netstack.Report, error) {
	manager := netstack.New(d.dataDir, buildScriptEnv(cfg, d.dataDir), core.ExecScript)
	report := manager.Apply()
	if err := report.Err(); err != nil {
		return report, err
	}
	report = manager.Verify()
	if err := report.Err(); err != nil {
		return report, err
	}
	return report, nil
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

type runtimeCodeError interface {
	RuntimeCode() string
}

func runtimeErrorCode(err error, fallback string) string {
	var coded runtimeCodeError
	if errors.As(err, &coded) {
		if code := strings.TrimSpace(coded.RuntimeCode()); code != "" {
			return code
		}
	}
	var busy *runtimev2.OperationBusyError
	if errors.As(err, &busy) && strings.TrimSpace(busy.Code) != "" {
		return busy.Code
	}
	var netErr *netstack.Error
	if errors.As(err, &netErr) && strings.TrimSpace(netErr.Code) != "" {
		return netErr.Code
	}
	return fallback
}

func buildScriptEnv(cfg *config.Config, dataDir string) map[string]string {
	gid := cfg.Proxy.GID
	if gid == 0 {
		gid = 23333
	}
	mark := cfg.Proxy.Mark
	if mark == 0 {
		mark = 0x2023
	}
	tproxyPort := cfg.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	dnsPort := cfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	apiPort := cfg.Proxy.APIPort
	profileInbounds := cfg.ResolveProfileInbounds()
	appRouting := core.BuildRuntimeAppRoutingEnv(
		cfg.Apps.Mode,
		cfg.Apps.Packages,
		cfg.Routing.AlwaysDirectApps,
		cfg.Routing.Mode,
	)
	chainProxyPorts, chainProxyUIDs := core.BuildChainedProxyProtectionEnv(cfg)

	return map[string]string{
		"PRIVSTACK_DIR":     dataDir,
		"CORE_GID":          strconv.Itoa(gid),
		"TPROXY_PORT":       strconv.Itoa(tproxyPort),
		"DNS_PORT":          strconv.Itoa(dnsPort),
		"API_PORT":          strconv.Itoa(apiPort),
		"SOCKS_PORT":        strconv.Itoa(profileInbounds.SocksPort),
		"HTTP_PORT":         strconv.Itoa(profileInbounds.HTTPPort),
		"CHAIN_PROXY_PORTS": chainProxyPorts,
		"CHAIN_PROXY_UIDS":  chainProxyUIDs,
		"FWMARK":            fmt.Sprintf("0x%x", mark),
		"ROUTE_TABLE":       "2023",
		"ROUTE_TABLE_V6":    "2024",
		"APP_MODE":          appRouting.AppMode,
		"PROXY_UIDS":        appRouting.ProxyUIDs,
		"DIRECT_UIDS":       appRouting.DirectUIDs,
		"BYPASS_UIDS":       appRouting.BypassUIDs,
		"DNS_SCOPE":         appRouting.DNSScope,
		"DNS_MODE":          appRouting.DNSMode,
		"PROXY_MODE":        "tproxy",
		"SHARING_MODE":      cfg.SharingModeEnv(),
		"SHARING_IFACES":    cfg.SharingInterfacesEnv(),
	}
}

func runtimeReloadNeedsFullRestart(oldCfg *config.Config, newCfg *config.Config, dataDir string) bool {
	if oldCfg == nil || newCfg == nil {
		return true
	}
	oldEnv := buildScriptEnv(oldCfg, dataDir)
	newEnv := buildScriptEnv(newCfg, dataDir)
	for _, key := range []string{
		"CORE_GID",
		"TPROXY_PORT",
		"DNS_PORT",
		"API_PORT",
		"SOCKS_PORT",
		"HTTP_PORT",
		"CHAIN_PROXY_PORTS",
		"CHAIN_PROXY_UIDS",
		"FWMARK",
		"ROUTE_TABLE",
		"ROUTE_TABLE_V6",
		"APP_MODE",
		"PROXY_UIDS",
		"DIRECT_UIDS",
		"BYPASS_UIDS",
		"DNS_SCOPE",
		"DNS_MODE",
		"PROXY_MODE",
		"SHARING_MODE",
		"SHARING_IFACES",
	} {
		if oldEnv[key] != newEnv[key] {
			return true
		}
	}
	return false
}
