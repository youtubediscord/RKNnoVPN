package main

import (
	"encoding/json"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
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
	if err := json.Unmarshal(*params, &p); err != nil {
		var doc profiledoc.Document
		if docErr := json.Unmarshal(*params, &doc); docErr != nil {
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
	nodes, sub, failures, rpcErr := d.fetchAndParseSubscription(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	_, stats := profiledoc.MergeNodes(current, nodes, true)
	return map[string]interface{}{
		"subscription":  sub,
		"nodes":         nodes,
		"added":         stats["added"],
		"updated":       stats["updated"],
		"unchanged":     stats["unchanged"],
		"stale":         stats["stale"],
		"parseFailures": failures,
	}, nil
}

func (d *daemon) handleSubscriptionRefresh(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	nodes, sub, failures, rpcErr := d.fetchAndParseSubscription(params)
	if rpcErr != nil {
		return nil, rpcErr
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	next, stats := profiledoc.MergeNodes(current, nodes, true)
	replaced := false
	for i, existing := range next.Subscriptions {
		if existing.ProviderKey == sub.ProviderKey {
			sub.Name = existing.Name
			next.Subscriptions[i] = sub
			replaced = true
			break
		}
	}
	if !replaced {
		next.Subscriptions = append(next.Subscriptions, sub)
	}
	result, applyErr := d.applyProfileDocument(next, true, "subscription.refresh", len(nodes))
	if applyErr != nil {
		return nil, applyErr
	}
	if obj, ok := result.(map[string]interface{}); ok {
		obj["subscription"] = sub
		obj["imported"] = len(nodes)
		obj["parseFailures"] = failures
		obj["merge"] = stats
	}
	return result, nil
}

func (d *daemon) fetchAndParseSubscription(params *json.RawMessage) ([]profiledoc.Node, profiledoc.Subscription, int, *ipc.RPCError) {
	if params == nil {
		return nil, profiledoc.Subscription{}, 0, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "params required: {\"url\": \"https://...\"}"}
	}
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, profiledoc.Subscription{}, 0, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	fetched, err := fetchSubscriptionURL(p.URL)
	if err != nil {
		code := ipc.CodeInternalError
		if validateSubscriptionFetchURL(p.URL) != nil || p.URL == "" {
			code = ipc.CodeInvalidParams
		}
		return nil, profiledoc.Subscription{}, 0, &ipc.RPCError{
			Code:    code,
			Message: err.Error(),
			Data: map[string]interface{}{
				"status":  fetched.Status,
				"headers": fetched.Headers,
			},
		}
	}
	nodes, sub, failures := profiledoc.ParseSubscription(fetched.Body, fetched.Headers, p.URL, time.Now().UnixMilli())
	if len(nodes) == 0 {
		return nil, sub, failures, &ipc.RPCError{Code: ipc.CodeConfigError, Message: "subscription contains no supported nodes"}
	}
	return nodes, sub, failures, nil
}

func (d *daemon) applyProfileDocument(doc profiledoc.Document, reload bool, action string, updated int) (interface{}, *ipc.RPCError) {
	runtimeWasRunning := d.runtimeIsRunning()
	before := d.runtimeV2.Status()

	d.mu.Lock()
	base := d.cfg
	oldPanel := d.cfg.Panel
	d.mu.Unlock()
	nextCfg, warnings, err := profiledoc.ApplyToConfig(base, doc)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "profile validation failed: " + err.Error(),
			Data:    profileOperation(action, "failed", false, false, "not_started", before.AppliedState.Generation+1, before.AppliedState.Generation, "validation_failed", err.Error(), nil, warnings, updated),
		}
	}

	if err := config.SavePanel(d.panelPath, nextCfg.Panel); err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: "persist profile: " + err.Error()}
	}
	if err := d.applyConfig(nextCfg, reload); err != nil {
		_ = config.SavePanel(d.panelPath, oldPanel)
		rpcErr := d.configApplyRPCError(action, err)
		status := d.runtimeV2.Status()
		rpcErr.Data = profileOperation(action, "saved_not_applied", true, false, "failed", desiredGeneration(status, before), status.AppliedState.Generation, runtimeErrorCode(err, "PROFILE_APPLY_FAILED"), err.Error(), resetReportFromRuntimeError(err), warnings, updated)
		return nil, rpcErr
	}

	status := d.runtimeV2.Status()
	runtimeApply := configRuntimeApplyStatus(reload, runtimeWasRunning)
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
	rollback := "not_needed"
	if resetReport != nil {
		rollback = "cleanup_incomplete"
		if resetReport.Status == "ok" {
			rollback = "cleanup_succeeded"
		}
	} else if configSaved && status == "saved_not_applied" {
		rollback = "not_needed"
	}
	stages := []map[string]interface{}{
		{"name": "validate", "status": "ok"},
		{"name": "render", "status": "ok"},
		{"name": "persist-desired", "status": stageStatus(configSaved, status == "failed")},
		{"name": "runtime-apply", "status": runtimeApply},
	}
	if resetReport != nil {
		stages = append(stages, map[string]interface{}{"name": "cleanup", "status": resetReport.Status})
	}
	warningItems := make([]map[string]string, 0, len(warnings))
	for _, warning := range warnings {
		warningItems = append(warningItems, map[string]string{
			"code":    warning.Code,
			"message": warning.Message,
		})
	}
	result := map[string]interface{}{
		"status":            status,
		"configSaved":       configSaved,
		"config_saved":      configSaved,
		"runtimeApplied":    runtimeApplied,
		"runtime_applied":   runtimeApplied,
		"runtimeApply":      runtimeApply,
		"runtime_apply":     runtimeApply,
		"desiredGeneration": desiredGeneration,
		"appliedGeneration": appliedGeneration,
		"rollback":          rollback,
		"stages":            stages,
		"warnings":          warningItems,
		"operation": map[string]interface{}{
			"type":              "profile-apply",
			"action":            action,
			"status":            status,
			"configSaved":       configSaved,
			"runtimeApplied":    runtimeApplied,
			"runtimeApply":      runtimeApply,
			"desiredGeneration": desiredGeneration,
			"appliedGeneration": appliedGeneration,
			"rollback":          rollback,
			"stages":            stages,
			"warnings":          warningItems,
		},
	}
	if updated >= 0 {
		result["updated"] = updated
		result["operation"].(map[string]interface{})["updated"] = updated
	}
	if code != "" {
		result["code"] = code
		result["operation"].(map[string]interface{})["code"] = code
	}
	if message != "" {
		result["message"] = message
		result["operation"].(map[string]interface{})["message"] = message
	}
	if resetReport != nil {
		result["resetReport"] = resetReport
	}
	return result
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
