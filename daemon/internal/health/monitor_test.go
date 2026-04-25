package health

import (
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
)

func TestRunOnceTreatsDNSAsOperationalOnly(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), log.New(os.Stderr, "", 0))
	monitor := NewHealthMonitor(
		manager,
		time.Second,
		1,
		cfg.Proxy.TProxyPort,
		cfg.Proxy.DNSPort,
		cfg.Proxy.Mark,
		cfg.Health.URL,
		time.Second,
		log.New(os.Stderr, "", 0),
	)
	monitor.runProcessAliveCheck = func(pid int) CheckResult {
		return CheckResult{Pass: true, Detail: "alive"}
	}
	monitor.runPortListeningCheck = func(port int) CheckResult {
		return CheckResult{Pass: true, Detail: "listening"}
	}
	monitor.runIptablesCheck = func() CheckResult {
		return CheckResult{Pass: true, Detail: "iptables"}
	}
	monitor.runRoutingCheck = func() CheckResult {
		return CheckResult{Pass: true, Detail: "routing"}
	}
	monitor.runDNSCheck = func() CheckResult {
		return CheckResult{Pass: false, Detail: "dns timeout"}
	}

	result := monitor.RunOnce()
	if !result.Overall {
		t.Fatalf("DNS failure must not fail hard readiness: %#v", result)
	}
	if result.Checks["dns"].Pass {
		t.Fatalf("DNS check should still be reported as failed")
	}
}

func TestRunOnceFailsHardReadinessOnRoutingFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), log.New(os.Stderr, "", 0))
	monitor := NewHealthMonitor(
		manager,
		time.Second,
		1,
		cfg.Proxy.TProxyPort,
		cfg.Proxy.DNSPort,
		cfg.Proxy.Mark,
		cfg.Health.URL,
		time.Second,
		log.New(os.Stderr, "", 0),
	)
	monitor.runProcessAliveCheck = func(pid int) CheckResult {
		return CheckResult{Pass: true, Detail: "alive"}
	}
	monitor.runPortListeningCheck = func(port int) CheckResult {
		return CheckResult{Pass: true, Detail: "listening"}
	}
	monitor.runIptablesCheck = func() CheckResult {
		return CheckResult{Pass: true, Detail: "iptables"}
	}
	monitor.runRoutingCheck = func() CheckResult {
		return CheckResult{Pass: false, Detail: "routing missing"}
	}
	monitor.runDNSCheck = func() CheckResult {
		return CheckResult{Pass: true, Detail: "dns"}
	}

	result := monitor.RunOnce()
	if result.Overall {
		t.Fatalf("routing failure must fail hard readiness: %#v", result)
	}
}

func TestRunOnceCanPromoteDNSProbeToHardReadiness(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), log.New(os.Stderr, "", 0))
	monitor := NewHealthMonitor(
		manager,
		time.Second,
		1,
		cfg.Proxy.TProxyPort,
		cfg.Proxy.DNSPort,
		cfg.Proxy.Mark,
		cfg.Health.URL,
		time.Second,
		log.New(os.Stderr, "", 0),
	)
	monitor.SetConfig(time.Second, 1, cfg.Proxy.TProxyPort, cfg.Proxy.DNSPort, cfg.Proxy.Mark, cfg.Health.URL, cfg.Health.DNSProbeDomains, true, time.Second)
	monitor.runProcessAliveCheck = func(pid int) CheckResult {
		return CheckResult{Pass: true, Detail: "alive"}
	}
	monitor.runPortListeningCheck = func(port int) CheckResult {
		return CheckResult{Pass: true, Detail: "listening"}
	}
	monitor.runIptablesCheck = func() CheckResult {
		return CheckResult{Pass: true, Detail: "iptables"}
	}
	monitor.runRoutingCheck = func() CheckResult {
		return CheckResult{Pass: true, Detail: "routing"}
	}
	monitor.runDNSCheck = func() CheckResult {
		return CheckResult{Pass: false, Detail: "dns timeout", Code: "DNS_LOOKUP_TIMEOUT"}
	}

	result := monitor.RunOnce()
	if result.Overall {
		t.Fatalf("DNS failure must fail hard readiness when explicitly configured: %#v", result)
	}
}

func TestCheckDNSSkipsStandaloneRedirectProbe(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), log.New(os.Stderr, "", 0))
	monitor := NewHealthMonitor(
		manager,
		time.Second,
		1,
		cfg.Proxy.TProxyPort,
		cfg.Proxy.DNSPort,
		cfg.Proxy.Mark,
		cfg.Health.URL,
		time.Second,
		log.New(os.Stderr, "", 0),
	)

	result := monitor.checkDNS()
	if !result.Pass {
		t.Fatalf("transparent redirect DNS probe should be skipped as healthy diagnostic: %#v", result)
	}
	if result.Code != "" {
		t.Fatalf("skipped DNS probe should not emit a failure code: %#v", result)
	}
	if !strings.Contains(result.Detail, "standalone lookup") {
		t.Fatalf("detail should explain why the lookup was skipped: %#v", result)
	}
}

func TestNormalizeDNSProbeHostsUsesConfiguredProbeSet(t *testing.T) {
	got := normalizeDNSProbeHosts(
		"https://www.gstatic.com/generate_204",
		[]string{" example.com ", "", "cloudflare.com", "example.com"},
	)
	want := []string{"example.com", "cloudflare.com"}
	if len(got) != len(want) {
		t.Fatalf("unexpected probe hosts: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected probe hosts: got %#v want %#v", got, want)
		}
	}
}

func TestClearDropsStickyHealthState(t *testing.T) {
	cfg := config.DefaultConfig()
	manager := core.NewCoreManager(cfg, t.TempDir(), log.New(os.Stderr, "", 0))
	monitor := NewHealthMonitor(
		manager,
		time.Second,
		1,
		cfg.Proxy.TProxyPort,
		cfg.Proxy.DNSPort,
		cfg.Proxy.Mark,
		cfg.Health.URL,
		time.Second,
		log.New(os.Stderr, "", 0),
	)
	monitor.failures = 2
	monitor.lastResult = &HealthResult{Timestamp: time.Now()}

	monitor.Clear()
	if monitor.Failures() != 0 {
		t.Fatalf("failures were not cleared")
	}
	if monitor.LastResult() != nil {
		t.Fatalf("last result was not cleared")
	}
}
