package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

type recordingRegistrar struct {
	methods []string
}

func (r *recordingRegistrar) Register(method string, handler ipc.Handler) {
	r.methods = append(r.methods, method)
}

func TestRegisterContractHandlersRegistersFullContract(t *testing.T) {
	handlers := contractHandlerMap()
	registrar := &recordingRegistrar{}

	if err := RegisterContractHandlers(registrar, handlers); err != nil {
		t.Fatal(err)
	}

	if len(registrar.methods) != len(ipc.MethodContracts()) {
		t.Fatalf("registered method count drifted: got %d want %d", len(registrar.methods), len(ipc.MethodContracts()))
	}
	seen := map[string]bool{}
	for _, method := range registrar.methods {
		seen[method] = true
	}
	for _, contract := range ipc.MethodContracts() {
		if !seen[contract.Method] {
			t.Fatalf("contract method %s was not registered: %#v", contract.Method, registrar.methods)
		}
	}
}

func TestRegisterContractHandlersRejectsDriftWithoutPartialRegistration(t *testing.T) {
	handlers := contractHandlerMap()
	delete(handlers, ipc.MethodContracts()[0].Method)
	handlers["not.in.contract"] = dummyHandler
	registrar := &recordingRegistrar{}

	err := RegisterContractHandlers(registrar, handlers)
	if err == nil {
		t.Fatal("expected contract drift error")
	}
	if !strings.Contains(err.Error(), "without daemon handlers") || !strings.Contains(err.Error(), "without contract methods") {
		t.Fatalf("expected both missing and extra details, got %v", err)
	}
	if len(registrar.methods) != 0 {
		t.Fatalf("contract drift must not partially register handlers: %#v", registrar.methods)
	}
}

func TestRegisterContractHandlersRejectsNilRegistrar(t *testing.T) {
	if err := RegisterContractHandlers(nil, contractHandlerMap()); err == nil || !strings.Contains(err.Error(), "registrar") {
		t.Fatalf("expected nil registrar error, got %v", err)
	}
}

func contractHandlerMap() map[string]ipc.Handler {
	handlers := make(map[string]ipc.Handler, len(ipc.MethodContracts()))
	for _, contract := range ipc.MethodContracts() {
		handlers[contract.Method] = dummyHandler
	}
	return handlers
}

func dummyHandler(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	return map[string]bool{"ok": true}, nil
}
