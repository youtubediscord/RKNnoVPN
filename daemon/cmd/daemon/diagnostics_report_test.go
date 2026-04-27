package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestDiagnosticRedactsSensitiveJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "singbox.json")
	raw := []byte(`{
		"outbounds": [{
			"server": "proxy.example.com",
			"server_port": 443,
			"uuid": "00000000-0000-0000-0000-000000000000",
			"password": "secret",
			"pre_shared_key": "psk-secret",
			"tls": {"server_name": "cdn.example.com", "reality": {"public_key": "pubsecretvalue", "short_id": "sidsecretvalue"}}
		}]
	}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}

	section := readRedactedJSONFile(path)
	if section.Error != "" {
		t.Fatalf("unexpected parse error: %s", section.Error)
	}
	text := redactDiagnosticText(mustMarshalForTest(t, section.Value))
	for _, secret := range []string{"00000000-0000-0000-0000-000000000000", "secret", "psk-secret", "pubsecretvalue", "sidsecretvalue"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret %q was not redacted from %s", secret, text)
		}
	}
	for _, diagnostic := range []string{`"server":"proxy.example.com"`, `"server_port":443`, `"server_name":"cdn.example.com"`} {
		if !strings.Contains(text, diagnostic) {
			t.Fatalf("diagnostic endpoint field %s should remain available, got %s", diagnostic, text)
		}
	}
}

func TestSupportedRPCMethodsAdvertiseCanonicalContract(t *testing.T) {
	methods := supportedRPCMethods()
	if !slices.Equal(methods, ipc.SupportedMethods()) {
		t.Fatalf("supported methods drifted from IPC contract:\nmethods=%#v\ncontract=%#v", methods, ipc.SupportedMethods())
	}
	for _, method := range []string{"diagnostics.report", "config-import", "backend.reset", "diagnostics.testNodes", "self-check", "ipc.contract", "profile.get", "profile.apply", "profile.importNodes", "profile.setActiveNode", "subscription.preview", "subscription.refresh"} {
		if !slices.Contains(methods, method) {
			t.Fatalf("supported methods missing %s: %#v", method, methods)
		}
	}
	for _, method := range []string{"config.import", "config-get", "config-set", "config-set-many", "network.reset", "node.test", "panel-get", "panel-set", "self.check", "status", "start", "stop", "subscription-fetch", "reload", "health"} {
		if slices.Contains(methods, method) {
			t.Fatalf("supported methods must not advertise legacy alias %s: %#v", method, methods)
		}
	}
}

func TestRegisteredHandlersMatchIPCContract(t *testing.T) {
	server := ipc.NewServer(filepath.Join(t.TempDir(), "daemon.sock"))
	d := &daemon{ipcServer: server}
	if err := d.registerHandlers(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(server.Methods(), ipc.SupportedMethods()) {
		t.Fatalf("registered handlers drifted from IPC contract:\nregistered=%#v\ncontract=%#v", server.Methods(), ipc.SupportedMethods())
	}
}

func TestDaemonctlCommandsMatchIPCContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "daemonctl", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := quotedMapKeysInVar(string(data), "commands")
	if !slices.Equal(got, ipc.SupportedMethods()) {
		t.Fatalf("daemonctl command table drifted from IPC contract:\ndaemonctl=%#v\ncontract=%#v", got, ipc.SupportedMethods())
	}
}

func TestKotlinRequiredMethodsStayWithinIPCContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "app", "app", "src", "main", "kotlin", "com", "rknnovpn", "panel", "ipc", "DaemonClient.kt"))
	if err != nil {
		t.Fatal(err)
	}
	required := quotedSetValuesInKotlinVar(string(data), "REQUIRED_METHODS")
	if len(required) == 0 {
		t.Fatalf("Kotlin REQUIRED_METHODS was not found")
	}
	contract := ipc.SupportedMethods()
	for _, method := range required {
		if !slices.Contains(contract, method) {
			t.Fatalf("Kotlin REQUIRED_METHODS contains method outside IPC contract: %s", method)
		}
	}
	for _, method := range []string{"backend.status", "profile.get", "profile.apply", "profile.importNodes", "profile.setActiveNode", "subscription.preview", "subscription.refresh", "ipc.contract", "version"} {
		if !slices.Contains(required, method) {
			t.Fatalf("Kotlin REQUIRED_METHODS missing APK-used contract method %s: %#v", method, required)
		}
	}
}

func quotedMapKeysInVar(source string, varName string) []string {
	block := extractInitializerBlock(source, varName)
	re := regexp.MustCompile(`(?m)^\s*"([^"]+)"\s*:`)
	matches := re.FindAllStringSubmatch(block, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		values = append(values, match[1])
	}
	slices.Sort(values)
	return values
}

func quotedSetValuesInKotlinVar(source string, varName string) []string {
	block := extractInitializerBlock(source, varName)
	re := regexp.MustCompile(`"([^"]+)"`)
	matches := re.FindAllStringSubmatch(block, -1)
	values := make([]string, 0, len(matches))
	for _, match := range matches {
		values = append(values, match[1])
	}
	slices.Sort(values)
	return values
}

func extractInitializerBlock(source string, varName string) string {
	start := strings.Index(source, varName)
	if start < 0 {
		return ""
	}
	open := strings.IndexAny(source[start:], "({")
	if open < 0 {
		return ""
	}
	open += start
	closeChar := byte('}')
	if source[open] == '(' {
		closeChar = ')'
	}
	depth := 0
	for i := open; i < len(source); i++ {
		switch source[i] {
		case source[open]:
			depth++
		case closeChar:
			depth--
			if depth == 0 {
				return source[open : i+1]
			}
		}
	}
	return source[open:]
}

func TestSupportedCapabilitiesAdvertiseSchemaAndDiagnostics(t *testing.T) {
	caps := supportedCapabilities()
	for _, capability := range []string{"backend.reset.warnings.v1", "config.mutation.envelope.v1", "config.schema.v5", "diagnostics.bundle.v2", "diagnostics.testNodes.tcp-direct", "ipc.envelope.v1", "netstack.runtime.verify.v1", "netstack.verify.v1", "profile.apply.v2", "profile.document.v2", "profile.importNodes.v2", "profile.subscription.v2", "privacy.localhost-listeners.v1", "privacy.loopback-dns.v1", "privacy.self-check.v1", "privacy.self-test-protected-apps.v1", "privacy.vpn-interface-patterns.v1", "runtime.logs"} {
		if !slices.Contains(caps, capability) {
			t.Fatalf("supported capabilities missing %s: %#v", capability, caps)
		}
	}
}

func TestDiagnosticKeepsNodeProbeEndpointMetadata(t *testing.T) {
	value := redactNodeProbeResults([]runtimev2.NodeProbeResult{
		{
			ID:     "node-1",
			Name:   "secret.example.com",
			Server: "secret.example.com",
			Port:   443,
		},
	})
	text := mustMarshalForTest(t, value)
	for _, diagnostic := range []string{`"name":"secret.example.com"`, `"server":"secret.example.com"`, `"port":443`} {
		if !strings.Contains(text, diagnostic) {
			t.Fatalf("node probe diagnostic field %s should remain available, got %s", diagnostic, text)
		}
	}
}

func TestDiagnosticSummaryFlagsTCPOnlyAndLeftovers(t *testing.T) {
	summary := diagnostics.BuildSummary(
		Version,
		controlProtocolVersion,
		runtimev2.HealthSnapshot{
			CoreReady:    true,
			RoutingReady: true,
			DNSReady:     true,
			EgressReady:  false,
			LastCode:     "OUTBOUND_URL_FAILED",
			LastError:    "URL probe failed",
			CheckedAt:    time.Now(),
		},
		[]string{"iptables mangle rule remains"},
		netstack.Report{},
		[]runtimev2.NodeProbeResult{
			{TCPStatus: "ok", URLStatus: "fail", Verdict: "unusable"},
		},
		nil,
		map[string]interface{}{"checks": map[string]interface{}{"helper_inbounds_disabled": true}},
		map[string]string{"version": "v1.6.4"},
		diagnosticCommandResult{},
		diagnosticReleaseIntegrity{OK: true, MissingCurrent: true},
		diagnosticProfileSummary{},
		diagnosticRoutingSummary{},
		diagnosticPackageResolution{},
	)

	if summary.Status != "partial_reset" {
		t.Fatalf("leftovers should produce partial_reset summary, got %#v", summary)
	}
	if !summary.RebootRequired {
		t.Fatalf("leftovers should request reboot in summary")
	}
	if summary.NodeTests.TCPOnly != 1 {
		t.Fatalf("expected TCP-only node count, got %#v", summary.NodeTests)
	}
	if !strings.Contains(strings.Join(summary.Issues, "\n"), "TCP reachability") {
		t.Fatalf("expected TCP-only issue, got %#v", summary.Issues)
	}
}

func TestDiagnosticSummaryFlagsPrivacyFailures(t *testing.T) {
	summary := diagnostics.BuildSummary(
		Version,
		controlProtocolVersion,
		runtimev2.HealthSnapshot{CoreReady: true, RoutingReady: true, DNSReady: true, EgressReady: true},
		nil,
		netstack.Report{},
		nil,
		[]diagnosticPortStatus{{Port: 10809, TCPListening: true}},
		map[string]interface{}{"checks": map[string]interface{}{
			"helper_inbounds_disabled":    false,
			"localhost_proxy_ports_clear": true,
			"system_proxy_unset":          true,
		}},
		map[string]string{"version": "v1.6.4"},
		diagnosticCommandResult{},
		diagnosticReleaseIntegrity{OK: true, MissingCurrent: true},
		diagnosticProfileSummary{},
		diagnosticRoutingSummary{},
		diagnosticPackageResolution{},
	)

	if summary.Status != "degraded" {
		t.Fatalf("privacy issue should degrade summary, got %#v", summary)
	}
	if len(summary.PrivacyIssues) == 0 {
		t.Fatalf("expected privacy issues, got %#v", summary)
	}
}

func TestDiagnosticLoopbackDNSDetection(t *testing.T) {
	if !diagnosticLinesContainLoopbackDNS([]string{
		"LinkProperties{DnsAddresses: [/127.0.0.1,/8.8.8.8]}",
	}) {
		t.Fatalf("expected IPv4 loopback DNS to be detected")
	}
	if got := diagnosticFirstLoopbackDNSLine([]string{
		"LinkProperties{DnsAddresses: [/127.0.0.1,/8.8.8.8]}",
	}); !strings.Contains(got, "127.0.0.1") {
		t.Fatalf("expected first loopback DNS line, got %q", got)
	}
	if !diagnosticLinesContainLoopbackDNS([]string{
		"mDnses: [ /::1 ]",
	}) {
		t.Fatalf("expected IPv6 loopback DNS to be detected")
	}
	if diagnosticLinesContainLoopbackDNS([]string{
		"LinkProperties{DnsAddresses: [/1.1.1.1,/8.8.8.8]}",
		"localhost proxy port is not a DNS line",
	}) {
		t.Fatalf("non-loopback DNS should stay clean")
	}
}

func TestDiagnosticVPNLikeInterfaceDetection(t *testing.T) {
	if got := firstVPNLikeInterfaceLine([]string{
		"2: wlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500",
		"9: wgcf: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420",
	}); !strings.Contains(got, "wgcf") {
		t.Fatalf("expected WireGuard-like interface line, got %q", got)
	}
	if got := firstVPNLikeInterfaceLine([]string{
		"2: wlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500",
		"3: rmnet_data0: <UP,LOWER_UP> mtu 1500",
	}); got != "" {
		t.Fatalf("non-VPN interfaces should stay clean, got %q", got)
	}
	if name := ipLinkInterfaceName("7: tun1@if5: <POINTOPOINT> mtu 1500"); name != "tun1" {
		t.Fatalf("unexpected interface name %q", name)
	}
}

func TestDiagnosticLocalhostProxyPortsUseConfiguredPorts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.APIPort = 19090
	cfg.Profile.Inbounds = json.RawMessage(`{"socksPort":19080,"httpPort":19081}`)

	if !diagnosticLocalhostProxyPortsClear(cfg) {
		t.Fatalf("unused configured ports should be clear")
	}
}

func TestDiagnosticSummaryFlagsReleaseIntegrityMismatch(t *testing.T) {
	summary := diagnostics.BuildSummary(
		Version,
		controlProtocolVersion,
		runtimev2.HealthSnapshot{CoreReady: true, RoutingReady: true, DNSReady: true, EgressReady: true},
		nil,
		netstack.Report{},
		nil,
		nil,
		map[string]interface{}{"checks": map[string]interface{}{}},
		map[string]string{"version": "v1.6.4"},
		diagnosticCommandResult{},
		diagnosticReleaseIntegrity{
			CurrentPath:  "/data/adb/modules/rknnovpn/current",
			ReleasePath:  "/data/adb/modules/rknnovpn/releases/v1.6.4",
			Version:      "v1.6.4",
			CheckedFiles: 2,
			Mismatches:   []string{"bin/daemon"},
		},
		diagnosticProfileSummary{},
		diagnosticRoutingSummary{},
		diagnosticPackageResolution{},
	)

	if summary.Status != "degraded" {
		t.Fatalf("release integrity issue should degrade summary, got %#v", summary)
	}
	if !strings.Contains(strings.Join(summary.CompatibilityIssues, "\n"), "checksum") {
		t.Fatalf("expected checksum compatibility issue, got %#v", summary.CompatibilityIssues)
	}
}

func TestDiagnosticSummaryFlagsRuntimeNetstackFailure(t *testing.T) {
	summary := diagnostics.BuildSummary(
		Version,
		controlProtocolVersion,
		runtimev2.HealthSnapshot{CoreReady: true, RoutingReady: true, DNSReady: true, EgressReady: true},
		nil,
		netstack.Report{
			Operation: "verify",
			Status:    "failed",
			Errors:    []string{"iptables-status failed"},
		},
		nil,
		nil,
		map[string]interface{}{"checks": map[string]interface{}{}},
		map[string]string{"version": "v1.6.4"},
		diagnosticCommandResult{},
		diagnosticReleaseIntegrity{OK: true, MissingCurrent: true},
		diagnosticProfileSummary{},
		diagnosticRoutingSummary{},
		diagnosticPackageResolution{},
	)

	if summary.Status != "failed" {
		t.Fatalf("runtime netstack failure should fail summary, got %#v", summary)
	}
	if !strings.Contains(strings.Join(summary.Issues, "\n"), "runtime netstack") {
		t.Fatalf("expected runtime netstack issue, got %#v", summary.Issues)
	}
}

func TestDiagnosticRoutingSummaryDetectsAutoSelector(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Profile.Nodes = []json.RawMessage{
		json.RawMessage(`{"id":"node-a","name":"A","protocol":"vless","group":"EU"}`),
		json.RawMessage(`{"id":"node-b","name":"B","protocol":"trojan","group":"US"}`),
	}
	cfg.Apps.AppGroups = map[string]string{"org.telegram.messenger": "EU"}

	summary := diagnostics.RoutingSummaryFromConfig(cfg)

	if summary.ActiveNodeMode != "auto_selector" {
		t.Fatalf("expected auto selector mode, got %#v", summary)
	}
	if summary.NodeCount != 2 || summary.ActiveNodeProtocol != "selector" {
		t.Fatalf("unexpected routing summary: %#v", summary)
	}
	if summary.AppGroupRouteCount != 1 {
		t.Fatalf("expected one app group route, got %#v", summary)
	}
	if strings.Join(summary.Groups, ",") != "EU,US" {
		t.Fatalf("expected sorted groups, got %#v", summary.Groups)
	}
}

func TestDiagnosticPortStatusesIncludeRoles(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.APIPort = 9090
	cfg.Profile.Inbounds = json.RawMessage(`{"socksPort":10808,"httpPort":10809}`)

	roles := diagnosticLocalPortRoles(cfg)

	for port, role := range map[int]string{
		10853: "tproxy",
		10856: "dns",
		9090:  "clash_api",
		10808: "socks_helper",
		10809: "http_helper",
	} {
		if !slices.Contains(roles[port], role) {
			t.Fatalf("expected %s role for port %d, got %#v", role, port, roles)
		}
	}
}

func TestDiagnosticPortConflictsDetectDuplicateConfiguredPorts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.TProxyPort = 10853
	cfg.Proxy.DNSPort = 10853
	cfg.Proxy.APIPort = 19090
	cfg.Profile.Inbounds = json.RawMessage(`{"socksPort":19091,"httpPort":19090}`)

	conflicts := diagnosticLocalPortConflicts(cfg)
	if len(conflicts) != 2 {
		t.Fatalf("expected two duplicate local port conflicts, got %#v", conflicts)
	}
	if conflicts[0].Port != 10853 || strings.Join(conflicts[0].Roles, ",") != "dns,tproxy" {
		t.Fatalf("unexpected tproxy/dns conflict: %#v", conflicts)
	}
	if conflicts[1].Port != 19090 || strings.Join(conflicts[1].Roles, ",") != "clash_api,http_helper" {
		t.Fatalf("unexpected api/http conflict: %#v", conflicts)
	}

	statuses := diagnosticPortStatuses(cfg)
	found := false
	for _, status := range statuses {
		if status.Port == 10853 {
			found = status.Conflict && status.Role == "dns,tproxy"
		}
	}
	if !found {
		t.Fatalf("expected port status to expose conflict, got %#v", statuses)
	}
}

func TestDiagnosticSummaryFlagsPackageResolutionAndPortWarnings(t *testing.T) {
	summary := diagnostics.BuildSummary(
		Version,
		controlProtocolVersion,
		runtimev2.HealthSnapshot{CoreReady: true, RoutingReady: true, DNSReady: true, EgressReady: true},
		nil,
		netstack.Report{},
		nil,
		[]diagnosticPortStatus{{Role: "dns,tproxy", Port: 10853, Conflict: true}},
		map[string]interface{}{"checks": map[string]interface{}{}},
		map[string]string{"version": "v1.6.4"},
		diagnosticCommandResult{},
		diagnosticReleaseIntegrity{OK: true, MissingCurrent: true},
		diagnosticProfileSummary{},
		diagnosticRoutingSummary{},
		diagnosticPackageResolution{Warnings: []string{"per-app routing is enabled but selected packages resolved to zero UIDs"}},
	)

	if summary.Status != "degraded" {
		t.Fatalf("diagnostic warnings should degrade summary, got %#v", summary)
	}
	issues := strings.Join(summary.Issues, "\n")
	if !strings.Contains(issues, "conflicting roles") || !strings.Contains(issues, "zero UIDs") {
		t.Fatalf("expected port and package resolution issues, got %#v", summary.Issues)
	}
}

func TestDiagnosticPortRolesDoNotExpectDisabledLocalHelpers(t *testing.T) {
	cfg := config.DefaultConfig()

	roles := diagnosticLocalPortRoles(cfg)
	for _, port := range []int{10808, 10809, 9090} {
		if len(roles[port]) != 0 {
			t.Fatalf("disabled localhost helper/API port %d must not have diagnostics report roles: %#v", port, roles)
		}
	}
}

func TestDiagnosticNetstackReportHandlesMissingConfig(t *testing.T) {
	d := &daemon{dataDir: t.TempDir()}

	report := d.diagnosticNetstackReport(nil)
	if report.Status != "failed" {
		t.Fatalf("expected failed report for missing config, got %#v", report)
	}
	if len(report.Leftovers) != 1 || !strings.Contains(report.Leftovers[0], "config unavailable") {
		t.Fatalf("expected config unavailable leftover, got %#v", report.Leftovers)
	}
}

func TestDiagnosticRuntimeNetstackReportSkipsWhenStopped(t *testing.T) {
	cfg := config.DefaultConfig()
	d := &daemon{
		dataDir: t.TempDir(),
		coreMgr: core.NewCoreManager(cfg, t.TempDir(), nil),
	}

	report := d.diagnosticNetstackRuntimeReport(cfg)
	if report.Status != "skipped" {
		t.Fatalf("stopped runtime should skip netstack verify, got %#v", report)
	}
}

func TestDiagnosticReleaseIntegrityReportDetectsMismatch(t *testing.T) {
	dataDir := t.TempDir()
	releaseDir := filepath.Join(dataDir, "releases", "v1.6.4")
	if err := os.MkdirAll(filepath.Join(releaseDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "bin", "daemon"), []byte("changed\n"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":"v1.6.4","installed_at":"2026-04-24T00:00:00Z","files_sha256":{"bin/daemon":"0000"}}`
	if err := os.WriteFile(filepath.Join(releaseDir, "install-manifest.json"), []byte(manifest), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(releaseDir, filepath.Join(dataDir, "current")); err != nil {
		t.Fatal(err)
	}

	report := diagnosticReleaseIntegrityReport(dataDir)
	if report.OK {
		t.Fatalf("expected mismatch report, got %#v", report)
	}
	if len(report.Mismatches) != 1 {
		t.Fatalf("expected one mismatch, got %#v", report)
	}
}

func TestDiagnosticReleaseIntegrityReportFlagsMissingManifest(t *testing.T) {
	dataDir := t.TempDir()
	releaseDir := filepath.Join(dataDir, "releases", "v1.7.9")
	if err := os.MkdirAll(filepath.Join(releaseDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(releaseDir, filepath.Join(dataDir, "current")); err != nil {
		t.Fatal(err)
	}

	report := diagnosticReleaseIntegrityReport(dataDir)
	if report.OK || !report.MissingManifest {
		t.Fatalf("missing manifest should fail release integrity, got %#v", report)
	}
	if report.Version != "v1.7.9" {
		t.Fatalf("expected version inferred from release path, got %q", report.Version)
	}
	if issues := diagnosticReleaseIntegrityIssues(report); len(issues) == 0 {
		t.Fatalf("missing manifest should create a compatibility issue")
	}
}

func mustMarshalForTest(t *testing.T, value interface{}) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
