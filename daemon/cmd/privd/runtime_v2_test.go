package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/rescue"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestBuildRuntimeV2HealthSnapshotSeparatesOperationalFailures(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), nil)
	manager.SetState(core.StateRunning)
	d := &daemon{coreMgr: manager}

	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   true,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: true, Detail: "listening"},
			"iptables":      {Pass: true, Detail: "iptables"},
			"routing":       {Pass: true, Detail: "routing"},
			"dns":           {Pass: false, Detail: "dns timeout", Code: "DNS_LOOKUP_TIMEOUT"},
		},
	}

	snapshot := d.buildRuntimeV2HealthSnapshot(result, false)
	if !snapshot.Healthy() {
		t.Fatalf("readiness should be healthy with only DNS red: %#v", snapshot)
	}
	if snapshot.OperationalHealthy() {
		t.Fatalf("operational health should be red when DNS and egress are red: %#v", snapshot)
	}
	if !strings.Contains(snapshot.LastError, "operational degraded") {
		t.Fatalf("expected operational degraded diagnostic, got %q", snapshot.LastError)
	}
	if snapshot.LastCode != "DNS_LOOKUP_TIMEOUT" {
		t.Fatalf("expected stable DNS failure code, got %q", snapshot.LastCode)
	}
	if got := snapshot.Checks["dns"].Code; got != "DNS_LOOKUP_TIMEOUT" {
		t.Fatalf("expected DNS check code in structured checks, got %q", got)
	}
}

func TestBuildRuntimeV2HealthSnapshotIncludesLatestStageReport(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), nil)
	manager.SetState(core.StateRunning)
	d := &daemon{coreMgr: manager}

	report := core.NewRuntimeStageReport("apply config")
	report.AddStage("wait-dns", "failed", "DNS_LISTENER_DOWN", "dns listener down", false)
	d.setLastReloadReport(report)

	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   false,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: true, Detail: "listening"},
			"iptables":      {Pass: true, Detail: "iptables"},
			"routing":       {Pass: true, Detail: "routing"},
			"dns_listener":  {Pass: false, Detail: "listener down", Code: "DNS_LISTENER_DOWN"},
		},
	}

	snapshot := d.buildRuntimeV2HealthSnapshot(result, false)
	stageReport, ok := snapshot.StageReport.(core.RuntimeStageReport)
	if !ok {
		t.Fatalf("expected core stage report, got %T", snapshot.StageReport)
	}
	if stageReport.FailedStage != "wait-dns" || stageReport.LastCode != "DNS_LISTENER_DOWN" {
		t.Fatalf("unexpected stage report: %#v", stageReport)
	}
}

func TestBuildRuntimeV2HealthSnapshotUsesSuccessfulStageBeforeFirstHealth(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), nil)
	manager.SetState(core.StateRunning)
	d := &daemon{coreMgr: manager}

	report := core.NewRuntimeStageReport("start")
	report.AddStage("commit-state", "ok", "", "vless://example.com", false)
	report.FinishOK()
	d.setLastReloadReport(report)

	snapshot := d.buildRuntimeV2HealthSnapshot(nil, false)
	if !snapshot.CoreReady || !snapshot.RoutingReady {
		t.Fatalf("successful stage report should keep hard readiness green before first health result: %#v", snapshot)
	}
	if snapshot.DNSReady || snapshot.EgressReady {
		t.Fatalf("soft readiness should not be invented before health probes: %#v", snapshot)
	}
}

func TestFirstFailedGateUsesDeterministicReadinessPriority(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   false,
		Checks: map[string]health.CheckResult{
			"dns":           {Pass: false, Detail: "dns timeout"},
			"routing":       {Pass: false, Detail: "routing missing"},
			"iptables":      {Pass: false, Detail: "iptables missing"},
			"tproxy_port":   {Pass: false, Detail: "port closed"},
			"singbox_alive": {Pass: false, Detail: "pid missing"},
		},
	}

	got := firstFailedGate(result, runtimev2.HealthSnapshot{})
	if !strings.HasPrefix(got, "singbox_alive:") {
		t.Fatalf("expected singbox_alive first, got %q", got)
	}
}

func TestFirstFailedGateCodeUsesDeterministicReadinessPriority(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   false,
		Checks: map[string]health.CheckResult{
			"dns":           {Pass: false, Detail: "dns timeout", Code: "DNS_LOOKUP_TIMEOUT"},
			"routing":       {Pass: false, Detail: "routing missing", Code: "ROUTING_NOT_APPLIED"},
			"iptables":      {Pass: false, Detail: "iptables missing", Code: "RULES_NOT_APPLIED"},
			"tproxy_port":   {Pass: false, Detail: "port closed", Code: "TPROXY_PORT_DOWN"},
			"singbox_alive": {Pass: false, Detail: "pid missing", Code: "CORE_PID_MISSING"},
		},
	}

	got := firstFailedGateDiagnostic(result, runtimev2.HealthSnapshot{})
	if got.Code != "CORE_PID_MISSING" {
		t.Fatalf("expected CORE_PID_MISSING first, got %#v", got)
	}
}

func TestBuildHealthPayloadIncludesStableLastCode(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   true,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: true, Detail: "listening"},
			"iptables":      {Pass: true, Detail: "iptables"},
			"routing":       {Pass: true, Detail: "routing"},
			"dns_listener":  {Pass: false, Detail: "listener down", Code: "DNS_LISTENER_DOWN"},
			"dns":           {Pass: false, Detail: "dns timeout", Code: "DNS_LOOKUP_TIMEOUT"},
		},
	}

	payload := buildHealthPayload(core.StateRunning, result, false)
	if payload["lastCode"] != "DNS_LISTENER_DOWN" {
		t.Fatalf("expected DNS_LISTENER_DOWN code, got %#v", payload["lastCode"])
	}
	if payload["lastError"] == nil {
		t.Fatalf("expected legacy lastError detail to remain populated")
	}
}

func TestPortProtectionOutputRequiresProtocolAndDropRule(t *testing.T) {
	output := strings.Join([]string{
		"-A PRIVSTACK_OUT -p tcp -m tcp --dport 10853 -m owner ! --uid-owner 0 ! --gid-owner 23333 -j DROP",
		"-A PRIVSTACK_OUT -p udp -m udp --dport 10853 -m owner ! --uid-owner 0 ! --gid-owner 23333 -j DROP",
		"-A PRIVSTACK_OUT -p tcp -m tcp --dport 10856 -j RETURN",
	}, "\n")

	if !portProtectionOutputContains(output, "tcp", 10853) {
		t.Fatalf("expected TCP protection rule to be detected")
	}
	if !portProtectionOutputContains(output, "udp", 10853) {
		t.Fatalf("expected UDP protection rule to be detected")
	}
	if portProtectionOutputContains(output, "udp", 10856) {
		t.Fatalf("RETURN-only DNS rule must not count as listener protection")
	}
	if portProtectionOutputContains(output, "tcp", 10854) {
		t.Fatalf("wrong port must not count as listener protection")
	}
}

func TestBuildHealthPayloadIncludesOutboundURLCheck(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   true,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: true, Detail: "listening"},
			"iptables":      {Pass: true, Detail: "iptables"},
			"routing":       {Pass: true, Detail: "routing"},
			"dns_listener":  {Pass: true, Detail: "dns listener"},
			"dns":           {Pass: true, Detail: "dns"},
			"outbound_url":  {Pass: false, Detail: "probe timeout", Code: "OUTBOUND_URL_FAILED"},
		},
	}

	payload := buildHealthPayload(core.StateRunning, result, false)
	if payload["egressReady"] != false {
		t.Fatalf("expected egressReady=false, got %#v", payload["egressReady"])
	}
	if payload["operationalHealthy"] != false {
		t.Fatalf("expected operationalHealthy=false, got %#v", payload["operationalHealthy"])
	}
	if payload["lastCode"] != "OUTBOUND_URL_FAILED" {
		t.Fatalf("expected OUTBOUND_URL_FAILED code, got %#v", payload["lastCode"])
	}
}

func TestClassifyRuntimeURLTestFailureUsesLastHealthCode(t *testing.T) {
	base := runtimev2.HealthSnapshot{
		CoreReady:    true,
		RoutingReady: true,
		DNSReady:     true,
		EgressReady:  false,
		CheckedAt:    time.Now(),
	}
	base.LastCode = "OUTBOUND_URL_FAILED"
	if got := classifyRuntimeURLTestFailure(errors.New("timeout"), base); got != "outbound_url_failed" {
		t.Fatalf("expected outbound_url_failed, got %q", got)
	}
	base.LastCode = "DNS_LOOKUP_TIMEOUT"
	if got := classifyRuntimeURLTestFailure(errors.New("timeout"), base); got != "proxy_dns_unavailable" {
		t.Fatalf("expected proxy_dns_unavailable, got %q", got)
	}
	base.LastCode = "RULES_NOT_APPLIED"
	if got := classifyRuntimeURLTestFailure(errors.New("timeout"), base); got != "runtime_not_ready" {
		t.Fatalf("expected runtime_not_ready, got %q", got)
	}
}

func TestClassifyURLTestFailureUsesConcreteURLCause(t *testing.T) {
	base := runtimev2.HealthSnapshot{
		CoreReady:    true,
		RoutingReady: true,
		DNSReady:     true,
		EgressReady:  true,
		CheckedAt:    time.Now(),
	}
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("api_disabled"), "api_disabled"},
		{errors.New("Get http://127.0.0.1:9090/proxies/node/delay: connect: connection refused"), "api_unavailable"},
		{errors.New("clash delay HTTP 404: proxy not found"), "outbound_missing"},
		{errors.New("remote error: tls: handshake failure"), "tls_handshake_failed"},
		{errors.New("lookup example.com: no such host"), "proxy_dns_unavailable"},
	}
	for _, tc := range cases {
		if got := classifyRuntimeURLTestFailure(tc.err, base); got != tc.want {
			t.Fatalf("expected %q for %v, got %q", tc.want, tc.err, got)
		}
	}
}

func TestIgnorableKillallExitStatusOne(t *testing.T) {
	if !isIgnorableKillallError("anything", errors.New("exit status 1")) {
		t.Fatalf("killall exit status 1 should be treated as success-noop")
	}
	if isIgnorableKillallError("", errors.New("exit status 2")) {
		t.Fatalf("non-1 killall failures must still be reported")
	}
}

func TestResetModeCreatesManualLockAndClearsActive(t *testing.T) {
	d := &daemon{dataDir: t.TempDir()}
	if err := os.MkdirAll(d.dataDir+"/run", 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.activeFilePath(), []byte("active\n"), 0640); err != nil {
		t.Fatal(err)
	}

	if err := d.enterResetMode(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.resetLockPath()); err != nil {
		t.Fatalf("reset lock missing: %v", err)
	}
	if _, err := os.Stat(d.manualFlagPath()); err != nil {
		t.Fatalf("manual flag missing: %v", err)
	}
	if _, err := os.Stat(d.activeFilePath()); !os.IsNotExist(err) {
		t.Fatalf("active marker should be removed, stat err=%v", err)
	}
	if skip, detail := d.shouldSkipRootReconcile(); !skip || !strings.Contains(detail, "reset lock") {
		t.Fatalf("expected reset lock guard, got skip=%v detail=%q", skip, detail)
	}

	if err := d.leaveResetMode(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.resetLockPath()); !os.IsNotExist(err) {
		t.Fatalf("reset lock should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(d.manualFlagPath()); err != nil {
		t.Fatalf("manual flag should remain after reset: %v", err)
	}
}

func TestRootReconcileGuardRequiresActiveMarker(t *testing.T) {
	d := &daemon{dataDir: t.TempDir()}
	if err := os.MkdirAll(d.dataDir+"/run", 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.dataDir+"/config", 0700); err != nil {
		t.Fatal(err)
	}

	if skip, detail := d.shouldSkipRootReconcile(); !skip || !strings.Contains(detail, "not marked active") {
		t.Fatalf("expected inactive guard, got skip=%v detail=%q", skip, detail)
	}

	if err := os.WriteFile(d.activeFilePath(), []byte("active\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if skip, detail := d.shouldSkipRootReconcile(); skip {
		t.Fatalf("active runtime should reconcile, detail=%q", detail)
	}

	if err := os.WriteFile(d.manualFlagPath(), []byte("manual\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if skip, detail := d.shouldSkipRootReconcile(); !skip || !strings.Contains(detail, "manual mode") {
		t.Fatalf("expected manual guard, got skip=%v detail=%q", skip, detail)
	}
}

func TestRemoveStaleRuntimeFilesIsIdempotent(t *testing.T) {
	d := &daemon{dataDir: t.TempDir()}
	if err := os.MkdirAll(d.dataDir+"/run", 0750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"singbox.pid", "active", "net_change.lock", "iptables.rules", "ip6tables.rules", "env.sh"} {
		if err := os.WriteFile(d.dataDir+"/run/"+name, []byte("stale\n"), 0640); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := d.removeStaleRuntimeFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) < 6 {
		t.Fatalf("expected stale files to be removed, got %#v", removed)
	}
	removed, err = d.removeStaleRuntimeFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("second cleanup should be already clean, got %#v", removed)
	}
}

func TestResetNetworkStateReportSuccessIsStructuredAndIdempotent(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	for _, name := range []string{"singbox.pid", "active", "net_change.lock"} {
		if err := os.WriteFile(filepath.Join(d.dataDir, "run", name), []byte("stale\n"), 0640); err != nil {
			t.Fatal(err)
		}
	}

	report := d.resetNetworkStateReport(7, runtimev2.BackendRootTProxy)
	if report.Status != "ok" {
		t.Fatalf("expected ok reset report, got %#v", report)
	}
	if report.RebootRequired {
		t.Fatalf("clean reset must not require reboot: %#v", report)
	}
	for _, stepName := range []string{"enter-reset-mode", "stop-subsystems", "stop-core", "rescue-cleanup-script", "clear-runtime-state", "remove-stale-runtime-files", "verify-cleanup", "leave-reset-mode"} {
		if !resetReportHasStep(report, stepName) {
			t.Fatalf("reset report missing step %s: %#v", stepName, report.Steps)
		}
	}

	report = d.resetNetworkStateReport(8, runtimev2.BackendRootTProxy)
	if report.Status != "ok" {
		t.Fatalf("second reset should stay ok, got %#v", report)
	}
	if step := resetReportStep(report, "remove-stale-runtime-files"); step.Status != "already_clean" {
		t.Fatalf("second reset should report already_clean stale files, got %#v", step)
	}
}

func TestResetNetworkStateReportLeftoversAreWarningsAndRequireReboot(t *testing.T) {
	d := newTestResetDaemon(t, []string{"iptables mangle rule remains: -A PRIVSTACK_PRE"}, true)

	report := d.resetNetworkStateReport(9, runtimev2.BackendRootTProxy)
	if report.Status != "clean_with_warnings" {
		t.Fatalf("leftovers should make reset clean_with_warnings, got %#v", report)
	}
	if !report.RebootRequired {
		t.Fatalf("leftovers should require reboot: %#v", report)
	}
	if len(report.Leftovers) != 1 {
		t.Fatalf("expected leftovers in report, got %#v", report.Leftovers)
	}
	if len(report.Errors) != 0 {
		t.Fatalf("leftovers should not be hard errors, got %#v", report.Errors)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("leftovers should be warnings, got %#v", report)
	}
	if step := resetReportStep(report, "verify-cleanup"); step.Status != "warning" {
		t.Fatalf("verify-cleanup should warn on leftovers, got %#v", step)
	}
}

func TestResetNetworkStateReportMissingRescueScriptIsNoop(t *testing.T) {
	d := newTestResetDaemon(t, nil, false)

	report := d.resetNetworkStateReport(10, runtimev2.BackendRootTProxy)
	if report.Status != "ok" {
		t.Fatalf("missing rescue script should not fail reset, got %#v", report)
	}
	if step := resetReportStep(report, "rescue-cleanup-script"); step.Status != "already_clean" {
		t.Fatalf("missing rescue script should be already_clean, got %#v", step)
	}
}

func newTestResetDaemon(t *testing.T, leftovers []string, withRescueScript bool) *daemon {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Proxy.APIPort = 0
	if err := os.MkdirAll(filepath.Join(dataDir, "run"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	scriptsDir := filepath.Join(dataDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dns.sh", "iptables.sh"} {
		if err := os.WriteFile(filepath.Join(scriptsDir, name), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if withRescueScript {
		if err := os.WriteFile(filepath.Join(scriptsDir, "rescue_reset.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	coreMgr := core.NewCoreManager(cfg, dataDir, log.New(os.Stderr, "", 0))
	healthMon := health.NewHealthMonitor(coreMgr, time.Hour, 3, cfg.Proxy.TProxyPort, cfg.Proxy.DNSPort, cfg.Proxy.Mark, cfg.Health.URL, time.Second, nil)
	return &daemon{
		cfg:                      cfg,
		dataDir:                  dataDir,
		coreMgr:                  coreMgr,
		healthMon:                healthMon,
		rescueMgr:                rescue.NewRescueManager(coreMgr, cfg, dataDir, 3, time.Second, nil),
		collectLeftoversOverride: func(*config.Config) []string { return leftovers },
	}
}

func resetReportHasStep(report runtimev2.ResetReport, name string) bool {
	return resetReportStep(report, name).Name != ""
}

func resetReportStep(report runtimev2.ResetReport, name string) runtimev2.ResetStep {
	for _, step := range report.Steps {
		if step.Name == name {
			return step
		}
	}
	return runtimev2.ResetStep{}
}

func TestBuildScriptEnvUsesExplicitDNSScopeForBlacklist(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Apps.Mode = "blacklist"
	cfg.Apps.Packages = []string{"com.example.direct"}

	env := buildScriptEnv(cfg, t.TempDir())
	if env["APP_MODE"] != "blacklist" {
		t.Fatalf("unexpected APP_MODE: %q", env["APP_MODE"])
	}
	if env["DNS_SCOPE"] != "all_except_uids" {
		t.Fatalf("blacklist DNS must exclude direct UIDs, got %q", env["DNS_SCOPE"])
	}
	if env["PROXY_UIDS"] != "" {
		t.Fatalf("blacklist mode must not put selected packages into PROXY_UIDS: %q", env["PROXY_UIDS"])
	}
}

func TestBuildScriptEnvUsesExplicitDNSScopeForWhitelist(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Apps.Mode = "whitelist"
	cfg.Apps.Packages = []string{"com.example.proxy"}

	env := buildScriptEnv(cfg, t.TempDir())
	if env["DNS_SCOPE"] != "uids" {
		t.Fatalf("whitelist DNS must target proxied UIDs only, got %q", env["DNS_SCOPE"])
	}
	if env["DIRECT_UIDS"] != "" {
		t.Fatalf("whitelist mode must not put selected packages into DIRECT_UIDS: %q", env["DIRECT_UIDS"])
	}
}

func TestBuildScriptEnvIncludesLocalHelperPorts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Panel.Inbounds = []byte(`{"socksPort":10808,"httpPort":10809}`)

	env := buildScriptEnv(cfg, t.TempDir())
	if env["SOCKS_PORT"] != "10808" {
		t.Fatalf("expected SOCKS_PORT=10808, got %q", env["SOCKS_PORT"])
	}
	if env["HTTP_PORT"] != "10809" {
		t.Fatalf("expected HTTP_PORT=10809, got %q", env["HTTP_PORT"])
	}
}

func TestBuildScriptEnvDisablesLocalHelperPortsByDefault(t *testing.T) {
	cfg := config.DefaultConfig()

	env := buildScriptEnv(cfg, t.TempDir())
	if env["SOCKS_PORT"] != "0" {
		t.Fatalf("default SOCKS_PORT must stay disabled, got %q", env["SOCKS_PORT"])
	}
	if env["HTTP_PORT"] != "0" {
		t.Fatalf("default HTTP_PORT must stay disabled, got %q", env["HTTP_PORT"])
	}
}

func TestReloadReportAccessorsPreserveLastReport(t *testing.T) {
	d := &daemon{}
	report := core.NewRuntimeStageReport("apply config")
	report.AddStage("hot-swap", "ok", "", "", false)
	report.FinishOK()

	d.setLastReloadReport(report)
	got := d.LastReloadReport()
	if got.Operation != "apply config" || got.Status != "ok" || len(got.Stages) != 1 {
		t.Fatalf("unexpected reload report: %#v", got)
	}
}

func TestRuntimeErrorCodePrefersTypedNetstackCode(t *testing.T) {
	err := &netstack.Error{
		Operation: "apply",
		Code:      "DNS_APPLY_FAILED",
		Report: netstack.Report{
			Operation: "apply",
			Status:    "failed",
			Errors:    []string{"dns-start: failed"},
		},
	}

	if got := runtimeErrorCode(err, "fallback"); got != "DNS_APPLY_FAILED" {
		t.Fatalf("expected DNS_APPLY_FAILED, got %q", got)
	}
	if got := runtimeErrorCode(errors.New("plain"), "fallback"); got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestHealthEgressURLsPrefersConfiguredProbeSet(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Health.EgressURLs = []string{" https://cp.cloudflare.com/generate_204 ", "", "https://example.com/204"}
	cfg.Health.URL = "https://cp.cloudflare.com/generate_204"

	got := healthEgressURLs(cfg)
	want := []string{
		"https://cp.cloudflare.com/generate_204",
		"https://example.com/204",
		"https://www.gstatic.com/generate_204",
	}
	if len(got) < len(want) {
		t.Fatalf("unexpected probe URLs: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected probe URLs: got %#v want prefix %#v", got, want)
		}
	}
}

func TestReadLogTailReturnsBoundedTail(t *testing.T) {
	path := t.TempDir() + "/runtime.log"
	content := strings.Join([]string{"one", "two", "three", "four"}, "\n")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	lines, err := readLogTail(path, 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, ","); got != "three,four" {
		t.Fatalf("unexpected tail: %q", got)
	}
}
