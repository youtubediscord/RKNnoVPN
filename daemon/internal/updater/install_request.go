package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type InstallArtifacts struct {
	UpdateDir    string
	ModulePath   string
	ApkPath      string
	ModuleExists bool
	ApkExists    bool
}

func ResolveInstallArtifacts(dataDir string, params *json.RawMessage) (InstallArtifacts, error) {
	updateDir := filepath.Join(dataDir, "update")
	expectedModulePath := filepath.Join(updateDir, "module.zip")
	expectedApkPath := filepath.Join(updateDir, "panel.apk")

	request := struct {
		ModulePath string `json:"module_path"`
		ApkPath    string `json:"apk_path"`
	}{
		ModulePath: expectedModulePath,
		ApkPath:    expectedApkPath,
	}
	if params != nil {
		if err := json.Unmarshal(*params, &request); err != nil {
			return InstallArtifacts{}, fmt.Errorf("invalid params: %w", err)
		}
		if request.ModulePath == "" {
			request.ModulePath = expectedModulePath
		}
		if request.ApkPath == "" {
			request.ApkPath = expectedApkPath
		}
	}

	modulePath := filepath.Clean(request.ModulePath)
	apkPath := filepath.Clean(request.ApkPath)
	if modulePath != filepath.Clean(expectedModulePath) || apkPath != filepath.Clean(expectedApkPath) {
		return InstallArtifacts{}, fmt.Errorf("update-install only accepts verified artifacts from the canonical update directory")
	}

	moduleExists := fileExists(modulePath)
	apkExists := fileExists(apkPath)
	if !moduleExists && !apkExists {
		return InstallArtifacts{}, fmt.Errorf("no downloaded update files found")
	}
	if moduleExists != apkExists {
		return InstallArtifacts{}, fmt.Errorf("this update requires both module and APK artifacts")
	}

	return InstallArtifacts{
		UpdateDir:    updateDir,
		ModulePath:   modulePath,
		ApkPath:      apkPath,
		ModuleExists: moduleExists,
		ApkExists:    apkExists,
	}, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
