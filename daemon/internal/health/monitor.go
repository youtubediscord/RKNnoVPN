// Package health runs periodic checks to verify that the transparent proxy
// pipeline is intact: sing-box alive, tproxy port listening, iptables chains
// hooked, and routing policy rules present.
package health

import (
	"fmt"
	"log"
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
		logger:     logger,
	}
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

	// 5. DNS resolution (best-effort).
	result.Checks["dns"] = h.checkDNS()

	// Compute overall.
	for _, cr := range result.Checks {
		if !cr.Pass {
			result.Overall = false
			break
		}
	}
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
		return
	}

	h.failures++
	failures := h.failures
	h.mu.Unlock()

	h.logger.Printf("check failed (%d/%d): %s", failures, h.threshold, summarize(result))

	if failures >= h.threshold && state != core.StateDegraded {
		h.manager.SetState(core.StateDegraded)
		h.logger.Printf("threshold reached — state set to degraded")
	}
}

// --------------------------------------------------------------------------
// individual checks
// --------------------------------------------------------------------------

// checkProcessAlive verifies the sing-box PID is still a live process via
// kill(pid, 0).
func (h *HealthMonitor) checkProcessAlive(pid int) CheckResult {
	if pid <= 0 {
		return CheckResult{Pass: false, Detail: "no PID recorded"}
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("FindProcess: %v", err)}
	}
	// Signal 0 tests existence without actually sending a signal.
	err = proc.Signal(syscall.Signal(0))
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("kill -0 %d: %v", pid, err)}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("pid %d alive", pid)}
}

// checkPortListening verifies the tproxy port accepts TCP connections.
func (h *HealthMonitor) checkPortListening(port int) CheckResult {
	err := core.WaitForPort("127.0.0.1", port, 2*time.Second)
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("port %d: %v", port, err)}
	}
	return CheckResult{Pass: true, Detail: fmt.Sprintf("port %d open", port)}
}

// checkIptablesIntact verifies the PRIVSTACK_PRE chain is still hooked in
// the mangle PREROUTING chain.
func (h *HealthMonitor) checkIptablesIntact() CheckResult {
	err := core.ExecIptables("-t", "mangle", "-C", "PREROUTING", "-j", "PRIVSTACK_PRE")
	if err != nil {
		return CheckResult{Pass: false, Detail: "PRIVSTACK_PRE not in PREROUTING"}
	}
	return CheckResult{Pass: true, Detail: "iptables chain intact"}
}

// checkRoutingIntact verifies that the ip rule for fwmark 0x2023 is present.
func (h *HealthMonitor) checkRoutingIntact() CheckResult {
	out, err := core.ExecCommand("ip", "rule", "show")
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("ip rule show: %v", err)}
	}
	if strings.Contains(out, "0x2023") || strings.Contains(out, strconv.Itoa(0x2023)) {
		return CheckResult{Pass: true, Detail: "fwmark rule present"}
	}
	return CheckResult{Pass: false, Detail: "fwmark 0x2023 rule missing"}
}

// checkDNS attempts a trivial DNS lookup via the system resolver.
// This is best-effort: a failure here alone does not necessarily mean the
// proxy is broken (the upstream DNS might be temporarily unreachable).
func (h *HealthMonitor) checkDNS() CheckResult {
	// We use a well-known domain that should always resolve.
	out, err := core.ExecCommand("nslookup", "dns.google", "127.0.0.1")
	if err != nil {
		return CheckResult{Pass: false, Detail: fmt.Sprintf("nslookup: %v", err)}
	}
	if strings.Contains(out, "Address") && !strings.Contains(out, "NXDOMAIN") {
		return CheckResult{Pass: true, Detail: "DNS resolution OK"}
	}
	return CheckResult{Pass: false, Detail: "DNS resolution empty or NXDOMAIN"}
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
