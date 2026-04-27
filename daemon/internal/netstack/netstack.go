package netstack

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ExecScriptFunc func(scriptPath string, command string, env map[string]string) error
type ExecCommandFunc func(name string, args ...string) (string, error)

type Step struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	Operation       string   `json:"operation"`
	Status          string   `json:"status"`
	Steps           []Step   `json:"steps"`
	Errors          []string `json:"errors,omitempty"`
	Leftovers       []string `json:"leftovers,omitempty"`
	RollbackApplied bool     `json:"rollbackApplied,omitempty"`
}

type Error struct {
	Operation string
	Code      string
	Report    Report
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Report.Errors) == 0 {
		return e.Operation + " failed"
	}
	return fmt.Sprintf("%s failed: %s", e.Operation, strings.Join(e.Report.Errors, "; "))
}

type Manager struct {
	dataDir     string
	env         map[string]string
	execScript  ExecScriptFunc
	execCommand ExecCommandFunc
}

func New(dataDir string, env map[string]string, execScript ExecScriptFunc) Manager {
	return Manager{
		dataDir:    dataDir,
		env:        env,
		execScript: execScript,
	}
}

func (m Manager) WithExecCommand(execCommand ExecCommandFunc) Manager {
	m.execCommand = execCommand
	return m
}

func (m Manager) Apply() Report {
	report := newReport("apply")
	m.cleanupInto(&report, true)
	if report.Status == "failed" {
		return report
	}

	if err := m.run("iptables", "start"); err != nil {
		report.addFailure("iptables-start", err)
		report.RollbackApplied = true
		m.cleanupInto(&report, true)
		return report
	}
	report.addOK("iptables-start", "")

	if err := m.run("dns", "start"); err != nil {
		report.addFailure("dns-start", err)
		report.RollbackApplied = true
		m.cleanupInto(&report, true)
		return report
	}
	report.addOK("dns-start", "")
	return report
}

func (m Manager) Cleanup() Report {
	report := newReport("cleanup")
	m.cleanupInto(&report, true)
	return report
}

func (m Manager) Verify() Report {
	report := newReport("verify")

	if err := m.run("iptables", "status"); err != nil {
		report.addFailure("iptables-status", err)
		return report
	}
	report.addOK("iptables-status", "")

	if err := m.run("dns", "status"); err != nil {
		report.addFailure("dns-status", err)
		return report
	}
	report.addOK("dns-status", "")
	return report
}

func (m Manager) VerifyCleanup() Report {
	report := newReport("verify-cleanup")
	leftovers := m.CollectLeftovers()
	report.Leftovers = leftovers
	if len(leftovers) == 0 {
		report.addOK("verify-cleanup", "")
		return report
	}
	report.Status = "partial"
	report.Steps = append(report.Steps, Step{
		Name:   "verify-cleanup",
		Status: "failed",
		Detail: strings.Join(leftovers, "; "),
	})
	return report
}

func (m Manager) CollectLeftovers() []string {
	if m.execCommand == nil {
		return []string{"exec command function is nil"}
	}

	leftovers := make([]string, 0)
	add := func(format string, args ...interface{}) {
		leftovers = append(leftovers, fmt.Sprintf(format, args...))
	}

	for _, spec := range []struct {
		bin   string
		table string
	}{
		{bin: "iptables", table: "raw"},
		{bin: "iptables", table: "mangle"},
		{bin: "iptables", table: "nat"},
		{bin: "iptables", table: "filter"},
		{bin: "ip6tables", table: "raw"},
		{bin: "ip6tables", table: "mangle"},
		{bin: "ip6tables", table: "nat"},
		{bin: "ip6tables", table: "filter"},
		{bin: "iptables-legacy", table: "raw"},
		{bin: "iptables-legacy", table: "mangle"},
		{bin: "iptables-legacy", table: "nat"},
		{bin: "iptables-legacy", table: "filter"},
		{bin: "ip6tables-legacy", table: "raw"},
		{bin: "ip6tables-legacy", table: "mangle"},
		{bin: "ip6tables-legacy", table: "nat"},
		{bin: "ip6tables-legacy", table: "filter"},
		{bin: "iptables-nft", table: "raw"},
		{bin: "iptables-nft", table: "mangle"},
		{bin: "iptables-nft", table: "nat"},
		{bin: "iptables-nft", table: "filter"},
		{bin: "ip6tables-nft", table: "raw"},
		{bin: "ip6tables-nft", table: "mangle"},
		{bin: "ip6tables-nft", table: "nat"},
		{bin: "ip6tables-nft", table: "filter"},
	} {
		out, err := m.execCommand(spec.bin, "-w", "100", "-t", spec.table, "-S")
		if err != nil {
			if isMissingCommandError(err) || isMissingKernelTableOutput(out) {
				continue
			}
			if strings.TrimSpace(out) != "" {
				add("%s %s check failed: %v: %s", spec.bin, spec.table, err, firstLine(out))
			} else {
				add("%s %s check failed: %v", spec.bin, spec.table, err)
			}
			continue
		}
		if line := firstLineContaining(out, "PRIVSTACK"); line != "" {
			add("%s %s rule remains: %s", spec.bin, spec.table, line)
		}
	}

	mark := strings.ToLower(m.envValue("FWMARK"))
	routeTable := m.envValue("ROUTE_TABLE")
	routeTableV6 := m.envValue("ROUTE_TABLE_V6")
	for _, spec := range []struct {
		name  string
		args  []string
		table string
	}{
		{name: "ip rule", args: []string{"rule", "show"}, table: routeTable},
		{name: "ip -6 rule", args: []string{"-6", "rule", "show"}, table: routeTableV6},
	} {
		out, err := m.execCommand("ip", spec.args...)
		if err != nil {
			add("%s check failed: %v", spec.name, err)
			continue
		}
		for _, line := range splitLines(out) {
			if RuleLineMatches(line, mark, spec.table) {
				add("%s remains: %s", spec.name, strings.TrimSpace(line))
				break
			}
		}
	}

	for _, spec := range []struct {
		name string
		args []string
	}{
		{name: "ip route table " + routeTable, args: []string{"route", "show", "table", routeTable}},
		{name: "ip -6 route table " + routeTableV6, args: []string{"-6", "route", "show", "table", routeTableV6}},
	} {
		out, err := m.execCommand("ip", spec.args...)
		if err != nil {
			if isMissingRouteTableOutput(out) {
				continue
			}
			if strings.TrimSpace(out) == "" {
				add("%s check failed: %v", spec.name, err)
			} else {
				add("%s check failed: %v: %s", spec.name, err, firstLine(out))
			}
			continue
		}
		if strings.TrimSpace(out) != "" {
			add("%s still has routes: %s", spec.name, firstLine(out))
		}
	}

	if out, _ := m.execCommand("pidof", "sing-box"); strings.TrimSpace(out) != "" {
		add("sing-box process still running: %s", strings.TrimSpace(out))
	}

	for _, port := range m.effectiveLocalPorts() {
		if isTCPPortListening("127.0.0.1", port, 150*time.Millisecond) {
			add("localhost TCP port %d still listening", port)
		}
	}

	for _, path := range []string{
		filepath.Join(m.dataDir, "run", "singbox.pid"),
		filepath.Join(m.dataDir, "run", "active"),
		filepath.Join(m.dataDir, "run", "net_change.lock"),
		filepath.Join(m.dataDir, "run", "iptables.rules"),
		filepath.Join(m.dataDir, "run", "ip6tables.rules"),
		filepath.Join(m.dataDir, "run", "env.sh"),
	} {
		if _, err := os.Stat(path); err == nil {
			add("stale runtime file remains: %s", path)
		}
	}

	return leftovers
}

func (r Report) Err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	code := "NETSTACK_FAILED"
	for _, step := range r.Steps {
		if step.Status != "failed" {
			continue
		}
		switch step.Name {
		case "iptables-start":
			code = "RULES_NOT_APPLIED"
		case "dns-start":
			code = "DNS_APPLY_FAILED"
		case "dns-stop", "iptables-stop":
			code = "NETSTACK_CLEANUP_FAILED"
		case "dns-status", "iptables-status":
			code = "NETSTACK_VERIFY_FAILED"
		}
		break
	}
	return &Error{Operation: r.Operation, Code: code, Report: r}
}

func newReport(operation string) Report {
	return Report{Operation: operation, Status: "ok"}
}

func (r *Report) addOK(name string, detail string) {
	r.Steps = append(r.Steps, Step{Name: name, Status: "ok", Detail: detail})
}

func (r *Report) addAlreadyClean(name string, detail string) {
	r.Steps = append(r.Steps, Step{Name: name, Status: "already_clean", Detail: detail})
}

func (r *Report) addFailure(name string, err error) {
	r.Status = "failed"
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	r.Steps = append(r.Steps, Step{Name: name, Status: "failed", Detail: detail})
	r.Errors = append(r.Errors, name+": "+detail)
}

func (m Manager) cleanupInto(report *Report, tolerateMissing bool) {
	for _, name := range []string{"dns", "iptables"} {
		stepName := name + "-stop"
		err := m.run(name, "stop")
		switch {
		case err == nil:
			report.addOK(stepName, "")
		case tolerateMissing && isMissingScriptError(err):
			report.addAlreadyClean(stepName, err.Error())
		default:
			report.addFailure(stepName, err)
		}
	}
}

func (m Manager) run(name string, command string) error {
	if m.execScript == nil {
		return fmt.Errorf("exec script function is nil")
	}
	return m.execScript(filepath.Join(m.dataDir, "scripts", name+".sh"), command, m.env)
}

func isMissingScriptError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "script not found:") ||
		strings.Contains(lower, "no such file or directory")
}

func isMissingKernelTableOutput(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "table does not exist") ||
		strings.Contains(lower, "can't initialize") ||
		strings.Contains(lower, "does not exist")
}

func isMissingCommandError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "executable file not found") ||
		strings.Contains(lower, "no such file or directory")
}

func isMissingRouteTableOutput(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "fib table does not exist") ||
		strings.Contains(lower, "no such process") ||
		strings.Contains(lower, "no such file")
}

// RuleLineMatches reports whether an `ip rule show` line belongs to the
// PrivStack fwmark/table contract. It intentionally avoids substring matches
// so unrelated marks such as 0x20230 do not look like PrivStack leftovers.
func RuleLineMatches(line string, mark string, table string) bool {
	fields := strings.Fields(strings.ToLower(line))
	wantMark := strings.ToLower(strings.TrimSpace(mark))
	wantTable := strings.TrimSpace(table)
	for i, field := range fields {
		if field == "fwmark" && wantMark != "" && i+1 < len(fields) {
			got := fields[i+1]
			if got == wantMark || strings.HasPrefix(got, wantMark+"/") {
				return true
			}
		}
		if (field == "lookup" || field == "table") && wantTable != "" && i+1 < len(fields) {
			if fields[i+1] == wantTable {
				return true
			}
		}
	}
	return false
}

func (m Manager) effectiveLocalPorts() []int {
	ports := []int{
		valueOrDefaultInt(m.envInt("TPROXY_PORT"), 10853),
		valueOrDefaultInt(m.envInt("DNS_PORT"), 10856),
		m.envInt("API_PORT"),
		m.envInt("SOCKS_PORT"),
		m.envInt("HTTP_PORT"),
	}
	for _, field := range strings.Fields(m.envValue("CHAIN_PROXY_PORTS")) {
		port, err := strconv.Atoi(field)
		if err == nil {
			ports = append(ports, port)
		}
	}
	seen := make(map[int]bool, len(ports))
	result := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		result = append(result, port)
	}
	return result
}

func (m Manager) envValue(key string) string {
	if m.env == nil {
		return ""
	}
	return strings.TrimSpace(m.env[key])
}

func (m Manager) envInt(key string) int {
	value, _ := strconv.Atoi(m.envValue(key))
	return value
}

func valueOrDefaultInt(value int, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func isTCPPortListening(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func firstLineContaining(text string, needle string) string {
	for _, line := range splitLines(text) {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func firstLine(text string) string {
	for _, line := range splitLines(text) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
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
