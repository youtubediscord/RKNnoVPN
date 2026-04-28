package main

import (
	applytx "github.com/youtubediscord/RKNnoVPN/daemon/internal/apply"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func (d *daemon) applyProfileDocument(doc profiledoc.Document, reload bool, action string, updated int) (interface{}, *ipc.RPCError) {
	before := d.runtimeV2.Status()

	d.mu.Lock()
	base := d.cfg
	d.mu.Unlock()
	nextCfg, warnings, err := profiledoc.ApplyToConfig(base, doc)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "profile validation failed: " + err.Error(),
			Data:    profileOperation(action, "failed", false, false, "not_started", before.AppliedState.Generation+1, before.AppliedState.Generation, "validation_failed", err.Error(), nil, warnings, updated),
		}
	}

	mutation, err := d.persistProfileConfigMutationForAction(nextCfg, reload, action)
	if err != nil {
		rpcErr := d.configApplyRPCErrorSaved(action, err, mutation.ConfigSaved)
		status := d.runtimeV2.Status()
		resultStatus := "failed"
		runtimeApply := "not_started"
		if mutation.ConfigSaved {
			resultStatus = "saved_not_applied"
			runtimeApply = "failed"
		}
		rpcErr.Data = profileOperation(action, resultStatus, mutation.ConfigSaved, false, runtimeApply, desiredGeneration(status, before), status.AppliedState.Generation, rootruntime.RuntimeErrorCode(err, "PROFILE_APPLY_FAILED"), err.Error(), rootruntime.ResetReportFromError(err), warnings, updated)
		return nil, rpcErr
	}

	status := d.runtimeV2.Status()
	runtimeApply := applytx.RuntimeApplyStatus(reload, mutation.RuntimeWasRunning)
	runtimeApplied := runtimeApply == "applied"
	if runtimeApply == "accepted" {
		runtimeApplied = false
	}
	resultStatus := "ok"
	if runtimeApply == "skipped_runtime_stopped" {
		resultStatus = "saved"
	}
	result := profileOperation(action, resultStatus, true, runtimeApplied, runtimeApply, desiredGeneration(status, before), status.AppliedState.Generation, "", "", nil, warnings, updated)
	result["ok"] = true
	result["profile"] = profiledoc.FromConfig(nextCfg)
	result["runtimeStatus"] = status
	return result, nil
}

func profileOperation(
	action string,
	status string,
	configSaved bool,
	runtimeApplied bool,
	runtimeApply string,
	desiredGeneration int64,
	appliedGeneration int64,
	code string,
	message string,
	resetReport *runtimev2.ResetReport,
	warnings []profiledoc.Warning,
	updated int,
) map[string]interface{} {
	warningItems := make([]applytx.Warning, 0, len(warnings))
	for _, warning := range warnings {
		warningItems = append(warningItems, applytx.Warning{
			Code:    warning.Code,
			Message: warning.Message,
		})
	}
	return applytx.ProfileOperation(action, status, configSaved, runtimeApplied, runtimeApply, desiredGeneration, appliedGeneration, code, message, resetReport, warningItems, updated)
}

func desiredGeneration(status runtimev2.Status, before runtimev2.Status) int64 {
	if status.ActiveOperation != nil {
		return status.ActiveOperation.Generation
	}
	if status.AppliedState.Generation > before.AppliedState.Generation {
		return status.AppliedState.Generation
	}
	return before.AppliedState.Generation + 1
}
