package main

import (
	applytx "github.com/youtubediscord/RKNnoVPN/daemon/internal/apply"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

// persistProfileConfigMutationForAction is the daemon-owned transaction gate
// for every mutation that changes user intent and its runtime projection.
func (d *daemon) persistProfileConfigMutationForAction(nextCfg *config.Config, reload bool, action string) (applytx.ConfigTransactionResult, error) {
	return applytx.ConfigTransaction{
		Action:     action,
		EnsureIdle: d.failIfRuntimeOperationActive,
		SaveProfile: func(nextCfg *config.Config) error {
			return profiledoc.Save(d.profilePath, profiledoc.FromConfig(nextCfg))
		},
		RuntimeRunning: func() bool {
			state := d.coreMgr.GetState()
			return state == core.StateRunning || state == core.StateDegraded
		},
		ApplyConfig: func(nextCfg *config.Config, reload bool, operation runtimev2.OperationKind) error {
			return d.applyConfigWithOperation(nextCfg, reload, operation)
		},
	}.Run(nextCfg, reload)
}
