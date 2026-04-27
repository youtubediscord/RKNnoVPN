package control

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
)

func TestDecodeProfileApplyParamsAcceptsDirectDocument(t *testing.T) {
	raw := mustJSON(t, profiledoc.Document{
		SchemaVersion: profiledoc.CurrentSchemaVersion,
		ID:            "default",
		Name:          "Default",
	})

	request, err := DecodeProfileApplyParams(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if request.Profile.ID != "default" || !request.Reload {
		t.Fatalf("unexpected direct profile request: %#v", request)
	}
}

func TestDecodeProfileApplyParamsAcceptsWrappedDocumentAndReload(t *testing.T) {
	raw := json.RawMessage(`{"profile":{"profileSchemaVersion":2,"id":"default","name":"Default"},"reload":false}`)

	request, err := DecodeProfileApplyParams(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if request.Profile.ID != "default" || request.Reload {
		t.Fatalf("unexpected wrapped profile request: %#v", request)
	}
}

func TestDecodeProfileApplyParamsRejectsUnknownWrappedFields(t *testing.T) {
	raw := json.RawMessage(`{"profile":{"profileSchemaVersion":2,"id":"default","name":"Default"},"reload":true,"legacy":true}`)
	if _, err := DecodeProfileApplyParams(&raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestDecodeImportNodesParamsNormalizesManualNodes(t *testing.T) {
	raw := json.RawMessage(`{"nodes":[{"id":"node-1","name":"Local","protocol":"SOCKS","server":"127.0.0.1","port":10808,"stale":true,"source":{"type":"SUBSCRIPTION"}}],"reload":false}`)
	now := time.UnixMilli(1234567890)

	request, err := DecodeImportNodesParams(&raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if request.Reload || len(request.Nodes) != 1 {
		t.Fatalf("unexpected import request: %#v", request)
	}
	node := request.Nodes[0]
	if node.Stale || node.Source.Type != "MANUAL" || node.CreatedAt != now.UnixMilli() {
		t.Fatalf("import node was not normalized as manual/live: %#v", node)
	}
}

func TestDecodeImportNodesParamsRejectsEmptyNodes(t *testing.T) {
	raw := json.RawMessage(`{"nodes":[]}`)
	if _, err := DecodeImportNodesParams(&raw, time.UnixMilli(1)); err == nil || !strings.Contains(err.Error(), "nodes must not be empty") {
		t.Fatalf("expected empty nodes error, got %v", err)
	}
}

func TestDecodeSetActiveNodeParamsRequiresNodeID(t *testing.T) {
	raw := json.RawMessage(`{"reload":false}`)
	if _, err := DecodeSetActiveNodeParams(&raw); err == nil || !strings.Contains(err.Error(), "nodeId is required") {
		t.Fatalf("expected nodeId error, got %v", err)
	}
}

func TestDecodeSubscriptionURLParams(t *testing.T) {
	raw := json.RawMessage(`{"url":"https://example.com/sub"}`)
	request, err := DecodeSubscriptionURLParams(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if request.URL != "https://example.com/sub" {
		t.Fatalf("unexpected subscription request: %#v", request)
	}
}

func mustJSON(t *testing.T, value interface{}) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
