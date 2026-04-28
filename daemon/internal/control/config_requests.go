package control

import (
	"encoding/json"
	"fmt"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

type ConfigImportErrorKind string

const (
	ConfigImportInvalidParams ConfigImportErrorKind = "invalid_params"
	ConfigImportInvalidConfig ConfigImportErrorKind = "config_error"
)

type ConfigImportError struct {
	Kind ConfigImportErrorKind
	Err  error
}

func (e ConfigImportError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func DecodeConfigImportParams(params *json.RawMessage, currentProfile config.ProfileProjectionConfig) (*config.Config, error) {
	if params == nil {
		return nil, ConfigImportError{Kind: ConfigImportInvalidParams, Err: fmt.Errorf("params required: full config JSON object")}
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(*params, &raw); err != nil {
		return nil, ConfigImportError{Kind: ConfigImportInvalidConfig, Err: fmt.Errorf("invalid config: %w", err)}
	}
	if len(raw) == 0 {
		return nil, ConfigImportError{Kind: ConfigImportInvalidParams, Err: fmt.Errorf("params required: non-empty full config JSON object")}
	}
	for key := range raw {
		if !isFullConfigImportKey(key) {
			return nil, ConfigImportError{
				Kind: ConfigImportInvalidParams,
				Err:  fmt.Errorf("unknown config import field %q; config-import expects a full daemon config object", key),
			}
		}
	}
	if _, ok := raw["schema_version"]; !ok {
		return nil, ConfigImportError{
			Kind: ConfigImportInvalidParams,
			Err:  fmt.Errorf("schema_version is required for full config import; use profile.apply for user intent updates"),
		}
	}

	newCfg := config.DefaultConfig()
	if err := json.Unmarshal(*params, newCfg); err != nil {
		return nil, ConfigImportError{Kind: ConfigImportInvalidConfig, Err: fmt.Errorf("invalid config: %w", err)}
	}
	newCfg.Profile = currentProfile
	return newCfg, nil
}

func isFullConfigImportKey(key string) bool {
	switch key {
	case "schema_version",
		"proxy",
		"transport",
		"node",
		"runtime_v2",
		"routing",
		"apps",
		"dns",
		"ipv6",
		"sharing",
		"health",
		"rescue",
		"autostart":
		return true
	default:
		return false
	}
}
