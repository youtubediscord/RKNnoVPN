package ipc

import (
	"reflect"
	"testing"
)

func TestNewResponseWrapsResultEnvelope(t *testing.T) {
	activeOperation := map[string]interface{}{"kind": "start"}
	resp := NewResponse(7, map[string]interface{}{
		"state":           "running",
		"activeOperation": activeOperation,
	})

	envelope, ok := resp.Result.(Envelope)
	if !ok {
		t.Fatalf("expected response result envelope, got %#v", resp.Result)
	}
	if !envelope.OK {
		t.Fatalf("success envelope must set ok=true: %#v", envelope)
	}
	if envelope.Result == nil {
		t.Fatalf("success envelope must carry result: %#v", envelope)
	}
	if !reflect.DeepEqual(envelope.Operation, activeOperation) {
		t.Fatalf("success envelope must expose active operation: %#v", envelope)
	}
	if envelope.Warnings == nil || len(envelope.Warnings) != 0 {
		t.Fatalf("success envelope must carry an empty warnings list: %#v", envelope)
	}
}

func TestNewResponsePrefersExplicitOperationEnvelope(t *testing.T) {
	activeOperation := map[string]interface{}{"kind": "start"}
	configOperation := map[string]interface{}{"type": "profile-apply", "action": "profile.apply"}
	resp := NewResponse(8, map[string]interface{}{
		"activeOperation": activeOperation,
		"operation":       configOperation,
	})

	envelope, ok := resp.Result.(Envelope)
	if !ok {
		t.Fatalf("expected response result envelope, got %#v", resp.Result)
	}
	if !reflect.DeepEqual(envelope.Operation, configOperation) {
		t.Fatalf("success envelope must prefer explicit operation: %#v", envelope)
	}
}

func TestNewErrorResponseWrapsErrorEnvelope(t *testing.T) {
	details := map[string]interface{}{
		"code":         "CONFIG_APPLY_FAILED",
		"config_saved": true,
	}
	resp := NewErrorResponse(9, CodeInternalError, "apply failed", details)

	envelope, ok := resp.Error.Data.(Envelope)
	if !ok {
		t.Fatalf("expected response error envelope, got %#v", resp.Error.Data)
	}
	if envelope.OK {
		t.Fatalf("error envelope must set ok=false: %#v", envelope)
	}
	errPayload, ok := envelope.Error.(EnvelopeError)
	if !ok {
		t.Fatalf("expected typed envelope error, got %#v", envelope.Error)
	}
	if errPayload.Code != "CONFIG_APPLY_FAILED" {
		t.Fatalf("expected stable error code from details, got %#v", errPayload)
	}
	if errPayload.RPCCode != CodeInternalError {
		t.Fatalf("expected JSON-RPC code in envelope error, got %#v", errPayload)
	}
	if errPayload.Details == nil {
		t.Fatalf("error envelope must preserve details: %#v", errPayload)
	}
	if envelope.Warnings == nil || len(envelope.Warnings) != 0 {
		t.Fatalf("error envelope must carry an empty warnings list: %#v", envelope)
	}
}
