package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/modulecontract"
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
	artifacts, err := updater.ResolveInstallArtifacts(d.dataDir, params)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: err.Error(),
		}
	}
	if err := updater.VerifyDownloadedUpdate(artifacts.ModulePath, artifacts.ApkPath); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "update artifacts are not checksum-verified: " + err.Error(),
		}
	}
	verifiedManifest, err := updater.ReadVerifiedUpdateManifest(artifacts.UpdateDir)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "update artifacts are not manifest-verified: " + err.Error(),
		}
	}
	modulePreflight, err := updater.PreflightModuleUpdate(artifacts.ModulePath, d.dataDir)
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
		installTracker := updater.NewInstallTracker(d.dataDir, generation, artifacts.ModulePath, artifacts.ApkPath)
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
		if artifacts.ApkExists {
			markStep("update-install-apk", "running", "APK_INSTALLING", filepath.Base(artifacts.ApkPath))
			if err := updater.InstallApkUpdate(artifacts.ApkPath); err != nil {
				log.Printf("[updater] APK install failed: %v", err)
				markStep("update-install-apk", "failed", "APK_INSTALL_FAILED", err.Error())
				return fmt.Errorf("apk install failed: %w", err)
			}
			if err := installTracker.MarkAPKInstalled(); err != nil {
				log.Printf("[updater] warning: record APK install success: %v", err)
			}
			markStep("update-install-apk", "ok", "", "")
		}

		if artifacts.ModuleExists {
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

		if artifacts.ModuleExists {
			markStep("update-install-module", "running", "MODULE_INSTALLING", filepath.Base(artifacts.ModulePath))
			moduleDir := modulecontract.NewPaths(d.dataDir).Dir()
			if err := updater.InstallModuleUpdate(artifacts.ModulePath, d.dataDir, moduleDir); err != nil {
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

		markStep("update-cleanup-downloads", "running", "UPDATE_CLEANUP", artifacts.UpdateDir)
		if err := os.RemoveAll(artifacts.UpdateDir); err != nil {
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
