package ipc

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
)

//go:embed contract_manifest.json
var contractManifestJSON []byte

var (
	manifestOnce sync.Once
	manifestData Contract
	manifestErr  error
)

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
	APKRequiredMethods     []string         `json:"apkRequiredMethods,omitempty"`
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
	capabilities := append([]string(nil), contractManifest().Capabilities...)
	sort.Strings(capabilities)
	return capabilities
}

func APKRequiredMethods() []string {
	methods := append([]string(nil), contractManifest().APKRequiredMethods...)
	sort.Strings(methods)
	return methods
}

func MethodContracts() []MethodContract {
	return cloneMethodContracts(contractManifest().Methods)
}

func ContractVersion() int {
	return contractManifest().Version
}

func NewContract(controlProtocolVersion int, schemaVersion int, capabilities []string) Contract {
	copiedCapabilities := append([]string(nil), capabilities...)
	sort.Strings(copiedCapabilities)
	return Contract{
		Version:                contractManifest().Version,
		ControlProtocolVersion: controlProtocolVersion,
		SchemaVersion:          schemaVersion,
		Capabilities:           copiedCapabilities,
		APKRequiredMethods:     APKRequiredMethods(),
		Methods:                MethodContracts(),
	}
}

func contractManifest() Contract {
	manifestOnce.Do(func() {
		manifestErr = json.Unmarshal(contractManifestJSON, &manifestData)
		if manifestData.Version == 0 {
			manifestData.Version = 1
		}
	})
	if manifestErr != nil {
		panic(manifestErr)
	}
	return manifestData
}

func cloneMethodContracts(methods []MethodContract) []MethodContract {
	copied := make([]MethodContract, len(methods))
	for i, method := range methods {
		copied[i] = method
		copied[i].ErrorCodes = append([]string(nil), method.ErrorCodes...)
		if method.Operation != nil {
			operation := *method.Operation
			operation.Stages = append([]string(nil), method.Operation.Stages...)
			copied[i].Operation = &operation
		}
	}
	return copied
}
