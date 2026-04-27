package main

import (
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

type diagnosticReleaseIntegrity = diagnostics.ReleaseIntegrity

func diagnosticReleaseIntegrityReport(dataDir string) diagnosticReleaseIntegrity {
	return diagnostics.ReleaseIntegrityReport(dataDir)
}

func diagnosticReleaseIntegrityIssues(report diagnosticReleaseIntegrity) []string {
	return diagnostics.ReleaseIntegrityIssues(report)
}

func supportedCapabilities() []string {
	return ipc.SupportedCapabilities()
}

func supportedRPCMethods() []string {
	return ipc.SupportedMethods()
}
