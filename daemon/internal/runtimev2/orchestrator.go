package runtimev2

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type Backend interface {
	Kind() BackendKind
	Supported() (bool, string)
	Start(desired DesiredState) error
	Stop() error
	Reset(generation int64) ResetReport
	Restart(desired DesiredState, generation int64) error
	HandleNetworkChange(generation int64) error
	CurrentHealth() HealthSnapshot
	RefreshHealth() HealthSnapshot
	TestNodes(desired DesiredState, url string, timeoutMS int, nodeIDs []string) ([]NodeProbeResult, error)
}

type Orchestrator struct {
	mu       sync.Mutex
	backends map[BackendKind]Backend

	desired DesiredState
	applied AppliedState
	health  HealthSnapshot
}

func NewOrchestrator(desired DesiredState, backends ...Backend) *Orchestrator {
	if desired.BackendKind == "" {
		desired.BackendKind = BackendRootTProxy
	}
	o := &Orchestrator{
		backends: make(map[BackendKind]Backend, len(backends)),
		desired:  desired,
		applied: AppliedState{
			BackendKind: desired.BackendKind,
			Phase:       PhaseStopped,
		},
	}
	for _, backend := range backends {
		o.backends[backend.Kind()] = backend
	}
	return o
}

func (o *Orchestrator) ApplyDesiredState(desired DesiredState) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if desired.BackendKind == "" {
		desired.BackendKind = o.desired.BackendKind
	}
	if err := o.validateDesiredLocked(desired); err != nil {
		return err
	}
	o.desired = desired
	return nil
}

func (o *Orchestrator) Status() Status {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.statusLocked()
}

func (o *Orchestrator) Start() (Status, error) {
	o.mu.Lock()
	desired := o.desired
	backend, err := o.backendForLocked(desired.BackendKind)
	if err != nil {
		o.health = HealthSnapshot{LastError: err.Error(), CheckedAt: time.Now()}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
	supported, reason := backend.Supported()
	if !supported {
		err = fmt.Errorf("%s", reason)
		o.health = HealthSnapshot{LastError: err.Error(), CheckedAt: time.Now()}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}

	generation := o.nextGenerationLocked()
	startedAt := time.Now()
	o.applied = AppliedState{
		BackendKind:     desired.BackendKind,
		Phase:           PhaseStarting,
		ActiveProfileID: desired.ActiveProfileID,
		Generation:      generation,
	}
	o.mu.Unlock()

	if err := backend.Start(desired); err != nil {
		health := healthFromError(err)
		o.mu.Lock()
		o.health = health
		o.applied = AppliedState{
			BackendKind:     desired.BackendKind,
			Phase:           phaseFromHealth(health, PhaseFailed),
			ActiveProfileID: desired.ActiveProfileID,
			Generation:      generation,
		}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}

	health := backend.RefreshHealth()

	o.mu.Lock()
	o.health = health
	o.applied = AppliedState{
		BackendKind:     desired.BackendKind,
		Phase:           phaseFromHealth(health, PhaseHealthy),
		ActiveProfileID: desired.ActiveProfileID,
		StartedAt:       startedAt,
		Generation:      generation,
	}
	status := o.statusLocked()
	o.mu.Unlock()
	return status, nil
}

func (o *Orchestrator) Stop() (Status, error) {
	o.mu.Lock()
	backendKind := o.applied.BackendKind
	if backendKind == "" {
		backendKind = o.desired.BackendKind
	}
	backend, err := o.backendForLocked(backendKind)
	if err != nil {
		o.health = HealthSnapshot{LastError: err.Error(), CheckedAt: time.Now()}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
	if o.applied.Phase == PhaseStopped {
		generation := o.nextGenerationLocked()
		active := o.applied
		o.applied = AppliedState{
			BackendKind:     backendKind,
			Phase:           PhaseStopping,
			ActiveProfileID: active.ActiveProfileID,
			Generation:      generation,
		}
		o.mu.Unlock()

		stopErr := backend.Stop()

		o.mu.Lock()
		o.health = HealthSnapshot{CheckedAt: time.Now()}
		if stopErr != nil {
			o.health.LastError = stopErr.Error()
		}
		o.applied = AppliedState{
			BackendKind:     backendKind,
			Phase:           PhaseStopped,
			ActiveProfileID: active.ActiveProfileID,
			Generation:      generation,
		}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, stopErr
	}
	generation := o.nextGenerationLocked()
	active := o.applied
	o.applied.Phase = PhaseStopping
	o.mu.Unlock()

	stopErr := backend.Stop()
	if stopErr != nil {
		report := backend.Reset(generation)
		if report.Status != "ok" {
			o.mu.Lock()
			o.health = HealthSnapshot{LastError: stopErr.Error(), CheckedAt: time.Now()}
			o.applied = AppliedState{
				BackendKind:     active.BackendKind,
				Phase:           PhaseFailed,
				ActiveProfileID: active.ActiveProfileID,
				StartedAt:       active.StartedAt,
				Generation:      generation,
			}
			status := o.statusLocked()
			o.mu.Unlock()
			return status, stopErr
		}
	}

	o.mu.Lock()
	o.health = HealthSnapshot{CheckedAt: time.Now()}
	o.applied = AppliedState{
		BackendKind:     active.BackendKind,
		Phase:           PhaseStopped,
		ActiveProfileID: active.ActiveProfileID,
		Generation:      generation,
	}
	status := o.statusLocked()
	o.mu.Unlock()
	return status, nil
}

func (o *Orchestrator) Restart() (Status, error) {
	o.mu.Lock()
	desired := o.desired
	backend, err := o.backendForLocked(desired.BackendKind)
	if err != nil {
		o.health = HealthSnapshot{LastError: err.Error(), CheckedAt: time.Now()}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
	supported, reason := backend.Supported()
	if !supported {
		err = fmt.Errorf("%s", reason)
		o.health = HealthSnapshot{LastError: err.Error(), CheckedAt: time.Now()}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
	generation := o.nextGenerationLocked()
	startedAt := time.Now()
	o.applied = AppliedState{
		BackendKind:     desired.BackendKind,
		Phase:           PhaseStarting,
		ActiveProfileID: desired.ActiveProfileID,
		Generation:      generation,
	}
	o.mu.Unlock()

	if err := backend.Restart(desired, generation); err != nil {
		health := healthFromError(err)
		o.mu.Lock()
		o.health = health
		o.applied = AppliedState{
			BackendKind:     desired.BackendKind,
			Phase:           phaseFromHealth(health, PhaseFailed),
			ActiveProfileID: desired.ActiveProfileID,
			Generation:      generation,
		}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}

	health := backend.RefreshHealth()

	o.mu.Lock()
	o.health = health
	o.applied = AppliedState{
		BackendKind:     desired.BackendKind,
		Phase:           phaseFromHealth(health, PhaseHealthy),
		ActiveProfileID: desired.ActiveProfileID,
		StartedAt:       startedAt,
		Generation:      generation,
	}
	status := o.statusLocked()
	o.mu.Unlock()
	return status, nil
}

func (o *Orchestrator) Reset() ResetReport {
	o.mu.Lock()
	backendKind := o.applied.BackendKind
	if backendKind == "" {
		backendKind = o.desired.BackendKind
	}
	backend, err := o.backendForLocked(backendKind)
	generation := o.nextGenerationLocked()
	if err != nil {
		report := ResetReport{
			BackendKind: backendKind,
			Generation:  generation,
			Status:      "failed",
			Steps: []ResetStep{
				{Name: "resolve-backend", Status: "failed", Detail: err.Error()},
			},
			Errors: []string{err.Error()},
		}
		o.health = HealthSnapshot{LastError: err.Error(), CheckedAt: time.Now()}
		o.applied = AppliedState{
			BackendKind: backendKind,
			Phase:       PhaseStopped,
			Generation:  generation,
		}
		o.mu.Unlock()
		return report
	}
	o.applied = AppliedState{
		BackendKind:     backendKind,
		Phase:           PhaseResetting,
		ActiveProfileID: o.applied.ActiveProfileID,
		Generation:      generation,
	}
	o.mu.Unlock()

	report := backend.Reset(generation)

	o.mu.Lock()
	if report.Status == "ok" {
		o.health = HealthSnapshot{CheckedAt: time.Now()}
	} else {
		o.health = HealthSnapshot{LastError: firstError(report.Errors), CheckedAt: time.Now()}
	}
	o.applied = AppliedState{
		BackendKind:     backendKind,
		Phase:           PhaseStopped,
		ActiveProfileID: o.desired.ActiveProfileID,
		Generation:      generation,
	}
	o.mu.Unlock()
	return report
}

func (o *Orchestrator) HandleNetworkChange() (Status, error) {
	o.mu.Lock()
	if o.applied.Phase == PhaseStopped {
		status := o.statusLocked()
		o.mu.Unlock()
		return status, nil
	}
	backend, err := o.backendForLocked(o.applied.BackendKind)
	if err != nil {
		o.health = HealthSnapshot{LastError: err.Error(), CheckedAt: time.Now()}
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
	generation := o.nextGenerationLocked()
	active := o.applied
	o.applied.Phase = PhaseStarting
	o.applied.Generation = generation
	o.mu.Unlock()

	err = backend.HandleNetworkChange(generation)
	health := backend.CurrentHealth()
	if health.CheckedAt.IsZero() {
		health = backend.RefreshHealth()
	}

	o.mu.Lock()
	o.health = health
	o.applied = AppliedState{
		BackendKind:     active.BackendKind,
		Phase:           phaseFromHealth(health, PhaseHealthy),
		ActiveProfileID: active.ActiveProfileID,
		StartedAt:       active.StartedAt,
		Generation:      generation,
	}
	if err != nil {
		o.applied.Phase = PhaseDegraded
		if o.health.LastError == "" {
			o.health.LastError = err.Error()
		}
		if o.health.CheckedAt.IsZero() {
			o.health.CheckedAt = time.Now()
		}
	}
	status := o.statusLocked()
	o.mu.Unlock()
	return status, err
}

func (o *Orchestrator) RefreshHealth() HealthSnapshot {
	o.mu.Lock()
	backendKind := o.applied.BackendKind
	if backendKind == "" {
		backendKind = o.desired.BackendKind
	}
	backend, err := o.backendForLocked(backendKind)
	if err != nil {
		o.health = HealthSnapshot{LastError: err.Error(), CheckedAt: time.Now()}
		health := o.health
		o.mu.Unlock()
		return health
	}
	o.mu.Unlock()

	health := backend.RefreshHealth()

	o.mu.Lock()
	o.health = health
	if o.applied.Phase != PhaseStopped {
		o.applied.Phase = phaseFromHealth(health, o.applied.Phase)
	}
	o.mu.Unlock()
	return health
}

func (o *Orchestrator) CurrentHealth() HealthSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.health
}

func (o *Orchestrator) TestNodes(url string, timeoutMS int, nodeIDs []string) ([]NodeProbeResult, error) {
	o.mu.Lock()
	desired := o.desired
	backend, err := o.backendForLocked(desired.BackendKind)
	o.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return backend.TestNodes(desired, url, timeoutMS, nodeIDs)
}

func (o *Orchestrator) validateDesiredLocked(desired DesiredState) error {
	backend, err := o.backendForLocked(desired.BackendKind)
	if err != nil {
		return err
	}
	supported, reason := backend.Supported()
	if !supported && desired.BackendKind != BackendRootTProxy {
		return fmt.Errorf("%s", reason)
	}
	return nil
}

func (o *Orchestrator) backendForLocked(kind BackendKind) (Backend, error) {
	if kind == "" {
		kind = BackendRootTProxy
	}
	backend, ok := o.backends[kind]
	if !ok {
		return nil, fmt.Errorf("backend %s is not registered", kind)
	}
	return backend, nil
}

func (o *Orchestrator) nextGenerationLocked() int64 {
	next := o.applied.Generation + 1
	if next < 1 {
		next = 1
	}
	return next
}

func (o *Orchestrator) statusLocked() Status {
	caps := make([]BackendCapability, 0, len(o.backends))
	for _, kind := range []BackendKind{BackendRootTProxy} {
		backend, ok := o.backends[kind]
		if !ok {
			continue
		}
		supported, reason := backend.Supported()
		caps = append(caps, BackendCapability{
			Kind:      kind,
			Supported: supported,
			Reason:    reason,
		})
	}
	return Status{
		DesiredState: o.desired,
		AppliedState: o.applied,
		Health:       o.health,
		Capabilities: caps,
	}
}

func phaseFromHealth(health HealthSnapshot, fallback Phase) Phase {
	if health.CheckedAt.IsZero() {
		return fallback
	}
	if health.OperationalHealthy() {
		return PhaseHealthy
	}
	switch health.LastCode {
	case "TPROXY_PORT_DOWN":
		return PhaseCoreSpawned
	case "DNS_LISTENER_DOWN",
		"API_PORT_DOWN":
		return PhaseCoreListening
	case "RULES_NOT_APPLIED",
		"ROUTING_CHECK_FAILED",
		"ROUTING_V4_MISSING",
		"ROUTING_V6_MISSING",
		"ROUTING_NOT_APPLIED":
		return PhaseCoreListening
	case "DNS_APPLY_FAILED":
		return PhaseRulesApplied
	case "DNS_LOOKUP_TIMEOUT",
		"DNS_EMPTY_RESPONSE",
		"DNS_LOOKUP_FAILED",
		"PROXY_DNS_UNAVAILABLE":
		return PhaseDNSApplied
	case "OUTBOUND_URL_FAILED",
		"OPERATIONAL_DEGRADED":
		return PhaseOutboundChecked
	case "CORE_PID_MISSING",
		"CORE_PID_LOOKUP_FAILED",
		"CORE_PROCESS_DEAD",
		"CORE_LOG_OPEN_FAILED",
		"CORE_SPAWN_FAILED":
		return PhaseFailed
	case "CONFIG_RENDER_FAILED":
		return PhaseStarting
	case "CONFIG_CHECK_FAILED":
		return PhaseConfigChecked
	}
	if !health.Healthy() {
		return fallback
	}
	return PhaseDegraded
}

type runtimeCodedError interface {
	RuntimeCode() string
}

func healthFromError(err error) HealthSnapshot {
	health := HealthSnapshot{
		LastError: err.Error(),
		CheckedAt: time.Now(),
	}
	var coded runtimeCodedError
	if errors.As(err, &coded) {
		health.LastCode = coded.RuntimeCode()
	}
	return health
}

func firstError(errors []string) string {
	for _, err := range errors {
		if err != "" {
			return err
		}
	}
	return ""
}
