package updater

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInstallArtifactsDefaultsToCanonicalUpdateDir(t *testing.T) {
	dataDir := t.TempDir()
	updateDir := CanonicalUpdateDir(dataDir)
	modulePath := filepath.Join(updateDir, "module.zip")
	apkPath := filepath.Join(updateDir, "panel.apk")
	writeTestFile(t, modulePath, 0o644, "module")
	writeTestFile(t, apkPath, 0o644, "apk")

	artifacts, err := ResolveInstallArtifacts(dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.UpdateDir != updateDir || artifacts.ModulePath != modulePath || artifacts.ApkPath != apkPath {
		t.Fatalf("unexpected canonical artifacts: %#v", artifacts)
	}
	if !artifacts.ModuleExists || !artifacts.ApkExists {
		t.Fatalf("expected both artifacts to exist: %#v", artifacts)
	}
}

func TestResolveInstallArtifactsRejectsNonCanonicalPaths(t *testing.T) {
	dataDir := t.TempDir()
	raw := json.RawMessage(`{"module_path":"/tmp/module.zip","apk_path":"/tmp/panel.apk"}`)

	_, err := ResolveInstallArtifacts(dataDir, &raw)
	if err == nil || !strings.Contains(err.Error(), "canonical update directory") {
		t.Fatalf("expected canonical path error, got %v", err)
	}
}

func TestResolveInstallArtifactsRequiresBothArtifacts(t *testing.T) {
	dataDir := t.TempDir()
	updateDir := CanonicalUpdateDir(dataDir)
	writeTestFile(t, filepath.Join(updateDir, "module.zip"), 0o644, "module")

	_, err := ResolveInstallArtifacts(dataDir, nil)
	if err == nil || !strings.Contains(err.Error(), "both module and APK artifacts") {
		t.Fatalf("expected paired artifact error, got %v", err)
	}
}

func TestResolveInstallArtifactsRejectsInvalidParams(t *testing.T) {
	raw := json.RawMessage(`{"module_path":`)

	_, err := ResolveInstallArtifacts(t.TempDir(), &raw)
	if err == nil || !strings.Contains(err.Error(), "invalid params") {
		t.Fatalf("expected invalid params error, got %v", err)
	}
}
