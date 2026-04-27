package control

import (
	"fmt"
	"sort"
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

type Registrar interface {
	Register(method string, handler ipc.Handler)
}

func RegisterContractHandlers(registrar Registrar, handlers map[string]ipc.Handler) error {
	contractMethods := make(map[string]bool, len(handlers))
	var missing []string
	for _, contract := range ipc.MethodContracts() {
		contractMethods[contract.Method] = true
		handler, ok := handlers[contract.Method]
		if !ok {
			missing = append(missing, contract.Method)
			continue
		}
		registrar.Register(contract.Method, handler)
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
		return fmt.Errorf("%s", strings.Join(parts, "; "))
	}
	return nil
}
