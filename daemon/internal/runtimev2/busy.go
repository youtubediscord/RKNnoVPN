package runtimev2

const (
	BusyCodeRuntimeBusy     = "RUNTIME_BUSY"
	BusyCodeResetInProgress = "RESET_IN_PROGRESS"
)

type OperationBusyError struct {
	Code   string
	Detail string
	Active *OperationStatus
}

func NewRuntimeBusyError(active OperationStatus) *OperationBusyError {
	code := BusyCodeRuntimeBusy
	detail := "runtime operation already in progress"
	if active.Kind == OperationReset {
		code = BusyCodeResetInProgress
		detail = "reset is in progress"
	}
	return &OperationBusyError{
		Code:   code,
		Detail: detail,
		Active: cloneOperation(active),
	}
}

func NewResetInProgressError(detail string) *OperationBusyError {
	if detail == "" {
		detail = "reset is in progress"
	}
	return &OperationBusyError{
		Code:   BusyCodeResetInProgress,
		Detail: detail,
	}
}

func (e *OperationBusyError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return e.Detail
	}
	return "runtime operation already in progress"
}

func (e *OperationBusyError) Data() map[string]interface{} {
	if e == nil {
		return nil
	}
	data := map[string]interface{}{
		"code": e.Code,
	}
	if e.Detail != "" {
		data["detail"] = e.Detail
	}
	if e.Active != nil {
		data["activeOperation"] = e.Active
		data["phase"] = e.Active.Phase
		data["generation"] = e.Active.Generation
		data["startedAt"] = e.Active.StartedAt
	}
	return data
}
