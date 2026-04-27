package updater

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeVersionTag(t *testing.T) {
	cases := map[string]string{
		"":          "v0.0.0",
		"1.6.4":     "v1.6.4",
		"v1.6.4":    "v1.6.4",
		"V1.6.4":    "v1.6.4",
		"vv1.6.4":   "v1.6.4",
		" 1.6.4 \n": "v1.6.4",
	}
	for input, want := range cases {
		if got := NormalizeVersionTag(input); got != want {
			t.Fatalf("NormalizeVersionTag(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCompareSemverAcceptsBareCurrentVersion(t *testing.T) {
	if !compareSemver("1.6.4", "v1.6.5") {
		t.Fatal("expected v1.6.5 to be newer than bare 1.6.4")
	}
	if compareSemver("v1.6.4", "1.6.4") {
		t.Fatal("same version should not be newer")
	}
	if !compareSemver("vv1.6.4", "v1.6.5") {
		t.Fatal("expected v1.6.5 to be newer than duplicated-prefix vv1.6.4")
	}
}

func TestVerifyChecksumsRequiresEveryDownloadedArtifact(t *testing.T) {
	dir := t.TempDir()
	modulePath := filepath.Join(dir, "module.zip")
	apkPath := filepath.Join(dir, "panel.apk")
	writeTestFile(t, modulePath, 0644, "module")
	writeTestFile(t, apkPath, 0644, "apk")
	moduleHash, err := sha256File(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	sumPath := filepath.Join(dir, "SHA256SUMS.txt")
	if err := os.WriteFile(sumPath, []byte(moduleHash+"  RKNnoVPN-module.zip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ok, err := verifyChecksums(sumPath, dir)
	if err == nil || ok {
		t.Fatalf("expected missing panel.apk checksum to fail, ok=%v err=%v", ok, err)
	}
}

func TestVerifyChecksumsAcceptsAllDownloadedArtifacts(t *testing.T) {
	dir := t.TempDir()
	modulePath := filepath.Join(dir, "module.zip")
	apkPath := filepath.Join(dir, "panel.apk")
	writeTestFile(t, modulePath, 0644, "module")
	writeTestFile(t, apkPath, 0644, "apk")
	moduleHash, err := sha256File(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	apkHash, err := sha256File(apkPath)
	if err != nil {
		t.Fatal(err)
	}
	sumPath := filepath.Join(dir, "SHA256SUMS.txt")
	sums := moduleHash + "  RKNnoVPN-module.zip\n" +
		apkHash + "  RKNnoVPN-panel.apk\n"
	if err := os.WriteFile(sumPath, []byte(sums), 0644); err != nil {
		t.Fatal(err)
	}

	ok, err := verifyChecksums(sumPath, dir)
	if err != nil || !ok {
		t.Fatalf("expected checksum verification to pass, ok=%v err=%v", ok, err)
	}
}

func TestDownloadUpdateRejectsMissingChecksumAsset(t *testing.T) {
	_, err := DownloadUpdate(&UpdateInfo{
		ModuleURL:  "https://example.invalid/module.zip",
		ApkURL:     "https://example.invalid/panel.apk",
		ModuleSize: int64(len("module")),
		ApkSize:    int64(len("apk")),
	}, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected missing checksum URL to reject downloaded update")
	}
}

func TestVerifyDownloadedUpdateRequiresChecksumFile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "module.zip"), 0644, "module")
	writeTestFile(t, filepath.Join(dir, "panel.apk"), 0644, "apk")

	if err := VerifyDownloadedUpdate(filepath.Join(dir, "module.zip"), filepath.Join(dir, "panel.apk")); err == nil {
		t.Fatal("expected missing SHA256SUMS.txt to reject install")
	}
}

func TestVerifyDownloadedUpdateRequiresVerifiedManifest(t *testing.T) {
	dir := t.TempDir()
	modulePath := filepath.Join(dir, "module.zip")
	apkPath := filepath.Join(dir, "panel.apk")
	writeTestFile(t, modulePath, 0644, "module")
	writeTestFile(t, apkPath, 0644, "apk")
	writeChecksumsForTest(t, dir)

	err := VerifyDownloadedUpdate(modulePath, apkPath)
	if err == nil || !strings.Contains(err.Error(), "verified update manifest") {
		t.Fatalf("expected missing verified manifest error, got %v", err)
	}
}

func TestVerifyDownloadedUpdateAcceptsManifestVerifiedArtifacts(t *testing.T) {
	dir := t.TempDir()
	modulePath := filepath.Join(dir, "module.zip")
	apkPath := filepath.Join(dir, "panel.apk")
	writeTestFile(t, modulePath, 0644, "module")
	writeTestFile(t, apkPath, 0644, "apk")
	writeChecksumsForTest(t, dir)
	writeVerifiedManifestForTest(t, dir, "v1.7.4")

	if err := VerifyDownloadedUpdate(modulePath, apkPath); err != nil {
		t.Fatalf("expected manifest-verified update to pass: %v", err)
	}
}

func TestValidateModuleStagingRejectsIncompleteBundle(t *testing.T) {
	staging := t.TempDir()
	binDir := filepath.Join(staging, "binaries", runtimeBinaryArch())
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"privd", "privctl"} {
		writeTestFile(t, filepath.Join(binDir, name), 0755)
	}

	if err := validateModuleStaging(staging, binDir); err == nil {
		t.Fatal("expected missing sing-box to reject staged module")
	}
}

func TestValidateModuleStagingAcceptsCompleteBundle(t *testing.T) {
	staging := t.TempDir()
	binDir := filepath.Join(staging, "binaries", runtimeBinaryArch())
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sing-box", "privd", "privctl"} {
		writeTestFile(t, filepath.Join(binDir, name), 0755)
	}
	for _, path := range []string{
		"OWNERSHIP.md",
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"scripts/dns.sh",
		"scripts/iptables.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"scripts/lib/rknnovpn_env.sh",
		"scripts/lib/rknnovpn_install.sh",
		"scripts/lib/rknnovpn_installer_flow.sh",
		"scripts/lib/rknnovpn_netstack.sh",
		"scripts/lib/rknnovpn_iptables_rules.sh",
		"defaults/config.json",
	} {
		writeTestFile(t, filepath.Join(staging, path), 0644)
	}
	writeTestFile(t, filepath.Join(staging, "module.prop"), 0644, "id=rknnovpn\nversion=v1.6.4\nversionCode=164\n")

	if err := validateModuleStaging(staging, binDir); err != nil {
		t.Fatalf("expected complete staged module to pass: %v", err)
	}
}

func TestValidateModuleStagingRejectsBadModuleProp(t *testing.T) {
	staging := t.TempDir()
	binDir := filepath.Join(staging, "binaries", runtimeBinaryArch())
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sing-box", "privd", "privctl"} {
		writeTestFile(t, filepath.Join(binDir, name), 0755)
	}
	for _, path := range []string{
		"OWNERSHIP.md",
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"scripts/dns.sh",
		"scripts/iptables.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"scripts/lib/rknnovpn_env.sh",
		"scripts/lib/rknnovpn_install.sh",
		"scripts/lib/rknnovpn_installer_flow.sh",
		"scripts/lib/rknnovpn_netstack.sh",
		"scripts/lib/rknnovpn_iptables_rules.sh",
		"defaults/config.json",
	} {
		writeTestFile(t, filepath.Join(staging, path), 0644)
	}
	writeTestFile(t, filepath.Join(staging, "module.prop"), 0644, "id=other\nversion=v1.6.4\nversionCode=164\n")

	if err := validateModuleStaging(staging, binDir); err == nil {
		t.Fatal("expected invalid module id to reject staged module")
	}
}

func TestPreflightModuleUpdateRejectsIncompleteZip(t *testing.T) {
	dataDir := t.TempDir()
	zipPath := filepath.Join(dataDir, "module.zip")
	writeZipForTest(t, zipPath, map[string]zipTestFile{
		"module.prop": {mode: 0644, body: "id=rknnovpn\nversion=v1.7.4\nversionCode=174\n"},
	})

	if _, err := PreflightModuleUpdate(zipPath, dataDir); err == nil {
		t.Fatal("expected incomplete module zip to fail preflight")
	}
}

func TestPreflightModuleUpdateReturnsNormalizedModuleVersion(t *testing.T) {
	dataDir := t.TempDir()
	zipPath := filepath.Join(dataDir, "module.zip")
	writeCompleteModuleZipForTest(t, zipPath, "V1.7.4")

	info, err := PreflightModuleUpdate(zipPath, dataDir)
	if err != nil {
		t.Fatalf("expected complete module zip to pass preflight: %v", err)
	}
	if info.Version != "v1.7.4" || info.VersionCode != "174" {
		t.Fatalf("unexpected preflight info: %#v", info)
	}
}

func TestPrepareVersionedReleasePublishesNormalizedBundle(t *testing.T) {
	dataDir := t.TempDir()
	staging := t.TempDir()
	binDir := filepath.Join(staging, "binaries", runtimeBinaryArch())
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sing-box", "privd", "privctl"} {
		writeTestFile(t, filepath.Join(binDir, name), 0755)
	}
	for _, path := range []string{
		"OWNERSHIP.md",
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"scripts/dns.sh",
		"scripts/iptables.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"scripts/lib/rknnovpn_env.sh",
		"scripts/lib/rknnovpn_install.sh",
		"scripts/lib/rknnovpn_installer_flow.sh",
		"scripts/lib/rknnovpn_netstack.sh",
		"scripts/lib/rknnovpn_iptables_rules.sh",
		"defaults/config.json",
	} {
		writeTestFile(t, filepath.Join(staging, path), 0644)
	}
	writeTestFile(t, filepath.Join(staging, "module.prop"), 0644, "id=rknnovpn\nversion=V1.6.4\nversionCode=164\n")

	releaseDir, err := prepareVersionedRelease(staging, binDir, dataDir, "V1.6.4")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(releaseDir, "bin", "privd"),
		filepath.Join(releaseDir, "bin", "privctl"),
		filepath.Join(releaseDir, "bin", "sing-box"),
		filepath.Join(releaseDir, "module", "module.prop"),
		filepath.Join(releaseDir, "module", "OWNERSHIP.md"),
		filepath.Join(releaseDir, "module", "scripts", "rescue_reset.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "rknnovpn_env.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "rknnovpn_install.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "rknnovpn_installer_flow.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "rknnovpn_netstack.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "rknnovpn_iptables_rules.sh"),
		filepath.Join(releaseDir, "install-manifest.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("release artifact missing %s: %v", path, err)
		}
	}

	if err := updateCurrentReleaseSymlink(dataDir, releaseDir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dataDir, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != releaseDir {
		t.Fatalf("current symlink = %q, want %q", target, releaseDir)
	}

	manifestData, err := os.ReadFile(filepath.Join(releaseDir, "install-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest releaseManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "v1.6.4" {
		t.Fatalf("manifest version = %q, want v1.6.4", manifest.Version)
	}
	for _, rel := range []string{
		"bin/privd",
		"bin/privctl",
		"bin/sing-box",
		"module/module.prop",
		"module/OWNERSHIP.md",
		"module/scripts/rescue_reset.sh",
		"module/scripts/lib/rknnovpn_env.sh",
		"module/scripts/lib/rknnovpn_install.sh",
		"module/scripts/lib/rknnovpn_installer_flow.sh",
		"module/scripts/lib/rknnovpn_netstack.sh",
		"module/scripts/lib/rknnovpn_iptables_rules.sh",
	} {
		got := manifest.Files[rel]
		if got == "" {
			t.Fatalf("manifest missing hash for %s: %#v", rel, manifest.Files)
		}
		want, err := sha256File(filepath.Join(releaseDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("manifest hash for %s = %q, want %q", rel, got, want)
		}
	}
}

func TestUpdateCurrentReleaseSymlinkReplacesDirectory(t *testing.T) {
	dataDir := t.TempDir()
	releaseDir := filepath.Join(dataDir, "releases", "v1.7.3")
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		t.Fatal(err)
	}
	currentDir := filepath.Join(dataDir, "current")
	if err := os.MkdirAll(currentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(currentDir, "stale"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := updateCurrentReleaseSymlink(dataDir, releaseDir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(currentDir)
	if err != nil {
		t.Fatal(err)
	}
	if target != releaseDir {
		t.Fatalf("current symlink = %q, want %q", target, releaseDir)
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, "releases", "current.pre-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one current directory backup, got %v", matches)
	}
	if _, err := os.Stat(filepath.Join(matches[0], "stale")); err != nil {
		t.Fatalf("stale current directory was not moved aside: %v", err)
	}
}

func TestStopCurrentProxyUsesCanonicalRescueReset(t *testing.T) {
	dataDir := t.TempDir()
	logPath := filepath.Join(dataDir, "called.txt")
	scriptPath := filepath.Join(dataDir, "scripts", "rescue_reset.sh")
	writeTestFile(t, scriptPath, 0755, "#!/bin/sh\nprintf '%s:%s\\n' \"$1\" \"$RKNNOVPN_DIR\" > \"$RKNNOVPN_DIR/called.txt\"\n")

	if err := stopCurrentProxy(dataDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "update-clean:" + dataDir + "\n"
	if string(data) != want {
		t.Fatalf("rescue_reset invocation = %q, want %q", string(data), want)
	}
}

func TestStopCurrentProxyRequiresCanonicalRescueReset(t *testing.T) {
	if err := stopCurrentProxy(t.TempDir()); err == nil {
		t.Fatal("expected missing rescue_reset.sh to reject module cleanup")
	}
}

func TestInstallTrackerPersistsObservableState(t *testing.T) {
	dataDir := t.TempDir()
	tracker := NewInstallTracker(dataDir, 42, filepath.Join(dataDir, "update", "module.zip"), filepath.Join(dataDir, "update", "panel.apk"))
	if err := tracker.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Step("update-install-apk", "running", "APK_INSTALLING", "panel.apk"); err != nil {
		t.Fatal(err)
	}
	if err := tracker.MarkAPKInstalled(); err != nil {
		t.Fatal(err)
	}
	state, err := ReadInstallState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "running" ||
		state.Generation != 42 ||
		state.Step != "update-install-apk" ||
		state.StepStatus != "running" ||
		state.Code != "APK_INSTALLING" ||
		!state.ApkInstalled ||
		state.ModuleInstalled {
		t.Fatalf("unexpected install state: %#v", state)
	}
}

func TestInstallTrackerRecordsFailedStep(t *testing.T) {
	dataDir := t.TempDir()
	tracker := NewInstallTracker(dataDir, 7, "module.zip", "panel.apk")
	if err := tracker.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Step("update-install-module", "failed", "MODULE_INSTALL_FAILED", "boom"); err != nil {
		t.Fatal(err)
	}
	state, err := ReadInstallState(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" ||
		state.Step != "update-install-module" ||
		state.Code != "MODULE_INSTALL_FAILED" ||
		state.Detail != "boom" {
		t.Fatalf("unexpected failed install state: %#v", state)
	}
}

func writeTestFile(t *testing.T, path string, perm os.FileMode, contents ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	content := "test\n"
	if len(contents) > 0 {
		content = contents[0]
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func writeChecksumsForTest(t *testing.T, dir string) {
	t.Helper()
	moduleHash, err := sha256File(filepath.Join(dir, "module.zip"))
	if err != nil {
		t.Fatal(err)
	}
	apkHash, err := sha256File(filepath.Join(dir, "panel.apk"))
	if err != nil {
		t.Fatal(err)
	}
	sums := moduleHash + "  RKNnoVPN-module.zip\n" +
		apkHash + "  RKNnoVPN-panel.apk\n"
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS.txt"), []byte(sums), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeVerifiedManifestForTest(t *testing.T, dir string, version string) {
	t.Helper()
	moduleHash, err := sha256File(filepath.Join(dir, "module.zip"))
	if err != nil {
		t.Fatal(err)
	}
	apkHash, err := sha256File(filepath.Join(dir, "panel.apk"))
	if err != nil {
		t.Fatal(err)
	}
	checksumHash, err := sha256File(filepath.Join(dir, "SHA256SUMS.txt"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(VerifiedUpdateManifest{
		ManifestVersion: 1,
		CurrentVersion:  "v1.7.3",
		LatestVersion:   version,
		ModuleSHA256:    moduleHash,
		ApkSHA256:       apkHash,
		ChecksumsSHA256: checksumHash,
		VerifiedAt:      "2026-04-27T00:00:00Z",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "update-manifest.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

type zipTestFile struct {
	mode os.FileMode
	body string
}

func writeCompleteModuleZipForTest(t *testing.T, zipPath string, version string) {
	t.Helper()
	files := map[string]zipTestFile{}
	for _, name := range []string{"sing-box", "privd", "privctl"} {
		files[filepath.ToSlash(filepath.Join("binaries", runtimeBinaryArch(), name))] = zipTestFile{mode: 0755, body: "bin\n"}
	}
	for _, path := range []string{
		"OWNERSHIP.md",
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"scripts/dns.sh",
		"scripts/iptables.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"scripts/lib/rknnovpn_env.sh",
		"scripts/lib/rknnovpn_install.sh",
		"scripts/lib/rknnovpn_installer_flow.sh",
		"scripts/lib/rknnovpn_netstack.sh",
		"scripts/lib/rknnovpn_iptables_rules.sh",
		"defaults/config.json",
	} {
		files[path] = zipTestFile{mode: 0644, body: "test\n"}
	}
	files["module.prop"] = zipTestFile{
		mode: 0644,
		body: "id=rknnovpn\nversion=" + version + "\nversionCode=174\n",
	}
	writeZipForTest(t, zipPath, files)
}

func writeZipForTest(t *testing.T, path string, files map[string]zipTestFile) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	for name, file := range files {
		header := &zip.FileHeader{
			Name:   filepath.ToSlash(name),
			Method: zip.Deflate,
		}
		header.SetMode(file.mode)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}
