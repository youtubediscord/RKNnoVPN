// Package core manages the sing-box process lifecycle — start, stop,
// hot-swap, and status reporting.
package core

import (
	"context"
	"errors"
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
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
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
	Layer           string
	Code            string
	Hard            bool
	UserMessage     string
	Debug           string
	RollbackApplied bool
	StageReport     RuntimeStageReport
	Err             error
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

func (e *RuntimeError) RuntimeUserMessage() string {
	if e == nil {
		return ""
	}
	return e.UserMessage
}

func (e *RuntimeError) RuntimeDebug() string {
	if e == nil {
		return ""
	}
	return e.Debug
}

func (e *RuntimeError) RuntimeRollbackApplied() bool {
	if e == nil {
		return false
	}
	return e.RollbackApplied
}

func (e *RuntimeError) RuntimeStageReport() interface{} {
	if e == nil {
		return nil
	}
	return e.StageReport
}

func runtimeError(layer string, code string, err error) error {
	return runtimeErrorWithRollback(layer, code, err, false)
}

func runtimeErrorWithRollback(layer string, code string, err error, rollbackApplied bool) error {
	return runtimeErrorWithReport(layer, code, err, rollbackApplied, RuntimeStageReport{})
}

func runtimeErrorWithReport(layer string, code string, err error, rollbackApplied bool, report RuntimeStageReport) error {
	if err == nil {
		return nil
	}
	return &RuntimeError{
		Layer:           layer,
		Code:            code,
		Hard:            true,
		UserMessage:     runtimeUserMessage(code),
		Debug:           err.Error(),
		RollbackApplied: rollbackApplied,
		StageReport:     report,
		Err:             err,
	}
}

func runtimeUserMessage(code string) string {
	switch code {
	case "CONFIG_RENDER_FAILED":
		return "Generated sing-box config could not be rendered."
	case "CONFIG_CHECK_FAILED":
		return "Generated sing-box config did not pass sing-box check."
	case "CORE_LOG_OPEN_FAILED":
		return "sing-box log file could not be opened."
	case "CORE_SPAWN_FAILED":
		return "sing-box process could not be started."
	case "CORE_STOP_FAILED":
		return "previous sing-box process could not be stopped."
	case "TPROXY_PORT_DOWN":
		return "sing-box did not open the TPROXY listener."
	case "DNS_LISTENER_DOWN":
		return "sing-box did not open the local DNS listener."
	case "API_PORT_DOWN":
		return "sing-box did not open the local API listener."
	case "RULES_NOT_APPLIED":
		return "PrivStack routing rules could not be applied."
	case "DNS_APPLY_FAILED":
		return "PrivStack DNS interception could not be applied."
	case "NETSTACK_VERIFY_FAILED":
		return "PrivStack network rules were applied but did not pass verification."
	case "NETSTACK_CLEANUP_FAILED":
		return "Previous PrivStack network rules could not be cleaned up."
	default:
		return "Runtime stage failed."
	}
}

func newRuntimeStageReport(operation string) RuntimeStageReport {
	return NewRuntimeStageReport(operation)
}

func NewRuntimeStageReport(operation string) RuntimeStageReport {
	now := time.Now()
	return RuntimeStageReport{
		Operation: operation,
		Status:    "running",
		StartedAt: now,
	}
}

func (r *RuntimeStageReport) addStage(name string, status string, code string, detail string, rollbackApplied bool) {
	r.AddStage(name, status, code, detail, rollbackApplied)
}

func (r *RuntimeStageReport) AddStage(name string, status string, code string, detail string, rollbackApplied bool) {
	if r == nil {
		return
	}
	if status == "" {
		status = "ok"
	}
	stage := RuntimeStage{
		Name:            name,
		Status:          status,
		Code:            code,
		Detail:          detail,
		RollbackApplied: rollbackApplied,
		At:              time.Now(),
	}
	r.Stages = append(r.Stages, stage)
	if status == "failed" {
		r.Status = "failed"
		r.FailedStage = name
		r.LastCode = code
		r.RollbackApplied = rollbackApplied
		r.FinishedAt = stage.At
	}
}

func (r *RuntimeStageReport) finishOK() {
	r.FinishOK()
}

func (r *RuntimeStageReport) FinishOK() {
	if r == nil {
		return
	}
	r.Status = "ok"
	r.FinishedAt = time.Now()
}

func (r RuntimeStageReport) empty() bool {
	return r.Empty()
}

func (r RuntimeStageReport) Empty() bool {
	return r.Operation == "" && len(r.Stages) == 0
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
	State         string             `json:"state"`
	PID           int                `json:"pid"`
	Uptime        string             `json:"uptime"`
	ActiveProfile string             `json:"active_profile"`
	StartedAt     time.Time          `json:"started_at"`
	StartReport   RuntimeStageReport `json:"start_report,omitempty"`
	RuntimeReport RuntimeStageReport `json:"runtime_report,omitempty"`
}

type RuntimeStageReport struct {
	Operation       string         `json:"operation,omitempty"`
	Status          string         `json:"status,omitempty"`
	Stages          []RuntimeStage `json:"stages,omitempty"`
	FailedStage     string         `json:"failedStage,omitempty"`
	LastCode        string         `json:"lastCode,omitempty"`
	RollbackApplied bool           `json:"rollbackApplied,omitempty"`
	StartedAt       time.Time      `json:"startedAt,omitempty"`
	FinishedAt      time.Time      `json:"finishedAt,omitempty"`
}

type RuntimeStage struct {
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	Code            string    `json:"code,omitempty"`
	Detail          string    `json:"detail,omitempty"`
	RollbackApplied bool      `json:"rollbackApplied,omitempty"`
	At              time.Time `json:"at,omitempty"`
}

type listenerWaitSpec struct {
	Stage   string
	Layer   string
	Code    string
	Label   string
	Port    int
	Timeout time.Duration
}

const singBoxCheckTimeout = 20 * time.Second

// CoreManager owns the sing-box child process and the iptables / DNS
// rules that make transparent proxying work.
type CoreManager struct {
	config  *config.Config
	process *os.Process
	exitCh  <-chan error
	pid     int
	state   State
	dataDir string
	logger  *log.Logger

	activeProfile     string
	startedAt         time.Time
	lastStartReport   RuntimeStageReport
	lastRuntimeReport RuntimeStageReport

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
	m.exitCh = nil
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
	stageReport := newRuntimeStageReport("start")
	m.lastStartReport = stageReport
	recordStage := func(name string, status string, code string, detail string, rollbackApplied bool) {
		stageReport.addStage(name, status, code, detail, rollbackApplied)
		m.lastStartReport = stageReport
		m.lastRuntimeReport = stageReport
	}
	failStage := func(name string, layer string, code string, err error, rollbackApplied bool) error {
		recordStage(name, "failed", code, err.Error(), rollbackApplied)
		return runtimeErrorWithReport(layer, code, err, rollbackApplied, stageReport)
	}

	m.state = StateStarting
	m.logger.Printf("starting sing-box for profile %q", profile.Protocol)

	// 1. Render the sing-box JSON config.
	configPath := filepath.Join(m.dataDir, "config", "rendered", "singbox.json")
	m.logger.Printf("rendering sing-box config to %s", configPath)
	if err := renderConfig(m.config, profile, configPath); err != nil {
		m.logger.Printf("render sing-box config failed: %v", err)
		m.state = StateStopped
		return failStage("render-config", "render config", "CONFIG_RENDER_FAILED", err, false)
	}
	recordStage("render-config", "ok", "", configPath, false)
	m.logger.Printf("checking sing-box config %s", configPath)
	if err := m.checkSingBoxConfig(configPath); err != nil {
		m.logger.Printf("sing-box config check failed: %v", err)
		m.state = StateStopped
		return failStage("config-check", "config check", "CONFIG_CHECK_FAILED", err, false)
	}
	m.logger.Printf("sing-box config check passed")
	recordStage("config-check", "ok", "", configPath, false)

	// 2. Spawn sing-box.
	binPath := filepath.Join(m.dataDir, "bin", "sing-box")
	cmd := exec.Command(binPath, "run", "-c", configPath)
	logFile, logPath, err := m.openSingBoxLog()
	if err != nil {
		m.state = StateStopped
		return failStage("open-core-log", "open sing-box log", "CORE_LOG_OPEN_FAILED", err, false)
	}
	recordStage("open-core-log", "ok", "", logPath, false)
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
		return failStage("spawn-core", "spawn sing-box", "CORE_SPAWN_FAILED", err, false)
	}
	logFile.Close()
	m.process = cmd.Process
	m.pid = cmd.Process.Pid
	exitCh := watchCommand(cmd)
	m.exitCh = exitCh
	recordStage("spawn-core", "ok", "", fmt.Sprintf("pid=%d", m.pid), false)

	// Write PID file.
	pidPath := filepath.Join(m.dataDir, "run", "singbox.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(m.pid)), 0640)
	rollbackStarted := func(signal syscall.Signal, stopDNS bool, stopRules bool) {
		if stopDNS || stopRules {
			_ = m.netstack().Cleanup().Err()
		}
		if m.process != nil {
			_ = m.process.Signal(signal)
			select {
			case <-m.exitCh:
			case <-time.After(2 * time.Second):
			}
		}
		m.process = nil
		m.exitCh = nil
		m.pid = 0
		m.activeProfile = ""
		m.startedAt = time.Time{}
		m.state = StateStopped
		_ = os.Remove(pidPath)
	}

	m.logger.Printf("sing-box spawned, pid=%d", m.pid)

	// 4. Wait for runtime listeners.
	if spec, err := m.waitRuntimeListeners(exitCh, logPath, recordStage); err != nil {
		m.logger.Printf("%s port %d did not open in time, killing pid %d", spec.Label, spec.Port, m.pid)
		rollbackStarted(syscall.SIGKILL, false, false)
		return failStage(spec.Stage, spec.Layer, spec.Code, fmt.Errorf("%s port %d not ready: %w", spec.Label, spec.Port, err), true)
	}

	// 5-6. Apply PrivStack-owned iptables and DNS interception.
	netReport := m.netstack().Apply()
	if err := netReport.Err(); err != nil {
		code := "RULES_NOT_APPLIED"
		var netErr *netstack.Error
		if errors.As(err, &netErr) && netErr.Code != "" {
			code = netErr.Code
		}
		m.logger.Printf("netstack apply failed: %v — rolling back", err)
		rollbackStarted(syscall.SIGTERM, false, false)
		return failStage("netstack-apply", "netstack apply", code, err, true)
	}
	m.logger.Println("netstack applied")
	recordStage("netstack-apply", "ok", "", fmt.Sprintf("steps=%d", len(netReport.Steps)), false)
	netVerifyReport := m.netstack().Verify()
	if err := netVerifyReport.Err(); err != nil {
		code := "NETSTACK_VERIFY_FAILED"
		var netErr *netstack.Error
		if errors.As(err, &netErr) && netErr.Code != "" {
			code = netErr.Code
		}
		m.logger.Printf("netstack verify failed: %v — rolling back", err)
		rollbackStarted(syscall.SIGTERM, false, false)
		return failStage("netstack-verify", "netstack verify", code, err, true)
	}
	m.logger.Println("netstack verified")
	recordStage("netstack-verify", "ok", "", fmt.Sprintf("steps=%d", len(netVerifyReport.Steps)), false)

	// 7. Mark running.
	m.activeProfile = profile.Protocol + "://" + profile.Address
	m.startedAt = time.Now()
	m.state = StateRunning
	m.markActive()
	_ = os.Remove(filepath.Join(m.dataDir, "config", "manual"))
	m.logger.Printf("core is running (pid=%d)", m.pid)
	recordStage("commit-state", "ok", "", m.activeProfile, false)
	m.finishStartReport(stageReport)
	return nil
}

func (m *CoreManager) finishStartReport(report RuntimeStageReport) {
	report.finishOK()
	m.lastStartReport = report
	m.lastRuntimeReport = report
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

	// 1-2. Remove DNS interception and iptables BEFORE killing sing-box so
	// inflight connections are not TPROXY'd into a dead socket.
	if err := m.netstack().Cleanup().Err(); err != nil {
		m.logger.Printf("netstack cleanup: %v", err)
		firstErr = err
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
	m.exitCh = nil
	m.pid = 0
	m.activeProfile = ""
	m.startedAt = time.Time{}
	m.state = StateStopped
	m.logger.Println("core stopped")
	return firstErr
}

func ignorableCleanupScriptError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "script not found:") ||
		strings.Contains(lower, "no such file or directory")
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

	stageReport := newRuntimeStageReport("hot-swap")
	m.lastRuntimeReport = stageReport
	recordStage := func(name string, status string, code string, detail string, rollbackApplied bool) {
		stageReport.addStage(name, status, code, detail, rollbackApplied)
		m.lastRuntimeReport = stageReport
	}
	failStage := func(name string, layer string, code string, err error, rollbackApplied bool) error {
		recordStage(name, "failed", code, err.Error(), rollbackApplied)
		return runtimeErrorWithReport(layer, code, err, rollbackApplied, stageReport)
	}

	m.logger.Printf("hot-swap to profile %q", profile.Protocol)

	// 1. Render new config.
	configPath := filepath.Join(m.dataDir, "config", "rendered", "singbox.json")
	if err := renderConfig(m.config, profile, configPath); err != nil {
		return failStage("render-config", "hot-swap render", "CONFIG_RENDER_FAILED", err, false)
	}
	recordStage("render-config", "ok", "", configPath, false)
	if err := m.checkSingBoxConfig(configPath); err != nil {
		return failStage("config-check", "hot-swap config check", "CONFIG_CHECK_FAILED", err, false)
	}
	recordStage("config-check", "ok", "", configPath, false)

	// 2. Stop sing-box (SIGTERM only, no iptables teardown).
	if m.process != nil {
		if err := m.killProcess(); err != nil {
			return failStage("stop-old-core", "hot-swap kill old", "CORE_STOP_FAILED", err, false)
		}
		recordStage("stop-old-core", "ok", "", "", false)
	} else {
		recordStage("stop-old-core", "already_clean", "", "no tracked process", false)
	}
	m.state = StateStarting

	// 3. Spawn new sing-box with the fresh config.
	binPath := filepath.Join(m.dataDir, "bin", "sing-box")
	cmd := exec.Command(binPath, "run", "-c", configPath)
	logFile, logPath, err := m.openSingBoxLog()
	if err != nil {
		m.state = StateDegraded
		return failStage("open-core-log", "hot-swap open sing-box log", "CORE_LOG_OPEN_FAILED", err, false)
	}
	recordStage("open-core-log", "ok", "", logPath, false)
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
		return failStage("spawn-core", "hot-swap spawn", "CORE_SPAWN_FAILED", err, false)
	}
	logFile.Close()
	m.process = cmd.Process
	m.pid = cmd.Process.Pid
	exitCh := watchCommand(cmd)
	m.exitCh = exitCh
	recordStage("spawn-core", "ok", "", fmt.Sprintf("pid=%d", m.pid), false)

	pidPath := filepath.Join(m.dataDir, "run", "singbox.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(m.pid)), 0640)

	// 4. Wait for runtime listeners.
	if spec, err := m.waitRuntimeListeners(exitCh, logPath, recordStage); err != nil {
		_ = m.process.Signal(syscall.SIGKILL)
		select {
		case <-exitCh:
		case <-time.After(2 * time.Second):
		}
		m.process = nil
		m.exitCh = nil
		m.pid = 0
		m.activeProfile = ""
		m.startedAt = time.Time{}
		_ = os.Remove(pidPath)
		m.state = StateDegraded
		return failStage(spec.Stage, "hot-swap "+spec.Layer, spec.Code, fmt.Errorf("%s port %d not ready: %w", spec.Label, spec.Port, err), true)
	}

	// 5. iptables left untouched — they still point at the same tproxy port.
	m.activeProfile = profile.Protocol + "://" + profile.Address
	m.startedAt = time.Now()
	m.state = StateRunning
	m.markActive()
	_ = os.Remove(filepath.Join(m.dataDir, "config", "manual"))
	m.logger.Printf("hot-swap complete (pid=%d)", m.pid)
	recordStage("commit-state", "ok", "", m.activeProfile, false)
	stageReport.finishOK()
	m.lastRuntimeReport = stageReport
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
	return runSingBoxConfigCheck(binPath, configPath, singBoxCheckTimeout)
}

func runSingBoxConfigCheck(binPath string, configPath string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "check", "-c", configPath)
	outBytes, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(outBytes))
	if ctx.Err() == context.DeadlineExceeded {
		if out != "" {
			return fmt.Errorf("sing-box check timed out after %s; output: %s", timeout, out)
		}
		return fmt.Errorf("sing-box check timed out after %s", timeout)
	}
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

func (m *CoreManager) waitRuntimeListeners(exitCh <-chan error, logPath string, recordStage func(string, string, string, string, bool)) (listenerWaitSpec, error) {
	for _, spec := range m.runtimeListenerWaits() {
		if err := m.waitForPortOrExit(spec.Port, spec.Timeout, exitCh, logPath); err != nil {
			return spec, err
		}
		m.logger.Printf("%s port %d is listening", spec.Label, spec.Port)
		if recordStage != nil {
			recordStage(spec.Stage, "ok", "", fmt.Sprintf("port=%d", spec.Port), false)
		}
	}
	return listenerWaitSpec{}, nil
}

func (m *CoreManager) runtimeListenerWaits() []listenerWaitSpec {
	tproxyPort := defaultPort(m.config.Proxy.TProxyPort, 10853)
	dnsPort := defaultPort(m.config.Proxy.DNSPort, 10856)
	specs := []listenerWaitSpec{
		{
			Stage:   "wait-tproxy",
			Layer:   "wait tproxy port",
			Code:    "TPROXY_PORT_DOWN",
			Label:   "TPROXY",
			Port:    tproxyPort,
			Timeout: 30 * time.Second,
		},
		{
			Stage:   "wait-dns",
			Layer:   "wait dns port",
			Code:    "DNS_LISTENER_DOWN",
			Label:   "DNS",
			Port:    dnsPort,
			Timeout: 10 * time.Second,
		},
	}
	if apiPort := m.config.Proxy.APIPort; apiPort > 0 {
		specs = append(specs, listenerWaitSpec{
			Stage:   "wait-api",
			Layer:   "wait API port",
			Code:    "API_PORT_DOWN",
			Label:   "API",
			Port:    apiPort,
			Timeout: 10 * time.Second,
		})
	}
	return specs
}

func defaultPort(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
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
		StartReport:   m.lastStartReport,
		RuntimeReport: m.lastRuntimeReport,
	}
	if !m.startedAt.IsZero() {
		info.Uptime = time.Since(m.startedAt).Truncate(time.Second).String()
	}
	return info
}

func (m *CoreManager) LastStartReport() RuntimeStageReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastStartReport
}

func (m *CoreManager) LastRuntimeReport() RuntimeStageReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastRuntimeReport
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// killProcess sends SIGTERM, waits up to 5 s, then SIGKILL.
func (m *CoreManager) killProcess() error {
	if m.process == nil {
		return nil
	}

	proc := m.process
	pid := m.pid
	waitCh := m.exitCh
	if waitCh == nil {
		return fmt.Errorf("pid %d has no tracked process wait channel", pid)
	}

	m.logger.Printf("sending SIGTERM to pid %d", pid)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Process may have already exited — not fatal.
		m.logger.Printf("SIGTERM failed (may be already dead): %v", err)
		select {
		case err := <-waitCh:
			if err != nil {
				m.logger.Printf("tracked wait after SIGTERM failure returned: %v", err)
			}
		case <-time.After(500 * time.Millisecond):
			if proc.Signal(syscall.Signal(0)) == nil {
				return fmt.Errorf("pid %d still appears alive after SIGTERM failed: %w", pid, err)
			}
		}
		return nil
	}

	select {
	case err := <-waitCh:
		if err != nil {
			m.logger.Printf("wait after SIGTERM returned: %v", err)
		} else {
			m.logger.Printf("pid %d exited after SIGTERM", pid)
		}
		return nil
	case <-time.After(5 * time.Second):
	}

	// Still alive — escalate.
	m.logger.Printf("pid %d did not exit, sending SIGKILL", pid)
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("SIGKILL pid %d: %w", pid, err)
	}
	select {
	case err := <-waitCh:
		if err != nil {
			m.logger.Printf("wait after SIGKILL returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		return fmt.Errorf("pid %d did not exit after SIGKILL", pid)
	}
	return nil
}

func watchCommand(cmd *exec.Cmd) <-chan error {
	exitCh := make(chan error, 1)
	go func() {
		exitCh <- cmd.Wait()
	}()
	return exitCh
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
	appRouting := BuildRuntimeAppRoutingEnv(
		m.config.Apps.Mode,
		m.config.Apps.Packages,
		m.config.Routing.AlwaysDirectApps,
		m.config.Routing.Mode,
	)
	chainProxyPorts, chainProxyUIDs := BuildChainedProxyProtectionEnv(m.config)

	return map[string]string{
		"PRIVSTACK_DIR":     m.dataDir,
		"CORE_GID":          strconv.Itoa(gid),
		"TPROXY_PORT":       strconv.Itoa(tproxyPort),
		"DNS_PORT":          strconv.Itoa(dnsPort),
		"API_PORT":          strconv.Itoa(apiPort),
		"SOCKS_PORT":        strconv.Itoa(panelInbounds.SocksPort),
		"HTTP_PORT":         strconv.Itoa(panelInbounds.HTTPPort),
		"CHAIN_PROXY_PORTS": chainProxyPorts,
		"CHAIN_PROXY_UIDS":  chainProxyUIDs,
		"FWMARK":            fmt.Sprintf("0x%x", mark),
		"ROUTE_TABLE":       "2023",
		"ROUTE_TABLE_V6":    "2024",
		"APP_MODE":          appRouting.AppMode,
		"APP_UIDS":          appRouting.AppUIDs,
		"PROXY_UIDS":        appRouting.ProxyUIDs,
		"DIRECT_UIDS":       appRouting.DirectUIDs,
		"BYPASS_UIDS":       appRouting.BypassUIDs,
		"DNS_SCOPE":         appRouting.DNSScope,
		"DNS_MODE":          appRouting.LegacyDNSMode,
		"PROXY_MODE":        "tproxy",
		"SHARING_MODE":      m.config.SharingModeEnv(),
		"SHARING_IFACES":    m.config.SharingInterfacesEnv(),
	}
}

func (m *CoreManager) netstack() netstack.Manager {
	return netstack.New(m.dataDir, m.scriptEnv(), ExecScript)
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
