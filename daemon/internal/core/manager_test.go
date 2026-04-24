package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func TestIgnorableCleanupScriptError(t *testing.T) {
	if !ignorableCleanupScriptError(errors.New("script not found: /data/adb/privstack/scripts/dns.sh: no such file or directory")) {
		t.Fatal("missing cleanup script should be treated as an idempotent cleanup no-op")
	}
	if ignorableCleanupScriptError(errors.New("exec iptables.sh stop: exit status 2")) {
		t.Fatal("real cleanup command failures must still be reported")
	}
}

func TestSelfTestAppsAreBuiltInAlwaysDirect(t *testing.T) {
	for _, packageName := range SelfTestProtectedPackages {
		if !IsBuiltInAlwaysDirectPackage(packageName) {
			t.Fatalf("%s self-test app must stay direct by default", packageName)
		}
	}
}

func TestRuntimeErrorCarriesTypedStageDetails(t *testing.T) {
	report := newRuntimeStageReport("start")
	report.addStage("netstack-apply", "failed", "RULES_NOT_APPLIED", "iptables denied", true)
	err := runtimeErrorWithReport("iptables start", "RULES_NOT_APPLIED", errors.New("iptables denied"), true, report)
	runtimeErr, ok := err.(*RuntimeError)
	if !ok {
		t.Fatalf("expected RuntimeError, got %T", err)
	}
	if runtimeErr.RuntimeCode() != "RULES_NOT_APPLIED" {
		t.Fatalf("unexpected code: %#v", runtimeErr)
	}
	if !runtimeErr.RuntimeRollbackApplied() {
		t.Fatalf("rollback flag should be preserved: %#v", runtimeErr)
	}
	if !strings.Contains(runtimeErr.RuntimeUserMessage(), "routing rules") {
		t.Fatalf("expected user-facing message, got %q", runtimeErr.RuntimeUserMessage())
	}
	if runtimeErr.RuntimeDebug() != "iptables denied" {
		t.Fatalf("expected debug detail, got %q", runtimeErr.RuntimeDebug())
	}
	stageReport, ok := runtimeErr.RuntimeStageReport().(RuntimeStageReport)
	if !ok {
		t.Fatalf("expected runtime stage report, got %T", runtimeErr.RuntimeStageReport())
	}
	if stageReport.FailedStage != "netstack-apply" || stageReport.LastCode != "RULES_NOT_APPLIED" {
		t.Fatalf("unexpected stage report: %#v", stageReport)
	}
}

func TestRuntimeStageReportRecordsFailureAndFinish(t *testing.T) {
	report := newRuntimeStageReport("hot-swap")
	report.addStage("render-config", "ok", "", "/tmp/singbox.json", false)
	report.addStage("wait-tproxy", "failed", "TPROXY_PORT_DOWN", "timeout", true)

	if report.Status != "failed" {
		t.Fatalf("expected failed report, got %#v", report)
	}
	if report.FailedStage != "wait-tproxy" || report.LastCode != "TPROXY_PORT_DOWN" {
		t.Fatalf("unexpected failed stage metadata: %#v", report)
	}
	if !report.RollbackApplied {
		t.Fatalf("rollback flag should be set: %#v", report)
	}
	if report.FinishedAt.IsZero() {
		t.Fatalf("failed report should have finished timestamp: %#v", report)
	}

	report = newRuntimeStageReport("hot-swap")
	report.addStage("render-config", "ok", "", "", false)
	report.finishOK()
	if report.Status != "ok" || report.FinishedAt.IsZero() {
		t.Fatalf("expected ok finished report, got %#v", report)
	}
}

func TestSuccessfulStartReportUpdatesRuntimeReport(t *testing.T) {
	manager := NewCoreManager(config.DefaultConfig(), t.TempDir(), nil)
	report := newRuntimeStageReport("start")
	report.addStage("commit-state", "ok", "", "vless://example.com", false)

	manager.finishStartReport(report)

	if manager.LastRuntimeReport().Status != "ok" {
		t.Fatalf("successful start should leave runtime report finished: %#v", manager.LastRuntimeReport())
	}
	if manager.LastStartReport().Status != "ok" {
		t.Fatalf("successful start should leave start report finished: %#v", manager.LastStartReport())
	}
}

func TestStartStopsBeforeSpawnAndNetstackWhenConfigCheckFails(t *testing.T) {
	dataDir := t.TempDir()
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	singBoxPath := filepath.Join(binDir, "sing-box")
	if err := os.WriteFile(singBoxPath, []byte("#!/bin/sh\necho invalid config >&2\nexit 2\n"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Node.Address = "example.com"
	cfg.Node.UUID = "00000000-0000-0000-0000-000000000000"
	manager := NewCoreManager(cfg, dataDir, nil)

	err := manager.Start(cfg.ResolveProfile())
	if err == nil {
		t.Fatal("expected config-check failure")
	}
	runtimeErr, ok := err.(*RuntimeError)
	if !ok {
		t.Fatalf("expected RuntimeError, got %T: %v", err, err)
	}
	if runtimeErr.RuntimeCode() != "CONFIG_CHECK_FAILED" {
		t.Fatalf("expected CONFIG_CHECK_FAILED, got %#v", runtimeErr)
	}
	if manager.GetState() != StateStopped {
		t.Fatalf("failed config check must leave core stopped, got %s", manager.GetState())
	}
	report := manager.LastStartReport()
	if report.FailedStage != "config-check" {
		t.Fatalf("expected config-check failed stage, got %#v", report)
	}
	for _, stage := range report.Stages {
		if stage.Name == "spawn-core" || stage.Name == "netstack-apply" {
			t.Fatalf("config-check failure must not reach %s: %#v", stage.Name, report)
		}
	}
}

func TestScriptEnvIncludesLocalHelperPorts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Panel.Inbounds = []byte(`{"socksPort":10808,"httpPort":10809}`)
	manager := NewCoreManager(cfg, t.TempDir(), nil)

	env := manager.scriptEnv()
	if env["SOCKS_PORT"] != "10808" {
		t.Fatalf("expected SOCKS_PORT=10808, got %q", env["SOCKS_PORT"])
	}
	if env["HTTP_PORT"] != "10809" {
		t.Fatalf("expected HTTP_PORT=10809, got %q", env["HTTP_PORT"])
	}
}

func TestScriptEnvDisablesLocalHelperPortsByDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := NewCoreManager(cfg, t.TempDir(), nil)

	env := manager.scriptEnv()
	if env["SOCKS_PORT"] != "0" {
		t.Fatalf("default SOCKS_PORT must stay disabled, got %q", env["SOCKS_PORT"])
	}
	if env["HTTP_PORT"] != "0" {
		t.Fatalf("default HTTP_PORT must stay disabled, got %q", env["HTTP_PORT"])
	}
}

func TestRuntimeListenerWaitsIncludeDNSAndOptionalAPI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.TProxyPort = 19053
	cfg.Proxy.DNSPort = 19056
	cfg.Proxy.APIPort = 19090
	manager := NewCoreManager(cfg, t.TempDir(), nil)

	specs := manager.runtimeListenerWaits()
	if len(specs) != 3 {
		t.Fatalf("expected tproxy, DNS, and API listener waits, got %#v", specs)
	}
	expected := []struct {
		stage string
		code  string
		port  int
	}{
		{"wait-tproxy", "TPROXY_PORT_DOWN", 19053},
		{"wait-dns", "DNS_LISTENER_DOWN", 19056},
		{"wait-api", "API_PORT_DOWN", 19090},
	}
	for i, want := range expected {
		if specs[i].Stage != want.stage || specs[i].Code != want.code || specs[i].Port != want.port {
			t.Fatalf("unexpected listener wait %d: got %#v want %#v", i, specs[i], want)
		}
	}
}

func TestRuntimeListenerWaitsSkipDisabledAPI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.APIPort = 0
	manager := NewCoreManager(cfg, t.TempDir(), nil)

	specs := manager.runtimeListenerWaits()
	if len(specs) != 2 {
		t.Fatalf("disabled API should leave only tproxy and DNS waits, got %#v", specs)
	}
	if specs[0].Port != 10853 || specs[1].Port != 10856 {
		t.Fatalf("default listener ports not applied: %#v", specs)
	}
}
