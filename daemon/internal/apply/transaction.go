package applytx

import (
	"fmt"
	"reflect"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type ConfigTransactionResult struct {
	Action            string
	ConfigSaved       bool
	RuntimeWasRunning bool
	RuntimeOperation  runtimev2.OperationKind
}

type ConfigTransaction struct {
	Action         string
	EnsureIdle     func() error
	SaveProfile    func(*config.Config) error
	ApplyConfig    func(*config.Config, bool, runtimev2.OperationKind) error
	RuntimeRunning func() bool
}

func (tx ConfigTransaction) Run(nextCfg *config.Config, reload bool) (ConfigTransactionResult, error) {
	var result ConfigTransactionResult
	if nextCfg == nil {
		return result, fmt.Errorf("config is nil")
	}
	if tx.Action == "" {
		return result, fmt.Errorf("mutation action is not configured")
	}
	result.Action = tx.Action
	result.RuntimeOperation = RuntimeOperationForAction(tx.Action)
	if tx.EnsureIdle != nil {
		if err := tx.EnsureIdle(); err != nil {
			return result, err
		}
	}
	if tx.SaveProfile == nil {
		return result, fmt.Errorf("profile persistence is not configured")
	}
	if err := tx.SaveProfile(nextCfg); err != nil {
		return result, fmt.Errorf("persist profile: %w", err)
	}
	result.ConfigSaved = true
	if tx.RuntimeRunning != nil {
		result.RuntimeWasRunning = tx.RuntimeRunning()
	}
	if tx.ApplyConfig == nil {
		return result, fmt.Errorf("config apply is not configured")
	}
	if err := tx.ApplyConfig(nextCfg, reload, result.RuntimeOperation); err != nil {
		return result, err
	}
	return result, nil
}

func RuntimeOperationForAction(action string) runtimev2.OperationKind {
	switch action {
	case "config-import":
		return runtimev2.OperationConfigMutation
	case "profile.apply", "profile.importNodes", "profile.setActiveNode", "subscription.refresh", "backend.applyDesiredState":
		return runtimev2.OperationProfileApply
	default:
		return runtimev2.OperationProfileApply
	}
}

type Warning struct {
	Code    string
	Message string
}

func RuntimeApplyStatus(reload bool, runtimeWasRunning bool) string {
	switch {
	case reload && runtimeWasRunning:
		return "accepted"
	case reload:
		return "skipped_runtime_stopped"
	default:
		return "not_requested"
	}
}

func MutationSuccess(action string, status string, reload bool, runtimeWasRunning bool, updated int) map[string]interface{} {
	runtimeApply := RuntimeApplyStatus(reload, runtimeWasRunning)
	runtimeApplied := runtimeApply == "applied"
	accepted := runtimeApply == "accepted"
	if accepted {
		status = "accepted"
	}
	operation := ConfigMutationOperation(action, status, true, reload, runtimeApplied, runtimeApply, updated, "", "", nil)
	result := map[string]interface{}{
		"ok":               true,
		"status":           status,
		"reload":           reload,
		"config_saved":     true,
		"runtime_applied":  runtimeApplied,
		"runtime_apply":    runtimeApply,
		"accepted":         accepted,
		"operation_active": accepted,
		"operation":        operation,
	}
	if updated >= 0 {
		result["updated"] = updated
	}
	return result
}

func MutationErrorData(action string, saved bool, code string, message string, resetReport interface{}) map[string]interface{} {
	runtimeApply := "not_started"
	status := "failed"
	if saved {
		runtimeApply = "failed"
		status = "saved_not_applied"
	}
	operation := ConfigMutationOperation(action, status, saved, saved, false, runtimeApply, -1, code, message, resetReport)
	data := map[string]interface{}{
		"ok":              false,
		"status":          status,
		"config_saved":    saved,
		"runtime_applied": false,
		"message":         message,
		"code":            code,
		"runtime_apply":   runtimeApply,
		"operation":       operation,
	}
	if resetReport != nil {
		data["resetReport"] = resetReport
	}
	return data
}

func ConfigMutationOperation(action string, status string, saved bool, reload bool, runtimeApplied bool, runtimeApply string, updated int, code string, message string, resetReport interface{}) map[string]interface{} {
	rollback := rollbackStatus(saved, status, resetReport)
	stages := []map[string]interface{}{
		{"name": "validate", "status": "ok"},
		{"name": "render", "status": "ok"},
	}
	if status == "failed" && !saved && (code == "RUNTIME_BUSY" || code == "RESET_IN_PROGRESS") {
		stages = append(stages,
			map[string]interface{}{"name": "runtime-idle", "status": "failed"},
			map[string]interface{}{"name": "persist-draft", "status": "not_started"},
		)
	} else {
		stages = append(stages, map[string]interface{}{"name": "persist-draft", "status": stageStatus(saved, status == "failed")})
	}
	stages = append(stages, map[string]interface{}{"name": "runtime-apply", "status": runtimeApplyStage(reload, runtimeApply)})
	stages = append(stages, map[string]interface{}{"name": "verify", "status": verifyStage(status, runtimeApply, resetReport)})
	stages = append(stages, map[string]interface{}{"name": "commit-generation", "status": commitStage(status, runtimeApply, saved)})
	if resetReport != nil {
		stages = append(stages, map[string]interface{}{"name": "cleanup", "status": resetReportStatus(resetReport)})
	}
	operation := map[string]interface{}{
		"type":            "config-mutation",
		"action":          action,
		"status":          status,
		"configSaved":     saved,
		"runtimeApplied":  runtimeApplied,
		"runtimeApply":    runtimeApply,
		"accepted":        runtimeApply == "accepted",
		"operationActive": runtimeApply == "accepted",
		"rollback":        rollback,
		"stages":          stages,
	}
	if updated >= 0 {
		operation["updated"] = updated
	}
	if code != "" {
		operation["code"] = code
	}
	if message != "" {
		operation["message"] = message
	}
	return operation
}

func ProfileOperation(
	action string,
	status string,
	configSaved bool,
	runtimeApplied bool,
	runtimeApply string,
	desiredGeneration int64,
	appliedGeneration int64,
	code string,
	message string,
	resetReport interface{},
	warnings []Warning,
	updated int,
) map[string]interface{} {
	rollback := rollbackStatus(configSaved, status, resetReport)
	stages := []map[string]interface{}{
		{"name": "validate", "status": "ok"},
		{"name": "render", "status": "ok"},
		{"name": "persist-draft", "status": stageStatus(configSaved, status == "failed")},
		{"name": "runtime-apply", "status": runtimeApply},
		{"name": "verify", "status": verifyStage(status, runtimeApply, resetReport)},
		{"name": "commit-generation", "status": commitStage(status, runtimeApply, configSaved)},
	}
	if resetReport != nil {
		stages = append(stages, map[string]interface{}{"name": "cleanup", "status": resetReportStatus(resetReport)})
	}
	warningItems := make([]map[string]string, 0, len(warnings))
	for _, warning := range warnings {
		warningItems = append(warningItems, map[string]string{
			"code":    warning.Code,
			"message": warning.Message,
		})
	}
	result := map[string]interface{}{
		"status":            status,
		"configSaved":       configSaved,
		"config_saved":      configSaved,
		"runtimeApplied":    runtimeApplied,
		"runtime_applied":   runtimeApplied,
		"runtimeApply":      runtimeApply,
		"runtime_apply":     runtimeApply,
		"desiredGeneration": desiredGeneration,
		"appliedGeneration": appliedGeneration,
		"rollback":          rollback,
		"stages":            stages,
		"warnings":          warningItems,
		"operation": map[string]interface{}{
			"type":              "profile-apply",
			"action":            action,
			"status":            status,
			"configSaved":       configSaved,
			"runtimeApplied":    runtimeApplied,
			"runtimeApply":      runtimeApply,
			"desiredGeneration": desiredGeneration,
			"appliedGeneration": appliedGeneration,
			"rollback":          rollback,
			"stages":            stages,
			"warnings":          warningItems,
		},
	}
	if updated >= 0 {
		result["updated"] = updated
		result["operation"].(map[string]interface{})["updated"] = updated
	}
	if code != "" {
		result["code"] = code
		result["operation"].(map[string]interface{})["code"] = code
	}
	if message != "" {
		result["message"] = message
		result["operation"].(map[string]interface{})["message"] = message
	}
	if resetReport != nil {
		result["resetReport"] = resetReport
	}
	return result
}

func runtimeApplyStage(reload bool, runtimeApply string) string {
	if reload {
		return runtimeApply
	}
	return "not_requested"
}

func verifyStage(status string, runtimeApply string, resetReport interface{}) string {
	if resetReport != nil {
		return "failed"
	}
	if status == "failed" || status == "saved_not_applied" {
		return "not_started"
	}
	if runtimeApply == "accepted" {
		return "accepted"
	}
	if runtimeApply == "skipped_runtime_stopped" || runtimeApply == "not_requested" {
		return "skipped"
	}
	return "ok"
}

func commitStage(status string, runtimeApply string, saved bool) string {
	if !saved || status == "failed" || status == "saved_not_applied" {
		return "not_started"
	}
	if runtimeApply == "accepted" {
		return "accepted"
	}
	return "ok"
}

func rollbackStatus(saved bool, status string, resetReport interface{}) string {
	if resetReport != nil {
		if resetReportStatus(resetReport) == "ok" {
			return "cleanup_succeeded"
		}
		return "cleanup_incomplete"
	}
	if saved && status == "failed" {
		return "unknown"
	}
	return "not_needed"
}

func resetReportStatus(resetReport interface{}) string {
	if m, ok := resetReport.(interface{ ResetStatus() string }); ok {
		return m.ResetStatus()
	}
	if m, ok := resetReport.(map[string]interface{}); ok {
		if status, ok := m["status"].(string); ok && status != "" {
			return status
		}
	}
	value := reflect.ValueOf(resetReport)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return ""
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.Struct {
		field := value.FieldByName("Status")
		if field.IsValid() && field.Kind() == reflect.String {
			return field.String()
		}
	}
	return ""
}

func stageStatus(ok bool, failed bool) string {
	if ok {
		return "ok"
	}
	if failed {
		return "failed"
	}
	return "not_started"
}
