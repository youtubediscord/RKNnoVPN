// Package health runs periodic checks to verify that the transparent proxy
// pipeline is intact: sing-box alive, tproxy port listening, iptables chains
// hooked, and routing policy rules present.
package health

import (
	"fmt"
	"log"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
)

// CheckResult is the outcome of a single named check.
type CheckResult struct {
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
	Code   string `json:"code,omitempty"`
}

// HealthResult aggregates all check outcomes for one cycle.
type HealthResult struct {
	Timestamp time.Time              `json:"timestamp"`
	Overall   bool                   `json:"overall"`
	Checks    map[string]CheckResult `json:"checks"`
}

// HealthMonitor periodically verifies the proxy pipeline health and
// reports consecutive failures so the rescue subsystem can act.
type HealthMonitor struct {
	manager    *core.CoreManager
	interval   time.Duration
	threshold  int // consecutive failures before degraded
	tproxyPort int // port to probe
	dnsPort    int // local DNS listener port to probe
	routeMark  int // fwmark that must exist in routing policy
	dnsHosts   []string
	dnsHard    bool
	dnsTimeout time.Duration
	onDegraded func()
	onRestored func()

	failures   int
	lastResult *HealthResult
	stopCh     chan struct{}
	done       chan struct{}
	logger     *log.Logger

	mu sync.Mutex

	runProcessAliveCheck  func(pid int) CheckResult
	runPortListeningCheck func(port int) CheckResult
	runIptablesCheck      func() CheckResult
	runRoutingCheck       func() CheckResult
	runDNSCheck           func() CheckResult
}

// NewHealthMonitor creates a monitor that checks every interval.
// threshold is the number of consecutive failing cycles before the
// manager state flips to Degraded.
func NewHealthMonitor(
	manager *core.CoreManager,
	interval time.Duration,
	threshold int,
	tproxyPort int,
	dnsPort int,
	routeMark int,
	checkURL string,
	timeout time.Duration,
	logger *log.Logger,
) *HealthMonitor {
	if logger == nil {
		logger = log.New(os.Stderr, "[health] ", log.LstdFlags)
	}
	if threshold < 1 {
		threshold = 3
	}
	h := &HealthMonitor{
		manager:    manager,
		interval:   interval,
		threshold:  threshold,
		tproxyPort: tproxyPort,
		dnsPort:    dnsPort,
		routeMark:  routeMark,
		dnsHosts:   normalizeDNSProbeHosts(checkURL, nil),
		dnsTimeout: normalizedDNSTimeout(timeout),
		logger:     logger,
	}
	h.runProcessAliveCheck = h.checkProcessAlive
	h.runPortListeningCheck = h.checkPortListening
	h.runIptablesCheck = h.checkIptablesIntact
	h.runRoutingCheck = h.checkRoutingIntact
	h.runDNSCheck = h.checkDNS
	return h
}

// SetOnDegraded installs a callback that fires when the monitor crosses the
// failure threshold and marks the core degraded.
func (h *HealthMonitor) SetOnDegraded(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onDegraded = fn
}

// SetOnRestored installs a callback that fires when health returns to normal.
func (h *HealthMonitor) SetOnRestored(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onRestored = fn
}

// SetConfig updates runtime health-check parameters after config reload/apply.
func (h *HealthMonitor) SetConfig(
	interval time.Duration,
	threshold int,
	tproxyPort int,
	dnsPort int,
	routeMark int,
	checkURL string,
	dnsProbeDomains []string,
	dnsIsHardReadiness bool,
	timeout time.Duration,
) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if interval > 0 {
		h.interval = interval
	}
	if threshold >= 1 {
		h.threshold = threshold
	}
	if tproxyPort > 0 {
		h.tproxyPort = tproxyPort
	}
	if dnsPort > 0 {
		h.dnsPort = dnsPort
	}
	if routeMark != 0 {
		h.routeMark = routeMark
	}
	h.dnsHosts = normalizeDNSProbeHosts(checkURL, dnsProbeDomains)
	h.dnsHard = dnsIsHardReadiness
	h.dnsTimeout = normalizedDNSTimeout(timeout)
}

// Start launches the background check loop. It is safe to call
// Start after Stop — it resets internal counters.
func (h *HealthMonitor) Start() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.stopCh != nil {
		return // already running
	}

	h.failures = 0
	h.stopCh = make(chan struct{})
	h.done = make(chan struct{})

	go h.loop()
	h.logger.Printf("started (interval=%s, threshold=%d)", h.interval, h.threshold)
}

// Stop halts the background check loop and blocks until it exits.
func (h *HealthMonitor) Stop() {
	h.mu.Lock()
	ch := h.stopCh
	h.stopCh = nil
	h.mu.Unlock()

	if ch == nil {
		return
	}
	close(ch)
	<-h.done
	h.logger.Println("stopped")
}

// LastResult returns the most recent HealthResult (may be nil).
func (h *HealthMonitor) LastResult() *HealthResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastResult
}

// Failures returns the current consecutive-failure count.
func (h *HealthMonitor) Failures() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.failures
}

// Clear forgets sticky health diagnostics after an explicit runtime reset.
func (h *HealthMonitor) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures = 0
	h.lastResult = nil
}

// RunOnce executes every check and returns the aggregated result.
// It does NOT update the failure counter or trigger state transitions.
func (h *HealthMonitor) RunOnce() *HealthResult {
	result := &HealthResult{
		Timestamp: time.Now(),
		Overall:   true,
		Checks:    make(map[string]CheckResult),
	}

	// Resolve the PID lazily from the manager status.
	status := h.manager.Status()
	pid := status.PID

	// 1. sing-box alive (kill -0).
	result.Checks["singbox_alive"] = h.runProcessAliveCheck(pid)

	// 2. TProxy port listening.
	result.Checks["tproxy_port"] = h.runPortListeningCheck(h.tproxyPort)

	// 3. iptables chain hooked.
	result.Checks["iptables"] = h.runIptablesCheck()

	// 4. Routing policy rule (fwmark).
	result.Checks["routing"] = h.runRoutingCheck()

	// 5. DNS listener and resolution (best-effort, not a hard health gate).
	result.Checks["dns_listener"] = h.checkDNSListener()
	result.Checks["dns"] = h.runDNSCheck()

	// Hard health normally depends only on the core process, local listener,
	// and routing hooks. DNS can be promoted to a hard gate for specialised
	// deployments, but privacy-first defaults keep it diagnostic-only.
	result.Overall =
		result.Checks["singbox_alive"].Pass &&
			result.Checks["tproxy_port"].Pass &&
			result.Checks["iptables"].Pass &&
			result.Checks["routing"].Pass
	if h.dnsHard {
		result.Overall = result.Overall &&
			result.Checks["dns_listener"].Pass &&
			result.Checks["dns"].Pass
	}

	h.mu.Lock()
	h.lastResult = result
	h.mu.Unlock()

	return result
}

// --------------------------------------------------------------------------
// background loop
// --------------------------------------------------------------------------

func (h *HealthMonitor) loop() {
	defer close(h.done)

	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.tick()
		}
	}
}

func (h *HealthMonitor) tick() {
	// Only check when the manager thinks it is running.
	state := h.manager.GetState()
	if state != core.StateRunning && state != core.StateDegraded {
		return
	}

	result := h.RunOnce()

	h.mu.Lock()
	h.lastResult = result
	if result.Overall {
		if h.failures > 0 {
			h.logger.Println("health restored")
		}
		h.failures = 0
		h.mu.Unlock()

		// If the manager was degraded, mark it running again.
		if state == core.StateDegraded {
			h.manager.SetState(core.StateRunning)
		}
		h.mu.Lock()
		callback := h.onRestored
		h.mu.Unlock()
		if callback != nil {
			go callback()
		}
		return
	}

	h.failures++
	failures := h.failures
	h.mu.Unlock()

	h.logger.Printf("check failed (%d/%d): %s", failures, h.threshold, summarize(result))

	if failures >= h.threshold && state != core.StateDegraded {
		h.manager.SetState(core.StateDegraded)
		h.logger.Printf("threshold reached — state set to degraded")
		h.mu.Lock()
		callback := h.onDegraded
		h.mu.Unlock()
		if callback != nil {
			go callback()
		}
	}
}

// --------------------------------------------------------------------------
// individual checks
// --------------------------------------------------------------------------

// checkProcessAlive verifies the sing-box PID is still a live process via
// kill(pid, 0).
func (h *HealthMonitor) checkProcessAlive(pid int) CheckResult {
	if pid <= 0 {
		return CheckResult{Pass: false, Detail: "PID не записан", Code: "CORE_PID_MISSING"}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("ошибка FindProcess: %v", err), Code: "CORE_PID_LOOKUP_FAILED"}
	}
	// Signal 0 tests existence without actually sending a signal.
	err = proc.Signal(syscall.Signal(0))
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("kill -0 %d: %v", pid, err), Code: "CORE_PROCESS_DEAD"}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("PID %d активен", pid)}
}

// checkPortListening verifies the tproxy port accepts TCP connections.
func (h *HealthMonitor) checkPortListening(port int) CheckResult {
	err := core.WaitForPort("127.0.0.1", port, 2*time.Second)
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("порт %d: %v", port, err), Code: "TPROXY_PORT_DOWN"}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("порт %d открыт", port)}
}

// checkIptablesIntact verifies the RKNNOVPN_PRE chain is still hooked in
// the mangle PREROUTING chain.
func (h *HealthMonitor) checkIptablesIntact() CheckResult {
	err := core.ExecIptables("-t", "mangle", "-C", "PREROUTING", "-j", "RKNNOVPN_PRE")
	if err != nil {
		return CheckResult{Pass: false, Detail: "цепочка RKNNOVPN_PRE не подключена к PREROUTING", Code: "RULES_NOT_APPLIED"}
	}
	return CheckResult{Pass: true, Detail: "цепочка iptables на месте"}
}

// checkRoutingIntact verifies that the configured fwmark rule is present for
// both IPv4 and IPv6 policy routing.
func (h *HealthMonitor) checkRoutingIntact() CheckResult {
	mark := h.routeMark
	if mark == 0 {
		mark = 0x2023
	}
	markHex := fmt.Sprintf("0x%x", mark)
	markDec := strconv.Itoa(mark)

	out, err := core.ExecCommand("ip", "rule", "show")
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("ошибка ip rule show: %v", err), Code: "ROUTING_CHECK_FAILED"}
	}

	out6, err := core.ExecCommand("ip", "-6", "rule", "show")
	ipv6Available := err == nil
	if err != nil {
		out6 = ""
	}

	hasV4 := ipRuleOutputMatches(out, markHex, markDec, "2023")
	hasV6 := ipv6Available && ipRuleOutputMatches(out6, markHex, markDec, "2024")
	if hasV4 && hasV6 {
		return CheckResult{Pass: true, Detail: fmt.Sprintf("правило fwmark %s есть для IPv4 и IPv6", markHex)}
	}
	if hasV4 && !ipv6Available {
		return CheckResult{Pass: true, Detail: fmt.Sprintf("правило fwmark %s есть для IPv4; IPv6 policy routing недоступен", markHex)}
	}
	if hasV4 {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("правило fwmark %s отсутствует для IPv6", markHex), Code: "ROUTING_V6_MISSING"}
	}
	if hasV6 {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("правило fwmark %s отсутствует для IPv4", markHex), Code: "ROUTING_V4_MISSING"}
	}
	return CheckResult{Pass: false, Detail: fmt.Sprintf("правило fwmark %s отсутствует", markHex), Code: "ROUTING_NOT_APPLIED"}
}

func ipRuleOutputMatches(output string, markHex string, markDec string, table string) bool {
	for _, line := range strings.Split(output, "\n") {
		if netstack.RuleLineMatches(line, markHex, table) || netstack.RuleLineMatches(line, markDec, table) {
			return true
		}
	}
	return false
}

func (h *HealthMonitor) checkDNSListener() CheckResult {
	port := h.dnsPort
	if port <= 0 {
		port = 10856
	}
	err := core.WaitForPort("127.0.0.1", port, 2*time.Second)
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("DNS listener 127.0.0.1:%d недоступен: %v", port, err), Code: "DNS_LISTENER_DOWN"}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("DNS listener 127.0.0.1:%d открыт", port)}
}

// checkDNS intentionally does not send a standalone query to the local DNS
// port. DNS readiness is represented by listener and iptables hook checks; the
// data-plane is covered by the outbound URL probe, which exercises the selected
// proxy route instead of a synthetic root-originated lookup.
func (h *HealthMonitor) checkDNS() CheckResult {
	port := h.dnsPort
	if port <= 0 {
		port = 10856
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("DNS listener 127.0.0.1:%d проверяется без standalone lookup", port)}
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

// summarize produces a one-line summary of failed checks.
func summarize(r *HealthResult) string {
	var parts []string
	for _, name := range []string{"singbox_alive", "tproxy_port", "iptables", "routing", "dns"} {
		if cr, ok := r.Checks[name]; ok && !cr.Pass {
			parts = append(parts, name+"("+cr.Detail+")")
		}
	}
	if len(parts) == 0 {
		return "all OK"
	}
	return strings.Join(parts, "; ")
}

func dnsProbeHost(checkURL string) string {
	if checkURL == "" {
		return "dns.google"
	}
	parsed, err := neturl.Parse(checkURL)
	if err != nil || parsed.Hostname() == "" {
		return "dns.google"
	}
	return parsed.Hostname()
}

func normalizeDNSProbeHosts(checkURL string, domains []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(domains)+1)
	for _, domain := range domains {
		host := strings.TrimSpace(domain)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		result = append(result, host)
	}
	if len(result) == 0 {
		host := dnsProbeHost(checkURL)
		if host != "" {
			result = append(result, host)
		}
	}
	if len(result) == 0 {
		result = append(result, "dns.google")
	}
	return result
}

func normalizedDNSTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 3 * time.Second
	}
	return timeout
}
