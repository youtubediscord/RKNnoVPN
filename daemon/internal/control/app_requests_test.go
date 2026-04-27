package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeResolveUIDParams(t *testing.T) {
	raw := json.RawMessage(`{"uid":10123}`)

	request, err := DecodeResolveUIDParams(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if request.UID != 10123 {
		t.Fatalf("unexpected request: %#v", request)
	}
}

func TestDecodeResolveUIDParamsRejectsMissingAndInvalidUID(t *testing.T) {
	if _, err := DecodeResolveUIDParams(nil); err == nil || !strings.Contains(err.Error(), "params required") {
		t.Fatalf("expected missing params error, got %v", err)
	}

	raw := json.RawMessage(`{"uid":0}`)
	if _, err := DecodeResolveUIDParams(&raw); err == nil || !strings.Contains(err.Error(), "uid must be > 0") {
		t.Fatalf("expected invalid uid error, got %v", err)
	}
}
