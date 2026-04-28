package resetcontroller

import (
	"fmt"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type Hooks interface {
	StopSubsystems()
	RescueResetCore() error
	ScriptEnv() map[string]string
	ExecRescueReset(scriptPath string, env map[string]string) error
	ClearRuntimeState()
	VerifyCleanup() []string
}

type Controller struct {
	Paths    Paths
	Backend  runtimev2.BackendKind
	Hooks    Hooks
	Observer StepObserver
	Now      func() time.Time
}

func (c Controller) Run(generation int64) runtimev2.ResetReport {
	report := NewReportController(generation, c.Backend, c.Observer)
	c.runEnterResetMode(report)
	c.runStopSubsystems(report)
	c.runStopCore(report)
	c.runRescueCleanupScript(report)
	if report.HasErrors() {
		c.runLeaveResetMode(report)
		return report.Finish()
	}
	c.runClearRuntimeState(report)
	c.runRemoveStaleRuntimeFiles(report)
	c.runVerifyCleanup(report)
	c.runLeaveResetMode(report)
	return report.Finish()
}

func (c Controller) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Controller) runEnterResetMode(report *ReportController) {
	report.Run("enter-reset-mode", func() (string, string, error) {
		if err := EnterResetMode(c.Paths, c.now()); err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})
}

func (c Controller) runStopSubsystems(report *ReportController) {
	report.Run("stop-subsystems", func() (string, string, error) {
		c.Hooks.StopSubsystems()
		return "ok", "", nil
	})
}

func (c Controller) runStopCore(report *ReportController) {
	report.Run("stop-core", func() (string, string, error) {
		if err := c.Hooks.RescueResetCore(); err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})
}

func (c Controller) runRescueCleanupScript(report *ReportController) {
	report.Run("rescue-cleanup-script", func() (string, string, error) {
		if err := c.Hooks.ExecRescueReset(c.Paths.RescueResetScript(), c.Hooks.ScriptEnv()); err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})
}

func (c Controller) runClearRuntimeState(report *ReportController) {
	report.Run("clear-runtime-state", func() (string, string, error) {
		c.Hooks.ClearRuntimeState()
		return "ok", "", nil
	})
}

func (c Controller) runRemoveStaleRuntimeFiles(report *ReportController) {
	report.Run("remove-stale-runtime-files", func() (string, string, error) {
		removed, err := RemoveStaleRuntimeFiles(c.Paths)
		if err != nil {
			return "", "", err
		}
		if len(removed) == 0 {
			return "already_clean", "", nil
		}
		return "ok", strings.Join(removed, ", "), nil
	})
}

func (c Controller) runVerifyCleanup(report *ReportController) {
	report.Run("verify-cleanup", func() (string, string, error) {
		leftovers := c.Hooks.VerifyCleanup()
		report.SetLeftovers(leftovers)
		if len(leftovers) == 0 {
			return "ok", "", nil
		}
		report.AddWarning(fmt.Sprintf("verify-cleanup: %d leftover(s) after reset", len(leftovers)))
		return "warning", strings.Join(leftovers, "; "), nil
	})
}

func (c Controller) runLeaveResetMode(report *ReportController) {
	report.Run("leave-reset-mode", func() (string, string, error) {
		if err := LeaveResetMode(c.Paths); err != nil {
			return "", "", err
		}
		return "ok", "", nil
	})
}
