package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	applytx "github.com/youtubediscord/RKNnoVPN/daemon/internal/apply"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/subscription"
)

func (d *daemon) handleProfileGet(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	return profiledoc.FromConfig(cfg), nil
}

func (d *daemon) handleProfileApply(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "params required: profile document"}
	}
	var p struct {
		Profile *profiledoc.Document `json:"profile"`
		Reload  *bool                `json:"reload"`
	}
	if hasJSONField(*params, "profile") {
		decoder := json.NewDecoder(bytes.NewReader(*params))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid profile.apply params: " + err.Error()}
		}
	} else {
		doc, err := profiledoc.DecodeStrictDocument(*params)
		if err != nil {
			return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid profile: " + err.Error()}
		}
		p.Profile = &doc
	}
	if p.Profile == nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "profile is required"}
	}
	reload := true
	if p.Reload != nil {
		reload = *p.Reload
	}
	return d.applyProfileDocument(*p.Profile, reload, "profile.apply", -1)
}

func hasJSONField(data []byte, field string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw[field]
	return ok
}

func (d *daemon) handleProfileImportNodes(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "params required: {\"nodes\": [...]}"}
	}
	var p struct {
		Nodes  []profiledoc.Node `json:"nodes"`
		Reload *bool             `json:"reload"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if len(p.Nodes) == 0 {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "nodes must not be empty"}
	}
	now := time.Now().UnixMilli()
	for i := range p.Nodes {
		p.Nodes[i].Stale = false
		p.Nodes[i].Source = profiledoc.NodeSource{Type: "MANUAL"}
		if p.Nodes[i].CreatedAt == 0 {
			p.Nodes[i].CreatedAt = now
		}
	}
	reload := true
	if p.Reload != nil {
		reload = *p.Reload
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	next, stats := profiledoc.MergeNodes(current, p.Nodes, false)
	result, rpcErr := d.applyProfileDocument(next, reload, "profile.importNodes", len(p.Nodes))
	if rpcErr != nil {
		return nil, rpcErr
	}
	if obj, ok := result.(map[string]interface{}); ok {
		obj["imported"] = len(p.Nodes)
		obj["merge"] = stats
	}
	return result, nil
}

func (d *daemon) handleProfileSetActiveNode(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "params required: {\"nodeId\": \"...\"}"}
	}
	var p struct {
		NodeID string `json:"nodeId"`
		Reload *bool  `json:"reload"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	if p.NodeID == "" {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "nodeId is required"}
	}
	reload := true
	if p.Reload != nil {
		reload = *p.Reload
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	found := false
	for _, node := range current.Nodes {
		if node.ID == p.NodeID && !node.Stale {
			found = true
			break
		}
	}
	if !found {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "active node is missing or stale"}
	}
	current.ActiveNodeID = p.NodeID
	return d.applyProfileDocument(current, reload, "profile.setActiveNode", 1)
}

func (d *daemon) handleSubscriptionPreview(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	rawURL, rpcErr := subscriptionURLFromParams(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	preview, err := subscription.NewClient(nil).Preview(rawURL, current)
	if err != nil {
		return nil, subscriptionRPCError(rawURL, preview.FetchStatus, preview.FetchHeaders, err)
	}
	return map[string]interface{}{
		"source":        preview.Source,
		"subscription":  preview.Subscription,
		"nodes":         preview.Nodes,
		"added":         preview.Added,
		"updated":       preview.Updated,
		"unchanged":     preview.Unchanged,
		"stale":         preview.Stale,
		"parseFailures": preview.ParseFailures,
	}, nil
}

func (d *daemon) handleSubscriptionRefresh(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	rawURL, rpcErr := subscriptionURLFromParams(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	refresh, err := subscription.NewClient(nil).ApplyRefresh(rawURL, current)
	if err != nil {
		return nil, subscriptionRPCError(rawURL, refresh.FetchStatus, refresh.FetchHeaders, err)
	}
	result, applyErr := d.applyProfileDocument(refresh.Profile, true, "subscription.refresh", len(refresh.Nodes))
	if applyErr != nil {
		return nil, applyErr
	}
	if obj, ok := result.(map[string]interface{}); ok {
		obj["source"] = refresh.Source
		obj["subscription"] = refresh.Subscription
		obj["imported"] = len(refresh.Nodes)
		obj["parseFailures"] = refresh.ParseFailures
		obj["merge"] = refresh.Merge
	}
	return result, nil
}

func subscriptionURLFromParams(params *json.RawMessage) (string, *ipc.RPCError) {
	if params == nil {
		return "", &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "params required: {\"url\": \"https://...\"}"}
	}
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return "", &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	return p.URL, nil
}

func subscriptionRPCError(rawURL string, status int, headers map[string]string, err error) *ipc.RPCError {
	code := ipc.CodeInternalError
	if subscription.ValidateFetchURL(rawURL) != nil || rawURL == "" {
		code = ipc.CodeInvalidParams
	}
	if errors.Is(err, subscription.ErrNoSupportedNodes) {
		code = ipc.CodeConfigError
	}
	return &ipc.RPCError{
		Code:    code,
		Message: err.Error(),
		Data: map[string]interface{}{
			"status":  status,
			"headers": headers,
		},
	}
}

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
		rpcErr := d.configApplyRPCError(action, err)
		status := d.runtimeV2.Status()
		resultStatus := "failed"
		runtimeApply := "not_started"
		if mutation.ConfigSaved {
			resultStatus = "saved_not_applied"
			runtimeApply = "failed"
		}
		rpcErr.Data = profileOperation(action, resultStatus, mutation.ConfigSaved, false, runtimeApply, desiredGeneration(status, before), status.AppliedState.Generation, runtimeErrorCode(err, "PROFILE_APPLY_FAILED"), err.Error(), resetReportFromRuntimeError(err), warnings, updated)
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
