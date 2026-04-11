// Package core manages the sing-box process lifecycle — start, stop,
// hot-swap, and status reporting.
package core

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/privstack/daemon/internal/config"
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
		return fmt.Errorf("core: render config: %w", err)
	}

	// 2. Spawn sing-box.
	binPath := filepath.Join(m.dataDir, "bin", "sing-box")
	cmd := exec.Command(binPath, "run", "-c", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Gid: 23333,
		},
	}

	if err := cmd.Start(); err != nil {
		m.state = StateStopped
		return fmt.Errorf("core: spawn sing-box: %w", err)
	}
	m.process = cmd.Process
	m.pid = cmd.Process.Pid

	// Write PID file.
	pidPath := filepath.Join(m.dataDir, "run", "singbox.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(m.pid)), 0640)

	m.logger.Printf("sing-box spawned, pid=%d", m.pid)

	// 3. Start a goroutine to reap the child so we don't leak zombies.
	go func() {
		_ = cmd.Wait()
	}()

	// 4. Wait for tproxy port.
	tproxyPort := m.config.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10808
	}
	if err := WaitForPort("127.0.0.1", tproxyPort, 30*time.Second); err != nil {
		m.logger.Printf("port %d did not open in time, killing pid %d", tproxyPort, m.pid)
		_ = m.process.Signal(syscall.SIGKILL)
		m.process = nil
		m.pid = 0
		m.state = StateStopped
		return fmt.Errorf("core: port %d not ready: %w", tproxyPort, err)
	}
	m.logger.Printf("port %d is listening", tproxyPort)

	// 5. Apply iptables rules.
	iptablesScript := filepath.Join(m.dataDir, "scripts", "iptables.sh")
	if err := ExecScript(iptablesScript, "start", m.scriptEnv()); err != nil {
		m.logger.Printf("iptables apply failed: %v — rolling back", err)
		_ = m.process.Signal(syscall.SIGTERM)
		m.process = nil
		m.pid = 0
		m.state = StateStopped
		return fmt.Errorf("core: iptables start: %w", err)
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
		m.state = StateStopped
		return fmt.Errorf("core: dns start: %w", err)
	}
	m.logger.Println("DNS interception applied")

	// 7. Mark running.
	m.activeProfile = profile.Protocol + "://" + profile.Address
	m.startedAt = time.Now()
	m.state = StateRunning
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
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == StateStopped {
		return nil
	}
	m.state = StateStopping
	m.logger.Println("stopping core")

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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Gid: 23333,
		},
	}

	if err := cmd.Start(); err != nil {
		m.state = StateDegraded
		return fmt.Errorf("core: hot-swap spawn: %w", err)
	}
	m.process = cmd.Process
	m.pid = cmd.Process.Pid

	pidPath := filepath.Join(m.dataDir, "run", "singbox.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(m.pid)), 0640)

	go func() {
		_ = cmd.Wait()
	}()

	// 4. Wait for port.
	tproxyPort := m.config.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10808
	}
	if err := WaitForPort("127.0.0.1", tproxyPort, 30*time.Second); err != nil {
		_ = m.process.Signal(syscall.SIGKILL)
		m.state = StateDegraded
		return fmt.Errorf("core: hot-swap port wait: %w", err)
	}

	// 5. iptables left untouched — they still point at the same tproxy port.
	m.activeProfile = profile.Protocol + "://" + profile.Address
	m.startedAt = time.Now()
	m.state = StateRunning
	m.logger.Printf("hot-swap complete (pid=%d)", m.pid)
	return nil
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

	m.logger.Printf("sending SIGTERM to pid %d", m.pid)
	if err := m.process.Signal(syscall.SIGTERM); err != nil {
		// Process may have already exited — not fatal.
		m.logger.Printf("SIGTERM failed (may be already dead): %v", err)
		return nil
	}

	// Poll for exit over 5 seconds.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := m.process.Signal(syscall.Signal(0)); err != nil {
			// kill -0 failed ⇒ process is gone.
			m.logger.Printf("pid %d exited after SIGTERM", m.pid)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Still alive — escalate.
	m.logger.Printf("pid %d did not exit, sending SIGKILL", m.pid)
	if err := m.process.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("SIGKILL pid %d: %w", m.pid, err)
	}
	return nil
}

// scriptEnv returns the environment variables that shell scripts expect.
func (m *CoreManager) scriptEnv() map[string]string {
	dnsPort := m.config.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10853
	}
	return map[string]string{
		"PRIVSTACK_DIR": m.dataDir,
		"CORE_GID":      "23333",
		"TPROXY_PORT":   strconv.Itoa(m.config.Proxy.TProxyPort),
		"DNS_PORT":      strconv.Itoa(dnsPort),
		"FWMARK":        "0x2023",
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

	return os.WriteFile(path, data, 0640)
}
