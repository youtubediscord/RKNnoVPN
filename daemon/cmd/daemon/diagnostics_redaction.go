package main

import "github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"

func redactNodeProbeResults(results interface{}) interface{} {
	return diagnostics.RedactNodeProbeResults(results)
}

func readRedactedJSONFile(path string) diagnosticJSONSection {
	return diagnostics.ReadRedactedJSONFile(path)
}

func redactDiagnosticValue(key string, value interface{}) interface{} {
	return diagnostics.RedactValue(key, value)
}

func isSensitiveDiagnosticKey(key string) bool {
	return diagnostics.IsSensitiveKey(key)
}

func redactDiagnosticText(text string) string {
	return diagnostics.RedactText(text)
}

func diagnosticLinesContainAny(lines []string, needles ...string) bool {
	return diagnostics.LinesContainAny(lines, needles...)
}

func firstVPNLikeInterfaceLine(lines []string) string {
	return diagnostics.FirstVPNLikeInterfaceLine(lines)
}

func ipLinkInterfaceName(line string) string {
	return diagnostics.IPLinkInterfaceName(line)
}

func diagnosticLinesContainLoopbackDNS(lines []string) bool {
	return diagnostics.LinesContainLoopbackDNS(lines)
}

func diagnosticFirstLoopbackDNSLine(lines []string) string {
	return diagnostics.FirstLoopbackDNSLine(lines)
}

func diagnosticCommandLooksEmptySetting(result diagnosticCommandResult) bool {
	return diagnostics.CommandLooksEmptySetting(result)
}

func limitLines(lines []string, max int) []string {
	return diagnostics.LimitLines(lines, max)
}
