package main

import (
	"encoding/json"
	"path/filepath"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

func (d *daemon) handleIPCContract(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	return ipc.NewContract(controlProtocolVersion, config.CurrentSchemaVersion, supportedCapabilities()), nil
}

func (d *daemon) handleVersion(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	singBoxPath := filepath.Join(d.dataDir, "bin", "sing-box")
	return map[string]interface{}{
		"daemon":                   Version,
		"core":                     Version,
		"privctl":                  Version,
		"module":                   readModuleVersion(),
		"current_release":          doctorReleaseIntegrityReport(d.dataDir),
		"sing_box":                 d.singBoxVersion(singBoxPath, 20),
		"control_protocol":         controlProtocolVersion,
		"control_protocol_version": controlProtocolVersion,
		"schema_version":           config.CurrentSchemaVersion,
		"panel_min_version":        Version,
		"capabilities":             supportedCapabilities(),
		"supported_methods":        supportedRPCMethods(),
		"ipc_contract_version":     1,
	}, nil
}
