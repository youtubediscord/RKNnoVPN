package runtimev2

import (
	"os"
	"testing"
	"time"
)

func TestRuntimeStateStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	status := Status{
		DesiredState: DesiredState{BackendKind: BackendRootTProxy, ActiveProfileID: "node-1"},
		AppliedState: AppliedState{
			BackendKind:     BackendRootTProxy,
			Phase:           PhaseHealthy,
			ActiveProfileID: "node-1",
			Generation:      7,
		},
		Health: HealthSnapshot{
			LastCode:        "OK",
			LastUserMessage: "healthy",
			CheckedAt:       time.Now(),
		},
		LastOperation: &OperationResult{
			OperationID: "op-1",
			Kind:        OperationStart,
			Generation:  7,
			StartedAt:   time.Now(),
			FinishedAt:  time.Now(),
			Succeeded:   true,
		},
	}

	if err := WriteRuntimeState(dir, status); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(RuntimeStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("runtime state mode = %v, want 0640", info.Mode().Perm())
	}
	loaded, err := ReadRuntimeState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DesiredGeneration != 7 || loaded.AppliedGeneration != 7 || loaded.AppliedState.Generation != 7 || loaded.LastOperation == nil || loaded.LastOperation.Kind != OperationStart {
		t.Fatalf("runtime state lost operation data: %#v", loaded)
	}
	if loaded.LastVerifiedAt == "" || loaded.LastHealthCode != "OK" || loaded.LastHealthMessage != "healthy" {
		t.Fatalf("runtime state lost health metadata: %#v", loaded)
	}
	if loaded.UpdatedAt == "" {
		t.Fatalf("runtime state must record update time: %#v", loaded)
	}
}
