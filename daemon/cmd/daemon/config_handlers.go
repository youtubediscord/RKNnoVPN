package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	applytx "github.com/youtubediscord/RKNnoVPN/daemon/internal/apply"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/control"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) handleConfigList(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	d.mu.Lock()
	data, err := json.Marshal(d.cfg)
	d.mu.Unlock()
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}

	var full map[string]interface{}
	json.Unmarshal(data, &full)

	keys := make(map[string]string)
	for k, v := range full {
		keys[k] = fmt.Sprintf("%T", v)
	}
	return keys, nil
}

func (d *daemon) handleConfigImport(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	d.mu.Lock()
	currentProfile := d.cfg.Profile
	d.mu.Unlock()
	newCfg, err := control.DecodeConfigImportParams(params, currentProfile)
	if err != nil {
		return nil, configImportRPCError(err)
	}

	if err := newCfg.Validate(); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "validation failed: " + err.Error(),
		}
	}
	if profile := newCfg.ResolveProfile(); profile != nil && profile.Address != "" {
		if _, err := config.RenderSingboxConfig(newCfg, profile); err != nil {
			return nil, &ipc.RPCError{
				Code:    ipc.CodeConfigError,
				Message: "render validation failed: " + err.Error(),
			}
		}
	}

	mutation, err := d.persistProfileConfigMutationForAction(newCfg, true, "config-import")
	if err != nil {
		return nil, d.configApplyRPCErrorSaved("config-import", err, mutation.ConfigSaved)
	}

	return d.configMutationSuccess("config-import", "imported", true, mutation.RuntimeWasRunning, -1), nil
}

func configImportRPCError(err error) *ipc.RPCError {
	code := ipc.CodeInvalidParams
	var requestErr control.ConfigImportError
	if errors.As(err, &requestErr) && requestErr.Kind == control.ConfigImportInvalidConfig {
		code = ipc.CodeConfigError
	}
	return &ipc.RPCError{Code: code, Message: err.Error()}
}

func (d *daemon) configApplyRPCErrorSaved(action string, err error, saved bool) *ipc.RPCError {
	var busy *runtimev2.OperationBusyError
	if errors.As(err, &busy) {
		rpcErr := d.rpcErrorFromRuntimeError(err)
		rpcErr.Data = d.configMutationErrorData(action, err, saved)
		rpcErr.Data.(map[string]interface{})["busy"] = busy.Data()
		return rpcErr
	}
	rpcErr := &ipc.RPCError{
		Code:    ipc.CodeInternalError,
		Message: err.Error(),
	}
	rpcErr.Data = d.configMutationErrorData(action, err, saved)
	return rpcErr
}

func (d *daemon) runtimeIsRunning() bool {
	state := d.coreMgr.GetState()
	return state == core.StateRunning || state == core.StateDegraded
}

func (d *daemon) configMutationSuccess(action string, status string, reload bool, runtimeWasRunning bool, updated int) map[string]interface{} {
	result := applytx.MutationSuccess(action, status, reload, runtimeWasRunning, updated)
	if d.runtimeV2 != nil {
		status := d.runtimeV2.Status()
		operation, _ := result["operation"].(map[string]interface{})
		attachMutationGenerations(result, operation, status)
		result["runtimeStatus"] = status
	}
	return result
}

func (d *daemon) configMutationErrorData(action string, err error, saved bool) map[string]interface{} {
	code := runtimeErrorCode(err, "CONFIG_APPLY_FAILED")
	resetReport := resetReportFromRuntimeError(err)
	data := applytx.MutationErrorData(action, saved, code, err.Error(), resetReport)
	if d.runtimeV2 != nil {
		status := d.runtimeV2.Status()
		operation, _ := data["operation"].(map[string]interface{})
		attachMutationGenerations(data, operation, status)
		data["runtimeStatus"] = status
	}
	return data
}

func attachMutationGenerations(result map[string]interface{}, operation map[string]interface{}, status runtimev2.Status) {
	appliedGeneration := status.AppliedState.Generation
	desiredGeneration := appliedGeneration
	if status.ActiveOperation != nil {
		desiredGeneration = status.ActiveOperation.Generation
	} else if result["config_saved"] == true || result["configSaved"] == true {
		desiredGeneration = appliedGeneration + 1
	}
	result["desiredGeneration"] = desiredGeneration
	result["appliedGeneration"] = appliedGeneration
	if operation != nil {
		operation["desiredGeneration"] = desiredGeneration
		operation["appliedGeneration"] = appliedGeneration
	}
}

func configMutationWasSaved(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "config saved")
}

func (d *daemon) failIfRuntimeOperationActive() error {
	if d.runtimeV2 == nil {
		return nil
	}
	status := d.runtimeV2.Status()
	if status.ActiveOperation == nil {
		return nil
	}
	return runtimev2.NewRuntimeBusyError(*status.ActiveOperation)
}
