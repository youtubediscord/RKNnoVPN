package root

import (
	"fmt"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type HealthInput struct {
	State        core.State
	Result       *health.HealthResult
	StageReport  core.RuntimeStageReport
	RecentEgress bool
	CheckedAt    time.Time
}

func ClassifyHealth(input HealthInput) runtimev2.HealthSnapshot {
	snapshot := runtimev2.HealthSnapshot{
		CoreReady: input.State == core.StateRunning,
		CheckedAt: input.CheckedAt,
	}
	if !input.StageReport.Empty() {
		snapshot.StageReport = input.StageReport
	}
	if input.State == core.StateDegraded {
		snapshot.CoreReady = true
	}
	if input.Result == nil {
		if !input.StageReport.Empty() &&
			input.StageReport.Status == "ok" &&
			(input.State == core.StateRunning || input.State == core.StateDegraded) {
			snapshot.RoutingReady = true
		}
		snapshot.EgressReady = input.RecentEgress
		return snapshot
	}

	snapshot.CheckedAt = input.Result.Timestamp
	checkPassed := func(name string) bool {
		check, ok := input.Result.Checks[name]
		return ok && check.Pass
	}
	checkPassedOrMissing := func(name string) bool {
		check, ok := input.Result.Checks[name]
		return !ok || check.Pass
	}
	snapshot.CoreReady = checkPassed("singbox_alive") && checkPassed("tproxy_port")
	snapshot.RoutingReady = checkPassed("iptables") && checkPassed("routing")
	snapshot.DNSReady = checkPassedOrMissing("dns_listener") && checkPassedOrMissing("dns")
	if check, ok := input.Result.Checks["outbound_url"]; ok {
		snapshot.EgressReady = check.Pass
	} else {
		snapshot.EgressReady = input.RecentEgress
	}
	snapshot.Checks = runtimeHealthChecks(input.Result)

	diagnostic := FirstFailedGateDiagnostic(input.Result, snapshot)
	if snapshot.LastCode == "" {
		snapshot.LastCode = diagnostic.Code
	}
	if snapshot.LastError == "" {
		snapshot.LastError = diagnostic.Detail
	}
	return snapshot
}

func runtimeHealthChecks(result *health.HealthResult) map[string]runtimev2.HealthCheckSnapshot {
	if result == nil || len(result.Checks) == 0 {
		return nil
	}
	checks := make(map[string]runtimev2.HealthCheckSnapshot, len(result.Checks))
	for name, check := range result.Checks {
		checks[name] = runtimev2.HealthCheckSnapshot{
			Pass:   check.Pass,
			Code:   check.Code,
			Detail: check.Detail,
		}
	}
	return checks
}

type HealthGateDiagnostic struct {
	Code   string
	Detail string
}

func FirstFailedGateDiagnostic(result *health.HealthResult, snapshot runtimev2.HealthSnapshot) HealthGateDiagnostic {
	if result != nil {
		for _, name := range []string{"singbox_alive", "tproxy_port", "iptables", "routing"} {
			if check, ok := result.Checks[name]; ok && !check.Pass {
				return HealthGateDiagnostic{
					Code:   firstNonEmpty(check.Code, "READINESS_GATE_FAILED"),
					Detail: formatHealthCheckError(name, check),
				}
			}
		}
		if snapshot.Healthy() {
			for _, name := range []string{"dns_listener", "dns"} {
				if check, ok := result.Checks[name]; ok && !check.Pass {
					return HealthGateDiagnostic{
						Code:   firstNonEmpty(check.Code, "PROXY_DNS_UNAVAILABLE"),
						Detail: fmt.Sprintf("operational degraded: proxy DNS unavailable: %s", formatHealthCheckError(name, check)),
					}
				}
			}
		}
	}
	if !snapshot.Healthy() {
		return HealthGateDiagnostic{Code: "READINESS_GATE_FAILED", Detail: "one or more readiness gates are red"}
	}
	if !snapshot.EgressReady {
		if result != nil {
			if check, ok := result.Checks["outbound_url"]; ok && !check.Pass {
				return HealthGateDiagnostic{
					Code:   firstNonEmpty(check.Code, "OUTBOUND_URL_FAILED"),
					Detail: fmt.Sprintf("operational degraded: outbound URL probe failed: %s", formatHealthCheckError("outbound_url", check)),
				}
			}
		}
		return HealthGateDiagnostic{Code: "OUTBOUND_URL_FAILED", Detail: "operational degraded: no recent successful egress probe"}
	}
	if !snapshot.OperationalHealthy() {
		return HealthGateDiagnostic{Code: "OPERATIONAL_DEGRADED", Detail: "operational degraded: one or more operational health signals are red"}
	}
	return HealthGateDiagnostic{}
}

func formatHealthCheckError(name string, check health.CheckResult) string {
	if check.Code != "" {
		return fmt.Sprintf("%s: %s: %s", name, check.Code, check.Detail)
	}
	return fmt.Sprintf("%s: %s", name, check.Detail)
}
