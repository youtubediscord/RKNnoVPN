package root

import (
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func ClassifyURLTestFailure(err error, snapshot runtimev2.HealthSnapshot) string {
	if direct := classifyURLTestControlPlaneError(err); direct != "" {
		return direct
	}
	if !snapshot.Healthy() {
		return "runtime_not_ready"
	}
	switch snapshot.LastCode {
	case "DNS_LISTENER_DOWN",
		"DNS_LOOKUP_TIMEOUT",
		"DNS_EMPTY_RESPONSE",
		"DNS_LOOKUP_FAILED",
		"PROXY_DNS_UNAVAILABLE":
		return "proxy_dns_unavailable"
	case "OUTBOUND_URL_FAILED":
		return "outbound_url_failed"
	case "TPROXY_PORT_DOWN",
		"RULES_NOT_APPLIED",
		"ROUTING_CHECK_FAILED",
		"ROUTING_V4_MISSING",
		"ROUTING_V6_MISSING",
		"ROUTING_NOT_APPLIED",
		"CORE_PID_MISSING",
		"CORE_PID_LOOKUP_FAILED",
		"CORE_PROCESS_DEAD":
		return "runtime_not_ready"
	}
	if !snapshot.DNSReady {
		return "proxy_dns_unavailable"
	}
	if !snapshot.EgressReady {
		return "runtime_degraded"
	}
	if err != nil {
		if direct := classifyURLTestError(err); direct != "" {
			return direct
		}
	}
	return "tunnel_delay_failed"
}

func classifyURLTestControlPlaneError(err error) string {
	if err == nil {
		return ""
	}
	detail := strings.ToLower(err.Error())
	switch {
	case strings.Contains(detail, "api_disabled"):
		return "api_disabled"
	case strings.Contains(detail, "connection refused"),
		strings.Contains(detail, "connect: connection"),
		strings.Contains(detail, "127.0.0.1"),
		strings.Contains(detail, "api port"):
		return "api_unavailable"
	case strings.Contains(detail, "http 404"),
		strings.Contains(detail, "not found"),
		strings.Contains(detail, "outbound tag"):
		return "outbound_missing"
	}
	return ""
}

func classifyURLTestError(err error) string {
	if err == nil {
		return ""
	}
	if control := classifyURLTestControlPlaneError(err); control != "" {
		return control
	}
	detail := strings.ToLower(err.Error())
	switch {
	case strings.Contains(detail, "no such host"),
		strings.Contains(detail, "dns"):
		return "proxy_dns_unavailable"
	case strings.Contains(detail, "tls"),
		strings.Contains(detail, "handshake"):
		return "tls_handshake_failed"
	case strings.Contains(detail, "timeout"),
		strings.Contains(detail, "deadline exceeded"),
		strings.Contains(detail, "i/o timeout"):
		return "tunnel_delay_failed"
	}
	return ""
}
