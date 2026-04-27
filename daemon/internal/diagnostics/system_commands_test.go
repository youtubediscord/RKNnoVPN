package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSingBoxCommandsRequireExistingFiles(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "sing-box")
	configPath := filepath.Join(dir, "config.json")

	version := SingBoxVersion(missing, 10, nil)
	if version.Error == "" || version.Command != missing+" version" {
		t.Fatalf("expected missing sing-box version error, got %#v", version)
	}

	check := SingBoxCheck(missing, configPath, 10, nil)
	if check.Error == "" || check.Command != missing+" check -c "+configPath {
		t.Fatalf("expected missing sing-box check error, got %#v", check)
	}
}

func TestSingBoxCheckRunsExpectedCommand(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box")
	configPath := filepath.Join(dir, "singbox.json")
	if err := os.WriteFile(binary, []byte("#!/system/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := SingBoxCheck(binary, configPath, 10, func(name string, args ...string) (string, error) {
		return name + " " + strings.Join(args, " "), nil
	})

	if result.Error != "" {
		t.Fatalf("unexpected command error: %#v", result)
	}
	if result.Command != binary+" check -c "+configPath {
		t.Fatalf("unexpected command string: %#v", result)
	}
	if strings.Join(result.Lines, "\n") != result.Command {
		t.Fatalf("unexpected output lines: %#v", result.Lines)
	}
}

func TestRuntimeAndDeviceCommandsExposeExpectedKeys(t *testing.T) {
	exec := func(name string, args ...string) (string, error) {
		return name + " " + strings.Join(args, " "), nil
	}

	runtime := RuntimeCommands(10, exec)
	for _, key := range []string{"iptables_save_mangle", "ip6tables_nat", "ip_rule", "ip_route_2024_v6", "listeners_ss"} {
		if runtime[key].Command == "" {
			t.Fatalf("missing runtime command %s in %#v", key, runtime)
		}
	}

	device := DeviceCommands(10, exec)
	for _, key := range []string{"model", "android_sdk", "selinux", "magisk", "ksu", "apatch"} {
		if device[key].Command == "" {
			t.Fatalf("missing device command %s in %#v", key, device)
		}
	}
}
