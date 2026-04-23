package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
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
			"dns":           {Pass: false, Detail: "dns timeout"},
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
