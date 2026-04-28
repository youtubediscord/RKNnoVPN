package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestConfigMutationSuccessEnvelopeIsExplicit(t *testing.T) {
	d := &daemon{}

	result := d.configMutationSuccess("config-import", "ok", true, true, 2)

	if result["ok"] != true {
		t.Fatalf("mutation success must set ok=true: %#v", result)
	}
	if result["config_saved"] != true {
		t.Fatalf("mutation success must set config_saved=true: %#v", result)
	}
	if result["status"] != "accepted" {
		t.Fatalf("mutation success must expose accepted runtime operation: %#v", result)
	}
	if result["runtime_applied"] != false {
		t.Fatalf("mutation success must not claim async runtime apply completed: %#v", result)
	}
	if result["runtime_apply"] != "accepted" {
		t.Fatalf("mutation success must expose runtime apply status: %#v", result)
	}
	if result["accepted"] != true || result["operation_active"] != true {
		t.Fatalf("mutation success must expose active accepted operation: %#v", result)
	}
	if result["updated"] != 2 {
		t.Fatalf("mutation success lost updated count: %#v", result)
	}
	operation, ok := result["operation"].(map[string]interface{})
	if !ok {
		t.Fatalf("mutation success must expose transaction operation: %#v", result)
	}
	if operation["type"] != "config-mutation" || operation["action"] != "config-import" {
		t.Fatalf("unexpected mutation operation identity: %#v", operation)
	}
	if operation["runtimeApply"] != "accepted" || operation["operationActive"] != true {
		t.Fatalf("mutation operation must expose runtime apply status: %#v", operation)
	}
}

func TestConfigApplyRPCErrorEnvelopeKeepsSavedFailureVisible(t *testing.T) {
	d := &daemon{}

	rpcErr := d.configApplyRPCErrorSaved("config-import", errors.New("config saved: apply config hot-swap failed"), true)
	data, ok := rpcErr.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected structured mutation error data, got %#v", rpcErr.Data)
	}
	if data["ok"] != false {
		t.Fatalf("saved apply failure must set ok=false: %#v", data)
	}
	if data["config_saved"] != true {
		t.Fatalf("saved apply failure must set config_saved=true: %#v", data)
	}
	if data["runtime_applied"] != false {
		t.Fatalf("saved apply failure must set runtime_applied=false: %#v", data)
	}
	if data["code"] == "" || data["message"] == "" {
		t.Fatalf("saved apply failure must include code and message: %#v", data)
	}
	if data["runtime_apply"] != "failed" {
		t.Fatalf("saved apply failure must expose failed runtime apply: %#v", data)
	}
	operation, ok := data["operation"].(map[string]interface{})
	if !ok {
		t.Fatalf("saved apply failure must expose transaction operation: %#v", data)
	}
	if operation["status"] != "saved_not_applied" || operation["configSaved"] != true {
		t.Fatalf("unexpected failure operation: %#v", operation)
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

func TestBackendStartUsesRuntimeActorWhenAlreadyRunning(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.initRuntimeV2()
	d.coreMgr.SetState(core.StateRunning)

	payload, rpcErr := d.handleBackendStart(nil)
	if rpcErr != nil {
		t.Fatalf("backend start should be idempotent through runtime actor, got %#v", rpcErr)
	}
	status, ok := payload.(runtimev2.Status)
	if !ok {
		t.Fatalf("unexpected start payload type %T", payload)
	}
	if status.ActiveOperation == nil || status.ActiveOperation.Kind != runtimev2.OperationStart {
		t.Fatalf("expected start operation record, got %#v", status.ActiveOperation)
	}
}

func TestBackendStartRecordsMissingNodeFailure(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfg.Node.Address = ""
	d.cfg.Profile.Nodes = nil
	d.initRuntimeV2()

	if _, rpcErr := d.handleBackendStart(nil); rpcErr != nil {
		t.Fatalf("backend start should accept through runtime actor, got %#v", rpcErr)
	}
	status := waitForDaemonRuntimeOperationDone(t, d.runtimeV2, runtimev2.OperationStart)
	if status.LastOperation == nil || status.LastOperation.Succeeded {
		t.Fatalf("expected failed start operation, got %#v", status.LastOperation)
	}
	if !strings.Contains(status.LastOperation.ErrorMessage, "no node configured") {
		t.Fatalf("expected missing node error in operation result, got %#v", status.LastOperation)
	}
}

func TestBackendApplyDesiredStateCompletesDefaultsAndReturnsStatus(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfg.Profile.ActiveNodeID = "node-default"
	d.initRuntimeV2()

	params := json.RawMessage(`{"fallbackPolicy":"AUTO_RESET_ROOTED"}`)
	payload, rpcErr := d.handleBackendApplyDesiredState(&params)
	if rpcErr != nil {
		t.Fatalf("backend.applyDesiredState failed: %#v", rpcErr)
	}
	status, ok := payload.(runtimev2.Status)
	if !ok {
		t.Fatalf("unexpected applyDesiredState payload type %T", payload)
	}
	if status.DesiredState.BackendKind != runtimev2.BackendRootTProxy {
		t.Fatalf("expected default root backend, got %#v", status.DesiredState)
	}
	if status.DesiredState.FallbackPolicy != runtimev2.FallbackAutoReset {
		t.Fatalf("expected requested fallback policy, got %#v", status.DesiredState)
	}
}

func TestBackendApplyDesiredStateBusyReturnsRuntimeBusy(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.initRuntimeV2()

	release := make(chan struct{})
	if _, err := d.runtimeV2.RunOperation(runtimev2.OperationReset, runtimev2.PhaseResetting, func(int64) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)

	params := json.RawMessage(`{}`)
	_, rpcErr := d.handleBackendApplyDesiredState(&params)
	if rpcErr == nil {
		t.Fatal("expected runtime busy error")
	}
	if rpcErr.Code != ipc.CodeRuntimeBusy {
		t.Fatalf("expected runtime busy code, got %#v", rpcErr)
	}
}

func TestBackendRestartReturnsRuntimeStatus(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.profilePath = profiledoc.Path(d.cfgPath)
	if err := d.cfg.Save(d.cfgPath); err != nil {
		t.Fatal(err)
	}
	d.initRuntimeV2()
	d.coreMgr.SetState(core.StateRunning)

	payload, rpcErr := d.handleBackendRestart(nil)
	if rpcErr != nil {
		t.Fatalf("backend restart failed: %#v", rpcErr)
	}
	status, ok := payload.(runtimev2.Status)
	if !ok {
		t.Fatalf("backend restart should return runtime status, got %T", payload)
	}
	if !statusHasOperation(status, runtimev2.OperationRestart) {
		t.Fatalf("backend restart response should expose restart operation, got %#v", status)
	}
	waitForDaemonRuntimeOperationDone(t, d.runtimeV2, runtimev2.OperationRestart)
}

func TestConfigImportReturnsRuntimeStatus(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.profilePath = profiledoc.Path(d.cfgPath)
	d.initRuntimeV2()
	d.coreMgr.SetState(core.StateRunning)

	importCfg := *d.cfg
	importCfg.Health.IntervalSec = 77
	raw, err := json.Marshal(&importCfg)
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
	if result["status"] != "accepted" || result["reload"] != true || result["runtime_apply"] != "accepted" {
		t.Fatalf("unexpected import payload %#v", result)
	}
	status, ok := result["runtimeStatus"].(runtimev2.Status)
	if !ok {
		t.Fatalf("runtimeStatus missing from import payload: %#v", result)
	}
	if !statusHasOperation(status, runtimev2.OperationConfigMutation) {
		t.Fatalf("config import should expose config-mutation operation, got %#v", status)
	}
	d.runtimeV2.SetStatusObserver(nil)
	savedProfile, found, err := profiledoc.Load(d.profilePath)
	if err != nil {
		t.Fatalf("load imported profile: %v", err)
	}
	if !found {
		t.Fatal("config import did not persist profile.json")
	}
	if savedProfile.Health.IntervalSec != importCfg.Health.IntervalSec {
		t.Fatalf("saved imported profile health interval = %d, want %d", savedProfile.Health.IntervalSec, importCfg.Health.IntervalSec)
	}
	waitForDaemonRuntimeOperationDone(t, d.runtimeV2, runtimev2.OperationConfigMutation)
}

func TestConfigImportRejectsLinkPayload(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.profilePath = profiledoc.Path(d.cfgPath)
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

func TestConfigImportRejectsPanelPayload(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.profilePath = profiledoc.Path(d.cfgPath)
	d.initRuntimeV2()

	params := json.RawMessage(`{"schema_version":5,"panel":{"id":"old-panel"}}`)
	_, rpcErr := d.handleConfigImport(&params)
	if rpcErr == nil {
		t.Fatal("panel payload must not be accepted by config-import")
	}
	if rpcErr.Code != ipc.CodeInvalidParams {
		t.Fatalf("unexpected error code: %#v", rpcErr)
	}
	if !strings.Contains(rpcErr.Message, "unknown config import field \"panel\"") {
		t.Fatalf("unexpected error message: %q", rpcErr.Message)
	}
}

func TestReloadConfigApplyBusyDoesNotPersistConfig(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.profilePath = profiledoc.Path(d.cfgPath)
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
	d.runtimeV2.SetStatusObserver(nil)
	defer close(release)

	nextCfg := *d.cfg
	nextCfg.Health.URL = "https://changed.invalid/generate_204"
	err := d.applyConfigWithOperation(&nextCfg, true, runtimev2.OperationReload)
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
	d.profilePath = profiledoc.Path(d.cfgPath)
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
	d.runtimeV2.SetStatusObserver(nil)
	defer close(release)

	nextCfg := *d.cfg
	nextCfg.Health.URL = "https://changed-without-reload.invalid/generate_204"
	err := d.applyConfigWithOperation(&nextCfg, false, runtimev2.OperationReload)
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

func TestProfileApplyReturnsRuntimeStatus(t *testing.T) {
	d := newTestResetDaemon(t, nil, true)
	d.cfgPath = filepath.Join(d.dataDir, "config", "config.json")
	d.profilePath = profiledoc.Path(d.cfgPath)
	d.initRuntimeV2()
	if err := d.cfg.Save(d.cfgPath); err != nil {
		t.Fatal(err)
	}

	doc := profiledoc.FromConfig(d.cfg)
	doc.Health.IntervalSec = 45
	paramsRaw, err := json.Marshal(map[string]interface{}{
		"profile": doc,
		"reload":  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(paramsRaw)

	payload, rpcErr := d.handleProfileApply(&params)
	if rpcErr != nil {
		t.Fatalf("profile apply failed: %#v", rpcErr)
	}
	result, ok := payload.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected profile.apply payload type %T", payload)
	}
	if result["status"] != "ok" || result["configSaved"] != true || result["runtimeApply"] != "not_requested" {
		t.Fatalf("unexpected profile.apply result %#v", result)
	}
	if _, ok := result["runtimeStatus"].(runtimev2.Status); !ok {
		t.Fatalf("runtimeStatus missing from profile.apply result: %#v", result)
	}
	saved, found, err := profiledoc.Load(d.profilePath)
	if err != nil {
		t.Fatalf("load saved profile: %v", err)
	}
	if !found {
		t.Fatal("profile.apply did not persist profile.json")
	}
	if saved.Health.IntervalSec != doc.Health.IntervalSec {
		t.Fatalf("saved profile health interval = %d, want %d", saved.Health.IntervalSec, doc.Health.IntervalSec)
	}
}
