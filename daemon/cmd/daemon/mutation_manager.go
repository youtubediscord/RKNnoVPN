package main

import (
	"fmt"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
)

type profileConfigMutationResult struct {
	ConfigSaved       bool
	RuntimeWasRunning bool
}

// persistProfileConfigMutation is the daemon-owned transaction gate for every
// mutation that changes user intent and its runtime projection.
func (d *daemon) persistProfileConfigMutation(nextCfg *config.Config, reload bool) (profileConfigMutationResult, error) {
	var result profileConfigMutationResult
	if nextCfg == nil {
		return result, fmt.Errorf("config is nil")
	}
	if err := d.failIfRuntimeOperationActive(); err != nil {
		return result, err
	}
	if err := profiledoc.Save(d.profilePath, profiledoc.FromConfig(nextCfg)); err != nil {
		return result, fmt.Errorf("persist profile: %w", err)
	}
	result.ConfigSaved = true
	result.RuntimeWasRunning = d.runtimeIsRunning()
	if err := d.applyConfig(nextCfg, reload); err != nil {
		return result, err
	}
	return result, nil
}
