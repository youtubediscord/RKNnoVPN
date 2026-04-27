package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadModuleVersionUsesFirstReadableModuleProp(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.prop")
	moduleProp := filepath.Join(dir, "module.prop")
	if err := os.WriteFile(moduleProp, []byte("id=rknnovpn\nversion=v2.0.0\nversionCode=2000\n"), 0644); err != nil {
		t.Fatal(err)
	}

	version := ReadModuleVersion(missing, moduleProp)
	if version["path"] != moduleProp || version["version"] != "v2.0.0" || version["versionCode"] != "2000" {
		t.Fatalf("unexpected module version: %#v", version)
	}
}

func TestReadModuleVersionReturnsUnknownWhenUnavailable(t *testing.T) {
	version := ReadModuleVersion(filepath.Join(t.TempDir(), "missing.prop"))
	if version["version"] != "unknown" {
		t.Fatalf("missing module.prop should return unknown version, got %#v", version)
	}
}
