package main

import (
	"encoding/json"
	"path/filepath"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

func (d *daemon) handleIPCContract(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	return ipc.NewContract(controlProtocolVersion, config.CurrentSchemaVersion, ipc.SupportedCapabilities()), nil
}

func (d *daemon) handleVersion(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	singBoxPath := filepath.Join(d.dataDir, "bin", "sing-box")
	return map[string]interface{}{
		"daemon":                   Version,
		"core":                     Version,
		"daemonctl":                Version,
		"module":                   diagnostics.ReadModuleVersion(),
		"current_release":          diagnostics.ReleaseIntegrityReport(d.dataDir),
		"sing_box":                 diagnostics.SingBoxVersion(singBoxPath, 20, core.ExecCommand),
		"control_protocol":         controlProtocolVersion,
		"control_protocol_version": controlProtocolVersion,
		"schema_version":           config.CurrentSchemaVersion,
		"panel_min_version":        Version,
		"capabilities":             ipc.SupportedCapabilities(),
		"supported_methods":        ipc.SupportedMethods(),
		"ipc_contract_version":     ipc.ContractVersion(),
	}, nil
}
