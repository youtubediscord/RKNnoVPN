package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/updater"
)

func (d *daemon) handleUpdateCheck(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	info, err := updater.CheckForUpdate(updater.NormalizeVersionTag(Version))
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "update check failed: " + err.Error(),
		}
	}
	return info, nil
}

func (d *daemon) handleUpdateDownload(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	info, err := updater.CheckForUpdate(updater.NormalizeVersionTag(Version))
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "update check failed: " + err.Error(),
		}
	}

	if !info.HasUpdate {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "no update available",
		}
	}

	destDir := filepath.Join(d.dataDir, "update")
	downloaded, err := updater.DownloadUpdate(info, destDir, func(downloaded, total int64) {
		log.Printf("[updater] download progress: %d / %d bytes", downloaded, total)
	})
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "download failed: " + err.Error(),
		}
	}

	return downloaded, nil
}

func (d *daemon) handleUpdateInstall(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	var p struct {
		ModulePath string `json:"module_path"`
		ApkPath    string `json:"apk_path"`
	}
	if params != nil {
		_ = json.Unmarshal(*params, &p)
	}

	updateDir := filepath.Join(d.dataDir, "update")
	expectedModulePath := filepath.Join(updateDir, "module.zip")
	expectedApkPath := filepath.Join(updateDir, "panel.apk")
	if p.ModulePath == "" {
		p.ModulePath = expectedModulePath
	}
	if p.ApkPath == "" {
		p.ApkPath = expectedApkPath
	}
	p.ModulePath = filepath.Clean(p.ModulePath)
	p.ApkPath = filepath.Clean(p.ApkPath)
	if p.ModulePath != filepath.Clean(expectedModulePath) || p.ApkPath != filepath.Clean(expectedApkPath) {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "update-install only accepts verified artifacts from the canonical update directory",
		}
	}

	moduleExists := false
	if _, err := os.Stat(p.ModulePath); err == nil {
		moduleExists = true
	}
	apkExists := false
	if _, err := os.Stat(p.ApkPath); err == nil {
		apkExists = true
	}
	if !moduleExists && !apkExists {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "no downloaded update files found",
		}
	}
	if moduleExists != apkExists {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "this update requires both module and APK artifacts",
		}
	}
	if err := updater.VerifyDownloadedUpdate(p.ModulePath, p.ApkPath); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "update artifacts are not checksum-verified: " + err.Error(),
		}
	}
	verifiedManifest, err := updater.ReadVerifiedUpdateManifest(updateDir)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "update artifacts are not manifest-verified: " + err.Error(),
		}
	}
	modulePreflight, err := updater.PreflightModuleUpdate(p.ModulePath, d.dataDir)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "module update preflight failed: " + err.Error(),
		}
	}
	if modulePreflight.Version != updater.NormalizeVersionTag(verifiedManifest.LatestVersion) {
		return nil, &ipc.RPCError{
			Code: ipc.CodeInvalidParams,
			Message: fmt.Sprintf(
				"module version %s does not match verified update version %s",
				modulePreflight.Version,
				updater.NormalizeVersionTag(verifiedManifest.LatestVersion),
			),
		}
	}

	wasRunning := d.coreMgr.GetState() == core.StateRunning ||
		d.coreMgr.GetState() == core.StateDegraded

	status, err := d.runtimeV2.RunOperation(runtimev2.OperationUpdateInstall, runtimev2.PhaseStopping, func(generation int64) error {
		installTracker := updater.NewInstallTracker(d.dataDir, generation, p.ModulePath, p.ApkPath)
		if err := installTracker.Begin(); err != nil {
			return fmt.Errorf("record update install state: %w", err)
		}
		markStep := func(name, status, code, detail string) {
			d.runtimeV2.SetActiveOperationStep(generation, name, status, code, detail)
			if err := installTracker.Step(name, status, code, detail); err != nil {
				log.Printf("[updater] warning: record install step %s/%s: %v", name, status, err)
			}
		}
		moduleUpdated := false
		defer func() {
			if moduleUpdated {
				markStep("update-schedule-self-exit", "ok", "", "daemon restart scheduled")
				go updater.ScheduleSelfExit(updater.SelfExitDelay)
			}
		}()
		if apkExists {
			markStep("update-install-apk", "running", "APK_INSTALLING", filepath.Base(p.ApkPath))
			if err := updater.InstallApkUpdate(p.ApkPath); err != nil {
				log.Printf("[updater] APK install failed: %v", err)
				markStep("update-install-apk", "failed", "APK_INSTALL_FAILED", err.Error())
				return fmt.Errorf("apk install failed: %w", err)
			}
			if err := installTracker.MarkAPKInstalled(); err != nil {
				log.Printf("[updater] warning: record APK install success: %v", err)
			}
			markStep("update-install-apk", "ok", "", "")
		}

		if moduleExists {
			markStep("update-stop-runtime", "running", "UPDATE_STOP_RUNTIME", "stopping runtime before module install")
			if err := d.failIfResetInProgress(); err != nil {
				markStep("update-stop-runtime", "failed", runtimeErrorCode(err, "RESET_IN_PROGRESS"), err.Error())
				return err
			}
			d.stopSubsystems()
			if err := d.coreMgr.Stop(); err != nil {
				log.Printf("[updater] warning: failed to stop core: %v", err)
			}
			markStep("update-stop-runtime", "ok", "", "")
		}

		if moduleExists {
			markStep("update-install-module", "running", "MODULE_INSTALLING", filepath.Base(p.ModulePath))
			moduleDir := "/data/adb/modules/rknnovpn"
			if err := updater.InstallModuleUpdate(p.ModulePath, d.dataDir, moduleDir); err != nil {
				if wasRunning {
					d.restoreCurrentRuntimeAfterFailedUpdate()
				}
				markStep("update-install-module", "failed", "MODULE_INSTALL_FAILED", err.Error())
				return fmt.Errorf("module install failed: %w", err)
			}
			moduleUpdated = true
			if err := installTracker.MarkModuleInstalled(); err != nil {
				log.Printf("[updater] warning: record module install success: %v", err)
			}
			markStep("update-install-module", "ok", "", "")
		}

		markStep("update-cleanup-downloads", "running", "UPDATE_CLEANUP", updateDir)
		if err := os.RemoveAll(updateDir); err != nil {
			markStep("update-cleanup-downloads", "failed", "UPDATE_CLEANUP_FAILED", err.Error())
			return fmt.Errorf("update cleanup failed: %w", err)
		}
		markStep("update-cleanup-downloads", "ok", "", "")
		if err := installTracker.Complete(); err != nil {
			log.Printf("[updater] warning: record completed update install state: %v", err)
		}

		return nil
	})
	if err != nil {
		return nil, d.rpcErrorFromRuntimeError(err)
	}

	return status, nil
}

func (d *daemon) restoreCurrentRuntimeAfterFailedUpdate() {
	if err := d.failIfResetInProgress(); err != nil {
		log.Printf("[updater] skipping runtime restore while reset is active: %v", err)
		return
	}

	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	profile := cfg.ResolveProfile()
	if profile.Address == "" {
		return
	}
	if err := d.coreMgr.Start(profile); err != nil {
		log.Printf("[updater] warning: failed to restore previous runtime after failed update: %v", err)
		return
	}
	d.rescueMgr.Reset()
	d.startSubsystems()
}
