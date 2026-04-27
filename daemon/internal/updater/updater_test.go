package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/module.zip":
			_, _ = w.Write([]byte("module"))
		case "/panel.apk":
			_, _ = w.Write([]byte("apk"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := DownloadUpdate(&UpdateInfo{
		ModuleURL:  server.URL + "/module.zip",
		ApkURL:     server.URL + "/panel.apk",
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
		"scripts/net_handler.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"scripts/lib/privstack_env.sh",
		"scripts/lib/privstack_install.sh",
		"scripts/lib/privstack_installer_flow.sh",
		"scripts/lib/privstack_netstack.sh",
		"scripts/lib/privstack_iptables_rules.sh",
		"defaults/config.json",
	} {
		writeTestFile(t, filepath.Join(staging, path), 0644)
	}
	writeTestFile(t, filepath.Join(staging, "module.prop"), 0644, "id=privstack\nversion=v1.6.4\nversionCode=164\n")

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
		"scripts/net_handler.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"scripts/lib/privstack_env.sh",
		"scripts/lib/privstack_install.sh",
		"scripts/lib/privstack_installer_flow.sh",
		"scripts/lib/privstack_netstack.sh",
		"scripts/lib/privstack_iptables_rules.sh",
		"defaults/config.json",
	} {
		writeTestFile(t, filepath.Join(staging, path), 0644)
	}
	writeTestFile(t, filepath.Join(staging, "module.prop"), 0644, "id=other\nversion=v1.6.4\nversionCode=164\n")

	if err := validateModuleStaging(staging, binDir); err == nil {
		t.Fatal("expected invalid module id to reject staged module")
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
		"scripts/net_handler.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"scripts/lib/privstack_env.sh",
		"scripts/lib/privstack_install.sh",
		"scripts/lib/privstack_installer_flow.sh",
		"scripts/lib/privstack_netstack.sh",
		"scripts/lib/privstack_iptables_rules.sh",
		"defaults/config.json",
	} {
		writeTestFile(t, filepath.Join(staging, path), 0644)
	}
	writeTestFile(t, filepath.Join(staging, "module.prop"), 0644, "id=privstack\nversion=V1.6.4\nversionCode=164\n")

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
		filepath.Join(releaseDir, "module", "scripts", "lib", "privstack_env.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "privstack_install.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "privstack_installer_flow.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "privstack_netstack.sh"),
		filepath.Join(releaseDir, "module", "scripts", "lib", "privstack_iptables_rules.sh"),
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
		"module/scripts/lib/privstack_env.sh",
		"module/scripts/lib/privstack_install.sh",
		"module/scripts/lib/privstack_installer_flow.sh",
		"module/scripts/lib/privstack_netstack.sh",
		"module/scripts/lib/privstack_iptables_rules.sh",
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
	writeTestFile(t, scriptPath, 0755, "#!/bin/sh\nprintf '%s:%s\\n' \"$1\" \"$PRIVSTACK_DIR\" > \"$PRIVSTACK_DIR/called.txt\"\n")

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
