package runtimev2

import (
	"testing"
	"time"
)

func TestPhaseFromHealthUsesOperationalHealthForDegradedPhase(t *testing.T) {
	health := HealthSnapshot{
		CoreReady:    true,
		RoutingReady: true,
		DNSReady:     false,
		EgressReady:  false,
		CheckedAt:    time.Now(),
	}

	if got := phaseFromHealth(health, PhaseHealthy); got != PhaseDegraded {
		t.Fatalf("expected operationally red runtime to be DEGRADED, got %s", got)
	}
	if !health.Healthy() {
		t.Fatalf("readiness health should remain green")
	}
	if health.OperationalHealthy() {
		t.Fatalf("operational health should be red")
	}
}

func TestPhaseFromHealthUsesStableFailureCodeForStage(t *testing.T) {
	cases := []struct {
		name string
		code string
		want Phase
	}{
		{name: "tproxy", code: "TPROXY_PORT_DOWN", want: PhaseCoreSpawned},
		{name: "rules", code: "RULES_NOT_APPLIED", want: PhaseCoreListening},
		{name: "netstack cleanup", code: "NETSTACK_CLEANUP_FAILED", want: PhaseCoreListening},
		{name: "dns listener", code: "DNS_LISTENER_DOWN", want: PhaseCoreListening},
		{name: "api listener", code: "API_PORT_DOWN", want: PhaseCoreListening},
		{name: "dns upstream", code: "DNS_LOOKUP_TIMEOUT", want: PhaseDNSApplied},
		{name: "outbound", code: "OUTBOUND_URL_FAILED", want: PhaseOutboundChecked},
		{name: "core crash", code: "CORE_PROCESS_DEAD", want: PhaseFailed},
		{name: "config render", code: "CONFIG_RENDER_FAILED", want: PhaseStarting},
		{name: "config check", code: "CONFIG_CHECK_FAILED", want: PhaseConfigChecked},
		{name: "dns apply", code: "DNS_APPLY_FAILED", want: PhaseRulesApplied},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			health := HealthSnapshot{
				CoreReady:    true,
				RoutingReady: true,
				DNSReady:     true,
				EgressReady:  false,
				LastCode:     tc.code,
				CheckedAt:    time.Now(),
			}

			if got := phaseFromHealth(health, PhaseHealthy); got != tc.want {
				t.Fatalf("expected %s for %s, got %s", tc.want, tc.code, got)
			}
		})
	}
}

func TestStopCallsBackendEvenWhenAppliedPhaseStopped(t *testing.T) {
	backend := &fakeBackend{kind: BackendRootTProxy}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	status, err := o.Stop()
	if err != nil {
		t.Fatalf("stop returned error: %v", err)
	}
	if backend.stopCalls != 1 {
		t.Fatalf("expected backend Stop to run once, got %d", backend.stopCalls)
	}
	if status.AppliedState.Phase != PhaseStopped {
		t.Fatalf("expected stopped phase, got %#v", status.AppliedState)
	}
}

func TestStartErrorUsesCodedRuntimePhase(t *testing.T) {
	stageReport := map[string]interface{}{"failedStage": "netstack-apply"}
	backend := &fakeBackend{
		kind:     BackendRootTProxy,
		startErr: codedTestError{code: "RULES_NOT_APPLIED", message: "iptables start failed", userMessage: "Routing rules failed.", debug: "iptables exit status 1", rollbackApplied: true, stageReport: stageReport},
	}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	status, err := o.Start()
	if err == nil {
		t.Fatal("expected start error")
	}
	if status.Health.LastCode != "RULES_NOT_APPLIED" {
		t.Fatalf("expected LastCode RULES_NOT_APPLIED, got %#v", status.Health)
	}
	if status.AppliedState.Phase != PhaseCoreListening {
		t.Fatalf("expected phase %s, got %#v", PhaseCoreListening, status.AppliedState)
	}
	if status.Health.LastUserMessage != "Routing rules failed." {
		t.Fatalf("expected user message, got %#v", status.Health)
	}
	if status.Health.LastDebug != "iptables exit status 1" {
		t.Fatalf("expected debug detail, got %#v", status.Health)
	}
	if !status.Health.RollbackApplied {
		t.Fatalf("expected rollback flag, got %#v", status.Health)
	}
	if status.Health.StageReport == nil {
		t.Fatalf("expected stage report, got %#v", status.Health)
	}
}

type fakeBackend struct {
	kind      BackendKind
	stopCalls int
	startErr  error
}

func (f *fakeBackend) Kind() BackendKind         { return f.kind }
func (f *fakeBackend) Supported() (bool, string) { return true, "" }
func (f *fakeBackend) Start(DesiredState) error  { return f.startErr }
func (f *fakeBackend) Stop() error {
	f.stopCalls++
	return nil
}
func (f *fakeBackend) Reset(generation int64) ResetReport {
	return ResetReport{BackendKind: f.kind, Generation: generation, Status: "ok"}
}
func (f *fakeBackend) Restart(DesiredState, int64) error { return nil }
func (f *fakeBackend) HandleNetworkChange(int64) error   { return nil }
func (f *fakeBackend) CurrentHealth() HealthSnapshot     { return HealthSnapshot{} }
func (f *fakeBackend) RefreshHealth() HealthSnapshot {
	return HealthSnapshot{CoreReady: true, RoutingReady: true, DNSReady: true, EgressReady: true, CheckedAt: time.Now()}
}
func (f *fakeBackend) TestNodes(DesiredState, string, int, []string) ([]NodeProbeResult, error) {
	return nil, nil
}

type codedTestError struct {
	code            string
	message         string
	userMessage     string
	debug           string
	rollbackApplied bool
	stageReport     interface{}
}

func (e codedTestError) Error() string                { return e.message }
func (e codedTestError) RuntimeCode() string          { return e.code }
func (e codedTestError) RuntimeUserMessage() string   { return e.userMessage }
func (e codedTestError) RuntimeDebug() string         { return e.debug }
func (e codedTestError) RuntimeRollbackApplied() bool { return e.rollbackApplied }
func (e codedTestError) RuntimeStageReport() interface{} {
	return e.stageReport
}
