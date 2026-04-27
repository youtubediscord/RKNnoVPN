package diagnostics

import "testing"

func TestVerifyCleanupReportsUnavailableConfig(t *testing.T) {
	report := VerifyCleanup(t.TempDir(), nil, false)
	if report.Operation != "verify-cleanup" || report.Status != "failed" {
		t.Fatalf("unexpected cleanup report: %#v", report)
	}
	if len(report.Leftovers) != 1 || report.Leftovers[0] != "config unavailable for cleanup verification" {
		t.Fatalf("unexpected cleanup leftovers: %#v", report.Leftovers)
	}
}

func TestVerifyRuntimeReportsUnavailableConfig(t *testing.T) {
	report := VerifyRuntime(t.TempDir(), nil, false, false)
	if report.Operation != "verify" || report.Status != "failed" {
		t.Fatalf("unexpected runtime report: %#v", report)
	}
	if len(report.Errors) != 1 || report.Errors[0] != "config unavailable for runtime netstack verification" {
		t.Fatalf("unexpected runtime errors: %#v", report.Errors)
	}
}

func TestVerifyRuntimeSkipsInactiveRuntime(t *testing.T) {
	report := VerifyRuntime(t.TempDir(), map[string]string{"RKNNOVPN_DIR": "/data/adb/modules/rknnovpn"}, true, false)
	if report.Operation != "verify" || report.Status != "skipped" {
		t.Fatalf("inactive runtime should skip netstack verify, got %#v", report)
	}
	if len(report.Steps) != 1 || report.Steps[0].Status != "skipped" {
		t.Fatalf("inactive runtime should expose skipped step, got %#v", report.Steps)
	}
}
