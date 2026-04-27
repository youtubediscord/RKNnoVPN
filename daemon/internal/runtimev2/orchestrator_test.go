package runtimev2

import (
	"errors"
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

func TestStatusIncludesCompatibilitySnapshot(t *testing.T) {
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, &fakeBackend{kind: BackendRootTProxy})
	compatibility := CompatibilityStatus{
		DaemonVersion:          "v1.7.0",
		ModuleVersion:          "v1.7.0",
		CurrentReleaseVersion:  "v1.7.0",
		CurrentReleaseOK:       true,
		ControlProtocolVersion: 3,
		SchemaVersion:          4,
		PanelMinVersion:        "v1.7.0",
		Capabilities:           []string{"backend.root-tproxy"},
		SupportedMethods:       []string{"backend.status"},
	}
	o.SetCompatibility(compatibility)
	compatibility.Capabilities[0] = "mutated"
	compatibility.SupportedMethods[0] = "mutated"

	status := o.Status()
	if status.Compatibility.ControlProtocolVersion != 3 || status.Compatibility.SchemaVersion != 4 {
		t.Fatalf("expected compatibility protocol/schema in status, got %#v", status.Compatibility)
	}
	if got := status.Compatibility.Capabilities; len(got) != 1 || got[0] != "backend.root-tproxy" {
		t.Fatalf("compatibility capabilities were not cloned: %#v", got)
	}
	if got := status.Compatibility.SupportedMethods; len(got) != 1 || got[0] != "backend.status" {
		t.Fatalf("compatibility methods were not cloned: %#v", got)
	}
}

func TestStopCallsBackendEvenWhenAppliedPhaseStopped(t *testing.T) {
	backend := &fakeBackend{kind: BackendRootTProxy}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	status, err := o.Stop()
	if err != nil {
		t.Fatalf("stop returned error: %v", err)
	}
	status = waitForOperationDone(t, o, OperationStop)
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
	if err != nil {
		t.Fatalf("start acceptance returned error: %v", err)
	}
	status = waitForOperationDone(t, o, OperationStart)
	if status.LastOperation == nil || status.LastOperation.Succeeded {
		t.Fatalf("expected failed last operation, got %#v", status.LastOperation)
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

func TestStartSubmitReturnsBeforeBlockedBackend(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBackend{kind: BackendRootTProxy, startStarted: started, startBlock: release}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	status, err := o.Start()
	if err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	if status.ActiveOperation == nil || status.ActiveOperation.Kind != OperationStart {
		t.Fatalf("expected accepted active start, got %#v", status)
	}

	waitForSignal(t, started)
	close(release)
	status = waitForOperationDone(t, o, OperationStart)
	if status.ActiveOperation != nil {
		t.Fatalf("expected active operation to be cleared: %#v", status.ActiveOperation)
	}
	if status.LastOperation == nil || !status.LastOperation.Succeeded {
		t.Fatalf("expected successful last operation, got %#v", status.LastOperation)
	}
}

func TestActiveOperationReportsWatchdogStuck(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBackend{
		kind:         BackendRootTProxy,
		startStarted: started,
		startBlock:   release,
		currentHealth: HealthSnapshot{
			StageReport: map[string]interface{}{
				"stages": []interface{}{
					map[string]interface{}{
						"name":   "netstack-apply",
						"status": "running",
						"code":   "RULES_APPLYING",
						"detail": "applying rules",
					},
				},
			},
			CheckedAt: time.Now(),
		},
	}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)
	o.SetOperationWatchdog(time.Nanosecond)
	var logs []OperationLogEvent
	o.SetOperationLogger(func(event OperationLogEvent) {
		logs = append(logs, event)
	})

	if _, err := o.Start(); err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	done := operationDoneChan(o, OperationStart)
	waitForSignal(t, started)
	time.Sleep(time.Millisecond)

	status := o.RefreshActiveProgress()
	if status.ActiveOperation == nil {
		t.Fatalf("expected active operation: %#v", status)
	}
	if !status.ActiveOperation.Stuck || status.ActiveOperation.WatchdogAfterMS == 0 {
		t.Fatalf("expected stuck active operation with watchdog metadata, got %#v", status.ActiveOperation)
	}
	if status.ActiveOperation.Step != "netstack-apply" ||
		status.ActiveOperation.StepStatus != "running" ||
		status.ActiveOperation.StepCode != "RULES_APPLYING" ||
		status.ActiveOperation.StepDetail != "applying rules" {
		t.Fatalf("expected active operation step metadata, got %#v", status.ActiveOperation)
	}
	_, err := o.Reset()
	busy, ok := err.(*OperationBusyError)
	if !ok || busy.Active == nil || !busy.Active.Stuck || busy.Active.RuntimeMS == 0 {
		t.Fatalf("expected busy error to include stuck active operation, got %T %#v", err, err)
	}
	if data := busy.Data(); data["stuck"] != true || data["runtimeMs"] == int64(0) || data["step"] != "netstack-apply" {
		t.Fatalf("expected busy data to include watchdog fields, got %#v", data)
	}
	if countOperationLogs(logs, "stuck") != 1 {
		t.Fatalf("expected exactly one stuck log event, got %#v", logs)
	}
	stuckLog := operationLogByResult(logs, "stuck")
	if stuckLog.Step != "netstack-apply" {
		t.Fatalf("expected stuck log to include step, got %#v", stuckLog)
	}
	_ = o.Status()
	if countOperationLogs(logs, "stuck") != 1 {
		t.Fatalf("status polling should not spam stuck log events, got %#v", logs)
	}

	close(release)
	waitForSignal(t, done)
}

func TestSetActiveOperationStepUpdatesStatusBusyAndLogs(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBackend{
		kind:         BackendRootTProxy,
		startStarted: started,
		startBlock:   release,
	}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)
	var logs []OperationLogEvent
	o.SetOperationLogger(func(event OperationLogEvent) {
		logs = append(logs, event)
	})

	if _, err := o.Start(); err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	done := operationDoneChan(o, OperationStart)
	waitForSignal(t, started)
	status := o.Status()
	if status.ActiveOperation == nil {
		t.Fatalf("expected active operation: %#v", status)
	}
	generation := status.ActiveOperation.Generation

	if !o.SetActiveOperationStep(generation, "update-install-apk", "running", "APK_INSTALLING", "installing apk") {
		t.Fatalf("expected active step update to succeed")
	}
	if o.SetActiveOperationStep(generation+1, "stale", "running", "", "") {
		t.Fatalf("stale generation must not update active step")
	}
	status = o.Status()
	if status.ActiveOperation == nil ||
		status.ActiveOperation.Step != "update-install-apk" ||
		status.ActiveOperation.StepStatus != "running" ||
		status.ActiveOperation.StepCode != "APK_INSTALLING" ||
		status.ActiveOperation.StepDetail != "installing apk" {
		t.Fatalf("expected active step metadata, got %#v", status.ActiveOperation)
	}
	_, err := o.Reset()
	busy, ok := err.(*OperationBusyError)
	if !ok || busy.Active == nil || busy.Active.Step != "update-install-apk" {
		t.Fatalf("expected busy error to carry active step, got %T %#v", err, err)
	}
	stepLog := operationLogByResult(logs, "step")
	if stepLog.Step != "update-install-apk" ||
		stepLog.StepStatus != "running" ||
		stepLog.StepDetail != "installing apk" ||
		stepLog.ErrorCode != "APK_INSTALLING" {
		t.Fatalf("expected structured step log, got %#v", stepLog)
	}

	close(release)
	waitForSignal(t, done)
}

func TestFailedStartCanAttachRecoveryResetReport(t *testing.T) {
	report := ResetReport{
		BackendKind: BackendRootTProxy,
		Status:      "clean_with_warnings",
		Steps:       []ResetStep{{Name: "verify-cleanup", Status: "warning", Detail: "leftover"}},
		Warnings:    []string{"leftover"},
	}
	backend := &fakeBackend{
		kind:     BackendRootTProxy,
		startErr: resetReportTestError{err: errors.New("readiness failed"), report: report},
	}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	if _, err := o.Start(); err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	status := waitForOperationDone(t, o, OperationStart)
	if status.LastOperation == nil || status.LastOperation.Succeeded {
		t.Fatalf("expected failed start result, got %#v", status.LastOperation)
	}
	if status.LastOperation.ResetReport == nil {
		t.Fatalf("expected recovery reset report in last operation")
	}
	if status.LastOperation.ResetReport.Status != "clean_with_warnings" {
		t.Fatalf("unexpected reset report: %#v", status.LastOperation.ResetReport)
	}
}

func TestSuccessfulStartCanAttachRecoveryResetReport(t *testing.T) {
	report := ResetReport{
		BackendKind: BackendRootTProxy,
		Status:      "ok",
		Steps:       []ResetStep{{Name: "stale-lock-recovery", Status: "ok"}},
	}
	backend := &fakeBackend{kind: BackendRootTProxy, startReport: &report}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	if _, err := o.Start(); err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	status := waitForOperationDone(t, o, OperationStart)
	if status.LastOperation == nil || !status.LastOperation.Succeeded {
		t.Fatalf("expected successful start result, got %#v", status.LastOperation)
	}
	if status.LastOperation.ResetReport == nil || status.LastOperation.ResetReport.Status != "ok" {
		t.Fatalf("expected successful recovery reset report, got %#v", status.LastOperation)
	}
}

func TestPanicOperationClearsActiveAndRecordsFailure(t *testing.T) {
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, &fakeBackend{kind: BackendRootTProxy})

	status, err := o.RunOperation(OperationReload, PhaseStarting, func(int64) error {
		panic("boom")
	})
	if err != nil {
		t.Fatalf("reload acceptance failed: %v", err)
	}
	if status.ActiveOperation == nil {
		t.Fatalf("expected active operation after acceptance: %#v", status)
	}

	status = waitForOperationDone(t, o, OperationReload)
	if status.LastOperation == nil || status.LastOperation.Succeeded {
		t.Fatalf("expected failed last operation, got %#v", status.LastOperation)
	}
	if status.LastOperation.ErrorCode != "PANIC" || status.LastOperation.ErrorMessage != "boom" {
		t.Fatalf("expected panic details in last operation, got %#v", status.LastOperation)
	}
	if status.AppliedState.Phase != PhaseFailed {
		t.Fatalf("expected failed phase after panic, got %#v", status.AppliedState)
	}
}

func TestResetReportStoredInLastOperation(t *testing.T) {
	report := ResetReport{
		BackendKind: BackendRootTProxy,
		Status:      "clean_with_warnings",
		Steps:       []ResetStep{{Name: "verify-cleanup", Status: "warning", Detail: "leftover"}},
		Warnings:    []string{"leftover"},
	}
	backend := &fakeBackend{kind: BackendRootTProxy, resetReport: report}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	if _, err := o.Reset(); err != nil {
		t.Fatalf("reset acceptance failed: %v", err)
	}
	status := waitForOperationDone(t, o, OperationReset)
	if status.LastOperation == nil || !status.LastOperation.Succeeded {
		t.Fatalf("expected successful reset result, got %#v", status.LastOperation)
	}
	if status.LastOperation.ResetReport == nil {
		t.Fatalf("expected reset report in last operation")
	}
	if status.LastOperation.ResetReport.Status != "clean_with_warnings" {
		t.Fatalf("unexpected reset report: %#v", status.LastOperation.ResetReport)
	}
	if status.LastOperation.ResetReport.Generation != status.LastOperation.Generation {
		t.Fatalf("expected reset report generation to be filled, got %#v", status.LastOperation.ResetReport)
	}
}

func TestActorRejectsResetWhileStartIsActive(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBackend{kind: BackendRootTProxy, startStarted: started, startBlock: release}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	status, err := o.Start()
	if err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	if status.ActiveOperation == nil || status.ActiveOperation.Kind != OperationStart {
		t.Fatalf("expected accepted active start: %#v", status)
	}
	done := operationDoneChan(o, OperationStart)
	waitForSignal(t, started)

	_, err = o.Reset()
	if !isBusyCode(err, BusyCodeRuntimeBusy) {
		t.Fatalf("expected runtime busy while start is active, got %T %v", err, err)
	}
	if status := o.Status(); status.AppliedState.Generation != 1 {
		t.Fatalf("blocked reset must not advance generation: %#v", status)
	}

	close(release)
	waitForSignal(t, done)
}

func TestActorRejectsEveryMutatingOperationWhileAnotherOperationIsActive(t *testing.T) {
	activeOperations := []struct {
		name      string
		kind      OperationKind
		start     func(t *testing.T, o *Orchestrator, backend *fakeBackend) (release func(), done <-chan struct{})
		busyCode  string
		wantPhase Phase
	}{
		{
			name:      "start",
			kind:      OperationStart,
			busyCode:  BusyCodeRuntimeBusy,
			wantPhase: PhaseStarting,
			start: func(t *testing.T, o *Orchestrator, backend *fakeBackend) (func(), <-chan struct{}) {
				started := make(chan struct{})
				release := make(chan struct{})
				backend.startStarted = started
				backend.startBlock = release
				if _, err := o.Start(); err != nil {
					t.Fatalf("start acceptance failed: %v", err)
				}
				done := operationDoneChan(o, OperationStart)
				waitForSignal(t, started)
				return func() { close(release) }, done
			},
		},
		{
			name:      "stop",
			kind:      OperationStop,
			busyCode:  BusyCodeRuntimeBusy,
			wantPhase: PhaseStopping,
			start: func(t *testing.T, o *Orchestrator, backend *fakeBackend) (func(), <-chan struct{}) {
				startAndWait(t, o)
				started := make(chan struct{})
				release := make(chan struct{})
				backend.stopStarted = started
				backend.stopBlock = release
				if _, err := o.Stop(); err != nil {
					t.Fatalf("stop acceptance failed: %v", err)
				}
				done := operationDoneChan(o, OperationStop)
				waitForSignal(t, started)
				return func() { close(release) }, done
			},
		},
		{
			name:      "restart",
			kind:      OperationRestart,
			busyCode:  BusyCodeRuntimeBusy,
			wantPhase: PhaseStarting,
			start: func(t *testing.T, o *Orchestrator, backend *fakeBackend) (func(), <-chan struct{}) {
				started := make(chan struct{})
				release := make(chan struct{})
				backend.restartStarted = started
				backend.restartBlock = release
				if _, err := o.Restart(); err != nil {
					t.Fatalf("restart acceptance failed: %v", err)
				}
				done := operationDoneChan(o, OperationRestart)
				waitForSignal(t, started)
				return func() { close(release) }, done
			},
		},
		{
			name:      "reset",
			kind:      OperationReset,
			busyCode:  BusyCodeResetInProgress,
			wantPhase: PhaseResetting,
			start: func(t *testing.T, o *Orchestrator, backend *fakeBackend) (func(), <-chan struct{}) {
				started := make(chan struct{})
				release := make(chan struct{})
				backend.resetStarted = started
				backend.resetBlock = release
				if _, err := o.Reset(); err != nil {
					t.Fatalf("reset acceptance failed: %v", err)
				}
				done := operationDoneChan(o, OperationReset)
				waitForSignal(t, started)
				return func() { close(release) }, done
			},
		},
		{
			name:      "reload",
			kind:      OperationReload,
			busyCode:  BusyCodeRuntimeBusy,
			wantPhase: PhaseStarting,
			start: func(t *testing.T, o *Orchestrator, backend *fakeBackend) (func(), <-chan struct{}) {
				started := make(chan struct{})
				release := make(chan struct{})
				if _, err := o.RunOperation(OperationReload, PhaseStarting, func(int64) error {
					signalAndWait(started, release)
					return nil
				}); err != nil {
					t.Fatalf("reload acceptance failed: %v", err)
				}
				done := operationDoneChan(o, OperationReload)
				waitForSignal(t, started)
				return func() { close(release) }, done
			},
		},
		{
			name:      "network-change",
			kind:      OperationNetworkChange,
			busyCode:  BusyCodeRuntimeBusy,
			wantPhase: PhaseStarting,
			start: func(t *testing.T, o *Orchestrator, backend *fakeBackend) (func(), <-chan struct{}) {
				startAndWait(t, o)
				started := make(chan struct{})
				release := make(chan struct{})
				backend.networkStarted = started
				backend.networkBlock = release
				if _, err := o.HandleNetworkChange(); err != nil {
					t.Fatalf("network-change acceptance failed: %v", err)
				}
				done := operationDoneChan(o, OperationNetworkChange)
				waitForSignal(t, started)
				return func() { close(release) }, done
			},
		},
		{
			name:      "rescue",
			kind:      OperationRescue,
			busyCode:  BusyCodeRuntimeBusy,
			wantPhase: PhaseStarting,
			start: func(t *testing.T, o *Orchestrator, backend *fakeBackend) (func(), <-chan struct{}) {
				started := make(chan struct{})
				release := make(chan struct{})
				if _, err := o.RunOperation(OperationRescue, PhaseStarting, func(int64) error {
					signalAndWait(started, release)
					return nil
				}); err != nil {
					t.Fatalf("rescue acceptance failed: %v", err)
				}
				done := operationDoneChan(o, OperationRescue)
				waitForSignal(t, started)
				return func() { close(release) }, done
			},
		},
		{
			name:      "update-install",
			kind:      OperationUpdateInstall,
			busyCode:  BusyCodeRuntimeBusy,
			wantPhase: PhaseStopping,
			start: func(t *testing.T, o *Orchestrator, backend *fakeBackend) (func(), <-chan struct{}) {
				started := make(chan struct{})
				release := make(chan struct{})
				if _, err := o.RunOperation(OperationUpdateInstall, PhaseStopping, func(int64) error {
					signalAndWait(started, release)
					return nil
				}); err != nil {
					t.Fatalf("update-install acceptance failed: %v", err)
				}
				done := operationDoneChan(o, OperationUpdateInstall)
				waitForSignal(t, started)
				return func() { close(release) }, done
			},
		},
	}
	incomingOperations := []struct {
		name string
		run  func(o *Orchestrator) error
	}{
		{name: "start", run: func(o *Orchestrator) error { _, err := o.Start(); return err }},
		{name: "stop", run: func(o *Orchestrator) error { _, err := o.Stop(); return err }},
		{name: "restart", run: func(o *Orchestrator) error { _, err := o.Restart(); return err }},
		{name: "reset", run: func(o *Orchestrator) error { _, err := o.Reset(); return err }},
		{name: "reload", run: func(o *Orchestrator) error {
			_, err := o.RunOperation(OperationReload, PhaseStarting, func(int64) error { return nil })
			return err
		}},
		{name: "network-change", run: func(o *Orchestrator) error { _, err := o.HandleNetworkChange(); return err }},
		{name: "rescue", run: func(o *Orchestrator) error {
			_, err := o.RunOperation(OperationRescue, PhaseStarting, func(int64) error { return nil })
			return err
		}},
		{name: "update-install", run: func(o *Orchestrator) error {
			_, err := o.RunOperation(OperationUpdateInstall, PhaseStopping, func(int64) error { return nil })
			return err
		}},
	}

	for _, active := range activeOperations {
		for _, incoming := range incomingOperations {
			t.Run(string(active.kind)+" blocks "+incoming.name, func(t *testing.T) {
				backend := &fakeBackend{kind: BackendRootTProxy}
				o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

				release, done := active.start(t, o, backend)
				status := o.Status()
				if status.ActiveOperation == nil {
					t.Fatalf("expected active %s operation: %#v", active.kind, status)
				}
				activeGeneration := status.ActiveOperation.Generation
				if status.ActiveOperation.Kind != active.kind {
					t.Fatalf("expected active kind %s, got %#v", active.kind, status.ActiveOperation)
				}
				if status.ActiveOperation.Phase != active.wantPhase {
					t.Fatalf("expected active phase %s, got %#v", active.wantPhase, status.ActiveOperation)
				}

				if err := incoming.run(o); !isBusyCode(err, active.busyCode) {
					t.Fatalf("expected %s while %s active, got %T %v", active.busyCode, active.kind, err, err)
				}
				status = o.Status()
				if status.ActiveOperation == nil || status.ActiveOperation.Generation != activeGeneration {
					t.Fatalf("blocked %s must not replace active operation: %#v", incoming.name, status)
				}
				if status.AppliedState.Generation != activeGeneration {
					t.Fatalf("blocked %s must not advance generation from %d: %#v", incoming.name, activeGeneration, status.AppliedState)
				}

				release()
				waitForSignal(t, done)
			})
		}
	}
}

func TestActorRejectsStartWhileResetIsActive(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBackend{kind: BackendRootTProxy, resetStarted: started, resetBlock: release}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	if _, err := o.Reset(); err != nil {
		t.Fatalf("reset acceptance failed: %v", err)
	}
	done := operationDoneChan(o, OperationReset)
	waitForSignal(t, started)

	_, err := o.Start()
	if !isBusyCode(err, BusyCodeResetInProgress) {
		t.Fatalf("expected reset-in-progress while reset is active, got %T %v", err, err)
	}

	close(release)
	waitForSignal(t, done)
}

func TestActorRejectsReloadNetworkRescueAndUpdateDuringReset(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBackend{kind: BackendRootTProxy, resetStarted: started, resetBlock: release}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	if _, err := o.Reset(); err != nil {
		t.Fatalf("reset acceptance failed: %v", err)
	}
	done := operationDoneChan(o, OperationReset)
	waitForSignal(t, started)

	if _, err := o.RunOperation(OperationReload, PhaseStarting, func(int64) error { return nil }); !isBusyCode(err, BusyCodeResetInProgress) {
		t.Fatalf("expected reload to fail fast during reset, got %T %v", err, err)
	}
	if _, err := o.HandleNetworkChange(); !isBusyCode(err, BusyCodeResetInProgress) {
		t.Fatalf("expected network-change to fail fast during reset, got %T %v", err, err)
	}
	if _, err := o.RunOperation(OperationRescue, PhaseStarting, func(int64) error { return nil }); !isBusyCode(err, BusyCodeResetInProgress) {
		t.Fatalf("expected rescue to fail fast during reset, got %T %v", err, err)
	}
	if _, err := o.RunOperation(OperationUpdateInstall, PhaseStopping, func(int64) error { return nil }); !isBusyCode(err, BusyCodeResetInProgress) {
		t.Fatalf("expected update install to fail fast during reset, got %T %v", err, err)
	}

	close(release)
	waitForSignal(t, done)
}

func TestActorRejectsRescueDuringStart(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBackend{kind: BackendRootTProxy, startStarted: started, startBlock: release}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	if _, err := o.Start(); err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	done := operationDoneChan(o, OperationStart)
	waitForSignal(t, started)

	if _, err := o.RunOperation(OperationRescue, PhaseStarting, func(int64) error { return nil }); !isBusyCode(err, BusyCodeRuntimeBusy) {
		t.Fatalf("expected rescue to fail fast during start, got %T %v", err, err)
	}

	close(release)
	waitForSignal(t, done)
}

func TestActorStatusAndHealthRemainReadableDuringOperation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeBackend{kind: BackendRootTProxy, startStarted: started, startBlock: release}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	if _, err := o.Start(); err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	done := operationDoneChan(o, OperationStart)
	waitForSignal(t, started)

	status := o.Status()
	if status.ActiveOperation == nil || status.ActiveOperation.Kind != OperationStart {
		t.Fatalf("expected active start operation in status: %#v", status)
	}
	if status.AppliedState.Phase != PhaseStarting || status.AppliedState.Generation != 1 {
		t.Fatalf("expected starting generation 1, got %#v", status.AppliedState)
	}
	health := o.CurrentHealth()
	if !health.CheckedAt.IsZero() {
		t.Fatalf("current health should be readable without forcing probes: %#v", health)
	}

	close(release)
	waitForSignal(t, done)
}

func TestActorFailedAndBlockedOperationsDoNotRollBackGeneration(t *testing.T) {
	backend := &fakeBackend{kind: BackendRootTProxy, startErr: errors.New("boom")}
	o := NewOrchestrator(DesiredState{BackendKind: BackendRootTProxy}, backend)

	status, err := o.Start()
	if err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	status = waitForOperationDone(t, o, OperationStart)
	if status.LastOperation == nil || status.LastOperation.Succeeded {
		t.Fatalf("expected failed start last operation, got %#v", status.LastOperation)
	}
	if status.AppliedState.Generation != 1 {
		t.Fatalf("failed start should leave generation 1, got %#v", status.AppliedState)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	backend.startErr = nil
	backend.startStarted = started
	backend.startBlock = release
	if _, err := o.Start(); err != nil {
		t.Fatalf("start acceptance failed: %v", err)
	}
	done := operationDoneChan(o, OperationStart)
	waitForSignal(t, started)
	if _, err := o.Reset(); !isBusyCode(err, BusyCodeRuntimeBusy) {
		t.Fatalf("expected blocked reset, got %T %v", err, err)
	}
	if status := o.Status(); status.AppliedState.Generation != 2 {
		t.Fatalf("blocked reset must not advance active generation: %#v", status.AppliedState)
	}
	close(release)
	waitForSignal(t, done)
}

type fakeBackend struct {
	kind           BackendKind
	stopCalls      int
	startErr       error
	startStarted   chan struct{}
	startBlock     chan struct{}
	startReport    *ResetReport
	stopStarted    chan struct{}
	stopBlock      chan struct{}
	restartStarted chan struct{}
	restartBlock   chan struct{}
	resetStarted   chan struct{}
	resetBlock     chan struct{}
	resetReport    ResetReport
	networkStarted chan struct{}
	networkBlock   chan struct{}
	currentHealth  HealthSnapshot
}

func (f *fakeBackend) Kind() BackendKind         { return f.kind }
func (f *fakeBackend) Supported() (bool, string) { return true, "" }
func (f *fakeBackend) Start(DesiredState, int64) (*ResetReport, error) {
	signalAndWait(f.startStarted, f.startBlock)
	return f.startReport, f.startErr
}
func (f *fakeBackend) Stop() error {
	signalAndWait(f.stopStarted, f.stopBlock)
	f.stopCalls++
	return nil
}
func (f *fakeBackend) Reset(generation int64) ResetReport {
	signalAndWait(f.resetStarted, f.resetBlock)
	if f.resetReport.Status != "" {
		report := f.resetReport
		report.Generation = generation
		return report
	}
	return ResetReport{BackendKind: f.kind, Generation: generation, Status: "ok"}
}
func (f *fakeBackend) Restart(DesiredState, int64) (*ResetReport, error) {
	signalAndWait(f.restartStarted, f.restartBlock)
	return nil, nil
}
func (f *fakeBackend) HandleNetworkChange(int64) (*ResetReport, error) {
	signalAndWait(f.networkStarted, f.networkBlock)
	return nil, nil
}
func (f *fakeBackend) CurrentHealth() HealthSnapshot { return f.currentHealth }
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

type resetReportTestError struct {
	err    error
	report ResetReport
}

func (e resetReportTestError) Error() string {
	return e.err.Error()
}

func (e resetReportTestError) Unwrap() error {
	return e.err
}

func (e resetReportTestError) RuntimeResetReport() ResetReport {
	return e.report
}

func countOperationLogs(events []OperationLogEvent, result string) int {
	count := 0
	for _, event := range events {
		if event.Result == result {
			count++
		}
	}
	return count
}

func operationLogByResult(events []OperationLogEvent, result string) OperationLogEvent {
	for _, event := range events {
		if event.Result == result {
			return event
		}
	}
	return OperationLogEvent{}
}

func signalAndWait(started chan struct{}, block chan struct{}) {
	if started != nil {
		close(started)
	}
	if block != nil {
		<-block
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for operation signal")
	}
}

func startAndWait(t *testing.T, o *Orchestrator) Status {
	t.Helper()
	if _, err := o.Start(); err != nil {
		t.Fatalf("prime start acceptance failed: %v", err)
	}
	return waitForOperationDone(t, o, OperationStart)
}

func operationDoneChan(o *Orchestrator, kind OperationKind) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for {
			status := o.Status()
			if status.ActiveOperation == nil && status.LastOperation != nil && status.LastOperation.Kind == kind {
				close(done)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return done
}

func waitForOperationDone(t *testing.T, o *Orchestrator, kind OperationKind) Status {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		status := o.Status()
		if status.ActiveOperation == nil && status.LastOperation != nil && status.LastOperation.Kind == kind {
			return status
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s operation to finish: %#v", kind, status)
		case <-time.After(time.Millisecond):
		}
	}
}

func isBusyCode(err error, code string) bool {
	var busy *OperationBusyError
	return errors.As(err, &busy) && busy.Code == code
}
