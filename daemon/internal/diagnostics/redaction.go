package diagnostics

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

type CommandResult struct {
	Command string   `json:"command"`
	Lines   []string `json:"lines,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type JSONSection struct {
	Path    string      `json:"path"`
	Value   interface{} `json:"value,omitempty"`
	Missing bool        `json:"missing,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func RedactNodeProbeResults(results interface{}) interface{} {
	data, err := json.Marshal(results)
	if err != nil {
		return results
	}
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		return results
	}
	return RedactValue("", value)
}

func ReadRedactedJSONFile(path string) JSONSection {
	section := JSONSection{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			section.Missing = true
		} else {
			section.Error = err.Error()
		}
		return section
	}
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		section.Error = err.Error()
		section.Value = LimitLines(SplitLines(RedactText(string(data))), 80)
		return section
	}
	section.Value = RedactValue("", value)
	return section
}

func RedactValue(key string, value interface{}) interface{} {
	if IsSensitiveKey(key) {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		redacted := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			redacted[k] = RedactValue(k, v)
		}
		return redacted
	case []interface{}:
		redacted := make([]interface{}, len(typed))
		for i, v := range typed {
			redacted[i] = RedactValue("", v)
		}
		return redacted
	case string:
		return RedactText(typed)
	default:
		return value
	}
}

func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	switch lower {
	case "uuid", "password", "private_key", "pre_shared_key", "preshared_key", "psk", "short_id", "public_key", "reality_public_key":
		return true
	}
	for _, token := range []string{"password", "private", "preshared", "pre_shared", "secret", "token", "uuid", "short_id", "public_key"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

var (
	uuidPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	keyPattern  = regexp.MustCompile(`(?i)("?(?:uuid|password|private_key|pre_shared_key|preshared_key|psk|short_id|public_key|reality_public_key)"?\s*[:=]\s*)("[^"]*"|[^,\s}]+)`)
)

func RedactText(text string) string {
	text = keyPattern.ReplaceAllString(text, `${1}"[redacted]"`)
	text = uuidPattern.ReplaceAllString(text, "[redacted-uuid]")
	return text
}

func LinesContainAny(lines []string, needles ...string) bool {
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, needle := range needles {
			if strings.Contains(lower, strings.ToLower(needle)) {
				return true
			}
		}
	}
	return false
}

func LinesContainLoopbackDNS(lines []string) bool {
	return FirstLoopbackDNSLine(lines) != ""
}

func FirstLoopbackDNSLine(lines []string) string {
	for _, line := range lines {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "dns") && !strings.Contains(lower, "linkproperties") {
			continue
		}
		if strings.Contains(lower, "127.") ||
			strings.Contains(lower, "/::1") ||
			strings.Contains(lower, "[::1]") ||
			strings.Contains(lower, " localhost") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func CommandLooksEmptySetting(result CommandResult) bool {
	if result.Error != "" {
		return true
	}
	for _, line := range result.Lines {
		value := strings.TrimSpace(line)
		if value != "" && value != "null" && value != ":0" {
			return false
		}
	}
	return true
}

func LimitLines(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return lines[len(lines)-max:]
}

func SplitLines(s string) []string {
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
