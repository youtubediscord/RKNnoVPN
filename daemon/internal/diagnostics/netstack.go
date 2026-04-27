package diagnostics

import (
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
)

func VerifyCleanup(dataDir string, env map[string]string, cfgAvailable bool) netstack.Report {
	if !cfgAvailable {
		return netstack.Report{
			Operation: "verify-cleanup",
			Status:    "failed",
			Steps: []netstack.Step{{
				Name:   "verify-cleanup",
				Status: "failed",
				Detail: "config unavailable for cleanup verification",
			}},
			Leftovers: []string{"config unavailable for cleanup verification"},
		}
	}
	return netstack.New(dataDir, env, core.ExecScript).
		WithExecCommand(core.ExecCommand).
		VerifyCleanup()
}

func VerifyRuntime(dataDir string, env map[string]string, cfgAvailable bool, runtimeActive bool) netstack.Report {
	if !cfgAvailable {
		return netstack.Report{
			Operation: "verify",
			Status:    "failed",
			Steps: []netstack.Step{{
				Name:   "netstack-verify",
				Status: "failed",
				Detail: "config unavailable for runtime netstack verification",
			}},
			Errors: []string{"config unavailable for runtime netstack verification"},
		}
	}
	if !runtimeActive {
		return netstack.Report{
			Operation: "verify",
			Status:    "skipped",
			Steps: []netstack.Step{{
				Name:   "netstack-verify",
				Status: "skipped",
				Detail: "runtime is not active",
			}},
		}
	}
	return netstack.New(dataDir, env, core.ExecScript).Verify()
}
