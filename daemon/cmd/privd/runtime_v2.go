package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type rootBackendV2 struct {
	d *daemon
}

func (b *rootBackendV2) Kind() runtimev2.BackendKind {
	return runtimev2.BackendRootTProxy
}

func (b *rootBackendV2) Supported() (bool, string) {
	return true, ""
}

func (b *rootBackendV2) Start(desired runtimev2.DesiredState) error {
	epoch := b.d.beginRuntimeStartOperation()
	state := b.d.coreMgr.GetState()
	if state == core.StateRunning || state == core.StateDegraded {
		return nil
	}

	b.d.mu.Lock()
	profile := b.d.cfg.ResolveProfile()
	hasPanelNodes := len(b.d.cfg.Panel.Nodes) > 0
	b.d.mu.Unlock()

	if profile.Address == "" && !hasPanelNodes {
		b.d.markRuntimeStartFailed(epoch)
		return fmt.Errorf("no node configured (address is empty)")
	}
	if err := b.d.coreMgr.Start(profile); err != nil {
		b.d.markRuntimeStartFailed(epoch)
		return err
	}
	b.d.rescueMgr.Reset()
	b.d.startSubsystems()

	snapshot := b.RefreshHealth()
	if !snapshot.Healthy() {
		b.d.resetNetworkStateReport(0, runtimev2.BackendRootTProxy)
		b.d.markRuntimeStartFailed(epoch)
		return fmt.Errorf("readiness gates failed after start: %s", snapshot.LastError)
	}
	return nil
}

func (b *rootBackendV2) Stop() error {
	b.d.beginRuntimeStopOperation()
	b.d.stopSubsystems()
	return b.d.coreMgr.Stop()
}

func (b *rootBackendV2) Reset(generation int64) runtimev2.ResetReport {
	b.d.beginRuntimeStopOperation()
	return b.d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
}

func (b *rootBackendV2) Restart(desired runtimev2.DesiredState, generation int64) error {
	b.d.beginRuntimeStartOperation()
	return b.d.restartRootBackendV2()
}

func (b *rootBackendV2) HandleNetworkChange(generation int64) error {
	return b.d.reconcileRootRuntime("network-change")
}

func (b *rootBackendV2) CurrentHealth() runtimev2.HealthSnapshot {
	return b.d.buildRuntimeV2HealthSnapshot(b.d.healthMon.LastResult(), false)
}

func (b *rootBackendV2) RefreshHealth() runtimev2.HealthSnapshot {
	result := b.d.healthMon.RunOnce()
	return b.d.buildRuntimeV2HealthSnapshot(result, true)
}

func (b *rootBackendV2) TestNodes(desired runtimev2.DesiredState, url string, timeoutMS int, nodeIDs []string) ([]runtimev2.NodeProbeResult, error) {
	return b.d.testNodeProbesV2(url, timeoutMS, nodeIDs), nil
}

func (d *daemon) initRuntimeV2() {
	d.runtimeV2 = runtimev2.NewOrchestrator(
		d.desiredStateV2(),
		&rootBackendV2{d: d},
	)
}

func (d *daemon) beginRuntimeStartOperation() uint64 {
	d.runtimeOpMu.Lock()
	defer d.runtimeOpMu.Unlock()
	d.runtimeOpEpoch++
	d.runtimeDesiredRunning = true
	return d.runtimeOpEpoch
}

func (d *daemon) beginRuntimeStopOperation() uint64 {
	d.runtimeOpMu.Lock()
	defer d.runtimeOpMu.Unlock()
	d.runtimeOpEpoch++
	d.runtimeDesiredRunning = false
	return d.runtimeOpEpoch
}

func (d *daemon) markRuntimeStartFailed(epoch uint64) {
	d.runtimeOpMu.Lock()
	defer d.runtimeOpMu.Unlock()
	if d.runtimeOpEpoch == epoch {
		d.runtimeDesiredRunning = false
	}
}

func (d *daemon) currentRuntimeOperationEpoch() uint64 {
	d.runtimeOpMu.Lock()
	defer d.runtimeOpMu.Unlock()
	return d.runtimeOpEpoch
}

func (d *daemon) canRunRuntimeRecovery(epoch uint64) bool {
	d.runtimeOpMu.Lock()
	allowed := d.runtimeDesiredRunning && d.runtimeOpEpoch == epoch
	d.runtimeOpMu.Unlock()
	if !allowed {
		return false
	}
	if skip, _ := d.shouldSkipRootReconcile(); skip {
		return false
	}
	state := d.coreMgr.GetState()
	return state == core.StateRunning || state == core.StateDegraded || state == core.StateRescue
}

func (d *daemon) desiredStateV2() runtimev2.DesiredState {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	backendKind := runtimev2.BackendKind(strings.TrimSpace(cfg.RuntimeV2.BackendKind))
	if backendKind == "" {
		backendKind = runtimev2.BackendRootTProxy
	}
	fallback := runtimev2.FallbackPolicy(strings.TrimSpace(cfg.RuntimeV2.FallbackPolicy))
	if fallback == "" {
		fallback = runtimev2.FallbackOfferReset
	}

	appSelection := runtimev2.AppSelection{
		BypassPackages: append([]string(nil), cfg.Routing.AlwaysDirectApps...),
	}
	switch cfg.Apps.Mode {
	case "whitelist":
		appSelection.ProxyPackages = append([]string(nil), cfg.Apps.Packages...)
	case "blacklist":
		appSelection.BypassPackages = append(appSelection.BypassPackages, cfg.Apps.Packages...)
	}

	return runtimev2.DesiredState{
		BackendKind:     backendKind,
		ActiveProfileID: cfg.Panel.ActiveNodeID,
		RoutingMode:     mapRoutingModeV2(cfg),
		AppSelection:    appSelection,
		DNSPolicy: runtimev2.DNSPolicy{
			RemoteDNS: cfg.DNS.ProxyDNS,
			DirectDNS: cfg.DNS.DirectDNS,
			FakeDNS:   cfg.DNS.FakeIP,
			IPv6Mode:  cfg.IPv6.Mode,
		},
		FallbackPolicy: fallback,
	}
}

func mapRoutingModeV2(cfg *config.Config) string {
	switch cfg.Routing.Mode {
	case "all":
		if cfg.Apps.Mode == "whitelist" || cfg.Apps.Mode == "all" {
			return "PER_APP"
		}
		if cfg.Apps.Mode == "blacklist" {
			return "PER_APP_BYPASS"
		}
		return "PROXY_ALL"
	case "whitelist":
		return "PER_APP"
	case "blacklist":
		return "PER_APP_BYPASS"
	case "rules":
		return "RULES"
	case "direct":
		return "DIRECT"
	default:
		return "PROXY_ALL"
	}
}

func (d *daemon) syncRuntimeV2DesiredState() {
	if d.runtimeV2 == nil {
		return
	}
	_ = d.runtimeV2.ApplyDesiredState(d.desiredStateV2())
}

func (d *daemon) restartRootBackendV2() error {
	d.stopSubsystems()
	if d.coreMgr.GetState() != core.StateStopped {
		if err := d.coreMgr.Stop(); err != nil {
			d.resetNetworkStateReport(0, runtimev2.BackendRootTProxy)
			return fmt.Errorf("restart stop failed: %w", err)
		}
	}

	d.mu.Lock()
	profile := d.cfg.ResolveProfile()
	hasPanelNodes := len(d.cfg.Panel.Nodes) > 0
	d.mu.Unlock()

	if profile.Address == "" && !hasPanelNodes {
		return fmt.Errorf("no node configured (address is empty)")
	}
	if err := d.coreMgr.Start(profile); err != nil {
		d.resetNetworkStateReport(0, runtimev2.BackendRootTProxy)
		return fmt.Errorf("restart start failed: %w", err)
	}
	d.rescueMgr.Reset()
	d.startSubsystems()

	snapshot := d.buildRuntimeV2HealthSnapshot(d.healthMon.RunOnce(), false)
	if !snapshot.Healthy() {
		d.resetNetworkStateReport(0, runtimev2.BackendRootTProxy)
		return fmt.Errorf("restart readiness gates failed: %s", snapshot.LastError)
	}
	return nil
}

func (d *daemon) reconcileRootRuntime(reason string) error {
	state := d.coreMgr.GetState()
	if state != core.StateRunning && state != core.StateDegraded {
		return nil
	}
	if skip, _ := d.shouldSkipRootReconcile(); skip {
		return nil
	}

	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	if err := d.reapplyRuntimeRules(cfg); err != nil {
		d.resetNetworkStateReport(0, runtimev2.BackendRootTProxy)
		return fmt.Errorf("%s reapply failed: %w", reason, err)
	}

	snapshot := d.buildRuntimeV2HealthSnapshot(d.healthMon.RunOnce(), false)
	if snapshot.Healthy() {
		return nil
	}

	d.resetNetworkStateReport(0, runtimev2.BackendRootTProxy)
	return fmt.Errorf("%s readiness gates failed: %s", reason, snapshot.LastError)
}

func (d *daemon) buildRuntimeV2HealthSnapshot(result *health.HealthResult, allowEgressProbe bool) runtimev2.HealthSnapshot {
	state := d.coreMgr.GetState()
	snapshot := runtimev2.HealthSnapshot{
		CoreReady: state == core.StateRunning,
		CheckedAt: time.Now(),
	}
	if state == core.StateDegraded {
		snapshot.CoreReady = true
	}
	if result == nil {
		snapshot.EgressReady = d.hasRecentEgress()
		return snapshot
	}

	snapshot.CheckedAt = result.Timestamp
	singboxReady := false
	tproxyReady := false
	iptablesReady := false
	routeReady := false
	dnsListenerReady := true
	dnsLookupReady := true
	for name, check := range result.Checks {
		switch name {
		case "singbox_alive", "tproxy_port":
			if name == "singbox_alive" {
				singboxReady = check.Pass
			}
			if name == "tproxy_port" {
				tproxyReady = check.Pass
			}
		case "dns_listener":
			dnsListenerReady = check.Pass
		case "dns":
			dnsLookupReady = check.Pass
		case "iptables", "routing":
			if name == "iptables" {
				iptablesReady = check.Pass
			}
			if name == "routing" {
				routeReady = check.Pass
			}
		}
	}
	snapshot.CoreReady = singboxReady && tproxyReady
	snapshot.RoutingReady = iptablesReady && routeReady
	snapshot.DNSReady = dnsListenerReady && dnsLookupReady

	if allowEgressProbe && snapshot.Healthy() {
		d.mu.Lock()
		cfg := d.cfg
		d.mu.Unlock()
		apiPort := cfg.Proxy.APIPort
		_, outboundURLCheck := d.refreshOutboundURLProbe(state, cfg, apiPort, 2500)
		if result.Checks == nil {
			result.Checks = make(map[string]health.CheckResult)
		}
		result.Checks["outbound_url"] = outboundURLCheck
	}
	if check, ok := result.Checks["outbound_url"]; ok {
		snapshot.EgressReady = check.Pass
	} else {
		snapshot.EgressReady = d.hasRecentEgress()
	}
	snapshot.Checks = runtimeHealthChecks(result)

	diagnostic := firstFailedGateDiagnostic(result, snapshot)
	if snapshot.LastCode == "" {
		snapshot.LastCode = diagnostic.Code
	}
	if snapshot.LastError == "" {
		snapshot.LastError = diagnostic.Detail
	}
	return snapshot
}

func runtimeHealthChecks(result *health.HealthResult) map[string]runtimev2.HealthCheckSnapshot {
	if result == nil || len(result.Checks) == 0 {
		return nil
	}
	checks := make(map[string]runtimev2.HealthCheckSnapshot, len(result.Checks))
	for name, check := range result.Checks {
		checks[name] = runtimev2.HealthCheckSnapshot{
			Pass:   check.Pass,
			Code:   check.Code,
			Detail: check.Detail,
		}
	}
	return checks
}

func firstFailedGate(result *health.HealthResult, snapshot runtimev2.HealthSnapshot) string {
	return firstFailedGateDiagnostic(result, snapshot).Detail
}

type healthGateDiagnostic struct {
	Code   string
	Detail string
}

func firstFailedGateDiagnostic(result *health.HealthResult, snapshot runtimev2.HealthSnapshot) healthGateDiagnostic {
	if result != nil {
		for _, name := range []string{"singbox_alive", "tproxy_port", "iptables", "routing"} {
			if check, ok := result.Checks[name]; ok && !check.Pass {
				return healthGateDiagnostic{
					Code:   firstNonEmpty(check.Code, "READINESS_GATE_FAILED"),
					Detail: formatHealthCheckError(name, check),
				}
			}
		}
		if snapshot.Healthy() {
			for _, name := range []string{"dns_listener", "dns"} {
				if check, ok := result.Checks[name]; ok && !check.Pass {
					return healthGateDiagnostic{
						Code:   firstNonEmpty(check.Code, "PROXY_DNS_UNAVAILABLE"),
						Detail: fmt.Sprintf("operational degraded: proxy DNS unavailable: %s", formatHealthCheckError(name, check)),
					}
				}
			}
		}
	}
	if !snapshot.Healthy() {
		return healthGateDiagnostic{Code: "READINESS_GATE_FAILED", Detail: "one or more readiness gates are red"}
	}
	if !snapshot.EgressReady {
		if result != nil {
			if check, ok := result.Checks["outbound_url"]; ok && !check.Pass {
				return healthGateDiagnostic{
					Code:   firstNonEmpty(check.Code, "OUTBOUND_URL_FAILED"),
					Detail: fmt.Sprintf("operational degraded: outbound URL probe failed: %s", formatHealthCheckError("outbound_url", check)),
				}
			}
		}
		return healthGateDiagnostic{Code: "OUTBOUND_URL_FAILED", Detail: "operational degraded: no recent successful egress probe"}
	}
	if !snapshot.OperationalHealthy() {
		return healthGateDiagnostic{Code: "OPERATIONAL_DEGRADED", Detail: "operational degraded: one or more operational health signals are red"}
	}
	return healthGateDiagnostic{}
}

func formatHealthCheckError(name string, check health.CheckResult) string {
	if check.Code != "" {
		return fmt.Sprintf("%s: %s: %s", name, check.Code, check.Detail)
	}
	return fmt.Sprintf("%s: %s", name, check.Detail)
}

func (d *daemon) hasRecentEgress() bool {
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	return (d.egress.Valid && time.Since(d.egress.CheckedAt) < 30*time.Second) ||
		(d.latency.Valid && time.Since(d.latency.CheckedAt) < 30*time.Second)
}

func (d *daemon) resetNetworkStateReport(generation int64, backend runtimev2.BackendKind) runtimev2.ResetReport {
	report := runtimev2.ResetReport{
		BackendKind: backend,
		Generation:  generation,
		Status:      "ok",
	}

	runStep := func(name string, fn func() (string, string, error)) {
		status, detail, err := fn()
		if status == "" {
			status = "ok"
		}
		step := runtimev2.ResetStep{Name: name, Status: status, Detail: detail}
		if err != nil {
			step.Status = "failed"
			step.Detail = err.Error()
			report.Status = "partial"
			report.Errors = append(report.Errors, name+": "+err.Error())
		}
		report.Steps = append(report.Steps, step)
	}
	runSimpleStep := func(name string, fn func() error) {
		runStep(name, func() (string, string, error) {
			if err := fn(); err != nil {
				return "", "", err
			}
			return "ok", "", nil
		})
	}

	runSimpleStep("enter-reset-mode", d.enterResetMode)

	runSimpleStep("stop-subsystems", func() error {
		d.stopSubsystems()
		return nil
	})

	runStep("stop-core", func() (string, string, error) {
		if err := d.coreMgr.RescueReset(); err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})

	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	env := buildScriptEnv(cfg, d.dataDir)

	runSimpleStep("rescue-cleanup-script", func() error {
		return core.ExecScript(filepath.Join(d.dataDir, "scripts", "rescue_reset.sh"), "daemon-reset", env)
	})

	runStep("clear-runtime-state", func() (string, string, error) {
		d.rescueMgr.Reset()
		d.coreMgr.ResetState()
		d.healthMon.Clear()
		d.resetRuntimeMetrics()
		return "ok", "", nil
	})

	runStep("remove-stale-runtime-files", func() (string, string, error) {
		removed, err := d.removeStaleRuntimeFiles()
		if err != nil {
			return "", "", err
		}
		if len(removed) == 0 {
			return "already_clean", "", nil
		}
		return "ok", strings.Join(removed, ", "), nil
	})

	runStep("verify-cleanup", func() (string, string, error) {
		leftovers := d.collectNetworkLeftovers(cfg)
		report.Leftovers = leftovers
		if len(leftovers) == 0 {
			return "ok", "", nil
		}
		report.RebootRequired = true
		return "failed", strings.Join(leftovers, "; "), fmt.Errorf("%d leftover(s) after reset", len(leftovers))
	})

	runSimpleStep("leave-reset-mode", d.leaveResetMode)

	if len(report.Errors) > 0 {
		report.Status = "partial"
	}
	if len(report.Leftovers) > 0 {
		report.Status = "partial"
		report.RebootRequired = true
	}
	return report
}

func (d *daemon) enterResetMode() error {
	if err := os.MkdirAll(filepath.Join(d.dataDir, "run"), 0750); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(d.dataDir, "config"), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(d.resetLockPath(), []byte(time.Now().Format(time.RFC3339)+"\n"), 0640); err != nil {
		return err
	}
	_ = os.Remove(d.activeFilePath())
	if err := os.WriteFile(d.manualFlagPath(), []byte("network reset\n"), 0600); err != nil {
		return err
	}
	return nil
}

func (d *daemon) leaveResetMode() error {
	if err := os.Remove(d.resetLockPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *daemon) shouldSkipRootReconcile() (bool, string) {
	if _, err := os.Stat(d.resetLockPath()); err == nil {
		return true, "reset lock is present"
	}
	if _, err := os.Stat(d.manualFlagPath()); err == nil {
		return true, "manual mode is enabled"
	}
	if _, err := os.Stat(d.activeFilePath()); err != nil {
		if os.IsNotExist(err) {
			return true, "runtime is not marked active"
		}
		return true, "active marker is not readable: " + err.Error()
	}
	return false, ""
}

func (d *daemon) resetLockPath() string {
	return filepath.Join(d.dataDir, "run", "reset.lock")
}

func (d *daemon) activeFilePath() string {
	return filepath.Join(d.dataDir, "run", "active")
}

func (d *daemon) manualFlagPath() string {
	return filepath.Join(d.dataDir, "config", "manual")
}

func isIgnorableKillallError(output string, err error) bool {
	if err == nil {
		return false
	}
	return err.Error() == "exit status 1"
}

func (d *daemon) removeStaleRuntimeFiles() ([]string, error) {
	files := []string{
		filepath.Join(d.dataDir, "run", "singbox.pid"),
		filepath.Join(d.dataDir, "run", "active"),
		filepath.Join(d.dataDir, "run", "net_change.lock"),
		filepath.Join(d.dataDir, "run", "iptables.rules"),
		filepath.Join(d.dataDir, "run", "ip6tables.rules"),
		filepath.Join(d.dataDir, "run", "iptables_backup.rules"),
		filepath.Join(d.dataDir, "run", "ip6tables_backup.rules"),
		filepath.Join(d.dataDir, "run", "env.sh"),
	}

	removed := make([]string, 0, len(files))
	errs := make([]string, 0)
	for _, path := range files {
		if err := os.Remove(path); err == nil {
			removed = append(removed, filepath.Base(path))
		} else if !os.IsNotExist(err) {
			errs = append(errs, filepath.Base(path)+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return removed, nil
}

func (d *daemon) collectNetworkLeftovers(cfg *config.Config) []string {
	if cfg == nil {
		return []string{"config unavailable for cleanup verification"}
	}

	env := buildScriptEnv(cfg, d.dataDir)
	leftovers := make([]string, 0)
	add := func(format string, args ...interface{}) {
		leftovers = append(leftovers, fmt.Sprintf(format, args...))
	}

	for _, spec := range []struct {
		bin   string
		table string
	}{
		{bin: "iptables", table: "raw"},
		{bin: "iptables", table: "mangle"},
		{bin: "iptables", table: "nat"},
		{bin: "iptables", table: "filter"},
		{bin: "ip6tables", table: "raw"},
		{bin: "ip6tables", table: "mangle"},
		{bin: "ip6tables", table: "nat"},
		{bin: "ip6tables", table: "filter"},
	} {
		out, err := core.ExecCommand(spec.bin, "-w", "100", "-t", spec.table, "-S")
		if err != nil {
			if isMissingKernelTableOutput(out) {
				continue
			}
			if strings.TrimSpace(out) != "" {
				add("%s %s check failed: %v: %s", spec.bin, spec.table, err, firstLine(out))
			} else {
				add("%s %s check failed: %v", spec.bin, spec.table, err)
			}
			continue
		}
		if line := firstLineContaining(out, "PRIVSTACK"); line != "" {
			add("%s %s rule remains: %s", spec.bin, spec.table, line)
		}
	}

	mark := strings.ToLower(env["FWMARK"])
	routeTable := env["ROUTE_TABLE"]
	routeTableV6 := env["ROUTE_TABLE_V6"]
	for _, spec := range []struct {
		name  string
		args  []string
		table string
	}{
		{name: "ip rule", args: []string{"rule", "show"}, table: routeTable},
		{name: "ip -6 rule", args: []string{"-6", "rule", "show"}, table: routeTableV6},
	} {
		out, err := core.ExecCommand("ip", spec.args...)
		if err != nil {
			add("%s check failed: %v", spec.name, err)
			continue
		}
		for _, line := range splitLines(out) {
			lower := strings.ToLower(line)
			if strings.Contains(lower, mark) || strings.Contains(lower, "lookup "+spec.table) {
				add("%s remains: %s", spec.name, strings.TrimSpace(line))
				break
			}
		}
	}

	for _, spec := range []struct {
		name string
		args []string
	}{
		{name: "ip route table " + routeTable, args: []string{"route", "show", "table", routeTable}},
		{name: "ip -6 route table " + routeTableV6, args: []string{"-6", "route", "show", "table", routeTableV6}},
	} {
		out, err := core.ExecCommand("ip", spec.args...)
		if err != nil {
			if isMissingRouteTableOutput(out) {
				continue
			}
			if strings.TrimSpace(out) == "" {
				add("%s check failed: %v", spec.name, err)
			} else {
				add("%s check failed: %v: %s", spec.name, err, firstLine(out))
			}
			continue
		}
		if strings.TrimSpace(out) != "" {
			add("%s still has routes: %s", spec.name, firstLine(out))
		}
	}

	if out, _ := core.ExecCommand("pidof", "sing-box"); strings.TrimSpace(out) != "" {
		add("sing-box process still running: %s", strings.TrimSpace(out))
	}

	for _, port := range effectiveLocalPorts(cfg) {
		if isTCPPortListening("127.0.0.1", port, 150*time.Millisecond) {
			add("localhost TCP port %d still listening", port)
		}
	}

	for _, path := range []string{
		filepath.Join(d.dataDir, "run", "singbox.pid"),
		filepath.Join(d.dataDir, "run", "active"),
		filepath.Join(d.dataDir, "run", "net_change.lock"),
		filepath.Join(d.dataDir, "run", "iptables.rules"),
		filepath.Join(d.dataDir, "run", "ip6tables.rules"),
		filepath.Join(d.dataDir, "run", "env.sh"),
	} {
		if _, err := os.Stat(path); err == nil {
			add("stale runtime file remains: %s", path)
		}
	}

	return leftovers
}

func isMissingKernelTableOutput(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "table does not exist") ||
		strings.Contains(lower, "can't initialize") ||
		strings.Contains(lower, "does not exist")
}

func isMissingRouteTableOutput(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "fib table does not exist") ||
		strings.Contains(lower, "no such process") ||
		strings.Contains(lower, "no such file")
}

func effectiveLocalPorts(cfg *config.Config) []int {
	if cfg == nil {
		return nil
	}
	panelInbounds := cfg.ResolvePanelInbounds()
	ports := []int{
		valueOrDefaultInt(cfg.Proxy.TProxyPort, 10853),
		valueOrDefaultInt(cfg.Proxy.DNSPort, 10856),
		cfg.Proxy.APIPort,
		panelInbounds.SocksPort,
		panelInbounds.HTTPPort,
	}
	seen := make(map[int]bool, len(ports))
	result := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		result = append(result, port)
	}
	return result
}

func valueOrDefaultInt(value int, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func isTCPPortListening(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func firstLineContaining(text string, needle string) string {
	for _, line := range splitLines(text) {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func firstLine(text string) string {
	for _, line := range splitLines(text) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func (d *daemon) persistDesiredStateV2(desired runtimev2.DesiredState) error {
	d.mu.Lock()
	currentCfg := d.cfg
	d.mu.Unlock()

	raw, err := json.Marshal(currentCfg)
	if err != nil {
		return err
	}
	nextCfg := config.DefaultConfig()
	if err := json.Unmarshal(raw, nextCfg); err != nil {
		return err
	}

	if desired.BackendKind != "" {
		nextCfg.RuntimeV2.BackendKind = string(desired.BackendKind)
	}
	if desired.FallbackPolicy != "" {
		nextCfg.RuntimeV2.FallbackPolicy = string(desired.FallbackPolicy)
	}
	d.mu.Lock()
	currentPanel := d.cfg.Panel
	nextCfg.Panel = currentPanel
	d.mu.Unlock()
	if desired.ActiveProfileID != "" {
		nextCfg.Panel.ActiveNodeID = desired.ActiveProfileID
	}
	nextCfg.SyncFromPanel(true)
	if err := config.SavePanel(d.panelPath, nextCfg.Panel); err != nil {
		return err
	}
	if err := d.applyConfig(nextCfg, false); err != nil {
		_ = config.SavePanel(d.panelPath, currentPanel)
		return err
	}
	return nil
}

func (d *daemon) handleBackendStatus(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if d.runtimeV2 == nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: "v2 runtime is not initialized"}
	}
	state := d.coreMgr.GetState()
	if state == core.StateRunning || state == core.StateDegraded {
		healthSnapshot := d.runtimeV2.CurrentHealth()
		if healthSnapshot.CheckedAt.IsZero() || time.Since(healthSnapshot.CheckedAt) > 10*time.Second {
			go d.runtimeV2.RefreshHealth()
		}
	}
	return d.runtimeV2.Status(), nil
}

func (d *daemon) handleBackendApplyDesiredState(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: desired state object",
		}
	}
	var desired runtimev2.DesiredState
	if err := json.Unmarshal(*params, &desired); err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if desired.BackendKind == "" {
		desired.BackendKind = d.desiredStateV2().BackendKind
	}
	if desired.FallbackPolicy == "" {
		desired.FallbackPolicy = d.desiredStateV2().FallbackPolicy
	}
	if desired.ActiveProfileID == "" {
		desired.ActiveProfileID = d.desiredStateV2().ActiveProfileID
	}
	if err := d.runtimeV2.ApplyDesiredState(desired); err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: err.Error()}
	}
	if err := d.persistDesiredStateV2(desired); err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: "persist desired state: " + err.Error()}
	}
	_ = d.runtimeV2.ApplyDesiredState(d.desiredStateV2())
	return d.runtimeV2.Status(), nil
}

func (d *daemon) handleBackendStart(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	d.syncRuntimeV2DesiredState()
	status, err := d.runtimeV2.Start()
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}
	return status, nil
}

func (d *daemon) handleBackendStop(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	status, err := d.runtimeV2.Stop()
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}
	return status, nil
}

func (d *daemon) handleBackendRestart(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	d.syncRuntimeV2DesiredState()
	status, err := d.runtimeV2.Restart()
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}
	return status, nil
}

func (d *daemon) handleBackendReset(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	return d.runtimeV2.Reset(), nil
}

func (d *daemon) handleDiagnosticsHealth(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	return d.runtimeV2.RefreshHealth(), nil
}

func (d *daemon) handleDiagnosticsTestNodes(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	var p struct {
		NodeIDs   []string `json:"node_ids"`
		URL       string   `json:"url"`
		TimeoutMS int      `json:"timeout_ms"`
	}
	if params != nil {
		if err := json.Unmarshal(*params, &p); err != nil {
			return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
		}
	}
	if p.TimeoutMS <= 0 {
		p.TimeoutMS = 5000
	}

	results, err := d.runtimeV2.TestNodes(p.URL, p.TimeoutMS, p.NodeIDs)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}
	return map[string]interface{}{
		"url":     p.URL,
		"results": results,
	}, nil
}

func (d *daemon) testNodeProbesV2(url string, timeoutMS int, nodeIDs []string) []runtimev2.NodeProbeResult {
	timeout := time.Duration(timeoutMS) * time.Millisecond
	requested := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		requested[id] = true
	}

	d.mu.Lock()
	cfg := d.cfg
	profiles := config.ProfilesFromPanelNodes(cfg)
	if len(profiles) == 0 {
		profile := cfg.ResolveProfile()
		if profile.Address != "" {
			profile.Tag = "proxy"
			profiles = []*config.NodeProfile{profile}
		}
	}
	apiPort := cfg.Proxy.APIPort
	testURL := strings.TrimSpace(url)
	if testURL == "" {
		testURL = strings.TrimSpace(cfg.Health.URL)
	}
	if testURL == "" {
		testURL = "https://www.gstatic.com/generate_204"
	}
	d.mu.Unlock()

	var runtimeHealth runtimev2.HealthSnapshot
	runtimeRunning := d.coreMgr.GetState() == core.StateRunning || d.coreMgr.GetState() == core.StateDegraded
	if runtimeRunning {
		runtimeHealth = d.buildRuntimeV2HealthSnapshot(d.healthMon.RunOnce(), false)
	}

	results := make([]runtimev2.NodeProbeResult, 0, len(profiles))
	for _, profile := range profiles {
		if len(requested) > 0 && !requested[profile.ID] {
			continue
		}

		result := runtimev2.NodeProbeResult{
			ID:        profile.ID,
			Name:      firstNonEmpty(profile.Name, profile.Tag, profile.Address),
			Protocol:  profile.Protocol,
			Server:    profile.Address,
			Port:      profile.Port,
			TCPStatus: "not_run",
			URLStatus: "not_run",
			Verdict:   "unknown",
		}

		tcpMS, tcpErr := testTCPConnect(profile.Address, profile.Port, timeout)
		if tcpErr == nil {
			result.TCPDirect = &tcpMS
			result.TCPStatus = "ok"
		} else {
			result.TCPStatus = "fail"
			result.ErrorClass = "tcp_direct_failed"
		}

		result.DNSBootstrap = d.probeNodeBootstrapDNS(cfg, profile.Address, timeout)
		if !result.DNSBootstrap && result.ErrorClass == "" {
			result.ErrorClass = "dns_bootstrap_failed"
		}

		if runtimeRunning {
			var urlMS int64
			var urlErr error
			if apiPort > 0 {
				urlMS, _, urlErr = testClashDelay(apiPort, profile.Tag, testURL, timeoutMS)
			} else if len(profiles) == 1 {
				urlMS, _, urlErr = testTransparentURLDelay(cfg, testURL, timeoutMS)
			} else {
				urlErr = fmt.Errorf("api_disabled")
			}
			if urlErr == nil {
				result.TunnelDelay = &urlMS
				result.URLStatus = "ok"
				result.Verdict = "usable"
				result.ErrorClass = "ok"
			} else {
				result.URLStatus = "fail"
				result.Verdict = "unusable"
				if result.ErrorClass == "" {
					result.ErrorClass = classifyRuntimeURLTestFailure(urlErr, runtimeHealth)
				}
			}
		} else {
			result.URLStatus = "fail"
			result.Verdict = "unusable"
			if result.ErrorClass == "" {
				result.ErrorClass = "tunnel_unavailable"
			}
		}

		if result.TCPStatus == "ok" && result.URLStatus != "ok" {
			result.Verdict = "unusable"
		}

		results = append(results, result)
	}

	return results
}

func classifyRuntimeURLTestFailure(err error, snapshot runtimev2.HealthSnapshot) string {
	if !snapshot.Healthy() {
		return "runtime_not_ready"
	}
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "api_disabled") {
		return "api_disabled"
	}
	switch snapshot.LastCode {
	case "DNS_LISTENER_DOWN",
		"DNS_LOOKUP_TIMEOUT",
		"DNS_EMPTY_RESPONSE",
		"DNS_LOOKUP_FAILED",
		"PROXY_DNS_UNAVAILABLE":
		return "proxy_dns_unavailable"
	case "OUTBOUND_URL_FAILED":
		return "outbound_url_failed"
	case "TPROXY_PORT_DOWN",
		"RULES_NOT_APPLIED",
		"ROUTING_CHECK_FAILED",
		"ROUTING_V4_MISSING",
		"ROUTING_V6_MISSING",
		"ROUTING_NOT_APPLIED",
		"CORE_PID_MISSING",
		"CORE_PID_LOOKUP_FAILED",
		"CORE_PROCESS_DEAD":
		return "runtime_not_ready"
	}
	if !snapshot.DNSReady {
		return "proxy_dns_unavailable"
	}
	if !snapshot.EgressReady {
		return "runtime_degraded"
	}
	if err != nil {
		detail := strings.ToLower(err.Error())
		if strings.Contains(detail, "127.0.0.1") ||
			strings.Contains(detail, "connection refused") ||
			strings.Contains(detail, "connect:") {
			return "http_helper_unavailable"
		}
	}
	return "tunnel_delay_failed"
}

func (d *daemon) probeNodeBootstrapDNS(cfg *config.Config, host string, timeout time.Duration) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	bootstrapIP := strings.TrimSpace(cfg.DNS.BootstrapIP)
	if bootstrapIP == "" {
		return false
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: timeout}
			return dialer.DialContext(ctx, "udp", net.JoinHostPort(bootstrapIP, "53"))
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	addrs, err := resolver.LookupHost(ctx, host)
	return err == nil && len(addrs) > 0
}
