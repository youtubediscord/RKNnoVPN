package main

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
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
		return updater.RunInstallTransaction(updater.InstallTransaction{
			DataDir:           d.dataDir,
			Generation:        generation,
			Artifacts:         artifacts,
			WasRuntimeRunning: wasRunning,
			Hooks: updater.InstallHooks{
				SetOperationStep: func(name, status, code, detail string) {
					d.runtimeV2.SetActiveOperationStep(generation, name, status, code, detail)
				},
				StopRuntimeForModuleInstall: func() error {
					if err := d.failIfResetInProgress(); err != nil {
						return err
					}
					d.stopSubsystems()
					if err := d.coreMgr.Stop(); err != nil {
						log.Printf("[updater] warning: failed to stop core: %v", err)
					}
					return nil
				},
				RestoreRuntimeAfterModuleFail: d.restoreCurrentRuntimeAfterFailedUpdate,
				RuntimeErrorCode:              rootruntime.RuntimeErrorCode,
				ScheduleSelfExit: func() {
					go updater.ScheduleSelfExit(updater.SelfExitDelay)
				},
				Logf: func(format string, args ...interface{}) {
					log.Printf("[updater] "+format, args...)
				},
			},
		})
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
