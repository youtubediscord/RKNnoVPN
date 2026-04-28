package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

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

	_, err := newRootRuntimeBackend(d).Start(runtimev2.DesiredState{BackendKind: runtimev2.BackendRootTProxy}, 1)
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
		if resetReportStep(report, stepName).Name == "" {
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
	d := newTestResetDaemon(t, []string{"iptables mangle rule remains: -A RKNNOVPN_PRE"}, true)

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
