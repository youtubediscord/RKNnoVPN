package diagnostics

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseIntegrityReportTreatsMissingCurrentAsFreshInstall(t *testing.T) {
	report := ReleaseIntegrityReport(t.TempDir())
	if !report.OK || !report.MissingCurrent {
		t.Fatalf("missing current release should be fresh-install OK, got %#v", report)
	}
	if issues := ReleaseIntegrityIssues(report); len(issues) != 0 {
		t.Fatalf("missing current release should not create compatibility issues, got %#v", issues)
	}
}

func TestReleaseIntegrityReportValidatesManifestHashes(t *testing.T) {
	dataDir := t.TempDir()
	releaseDir := filepath.Join(dataDir, "releases", "v2.0.0")
	writeFile(t, filepath.Join(releaseDir, "bin", "daemon"), "daemon-binary\n")
	hash := sha256.Sum256([]byte("daemon-binary\n"))
	manifest := `{"version":"v2.0.0","installed_at":"2026-04-28T00:00:00Z","files_sha256":{"bin/daemon":"` + hex.EncodeToString(hash[:]) + `"}}`
	writeFile(t, filepath.Join(releaseDir, "install-manifest.json"), manifest)
	linkCurrent(t, dataDir, releaseDir)

	report := ReleaseIntegrityReport(dataDir)
	if !report.OK || report.CheckedFiles != 1 || report.Version != "v2.0.0" {
		t.Fatalf("expected valid release integrity report, got %#v", report)
	}
	if issues := ReleaseIntegrityIssues(report); len(issues) != 0 {
		t.Fatalf("valid release should not create issues, got %#v", issues)
	}
}

func TestReleaseIntegrityReportFlagsMissingManifest(t *testing.T) {
	dataDir := t.TempDir()
	releaseDir := filepath.Join(dataDir, "releases", "v2.0.0")
	writeFile(t, filepath.Join(releaseDir, "bin", "daemon"), "daemon-binary\n")
	linkCurrent(t, dataDir, releaseDir)

	report := ReleaseIntegrityReport(dataDir)
	if report.OK || !report.MissingManifest {
		t.Fatalf("missing manifest should fail integrity, got %#v", report)
	}
	if issues := strings.Join(ReleaseIntegrityIssues(report), "\n"); !strings.Contains(issues, "manifest is missing") {
		t.Fatalf("missing manifest issue not reported: %q", issues)
	}
}

func TestReleaseIntegrityReportFlagsChecksumMismatch(t *testing.T) {
	dataDir := t.TempDir()
	releaseDir := filepath.Join(dataDir, "releases", "v2.0.0")
	writeFile(t, filepath.Join(releaseDir, "bin", "daemon"), "changed\n")
	writeFile(t, filepath.Join(releaseDir, "install-manifest.json"), `{"version":"v2.0.0","installed_at":"2026-04-28T00:00:00Z","files_sha256":{"bin/daemon":"0000"}}`)
	linkCurrent(t, dataDir, releaseDir)

	report := ReleaseIntegrityReport(dataDir)
	if report.OK || len(report.Mismatches) != 1 {
		t.Fatalf("checksum mismatch should fail integrity, got %#v", report)
	}
	if issues := strings.Join(ReleaseIntegrityIssues(report), "\n"); !strings.Contains(issues, "checksum mismatch") {
		t.Fatalf("checksum mismatch issue not reported: %q", issues)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func linkCurrent(t *testing.T, dataDir string, releaseDir string) {
	t.Helper()
	if err := os.Symlink(releaseDir, filepath.Join(dataDir, "current")); err != nil {
		t.Fatal(err)
	}
}
