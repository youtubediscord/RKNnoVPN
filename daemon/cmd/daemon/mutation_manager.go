package main

import (
	applytx "github.com/youtubediscord/RKNnoVPN/daemon/internal/apply"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

// persistProfileConfigMutationForAction is the daemon-owned transaction gate
// for every mutation that changes user intent and its runtime projection.
func (d *daemon) persistProfileConfigMutationForAction(nextCfg *config.Config, reload bool, action string) (applytx.ConfigTransactionResult, error) {
	operation := runtimeOperationForConfigMutation(action)
	return applytx.ConfigTransaction{
		EnsureIdle: d.failIfRuntimeOperationActive,
		SaveProfile: func(nextCfg *config.Config) error {
			return profiledoc.Save(d.profilePath, profiledoc.FromConfig(nextCfg))
		},
		RuntimeRunning: d.runtimeIsRunning,
		ApplyConfig: func(nextCfg *config.Config, reload bool) error {
			return d.applyConfigWithOperation(nextCfg, reload, operation)
		},
	}.Run(nextCfg, reload)
}

func runtimeOperationForConfigMutation(action string) runtimev2.OperationKind {
	switch action {
	case "config-import":
		return runtimev2.OperationConfigMutation
	case "profile.apply", "profile.importNodes", "profile.setActiveNode", "subscription.refresh", "backend.applyDesiredState":
		return runtimev2.OperationProfileApply
	default:
		return runtimev2.OperationProfileApply
	}
}
