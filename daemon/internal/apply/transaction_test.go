package applytx

import "testing"

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
