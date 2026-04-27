package control

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func TestDecodeConfigImportParamsPreservesCurrentProfile(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.TProxyPort = 17001
	raw := mustJSON(t, cfg)
	currentProfile := config.ProfileProjectionConfig{
		ID:           "user-profile",
		ActiveNodeID: "node-1",
	}

	decoded, err := DecodeConfigImportParams(&raw, currentProfile)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Proxy.TProxyPort != 17001 {
		t.Fatalf("expected imported proxy port, got %d", decoded.Proxy.TProxyPort)
	}
	if decoded.Profile.ID != currentProfile.ID || decoded.Profile.ActiveNodeID != currentProfile.ActiveNodeID {
		t.Fatalf("expected profile projection to be preserved, got %#v", decoded.Profile)
	}
}

func TestDecodeConfigImportParamsRejectsMissingParams(t *testing.T) {
	_, err := DecodeConfigImportParams(nil, config.ProfileProjectionConfig{})
	var requestErr ConfigImportError
	if err == nil || !errors.As(err, &requestErr) || requestErr.Kind != ConfigImportInvalidParams {
		t.Fatalf("expected invalid params error, got %T %v", err, err)
	}
}

func TestDecodeConfigImportParamsRejectsInvalidJSONAsConfigError(t *testing.T) {
	raw := json.RawMessage(`{"schema_version":`)

	_, err := DecodeConfigImportParams(&raw, config.ProfileProjectionConfig{})
	var requestErr ConfigImportError
	if err == nil || !errors.As(err, &requestErr) || requestErr.Kind != ConfigImportInvalidConfig {
		t.Fatalf("expected config error, got %T %v", err, err)
	}
}

func TestDecodeConfigImportParamsRejectsUnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"schema_version":5,"legacy":true}`)

	_, err := DecodeConfigImportParams(&raw, config.ProfileProjectionConfig{})
	if err == nil || !strings.Contains(err.Error(), "unknown config import field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestDecodeConfigImportParamsRequiresSchemaVersion(t *testing.T) {
	raw := json.RawMessage(`{"proxy":{}}`)

	_, err := DecodeConfigImportParams(&raw, config.ProfileProjectionConfig{})
	if err == nil || !strings.Contains(err.Error(), "schema_version is required") {
		t.Fatalf("expected schema_version error, got %v", err)
	}
}
