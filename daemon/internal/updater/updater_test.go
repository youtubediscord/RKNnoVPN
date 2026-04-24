package updater

import (
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
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"scripts/dns.sh",
		"scripts/iptables.sh",
		"scripts/net_handler.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
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
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"scripts/dns.sh",
		"scripts/iptables.sh",
		"scripts/net_handler.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"defaults/config.json",
	} {
		writeTestFile(t, filepath.Join(staging, path), 0644)
	}
	writeTestFile(t, filepath.Join(staging, "module.prop"), 0644, "id=other\nversion=v1.6.4\nversionCode=164\n")

	if err := validateModuleStaging(staging, binDir); err == nil {
		t.Fatal("expected invalid module id to reject staged module")
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
