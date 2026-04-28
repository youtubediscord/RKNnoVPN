package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/subscription"
)

func TestBuildScriptEnvUsesExplicitDNSScopeForBlacklist(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Apps.Mode = "blacklist"
	cfg.Apps.Packages = []string{"com.example.direct"}

	env := buildScriptEnv(cfg, t.TempDir())
	if env["APP_MODE"] != "blacklist" {
		t.Fatalf("unexpected APP_MODE: %q", env["APP_MODE"])
	}
	if env["DNS_SCOPE"] != "all_except_uids" {
		t.Fatalf("blacklist DNS must exclude direct UIDs, got %q", env["DNS_SCOPE"])
	}
	if env["PROXY_UIDS"] != "" {
		t.Fatalf("blacklist mode must not put selected packages into PROXY_UIDS: %q", env["PROXY_UIDS"])
	}
}

func TestBuildScriptEnvUsesExplicitDNSScopeForWhitelist(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Apps.Mode = "whitelist"
	cfg.Apps.Packages = []string{"com.example.proxy"}

	env := buildScriptEnv(cfg, t.TempDir())
	if env["DNS_SCOPE"] != "uids" {
		t.Fatalf("whitelist DNS must target proxied UIDs only, got %q", env["DNS_SCOPE"])
	}
	if env["DIRECT_UIDS"] != "" {
		t.Fatalf("whitelist mode must not put selected packages into DIRECT_UIDS: %q", env["DIRECT_UIDS"])
	}
}

func TestBuildScriptEnvIncludesLocalHelperPorts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Profile.Inbounds = []byte(`{"socksPort":10808,"httpPort":10809}`)

	env := buildScriptEnv(cfg, t.TempDir())
	if env["SOCKS_PORT"] != "10808" {
		t.Fatalf("expected SOCKS_PORT=10808, got %q", env["SOCKS_PORT"])
	}
	if env["HTTP_PORT"] != "10809" {
		t.Fatalf("expected HTTP_PORT=10809, got %q", env["HTTP_PORT"])
	}
}

func TestBuildScriptEnvDisablesLocalHelperPortsByDefault(t *testing.T) {
	cfg := config.DefaultConfig()

	env := buildScriptEnv(cfg, t.TempDir())
	if env["SOCKS_PORT"] != "0" {
		t.Fatalf("default SOCKS_PORT must stay disabled, got %q", env["SOCKS_PORT"])
	}
	if env["HTTP_PORT"] != "0" {
		t.Fatalf("default HTTP_PORT must stay disabled, got %q", env["HTTP_PORT"])
	}
}

func TestReloadReportAccessorsPreserveLastReport(t *testing.T) {
	d := &daemon{}
	report := core.NewRuntimeStageReport("apply config")
	report.AddStage("hot-swap", "ok", "", "", false)
	report.FinishOK()

	d.setLastReloadReport(report)
	got := d.LastReloadReport()
	if got.Operation != "apply config" || got.Status != "ok" || len(got.Stages) != 1 {
		t.Fatalf("unexpected reload report: %#v", got)
	}
}

func TestRuntimeErrorCodePrefersTypedNetstackCode(t *testing.T) {
	err := &netstack.Error{
		Operation: "apply",
		Code:      "DNS_APPLY_FAILED",
		Report: netstack.Report{
			Operation: "apply",
			Status:    "failed",
			Errors:    []string{"dns-start: failed"},
		},
	}

	if got := runtimeErrorCode(err, "fallback"); got != "DNS_APPLY_FAILED" {
		t.Fatalf("expected DNS_APPLY_FAILED, got %q", got)
	}
	if got := runtimeErrorCode(errors.New("plain"), "fallback"); got != "fallback" {
		t.Fatalf("expected fallback, got %q", got)
	}
}

func TestHealthEgressURLsPrefersConfiguredProbeSet(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Health.EgressURLs = []string{" https://cp.cloudflare.com/generate_204 ", "", "https://example.com/204"}
	cfg.Health.URL = "https://cp.cloudflare.com/generate_204"

	got := healthEgressURLs(cfg)
	want := []string{
		"https://cp.cloudflare.com/generate_204",
		"https://example.com/204",
		"https://www.gstatic.com/generate_204",
	}
	if len(got) < len(want) {
		t.Fatalf("unexpected probe URLs: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected probe URLs: got %#v want prefix %#v", got, want)
		}
	}
}

func TestValidateSubscriptionFetchURLRejectsLocalTargets(t *testing.T) {
	for _, rawURL := range []string{
		"file:///data/local/tmp/sub.txt",
		"http://127.0.0.1:8080/sub",
		"http://localhost/sub",
		"http://192.168.1.1/sub",
		"http://[::1]/sub",
	} {
		if err := subscription.ValidateFetchURL(rawURL); err == nil {
			t.Fatalf("expected %q to be rejected", rawURL)
		}
	}
	if err := subscription.ValidateFetchURL("https://example.com/sub"); err != nil {
		t.Fatalf("expected public https URL to pass, got %v", err)
	}
}

func TestReadLogTailReturnsBoundedTail(t *testing.T) {
	path := t.TempDir() + "/runtime.log"
	content := strings.Join([]string{"one", "two", "three", "four"}, "\n")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	lines, err := diagnostics.ReadLogTail(path, 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, ","); got != "three,four" {
		t.Fatalf("unexpected tail: %q", got)
	}
}
