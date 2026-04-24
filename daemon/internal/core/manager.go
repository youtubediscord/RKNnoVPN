// Package core manages the sing-box process lifecycle — start, stop,
// hot-swap, and status reporting.
package core

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

// State represents the daemon's proxy-core lifecycle phase.
type State int

const (
	StateStopped  State = iota // sing-box is not running
	StateStarting              // spawn in progress, waiting for port
	StateRunning               // sing-box is listening and iptables are applied
	StateDegraded              // health checks are failing but process is alive
	StateRescue                // automatic recovery in progress
	StateStopping              // teardown in progress
)

// RuntimeError carries a stable startup/runtime code across package
// boundaries so the control plane can show the failing stage.
type RuntimeError struct {
	Layer string
	Code  string
	Hard  bool
	Err   error
}

func (e *RuntimeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Layer != "" {
		return fmt.Sprintf("core: %s: %v", e.Layer, e.Err)
	}
	return fmt.Sprintf("core: %v", e.Err)
}

func (e *RuntimeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *RuntimeError) RuntimeCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func runtimeError(layer string, code string, err error) error {
	if err == nil {
		return nil
	}
	return &RuntimeError{
		Layer: layer,
		Code:  code,
		Hard:  true,
		Err:   err,
	}
}

// String returns a human-readable label for the state.
func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateDegraded:
		return "degraded"
	case StateRescue:
		return "rescue"
	case StateStopping:
		return "stopping"
	default:
		return "unknown"
	}
}

// StatusInfo is the snapshot returned by Status().
type StatusInfo struct {
	State         string    `json:"state"`
	PID           int       `json:"pid"`
	Uptime        string    `json:"uptime"`
	ActiveProfile string    `json:"active_profile"`
	StartedAt     time.Time `json:"started_at"`
}

// CoreManager owns the sing-box child process and the iptables / DNS
// rules that make transparent proxying work.
type CoreManager struct {
	config  *config.Config
	process *os.Process
	pid     int
	state   State
	dataDir string
	logger  *log.Logger

	activeProfile string
	startedAt     time.Time

	mu sync.Mutex
}

// NewCoreManager creates a CoreManager that stores runtime data under dataDir
// (normally /data/adb/privstack).
func NewCoreManager(cfg *config.Config, dataDir string, logger *log.Logger) *CoreManager {
	if logger == nil {
		logger = log.New(os.Stderr, "[core] ", log.LstdFlags)
	}
	return &CoreManager{
		config:  cfg,
		dataDir: dataDir,
		logger:  logger,
		state:   StateStopped,
	}
}

// SetConfig replaces the live configuration. This does NOT restart
// sing-box — call HotSwap to apply changes.
func (m *CoreManager) SetConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = cfg
}

// State returns the current lifecycle state (safe for concurrent access).
func (m *CoreManager) GetState() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// SetState is used by the health/rescue subsystems to mark degraded or
// rescue states externally.
func (m *CoreManager) SetState(s State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = s
}

// ResetState forgets any tracked sing-box process metadata after an external
// cleanup path already tore the runtime down.
func (m *CoreManager) ResetState() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.process = nil
	m.pid = 0
	m.activeProfile = ""
	m.startedAt = time.Time{}
	m.state = StateStopped
	_ = os.Remove(filepath.Join(m.dataDir, "run", "singbox.pid"))
	_ = os.Remove(filepath.Join(m.dataDir, "run", "active"))
}

// --------------------------------------------------------------------------
// Start
// --------------------------------------------------------------------------

// Start renders a sing-box configuration from the given profile, spawns the
// sing-box process, waits for its tproxy port to be listening, and applies
// iptables + DNS interception rules.
func (m *CoreManager) Start(profile *config.NodeProfile) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == StateRunning || m.state == StateStarting {
		return fmt.Errorf("core: already %s", m.state)
	}
	m.state = StateStarting
	m.logger.Printf("starting sing-box for profile %q", profile.Protocol)

	// 1. Render the sing-box JSON config.
	configPath := filepath.Join(m.dataDir, "config", "rendered", "singbox.json")
	if err := renderConfig(m.config, profile, configPath); err != nil {
		m.state = StateStopped
		return runtimeError("render config", "CONFIG_RENDER_FAILED", err)
	}
	if err := m.checkSingBoxConfig(configPath); err != nil {
		m.state = StateStopped
		return runtimeError("config check", "CONFIG_CHECK_FAILED", err)
	}

	// 2. Spawn sing-box.
	binPath := filepath.Join(m.dataDir, "bin", "sing-box")
	cmd := exec.Command(binPath, "run", "-c", configPath)
	logFile, logPath, err := m.openSingBoxLog()
	if err != nil {
		m.state = StateStopped
		return runtimeError("open sing-box log", "CORE_LOG_OPEN_FAILED", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Gid: m.coreGID(),
		},
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		m.state = StateStopped
		return runtimeError("spawn sing-box", "CORE_SPAWN_FAILED", err)
	}
	logFile.Close()
	m.process = cmd.Process
	m.pid = cmd.Process.Pid

	// Write PID file.
	pidPath := filepath.Join(m.dataDir, "run", "singbox.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(m.pid)), 0640)

	m.logger.Printf("sing-box spawned, pid=%d", m.pid)

	// 3. Start a goroutine to reap the child so we don't leak zombies.
	exitCh := make(chan error, 1)
	go func() {
		exitCh <- cmd.Wait()
	}()

	// 4. Wait for tproxy port.
	tproxyPort := m.config.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	if err := m.waitForPortOrExit(tproxyPort, 30*time.Second, exitCh, logPath); err != nil {
		m.logger.Printf("port %d did not open in time, killing pid %d", tproxyPort, m.pid)
		_ = m.process.Signal(syscall.SIGKILL)
		m.process = nil
		m.pid = 0
		m.activeProfile = ""
		m.startedAt = time.Time{}
		m.state = StateStopped
		_ = os.Remove(pidPath)
		return runtimeError("wait tproxy port", "TPROXY_PORT_DOWN", fmt.Errorf("port %d not ready: %w", tproxyPort, err))
	}
	m.logger.Printf("port %d is listening", tproxyPort)

	// 5. Apply iptables rules.
	iptablesScript := filepath.Join(m.dataDir, "scripts", "iptables.sh")
	if err := ExecScript(iptablesScript, "start", m.scriptEnv()); err != nil {
		m.logger.Printf("iptables apply failed: %v — rolling back", err)
		_ = m.process.Signal(syscall.SIGTERM)
		m.process = nil
		m.pid = 0
		m.activeProfile = ""
		m.startedAt = time.Time{}
		m.state = StateStopped
		_ = os.Remove(pidPath)
		return runtimeError("iptables start", "RULES_NOT_APPLIED", err)
	}
	m.logger.Println("iptables rules applied")

	// 6. Apply DNS interception.
	dnsScript := filepath.Join(m.dataDir, "scripts", "dns.sh")
	if err := ExecScript(dnsScript, "start", m.scriptEnv()); err != nil {
		m.logger.Printf("DNS apply failed: %v — rolling back iptables", err)
		_ = ExecScript(iptablesScript, "stop", m.scriptEnv())
		_ = m.process.Signal(syscall.SIGTERM)
		m.process = nil
		m.pid = 0
		m.activeProfile = ""
		m.startedAt = time.Time{}
		m.state = StateStopped
		_ = os.Remove(pidPath)
		return runtimeError("dns start", "DNS_APPLY_FAILED", err)
	}
	m.logger.Println("DNS interception applied")

	// 7. Mark running.
	m.activeProfile = profile.Protocol + "://" + profile.Address
	m.startedAt = time.Now()
	m.state = StateRunning
	m.markActive()
	_ = os.Remove(filepath.Join(m.dataDir, "run", "reset.lock"))
	_ = os.Remove(filepath.Join(m.dataDir, "config", "manual"))
	m.logger.Printf("core is running (pid=%d)", m.pid)
	return nil
}

// --------------------------------------------------------------------------
// Stop
// --------------------------------------------------------------------------

// Stop tears down networking rules and terminates the sing-box process.
// Order matters: DNS first, then iptables, then kill — so traffic is never
// black-holed through a dead proxy.
func (m *CoreManager) Stop() error {
	return m.stopWithMode(true)
}

// RescueReset tears down PrivStack-owned runtime state even if the in-memory
// lifecycle already says stopped. Use this for recovery paths where kernel
// rules can outlive daemon state.
func (m *CoreManager) RescueReset() error {
	return m.stopWithMode(true)
}

func (m *CoreManager) stopWithMode(forceCleanup bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == StateStopped && !forceCleanup {
		return nil
	}
	m.state = StateStopping
	m.logger.Println("stopping core")
	_ = os.Remove(filepath.Join(m.dataDir, "run", "active"))

	var firstErr error

	// 1. Remove DNS interception (before iptables so nothing routes to
	//    a listener that may already be partly torn down).
	dnsScript := filepath.Join(m.dataDir, "scripts", "dns.sh")
	if err := ExecScript(dnsScript, "stop", m.scriptEnv()); err != nil {
		m.logger.Printf("dns stop: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	// 2. Remove iptables BEFORE killing sing-box so that inflight
	//    connections are not TPROXY'd into a dead socket.
	iptablesScript := filepath.Join(m.dataDir, "scripts", "iptables.sh")
	if err := ExecScript(iptablesScript, "stop", m.scriptEnv()); err != nil {
		m.logger.Printf("iptables stop: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	// 3. SIGTERM → wait 5 s → SIGKILL.
	if m.process != nil {
		if err := m.killProcess(); err != nil && firstErr == nil {
			firstErr = err
		}
	} else if err := m.killTrackedSingBox(); err != nil && firstErr == nil {
		firstErr = err
	}

	// 4. Clean PID file.
	pidPath := filepath.Join(m.dataDir, "run", "singbox.pid")
	_ = os.Remove(pidPath)

	m.process = nil
	m.pid = 0
	m.activeProfile = ""
	m.startedAt = time.Time{}
	m.state = StateStopped
	m.logger.Println("core stopped")
	return firstErr
}

// --------------------------------------------------------------------------
// HotSwap
// --------------------------------------------------------------------------

// HotSwap replaces the running sing-box config without touching iptables.
// This allows seamless node switching — the tproxy port stays the same,
// so all kernel routing rules remain valid.
func (m *CoreManager) HotSwap(profile *config.NodeProfile) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Printf("hot-swap to profile %q", profile.Protocol)

	// 1. Render new config.
	configPath := filepath.Join(m.dataDir, "config", "rendered", "singbox.json")
	if err := renderConfig(m.config, profile, configPath); err != nil {
		return fmt.Errorf("core: hot-swap render: %w", err)
	}
	if err := m.checkSingBoxConfig(configPath); err != nil {
		return fmt.Errorf("core: hot-swap config check: %w", err)
	}

	// 2. Stop sing-box (SIGTERM only, no iptables teardown).
	if m.process != nil {
		if err := m.killProcess(); err != nil {
			return fmt.Errorf("core: hot-swap kill old: %w", err)
		}
	}
	m.state = StateStarting

	// 3. Spawn new sing-box with the fresh config.
	binPath := filepath.Join(m.dataDir, "bin", "sing-box")
	cmd := exec.Command(binPath, "run", "-c", configPath)
	logFile, logPath, err := m.openSingBoxLog()
	if err != nil {
		m.state = StateDegraded
		return fmt.Errorf("core: hot-swap open sing-box log: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Gid: m.coreGID(),
		},
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		m.state = StateDegraded
		return fmt.Errorf("core: hot-swap spawn: %w", err)
	}
	logFile.Close()
	m.process = cmd.Process
	m.pid = cmd.Process.Pid

	pidPath := filepath.Join(m.dataDir, "run", "singbox.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(m.pid)), 0640)

	exitCh := make(chan error, 1)
	go func() {
		exitCh <- cmd.Wait()
	}()

	// 4. Wait for port.
	tproxyPort := m.config.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	if err := m.waitForPortOrExit(tproxyPort, 30*time.Second, exitCh, logPath); err != nil {
		_ = m.process.Signal(syscall.SIGKILL)
		m.process = nil
		m.pid = 0
		m.activeProfile = ""
		m.startedAt = time.Time{}
		_ = os.Remove(pidPath)
		m.state = StateDegraded
		return fmt.Errorf("core: hot-swap port wait: %w", err)
	}

	// 5. iptables left untouched — they still point at the same tproxy port.
	m.activeProfile = profile.Protocol + "://" + profile.Address
	m.startedAt = time.Now()
	m.state = StateRunning
	m.markActive()
	_ = os.Remove(filepath.Join(m.dataDir, "run", "reset.lock"))
	_ = os.Remove(filepath.Join(m.dataDir, "config", "manual"))
	m.logger.Printf("hot-swap complete (pid=%d)", m.pid)
	return nil
}

func (m *CoreManager) markActive() {
	runDir := filepath.Join(m.dataDir, "run")
	if err := os.MkdirAll(runDir, 0750); err != nil {
		m.logger.Printf("mark active: mkdir %s: %v", runDir, err)
		return
	}
	activePath := filepath.Join(runDir, "active")
	content := []byte(time.Now().Format(time.RFC3339) + "\n")
	if err := os.WriteFile(activePath, content, 0640); err != nil {
		m.logger.Printf("mark active: write %s: %v", activePath, err)
	}
}

func (m *CoreManager) openSingBoxLog() (*os.File, string, error) {
	logDir := filepath.Join(m.dataDir, "logs")
	if err := os.MkdirAll(logDir, 0750); err != nil {
		return nil, "", err
	}
	logPath := filepath.Join(logDir, "sing-box.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, "", err
	}
	_, _ = fmt.Fprintf(file, "\n--- sing-box start %s ---\n", time.Now().Format(time.RFC3339))
	return file, logPath, nil
}

func (m *CoreManager) checkSingBoxConfig(configPath string) error {
	binPath := filepath.Join(m.dataDir, "bin", "sing-box")
	out, err := ExecCommand(binPath, "check", "-c", configPath)
	if err != nil {
		if out != "" {
			return fmt.Errorf("sing-box check failed: %w; output: %s", err, out)
		}
		return fmt.Errorf("sing-box check failed: %w", err)
	}
	return nil
}

func (m *CoreManager) waitForPortOrExit(port int, timeout time.Duration, exitCh <-chan error, logPath string) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		select {
		case exitErr := <-exitCh:
			tail := tailFile(logPath, 4096)
			if tail != "" {
				return fmt.Errorf("sing-box exited before listening on %s: %v; log tail: %s", addr, exitErr, tail)
			}
			return fmt.Errorf("sing-box exited before listening on %s: %v", addr, exitErr)
		case <-ticker.C:
			if time.Now().After(deadline) {
				tail := tailFile(logPath, 4096)
				if tail != "" {
					return fmt.Errorf("port %s not listening after %s; sing-box log tail: %s", addr, timeout, tail)
				}
				return fmt.Errorf("port %s not listening after %s", addr, timeout)
			}
		}
	}
}

func tailFile(path string, maxBytes int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	return strings.TrimSpace(string(data))
}

// --------------------------------------------------------------------------
// Status
// --------------------------------------------------------------------------

// Status returns a snapshot of the core state.
func (m *CoreManager) Status() *StatusInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	info := &StatusInfo{
		State:         m.state.String(),
		PID:           m.pid,
		ActiveProfile: m.activeProfile,
		StartedAt:     m.startedAt,
	}
	if !m.startedAt.IsZero() {
		info.Uptime = time.Since(m.startedAt).Truncate(time.Second).String()
	}
	return info
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// killProcess sends SIGTERM, waits up to 5 s, then SIGKILL.
func (m *CoreManager) killProcess() error {
	if m.process == nil {
		return nil
	}

	waitCh := make(chan error, 1)
	go func(proc *os.Process) {
		_, err := proc.Wait()
		waitCh <- err
	}(m.process)

	m.logger.Printf("sending SIGTERM to pid %d", m.pid)
	if err := m.process.Signal(syscall.SIGTERM); err != nil {
		// Process may have already exited — not fatal.
		m.logger.Printf("SIGTERM failed (may be already dead): %v", err)
		select {
		case <-waitCh:
		default:
		}
		return nil
	}

	select {
	case err := <-waitCh:
		if err != nil {
			m.logger.Printf("wait after SIGTERM returned: %v", err)
		} else {
			m.logger.Printf("pid %d exited after SIGTERM", m.pid)
		}
		return nil
	case <-time.After(5 * time.Second):
	}

	// Still alive — escalate.
	m.logger.Printf("pid %d did not exit, sending SIGKILL", m.pid)
	if err := m.process.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("SIGKILL pid %d: %w", m.pid, err)
	}
	select {
	case err := <-waitCh:
		if err != nil {
			m.logger.Printf("wait after SIGKILL returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		return fmt.Errorf("pid %d did not exit after SIGKILL", m.pid)
	}
	return nil
}

func (m *CoreManager) killTrackedSingBox() error {
	pids := make(map[int]bool)
	if m.pid > 0 {
		pids[m.pid] = true
	}
	if raw, err := os.ReadFile(filepath.Join(m.dataDir, "run", "singbox.pid")); err == nil {
		if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil && pid > 0 {
			pids[pid] = true
		}
	}

	var errs []string
	for pid := range pids {
		if pid == os.Getpid() {
			continue
		}
		if !m.pidLooksLikeSingBox(pid) {
			m.logger.Printf("skipping stale sing-box pid %d: cmdline does not match", pid)
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		m.logger.Printf("sending SIGTERM to tracked sing-box pid %d", pid)
		_ = proc.Signal(syscall.SIGTERM)
		time.Sleep(500 * time.Millisecond)
		if err := proc.Signal(syscall.Signal(0)); err == nil {
			m.logger.Printf("tracked sing-box pid %d still alive, sending SIGKILL", pid)
			if killErr := proc.Signal(syscall.SIGKILL); killErr != nil {
				errs = append(errs, fmt.Sprintf("SIGKILL pid %d: %v", pid, killErr))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *CoreManager) pidLooksLikeSingBox(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	cmdline := strings.ReplaceAll(string(data), "\x00", " ")
	return strings.Contains(cmdline, "sing-box")
}

func (m *CoreManager) coreGID() uint32 {
	gid := m.config.Proxy.GID
	if gid == 0 {
		gid = 23333
	}
	return uint32(gid)
}

// scriptEnv returns the environment variables that shell scripts expect.
// These must match the validate_env() check in iptables.sh.
func (m *CoreManager) scriptEnv() map[string]string {
	tproxyPort := m.config.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	dnsPort := m.config.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	apiPort := m.config.Proxy.APIPort
	gid := m.config.Proxy.GID
	if gid == 0 {
		gid = 23333
	}
	mark := m.config.Proxy.Mark
	if mark == 0 {
		mark = 0x2023
	}

	panelInbounds := m.config.ResolvePanelInbounds()
	appRouting := BuildAppRoutingEnv(
		m.config.Apps.Mode,
		m.config.Apps.Packages,
		m.config.Routing.AlwaysDirectApps,
	)

	return map[string]string{
		"PRIVSTACK_DIR":  m.dataDir,
		"CORE_GID":       strconv.Itoa(gid),
		"TPROXY_PORT":    strconv.Itoa(tproxyPort),
		"DNS_PORT":       strconv.Itoa(dnsPort),
		"API_PORT":       strconv.Itoa(apiPort),
		"HTTP_PORT":      strconv.Itoa(panelInbounds.HTTPPort),
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

// renderConfig uses the config package's RenderSingboxConfig to produce
// a complete sing-box JSON configuration and writes it to path.
func renderConfig(cfg *config.Config, profile *config.NodeProfile, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	data, err := config.RenderSingboxConfig(cfg, profile)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	data = append(data, '\n')

	return os.WriteFile(path, data, 0600)
}
