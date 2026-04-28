package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

const resetLockStaleAfter = 10 * time.Minute

type resetReportController struct {
	daemon     *daemon
	generation int64
	report     runtimev2.ResetReport
}

func newResetReportController(d *daemon, generation int64, backend runtimev2.BackendKind) *resetReportController {
	return &resetReportController{
		daemon:     d,
		generation: generation,
		report: runtimev2.ResetReport{
			BackendKind: backend,
			Generation:  generation,
			Status:      "ok",
		},
	}
}

func (c *resetReportController) run(name string, fn func() (string, string, error)) {
	if c.daemon.runtimeV2 != nil {
		c.daemon.runtimeV2.SetActiveOperationStep(c.generation, name, "running", "", "")
	}
	status, detail, err := fn()
	if status == "" {
		status = "ok"
	}
	step := runtimev2.ResetStep{Name: name, Status: status, Detail: detail}
	if err != nil {
		step.Status = "failed"
		step.Detail = err.Error()
		c.report.Status = "partial"
		c.report.Errors = append(c.report.Errors, name+": "+err.Error())
	}
	c.report.Steps = append(c.report.Steps, step)
}

func (c *resetReportController) finish() runtimev2.ResetReport {
	if len(c.report.Errors) > 0 {
		c.report.Status = "partial"
	} else if len(c.report.Warnings) > 0 || len(c.report.Leftovers) > 0 {
		c.report.Status = "clean_with_warnings"
	}
	if len(c.report.Leftovers) > 0 {
		c.report.RebootRequired = true
	}
	return c.report
}

func (d *daemon) failIfResetInProgress() error {
	active, stale, detail, err := d.inspectResetLock()
	if err != nil {
		return err
	}
	if active {
		if stale {
			return runtimev2.NewResetInProgressError(detail)
		}
		return runtimev2.NewResetInProgressError("reset is in progress")
	}
	return nil
}

func (d *daemon) recoverStaleResetLock(generation int64) (*runtimev2.ResetReport, error) {
	active, stale, detail, err := d.inspectResetLock()
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	if !stale {
		return nil, runtimev2.NewResetInProgressError("reset is in progress")
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
		err := fmt.Errorf("%s: %s", detail, message)
		return &report, runtimeErrorWithResetReport(err, report)
	}
	return &report, nil
}

func (d *daemon) inspectResetLock() (active bool, stale bool, detail string, err error) {
	info, err := os.Stat(d.resetLockPath())
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, "", nil
		}
		return false, false, "", fmt.Errorf("reset lock is not readable: %w", err)
	}

	startedAt := info.ModTime()
	if data, readErr := os.ReadFile(d.resetLockPath()); readErr == nil {
		text := strings.TrimSpace(string(data))
		if parsed, parseErr := time.Parse(time.RFC3339, text); parseErr == nil {
			startedAt = parsed
		}
	}

	age := time.Since(startedAt)
	if age < 0 {
		age = 0
	}
	detail = fmt.Sprintf("reset lock is present for %s", age.Truncate(time.Second))
	return true, age > resetLockStaleAfter, detail, nil
}

func (d *daemon) resetNetworkStateReport(generation int64, backend runtimev2.BackendKind) runtimev2.ResetReport {
	d.resetMu.Lock()
	defer d.resetMu.Unlock()

	controller := newResetReportController(d, generation, backend)
	controller.runEnterResetMode()
	controller.runStopSubsystems()
	controller.runStopCore()
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	controller.runRescueCleanupScript(cfg)
	controller.runClearRuntimeState()
	controller.runRemoveStaleRuntimeFiles()
	controller.runVerifyCleanup(cfg)
	controller.runLeaveResetMode()
	return controller.finish()
}

func (c *resetReportController) runEnterResetMode() {
	c.run("enter-reset-mode", func() (string, string, error) {
		if err := c.daemon.enterResetMode(); err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})
}

func (c *resetReportController) runStopSubsystems() {
	c.run("stop-subsystems", func() (string, string, error) {
		c.daemon.stopSubsystems()
		return "ok", "", nil
	})
}

func (c *resetReportController) runStopCore() {
	c.run("stop-core", func() (string, string, error) {
		if err := c.daemon.coreMgr.RescueReset(); err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})
}

func (c *resetReportController) runRescueCleanupScript(cfg *config.Config) {
	env := buildScriptEnv(cfg, c.daemon.dataDir)
	c.run("rescue-cleanup-script", func() (string, string, error) {
		err := core.ExecScript(filepath.Join(c.daemon.dataDir, "scripts", "rescue_reset.sh"), "daemon-reset", env)
		if isIgnorableResetScriptError(err) {
			return "already_clean", err.Error(), nil
		}
		if err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})
}

func (c *resetReportController) runClearRuntimeState() {
	c.run("clear-runtime-state", func() (string, string, error) {
		c.daemon.rescueMgr.Reset()
		c.daemon.coreMgr.ResetState()
		c.daemon.healthMon.Clear()
		c.daemon.resetRuntimeMetrics()
		return "ok", "", nil
	})
}

func (c *resetReportController) runRemoveStaleRuntimeFiles() {
	c.run("remove-stale-runtime-files", func() (string, string, error) {
		removed, err := c.daemon.removeStaleRuntimeFiles()
		if err != nil {
			return "", "", err
		}
		if len(removed) == 0 {
			return "already_clean", "", nil
		}
		return "ok", strings.Join(removed, ", "), nil
	})
}

func (c *resetReportController) runVerifyCleanup(cfg *config.Config) {
	c.run("verify-cleanup", func() (string, string, error) {
		leftovers := c.daemon.collectNetworkLeftovers(cfg)
		c.report.Leftovers = leftovers
		if len(leftovers) == 0 {
			return "ok", "", nil
		}
		c.report.RebootRequired = true
		c.report.Warnings = append(c.report.Warnings, fmt.Sprintf("verify-cleanup: %d leftover(s) after reset", len(leftovers)))
		return "warning", strings.Join(leftovers, "; "), nil
	})
}

func (c *resetReportController) runLeaveResetMode() {
	c.run("leave-reset-mode", func() (string, string, error) {
		if err := c.daemon.leaveResetMode(); err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})
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
	if d.collectLeftoversOverride != nil {
		return d.collectLeftoversOverride(cfg)
	}
	if cfg == nil {
		return []string{"config unavailable for cleanup verification"}
	}
	env := buildScriptEnv(cfg, d.dataDir)
	report := netstack.New(d.dataDir, env, core.ExecScript).
		WithExecCommand(core.ExecCommand).
		VerifyCleanup()
	return report.Leftovers
}

func isIgnorableResetScriptError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "script not found:") ||
		strings.Contains(lower, "no such file or directory")
}
