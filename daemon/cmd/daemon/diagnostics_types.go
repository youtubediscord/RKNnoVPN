package main

import "github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"

const controlProtocolVersion = 5

type diagnosticCommandResult = diagnostics.CommandResult

type diagnosticFileStatus = diagnostics.FileStatus

type diagnosticLogSection = diagnostics.LogSection

type diagnosticJSONSection = diagnostics.JSONSection

type diagnosticPortStatus = diagnostics.PortStatus

type diagnosticPortConflict = diagnostics.PortConflict

type diagnosticPackageResolution = diagnostics.PackageResolution

type diagnosticSummary = diagnostics.Summary

type diagnosticCompatSummary = diagnostics.CompatSummary

type diagnosticRuntimeSummary = diagnostics.RuntimeSummary

type diagnosticProfileSummary = diagnostics.ProfileSummary

type diagnosticNodeTestSummary = diagnostics.NodeTestSummary

type diagnosticRoutingSummary = diagnostics.RoutingSummary
