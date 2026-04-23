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
