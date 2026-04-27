package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

func redactNodeProbeResults(results interface{}) interface{} {
	data, err := json.Marshal(results)
	if err != nil {
		return results
	}
	var value interface{}
	if err := json.Unmarshal(data, &value); err != nil {
		return results
	}
	return redactDiagnosticValue("", value)
}

func readRedactedJSONFile(path string) diagnosticJSONSection {
	section := diagnosticJSONSection{Path: path}
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
		section.Value = limitLines(splitLines(redactDiagnosticText(string(data))), 80)
		return section
	}
	section.Value = redactDiagnosticValue("", value)
	return section
}

func redactDiagnosticValue(key string, value interface{}) interface{} {
	if isSensitiveDiagnosticKey(key) {
		return "[redacted]"
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		redacted := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			redacted[k] = redactDiagnosticValue(k, v)
		}
		return redacted
	case []interface{}:
		redacted := make([]interface{}, len(typed))
		for i, v := range typed {
			redacted[i] = redactDiagnosticValue("", v)
		}
		return redacted
	case string:
		return redactDiagnosticText(typed)
	default:
		return value
	}
}

func isSensitiveDiagnosticKey(key string) bool {
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
	diagnosticUUIDPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	diagnosticKeyPattern  = regexp.MustCompile(`(?i)("?(?:uuid|password|private_key|pre_shared_key|preshared_key|psk|short_id|public_key|reality_public_key)"?\s*[:=]\s*)("[^"]*"|[^,\s}]+)`)
)

func redactDiagnosticText(text string) string {
	text = diagnosticKeyPattern.ReplaceAllString(text, `${1}"[redacted]"`)
	text = diagnosticUUIDPattern.ReplaceAllString(text, "[redacted-uuid]")
	return text
}

func diagnosticLinesContainAny(lines []string, needles ...string) bool {
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

func firstVPNLikeInterfaceLine(lines []string) string {
	for _, line := range lines {
		name := ipLinkInterfaceName(line)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "tun") ||
			strings.HasPrefix(lower, "wg") ||
			strings.HasPrefix(lower, "tap") ||
			strings.HasPrefix(lower, "ppp") ||
			strings.HasPrefix(lower, "ipsec") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func ipLinkInterfaceName(line string) string {
	line = strings.TrimSpace(line)
	firstColon := strings.Index(line, ":")
	if firstColon < 0 {
		return ""
	}
	rest := strings.TrimSpace(line[firstColon+1:])
	secondColon := strings.Index(rest, ":")
	if secondColon < 0 {
		return ""
	}
	name := strings.TrimSpace(rest[:secondColon])
	name = strings.TrimSuffix(name, "@NONE")
	if at := strings.Index(name, "@"); at >= 0 {
		name = name[:at]
	}
	return name
}

func diagnosticLinesContainLoopbackDNS(lines []string) bool {
	return diagnosticFirstLoopbackDNSLine(lines) != ""
}

func diagnosticFirstLoopbackDNSLine(lines []string) string {
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

func diagnosticCommandLooksEmptySetting(result diagnosticCommandResult) bool {
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

func limitLines(lines []string, max int) []string {
	if max <= 0 || len(lines) <= max {
		return lines
	}
	return lines[len(lines)-max:]
}
