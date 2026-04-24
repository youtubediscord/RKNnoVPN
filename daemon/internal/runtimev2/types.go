package runtimev2

import "time"

type BackendKind string

const (
	BackendRootTProxy BackendKind = "ROOT_TPROXY"
)

type Phase string

const (
	PhaseStopped         Phase = "STOPPED"
	PhaseApplying        Phase = "APPLYING" // legacy coarse in-flight phase
	PhaseStarting        Phase = "STARTING"
	PhaseConfigChecked   Phase = "CONFIG_CHECKED"
	PhaseCoreSpawned     Phase = "CORE_SPAWNED"
	PhaseCoreListening   Phase = "CORE_LISTENING"
	PhaseRulesApplied    Phase = "RULES_APPLIED"
	PhaseDNSApplied      Phase = "DNS_APPLIED"
	PhaseOutboundChecked Phase = "OUTBOUND_CHECKED"
	PhaseStopping        Phase = "STOPPING"
	PhaseResetting       Phase = "RESETTING"
	PhaseHealthy         Phase = "HEALTHY"
	PhaseDegraded        Phase = "DEGRADED"
	PhaseFailed          Phase = "FAILED"
)

type FallbackPolicy string

const (
	FallbackOfferReset FallbackPolicy = "OFFER_RESET"
	FallbackStayRoot   FallbackPolicy = "STAY_ON_SELECTED"
	FallbackAutoReset  FallbackPolicy = "AUTO_RESET_ROOTED"
)

type DesiredState struct {
	BackendKind     BackendKind    `json:"backendKind"`
	ActiveProfileID string         `json:"activeProfileId,omitempty"`
	RoutingMode     string         `json:"routingMode,omitempty"`
	AppSelection    AppSelection   `json:"appSelection,omitempty"`
	DNSPolicy       DNSPolicy      `json:"dnsPolicy,omitempty"`
	FallbackPolicy  FallbackPolicy `json:"fallbackPolicy,omitempty"`
}

type AppSelection struct {
	ProxyPackages  []string `json:"proxyPackages,omitempty"`
	BypassPackages []string `json:"bypassPackages,omitempty"`
}

type DNSPolicy struct {
	RemoteDNS string `json:"remoteDns,omitempty"`
	DirectDNS string `json:"directDns,omitempty"`
	FakeDNS   bool   `json:"fakeDns"`
	IPv6Mode  string `json:"ipv6Mode,omitempty"`
}

type AppliedState struct {
	BackendKind     BackendKind `json:"backendKind"`
	Phase           Phase       `json:"phase"`
	ActiveProfileID string      `json:"activeProfileId,omitempty"`
	StartedAt       time.Time   `json:"startedAt,omitempty"`
	Generation      int64       `json:"generation"`
}

type HealthSnapshot struct {
	CoreReady       bool                           `json:"coreReady"`
	DNSReady        bool                           `json:"dnsReady"`
	RoutingReady    bool                           `json:"routingReady"`
	EgressReady     bool                           `json:"egressReady"`
	LastCode        string                         `json:"lastCode,omitempty"`
	LastError       string                         `json:"lastError,omitempty"`
	LastUserMessage string                         `json:"lastUserMessage,omitempty"`
	LastDebug       string                         `json:"lastDebug,omitempty"`
	RollbackApplied bool                           `json:"rollbackApplied,omitempty"`
	StageReport     interface{}                    `json:"stageReport,omitempty"`
	CheckedAt       time.Time                      `json:"checkedAt,omitempty"`
	Checks          map[string]HealthCheckSnapshot `json:"checks,omitempty"`
}

func (h HealthSnapshot) Healthy() bool {
	return h.CoreReady && h.RoutingReady
}

func (h HealthSnapshot) OperationalHealthy() bool {
	return h.Healthy() && h.DNSReady && h.EgressReady
}

type HealthCheckSnapshot struct {
	Pass   bool   `json:"pass"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type ResetStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type ResetReport struct {
	BackendKind    BackendKind `json:"backendKind"`
	Generation     int64       `json:"generation"`
	Status         string      `json:"status"`
	Steps          []ResetStep `json:"steps"`
	Warnings       []string    `json:"warnings,omitempty"`
	Errors         []string    `json:"errors,omitempty"`
	Leftovers      []string    `json:"leftovers,omitempty"`
	RebootRequired bool        `json:"rebootRequired"`
}

type NodeProbeResult struct {
	ID               string `json:"id,omitempty"`
	Name             string `json:"name,omitempty"`
	Protocol         string `json:"protocol,omitempty"`
	Server           string `json:"server,omitempty"`
	Port             int    `json:"port,omitempty"`
	TCPDirect        *int64 `json:"tcpDirect,omitempty"`
	TunnelDelay      *int64 `json:"tunnelDelay,omitempty"`
	ResponseBytes    *int64 `json:"responseBytes,omitempty"`
	ThroughputBps    *int64 `json:"throughputBps,omitempty"`
	DNSBootstrap     bool   `json:"dnsBootstrap"`
	TCPStatus        string `json:"tcpStatus,omitempty"`
	URLStatus        string `json:"urlStatus,omitempty"`
	ThroughputStatus string `json:"throughputStatus,omitempty"`
	Verdict          string `json:"verdict,omitempty"`
	ErrorClass       string `json:"errorClass,omitempty"`
	ErrorDetail      string `json:"errorDetail,omitempty"`
}

type DiagnosticsSnapshot struct {
	Health HealthSnapshot    `json:"health"`
	Nodes  []NodeProbeResult `json:"nodes,omitempty"`
}

type BackendCapability struct {
	Kind      BackendKind `json:"kind"`
	Supported bool        `json:"supported"`
	Reason    string      `json:"reason,omitempty"`
}

type Status struct {
	DesiredState DesiredState        `json:"desiredState"`
	AppliedState AppliedState        `json:"appliedState"`
	Health       HealthSnapshot      `json:"health"`
	Capabilities []BackendCapability `json:"capabilities"`
}
