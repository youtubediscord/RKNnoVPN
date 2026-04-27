package main

import (
	"encoding/json"
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
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
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

func TestRuntimeStartFailsWhileResetLockPresent(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	if err := os.WriteFile(d.resetLockPath(), []byte("reset\n"), 0640); err != nil {
		t.Fatal(err)
	}

	_, err := (&rootBackendV2{d: d}).Start(runtimev2.DesiredState{BackendKind: runtimev2.BackendRootTProxy}, 1)
	if err == nil || !strings.Contains(err.Error(), "reset is in progress") {
		t.Fatalf("expected reset-in-progress error, got %v", err)
	}
	rpcErr := d.rpcErrorFromRuntimeError(err)
	if rpcErr.Code != ipc.CodeRuntimeBusy {
		t.Fatalf("expected runtime busy RPC code, got %#v", rpcErr)
	}
}

func TestRecoverStaleResetLockRunsStructuredCleanup(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	old := time.Now().Add(-resetLockStaleAfter - time.Minute).Format(time.RFC3339)
	if err := os.WriteFile(d.resetLockPath(), []byte(old+"\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.activeFilePath(), []byte("active\n"), 0640); err != nil {
		t.Fatal(err)
	}

	report, err := d.recoverStaleResetLock(12)
	if err != nil {
		t.Fatalf("stale lock recovery should succeed: %v", err)
	}
	if report == nil || report.Generation != 12 || report.Status != "ok" {
		t.Fatalf("expected structured recovery report, got %#v", report)
	}
	if _, err := os.Stat(d.resetLockPath()); !os.IsNotExist(err) {
		t.Fatalf("stale reset lock should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(d.activeFilePath()); !os.IsNotExist(err) {
		t.Fatalf("stale recovery should clear active marker, stat err=%v", err)
	}
}

func TestRecoverFreshResetLockStillBlocks(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	if err := os.WriteFile(d.resetLockPath(), []byte(time.Now().Format(time.RFC3339)+"\n"), 0640); err != nil {
		t.Fatal(err)
	}

	report, err := d.recoverStaleResetLock(13)
	if report != nil {
		t.Fatalf("fresh reset lock must not run cleanup, got %#v", report)
	}
	if !isRuntimeBusyCode(err, runtimev2.BusyCodeResetInProgress) {
		t.Fatalf("expected reset-in-progress busy error, got %T %v", err, err)
	}
}

func TestBackendStatusIncludesCompatibilitySnapshot(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.initRuntimeV2()

	payload, rpcErr := d.handleBackendStatus(nil)
	if rpcErr != nil {
		t.Fatalf("backend status failed: %#v", rpcErr)
	}
	status, ok := payload.(runtimev2.Status)
	if !ok {
		t.Fatalf("unexpected status payload type %T", payload)
	}
	if status.Compatibility.ControlProtocolVersion != controlProtocolVersion {
		t.Fatalf("expected control protocol in backend.status, got %#v", status.Compatibility)
	}
	if status.Compatibility.SchemaVersion != config.CurrentSchemaVersion {
		t.Fatalf("expected schema version in backend.status, got %#v", status.Compatibility)
	}
	if !containsString(status.Compatibility.Capabilities, "backend.root-tproxy") {
		t.Fatalf("expected daemon capabilities in backend.status, got %#v", status.Compatibility.Capabilities)
	}
	if !containsString(status.Compatibility.SupportedMethods, "backend.status") {
		t.Fatalf("expected supported methods in backend.status, got %#v", status.Compatibility.SupportedMethods)
	}
}

func TestLegacyStartUsesRuntimeActorWhenAlreadyRunning(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.initRuntimeV2()
	d.coreMgr.SetState(core.StateRunning)

	payload, rpcErr := d.handleStart(nil)
	if rpcErr != nil {
		t.Fatalf("legacy start should be idempotent through runtime actor, got %#v", rpcErr)
	}
	status, ok := payload.(runtimev2.Status)
	if !ok {
		t.Fatalf("unexpected start payload type %T", payload)
	}
	if status.ActiveOperation == nil || status.ActiveOperation.Kind != runtimev2.OperationStart {
		t.Fatalf("expected start operation record, got %#v", status.ActiveOperation)
	}
}

func TestLegacyStartRecordsMissingNodeFailure(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfg.Node.Address = ""
	d.cfg.Panel.Nodes = nil
	d.initRuntimeV2()

	if _, rpcErr := d.handleStart(nil); rpcErr != nil {
		t.Fatalf("legacy start should accept through runtime actor, got %#v", rpcErr)
	}
	status := waitForDaemonRuntimeOperationDone(t, d.runtimeV2, runtimev2.OperationStart)
	if status.LastOperation == nil || status.LastOperation.Succeeded {
		t.Fatalf("expected failed start operation, got %#v", status.LastOperation)
	}
	if !strings.Contains(status.LastOperation.ErrorMessage, "no node configured") {
		t.Fatalf("expected missing node error in operation result, got %#v", status.LastOperation)
	}
}

func TestLegacyReloadReturnsRuntimeStatus(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.panelPath = config.PanelPath(d.cfgPath)
	if err := d.cfg.Save(d.cfgPath); err != nil {
		t.Fatal(err)
	}
	d.initRuntimeV2()
	d.coreMgr.SetState(core.StateRunning)

	payload, rpcErr := d.handleReload(nil)
	if rpcErr != nil {
		t.Fatalf("reload failed: %#v", rpcErr)
	}
	status, ok := payload.(runtimev2.Status)
	if !ok {
		t.Fatalf("reload should return runtime status, got %T", payload)
	}
	if !statusHasOperation(status, runtimev2.OperationReload) {
		t.Fatalf("reload response should expose reload operation, got %#v", status)
	}
	waitForDaemonRuntimeOperationDone(t, d.runtimeV2, runtimev2.OperationReload)
}

func TestConfigImportReturnsRuntimeStatus(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.panelPath = config.PanelPath(d.cfgPath)
	d.initRuntimeV2()
	d.coreMgr.SetState(core.StateRunning)

	raw, err := json.Marshal(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(raw)
	payload, rpcErr := d.handleConfigImport(&params)
	if rpcErr != nil {
		t.Fatalf("config import failed: %#v", rpcErr)
	}
	result, ok := payload.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected import payload type %T", payload)
	}
	if result["status"] != "imported" || result["reload"] != true {
		t.Fatalf("unexpected import payload %#v", result)
	}
	status, ok := result["runtimeStatus"].(runtimev2.Status)
	if !ok {
		t.Fatalf("runtimeStatus missing from import payload: %#v", result)
	}
	if !statusHasOperation(status, runtimev2.OperationReload) {
		t.Fatalf("config import should expose reload operation, got %#v", status)
	}
	waitForDaemonRuntimeOperationDone(t, d.runtimeV2, runtimev2.OperationReload)
}

func TestConfigImportRejectsLinkPayload(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.panelPath = config.PanelPath(d.cfgPath)
	d.initRuntimeV2()

	params := json.RawMessage(`{"links":"vless://example"}`)
	_, rpcErr := d.handleConfigImport(&params)
	if rpcErr == nil {
		t.Fatal("link payload must not be treated as a full config import")
	}
	if rpcErr.Code != ipc.CodeInvalidParams {
		t.Fatalf("unexpected error code: %#v", rpcErr)
	}
	if !strings.Contains(rpcErr.Message, "unknown config import field") {
		t.Fatalf("unexpected error message: %q", rpcErr.Message)
	}
}

func TestReloadConfigApplyBusyDoesNotPersistConfig(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.panelPath = config.PanelPath(d.cfgPath)
	d.initRuntimeV2()
	if err := d.cfg.Save(d.cfgPath); err != nil {
		t.Fatal(err)
	}
	originalURL := d.cfg.Health.URL

	release := make(chan struct{})
	if _, err := d.runtimeV2.RunOperation(runtimev2.OperationStart, runtimev2.PhaseStarting, func(int64) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	nextCfg := *d.cfg
	nextCfg.Health.URL = "https://changed.invalid/generate_204"
	err := d.applyConfig(&nextCfg, true)
	if !isRuntimeBusyCode(err, runtimev2.BusyCodeRuntimeBusy) {
		t.Fatalf("expected runtime busy before config write, got %T %v", err, err)
	}

	reloaded, err := config.Load(d.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Health.URL != originalURL {
		t.Fatalf("config was persisted while runtime busy: got %q want %q", reloaded.Health.URL, originalURL)
	}
}

func TestConfigApplyWithoutReloadBusyDoesNotPersistConfig(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.panelPath = config.PanelPath(d.cfgPath)
	d.initRuntimeV2()
	if err := d.cfg.Save(d.cfgPath); err != nil {
		t.Fatal(err)
	}
	originalURL := d.cfg.Health.URL

	release := make(chan struct{})
	if _, err := d.runtimeV2.RunOperation(runtimev2.OperationReset, runtimev2.PhaseResetting, func(int64) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	nextCfg := *d.cfg
	nextCfg.Health.URL = "https://changed-without-reload.invalid/generate_204"
	err := d.applyConfig(&nextCfg, false)
	if !isRuntimeBusyCode(err, runtimev2.BusyCodeResetInProgress) {
		t.Fatalf("expected runtime busy before config write, got %T %v", err, err)
	}

	reloaded, err := config.Load(d.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Health.URL != originalURL {
		t.Fatalf("config was persisted while runtime busy: got %q want %q", reloaded.Health.URL, originalURL)
	}
}

func TestPanelApplyWithoutReloadBusyDoesNotPersistPanel(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.panelPath = config.PanelPath(d.cfgPath)
	d.initRuntimeV2()
	originalPanel := d.cfg.Panel
	originalPanel.ActiveNodeID = "original"
	if err := config.SavePanel(d.panelPath, originalPanel); err != nil {
		t.Fatal(err)
	}
	d.cfg.Panel = originalPanel

	release := make(chan struct{})
	if _, err := d.runtimeV2.RunOperation(runtimev2.OperationReload, runtimev2.PhaseStarting, func(int64) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	nextPanel := originalPanel
	nextPanel.ActiveNodeID = "changed"
	err := d.applyPanelConfig(nextPanel, false)
	if !isRuntimeBusyCode(err, runtimev2.BusyCodeRuntimeBusy) {
		t.Fatalf("expected runtime busy before panel write, got %T %v", err, err)
	}

	data, err := os.ReadFile(d.panelPath)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded config.PanelConfig
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.ActiveNodeID != originalPanel.ActiveNodeID {
		t.Fatalf("panel was persisted while runtime busy: got %q want %q", reloaded.ActiveNodeID, originalPanel.ActiveNodeID)
	}
}

func TestConfigSetReturnsRuntimeStatus(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.panelPath = config.PanelPath(d.cfgPath)
	d.initRuntimeV2()
	if err := d.cfg.Save(d.cfgPath); err != nil {
		t.Fatal(err)
	}

	rawHealth, err := json.Marshal(d.cfg.Health)
	if err != nil {
		t.Fatal(err)
	}
	paramsRaw, err := json.Marshal(map[string]json.RawMessage{
		"key":   json.RawMessage(`"health"`),
		"value": rawHealth,
	})
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(paramsRaw)

	payload, rpcErr := d.handleConfigSet(&params)
	if rpcErr != nil {
		t.Fatalf("config set failed: %#v", rpcErr)
	}
	result, ok := payload.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected config-set payload type %T", payload)
	}
	if result["status"] != "ok" || result["reload"] != false || result["updated"] != 1 {
		t.Fatalf("unexpected config-set result %#v", result)
	}
	if _, ok := result["runtimeStatus"].(runtimev2.Status); !ok {
		t.Fatalf("runtimeStatus missing from config-set result: %#v", result)
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

func isRuntimeBusyCode(err error, code string) bool {
	var busy *runtimev2.OperationBusyError
	return errors.As(err, &busy) && busy.Code == code
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func statusHasOperation(status runtimev2.Status, kind runtimev2.OperationKind) bool {
	if status.ActiveOperation != nil && status.ActiveOperation.Kind == kind {
		return true
	}
	return status.LastOperation != nil && status.LastOperation.Kind == kind
}

func waitForDaemonRuntimeOperationDone(t *testing.T, orchestrator *runtimev2.Orchestrator, kind runtimev2.OperationKind) runtimev2.Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status := orchestrator.Status()
		if status.ActiveOperation == nil && status.LastOperation != nil && status.LastOperation.Kind == kind {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s operation to finish, status=%#v", kind, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
