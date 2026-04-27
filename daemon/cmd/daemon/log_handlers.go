package main

import (
	"encoding/json"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/control"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

func (d *daemon) handleLogs(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	request, err := control.DecodeLogsParams(params)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: err.Error(),
		}
	}

	sections := diagnostics.ReadLogSections(d.resolveLogFileSpecs(request.Files), request.Lines, 512*1024, nil)
	combined := make([]string, 0, len(request.Files)*request.Lines)
	for _, section := range sections {
		combined = append(combined, "== "+section.Path+" ==")
		if section.Missing {
			combined = append(combined, "(missing)")
			continue
		}
		if section.Error != "" {
			combined = append(combined, "(error: "+section.Error+")")
			continue
		}
		combined = append(combined, section.Lines...)
	}

	return map[string]interface{}{
		"lines": combined,
		"logs":  sections,
	}, nil
}

type logFileSpec = diagnostics.LogFileSpec

func (d *daemon) resolveLogFileSpecs(requested []string) []logFileSpec {
	return diagnostics.ResolveLogFileSpecs(d.dataDir, requested)
}

func readLogTail(path string, maxLines int, maxBytes int64) ([]string, error) {
	return diagnostics.ReadLogTail(path, maxLines, maxBytes)
}
