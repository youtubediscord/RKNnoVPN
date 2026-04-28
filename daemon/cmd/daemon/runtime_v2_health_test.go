package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/audit"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func TestBuildRuntimeV2HealthSnapshotSeparatesOperationalFailures(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), nil)
	manager.SetState(core.StateRunning)
	d := &daemon{coreMgr: manager}

	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   true,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: true, Detail: "listening"},
			"iptables":      {Pass: true, Detail: "iptables"},
			"routing":       {Pass: true, Detail: "routing"},
			"dns":           {Pass: false, Detail: "dns timeout", Code: "DNS_LOOKUP_TIMEOUT"},
		},
	}

	snapshot := d.buildRuntimeV2HealthSnapshot(result, false)
	if !snapshot.Healthy() {
		t.Fatalf("readiness should be healthy with only DNS red: %#v", snapshot)
	}
	if snapshot.OperationalHealthy() {
		t.Fatalf("operational health should be red when DNS and egress are red: %#v", snapshot)
	}
	if !strings.Contains(snapshot.LastError, "operational degraded") {
		t.Fatalf("expected operational degraded diagnostic, got %q", snapshot.LastError)
	}
	if snapshot.LastCode != "DNS_LOOKUP_TIMEOUT" {
		t.Fatalf("expected stable DNS failure code, got %q", snapshot.LastCode)
	}
	if got := snapshot.Checks["dns"].Code; got != "DNS_LOOKUP_TIMEOUT" {
		t.Fatalf("expected DNS check code in structured checks, got %q", got)
	}
}

func TestBuildRuntimeV2HealthSnapshotIncludesLatestStageReport(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), nil)
	manager.SetState(core.StateRunning)
	d := &daemon{coreMgr: manager}

	report := core.NewRuntimeStageReport("apply config")
	report.AddStage("wait-dns", "failed", "DNS_LISTENER_DOWN", "dns listener down", false)
	d.setLastReloadReport(report)

	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   false,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: true, Detail: "listening"},
			"iptables":      {Pass: true, Detail: "iptables"},
			"routing":       {Pass: true, Detail: "routing"},
			"dns_listener":  {Pass: false, Detail: "listener down", Code: "DNS_LISTENER_DOWN"},
		},
	}

	snapshot := d.buildRuntimeV2HealthSnapshot(result, false)
	stageReport, ok := snapshot.StageReport.(core.RuntimeStageReport)
	if !ok {
		t.Fatalf("expected core stage report, got %T", snapshot.StageReport)
	}
	if stageReport.FailedStage != "wait-dns" || stageReport.LastCode != "DNS_LISTENER_DOWN" {
		t.Fatalf("unexpected stage report: %#v", stageReport)
	}
}

func TestBuildRuntimeV2HealthSnapshotUsesSuccessfulStageBeforeFirstHealth(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), nil)
	manager.SetState(core.StateRunning)
	d := &daemon{coreMgr: manager}

	report := core.NewRuntimeStageReport("start")
	report.AddStage("commit-state", "ok", "", "vless://example.com", false)
	report.FinishOK()
	d.setLastReloadReport(report)

	snapshot := d.buildRuntimeV2HealthSnapshot(nil, false)
	if !snapshot.CoreReady || !snapshot.RoutingReady {
		t.Fatalf("successful stage report should keep hard readiness green before first health result: %#v", snapshot)
	}
	if snapshot.DNSReady || snapshot.EgressReady {
		t.Fatalf("soft readiness should not be invented before health probes: %#v", snapshot)
	}
}

func TestClassifyRuntimeV2HealthUsesPureGateClassification(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   false,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: true, Detail: "listening"},
			"iptables":      {Pass: true, Detail: "iptables"},
			"routing":       {Pass: true, Detail: "routing"},
			"dns_listener":  {Pass: false, Detail: "listener down", Code: "DNS_LISTENER_DOWN"},
			"dns":           {Pass: false, Detail: "dns timeout", Code: "DNS_LOOKUP_TIMEOUT"},
			"outbound_url":  {Pass: false, Detail: "url timeout", Code: "OUTBOUND_URL_FAILED"},
		},
	}

	snapshot := rootruntime.ClassifyHealth(rootruntime.HealthInput{
		State:        core.StateRunning,
		Result:       result,
		RecentEgress: true,
		CheckedAt:    time.Now(),
	})

	if !snapshot.Healthy() {
		t.Fatalf("readiness must ignore DNS and outbound failures: %#v", snapshot)
	}
	if snapshot.OperationalHealthy() {
		t.Fatalf("operational health must include DNS and egress gates: %#v", snapshot)
	}
	if snapshot.DNSReady || snapshot.EgressReady {
		t.Fatalf("DNS and egress gates should be red: %#v", snapshot)
	}
	if snapshot.LastCode != "DNS_LISTENER_DOWN" {
		t.Fatalf("DNS listener must be the stable first operational code, got %#v", snapshot)
	}
	if got := snapshot.Checks["outbound_url"].Code; got != "OUTBOUND_URL_FAILED" {
		t.Fatalf("expected structured outbound check code, got %q", got)
	}
}

func TestClassifyRuntimeV2HealthReadinessPriorityBeatsOperationalPriority(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   false,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: false, Detail: "port closed", Code: "TPROXY_PORT_DOWN"},
			"iptables":      {Pass: false, Detail: "iptables missing", Code: "RULES_NOT_APPLIED"},
			"routing":       {Pass: false, Detail: "routing missing", Code: "ROUTING_NOT_APPLIED"},
			"dns_listener":  {Pass: false, Detail: "listener down", Code: "DNS_LISTENER_DOWN"},
			"outbound_url":  {Pass: false, Detail: "url timeout", Code: "OUTBOUND_URL_FAILED"},
		},
	}

	snapshot := rootruntime.ClassifyHealth(rootruntime.HealthInput{
		State:     core.StateRunning,
		Result:    result,
		CheckedAt: time.Now(),
	})

	if snapshot.Healthy() {
		t.Fatalf("readiness must be red when tproxy is down: %#v", snapshot)
	}
	if snapshot.LastCode != "TPROXY_PORT_DOWN" {
		t.Fatalf("readiness priority should beat operational failures, got %#v", snapshot)
	}
}

func TestClassifyRuntimeV2HealthUsesRecentEgressWhenURLProbeMissing(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   true,
		Checks: map[string]health.CheckResult{
			"singbox_alive": {Pass: true, Detail: "alive"},
			"tproxy_port":   {Pass: true, Detail: "listening"},
			"iptables":      {Pass: true, Detail: "iptables"},
			"routing":       {Pass: true, Detail: "routing"},
			"dns_listener":  {Pass: true, Detail: "dns listener"},
			"dns":           {Pass: true, Detail: "dns"},
		},
	}

	snapshot := rootruntime.ClassifyHealth(rootruntime.HealthInput{
		State:        core.StateRunning,
		Result:       result,
		RecentEgress: true,
		CheckedAt:    time.Now(),
	})

	if !snapshot.OperationalHealthy() {
		t.Fatalf("recent egress should satisfy operational health when URL probe is absent: %#v", snapshot)
	}
	if snapshot.LastCode != "" || snapshot.LastError != "" {
		t.Fatalf("healthy snapshot should not carry failure diagnostics: %#v", snapshot)
	}
}

func TestFirstFailedGateUsesDeterministicReadinessPriority(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   false,
		Checks: map[string]health.CheckResult{
			"dns":           {Pass: false, Detail: "dns timeout"},
			"routing":       {Pass: false, Detail: "routing missing"},
			"iptables":      {Pass: false, Detail: "iptables missing"},
			"tproxy_port":   {Pass: false, Detail: "port closed"},
			"singbox_alive": {Pass: false, Detail: "pid missing"},
		},
	}

	got := rootruntime.FirstFailedGateDiagnostic(result, runtimev2.HealthSnapshot{}).Detail
	if !strings.HasPrefix(got, "singbox_alive:") {
		t.Fatalf("expected singbox_alive first, got %q", got)
	}
}

func TestFirstFailedGateCodeUsesDeterministicReadinessPriority(t *testing.T) {
	result := &health.HealthResult{
		Timestamp: time.Now(),
		Overall:   false,
		Checks: map[string]health.CheckResult{
			"dns":           {Pass: false, Detail: "dns timeout", Code: "DNS_LOOKUP_TIMEOUT"},
			"routing":       {Pass: false, Detail: "routing missing", Code: "ROUTING_NOT_APPLIED"},
			"iptables":      {Pass: false, Detail: "iptables missing", Code: "RULES_NOT_APPLIED"},
			"tproxy_port":   {Pass: false, Detail: "port closed", Code: "TPROXY_PORT_DOWN"},
			"singbox_alive": {Pass: false, Detail: "pid missing", Code: "CORE_PID_MISSING"},
		},
	}

	got := rootruntime.FirstFailedGateDiagnostic(result, runtimev2.HealthSnapshot{})
	if got.Code != "CORE_PID_MISSING" {
		t.Fatalf("expected CORE_PID_MISSING first, got %#v", got)
	}
}

func TestPortProtectionOutputRequiresProtocolAndDropRule(t *testing.T) {
	output := strings.Join([]string{
		"-A RKNNOVPN_OUT -p tcp -m tcp --dport 10853 -m owner ! --uid-owner 0 ! --gid-owner 23333 -j DROP",
		"-A RKNNOVPN_OUT -p udp -m udp --dport 10853 -m owner ! --uid-owner 0 ! --gid-owner 23333 -j DROP",
		"-A RKNNOVPN_OUT -p tcp -m tcp --dport 10856 -j RETURN",
	}, "\n")

	if !audit.PortProtectionOutputContains(output, "tcp", 10853) {
		t.Fatalf("expected TCP protection rule to be detected")
	}
	if !audit.PortProtectionOutputContains(output, "udp", 10853) {
		t.Fatalf("expected UDP protection rule to be detected")
	}
	if audit.PortProtectionOutputContains(output, "udp", 10856) {
		t.Fatalf("RETURN-only DNS rule must not count as listener protection")
	}
	if audit.PortProtectionOutputContains(output, "tcp", 10854) {
		t.Fatalf("wrong port must not count as listener protection")
	}
}

func TestClassifyRuntimeURLTestFailureUsesLastHealthCode(t *testing.T) {
	base := runtimev2.HealthSnapshot{
		CoreReady:    true,
		RoutingReady: true,
		DNSReady:     true,
		EgressReady:  false,
		CheckedAt:    time.Now(),
	}
	base.LastCode = "OUTBOUND_URL_FAILED"
	if got := rootruntime.ClassifyURLTestFailure(errors.New("timeout"), base); got != "outbound_url_failed" {
		t.Fatalf("expected outbound_url_failed, got %q", got)
	}
	base.LastCode = "DNS_LOOKUP_TIMEOUT"
	if got := rootruntime.ClassifyURLTestFailure(errors.New("timeout"), base); got != "proxy_dns_unavailable" {
		t.Fatalf("expected proxy_dns_unavailable, got %q", got)
	}
	base.LastCode = "RULES_NOT_APPLIED"
	if got := rootruntime.ClassifyURLTestFailure(errors.New("timeout"), base); got != "runtime_not_ready" {
		t.Fatalf("expected runtime_not_ready, got %q", got)
	}
}

func TestClassifyURLTestFailureUsesConcreteURLCause(t *testing.T) {
	base := runtimev2.HealthSnapshot{
		CoreReady:    true,
		RoutingReady: true,
		DNSReady:     true,
		EgressReady:  true,
		CheckedAt:    time.Now(),
	}
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("api_disabled"), "api_disabled"},
		{errors.New("Get http://127.0.0.1:9090/proxies/node/delay: connect: connection refused"), "api_unavailable"},
		{errors.New("clash delay HTTP 404: proxy not found"), "outbound_missing"},
		{errors.New("remote error: tls: handshake failure"), "tls_handshake_failed"},
		{errors.New("lookup example.com: no such host"), "proxy_dns_unavailable"},
	}
	for _, tc := range cases {
		if got := rootruntime.ClassifyURLTestFailure(tc.err, base); got != tc.want {
			t.Fatalf("expected %q for %v, got %q", tc.want, tc.err, got)
		}
	}
}
