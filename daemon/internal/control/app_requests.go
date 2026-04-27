package control

import (
	"encoding/json"
	"fmt"
)

type ResolveUIDRequest struct {
	UID int
}

func DecodeResolveUIDParams(params *json.RawMessage) (ResolveUIDRequest, error) {
	var result ResolveUIDRequest
	if params == nil {
		return result, fmt.Errorf("params required: {\"uid\": 12345}")
	}

	var p struct {
		UID int `json:"uid"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return result, fmt.Errorf("invalid params: %w", err)
	}
	if p.UID <= 0 {
		return result, fmt.Errorf("uid must be > 0")
	}

	result.UID = p.UID
	return result, nil
}
