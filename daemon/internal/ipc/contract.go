package ipc

import "sort"

type OperationContract struct {
	Type           string   `json:"type"`
	AsyncResultVia string   `json:"asyncResultVia,omitempty"`
	Stages         []string `json:"stages,omitempty"`
}

type MethodContract struct {
	Method        string             `json:"method"`
	Capability    string             `json:"capability,omitempty"`
	Mutating      bool               `json:"mutating"`
	Async         bool               `json:"async"`
	Request       string             `json:"request"`
	Result        string             `json:"result"`
	ErrorCodes    []string           `json:"errorCodes,omitempty"`
	Operation     *OperationContract `json:"operation,omitempty"`
	Compatibility string             `json:"compatibility,omitempty"`
}

type Contract struct {
	Version                int              `json:"version"`
	ControlProtocolVersion int              `json:"controlProtocolVersion"`
	SchemaVersion          int              `json:"schemaVersion"`
	Capabilities           []string         `json:"capabilities"`
	Methods                []MethodContract `json:"methods"`
}

func SupportedMethods() []string {
	contracts := MethodContracts()
	methods := make([]string, 0, len(contracts))
	for _, contract := range contracts {
		methods = append(methods, contract.Method)
	}
	sort.Strings(methods)
	return methods
}

func SupportedCapabilities() []string {
	capabilities := []string{
		"backend.root-tproxy",
		"backend.reset.structured",
		"backend.reset.warnings.v1",
		"config.import.v2",
		"config.mutation.envelope.v1",
		"config.schema.v5",
		"diagnostics.bundle.v2",
		"diagnostics.health.v2",
		"diagnostics.testNodes.tcp-direct",
		"diagnostics.testNodes.url",
		"diagnostics.testNodes.v2",
		"ipc.contract.v1",
		"ipc.envelope.v1",
		"netstack.report.v1",
		"netstack.runtime.verify.v1",
		"netstack.verify.v1",
		"privacy.audit.v2",
		"privacy.localhost-listeners.v1",
		"privacy.loopback-dns.v1",
		"privacy.self-check.v1",
		"privacy.self-test-protected-apps.v1",
		"privacy.vpn-interface-patterns.v1",
		"profile.apply.v2",
		"profile.document.v2",
		"profile.importNodes.v2",
		"profile.subscription.v2",
		"runtime.logs",
		"runtime.v2",
		"update.install.v1",
	}
	sort.Strings(capabilities)
	return capabilities
}

func MethodContracts() []MethodContract {
	contracts := []MethodContract{
		{Method: "app.list", Capability: "privacy.audit.v2", Request: "empty", Result: "InstalledApp[]", ErrorCodes: commonReadErrors()},
		{Method: "app.resolveUid", Capability: "privacy.audit.v2", Request: "ResolveUidRequest", Result: "ResolveUidResult", ErrorCodes: commonReadErrors()},
		{Method: "audit", Capability: "privacy.audit.v2", Request: "empty", Result: "AuditReport", ErrorCodes: commonReadErrors()},
		{Method: "backend.applyDesiredState", Capability: "runtime.v2", Mutating: true, Request: "DesiredState", Result: "BackendStatusV2", ErrorCodes: runtimeMutationErrors(), Operation: runtimeOperationContract("applyDesiredState", false)},
		{Method: "backend.reset", Capability: "backend.reset.structured", Mutating: true, Async: true, Request: "empty", Result: "AcceptedStatus", ErrorCodes: runtimeMutationErrors(), Operation: runtimeOperationContract("reset", true)},
		{Method: "backend.restart", Capability: "runtime.v2", Mutating: true, Async: true, Request: "empty", Result: "AcceptedStatus", ErrorCodes: runtimeMutationErrors(), Operation: runtimeOperationContract("restart", true)},
		{Method: "backend.start", Capability: "runtime.v2", Mutating: true, Async: true, Request: "empty", Result: "AcceptedStatus", ErrorCodes: runtimeMutationErrors(), Operation: runtimeOperationContract("start", true)},
		{Method: "backend.status", Capability: "runtime.v2", Request: "empty", Result: "BackendStatusV2", ErrorCodes: commonReadErrors()},
		{Method: "backend.stop", Capability: "runtime.v2", Mutating: true, Async: true, Request: "empty", Result: "AcceptedStatus", ErrorCodes: runtimeMutationErrors(), Operation: runtimeOperationContract("stop", true)},
		{Method: "config-import", Capability: "config.import.v2", Mutating: true, Async: true, Request: "Config", Result: "ConfigMutationResult", ErrorCodes: configMutationErrors(), Operation: configOperationContract()},
		{Method: "config-list", Capability: "config.schema.v5", Request: "empty", Result: "ConfigKeyTypes", ErrorCodes: commonReadErrors()},
		{Method: "diagnostics.health", Capability: "diagnostics.health.v2", Request: "empty", Result: "HealthSnapshot", ErrorCodes: commonReadErrors()},
		{Method: "diagnostics.testNodes", Capability: "diagnostics.testNodes.v2", Request: "TestNodesRequest", Result: "NodeProbeResult[]", ErrorCodes: commonReadErrors()},
		{Method: "doctor", Capability: "diagnostics.bundle.v2", Request: "DoctorRequest", Result: "DoctorReport", ErrorCodes: commonReadErrors()},
		{Method: "ipc.contract", Capability: "ipc.contract.v1", Request: "empty", Result: "IPCContract", ErrorCodes: commonReadErrors()},
		{Method: "logs", Capability: "runtime.logs", Request: "LogsRequest", Result: "LogSection[]", ErrorCodes: commonReadErrors()},
		{Method: "profile.apply", Capability: "profile.apply.v2", Mutating: true, Async: true, Request: "ProfileApplyRequest|ProfileDocument", Result: "ProfileApplyResult", ErrorCodes: configMutationErrors(), Operation: profileOperationContract()},
		{Method: "profile.get", Capability: "profile.document.v2", Request: "empty", Result: "ProfileDocument", ErrorCodes: commonReadErrors()},
		{Method: "profile.importNodes", Capability: "profile.importNodes.v2", Mutating: true, Async: true, Request: "ImportNodesRequest", Result: "ProfileApplyResult", ErrorCodes: configMutationErrors(), Operation: profileOperationContract()},
		{Method: "profile.setActiveNode", Capability: "profile.apply.v2", Mutating: true, Async: true, Request: "SetActiveNodeRequest", Result: "ProfileApplyResult", ErrorCodes: configMutationErrors(), Operation: profileOperationContract()},
		{Method: "self-check", Capability: "privacy.self-check.v1", Request: "SelfCheckRequest", Result: "SelfCheckReport", ErrorCodes: commonReadErrors()},
		{Method: "subscription.preview", Capability: "profile.subscription.v2", Request: "SubscriptionURLRequest", Result: "SubscriptionPreview", ErrorCodes: append(commonReadErrors(), "CONFIG_ERROR")},
		{Method: "subscription.refresh", Capability: "profile.subscription.v2", Mutating: true, Async: true, Request: "SubscriptionURLRequest", Result: "SubscriptionRefreshResult", ErrorCodes: configMutationErrors(), Operation: profileOperationContract()},
		{Method: "update-check", Capability: "update.install.v1", Request: "empty", Result: "UpdateInfo", ErrorCodes: commonReadErrors()},
		{Method: "update-download", Capability: "update.install.v1", Mutating: true, Request: "UpdateDownloadRequest", Result: "UpdateArtifactSet", ErrorCodes: commonReadErrors(), Operation: artifactOperationContract("update-download")},
		{Method: "update-install", Capability: "update.install.v1", Mutating: true, Async: true, Request: "UpdateInstallRequest", Result: "AcceptedStatus", ErrorCodes: runtimeMutationErrors(), Operation: runtimeOperationContract("update-install", true)},
		{Method: "version", Capability: "ipc.contract.v1", Request: "empty", Result: "VersionInfo", ErrorCodes: commonReadErrors()},
	}
	return contracts
}

func NewContract(controlProtocolVersion int, schemaVersion int, capabilities []string) Contract {
	copiedCapabilities := append([]string(nil), capabilities...)
	sort.Strings(copiedCapabilities)
	return Contract{
		Version:                1,
		ControlProtocolVersion: controlProtocolVersion,
		SchemaVersion:          schemaVersion,
		Capabilities:           copiedCapabilities,
		Methods:                MethodContracts(),
	}
}

func commonReadErrors() []string {
	return []string{"INVALID_PARAMS", "INTERNAL_ERROR"}
}

func runtimeMutationErrors() []string {
	return []string{"INVALID_PARAMS", "RUNTIME_BUSY", "RESET_IN_PROGRESS", "CONFIG_ERROR", "INTERNAL_ERROR"}
}

func configMutationErrors() []string {
	return []string{"INVALID_PARAMS", "RUNTIME_BUSY", "RESET_IN_PROGRESS", "CONFIG_ERROR", "INTERNAL_ERROR"}
}

func runtimeOperationContract(kind string, async bool) *OperationContract {
	resultVia := ""
	if async {
		resultVia = "backend.status.lastOperation"
	}
	return &OperationContract{
		Type:           kind,
		AsyncResultVia: resultVia,
		Stages:         []string{"accepted", "activeOperation", "lastOperation"},
	}
}

func configOperationContract() *OperationContract {
	return &OperationContract{
		Type:           "config-mutation",
		AsyncResultVia: "backend.status.activeOperation|backend.status.lastOperation",
		Stages:         []string{"validate", "render", "persist-draft", "runtime-apply", "verify", "commit-generation", "cleanup"},
	}
}

func profileOperationContract() *OperationContract {
	return &OperationContract{
		Type:           "profile-apply",
		AsyncResultVia: "backend.status.activeOperation|backend.status.lastOperation",
		Stages:         []string{"validate", "render", "persist-draft", "runtime-apply", "verify", "commit-generation", "cleanup"},
	}
}

func artifactOperationContract(kind string) *OperationContract {
	return &OperationContract{
		Type:   kind,
		Stages: []string{"validate", "download", "verify", "persist-artifacts"},
	}
}
