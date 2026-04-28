package root

import (
	"fmt"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type CoreController interface {
	GetState() core.State
	Start(profile *config.NodeProfile) error
	Stop() error
}

type ProfileStore interface {
	RuntimeProfile() (profile *config.NodeProfile, hasProfileNodes bool)
}

type LifecycleController interface {
	BeginRuntimeStartOperation() uint64
	BeginRuntimeStopOperation() uint64
	MarkRuntimeStartFailed(epoch uint64)
	ResetRescueState()
	StartSubsystems()
	StopSubsystems()
}

type HealthAdapter interface {
	CurrentRuntimeHealth() runtimev2.HealthSnapshot
	RefreshRuntimeHealth(allowEgressProbe bool) runtimev2.HealthSnapshot
}

type NetstackVerifier interface {
	ReapplyRuntimeRules() error
}

type ResetController interface {
	RecoverStaleResetLock(generation int64) (*runtimev2.ResetReport, error)
	ResetNetworkStateReport(generation int64, backend runtimev2.BackendKind) runtimev2.ResetReport
	ShouldSkipRootReconcile() (bool, string)
}

type NodeProber interface {
	TestNodeProbes(url string, timeoutMS int, nodeIDs []string) []runtimev2.NodeProbeResult
}

type Dependencies struct {
	Core      CoreController
	Profiles  ProfileStore
	Lifecycle LifecycleController
	Health    HealthAdapter
	Netstack  NetstackVerifier
	Reset     ResetController
	Probes    NodeProber
}

type Backend struct {
	deps Dependencies
}

func NewBackend(deps Dependencies) *Backend {
	return &Backend{deps: deps}
}

func (b *Backend) Kind() runtimev2.BackendKind {
	return runtimev2.BackendRootTProxy
}

func (b *Backend) Supported() (bool, string) {
	return true, ""
}

func (b *Backend) Start(desired runtimev2.DesiredState, generation int64) (*runtimev2.ResetReport, error) {
	recoveryReport, err := b.deps.Reset.RecoverStaleResetLock(generation)
	if err != nil {
		return recoveryReport, err
	}
	epoch := b.deps.Lifecycle.BeginRuntimeStartOperation()
	state := b.deps.Core.GetState()
	if state == core.StateRunning || state == core.StateDegraded {
		return recoveryReport, nil
	}

	profile, hasProfileNodes := b.deps.Profiles.RuntimeProfile()
	if profile == nil {
		profile = &config.NodeProfile{}
	}
	if profile.Address == "" && !hasProfileNodes {
		b.deps.Lifecycle.MarkRuntimeStartFailed(epoch)
		return recoveryReport, fmt.Errorf("no node configured (address is empty)")
	}
	if err := b.deps.Core.Start(profile); err != nil {
		b.deps.Lifecycle.MarkRuntimeStartFailed(epoch)
		return recoveryReport, err
	}
	b.deps.Lifecycle.ResetRescueState()
	b.deps.Lifecycle.StartSubsystems()

	snapshot := b.RefreshHealth()
	if !snapshot.Healthy() {
		report := b.resetNetworkState(generation)
		b.deps.Lifecycle.MarkRuntimeStartFailed(epoch)
		return &report, RuntimeErrorWithResetReport(
			fmt.Errorf("readiness gates failed after start: %s", snapshot.LastError),
			report,
		)
	}
	return recoveryReport, nil
}

func (b *Backend) Stop() error {
	b.deps.Lifecycle.BeginRuntimeStopOperation()
	b.deps.Lifecycle.StopSubsystems()
	return b.deps.Core.Stop()
}

func (b *Backend) Reset(generation int64) runtimev2.ResetReport {
	b.deps.Lifecycle.BeginRuntimeStopOperation()
	return b.resetNetworkState(generation)
}

func (b *Backend) Restart(desired runtimev2.DesiredState, generation int64) (*runtimev2.ResetReport, error) {
	recoveryReport, err := b.deps.Reset.RecoverStaleResetLock(generation)
	if err != nil {
		return recoveryReport, err
	}
	b.deps.Lifecycle.BeginRuntimeStartOperation()
	err = b.restart(generation)
	if recoveryReport != nil {
		return recoveryReport, err
	}
	return ResetReportFromError(err), err
}

func (b *Backend) RestartAfterConfigChange(generation int64) error {
	return b.restart(generation)
}

func (b *Backend) HandleNetworkChange(generation int64) (*runtimev2.ResetReport, error) {
	recoveryReport, err := b.deps.Reset.RecoverStaleResetLock(generation)
	if err != nil {
		return recoveryReport, err
	}
	err = b.reconcile("network-change", generation)
	if recoveryReport != nil {
		return recoveryReport, err
	}
	return ResetReportFromError(err), err
}

func (b *Backend) CurrentHealth() runtimev2.HealthSnapshot {
	return b.deps.Health.CurrentRuntimeHealth()
}

func (b *Backend) RefreshHealth() runtimev2.HealthSnapshot {
	return b.deps.Health.RefreshRuntimeHealth(true)
}

func (b *Backend) TestNodes(desired runtimev2.DesiredState, url string, timeoutMS int, nodeIDs []string) ([]runtimev2.NodeProbeResult, error) {
	return b.deps.Probes.TestNodeProbes(url, timeoutMS, nodeIDs), nil
}

func (b *Backend) restart(generation int64) error {
	b.deps.Lifecycle.StopSubsystems()
	if b.deps.Core.GetState() != core.StateStopped {
		if err := b.deps.Core.Stop(); err != nil {
			report := b.resetNetworkState(generation)
			return RuntimeErrorWithResetReport(fmt.Errorf("restart stop failed: %w", err), report)
		}
	}

	profile, hasProfileNodes := b.deps.Profiles.RuntimeProfile()
	if profile == nil {
		profile = &config.NodeProfile{}
	}
	if profile.Address == "" && !hasProfileNodes {
		return fmt.Errorf("no node configured (address is empty)")
	}
	if err := b.deps.Core.Start(profile); err != nil {
		report := b.resetNetworkState(generation)
		return RuntimeErrorWithResetReport(fmt.Errorf("restart start failed: %w", err), report)
	}
	b.deps.Lifecycle.ResetRescueState()
	b.deps.Lifecycle.StartSubsystems()

	snapshot := b.deps.Health.RefreshRuntimeHealth(false)
	if !snapshot.Healthy() {
		report := b.resetNetworkState(generation)
		return RuntimeErrorWithResetReport(
			fmt.Errorf("restart readiness gates failed: %s", snapshot.LastError),
			report,
		)
	}
	return nil
}

func (b *Backend) reconcile(reason string, generation int64) error {
	state := b.deps.Core.GetState()
	if state != core.StateRunning && state != core.StateDegraded {
		return nil
	}
	if skip, _ := b.deps.Reset.ShouldSkipRootReconcile(); skip {
		return nil
	}

	if err := b.deps.Netstack.ReapplyRuntimeRules(); err != nil {
		report := b.resetNetworkState(generation)
		return RuntimeErrorWithResetReport(fmt.Errorf("%s reapply failed: %w", reason, err), report)
	}

	snapshot := b.deps.Health.RefreshRuntimeHealth(false)
	if snapshot.Healthy() {
		return nil
	}

	report := b.resetNetworkState(generation)
	return RuntimeErrorWithResetReport(
		fmt.Errorf("%s readiness gates failed: %s", reason, snapshot.LastError),
		report,
	)
}

func (b *Backend) resetNetworkState(generation int64) runtimev2.ResetReport {
	return b.deps.Reset.ResetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
}
