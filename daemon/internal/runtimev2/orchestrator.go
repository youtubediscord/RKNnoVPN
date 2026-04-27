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
	last    *OperationResult
	opSeq   uint64
}

type operationCompletion struct {
	err         error
	health      HealthSnapshot
	applied     AppliedState
	resetReport *ResetReport
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

	return o.submitLocked(
		OperationStart,
		PhaseStarting,
		func(op OperationStatus) {
			o.applied = AppliedState{
				BackendKind:     desired.BackendKind,
				Phase:           PhaseStarting,
				ActiveProfileID: desired.ActiveProfileID,
				Generation:      op.Generation,
			}
		},
		func(op OperationStatus) operationCompletion {
			if err := backend.Start(desired); err != nil {
				health := healthFromError(err)
				return operationCompletion{
					err:    err,
					health: health,
					applied: AppliedState{
						BackendKind:     desired.BackendKind,
						Phase:           phaseFromHealth(health, PhaseFailed),
						ActiveProfileID: desired.ActiveProfileID,
						Generation:      op.Generation,
					},
				}
			}

			health := backend.RefreshHealth()
			return operationCompletion{
				health: health,
				applied: AppliedState{
					BackendKind:     desired.BackendKind,
					Phase:           phaseFromHealth(health, PhaseHealthy),
					ActiveProfileID: desired.ActiveProfileID,
					StartedAt:       op.StartedAt,
					Generation:      op.Generation,
				},
			}
		},
	)
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
	active := o.applied
	return o.submitLocked(
		OperationStop,
		PhaseStopping,
		func(op OperationStatus) {
			o.applied = AppliedState{
				BackendKind:     backendKind,
				Phase:           PhaseStopping,
				ActiveProfileID: active.ActiveProfileID,
				StartedAt:       active.StartedAt,
				Generation:      op.Generation,
			}
		},
		func(op OperationStatus) operationCompletion {
			stopErr := backend.Stop()
			if stopErr != nil && active.Phase != PhaseStopped {
				report := backend.Reset(op.Generation)
				if report.Status != "ok" {
					return operationCompletion{
						err:    stopErr,
						health: HealthSnapshot{LastError: stopErr.Error(), CheckedAt: time.Now()},
						applied: AppliedState{
							BackendKind:     active.BackendKind,
							Phase:           PhaseFailed,
							ActiveProfileID: active.ActiveProfileID,
							StartedAt:       active.StartedAt,
							Generation:      op.Generation,
						},
					}
				}
			}
			health := HealthSnapshot{CheckedAt: time.Now()}
			if stopErr != nil {
				health.LastError = stopErr.Error()
			}
			return operationCompletion{
				err:    stopErr,
				health: health,
				applied: AppliedState{
					BackendKind:     backendKind,
					Phase:           PhaseStopped,
					ActiveProfileID: active.ActiveProfileID,
					Generation:      op.Generation,
				},
			}
		},
	)
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
	return o.submitLocked(
		OperationRestart,
		PhaseStarting,
		func(op OperationStatus) {
			o.applied = AppliedState{
				BackendKind:     desired.BackendKind,
				Phase:           PhaseStarting,
				ActiveProfileID: desired.ActiveProfileID,
				Generation:      op.Generation,
			}
		},
		func(op OperationStatus) operationCompletion {
			if err := backend.Restart(desired, op.Generation); err != nil {
				health := healthFromError(err)
				return operationCompletion{
					err:    err,
					health: health,
					applied: AppliedState{
						BackendKind:     desired.BackendKind,
						Phase:           phaseFromHealth(health, PhaseFailed),
						ActiveProfileID: desired.ActiveProfileID,
						Generation:      op.Generation,
					},
				}
			}

			health := backend.RefreshHealth()
			return operationCompletion{
				health: health,
				applied: AppliedState{
					BackendKind:     desired.BackendKind,
					Phase:           phaseFromHealth(health, PhaseHealthy),
					ActiveProfileID: desired.ActiveProfileID,
					StartedAt:       op.StartedAt,
					Generation:      op.Generation,
				},
			}
		},
	)
}

func (o *Orchestrator) Reset() (Status, error) {
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
	active := o.applied
	desired := o.desired
	return o.submitLocked(
		OperationReset,
		PhaseResetting,
		func(op OperationStatus) {
			o.applied = AppliedState{
				BackendKind:     backendKind,
				Phase:           PhaseResetting,
				ActiveProfileID: active.ActiveProfileID,
				Generation:      op.Generation,
			}
		},
		func(op OperationStatus) operationCompletion {
			report := backend.Reset(op.Generation)
			health := HealthSnapshot{CheckedAt: time.Now()}
			var err error
			if len(report.Errors) > 0 || report.Status == "partial" || report.Status == "failed" {
				message := firstError(report.Errors)
				if message == "" {
					message = "reset failed"
				}
				err = errors.New(message)
				health.LastError = message
			}
			return operationCompletion{
				err:         err,
				health:      health,
				resetReport: &report,
				applied: AppliedState{
					BackendKind:     backendKind,
					Phase:           PhaseStopped,
					ActiveProfileID: desired.ActiveProfileID,
					Generation:      op.Generation,
				},
			}
		},
	)
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
	active := o.applied
	return o.submitLocked(
		OperationNetworkChange,
		PhaseStarting,
		func(op OperationStatus) {
			o.applied.Phase = PhaseStarting
			o.applied.Generation = op.Generation
		},
		func(op OperationStatus) operationCompletion {
			err := backend.HandleNetworkChange(op.Generation)
			health := backend.CurrentHealth()
			if health.CheckedAt.IsZero() {
				health = backend.RefreshHealth()
			}
			applied := AppliedState{
				BackendKind:     active.BackendKind,
				Phase:           phaseFromHealth(health, PhaseHealthy),
				ActiveProfileID: active.ActiveProfileID,
				StartedAt:       active.StartedAt,
				Generation:      op.Generation,
			}
			if err != nil {
				applied.Phase = PhaseDegraded
				if health.LastError == "" {
					health.LastError = err.Error()
				}
				if health.CheckedAt.IsZero() {
					health.CheckedAt = time.Now()
				}
			}
			return operationCompletion{err: err, health: health, applied: applied}
		},
	)
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
	active := o.applied
	return o.submitLocked(
		kind,
		phase,
		func(op OperationStatus) {
			o.applied.Phase = phase
			o.applied.Generation = op.Generation
		},
		func(op OperationStatus) operationCompletion {
			err := fn(op.Generation)
			if err != nil {
				health := healthFromError(err)
				return operationCompletion{
					err:    err,
					health: health,
					applied: AppliedState{
						BackendKind:     active.BackendKind,
						Phase:           phaseFromHealth(health, PhaseFailed),
						ActiveProfileID: active.ActiveProfileID,
						StartedAt:       active.StartedAt,
						Generation:      op.Generation,
					},
				}
			}
			o.mu.Lock()
			health := o.health
			o.mu.Unlock()
			applied := active
			if kind == OperationUpdateInstall {
				applied.Phase = PhaseStopped
			} else if !health.CheckedAt.IsZero() && !health.CheckedAt.Before(op.StartedAt) {
				applied.Phase = phaseFromHealth(health, PhaseHealthy)
			}
			applied.Generation = op.Generation
			return operationCompletion{applied: applied}
		},
	)
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

func (o *Orchestrator) submitLocked(
	kind OperationKind,
	phase Phase,
	configure func(OperationStatus),
	run func(OperationStatus) operationCompletion,
) (Status, error) {
	op := o.beginOperationLocked(kind, phase)
	if configure != nil {
		configure(op)
	}
	status := o.statusLocked()
	o.mu.Unlock()

	go o.runSubmittedOperation(op, run)
	return status, nil
}

func (o *Orchestrator) runSubmittedOperation(op OperationStatus, run func(OperationStatus) operationCompletion) {
	var completion operationCompletion
	defer func() {
		if recovered := recover(); recovered != nil {
			o.mu.Lock()
			active := o.applied
			backendKind := active.BackendKind
			if backendKind == "" {
				backendKind = o.desired.BackendKind
			}
			completion = operationCompletion{
				err: errors.New(fmt.Sprint(recovered)),
				health: HealthSnapshot{
					LastCode:  "PANIC",
					LastError: fmt.Sprint(recovered),
					CheckedAt: time.Now(),
				},
				applied: AppliedState{
					BackendKind:     backendKind,
					Phase:           PhaseFailed,
					ActiveProfileID: active.ActiveProfileID,
					StartedAt:       active.StartedAt,
					Generation:      op.Generation,
				},
			}
		} else {
			o.mu.Lock()
		}
		o.finishOperationLocked(op, completion)
		o.mu.Unlock()
	}()
	if run != nil {
		completion = run(op)
	}
}

func (o *Orchestrator) finishOperationLocked(op OperationStatus, completion operationCompletion) {
	if !completion.health.CheckedAt.IsZero() || completion.health.LastError != "" || completion.health.LastCode != "" {
		o.health = completion.health
	}
	if completion.applied.Phase != "" {
		o.applied = completion.applied
	}
	if completion.err != nil && completion.health.CheckedAt.IsZero() {
		o.health = healthFromError(completion.err)
	}
	o.last = operationResult(op, completion, time.Now())
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
	var last *OperationResult
	if o.last != nil {
		last = cloneOperationResult(*o.last)
	}
	return Status{
		DesiredState:    o.desired,
		AppliedState:    o.applied,
		Health:          o.health,
		Capabilities:    caps,
		ActiveOperation: active,
		LastOperation:   last,
	}
}

func cloneOperation(op OperationStatus) *OperationStatus {
	copy := op
	return &copy
}

func cloneOperationResult(result OperationResult) *OperationResult {
	copy := result
	if result.ResetReport != nil {
		copy.ResetReport = cloneResetReport(*result.ResetReport)
	}
	return &copy
}

func cloneResetReport(report ResetReport) *ResetReport {
	copy := report
	copy.Steps = append([]ResetStep(nil), report.Steps...)
	copy.Warnings = append([]string(nil), report.Warnings...)
	copy.Errors = append([]string(nil), report.Errors...)
	copy.Leftovers = append([]string(nil), report.Leftovers...)
	return &copy
}

func operationResult(op OperationStatus, completion operationCompletion, finishedAt time.Time) *OperationResult {
	result := &OperationResult{
		OperationID: op.OperationID,
		Kind:        op.Kind,
		Generation:  op.Generation,
		Phase:       op.Phase,
		StartedAt:   op.StartedAt,
		FinishedAt:  finishedAt,
		Succeeded:   completion.err == nil,
	}
	if completion.err != nil {
		result.ErrorCode = errorCode(completion.err)
		if result.ErrorCode == "" {
			result.ErrorCode = completion.health.LastCode
		}
		result.ErrorMessage = completion.err.Error()
	}
	if completion.resetReport != nil {
		result.ResetReport = cloneResetReport(*completion.resetReport)
	}
	return result
}

func errorCode(err error) string {
	var coded runtimeCodedError
	if errors.As(err, &coded) {
		return coded.RuntimeCode()
	}
	return ""
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
