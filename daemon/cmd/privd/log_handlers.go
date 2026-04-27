package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

func (d *daemon) handleLogs(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	n := 50
	requestedFiles := []string{"privd"}
	if params != nil {
		var p struct {
			Lines int      `json:"lines"`
			Files []string `json:"files"`
		}
		if err := json.Unmarshal(*params, &p); err == nil {
			if p.Lines > 0 {
				n = p.Lines
			}
			if len(p.Files) > 0 {
				requestedFiles = p.Files
			}
		}
	}
	if n > 500 {
		n = 500
	}

	type logSection struct {
		Name    string   `json:"name"`
		Path    string   `json:"path"`
		Lines   []string `json:"lines"`
		Missing bool     `json:"missing,omitempty"`
		Error   string   `json:"error,omitempty"`
	}

	sections := make([]logSection, 0, len(requestedFiles))
	combined := make([]string, 0, len(requestedFiles)*n)
	for _, spec := range d.resolveLogFileSpecs(requestedFiles) {
		section := logSection{
			Name: spec.Name,
			Path: spec.Path,
		}
		lines, err := readLogTail(spec.Path, n, 512*1024)
		switch {
		case err == nil:
			section.Lines = lines
		case os.IsNotExist(err):
			section.Missing = true
		default:
			section.Error = err.Error()
		}
		sections = append(sections, section)

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

type logFileSpec struct {
	Name string
	Path string
}

func (d *daemon) resolveLogFileSpecs(requested []string) []logFileSpec {
	seen := make(map[string]bool)
	specs := make([]logFileSpec, 0, len(requested))
	for _, raw := range requested {
		key := strings.ToLower(strings.TrimSpace(raw))
		var spec logFileSpec
		switch key {
		case "privd", "daemon":
			spec = logFileSpec{Name: "privd", Path: filepath.Join(d.dataDir, "logs", "privd.log")}
		case "sing-box", "singbox":
			spec = logFileSpec{Name: "sing-box", Path: filepath.Join(d.dataDir, "logs", "sing-box.log")}
		default:
			continue
		}
		if !seen[spec.Name] {
			seen[spec.Name] = true
			specs = append(specs, spec)
		}
	}
	if len(specs) == 0 {
		specs = append(specs, logFileSpec{Name: "privd", Path: filepath.Join(d.dataDir, "logs", "privd.log")})
	}
	return specs
}

func readLogTail(path string, maxLines int, maxBytes int64) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if stat.Size() > maxBytes {
		offset = stat.Size() - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	text := string(data)
	lines := splitLines(text)
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}
