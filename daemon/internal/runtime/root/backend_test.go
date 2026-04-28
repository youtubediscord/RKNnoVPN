package root

import (
	"errors"
	"strings"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type fakePorts struct {
	state           core.State
	profile         *config.NodeProfile
	hasProfileNodes bool
	health          runtimev2.HealthSnapshot
	reapplyErr      error

	startCalls      int
	stopCalls       int
	startSubsystems int
	stopSubsystems  int
	resetRescue     int
	resetCalls      int
	startFailed     bool
}

func (p *fakePorts) GetState() core.State {
	return p.state
}

func (p *fakePorts) Start(profile *config.NodeProfile) error {
	p.startCalls++
	p.state = core.StateRunning
	return nil
}

func (p *fakePorts) Stop() error {
	p.stopCalls++
	p.state = core.StateStopped
	return nil
}

func (p *fakePorts) RuntimeProfile() (*config.NodeProfile, bool) {
	return p.profile, p.hasProfileNodes
}

func (p *fakePorts) BeginRuntimeStartOperation() uint64 {
	return 1
}

func (p *fakePorts) BeginRuntimeStopOperation() uint64 {
	return 1
}

func (p *fakePorts) MarkRuntimeStartFailed(epoch uint64) {
	p.startFailed = true
}

func (p *fakePorts) ResetRescueState() {
	p.resetRescue++
}

func (p *fakePorts) StartSubsystems() {
	p.startSubsystems++
}

func (p *fakePorts) StopSubsystems() {
	p.stopSubsystems++
}

func (p *fakePorts) CurrentRuntimeHealth() runtimev2.HealthSnapshot {
	return p.health
}

func (p *fakePorts) RefreshRuntimeHealth(allowEgressProbe bool) runtimev2.HealthSnapshot {
	return p.health
}

func (p *fakePorts) ReapplyRuntimeRules() error {
	return p.reapplyErr
}

func (p *fakePorts) RecoverStaleResetLock(generation int64) (*runtimev2.ResetReport, error) {
	return nil, nil
}

func (p *fakePorts) ResetNetworkStateReport(generation int64, backend runtimev2.BackendKind) runtimev2.ResetReport {
	p.resetCalls++
	return runtimev2.ResetReport{
		BackendKind: backend,
		Generation:  generation,
		Status:      "ok",
	}
}

func (p *fakePorts) ShouldSkipRootReconcile() (bool, string) {
	return false, ""
}

func (p *fakePorts) TestNodeProbes(url string, timeoutMS int, nodeIDs []string) []runtimev2.NodeProbeResult {
	return nil
}

func newFakeBackend(ports *fakePorts) *Backend {
	return NewBackend(Dependencies{
		Core:      ports,
		Profiles:  ports,
		Lifecycle: ports,
		Health:    ports,
		Netstack:  ports,
		Reset:     ports,
		Probes:    ports,
	})
}

func TestBackendStartFailsWithoutRuntimeProfile(t *testing.T) {
	ports := &fakePorts{
		state:  core.StateStopped,
		health: runtimev2.HealthSnapshot{CoreReady: true, RoutingReady: true},
	}
	backend := newFakeBackend(ports)

	_, err := backend.Start(runtimev2.DesiredState{BackendKind: runtimev2.BackendRootTProxy}, 7)
	if err == nil || !strings.Contains(err.Error(), "no node configured") {
		t.Fatalf("expected missing node error, got %v", err)
	}
	if ports.startCalls != 0 {
		t.Fatalf("core must not start without a profile, calls=%d", ports.startCalls)
	}
	if !ports.startFailed {
		t.Fatalf("missing profile should mark start operation failed")
	}
}

func TestBackendNetworkChangeResetsWhenReadinessFails(t *testing.T) {
	ports := &fakePorts{
		state:   core.StateRunning,
		profile: &config.NodeProfile{Address: "203.0.113.10", Port: 443},
		health:  runtimev2.HealthSnapshot{CoreReady: true, RoutingReady: false, LastError: "rules missing"},
	}
	backend := newFakeBackend(ports)

	report, err := backend.HandleNetworkChange(12)
	if err == nil || !strings.Contains(err.Error(), "network-change readiness gates failed") {
		t.Fatalf("expected readiness error, got report=%#v err=%v", report, err)
	}
	if report == nil || report.Generation != 12 {
		t.Fatalf("expected reset report for failed reconcile, got %#v", report)
	}
	if ports.resetCalls != 1 {
		t.Fatalf("expected exactly one reset after readiness failure, got %d", ports.resetCalls)
	}
}

func TestBackendNetworkChangeWrapsReapplyFailureWithResetReport(t *testing.T) {
	ports := &fakePorts{
		state:      core.StateRunning,
		profile:    &config.NodeProfile{Address: "203.0.113.10", Port: 443},
		health:     runtimev2.HealthSnapshot{CoreReady: true, RoutingReady: true},
		reapplyErr: errors.New("rules failed"),
	}
	backend := newFakeBackend(ports)

	report, err := backend.HandleNetworkChange(13)
	if err == nil || !strings.Contains(err.Error(), "network-change reapply failed") {
		t.Fatalf("expected reapply error, got report=%#v err=%v", report, err)
	}
	if report == nil || report.Generation != 13 {
		t.Fatalf("expected reset report for reapply failure, got %#v", report)
	}
	if fromErr := ResetReportFromError(err); fromErr == nil || fromErr.Generation != 13 {
		t.Fatalf("expected wrapped reset report, got %#v", fromErr)
	}
}

func TestReloadNeedsFullRestartComparesRuntimeEnvPolicy(t *testing.T) {
	base := map[string]string{
		"TPROXY_PORT": "10853",
		"DNS_PORT":    "10856",
		"PROXY_UIDS":  "10001",
	}
	same := map[string]string{
		"TPROXY_PORT": "10853",
		"DNS_PORT":    "10856",
		"PROXY_UIDS":  "10001",
		"IGNORED":     "changed",
	}
	changed := map[string]string{
		"TPROXY_PORT": "10854",
		"DNS_PORT":    "10856",
		"PROXY_UIDS":  "10001",
	}

	if ReloadNeedsFullRestart(base, same) {
		t.Fatalf("non-policy env changes must not force full restart")
	}
	if !ReloadNeedsFullRestart(base, changed) {
		t.Fatalf("runtime env policy change must force full restart")
	}
	if !ReloadNeedsFullRestart(nil, same) {
		t.Fatalf("missing old env must force full restart")
	}
}
