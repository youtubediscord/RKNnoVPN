// Package rescue provides automatic recovery when the health monitor
// detects persistent failures in the transparent proxy pipeline.
//
// Recovery proceeds through three increasingly aggressive strategies:
//  1. Restart sing-box only (iptables stay)
//  2. Re-apply iptables + DNS rules
//  3. Full teardown + cold start
//
// If all strategies are exhausted, Rollback tears down PrivStack-owned
// runtime state without restoring stale whole-table iptables backups.
package rescue

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
)

// Strategy identifies which recovery approach is being attempted.
type Strategy int

const (
	StrategyRestartCore  Strategy = iota // kill + restart sing-box
	StrategyReapplyRules                 // re-run iptables/DNS scripts
	StrategyFullRestart                  // full stop + start
)

// String returns a human-readable label.
func (s Strategy) String() string {
	switch s {
	case StrategyRestartCore:
		return "restart-core"
	case StrategyReapplyRules:
		return "reapply-rules"
	case StrategyFullRestart:
		return "full-restart"
	default:
		return "unknown"
	}
}

// RescueManager orchestrates automatic recovery from degraded state.
type RescueManager struct {
	core        *core.CoreManager
	cfg         *config.Config
	dataDir     string
	maxAttempts int
	attempts    int
	lastAttempt time.Time
	cooldown    time.Duration
	logger      *log.Logger
	mu          sync.Mutex
}

type RecoveryGate func() bool

// NewRescueManager creates a rescue manager that will try up to
// maxAttempts strategies before giving up.
func NewRescueManager(
	coreMgr *core.CoreManager,
	cfg *config.Config,
	dataDir string,
	maxAttempts int,
	cooldown time.Duration,
	logger *log.Logger,
) *RescueManager {
	if logger == nil {
		logger = log.New(os.Stderr, "[rescue] ", log.LstdFlags)
	}
	if maxAttempts < 1 {
		maxAttempts = 3
	}
	return &RescueManager{
		core:        coreMgr,
		cfg:         cfg,
		dataDir:     dataDir,
		maxAttempts: maxAttempts,
		cooldown:    cooldown,
		logger:      logger,
	}
}

// Attempt runs the next recovery strategy based on how many attempts
// have already been made.
//
// Returns nil if recovery succeeded (the health monitor should confirm
// on its next cycle). Returns an error if the strategy failed. If all
// strategies are exhausted, it returns an error indicating rescue is
// depleted and the caller should invoke Rollback.
func (r *RescueManager) Attempt(canProceed RecoveryGate) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !recoveryAllowed(canProceed) {
		return fmt.Errorf("rescue: cancelled by desired runtime state")
	}

	// Enforce cooldown between attempts.
	if !r.lastAttempt.IsZero() && time.Since(r.lastAttempt) < r.cooldown {
		wait := r.cooldown - time.Since(r.lastAttempt)
		r.logger.Printf("cooldown active, %s remaining", wait.Truncate(time.Second))
		return fmt.Errorf("rescue: cooldown active (%s remaining)", wait.Truncate(time.Second))
	}

	if r.attempts >= r.maxAttempts {
		r.logger.Printf("all %d strategies exhausted", r.maxAttempts)
		return fmt.Errorf("rescue: all %d strategies exhausted — call Rollback", r.maxAttempts)
	}

	strategy := r.pickStrategy()
	r.attempts++
	r.lastAttempt = time.Now()

	r.core.SetState(core.StateRescue)
	r.logger.Printf("attempt %d/%d: strategy=%s", r.attempts, r.maxAttempts, strategy)

	if !recoveryAllowed(canProceed) {
		return fmt.Errorf("rescue: cancelled by desired runtime state")
	}

	var err error
	switch strategy {
	case StrategyRestartCore:
		err = r.restartCore(canProceed)
	case StrategyReapplyRules:
		err = r.reapplyRules(canProceed)
	case StrategyFullRestart:
		err = r.fullRestart(canProceed)
	}

	if err != nil {
		r.logger.Printf("strategy %s failed: %v", strategy, err)
		if recoveryAllowed(canProceed) {
			r.core.SetState(core.StateDegraded)
		}
		return fmt.Errorf("rescue: %s: %w", strategy, err)
	}
	if !recoveryAllowed(canProceed) {
		return fmt.Errorf("rescue: cancelled by desired runtime state")
	}

	r.logger.Printf("strategy %s succeeded", strategy)
	r.core.SetState(core.StateRunning)
	return nil
}

// Rollback tears down all PrivStack-owned network state. This is the last
// resort when all recovery strategies have failed.
func (r *RescueManager) Rollback() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.logger.Println("rollback: tearing down all proxy state")

	// 1. Forced stop (DNS, PrivStack-only iptables cleanup, sing-box).
	if err := r.core.RescueReset(); err != nil {
		r.logger.Printf("rollback: stop failed: %v", err)
		return fmt.Errorf("rescue: rollback stop: %w", err)
	}

	// 2. Run the same root-level cleanup script used by the UI reset path.
	if err := core.ExecScript(filepath.Join(r.dataDir, "scripts", "rescue_reset.sh"), "daemon-reset", r.scriptEnv()); err != nil {
		r.logger.Printf("rollback: rescue cleanup script failed: %v", err)
	}

	// 3. Explicitly flush PRIVSTACK chains as a final safety net.
	r.flushPrivstackChains()

	r.core.SetState(core.StateStopped)
	r.logger.Println("rollback complete — proxy is fully down")
	return nil
}

// Reset clears the attempt counter, allowing fresh recovery cycles.
// Call this after the system has been stable for a while.
func (r *RescueManager) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = 0
	r.lastAttempt = time.Time{}
	r.logger.Println("attempt counter reset")
}

// SetConfig updates rescue parameters after a daemon config reload/apply.
func (r *RescueManager) SetConfig(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
	if cfg.Rescue.MaxAttempts >= 1 {
		r.maxAttempts = cfg.Rescue.MaxAttempts
	}
	if cfg.Rescue.CooldownSec > 0 {
		r.cooldown = time.Duration(cfg.Rescue.CooldownSec) * time.Second
	}
}

// Attempts returns the current attempt count.
func (r *RescueManager) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// --------------------------------------------------------------------------
// strategies
// --------------------------------------------------------------------------

// pickStrategy maps attempt number to escalating severity.
func (r *RescueManager) pickStrategy() Strategy {
	switch {
	case r.attempts == 0:
		return StrategyRestartCore
	case r.attempts == 1:
		return StrategyReapplyRules
	default:
		return StrategyFullRestart
	}
}

// restartCore kills and respawns sing-box without touching iptables.
func (r *RescueManager) restartCore(canProceed RecoveryGate) error {
	if !recoveryAllowed(canProceed) {
		return fmt.Errorf("cancelled")
	}
	profile := r.cfg.ResolveProfile()
	return r.core.HotSwap(profile)
}

// reapplyRules re-runs the iptables and DNS scripts without restarting
// sing-box.
func (r *RescueManager) reapplyRules(canProceed RecoveryGate) error {
	if !recoveryAllowed(canProceed) {
		return fmt.Errorf("cancelled")
	}
	env := r.scriptEnv()

	// Re-apply iptables.
	iptablesScript := filepath.Join(r.dataDir, "scripts", "iptables.sh")
	if err := core.ExecScript(iptablesScript, "stop", env); err != nil {
		r.logger.Printf("reapply: iptables stop failed (continuing): %v", err)
	}
	if err := core.ExecScript(iptablesScript, "start", env); err != nil {
		return fmt.Errorf("iptables start: %w", err)
	}
	if !recoveryAllowed(canProceed) {
		return fmt.Errorf("cancelled")
	}

	// Re-apply DNS.
	dnsScript := filepath.Join(r.dataDir, "scripts", "dns.sh")
	if err := core.ExecScript(dnsScript, "stop", env); err != nil {
		r.logger.Printf("reapply: dns stop failed (continuing): %v", err)
	}
	if err := core.ExecScript(dnsScript, "start", env); err != nil {
		return fmt.Errorf("dns start: %w", err)
	}

	return nil
}

// fullRestart performs a complete stop then start cycle.
func (r *RescueManager) fullRestart(canProceed RecoveryGate) error {
	if !recoveryAllowed(canProceed) {
		return fmt.Errorf("cancelled")
	}
	if err := r.core.RescueReset(); err != nil {
		r.logger.Printf("full-restart: stop failed: %v", err)
		// Continue anyway — we want a clean start.
	}
	if !recoveryAllowed(canProceed) {
		return fmt.Errorf("cancelled")
	}

	profile := r.cfg.ResolveProfile()
	return r.core.Start(profile)
}

func recoveryAllowed(canProceed RecoveryGate) bool {
	return canProceed == nil || canProceed()
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func (r *RescueManager) scriptEnv() map[string]string {
	tproxyPort := r.cfg.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	dnsPort := r.cfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	apiPort := r.cfg.Proxy.APIPort
	if apiPort == 0 {
		apiPort = 9090
	}
	gid := r.cfg.Proxy.GID
	if gid == 0 {
		gid = 23333
	}
	mark := r.cfg.Proxy.Mark
	if mark == 0 {
		mark = 0x2023
	}

	panelInbounds := r.cfg.ResolvePanelInbounds()
	appRouting := core.BuildAppRoutingEnv(
		r.cfg.Apps.Mode,
		r.cfg.Apps.Packages,
		r.cfg.Routing.AlwaysDirectApps,
	)

	return map[string]string{
		"PRIVSTACK_DIR":  r.dataDir,
		"CORE_GID":       fmt.Sprintf("%d", gid),
		"TPROXY_PORT":    fmt.Sprintf("%d", tproxyPort),
		"DNS_PORT":       fmt.Sprintf("%d", dnsPort),
		"API_PORT":       fmt.Sprintf("%d", apiPort),
		"HTTP_PORT":      fmt.Sprintf("%d", panelInbounds.HTTPPort),
		"FWMARK":         fmt.Sprintf("0x%x", mark),
		"ROUTE_TABLE":    "2023",
		"ROUTE_TABLE_V6": "2024",
		"APP_MODE":       appRouting.AppMode,
		"APP_UIDS":       appRouting.AppUIDs,
		"PROXY_UIDS":     appRouting.ProxyUIDs,
		"DIRECT_UIDS":    appRouting.DirectUIDs,
		"BYPASS_UIDS":    appRouting.BypassUIDs,
		"DNS_SCOPE":      appRouting.DNSScope,
		"DNS_MODE":       appRouting.LegacyDNSMode,
		"PROXY_MODE":     "tproxy",
	}
}

// flushPrivstackChains removes all PRIVSTACK-prefixed chains from
// iptables as a safety net during rollback.
func (r *RescueManager) flushPrivstackChains() {
	mangleChains := []string{
		"PRIVSTACK_PRE",
		"PRIVSTACK_OUT",
		"PRIVSTACK_APP",
		"PRIVSTACK_BYPASS",
		"PRIVSTACK_DIVERT",
	}
	natChains4 := []string{"PRIVSTACK_DNS", "PRIVSTACK_DNS_NAT"}
	natChains6 := []string{"PRIVSTACK_DNS", "PRIVSTACK_DNS_NAT6"}

	for _, chain := range mangleChains {
		flushChain(core.ExecIptables, "mangle", chain)
		flushChain(core.ExecIp6tables, "mangle", chain)
	}
	for _, chain := range natChains4 {
		flushChain(core.ExecIptables, "nat", chain)
	}
	for _, chain := range natChains6 {
		flushChain(core.ExecIp6tables, "nat", chain)
	}
}

func flushChain(execFn func(...string) error, table string, chain string) {
	for _, parent := range []string{"PREROUTING", "OUTPUT", "INPUT", "FORWARD", "POSTROUTING"} {
		for {
			if err := execFn("-t", table, "-D", parent, "-j", chain); err != nil {
				break
			}
		}
	}
	_ = execFn("-t", table, "-F", chain)
	_ = execFn("-t", table, "-X", chain)
}
