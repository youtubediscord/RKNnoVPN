package applytx

import (
	"errors"
	"reflect"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func TestConfigTransactionRunsInOrderAndReportsRuntimeState(t *testing.T) {
	cfg := config.DefaultConfig()
	var calls []string

	result, err := ConfigTransaction{
		EnsureIdle: func() error {
			calls = append(calls, "ensure-idle")
			return nil
		},
		SaveProfile: func(next *config.Config) error {
			if next != cfg {
				t.Fatalf("unexpected config pointer")
			}
			calls = append(calls, "save-profile")
			return nil
		},
		RuntimeRunning: func() bool {
			calls = append(calls, "runtime-running")
			return true
		},
		ApplyConfig: func(next *config.Config, reload bool) error {
			if next != cfg || !reload {
				t.Fatalf("unexpected apply args")
			}
			calls = append(calls, "apply-config")
			return nil
		},
	}.Run(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigSaved || !result.RuntimeWasRunning {
		t.Fatalf("unexpected transaction result: %#v", result)
	}
	want := []string{"ensure-idle", "save-profile", "runtime-running", "apply-config"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("unexpected call order: got %#v want %#v", calls, want)
	}
}

func TestConfigTransactionStopsBeforeSaveWhenRuntimeBusy(t *testing.T) {
	cfg := config.DefaultConfig()
	busyErr := errors.New("busy")
	saved := false

	result, err := ConfigTransaction{
		EnsureIdle: func() error {
			return busyErr
		},
		SaveProfile: func(*config.Config) error {
			saved = true
			return nil
		},
		ApplyConfig: func(*config.Config, bool) error {
			t.Fatal("apply must not run")
			return nil
		},
	}.Run(cfg, true)
	if !errors.Is(err, busyErr) {
		t.Fatalf("expected busy error, got %v", err)
	}
	if saved || result.ConfigSaved {
		t.Fatalf("busy transaction must not save config: saved=%v result=%#v", saved, result)
	}
}

func TestConfigTransactionReportsSavedApplyFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	applyErr := errors.New("apply failed")

	result, err := ConfigTransaction{
		SaveProfile: func(*config.Config) error {
			return nil
		},
		RuntimeRunning: func() bool {
			return true
		},
		ApplyConfig: func(*config.Config, bool) error {
			return applyErr
		},
	}.Run(cfg, true)
	if !errors.Is(err, applyErr) {
		t.Fatalf("expected apply error, got %v", err)
	}
	if !result.ConfigSaved || !result.RuntimeWasRunning {
		t.Fatalf("saved apply failure must preserve transaction state: %#v", result)
	}
}

func TestConfigTransactionRejectsIncompleteWiring(t *testing.T) {
	cfg := config.DefaultConfig()
	if _, err := (ConfigTransaction{}).Run(nil, false); err == nil {
		t.Fatal("nil config must fail")
	}
	if result, err := (ConfigTransaction{}).Run(cfg, false); err == nil || result.ConfigSaved {
		t.Fatalf("missing save callback must fail before save, result=%#v err=%v", result, err)
	}
	if result, err := (ConfigTransaction{
		SaveProfile: func(*config.Config) error { return nil },
	}).Run(cfg, false); err == nil || !result.ConfigSaved {
		t.Fatalf("missing apply callback must fail after save, result=%#v err=%v", result, err)
	}
}

func TestConfigMutationOperationDocumentsTransactionStages(t *testing.T) {
	op := ConfigMutationOperation("config-import", "accepted", true, true, false, "accepted", -1, "", "", nil)
	stages, ok := op["stages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("operation stages missing: %#v", op)
	}
	for _, name := range []string{"validate", "render", "persist-draft", "runtime-apply", "verify", "commit-generation"} {
		if !hasStage(stages, name) {
			t.Fatalf("operation missing stage %s: %#v", name, stages)
		}
	}
	if op["accepted"] != true || op["operationActive"] != true {
		t.Fatalf("accepted async mutation must be explicit: %#v", op)
	}
}

func TestProfileOperationMapsResetReportRollback(t *testing.T) {
	resetReport := struct {
		Status string
	}{Status: "ok"}
	result := ProfileOperation("profile.apply", "saved_not_applied", true, false, "failed", 2, 1, "CORE_SPAWN_FAILED", "boom", resetReport, nil, -1)
	if result["rollback"] != "cleanup_succeeded" {
		t.Fatalf("reset report status should drive rollback, got %#v", result)
	}
	if result["resetReport"] == nil {
		t.Fatalf("reset report should remain visible: %#v", result)
	}
}

func hasStage(stages []map[string]interface{}, name string) bool {
	for _, stage := range stages {
		if stage["name"] == name {
			return true
		}
	}
	return false
}
