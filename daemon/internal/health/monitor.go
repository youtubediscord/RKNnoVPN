// Package health runs periodic checks to verify that the transparent proxy
// pipeline is intact: sing-box alive, tproxy port listening, iptables chains
// hooked, and routing policy rules present.
package health

import (
	"context"
	"fmt"
	"log"
	"net"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/privstack/daemon/internal/core"
)

// CheckResult is the outcome of a single named check.
type CheckResult struct {
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
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
	dnsHost    string
	dnsTimeout time.Duration
	onDegraded func()
	onRestored func()

	failures   int
	lastResult *HealthResult
	stopCh     chan struct{}
	done       chan struct{}
	logger     *log.Logger

	mu sync.Mutex
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
	return &HealthMonitor{
		manager:    manager,
		interval:   interval,
		threshold:  threshold,
		tproxyPort: tproxyPort,
		dnsPort:    dnsPort,
		routeMark:  routeMark,
		dnsHost:    dnsProbeHost(checkURL),
		dnsTimeout: normalizedDNSTimeout(timeout),
		logger:     logger,
	}
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
	h.dnsHost = dnsProbeHost(checkURL)
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
	result.Checks["singbox_alive"] = h.checkProcessAlive(pid)

	// 2. TProxy port listening.
	result.Checks["tproxy_port"] = h.checkPortListening(h.tproxyPort)

	// 3. iptables chain hooked.
	result.Checks["iptables"] = h.checkIptablesIntact()

	// 4. Routing policy rule (fwmark).
	result.Checks["routing"] = h.checkRoutingIntact()

	// 5. DNS resolution (best-effort, not a hard health gate).
	result.Checks["dns"] = h.checkDNS()

	// Hard health only depends on the core process, the local listener, and
	// routing hooks. DNS is intentionally diagnostic-only here: it may fail
	// transiently while the runtime is still usable, and should not by itself
	// trigger teardown/recovery.
	result.Overall =
		result.Checks["singbox_alive"].Pass &&
			result.Checks["tproxy_port"].Pass &&
			result.Checks["iptables"].Pass &&
			result.Checks["routing"].Pass

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
		return CheckResult{Pass: false, Detail: "PID не записан"}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("ошибка FindProcess: %v", err)}
	}
	// Signal 0 tests existence without actually sending a signal.
	err = proc.Signal(syscall.Signal(0))
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("kill -0 %d: %v", pid, err)}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("PID %d активен", pid)}
}

// checkPortListening verifies the tproxy port accepts TCP connections.
func (h *HealthMonitor) checkPortListening(port int) CheckResult {
	err := core.WaitForPort("127.0.0.1", port, 2*time.Second)
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("порт %d: %v", port, err)}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("порт %d открыт", port)}
}

// checkIptablesIntact verifies the PRIVSTACK_PRE chain is still hooked in
// the mangle PREROUTING chain.
func (h *HealthMonitor) checkIptablesIntact() CheckResult {
	err := core.ExecIptables("-t", "mangle", "-C", "PREROUTING", "-j", "PRIVSTACK_PRE")
	if err != nil {
		return CheckResult{Pass: false, Detail: "цепочка PRIVSTACK_PRE не подключена к PREROUTING"}
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
		return CheckResult{Pass: false, Detail: fmt.Sprintf("ошибка ip rule show: %v", err)}
	}

	out6, err := core.ExecCommand("ip", "-6", "rule", "show")
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("ошибка ip -6 rule show: %v", err)}
	}

	hasV4 := strings.Contains(out, markHex) || strings.Contains(out, markDec)
	hasV6 := strings.Contains(out6, markHex) || strings.Contains(out6, markDec)
	if hasV4 && hasV6 {
		return CheckResult{Pass: true, Detail: fmt.Sprintf("правило fwmark %s есть для IPv4 и IPv6", markHex)}
	}
	if hasV4 {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("правило fwmark %s отсутствует для IPv6", markHex)}
	}
	if hasV6 {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("правило fwmark %s отсутствует для IPv4", markHex)}
	}
	return CheckResult{Pass: false, Detail: fmt.Sprintf("правило fwmark %s отсутствует", markHex)}
}

// checkDNS attempts a trivial DNS lookup via the system resolver.
// This is best-effort: a failure here alone does not necessarily mean the
// proxy is broken (the upstream DNS might be temporarily unreachable).
func (h *HealthMonitor) checkDNS() CheckResult {
	port := h.dnsPort
	if port <= 0 {
		port = 10856
	}

	timeout := h.dnsTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: timeout}
			return dialer.DialContext(ctx, "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		},
	}

	host := h.dnsHost
	if host == "" {
		host = "dns.google"
	}
	addrs, err := resolver.LookupHost(ctx, host)
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("lookup %s через 127.0.0.1:%d завершился ошибкой: %v", host, port, err)}
	}
	if len(addrs) == 0 {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("lookup %s через 127.0.0.1:%d не вернул ответов", host, port)}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("DNS для %s через 127.0.0.1:%d работает", host, port)}
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

// summarize produces a one-line summary of failed checks.
func summarize(r *HealthResult) string {
	var parts []string
	for name, cr := range r.Checks {
		if !cr.Pass {
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

func normalizedDNSTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 3 * time.Second
	}
	return timeout
}
