package runtimev2

func CanonicalStatusFromStatus(status Status) CanonicalStatus {
	phase := status.AppliedState.Phase
	generation := status.AppliedState.Generation
	lastCode := status.Health.LastCode
	lastError := status.Health.LastError

	if status.ActiveOperation != nil {
		phase = status.ActiveOperation.Phase
		if status.ActiveOperation.Generation > 0 {
			generation = status.ActiveOperation.Generation
		}
		if status.ActiveOperation.StepCode != "" {
			lastCode = status.ActiveOperation.StepCode
		}
		if status.ActiveOperation.StepDetail != "" {
			lastError = status.ActiveOperation.StepDetail
		}
	} else if status.LastOperation != nil && !status.LastOperation.Succeeded {
		if lastCode == "" {
			lastCode = status.LastOperation.ErrorCode
		}
		if lastError == "" {
			lastError = status.LastOperation.ErrorMessage
		}
	}

	return CanonicalStatus{
		BackendKind:     status.AppliedState.BackendKind,
		Phase:           phase,
		ActiveProfileID: firstNonEmpty(status.AppliedState.ActiveProfileID, status.DesiredState.ActiveProfileID),
		Generation:      generation,
		Readiness: ReadinessStatus{
			Ready:              status.Health.Healthy(),
			OperationalHealthy: status.Health.OperationalHealthy(),
			CoreReady:          status.Health.CoreReady,
			RoutingReady:       status.Health.RoutingReady,
			DNSReady:           status.Health.DNSReady,
			EgressReady:        status.Health.EgressReady,
		},
		LastCode:        lastCode,
		LastError:       lastError,
		LastUserMessage: status.Health.LastUserMessage,
		LastDebug:       status.Health.LastDebug,
		RollbackApplied: status.Health.RollbackApplied,
		CheckedAt:       status.Health.CheckedAt,
	}
}
