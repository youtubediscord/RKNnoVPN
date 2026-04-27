package diagnostics

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

type LogFileSpec struct {
	Name string
	Path string
}

type LogSection struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Lines   []string `json:"lines"`
	Missing bool     `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func ResolveLogFileSpecs(dataDir string, requested []string) []LogFileSpec {
	seen := make(map[string]bool)
	specs := make([]LogFileSpec, 0, len(requested))
	for _, raw := range requested {
		key := strings.ToLower(strings.TrimSpace(raw))
		var spec LogFileSpec
		switch key {
		case "daemon":
			spec = LogFileSpec{Name: "daemon", Path: filepath.Join(dataDir, "logs", "daemon.log")}
		case "sing-box", "singbox":
			spec = LogFileSpec{Name: "sing-box", Path: filepath.Join(dataDir, "logs", "sing-box.log")}
		default:
			continue
		}
		if !seen[spec.Name] {
			seen[spec.Name] = true
			specs = append(specs, spec)
		}
	}
	if len(specs) == 0 {
		specs = append(specs, LogFileSpec{Name: "daemon", Path: filepath.Join(dataDir, "logs", "daemon.log")})
	}
	return specs
}

func DefaultLogFileSpecs(dataDir string) []LogFileSpec {
	return []LogFileSpec{
		{Name: "daemon", Path: filepath.Join(dataDir, "logs", "daemon.log")},
		{Name: "sing-box", Path: filepath.Join(dataDir, "logs", "sing-box.log")},
	}
}

func ReadLogSections(specs []LogFileSpec, maxLines int, maxBytes int64, redact func(string) string) []LogSection {
	sections := make([]LogSection, 0, len(specs))
	for _, spec := range specs {
		section := LogSection{Name: spec.Name, Path: spec.Path}
		lines, err := ReadLogTail(spec.Path, maxLines, maxBytes)
		switch {
		case err == nil:
			if redact == nil {
				section.Lines = lines
			} else {
				section.Lines = make([]string, 0, len(lines))
				for _, line := range lines {
					section.Lines = append(section.Lines, redact(line))
				}
			}
		case os.IsNotExist(err):
			section.Missing = true
		default:
			section.Error = err.Error()
		}
		sections = append(sections, section)
	}
	return sections
}

func ReadLogTail(path string, maxLines int, maxBytes int64) ([]string, error) {
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
	lines := splitLines(string(data))
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
