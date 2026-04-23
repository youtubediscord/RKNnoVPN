package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/privstack/daemon/internal/config"
	"github.com/privstack/daemon/internal/core"
	"github.com/privstack/daemon/internal/health"
	"github.com/privstack/daemon/internal/ipc"
	"github.com/privstack/daemon/internal/runtimev2"
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
	state := b.d.coreMgr.GetState()
	if state == core.StateRunning || state == core.StateDegraded {
		return nil
	}

	b.d.mu.Lock()
	profile := b.d.cfg.ResolveProfile()
	hasPanelNodes := len(b.d.cfg.Panel.Nodes) > 0
	b.d.mu.Unlock()

	if profile.Address == "" && !hasPanelNodes {
		return fmt.Errorf("no node configured (address is empty)")
	}
	if err := b.d.coreMgr.Start(profile); err != nil {
		return err
	}
	b.d.rescueMgr.Reset()
	b.d.startSubsystems()

	snapshot := b.RefreshHealth()
	if !snapshot.Healthy() {
		b.d.resetNetworkStateReport(0, runtimev2.BackendRootTProxy)
		return fmt.Errorf("health gates failed after start: %s", snapshot.LastError)
	}
	return nil
}

func (b *rootBackendV2) Stop() error {
	if b.d.coreMgr.GetState() == core.StateStopped {
		return nil
	}
	b.d.stopSubsystems()
	return b.d.coreMgr.Stop()
}

func (b *rootBackendV2) Reset(generation int64) runtimev2.ResetReport {
	return b.d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
}

func (b *rootBackendV2) Restart(desired runtimev2.DesiredState, generation int64) error {
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
	return b.d.buildRuntimeV2HealthSnapshot(result, false)
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
		return fmt.Errorf("restart health gates failed: %s", snapshot.LastError)
	}
	return nil
}

func (d *daemon) reconcileRootRuntime(reason string) error {
	state := d.coreMgr.GetState()
	if state != core.StateRunning && state != core.StateDegraded {
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
	return fmt.Errorf("%s health gates failed: %s", reason, snapshot.LastError)
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
	for name, check := range result.Checks {
		switch name {
		case "singbox_alive", "tproxy_port":
			if name == "singbox_alive" {
				singboxReady = check.Pass
			}
			if name == "tproxy_port" {
				tproxyReady = check.Pass
			}
		case "dns":
			snapshot.DNSReady = check.Pass
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

	if allowEgressProbe {
		snapshot.EgressReady = d.hasRecentEgress()
	} else {
		snapshot.EgressReady = d.hasRecentEgress()
	}

	if snapshot.LastError == "" {
		snapshot.LastError = firstFailedGate(result, snapshot)
	}
	return snapshot
}

func firstFailedGate(result *health.HealthResult, snapshot runtimev2.HealthSnapshot) string {
	if result != nil {
		for _, name := range []string{"singbox_alive", "tproxy_port", "iptables", "routing", "dns"} {
			if check, ok := result.Checks[name]; ok && !check.Pass {
				return fmt.Sprintf("%s: %s", name, check.Detail)
			}
		}
	}
	if !snapshot.EgressReady {
		return "egress: no recent successful egress probe"
	}
	if !snapshot.Healthy() {
		return "one or more readiness gates are red"
	}
	if !snapshot.OperationalHealthy() {
		return "one or more operational health signals are red"
	}
	return ""
}

func (d *daemon) hasRecentEgress() bool {
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	return d.egress.Valid && time.Since(d.egress.CheckedAt) < 30*time.Second
}

func (d *daemon) resetNetworkStateReport(generation int64, backend runtimev2.BackendKind) runtimev2.ResetReport {
	report := runtimev2.ResetReport{
		BackendKind: backend,
		Generation:  generation,
		Status:      "ok",
	}

	runStep := func(name string, fn func() error) {
		step := runtimev2.ResetStep{Name: name, Status: "ok"}
		if err := fn(); err != nil {
			step.Status = "failed"
			step.Detail = err.Error()
			report.Status = "partial"
			report.Errors = append(report.Errors, name+": "+err.Error())
		}
		report.Steps = append(report.Steps, step)
	}

	runStep("stop-subsystems", func() error {
		d.stopSubsystems()
		return nil
	})

	runStep("stop-core", func() error {
		if d.coreMgr.GetState() == core.StateStopped {
			return nil
		}
		return d.coreMgr.Stop()
	})

	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	env := buildScriptEnv(cfg, d.dataDir)

	runStep("stop-dns-interception", func() error {
		return core.ExecScript(filepath.Join(d.dataDir, "scripts", "dns.sh"), "stop", env)
	})

	runStep("stop-firewall-routing", func() error {
		return core.ExecScript(filepath.Join(d.dataDir, "scripts", "iptables.sh"), "stop", env)
	})

	runStep("kill-sing-box", func() error {
		var errs []string
		if out, err := core.ExecCommand("killall", "-TERM", "sing-box"); err != nil &&
			!isIgnorableKillallError(out, err) {
			errs = append(errs, err.Error())
		}
		if out, err := core.ExecCommand("killall", "-KILL", "sing-box"); err != nil &&
			!isIgnorableKillallError(out, err) {
			errs = append(errs, err.Error())
		}
		if len(errs) > 0 {
			return fmt.Errorf(strings.Join(errs, "; "))
		}
		return nil
	})

	runStep("clear-runtime-state", func() error {
		d.rescueMgr.Reset()
		d.coreMgr.ResetState()
		d.resetRuntimeMetrics()
		return nil
	})

	if len(report.Errors) > 0 {
		report.Status = "partial"
	}
	return report
}

func isIgnorableKillallError(output string, err error) bool {
	if err == nil {
		return false
	}
	return strings.TrimSpace(output) == "" && err.Error() == "exit status 1"
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
	if apiPort == 0 {
		apiPort = 9090
	}
	testURL := strings.TrimSpace(url)
	if testURL == "" {
		testURL = strings.TrimSpace(cfg.Health.URL)
	}
	if testURL == "" {
		testURL = "https://www.gstatic.com/generate_204"
	}
	d.mu.Unlock()

	results := make([]runtimev2.NodeProbeResult, 0, len(profiles))
	for _, profile := range profiles {
		if len(requested) > 0 && !requested[profile.ID] {
			continue
		}

		result := runtimev2.NodeProbeResult{
			ID:       profile.ID,
			Name:     firstNonEmpty(profile.Name, profile.Tag, profile.Address),
			Protocol: profile.Protocol,
			Server:   profile.Address,
			Port:     profile.Port,
		}

		tcpMS, tcpErr := testTCPConnect(profile.Address, profile.Port, timeout)
		if tcpErr == nil {
			result.TCPDirect = &tcpMS
		} else {
			result.ErrorClass = "tcp_direct_failed"
		}

		result.DNSBootstrap = d.probeNodeBootstrapDNS(cfg, profile.Address, timeout)
		if !result.DNSBootstrap && result.ErrorClass == "" {
			result.ErrorClass = "dns_bootstrap_failed"
		}

		if d.coreMgr.GetState() == core.StateRunning || d.coreMgr.GetState() == core.StateDegraded {
			urlMS, _, urlErr := testClashDelay(apiPort, profile.Tag, testURL, timeoutMS)
			if urlErr == nil {
				result.TunnelDelay = &urlMS
				if result.ErrorClass == "" {
					result.ErrorClass = "ok"
				}
			} else if result.ErrorClass == "" {
				result.ErrorClass = "tunnel_delay_failed"
			}
		} else if result.ErrorClass == "" {
			result.ErrorClass = "tunnel_unavailable"
		}

		results = append(results, result)
	}

	return results
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
