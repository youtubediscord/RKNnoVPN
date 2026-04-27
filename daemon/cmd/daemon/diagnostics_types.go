package main

import (
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
)

const controlProtocolVersion = 5

type diagnosticCommandResult struct {
	Command string   `json:"command"`
	Lines   []string `json:"lines,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type diagnosticFileStatus struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Executable bool   `json:"executable,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Error      string `json:"error,omitempty"`
}

type diagnosticLogSection struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Lines   []string `json:"lines,omitempty"`
	Missing bool     `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type diagnosticJSONSection struct {
	Path    string      `json:"path"`
	Value   interface{} `json:"value,omitempty"`
	Missing bool        `json:"missing,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type diagnosticPortStatus struct {
	Role         string `json:"role,omitempty"`
	Port         int    `json:"port"`
	TCPListening bool   `json:"tcpListening"`
	Conflict     bool   `json:"conflict,omitempty"`
}

type diagnosticPortConflict struct {
	Port  int      `json:"port"`
	Roles []string `json:"roles"`
}

type diagnosticPackageResolution struct {
	Mode                         string                        `json:"mode,omitempty"`
	RequestedPackages            []string                      `json:"requestedPackages,omitempty"`
	SelectedSource               string                        `json:"selectedSource,omitempty"`
	ResolvedUIDCount             int                           `json:"resolvedUidCount"`
	UnresolvedPackages           []string                      `json:"unresolvedPackages,omitempty"`
	AlwaysDirectSource           string                        `json:"alwaysDirectSource,omitempty"`
	AlwaysDirectResolvedUIDCount int                           `json:"alwaysDirectResolvedUidCount"`
	Sources                      []core.PackageUIDSourceStatus `json:"sources,omitempty"`
	Errors                       []string                      `json:"errors,omitempty"`
	Warnings                     []string                      `json:"warnings,omitempty"`
}

type diagnosticSummary struct {
	Status              string                      `json:"status"`
	HealthCode          string                      `json:"healthCode,omitempty"`
	HealthDetail        string                      `json:"healthDetail,omitempty"`
	OperationalHealthy  bool                        `json:"operationalHealthy"`
	RebootRequired      bool                        `json:"rebootRequired"`
	IssueCount          int                         `json:"issueCount"`
	Issues              []string                    `json:"issues,omitempty"`
	CompatibilityIssues []string                    `json:"compatibilityIssues,omitempty"`
	PrivacyIssues       []string                    `json:"privacyIssues,omitempty"`
	Compatibility       diagnosticCompatSummary     `json:"compatibility"`
	Runtime             diagnosticRuntimeSummary    `json:"runtime"`
	Profile             diagnosticProfileSummary    `json:"profile"`
	Routing             diagnosticRoutingSummary    `json:"routing"`
	NodeTests           diagnosticNodeTestSummary   `json:"nodeTests"`
	PackageResolution   diagnosticPackageResolution `json:"packageResolution"`
}

type diagnosticCompatSummary struct {
	DaemonVersion          string `json:"daemonVersion"`
	ModuleVersion          string `json:"moduleVersion"`
	ControlProtocolVersion int    `json:"controlProtocolVersion"`
	SchemaVersion          int    `json:"schemaVersion"`
	PanelMinVersion        string `json:"panelMinVersion"`
	CurrentReleaseVersion  string `json:"currentReleaseVersion,omitempty"`
	CurrentReleaseOK       bool   `json:"currentReleaseOk"`
	SingBoxCheckOK         bool   `json:"singBoxCheckOk"`
}

type diagnosticRuntimeSummary struct {
	StageOperation   string `json:"stageOperation,omitempty"`
	StageStatus      string `json:"stageStatus,omitempty"`
	FailedStage      string `json:"failedStage,omitempty"`
	LastCode         string `json:"lastCode,omitempty"`
	RollbackApplied  bool   `json:"rollbackApplied,omitempty"`
	RuntimeReportAge string `json:"runtimeReportAge,omitempty"`
}

type diagnosticProfileSummary struct {
	SchemaVersion          int    `json:"schemaVersion"`
	DesiredGeneration      int64  `json:"desiredGeneration"`
	AppliedGeneration      int64  `json:"appliedGeneration"`
	ActiveNodeMode         string `json:"activeNodeMode"`
	ActiveNodeID           string `json:"activeNodeId,omitempty"`
	NodeCount              int    `json:"nodeCount"`
	LiveNodeCount          int    `json:"liveNodeCount"`
	StaleNodeCount         int    `json:"staleNodeCount"`
	SubscriptionCount      int    `json:"subscriptionCount"`
	StaleSubscriptionNodes int    `json:"staleSubscriptionNodes"`
	LastOperation          string `json:"lastOperation,omitempty"`
	LastOperationStatus    string `json:"lastOperationStatus,omitempty"`
}

type diagnosticNodeTestSummary struct {
	Total    int `json:"total"`
	Usable   int `json:"usable"`
	Unusable int `json:"unusable"`
	TCPOnly  int `json:"tcpOnly"`
}

type diagnosticRoutingSummary struct {
	Mode               string   `json:"mode,omitempty"`
	ActiveNodeMode     string   `json:"activeNodeMode"`
	ActiveNodeID       string   `json:"activeNodeId,omitempty"`
	ActiveNodeName     string   `json:"activeNodeName,omitempty"`
	ActiveNodeProtocol string   `json:"activeNodeProtocol,omitempty"`
	NodeCount          int      `json:"nodeCount"`
	Groups             []string `json:"groups,omitempty"`
	AppGroupRouteCount int      `json:"appGroupRouteCount,omitempty"`
	SharingEnabled     bool     `json:"sharingEnabled"`
}

type diagnosticReleaseIntegrity struct {
	CurrentPath     string   `json:"currentPath"`
	ReleasePath     string   `json:"releasePath,omitempty"`
	ManifestPath    string   `json:"manifestPath,omitempty"`
	Version         string   `json:"version,omitempty"`
	InstalledAt     string   `json:"installedAt,omitempty"`
	CheckedFiles    int      `json:"checkedFiles"`
	OK              bool     `json:"ok"`
	MissingCurrent  bool     `json:"missingCurrent,omitempty"`
	MissingManifest bool     `json:"missingManifest,omitempty"`
	MissingFiles    []string `json:"missingFiles,omitempty"`
	Mismatches      []string `json:"mismatches,omitempty"`
	Error           string   `json:"error,omitempty"`
}

type diagnosticReleaseManifest struct {
	Version     string            `json:"version"`
	InstalledAt string            `json:"installed_at"`
	Files       map[string]string `json:"files_sha256"`
}
