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

type fakeBackend struct {
	kind      BackendKind
	stopCalls int
}

func (f *fakeBackend) Kind() BackendKind         { return f.kind }
func (f *fakeBackend) Supported() (bool, string) { return true, "" }
func (f *fakeBackend) Start(DesiredState) error  { return nil }
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
