package control

import (
	"errors"
	"sort"
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

type Registrar interface {
	Register(method string, handler ipc.Handler)
}

func RegisterContractHandlers(registrar Registrar, handlers map[string]ipc.Handler) error {
	if registrar == nil {
		return errors.New("ipc registrar is not configured")
	}
	contractMethods := make(map[string]bool, len(handlers))
	var missing []string
	for _, contract := range ipc.MethodContracts() {
		contractMethods[contract.Method] = true
		if _, ok := handlers[contract.Method]; !ok {
			missing = append(missing, contract.Method)
		}
	}
	var extra []string
	for method := range handlers {
		if !contractMethods[method] {
			extra = append(extra, method)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		parts := make([]string, 0, 2)
		if len(missing) > 0 {
			parts = append(parts, "contract method(s) without daemon handlers: "+strings.Join(missing, ", "))
		}
		if len(extra) > 0 {
			parts = append(parts, "daemon handler(s) without contract methods: "+strings.Join(extra, ", "))
		}
		return errors.New(strings.Join(parts, "; "))
	}
	for _, contract := range ipc.MethodContracts() {
		registrar.Register(contract.Method, handlers[contract.Method])
	}
	return nil
}
