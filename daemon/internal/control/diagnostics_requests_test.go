package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeDiagnosticsReportParamsDefaultsCapsAndRejectsUnknownFields(t *testing.T) {
	request, err := DecodeDiagnosticsReportParams(nil)
	if err != nil {
		t.Fatal(err)
	}
	if request.Lines != DefaultDiagnosticsReportLines {
		t.Fatalf("unexpected default diagnostics lines: %#v", request)
	}

	raw := json.RawMessage(`{"lines":999}`)
	request, err = DecodeDiagnosticsReportParams(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if request.Lines != MaxDiagnosticsReportLines {
		t.Fatalf("expected capped diagnostics lines, got %#v", request)
	}

	raw = json.RawMessage(`{"legacy":true}`)
	if _, err := DecodeDiagnosticsReportParams(&raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestDecodeLogsParamsDefaultsCapsFilesAndRejectsUnknownFields(t *testing.T) {
	request, err := DecodeLogsParams(nil)
	if err != nil {
		t.Fatal(err)
	}
	if request.Lines != DefaultLogLines || strings.Join(request.Files, ",") != "daemon" {
		t.Fatalf("unexpected default logs request: %#v", request)
	}

	raw := json.RawMessage(`{"lines":999,"files":["daemon","sing-box"]}`)
	request, err = DecodeLogsParams(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if request.Lines != MaxLogLines || strings.Join(request.Files, ",") != "daemon,sing-box" {
		t.Fatalf("unexpected parsed logs request: %#v", request)
	}

	raw = json.RawMessage(`{"tail":100}`)
	if _, err := DecodeLogsParams(&raw); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}
