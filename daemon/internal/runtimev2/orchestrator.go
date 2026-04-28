package runtimev2

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

type Backend interface {
	Kind() BackendKind
	Supported() (bool, string)
	Start(desired DesiredState, generation int64) (*ResetReport, error)
	Stop() error
	Reset(generation int64) ResetReport
	Restart(desired DesiredState, generation int64) (*ResetReport, error)
	HandleNetworkChange(generation int64) (*ResetReport, error)
	CurrentHealth() HealthSnapshot
	RefreshHealth() HealthSnapshot
	TestNodes(desired DesiredState, url string, timeoutMS int, nodeIDs []string) ([]NodeProbeResult, error)
}

type Orchestrator struct {
	mu       sync.Mutex
	backends map[BackendKind]Backend

	desired           DesiredState
	applied           AppliedState
	health            HealthSnapshot
	compatibility     CompatibilityStatus
	active            *OperationStatus
	last              *OperationResult
	opSeq             uint64
	activeStuckLogged bool

	watchdogAfter  time.Duration
	logger         func(OperationLogEvent)
	statusObserver func(Status)
}

type operationCompletion struct {
	err         error
	health      HealthSnapshot
	applied     AppliedState
	resetReport *ResetReport
}

type OperationLogEvent struct {
	OperationID  string
	Kind         OperationKind
	Generation   int64
	Phase        Phase
	Step         string
	StepStatus   string
	StepDetail   string
	Result       string
	ErrorCode    string
	ErrorMessage string
	RuntimeMS    int64
	Stuck        bool
}

func NewOrchestrator(desired DesiredState, backends ...Backend) *Orchestrator {
	if desired.BackendKind == "" {
		desired.BackendKind = BackendRootTProxy
	}
	o := &Orchestrator{
		backends:      make(map[BackendKind]Backend, len(backends)),
		desired:       desired,
		watchdogAfter: 2 * time.Minute,
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

func (o *Orchestrator) SetOperationWatchdog(after time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.watchdogAfter = after
}

func (o *Orchestrator) SetOperationLogger(logger func(OperationLogEvent)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.logger = logger
}

func (o *Orchestrator) SetStatusObserver(observer func(Status)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.statusObserver = observer
}

func (o *Orchestrator) SetCompatibility(compatibility CompatibilityStatus) {
	o.mu.Lock()
	o.compatibility = cloneCompatibilityStatus(compatibility)
	status := o.statusLockedWithoutStuckLog()
	observer := o.statusObserver
	o.mu.Unlock()
	if observer != nil {
		observer(status)
	}
}

func (o *Orchestrator) SetActiveOperationStep(generation int64, name, status, code, detail string) bool {
	o.mu.Lock()
	if o.active == nil || o.active.Generation != generation {
		o.mu.Unlock()
		return false
	}
	o.active.Step = name
	o.active.StepStatus = status
	o.active.StepCode = code
	o.active.StepDetail = detail
	o.logLocked(OperationLogEvent{
		OperationID: o.active.OperationID,
		Kind:        o.active.Kind,
		Generation:  o.active.Generation,
		Phase:       o.active.Phase,
		Step:        name,
		StepStatus:  status,
		StepDetail:  detail,
		Result:      "step",
		ErrorCode:   code,
		RuntimeMS:   int64(time.Since(o.active.StartedAt) / time.Millisecond),
	})
	snapshot := o.statusLockedWithoutStuckLog()
	observer := o.statusObserver
	o.mu.Unlock()
	if observer != nil {
		observer(snapshot)
	}
	return true
}

func (o *Orchestrator) ApplyDesiredState(desired DesiredState) error {
	o.mu.Lock()

	if err := o.busyLocked(); err != nil {
		o.mu.Unlock()
		return err
	}
	if desired.BackendKind == "" {
		desired.BackendKind = o.desired.BackendKind
	}
	if err := o.validateDesiredLocked(desired); err != nil {
		o.mu.Unlock()
		return err
	}
	o.desired = desired
	status := o.statusLockedWithoutStuckLog()
	observer := o.statusObserver
	o.mu.Unlock()
	if observer != nil {
		observer(status)
	}
	return nil
}

func (o *Orchestrator) Status() Status {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.statusLocked()
}

func (o *Orchestrator) RefreshActiveProgress() Status {
	o.mu.Lock()
	if o.active == nil {
		status := o.statusLocked()
		o.mu.Unlock()
		return status
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
		return status
	}
	o.mu.Unlock()

	health := backend.CurrentHealth()

	o.mu.Lock()
	if o.active != nil && operationHealthHasProgress(health) {
		o.health = mergeOperationProgress(o.health, health)
	}
	status := o.statusLocked()
	o.mu.Unlock()
	return status
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
			resetReport, err := backend.Start(desired, op.Generation)
			if err != nil {
				health := healthFromError(err)
				if errorReport := resetReportFromError(err); errorReport != nil {
					resetReport = errorReport
				}
				return operationCompletion{
					err:         err,
					health:      health,
					resetReport: resetReport,
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
				health:      health,
				resetReport: resetReport,
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
			var resetReport *ResetReport
			if stopErr != nil && active.Phase != PhaseStopped {
				report := backend.Reset(op.Generation)
				resetReport = &report
				if report.Status != "ok" {
					return operationCompletion{
						err:         stopErr,
						health:      HealthSnapshot{LastError: stopErr.Error(), CheckedAt: time.Now()},
						resetReport: resetReport,
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
				err:         stopErr,
				health:      health,
				resetReport: resetReport,
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
			resetReport, err := backend.Restart(desired, op.Generation)
			if err != nil {
				health := healthFromError(err)
				if errorReport := resetReportFromError(err); errorReport != nil {
					resetReport = errorReport
				}
				return operationCompletion{
					err:         err,
					health:      health,
					resetReport: resetReport,
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
				health:      health,
				resetReport: resetReport,
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
			resetReport, err := backend.HandleNetworkChange(op.Generation)
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
			return operationCompletion{
				err:         err,
				health:      health,
				applied:     applied,
				resetReport: firstResetReport(resetReport, resetReportFromError(err)),
			}
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
				resetReport := resetReportFromError(err)
				return operationCompletion{
					err:         err,
					health:      health,
					resetReport: resetReport,
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
		status := o.statusLockedWithoutStuckLog()
		observer := o.statusObserver
		o.mu.Unlock()
		if observer != nil {
			observer(status)
		}
		return health
	}
	o.mu.Unlock()

	health := backend.RefreshHealth()

	o.mu.Lock()
	o.health = health
	if o.active == nil && o.applied.Phase != PhaseStopped {
		o.applied.Phase = phaseFromHealth(health, o.applied.Phase)
	}
	status := o.statusLockedWithoutStuckLog()
	observer := o.statusObserver
	o.mu.Unlock()
	if observer != nil {
		observer(status)
	}
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
	o.activeStuckLogged = false
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
	status := o.statusLockedWithoutStuckLog()
	o.logLocked(OperationLogEvent{
		OperationID: op.OperationID,
		Kind:        op.Kind,
		Generation:  op.Generation,
		Phase:       op.Phase,
		Result:      "accepted",
	})
	observer := o.statusObserver
	o.mu.Unlock()

	if observer != nil {
		observer(status)
	}
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
		status := o.statusLockedWithoutStuckLog()
		observer := o.statusObserver
		o.mu.Unlock()
		if observer != nil {
			observer(status)
		}
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
	active := o.active
	if active != nil && active.OperationID == op.OperationID {
		o.active = nil
	}
	result := "ok"
	if completion.err != nil {
		result = "failed"
	}
	step := operationStepFromReport(o.health.StageReport)
	if step.Name == "" && active != nil && active.OperationID == op.OperationID {
		step = operationStep{
			Name:   active.Step,
			Status: active.StepStatus,
			Code:   active.StepCode,
			Detail: active.StepDetail,
		}
	}
	logEvent := OperationLogEvent{
		OperationID:  op.OperationID,
		Kind:         op.Kind,
		Generation:   op.Generation,
		Phase:        o.applied.Phase,
		Step:         step.Name,
		StepStatus:   step.Status,
		StepDetail:   step.Detail,
		Result:       result,
		ErrorCode:    errorCode(completion.err),
		RuntimeMS:    int64(time.Since(op.StartedAt) / time.Millisecond),
		ErrorMessage: "",
	}
	if completion.err != nil {
		logEvent.ErrorMessage = completion.err.Error()
		if logEvent.ErrorCode == "" {
			logEvent.ErrorCode = completion.health.LastCode
		}
	}
	if o.watchdogAfter > 0 {
		logEvent.Stuck = time.Since(op.StartedAt) > o.watchdogAfter
	}
	o.logLocked(logEvent)
}

func (o *Orchestrator) busyLocked() error {
	if o.active == nil {
		return nil
	}
	return NewRuntimeBusyError(*o.cloneActiveOperationLocked(time.Now()))
}

func (o *Orchestrator) statusLocked() Status {
	return o.statusLockedWithStuckLog(true)
}

func (o *Orchestrator) statusLockedWithoutStuckLog() Status {
	return o.statusLockedWithStuckLog(false)
}

func (o *Orchestrator) statusLockedWithStuckLog(logStuck bool) Status {
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
		active = o.cloneActiveOperationLocked(time.Now())
		if logStuck && active.Stuck && active.RuntimeMS > 0 && active.Step != "" && !o.activeStuckLogged {
			o.activeStuckLogged = true
			o.logLocked(OperationLogEvent{
				OperationID: active.OperationID,
				Kind:        active.Kind,
				Generation:  active.Generation,
				Phase:       active.Phase,
				Step:        active.Step,
				StepStatus:  active.StepStatus,
				StepDetail:  active.StepDetail,
				Result:      "stuck",
				ErrorCode:   "OPERATION_STUCK",
				RuntimeMS:   active.RuntimeMS,
				Stuck:       true,
			})
		}
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
		Compatibility:   cloneCompatibilityStatus(o.compatibility),
		ActiveOperation: active,
		LastOperation:   last,
	}
}

func cloneCompatibilityStatus(compatibility CompatibilityStatus) CompatibilityStatus {
	copy := compatibility
	copy.Capabilities = append([]string(nil), compatibility.Capabilities...)
	copy.SupportedMethods = append([]string(nil), compatibility.SupportedMethods...)
	copy.Methods = append([]MethodCapability(nil), compatibility.Methods...)
	return copy
}

func (o *Orchestrator) cloneActiveOperationLocked(now time.Time) *OperationStatus {
	if o.active == nil {
		return nil
	}
	copy := *o.active
	runtimeMS := int64(now.Sub(copy.StartedAt) / time.Millisecond)
	if runtimeMS < 0 {
		runtimeMS = 0
	}
	copy.RuntimeMS = runtimeMS
	if o.watchdogAfter > 0 {
		copy.WatchdogAfterMS = durationMillisCeil(o.watchdogAfter)
		copy.Stuck = now.Sub(copy.StartedAt) > o.watchdogAfter
	}
	if step := operationStepFromReport(o.health.StageReport); step.Name != "" {
		copy.Step = step.Name
		copy.StepStatus = step.Status
		copy.StepCode = step.Code
		copy.StepDetail = step.Detail
	}
	return &copy
}

func durationMillisCeil(duration time.Duration) int64 {
	ms := int64(duration / time.Millisecond)
	if duration%time.Millisecond != 0 {
		ms++
	}
	if ms < 1 {
		return 1
	}
	return ms
}

func (o *Orchestrator) logLocked(event OperationLogEvent) {
	if o.logger != nil {
		o.logger(event)
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

type runtimeResetReportError interface {
	RuntimeResetReport() ResetReport
}

type operationStep struct {
	Name   string
	Status string
	Code   string
	Detail string
}

func operationHealthHasProgress(health HealthSnapshot) bool {
	return health.StageReport != nil ||
		health.LastCode != "" ||
		health.LastError != "" ||
		!health.CheckedAt.IsZero()
}

func mergeOperationProgress(current HealthSnapshot, progress HealthSnapshot) HealthSnapshot {
	if progress.StageReport != nil {
		current.StageReport = progress.StageReport
	}
	if progress.LastCode != "" {
		current.LastCode = progress.LastCode
	}
	if progress.LastError != "" {
		current.LastError = progress.LastError
	}
	if progress.LastUserMessage != "" {
		current.LastUserMessage = progress.LastUserMessage
	}
	if progress.LastDebug != "" {
		current.LastDebug = progress.LastDebug
	}
	if progress.RollbackApplied {
		current.RollbackApplied = true
	}
	if !progress.CheckedAt.IsZero() {
		current.CheckedAt = progress.CheckedAt
	}
	if len(progress.Checks) > 0 {
		current.Checks = progress.Checks
	}
	return current
}

func operationStepFromReport(report interface{}) operationStep {
	if report == nil {
		return operationStep{}
	}
	if step := operationStepFromMap(report); step.Name != "" {
		return step
	}
	return operationStepFromStruct(reflect.ValueOf(report))
}

func operationStepFromMap(report interface{}) operationStep {
	obj, ok := report.(map[string]interface{})
	if !ok {
		return operationStep{}
	}
	failedStage, _ := obj["failedStage"].(string)
	stages, _ := obj["stages"].([]interface{})
	if failedStage != "" {
		for _, rawStage := range stages {
			stage := operationStageFromMap(rawStage)
			if stage.Name == failedStage {
				return stage
			}
		}
		return operationStep{Name: failedStage, Status: "failed"}
	}
	for i := len(stages) - 1; i >= 0; i-- {
		if stage := operationStageFromMap(stages[i]); stage.Name != "" {
			return stage
		}
	}
	return operationStep{}
}

func operationStageFromMap(raw interface{}) operationStep {
	stage, ok := raw.(map[string]interface{})
	if !ok {
		return operationStep{}
	}
	return operationStep{
		Name:   stringFromMap(stage, "name"),
		Status: stringFromMap(stage, "status"),
		Code:   stringFromMap(stage, "code"),
		Detail: stringFromMap(stage, "detail"),
	}
}

func stringFromMap(obj map[string]interface{}, key string) string {
	value, _ := obj[key].(string)
	return value
}

func operationStepFromStruct(value reflect.Value) operationStep {
	if !value.IsValid() {
		return operationStep{}
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return operationStep{}
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return operationStep{}
	}
	failedStage := stringField(value, "FailedStage")
	stages := value.FieldByName("Stages")
	if stages.IsValid() && stages.Kind() == reflect.Slice {
		if failedStage != "" {
			for i := 0; i < stages.Len(); i++ {
				stage := operationStepFromStageValue(stages.Index(i))
				if stage.Name == failedStage {
					return stage
				}
			}
			return operationStep{Name: failedStage, Status: "failed"}
		}
		for i := stages.Len() - 1; i >= 0; i-- {
			if stage := operationStepFromStageValue(stages.Index(i)); stage.Name != "" {
				return stage
			}
		}
	}
	return operationStep{}
}

func operationStepFromStageValue(value reflect.Value) operationStep {
	if !value.IsValid() {
		return operationStep{}
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return operationStep{}
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return operationStep{}
	}
	return operationStep{
		Name:   stringField(value, "Name"),
		Status: stringField(value, "Status"),
		Code:   stringField(value, "Code"),
		Detail: stringField(value, "Detail"),
	}
}

func stringField(value reflect.Value, name string) string {
	field := value.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.String {
		return ""
	}
	return field.String()
}

func resetReportFromError(err error) *ResetReport {
	if err == nil {
		return nil
	}
	var withReport runtimeResetReportError
	if errors.As(err, &withReport) {
		report := withReport.RuntimeResetReport()
		return cloneResetReport(report)
	}
	return nil
}

func firstResetReport(reports ...*ResetReport) *ResetReport {
	for _, report := range reports {
		if report != nil {
			return report
		}
	}
	return nil
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
