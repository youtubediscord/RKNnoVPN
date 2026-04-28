package main

import (
	"encoding/json"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/control"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/subscription"
)

func (d *daemon) handleProfileGet(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	return profiledoc.FromConfig(cfg), nil
}

func (d *daemon) handleProfileApply(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	request, err := control.DecodeProfileApplyParams(params)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: err.Error()}
	}
	return d.applyProfileDocument(request.Profile, request.Reload, "profile.apply", -1)
}

func (d *daemon) handleProfileImportNodes(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	request, err := control.DecodeImportNodesParams(params, time.Now())
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: err.Error()}
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	next, stats := profiledoc.ImportNodes(current, request.Nodes)
	result, rpcErr := d.applyProfileDocument(next, request.Reload, "profile.importNodes", len(request.Nodes))
	if rpcErr != nil {
		return nil, rpcErr
	}
	if obj, ok := result.(map[string]interface{}); ok {
		obj["imported"] = len(request.Nodes)
		obj["merge"] = stats
	}
	return result, nil
}

func (d *daemon) handleProfileSetActiveNode(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	request, err := control.DecodeSetActiveNodeParams(params)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: err.Error()}
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	next, err := profiledoc.SetActiveNode(current, request.NodeID)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: err.Error()}
	}
	return d.applyProfileDocument(next, request.Reload, "profile.setActiveNode", 1)
}

func (d *daemon) handleSubscriptionPreview(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	request, err := control.DecodeSubscriptionURLParams(params)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: err.Error()}
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	preview, err := subscription.NewClient(nil).Preview(request.URL, current)
	if err != nil {
		return nil, subscriptionRPCError(request.URL, preview.FetchStatus, preview.FetchHeaders, nil, err)
	}
	return preview, nil
}

func (d *daemon) handleSubscriptionRefresh(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	request, err := control.DecodeSubscriptionURLParams(params)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInvalidParams, Message: err.Error()}
	}
	d.mu.Lock()
	current := profiledoc.FromConfig(d.cfg)
	d.mu.Unlock()
	refresh, err := subscription.NewClient(nil).ApplyRefresh(request.URL, current)
	if err != nil {
		return nil, subscriptionRPCError(request.URL, refresh.FetchStatus, refresh.FetchHeaders, &refresh, err)
	}
	result, applyErr := d.applyProfileDocument(refresh.Profile, true, "subscription.refresh", len(refresh.Nodes))
	if applyErr != nil {
		return nil, applyErr
	}
	if obj, ok := result.(map[string]interface{}); ok {
		response := refresh.Response()
		obj["source"] = response.Source
		obj["subscription"] = response.Subscription
		obj["imported"] = response.Imported
		obj["parseFailures"] = response.ParseFailures
		obj["rejected"] = response.Rejected
		obj["rejectedNodes"] = response.RejectedNodes
		obj["merge"] = response.Merge
	}
	return result, nil
}

func subscriptionRPCError(rawURL string, status int, headers map[string]string, refresh *subscription.RefreshResult, err error) *ipc.RPCError {
	code := ipc.CodeInternalError
	switch subscription.ClassifyError(rawURL, err) {
	case subscription.ErrorInvalidParams:
		code = ipc.CodeInvalidParams
	case subscription.ErrorConfig:
		code = ipc.CodeConfigError
	}
	data := map[string]interface{}{
		"status":  status,
		"headers": headers,
	}
	if refresh != nil {
		data["parseFailures"] = refresh.ParseFailures
		data["rejected"] = len(refresh.RejectedNodes)
		data["rejectedNodes"] = refresh.RejectedNodes
		data["subscription"] = refresh.Subscription
		data["source"] = refresh.Source
	}
	return &ipc.RPCError{
		Code:    code,
		Message: err.Error(),
		Data:    data,
	}
}
