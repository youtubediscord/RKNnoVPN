package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func TestParsePackagesListUIDs(t *testing.T) {
	uids, err := parsePackagesListUIDs(`
com.example.app 10123 0 /data/user/0/com.example.app default 3003
bad-line
com.example.other not-a-uid
`)
	if err != nil {
		t.Fatal(err)
	}
	if uids["com.example.app"] != 10123 {
		t.Fatalf("expected parsed UID, got %#v", uids)
	}
	if _, ok := uids["com.example.other"]; ok {
		t.Fatalf("invalid UID entry should be ignored: %#v", uids)
	}
}

func TestParseCmdPackageUIDs(t *testing.T) {
	uids, err := parseCmdPackageUIDs(`
package:com.example.app uid:10123
package:com.example.other uid:10124
`)
	if err != nil {
		t.Fatal(err)
	}
	if uids["com.example.app"] != 10123 || uids["com.example.other"] != 10124 {
		t.Fatalf("unexpected cmd package parse result: %#v", uids)
	}
}

func TestResolvePackageUIDsFallsBackWhenPackagesListMissing(t *testing.T) {
	withPackageResolverTestEnv(t, "", func(asShell bool) (string, error) {
		if asShell {
			return "", errors.New("shell fallback should not be needed")
		}
		return "package:com.example.app uid:10123", nil
	})

	result := ResolvePackageUIDsDetailed([]string{"com.example.app"})
	if result.Source != "cmd_package" {
		t.Fatalf("expected cmd_package fallback, got %#v", result)
	}
	if result.UIDString != "10123" {
		t.Fatalf("expected resolved UID, got %#v", result)
	}
}

func TestResolvePackageUIDsFallsBackToShellCommand(t *testing.T) {
	withPackageResolverTestEnv(t, "", func(asShell bool) (string, error) {
		if !asShell {
			return "", errors.New("cmd denied")
		}
		return "package:com.example.app uid:10123", nil
	})

	result := ResolvePackageUIDsDetailed([]string{"com.example.app"})
	if result.Source != "cmd_package_shell" {
		t.Fatalf("expected cmd_package_shell fallback, got %#v", result)
	}
	if result.UIDString != "10123" {
		t.Fatalf("expected resolved UID, got %#v", result)
	}
}

func TestResolvePackageUIDsFallsBackWhenPackagesListIsStale(t *testing.T) {
	withPackageResolverTestEnv(t, "com.example.old 10111 0 /data/user/0/com.example.old default\n", func(asShell bool) (string, error) {
		if asShell {
			return "", errors.New("shell fallback should not be needed")
		}
		return "package:com.example.app uid:10123", nil
	})

	result := ResolvePackageUIDsDetailed([]string{"com.example.app"})
	if result.Source != "cmd_package" {
		t.Fatalf("expected stale packages.list to fall back to cmd_package, got %#v", result)
	}
	if result.UIDString != "10123" || len(result.UnresolvedPackages) != 0 {
		t.Fatalf("expected resolved package, got %#v", result)
	}
}

func TestResolvePackageUIDsExpandsAndroidUsers(t *testing.T) {
	withPackageResolverTestEnv(t, "com.example.app 10123 0 /data/user/0/com.example.app default\n", func(bool) (string, error) {
		return "", errors.New("fallback should not be needed")
	})
	if err := os.Mkdir(filepath.Join(dataUserPath, "10"), 0755); err != nil {
		t.Fatal(err)
	}

	result := ResolvePackageUIDsDetailed([]string{"com.example.app"})
	if result.UIDString != "10123 1010123" {
		t.Fatalf("expected user 0 and user 10 UIDs, got %#v", result)
	}
}

func TestResolvePackageUIDsReportsUnresolvedPackages(t *testing.T) {
	withPackageResolverTestEnv(t, "com.example.other 10124 0 /data/user/0/com.example.other default\n", func(bool) (string, error) {
		return "", errors.New("cmd unavailable")
	})

	result := ResolvePackageUIDsDetailed([]string{"com.example.missing"})
	if result.UIDString != "" {
		t.Fatalf("missing package should not resolve UIDs, got %#v", result)
	}
	if strings.Join(result.UnresolvedPackages, ",") != "com.example.missing" {
		t.Fatalf("expected unresolved package report, got %#v", result)
	}
}

func TestBuildAppRoutingEnvModes(t *testing.T) {
	withPackageResolverTestEnv(t, "com.example.app 10123 0 /data/user/0/com.example.app default\n", func(bool) (string, error) {
		return "", errors.New("fallback should not be needed")
	})

	whitelist := BuildAppRoutingEnv("whitelist", []string{"com.example.app"}, nil)
	if whitelist.AppMode != "whitelist" || whitelist.ProxyUIDs != "10123" || whitelist.DirectUIDs != "" || whitelist.DNSScope != "uids" || whitelist.DNSMode != "per_uid" {
		t.Fatalf("unexpected whitelist env: %#v", whitelist)
	}

	blacklist := BuildAppRoutingEnv("blacklist", []string{"com.example.app"}, nil)
	if blacklist.AppMode != "blacklist" || blacklist.DirectUIDs != "10123" || blacklist.ProxyUIDs != "" || blacklist.DNSScope != "all_except_uids" || blacklist.DNSMode != "per_uid" {
		t.Fatalf("unexpected blacklist env: %#v", blacklist)
	}

	all := BuildAppRoutingEnv("all", []string{"com.example.app"}, nil)
	if all.AppMode != "all" || all.ProxyUIDs != "" || all.DirectUIDs != "" || all.DNSScope != "all" || all.DNSMode != "all" {
		t.Fatalf("unexpected all env: %#v", all)
	}

	off := BuildAppRoutingEnv("off", []string{"com.example.app"}, nil)
	if off.AppMode != "off" || off.ProxyUIDs != "" || off.DirectUIDs != "" || off.DNSScope != "off" || off.DNSMode != "off" {
		t.Fatalf("unexpected off env: %#v", off)
	}
}

func TestBuildRuntimeAppRoutingEnvDirectHardBypass(t *testing.T) {
	withPackageResolverTestEnv(t, "com.example.app 10123 0 /data/user/0/com.example.app default\n", func(bool) (string, error) {
		return "", errors.New("fallback should not be needed")
	})

	env := BuildRuntimeAppRoutingEnv("whitelist", []string{"com.example.app"}, []string{"com.android.vending"}, "direct")
	if env.AppMode != "off" || env.ProxyUIDs != "" || env.DirectUIDs != "" {
		t.Fatalf("direct routing must disable app interception, got %#v", env)
	}
	if env.DNSScope != "off" || env.DNSMode != "off" {
		t.Fatalf("direct routing must disable DNS interception, got %#v", env)
	}
	if env.BypassUIDs == "" {
		t.Fatalf("direct routing should preserve hard-bypass UID protection, got %#v", env)
	}
}

func TestBuildChainedProxyProtectionEnvAllowsMutualLocalProxyUIDs(t *testing.T) {
	withProcNetTCPTestEnv(t, `
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:2A38 00000000:0000 0A 00000000:00000000 00:00000000 00000000 10123 0 111 1 00000000
   1: 0100007F:2A39 00000000:0000 0A 00000000:00000000 00:00000000 00000000 10124 0 112 1 00000000
`, "")
	cfg := config.DefaultConfig()
	cfg.Panel.Nodes = jsonRawMessage(t, `{
		"id":"nekobox",
		"name":"NekoBox local",
		"protocol":"SOCKS",
		"server":"127.0.0.1",
		"port":10808,
		"outbound":{"protocol":"socks","settings":{"address":"127.0.0.1","port":10808}}
	}`, `{
		"id":"bbdpi",
		"name":"BBDPI local",
		"protocol":"SOCKS",
		"server":"localhost",
		"port":10809,
		"outbound":{"protocol":"socks","settings":{"address":"localhost","port":10809}}
	}`)

	ports, uids := BuildChainedProxyProtectionEnv(cfg)
	if ports != "10808 10809" {
		t.Fatalf("unexpected chain proxy ports: %q", ports)
	}
	if uids != "10123 10124" {
		t.Fatalf("unexpected chain proxy UIDs: %q", uids)
	}
}

func TestBuildChainedProxyProtectionEnvSkipsUnownedAndReservedPorts(t *testing.T) {
	withProcNetTCPTestEnv(t, `
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:2A38 00000000:0000 0A 00000000:00000000 00:00000000 00000000 10123 0 111 1 00000000
`, "")
	cfg := config.DefaultConfig()
	cfg.Proxy.APIPort = 10808
	cfg.Panel.Nodes = jsonRawMessage(t, `{
		"id":"api-duplicate",
		"protocol":"SOCKS",
		"server":"127.0.0.1",
		"port":10808,
		"outbound":{"protocol":"socks","settings":{"address":"127.0.0.1","port":10808}}
	}`, `{
		"id":"not-listening",
		"protocol":"SOCKS",
		"server":"127.0.0.1",
		"port":10809,
		"outbound":{"protocol":"socks","settings":{"address":"127.0.0.1","port":10809}}
	}`)

	ports, uids := BuildChainedProxyProtectionEnv(cfg)
	if ports != "" || uids != "" {
		t.Fatalf("reserved/unowned ports must stay fail-open, got ports=%q uids=%q", ports, uids)
	}
}

func withProcNetTCPTestEnv(t *testing.T, tcp string, tcp6 string) {
	t.Helper()
	oldFiles := procNetTCPFiles
	t.Cleanup(func() {
		procNetTCPFiles = oldFiles
	})
	dir := t.TempDir()
	paths := []string{}
	if tcp != "" {
		path := filepath.Join(dir, "tcp")
		if err := os.WriteFile(path, []byte(tcp), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if tcp6 != "" {
		path := filepath.Join(dir, "tcp6")
		if err := os.WriteFile(path, []byte(tcp6), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	procNetTCPFiles = paths
}

func jsonRawMessage(t *testing.T, values ...string) []json.RawMessage {
	t.Helper()
	raw := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		raw = append(raw, json.RawMessage(value))
	}
	return raw
}

func withPackageResolverTestEnv(t *testing.T, packagesList string, command func(bool) (string, error)) {
	t.Helper()
	oldPackageListPath := packageListPath
	oldDataUserPath := dataUserPath
	oldRunPackageUIDCommand := runPackageUIDCommand
	t.Cleanup(func() {
		packageListPath = oldPackageListPath
		dataUserPath = oldDataUserPath
		runPackageUIDCommand = oldRunPackageUIDCommand
	})

	tempDir := t.TempDir()
	dataUserPath = filepath.Join(tempDir, "user")
	if err := os.MkdirAll(dataUserPath, 0755); err != nil {
		t.Fatal(err)
	}
	packageListPath = filepath.Join(tempDir, "packages.list")
	if packagesList == "" {
		packageListPath = filepath.Join(tempDir, "missing-packages.list")
	} else if err := os.WriteFile(packageListPath, []byte(packagesList), 0644); err != nil {
		t.Fatal(err)
	}
	runPackageUIDCommand = command
}
