package updater

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/modulecontract"
)

type InstallTransaction struct {
	DataDir           string
	ModuleDir         string
	Generation        int64
	Artifacts         InstallArtifacts
	WasRuntimeRunning bool
	Hooks             InstallHooks
}

type InstallHooks struct {
	SetOperationStep              func(name, status, code, detail string)
	StopRuntimeForModuleInstall   func() error
	RestoreRuntimeAfterModuleFail func()
	RuntimeErrorCode              func(error, string) string
	ScheduleSelfExit              func()
	Logf                          func(format string, args ...interface{})
}

func RunInstallTransaction(tx InstallTransaction) error {
	if tx.ModuleDir == "" {
		tx.ModuleDir = modulecontract.NewPaths(tx.DataDir).Dir()
	}
	installTracker := NewInstallTracker(tx.DataDir, tx.Generation, tx.Artifacts.ModulePath, tx.Artifacts.ApkPath)
	if err := installTracker.Begin(); err != nil {
		return fmt.Errorf("record update install state: %w", err)
	}

	markStep := func(name, status, code, detail string) {
		if tx.Hooks.SetOperationStep != nil {
			tx.Hooks.SetOperationStep(name, status, code, detail)
		}
		if err := installTracker.Step(name, status, code, detail); err != nil {
			installLogf(tx, "warning: record install step %s/%s: %v", name, status, err)
		}
	}

	moduleUpdated := false
	defer func() {
		if moduleUpdated {
			markStep("update-schedule-self-exit", "ok", "", "daemon restart scheduled")
			if tx.Hooks.ScheduleSelfExit != nil {
				tx.Hooks.ScheduleSelfExit()
			}
		}
	}()

	if tx.Artifacts.ApkExists {
		markStep("update-install-apk", "running", "APK_INSTALLING", filepath.Base(tx.Artifacts.ApkPath))
		if err := InstallApkUpdate(tx.Artifacts.ApkPath); err != nil {
			installLogf(tx, "APK install failed: %v", err)
			markStep("update-install-apk", "failed", "APK_INSTALL_FAILED", err.Error())
			return fmt.Errorf("apk install failed: %w", err)
		}
		if err := installTracker.MarkAPKInstalled(); err != nil {
			installLogf(tx, "warning: record APK install success: %v", err)
		}
		markStep("update-install-apk", "ok", "", "")
	}

	if tx.Artifacts.ModuleExists {
		markStep("update-stop-runtime", "running", "UPDATE_STOP_RUNTIME", "stopping runtime before module install")
		if tx.Hooks.StopRuntimeForModuleInstall != nil {
			if err := tx.Hooks.StopRuntimeForModuleInstall(); err != nil {
				markStep("update-stop-runtime", "failed", installRuntimeErrorCode(tx, err, "RESET_IN_PROGRESS"), err.Error())
				return err
			}
		}
		markStep("update-stop-runtime", "ok", "", "")
	}

	if tx.Artifacts.ModuleExists {
		markStep("update-install-module", "running", "MODULE_INSTALLING", filepath.Base(tx.Artifacts.ModulePath))
		if err := InstallModuleUpdate(tx.Artifacts.ModulePath, tx.DataDir, tx.ModuleDir); err != nil {
			if tx.WasRuntimeRunning && tx.Hooks.RestoreRuntimeAfterModuleFail != nil {
				tx.Hooks.RestoreRuntimeAfterModuleFail()
			}
			markStep("update-install-module", "failed", "MODULE_INSTALL_FAILED", err.Error())
			return fmt.Errorf("module install failed: %w", err)
		}
		moduleUpdated = true
		if err := installTracker.MarkModuleInstalled(); err != nil {
			installLogf(tx, "warning: record module install success: %v", err)
		}
		markStep("update-install-module", "ok", "", "")
	}

	markStep("update-cleanup-downloads", "running", "UPDATE_CLEANUP", tx.Artifacts.UpdateDir)
	if err := os.RemoveAll(tx.Artifacts.UpdateDir); err != nil {
		markStep("update-cleanup-downloads", "failed", "UPDATE_CLEANUP_FAILED", err.Error())
		return fmt.Errorf("update cleanup failed: %w", err)
	}
	markStep("update-cleanup-downloads", "ok", "", "")
	if err := installTracker.Complete(); err != nil {
		installLogf(tx, "warning: record completed update install state: %v", err)
	}

	return nil
}

func installRuntimeErrorCode(tx InstallTransaction, err error, fallback string) string {
	if tx.Hooks.RuntimeErrorCode == nil {
		return fallback
	}
	return tx.Hooks.RuntimeErrorCode(err, fallback)
}

func installLogf(tx InstallTransaction, format string, args ...interface{}) {
	if tx.Hooks.Logf != nil {
		tx.Hooks.Logf(format, args...)
		return
	}
	log.Printf("[updater] "+format, args...)
}
