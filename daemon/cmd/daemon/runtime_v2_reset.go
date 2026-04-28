package main

import (
	"fmt"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/resetcontroller"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) failIfResetInProgress() error {
	return resetcontroller.FailIfResetInProgress(d.resetPaths(), time.Now())
}

func (d *daemon) recoverStaleResetLock(generation int64) (*runtimev2.ResetReport, error) {
	decision, err := resetcontroller.DecideStaleRecovery(d.resetPaths(), time.Now())
	if err != nil {
		return nil, err
	}
	if !decision.RunCleanup {
		return nil, nil
	}

	report := d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
	if len(report.Errors) > 0 || report.Status == "partial" || report.Status == "failed" {
		message := ""
		if len(report.Errors) > 0 {
			message = report.Errors[0]
		}
		if message == "" {
			message = "stale reset lock recovery failed"
		}
		err := fmt.Errorf("%s: %s", decision.Detail, message)
		return &report, rootruntime.RuntimeErrorWithResetReport(err, report)
	}
	return &report, nil
}

func (d *daemon) resetNetworkStateReport(generation int64, backend runtimev2.BackendKind) runtimev2.ResetReport {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	return resetcontroller.Controller{
		Paths:   d.resetPaths(),
		Backend: backend,
		Hooks:   daemonResetHooks{d: d},
		Observer: func(generation int64, name string) {
			if d.runtimeV2 != nil {
				d.runtimeV2.SetActiveOperationStep(generation, name, "running", "", "")
			}
		},
	}.Run(generation)
}

type daemonResetHooks struct {
	d *daemon
}

func (h daemonResetHooks) StopSubsystems() {
	h.d.stopSubsystems()
}

func (h daemonResetHooks) RescueResetCore() error {
	return h.d.coreMgr.RescueReset()
}

func (h daemonResetHooks) ScriptEnv() map[string]string {
	h.d.mu.Lock()
	cfg := h.d.cfg
	h.d.mu.Unlock()
	return rootruntime.BuildScriptEnv(cfg, h.d.dataDir)
}

func (h daemonResetHooks) ExecRescueReset(scriptPath string, env map[string]string) error {
	return core.ExecScript(scriptPath, "daemon-reset", env)
}

func (h daemonResetHooks) ClearRuntimeState() {
	h.d.rescueMgr.Reset()
	h.d.coreMgr.ResetState()
	h.d.healthMon.Clear()
	h.d.resetRuntimeMetrics()
}

func (h daemonResetHooks) VerifyCleanup() []string {
	h.d.mu.Lock()
	cfg := h.d.cfg
	h.d.mu.Unlock()
	return h.d.collectNetworkLeftovers(cfg)
}

func (d *daemon) enterResetMode() error {
	return resetcontroller.EnterResetMode(d.resetPaths(), time.Now())
}

func (d *daemon) leaveResetMode() error {
	return resetcontroller.LeaveResetMode(d.resetPaths())
}

func (d *daemon) shouldSkipRootReconcile() (bool, string) {
	return resetcontroller.ShouldSkipRootReconcile(d.resetPaths())
}

func (d *daemon) resetLockPath() string {
	return d.resetPaths().ResetLock()
}

func (d *daemon) activeFilePath() string {
	return d.resetPaths().ActiveMarker()
}

func (d *daemon) manualFlagPath() string {
	return d.resetPaths().ManualFlag()
}

func (d *daemon) resetPaths() resetcontroller.Paths {
	return resetcontroller.Paths{DataDir: d.dataDir}
}

func (d *daemon) removeStaleRuntimeFiles() ([]string, error) {
	return resetcontroller.RemoveStaleRuntimeFiles(d.resetPaths())
}

func (d *daemon) collectNetworkLeftovers(cfg *config.Config) []string {
	if d.collectLeftoversOverride != nil {
		return d.collectLeftoversOverride(cfg)
	}
	if cfg == nil {
		return []string{"config unavailable for cleanup verification"}
	}
	env := rootruntime.BuildScriptEnv(cfg, d.dataDir)
	report := netstack.New(d.dataDir, env, core.ExecScript).
		WithExecCommand(core.ExecCommand).
		VerifyCleanup()
	return report.Leftovers
}
