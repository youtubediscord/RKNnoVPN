package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) initRuntimeV2() {
	d.runtimeV2 = runtimev2.NewOrchestrator(
		d.desiredStateV2(),
		newRootRuntimeBackend(d),
	)
	d.runtimeV2.SetStatusObserver(func(status runtimev2.Status) {
		if err := runtimev2.WriteRuntimeState(d.dataDir, status); err != nil {
			log.Printf("runtime_state persist failed: %v", err)
		}
	})
	d.refreshRuntimeV2Compatibility()
	if err := runtimev2.WriteRuntimeState(d.dataDir, d.runtimeV2.Status()); err != nil {
		log.Printf("runtime_state initial persist failed: %v", err)
	}
}

func (d *daemon) refreshRuntimeV2Compatibility() {
	if d.runtimeV2 == nil {
		return
	}
	release := diagnostics.ReleaseIntegrityReport(d.dataDir)
	contracts := ipc.MethodContracts()
	methods := make([]runtimev2.MethodCapability, 0, len(contracts))
	for _, contract := range contracts {
		methods = append(methods, runtimev2.MethodCapability{
			Method:     contract.Method,
			Capability: contract.Capability,
		})
	}
	d.runtimeV2.SetCompatibility(runtimev2.CompatibilityStatus{
		DaemonVersion:          Version,
		ModuleVersion:          diagnostics.ReadModuleVersion()["version"],
		CurrentReleaseVersion:  release.Version,
		CurrentReleaseOK:       release.OK,
		CurrentReleaseError:    releaseIntegrityStatusDetail(release),
		ControlProtocolVersion: controlProtocolVersion,
		SchemaVersion:          config.CurrentSchemaVersion,
		PanelMinVersion:        Version,
		Capabilities:           ipc.SupportedCapabilities(),
		SupportedMethods:       ipc.SupportedMethods(),
		Methods:                methods,
	})
}

func releaseIntegrityStatusDetail(release diagnostics.ReleaseIntegrity) string {
	if release.OK {
		return ""
	}
	details := make([]string, 0, 4)
	if release.Error != "" {
		details = append(details, release.Error)
	}
	if release.MissingCurrent {
		details = append(details, "current release link missing")
	}
	if release.MissingManifest {
		details = append(details, "manifest missing")
	}
	if len(release.MissingFiles) > 0 {
		details = append(details, fmt.Sprintf("%d file(s) missing", len(release.MissingFiles)))
	}
	if len(release.Mismatches) > 0 {
		details = append(details, fmt.Sprintf("%d file hash mismatch(es)", len(release.Mismatches)))
	}
	return strings.Join(details, "; ")
}
