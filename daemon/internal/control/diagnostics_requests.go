package control

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	DefaultDiagnosticsReportLines = 80
	MaxDiagnosticsReportLines     = 300
	DefaultLogLines               = 50
	MaxLogLines                   = 500
)

type DiagnosticsReportRequest struct {
	Lines int
}

type LogsRequest struct {
	Lines int
	Files []string
}

func DecodeDiagnosticsReportParams(params *json.RawMessage) (DiagnosticsReportRequest, error) {
	request := DiagnosticsReportRequest{Lines: DefaultDiagnosticsReportLines}
	if params == nil {
		return request, nil
	}
	var p struct {
		Lines int `json:"lines"`
	}
	if err := decodeStrict(*params, &p); err != nil {
		return request, fmt.Errorf("invalid diagnostics.report params: %w", err)
	}
	if p.Lines > 0 {
		request.Lines = p.Lines
	}
	if request.Lines > MaxDiagnosticsReportLines {
		request.Lines = MaxDiagnosticsReportLines
	}
	return request, nil
}

func DecodeLogsParams(params *json.RawMessage) (LogsRequest, error) {
	request := LogsRequest{
		Lines: DefaultLogLines,
		Files: []string{"daemon"},
	}
	if params == nil {
		return request, nil
	}
	var p struct {
		Lines int      `json:"lines"`
		Files []string `json:"files"`
	}
	if err := decodeStrict(*params, &p); err != nil {
		return request, fmt.Errorf("invalid logs params: %w", err)
	}
	if p.Lines > 0 {
		request.Lines = p.Lines
	}
	if request.Lines > MaxLogLines {
		request.Lines = MaxLogLines
	}
	if len(p.Files) > 0 {
		request.Files = append([]string(nil), p.Files...)
	}
	return request, nil
}

func decodeStrict(data []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
