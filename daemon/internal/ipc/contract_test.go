package ipc

import (
	"slices"
	"testing"
)

func TestSupportedMethodsComeFromContract(t *testing.T) {
	methods := SupportedMethods()
	for _, method := range []string{"backend.status", "config-import", "ipc.contract", "profile.apply", "subscription.refresh", "update-install"} {
		if !slices.Contains(methods, method) {
			t.Fatalf("supported methods missing %s: %#v", method, methods)
		}
	}
	for _, legacy := range []string{"config.import", "network.reset", "subscription-fetch", "status", "start", "stop"} {
		if slices.Contains(methods, legacy) {
			t.Fatalf("supported methods must not advertise legacy alias %s: %#v", legacy, methods)
		}
	}
}

func TestMutatingContractsExposeOperationSurface(t *testing.T) {
	for _, contract := range MethodContracts() {
		if !contract.Mutating {
			continue
		}
		if contract.Operation == nil {
			t.Fatalf("mutating method %s lacks operation contract", contract.Method)
		}
		if contract.Async && contract.Operation.AsyncResultVia == "" {
			t.Fatalf("async method %s lacks async result surface", contract.Method)
		}
	}
}

func TestNewContractSortsCapabilities(t *testing.T) {
	contract := NewContract(5, 5, []string{"z.cap", "a.cap"})
	if contract.Version != 1 || contract.ControlProtocolVersion != 5 || contract.SchemaVersion != 5 {
		t.Fatalf("unexpected contract metadata: %#v", contract)
	}
	if got := contract.Capabilities; len(got) != 2 || got[0] != "a.cap" || got[1] != "z.cap" {
		t.Fatalf("capabilities must be sorted in contract output: %#v", got)
	}
}
