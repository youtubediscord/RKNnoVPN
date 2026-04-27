package main

import (
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestRuntimeOperationForConfigMutation(t *testing.T) {
	cases := map[string]runtimev2.OperationKind{
		"config-import":               runtimev2.OperationConfigMutation,
		"profile.apply":               runtimev2.OperationProfileApply,
		"profile.importNodes":         runtimev2.OperationProfileApply,
		"profile.setActiveNode":       runtimev2.OperationProfileApply,
		"subscription.refresh":        runtimev2.OperationProfileApply,
		"backend.applyDesiredState":   runtimev2.OperationProfileApply,
		"unknown-profile-like-action": runtimev2.OperationProfileApply,
	}
	for action, want := range cases {
		if got := runtimeOperationForConfigMutation(action); got != want {
			t.Fatalf("action %s mapped to %s, want %s", action, got, want)
		}
	}
}
