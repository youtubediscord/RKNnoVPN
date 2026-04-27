package runtimev2

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	active  *OperationStatus
	opSeq   uint64
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

	if err := o.busyLocked(); err != nil {
		return err
	}
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
	if err := o.busyLocked(); err != nil {
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
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

	op := o.beginOperationLocked(OperationStart, PhaseStarting)
	o.applied = AppliedState{
		BackendKind:     desired.BackendKind,
		Phase:           PhaseStarting,
		ActiveProfileID: desired.ActiveProfileID,
		Generation:      op.Generation,
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
			Generation:      op.Generation,
		}
		o.finishOperationLocked(op)
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
		StartedAt:       op.StartedAt,
		Generation:      op.Generation,
	}
	o.finishOperationLocked(op)
	status := o.statusLocked()
	o.mu.Unlock()
	return status, nil
}

func (o *Orchestrator) Stop() (Status, error) {
	o.mu.Lock()
	if err := o.busyLocked(); err != nil {
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
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
		op := o.beginOperationLocked(OperationStop, PhaseStopping)
		active := o.applied
		o.applied = AppliedState{
			BackendKind:     backendKind,
			Phase:           PhaseStopping,
			ActiveProfileID: active.ActiveProfileID,
			Generation:      op.Generation,
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
			Generation:      op.Generation,
		}
		o.finishOperationLocked(op)
		status := o.statusLocked()
		o.mu.Unlock()
		return status, stopErr
	}
	op := o.beginOperationLocked(OperationStop, PhaseStopping)
	active := o.applied
	o.applied.Phase = PhaseStopping
	o.applied.Generation = op.Generation
	o.mu.Unlock()

	stopErr := backend.Stop()
	if stopErr != nil {
		report := backend.Reset(op.Generation)
		if report.Status != "ok" {
			o.mu.Lock()
			o.health = HealthSnapshot{LastError: stopErr.Error(), CheckedAt: time.Now()}
			o.applied = AppliedState{
				BackendKind:     active.BackendKind,
				Phase:           PhaseFailed,
				ActiveProfileID: active.ActiveProfileID,
				StartedAt:       active.StartedAt,
				Generation:      op.Generation,
			}
			o.finishOperationLocked(op)
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
		Generation:      op.Generation,
	}
	o.finishOperationLocked(op)
	status := o.statusLocked()
	o.mu.Unlock()
	return status, nil
}

func (o *Orchestrator) Restart() (Status, error) {
	o.mu.Lock()
	if err := o.busyLocked(); err != nil {
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
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
	op := o.beginOperationLocked(OperationRestart, PhaseStarting)
	o.applied = AppliedState{
		BackendKind:     desired.BackendKind,
		Phase:           PhaseStarting,
		ActiveProfileID: desired.ActiveProfileID,
		Generation:      op.Generation,
	}
	o.mu.Unlock()

	if err := backend.Restart(desired, op.Generation); err != nil {
		health := healthFromError(err)
		o.mu.Lock()
		o.health = health
		o.applied = AppliedState{
			BackendKind:     desired.BackendKind,
			Phase:           phaseFromHealth(health, PhaseFailed),
			ActiveProfileID: desired.ActiveProfileID,
			Generation:      op.Generation,
		}
		o.finishOperationLocked(op)
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
		StartedAt:       op.StartedAt,
		Generation:      op.Generation,
	}
	o.finishOperationLocked(op)
	status := o.statusLocked()
	o.mu.Unlock()
	return status, nil
}

func (o *Orchestrator) Reset() (ResetReport, error) {
	o.mu.Lock()
	if err := o.busyLocked(); err != nil {
		o.mu.Unlock()
		return ResetReport{}, err
	}
	backendKind := o.applied.BackendKind
	if backendKind == "" {
		backendKind = o.desired.BackendKind
	}
	backend, err := o.backendForLocked(backendKind)
	op := o.beginOperationLocked(OperationReset, PhaseResetting)
	if err != nil {
		report := ResetReport{
			BackendKind: backendKind,
			Generation:  op.Generation,
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
			Generation:  op.Generation,
		}
		o.finishOperationLocked(op)
		o.mu.Unlock()
		return report, nil
	}
	o.applied = AppliedState{
		BackendKind:     backendKind,
		Phase:           PhaseResetting,
		ActiveProfileID: o.applied.ActiveProfileID,
		Generation:      op.Generation,
	}
	o.mu.Unlock()

	report := backend.Reset(op.Generation)

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
		Generation:      op.Generation,
	}
	o.finishOperationLocked(op)
	o.mu.Unlock()
	return report, nil
}

func (o *Orchestrator) HandleNetworkChange() (Status, error) {
	o.mu.Lock()
	if err := o.busyLocked(); err != nil {
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
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
	op := o.beginOperationLocked(OperationNetworkChange, PhaseStarting)
	active := o.applied
	o.applied.Phase = PhaseStarting
	o.applied.Generation = op.Generation
	o.mu.Unlock()

	err = backend.HandleNetworkChange(op.Generation)
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
		Generation:      op.Generation,
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
	o.finishOperationLocked(op)
	status := o.statusLocked()
	o.mu.Unlock()
	return status, err
}

func (o *Orchestrator) RunOperation(kind OperationKind, phase Phase, fn func(generation int64) error) (Status, error) {
	o.mu.Lock()
	if err := o.busyLocked(); err != nil {
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
	if phase == "" {
		phase = PhaseApplying
	}
	op := o.beginOperationLocked(kind, phase)
	active := o.applied
	o.applied.Phase = phase
	o.applied.Generation = op.Generation
	o.mu.Unlock()

	err := fn(op.Generation)

	o.mu.Lock()
	if err != nil {
		health := healthFromError(err)
		o.health = health
		o.applied = AppliedState{
			BackendKind:     active.BackendKind,
			Phase:           phaseFromHealth(health, PhaseFailed),
			ActiveProfileID: active.ActiveProfileID,
			StartedAt:       active.StartedAt,
			Generation:      op.Generation,
		}
		o.finishOperationLocked(op)
		status := o.statusLocked()
		o.mu.Unlock()
		return status, err
	}
	if !o.health.CheckedAt.IsZero() {
		o.applied.Phase = phaseFromHealth(o.health, PhaseHealthy)
	} else {
		o.applied.Phase = active.Phase
	}
	o.applied.Generation = op.Generation
	o.finishOperationLocked(op)
	status := o.statusLocked()
	o.mu.Unlock()
	return status, nil
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
	if o.active == nil && o.applied.Phase != PhaseStopped {
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

func (o *Orchestrator) beginOperationLocked(kind OperationKind, phase Phase) OperationStatus {
	generation := o.nextGenerationLocked()
	op := OperationStatus{
		OperationID: fmt.Sprintf("%d", atomic.AddUint64(&o.opSeq, 1)),
		Kind:        kind,
		Generation:  generation,
		Phase:       phase,
		StartedAt:   time.Now(),
	}
	o.active = cloneOperation(op)
	return op
}

func (o *Orchestrator) finishOperationLocked(op OperationStatus) {
	if o.active != nil && o.active.OperationID == op.OperationID {
		o.active = nil
	}
}

func (o *Orchestrator) busyLocked() error {
	if o.active == nil {
		return nil
	}
	return NewRuntimeBusyError(*o.active)
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
	var active *OperationStatus
	if o.active != nil {
		active = cloneOperation(*o.active)
	}
	return Status{
		DesiredState:    o.desired,
		AppliedState:    o.applied,
		Health:          o.health,
		Capabilities:    caps,
		ActiveOperation: active,
	}
}

func cloneOperation(op OperationStatus) *OperationStatus {
	copy := op
	return &copy
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
		"NETSTACK_CLEANUP_FAILED",
		"NETSTACK_VERIFY_FAILED",
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

type runtimeUserMessageError interface {
	RuntimeUserMessage() string
}

type runtimeDebugError interface {
	RuntimeDebug() string
}

type runtimeRollbackError interface {
	RuntimeRollbackApplied() bool
}

type runtimeStageReportError interface {
	RuntimeStageReport() interface{}
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
	var userMessage runtimeUserMessageError
	if errors.As(err, &userMessage) {
		health.LastUserMessage = userMessage.RuntimeUserMessage()
	}
	var debug runtimeDebugError
	if errors.As(err, &debug) {
		health.LastDebug = debug.RuntimeDebug()
	}
	var rollback runtimeRollbackError
	if errors.As(err, &rollback) {
		health.RollbackApplied = rollback.RuntimeRollbackApplied()
	}
	var stageReport runtimeStageReportError
	if errors.As(err, &stageReport) {
		health.StageReport = stageReport.RuntimeStageReport()
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
