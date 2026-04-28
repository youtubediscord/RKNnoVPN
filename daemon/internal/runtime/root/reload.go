package root

import (
	"fmt"
	"strconv"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func BuildScriptEnv(cfg *config.Config, dataDir string) map[string]string {
	if cfg == nil {
		return nil
	}
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
	chainProxyPorts, chainProxyUIDs, chainProxyRules := core.BuildChainedProxyProtectionEnv(cfg)

	return map[string]string{
		"RKNNOVPN_DIR":      dataDir,
		"CORE_GID":          strconv.Itoa(gid),
		"TPROXY_PORT":       strconv.Itoa(tproxyPort),
		"DNS_PORT":          strconv.Itoa(dnsPort),
		"API_PORT":          strconv.Itoa(apiPort),
		"SOCKS_PORT":        strconv.Itoa(profileInbounds.SocksPort),
		"HTTP_PORT":         strconv.Itoa(profileInbounds.HTTPPort),
		"CHAIN_PROXY_PORTS": chainProxyPorts,
		"CHAIN_PROXY_UIDS":  chainProxyUIDs,
		"CHAIN_PROXY_RULES": chainProxyRules,
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

type ConfigReloadInput struct {
	Config      *config.Config
	Context     string
	SavedLabel  string
	Generation  int64
	FullRestart bool
}

type ConfigReloadDeps struct {
	StopSubsystems      func()
	FullRestart         func(generation int64) error
	LastRuntimeReport   func() core.RuntimeStageReport
	HotSwap             func(profile *config.NodeProfile) error
	ReapplyRuntimeRules func(cfg *config.Config) (netstack.Report, error)
	ResetNetworkState   func(generation int64) runtimev2.ResetReport
	ResetRescueState    func()
	StartSubsystems     func()
	RefreshHealth       func() runtimev2.HealthSnapshot
	RuntimeErrorCode    func(err error, fallback string) string
	ObserveReloadReport func(report core.RuntimeStageReport)
}

func ReloadAfterConfigChange(input ConfigReloadInput, deps ConfigReloadDeps) error {
	context := firstNonEmpty(input.Context, "apply config")
	savedLabel := firstNonEmpty(input.SavedLabel, "config saved")
	report := core.NewRuntimeStageReport(context)
	observeReport(deps, report)
	recordStage := func(name string, status string, code string, detail string, rollbackApplied bool) {
		report.AddStage(name, status, code, detail, rollbackApplied)
		observeReport(deps, report)
	}
	failStage := func(name string, code string, err error, rollbackApplied bool) error {
		recordStage(name, "failed", code, err.Error(), rollbackApplied)
		return err
	}

	if deps.StopSubsystems != nil {
		deps.StopSubsystems()
	}
	recordStage("stop-subsystems", "ok", "", "", false)
	if input.FullRestart {
		if deps.FullRestart == nil {
			err := fmt.Errorf("full restart hook is not configured")
			err = failStage("full-restart", "RUNTIME_RESTART_FAILED", err, false)
			return fmt.Errorf("%s full restart failed; %s: %w", context, savedLabel, err)
		}
		if err := deps.FullRestart(input.Generation); err != nil {
			if resetReport := ResetReportFromError(err); resetReport != nil {
				recordStage("reset-after-full-restart-failure", resetReport.Status, "", resetReportDetail(*resetReport), resetReport.Status != "ok")
			}
			err = failStage("full-restart", reloadErrorCode(deps, err, "RUNTIME_RESTART_FAILED"), err, ResetReportFromError(err) != nil)
			return fmt.Errorf("%s full restart failed; %s: %w", context, savedLabel, err)
		}
		detail := ""
		if deps.LastRuntimeReport != nil {
			detail = deps.LastRuntimeReport().Status
		}
		recordStage("full-restart", "ok", "", detail, false)
		report.FinishOK()
		observeReport(deps, report)
		return nil
	}

	if deps.HotSwap == nil {
		err := fmt.Errorf("hot-swap hook is not configured")
		err = failStage("hot-swap", "CORE_SPAWN_FAILED", err, false)
		return err
	}
	profile := &config.NodeProfile{}
	if input.Config != nil {
		profile = input.Config.ResolveProfile()
	}
	if err := deps.HotSwap(profile); err != nil {
		resetReport := reloadResetReport(deps, input.Generation)
		recordStage("reset-after-hot-swap-failure", resetReport.Status, "", resetReportDetail(resetReport), resetReport.Status != "ok")
		err = failStage("hot-swap", reloadErrorCode(deps, err, "CORE_SPAWN_FAILED"), err, resetReport.Status != "ok")
		return RuntimeErrorWithResetReport(
			fmt.Errorf("%s hot-swap failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	detail := ""
	if deps.LastRuntimeReport != nil {
		detail = deps.LastRuntimeReport().Status
	}
	recordStage("hot-swap", "ok", "", detail, false)
	if deps.ReapplyRuntimeRules == nil {
		err := fmt.Errorf("netstack reapply hook is not configured")
		err = failStage("netstack-reapply", "RULES_NOT_APPLIED", err, false)
		return err
	}
	netReport, err := deps.ReapplyRuntimeRules(input.Config)
	if err != nil {
		resetReport := reloadResetReport(deps, input.Generation)
		recordStage("reset-after-netstack-failure", resetReport.Status, "", resetReportDetail(resetReport), resetReport.Status != "ok")
		err = failStage("netstack-reapply", reloadErrorCode(deps, err, "RULES_NOT_APPLIED"), err, resetReport.Status != "ok")
		return RuntimeErrorWithResetReport(
			fmt.Errorf("%s rules failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	recordStage("netstack-reapply", "ok", "", fmt.Sprintf("steps=%d", len(netReport.Steps)), false)
	if deps.ResetRescueState != nil {
		deps.ResetRescueState()
	}
	recordStage("rescue-reset", "ok", "", "", false)
	if deps.StartSubsystems != nil {
		deps.StartSubsystems()
	}
	recordStage("start-subsystems", "ok", "", "", false)
	snapshot := runtimev2.HealthSnapshot{}
	if deps.RefreshHealth != nil {
		snapshot = deps.RefreshHealth()
	}
	if !snapshot.Healthy() {
		resetReport := reloadResetReport(deps, input.Generation)
		recordStage("reset-after-health-failure", resetReport.Status, "", resetReportDetail(resetReport), resetReport.Status != "ok")
		err := fmt.Errorf("%s", firstNonEmpty(snapshot.LastError, "readiness gates failed"))
		err = failStage("health-refresh", firstNonEmpty(snapshot.LastCode, "READINESS_GATE_FAILED"), err, resetReport.Status != "ok")
		return RuntimeErrorWithResetReport(
			fmt.Errorf("%s readiness gates failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	recordStage("health-refresh", "ok", "", firstNonEmpty(snapshot.LastCode, "healthy"), false)
	report.FinishOK()
	observeReport(deps, report)
	return nil
}

func observeReport(deps ConfigReloadDeps, report core.RuntimeStageReport) {
	if deps.ObserveReloadReport != nil {
		deps.ObserveReloadReport(report)
	}
}

func reloadErrorCode(deps ConfigReloadDeps, err error, fallback string) string {
	if deps.RuntimeErrorCode == nil {
		return fallback
	}
	return deps.RuntimeErrorCode(err, fallback)
}

func reloadResetReport(deps ConfigReloadDeps, generation int64) runtimev2.ResetReport {
	if deps.ResetNetworkState == nil {
		return runtimev2.ResetReport{
			BackendKind: runtimev2.BackendRootTProxy,
			Generation:  generation,
			Status:      "failed",
			Errors:      []string{"reset hook is not configured"},
		}
	}
	return deps.ResetNetworkState(generation)
}

func resetReportDetail(report runtimev2.ResetReport) string {
	return fmt.Sprintf("errors=%d leftovers=%d", len(report.Errors), len(report.Leftovers))
}

func ReapplyRuntimeRules(cfg *config.Config, dataDir string, env map[string]string, execScript netstack.ExecScriptFunc) (netstack.Report, error) {
	if err := core.VerifyChainedProxyOwnerPackages(cfg); err != nil {
		report := netstack.Report{
			Operation: "apply",
			Status:    "failed",
			Steps: []netstack.Step{{
				Name:   "verify-chain-proxy-owners",
				Status: "failed",
				Detail: err.Error(),
			}},
			Errors: []string{err.Error()},
		}
		return report, &netstack.Error{Operation: "apply", Code: "LOCAL_PROXY_OWNER_MISMATCH", Report: report}
	}
	manager := netstack.New(dataDir, env, execScript)
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

func ReloadNeedsFullRestart(oldEnv map[string]string, newEnv map[string]string) bool {
	if oldEnv == nil || newEnv == nil {
		return true
	}
	for _, key := range reloadRestartEnvKeys {
		if oldEnv[key] != newEnv[key] {
			return true
		}
	}
	return false
}

var reloadRestartEnvKeys = []string{
	"CORE_GID",
	"TPROXY_PORT",
	"DNS_PORT",
	"API_PORT",
	"SOCKS_PORT",
	"HTTP_PORT",
	"CHAIN_PROXY_PORTS",
	"CHAIN_PROXY_UIDS",
	"CHAIN_PROXY_RULES",
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
}
