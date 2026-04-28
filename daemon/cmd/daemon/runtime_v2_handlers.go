package main

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/updater"
)

func (d *daemon) handleBackendStatus(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if d.runtimeV2 == nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: "v2 runtime is not initialized"}
	}
	d.refreshRuntimeV2Compatibility()
	status := d.runtimeV2.RefreshActiveProgress()
	if status.ActiveOperation != nil {
		return d.statusWithUpdateInstallState(status), nil
	}
	state := d.coreMgr.GetState()
	if state == core.StateRunning || state == core.StateDegraded {
		healthSnapshot := d.runtimeV2.CurrentHealth()
		if healthSnapshot.CheckedAt.IsZero() || time.Since(healthSnapshot.CheckedAt) > 10*time.Second {
			go d.runtimeV2.RefreshHealth()
		}
	}
	return d.statusWithUpdateInstallState(d.runtimeV2.Status()), nil
}

func (d *daemon) statusWithUpdateInstallState(status runtimev2.Status) runtimev2.Status {
	state, err := updater.ReadInstallState(d.dataDir)
	if err == nil {
		status.UpdateInstall = &runtimev2.UpdateInstallState{
			Status:          state.Status,
			Generation:      state.Generation,
			Step:            state.Step,
			StepStatus:      state.StepStatus,
			Code:            state.Code,
			Detail:          state.Detail,
			ModulePath:      state.ModulePath,
			ApkPath:         state.ApkPath,
			ApkInstalled:    state.ApkInstalled,
			ModuleInstalled: state.ModuleInstalled,
			StartedAt:       state.StartedAt,
			UpdatedAt:       state.UpdatedAt,
		}
		return status
	}
	if os.IsNotExist(err) {
		return status
	}
	status.UpdateInstall = &runtimev2.UpdateInstallState{
		Status: "unknown",
		Code:   "UPDATE_INSTALL_STATE_INVALID",
		Detail: err.Error(),
	}
	return status
}

func (d *daemon) handleBackendApplyDesiredState(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: desired state object",
		}
	}
	var desired runtimev2.DesiredState
	if err := json.Unmarshal(*params, &desired); err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	status, err := d.applyDesiredStateV2(desired)
	if err != nil {
		return nil, d.rpcErrorFromDesiredStateApplyError(err)
	}
	return status, nil
}

func (d *daemon) rpcErrorFromDesiredStateApplyError(err error) *ipc.RPCError {
	var applyErr desiredStateApplyError
	if !errors.As(err, &applyErr) {
		return d.rpcErrorFromRuntimeError(err)
	}
	if rpcErr := d.rpcErrorFromRuntimeError(applyErr.err); rpcErr.Code == ipc.CodeRuntimeBusy {
		return rpcErr
	}
	if applyErr.stage == desiredStateApplyStageRuntime {
		return &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: applyErr.err.Error()}
	}
	return &ipc.RPCError{Code: ipc.CodeInternalError, Message: applyErr.err.Error()}
}

func (d *daemon) handleBackendStart(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if err := d.syncRuntimeV2DesiredState(); err != nil {
		return nil, d.rpcErrorFromRuntimeError(err)
	}
	status, err := d.runtimeV2.Start()
	if err != nil {
		return nil, d.rpcErrorFromRuntimeError(err)
	}
	return status, nil
}

func (d *daemon) handleBackendStop(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	status, err := d.runtimeV2.Stop()
	if err != nil {
		return nil, d.rpcErrorFromRuntimeError(err)
	}
	return status, nil
}

func (d *daemon) handleBackendRestart(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if err := d.syncRuntimeV2DesiredState(); err != nil {
		return nil, d.rpcErrorFromRuntimeError(err)
	}
	status, err := d.runtimeV2.Restart()
	if err != nil {
		return nil, d.rpcErrorFromRuntimeError(err)
	}
	return status, nil
}

func (d *daemon) handleBackendReset(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	status, err := d.runtimeV2.Reset()
	if err != nil {
		return nil, d.rpcErrorFromRuntimeError(err)
	}
	return status, nil
}

func (d *daemon) handleDiagnosticsHealth(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	return d.runtimeV2.RefreshHealth(), nil
}

func (d *daemon) handleDiagnosticsTestNodes(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	var p struct {
		NodeIDs   []string `json:"node_ids"`
		URL       string   `json:"url"`
		TimeoutMS int      `json:"timeout_ms"`
	}
	if params != nil {
		if err := json.Unmarshal(*params, &p); err != nil {
			return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
		}
	}
	if p.TimeoutMS <= 0 {
		p.TimeoutMS = 5000
	}

	results, err := d.runtimeV2.TestNodes(p.URL, p.TimeoutMS, p.NodeIDs)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}
	return map[string]interface{}{
		"url":     p.URL,
		"results": results,
	}, nil
}
