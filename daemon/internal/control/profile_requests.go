package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
)

type ProfileApplyRequest struct {
	Profile profiledoc.Document
	Reload  bool
}

type ImportNodesRequest struct {
	Nodes  []profiledoc.Node
	Reload bool
}

type SetActiveNodeRequest struct {
	NodeID string
	Reload bool
}

type SubscriptionURLRequest struct {
	URL string
}

func DecodeProfileApplyParams(params *json.RawMessage) (ProfileApplyRequest, error) {
	var result ProfileApplyRequest
	result.Reload = true
	if params == nil {
		return result, fmt.Errorf("params required: profile document")
	}
	var p struct {
		Profile *profiledoc.Document `json:"profile"`
		Reload  *bool                `json:"reload"`
	}
	if hasJSONField(*params, "profile") {
		decoder := json.NewDecoder(bytes.NewReader(*params))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			return result, fmt.Errorf("invalid profile.apply params: %w", err)
		}
	} else {
		doc, err := profiledoc.DecodeStrictDocument(*params)
		if err != nil {
			return result, fmt.Errorf("invalid profile: %w", err)
		}
		p.Profile = &doc
	}
	if p.Profile == nil {
		return result, fmt.Errorf("profile is required")
	}
	if p.Reload != nil {
		result.Reload = *p.Reload
	}
	result.Profile = *p.Profile
	return result, nil
}

func DecodeImportNodesParams(params *json.RawMessage, now time.Time) (ImportNodesRequest, error) {
	var result ImportNodesRequest
	result.Reload = true
	if params == nil {
		return result, fmt.Errorf("params required: {\"nodes\": [...]}")
	}
	var p struct {
		Nodes  []profiledoc.Node `json:"nodes"`
		Reload *bool             `json:"reload"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return result, fmt.Errorf("invalid params: %w", err)
	}
	if len(p.Nodes) == 0 {
		return result, fmt.Errorf("nodes must not be empty")
	}
	if p.Reload != nil {
		result.Reload = *p.Reload
	}
	if now.IsZero() {
		now = time.Now()
	}
	createdAt := now.UnixMilli()
	for i := range p.Nodes {
		p.Nodes[i].Stale = false
		p.Nodes[i].Source = profiledoc.NodeSource{Type: "MANUAL"}
		if p.Nodes[i].CreatedAt == 0 {
			p.Nodes[i].CreatedAt = createdAt
		}
	}
	result.Nodes = p.Nodes
	return result, nil
}

func DecodeSetActiveNodeParams(params *json.RawMessage) (SetActiveNodeRequest, error) {
	var result SetActiveNodeRequest
	result.Reload = true
	if params == nil {
		return result, fmt.Errorf("params required: {\"nodeId\": \"...\"}")
	}
	var p struct {
		NodeID string `json:"nodeId"`
		Reload *bool  `json:"reload"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return result, fmt.Errorf("invalid params: %w", err)
	}
	if p.NodeID == "" {
		return result, fmt.Errorf("nodeId is required")
	}
	if p.Reload != nil {
		result.Reload = *p.Reload
	}
	result.NodeID = p.NodeID
	return result, nil
}

func DecodeSubscriptionURLParams(params *json.RawMessage) (SubscriptionURLRequest, error) {
	var result SubscriptionURLRequest
	if params == nil {
		return result, fmt.Errorf("params required: {\"url\": \"https://...\"}")
	}
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return result, fmt.Errorf("invalid params: %w", err)
	}
	result.URL = p.URL
	return result, nil
}

func hasJSONField(data []byte, field string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw[field]
	return ok
}
