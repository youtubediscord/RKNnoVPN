package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/rescue"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/watcher"
)

var Version = "v1.8.0"

// daemon holds all runtime state, wiring the internal subsystems together.
type daemon struct {
	cfg         *config.Config
	cfgPath     string
	profilePath string
	dataDir     string

	coreMgr    *core.CoreManager
	healthMon  *health.HealthMonitor
	rescueMgr  *rescue.RescueManager
	netWatcher *watcher.NetworkWatcher
	ipcServer  *ipc.Server
	runtimeV2  *runtimev2.Orchestrator

	mu                    sync.Mutex // protects cfg
	metricsMu             sync.Mutex
	runtimeOpMu           sync.Mutex
	resetMu               sync.Mutex
	reportMu              sync.Mutex
	runtimeDesiredRunning bool
	runtimeOpEpoch        uint64
	traffic               trafficSnapshot
	latency               latencySnapshot
	egress                egressSnapshot
	healthKick            time.Time
	lastReloadReport      core.RuntimeStageReport

	collectLeftoversOverride func(*config.Config) []string
}

type trafficSnapshot struct {
	TxBytes   int64
	RxBytes   int64
	CheckedAt time.Time
}

type latencySnapshot struct {
	Ms        int64
	Valid     bool
	CheckedAt time.Time
}

type egressSnapshot struct {
	IP        string
	Valid     bool
	CheckedAt time.Time
}

func main() {
	cfgPath := flag.String("config", "/data/adb/privstack/config/config.json", "path to config.json")
	dataDir := flag.String("data-dir", "/data/adb/privstack", "path to data directory")
	logFile := flag.String("log-file", "", "path to log file (default: stderr)")
	pidFile := flag.String("pid-file", "", "path to PID file")
	flag.Parse()

	// ---- Logging -----------------------------------------------------------

	if *logFile != "" {
		if err := os.MkdirAll(filepath.Dir(*logFile), 0750); err != nil {
			log.Fatalf("mkdir log dir: %v", err)
		}
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			log.Fatalf("open log file: %v", err)
		}
		defer f.Close()
		log.SetOutput(f)
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Printf("privd %s starting", Version)

	// ---- PID file ----------------------------------------------------------

	if *pidFile != "" {
		if err := writePID(*pidFile); err != nil {
			log.Fatalf("write pid: %v", err)
		}
		defer os.Remove(*pidFile)
	}

	// ---- Data directories --------------------------------------------------

	for _, sub := range []string{"run", "data", "log", "logs", "config/rendered", "backup"} {
		if err := os.MkdirAll(filepath.Join(*dataDir, sub), 0750); err != nil {
			log.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// ---- Configuration -----------------------------------------------------

	cfg, err := loadConfigWithProfile(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	profilePath := profiledoc.Path(*cfgPath)
	log.Printf("config loaded from %s", *cfgPath)

	// ---- Per-subsystem loggers ---------------------------------------------

	coreLogger := log.New(log.Writer(), "[core] ", log.LstdFlags)
	healthLogger := log.New(log.Writer(), "[health] ", log.LstdFlags)
	rescueLogger := log.New(log.Writer(), "[rescue] ", log.LstdFlags)
	watchLogger := log.New(log.Writer(), "[netwatch] ", log.LstdFlags)

	// ---- Core subsystems ---------------------------------------------------

	coreMgr := core.NewCoreManager(cfg, *dataDir, coreLogger)

	tproxyPort := cfg.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}

	healthInterval := time.Duration(cfg.Health.IntervalSec) * time.Second
	if healthInterval <= 0 {
		healthInterval = 30 * time.Second
	}

	healthThreshold := cfg.Health.Threshold
	if healthThreshold < 1 {
		healthThreshold = 3
	}

	cooldown := time.Duration(cfg.Rescue.CooldownSec) * time.Second
	if cooldown <= 0 {
		cooldown = 60 * time.Second
	}
	rescueMgr := rescue.NewRescueManager(coreMgr, cfg, *dataDir, cfg.Rescue.MaxAttempts, cooldown, rescueLogger)

	gid := cfg.Proxy.GID
	if gid == 0 {
		gid = 23333
	}
	mark := cfg.Proxy.Mark
	if mark == 0 {
		mark = 0x2023
	}
	dnsPort := cfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	healthTimeout := time.Duration(cfg.Health.TimeoutSec) * time.Second
	if healthTimeout <= 0 {
		healthTimeout = 5 * time.Second
	}
	healthMon := health.NewHealthMonitor(coreMgr, healthInterval, healthThreshold, tproxyPort, dnsPort, mark, cfg.Health.URL, healthTimeout, healthLogger)
	healthMon.SetConfig(healthInterval, healthThreshold, tproxyPort, dnsPort, mark, cfg.Health.URL, cfg.Health.DNSProbeDomains, cfg.Health.DNSIsHardReadiness, healthTimeout)
	scriptEnv := buildScriptEnv(cfg, *dataDir)
	var d *daemon
	netWatcher := watcher.NewNetworkWatcher(*dataDir, scriptEnv, func() error {
		if d == nil {
			return nil
		}
		_, err := d.runtimeV2.HandleNetworkChange()
		return err
	}, watchLogger)

	socketPath := filepath.Join(*dataDir, "run", "daemon.sock")

	d = &daemon{
		cfg:         cfg,
		cfgPath:     *cfgPath,
		profilePath: profilePath,
		dataDir:     *dataDir,
		coreMgr:     coreMgr,
		healthMon:   healthMon,
		rescueMgr:   rescueMgr,
		netWatcher:  netWatcher,
		ipcServer:   ipc.NewServer(socketPath),
	}
	d.initRuntimeV2()
	d.runtimeV2.SetOperationLogger(func(event runtimev2.OperationLogEvent) {
		fields := []string{
			"runtime_operation",
			"operation=" + string(event.Kind),
			"generation=" + strconv.FormatInt(event.Generation, 10),
			"phase=" + string(event.Phase),
			"result=" + event.Result,
		}
		if event.RuntimeMS > 0 {
			fields = append(fields, "runtime_ms="+strconv.FormatInt(event.RuntimeMS, 10))
		}
		if event.Step != "" {
			fields = append(fields, "step="+event.Step)
		}
		if event.StepStatus != "" {
			fields = append(fields, "step_status="+event.StepStatus)
		}
		if event.StepDetail != "" {
			fields = append(fields, "step_detail="+strconv.Quote(event.StepDetail))
		}
		if event.ErrorCode != "" {
			fields = append(fields, "code="+event.ErrorCode)
		}
		if event.Stuck {
			fields = append(fields, "stuck=true")
		}
		if event.ErrorMessage != "" {
			fields = append(fields, "error="+strconv.Quote(event.ErrorMessage))
		}
		log.Print(strings.Join(fields, " "))
	})

	d.healthMon.SetOnDegraded(func() {
		epoch := d.currentRuntimeOperationEpoch()
		if !d.canRunRuntimeRecovery(epoch) {
			log.Printf("rescue skipped: runtime is no longer desired running")
			return
		}
		_, err := d.runtimeV2.RunOperation(runtimev2.OperationRescue, runtimev2.PhaseStarting, func(generation int64) error {
			if !d.canRunRuntimeRecovery(epoch) {
				log.Printf("rescue skipped: runtime changed before recovery")
				return nil
			}
			d.mu.Lock()
			rescueEnabled := d.cfg.Rescue.Enabled
			d.mu.Unlock()
			if !rescueEnabled {
				log.Printf("rescue disabled, skipping automatic recovery")
				return nil
			}
			if err := d.rescueMgr.Attempt(func() bool {
				return d.canRunRuntimeRecovery(epoch)
			}); err != nil {
				log.Printf("rescue attempt failed: %v", err)
				d.mu.Lock()
				maxAttempts := d.cfg.Rescue.MaxAttempts
				d.mu.Unlock()
				if maxAttempts < 1 {
					maxAttempts = 1
				}
				if d.rescueMgr.Attempts() >= maxAttempts && d.canRunRuntimeRecovery(epoch) {
					if rollbackErr := d.rescueMgr.Rollback(); rollbackErr != nil {
						log.Printf("rescue rollback failed: %v", rollbackErr)
					}
				}
				return nil
			}
			return nil
		})
		if err != nil {
			log.Printf("rescue skipped or failed: %v", err)
		}
	})
	d.healthMon.SetOnRestored(func() {
		d.rescueMgr.Reset()
	})

	// ---- IPC server --------------------------------------------------------

	d.registerHandlers()
	if err := d.ipcServer.Start(); err != nil {
		log.Fatalf("ipc start: %v", err)
	}

	// ---- Autostart ---------------------------------------------------------

	if cfg.Autostart && fileMissing(filepath.Join(*dataDir, "config", "manual")) {
		log.Printf("autostart enabled, starting proxy")
		if err := d.syncRuntimeV2DesiredState(); err != nil {
			log.Printf("autostart desired state sync failed: %v", err)
		}
		if _, err := d.runtimeV2.Start(); err != nil {
			log.Printf("autostart failed: %v", err)
		}
	} else if cfg.Autostart {
		log.Printf("autostart skipped: manual reset flag is present")
	}

	// ---- Signal handling ---------------------------------------------------

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1)

	log.Printf("daemon ready, waiting for signals")
	for sig := range sigCh {
		switch sig {
		case syscall.SIGTERM, syscall.SIGINT:
			log.Printf("received %s, shutting down", sig)
			d.shutdown()
			log.Printf("goodbye")
			return

		case syscall.SIGHUP:
			log.Printf("received SIGHUP, reloading config")
			if err := d.reloadConfig(); err != nil {
				log.Printf("reload config failed: %v", err)
			}

		case syscall.SIGUSR1:
			log.Printf("received SIGUSR1, dumping state")
			d.dumpState()
		}
	}
}

func loadConfigWithProfile(cfgPath string) (*config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	profilePath := profiledoc.Path(cfgPath)
	profileDoc, profileFound, err := profiledoc.Load(profilePath)
	if err != nil {
		return nil, fmt.Errorf("load profile: %w", err)
	}
	if profileFound {
		cfg, _, err = profiledoc.ApplyToConfig(cfg, profileDoc)
		if err != nil {
			return nil, fmt.Errorf("apply profile: %w", err)
		}
		return cfg, nil
	}
	if cfg.Node.Address != "" {
		return nil, fmt.Errorf("profile.json is required for v2 config with an active node; legacy config-only nodes are unsupported")
	}
	if err := profiledoc.Save(profilePath, profiledoc.FromConfig(cfg)); err != nil {
		return nil, fmt.Errorf("initialize profile: %w", err)
	}
	return cfg, nil
}

// --------------------------------------------------------------------------
// Subsystem lifecycle helpers
// --------------------------------------------------------------------------

// startSubsystems launches health monitoring and network watching.
// Call this after CoreManager.Start succeeds.
func (d *daemon) startSubsystems() {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	// Health monitor.
	if d.healthMon != nil && cfg != nil && cfg.Health.Enabled && cfg.Health.IntervalSec > 0 {
		d.healthMon.Start()
	}

	// Network watcher (best-effort -- missing inotifyd is not fatal).
	if d.netWatcher != nil {
		if err := d.netWatcher.Start(); err != nil {
			log.Printf("network watcher not started: %v", err)
		}
	}
}

// stopSubsystems halts health monitoring and network watching.
func (d *daemon) stopSubsystems() {
	if d.healthMon != nil {
		d.healthMon.Stop()
	}
	if d.netWatcher != nil {
		d.netWatcher.Stop()
	}
}

// shutdown performs a full graceful shutdown of every subsystem.
func (d *daemon) shutdown() {
	d.stopSubsystems()
	if err := d.coreMgr.Stop(); err != nil {
		log.Printf("core stop error: %v", err)
	}
	if d.ipcServer != nil {
		d.ipcServer.Stop()
	}
}

// --------------------------------------------------------------------------
// Config reload
// --------------------------------------------------------------------------

func (d *daemon) reloadConfig() error {
	newCfg, err := loadConfigWithProfile(d.cfgPath)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}

	if err := d.applyConfig(newCfg, true); err != nil {
		return fmt.Errorf("reload config apply: %w", err)
	}

	log.Printf("config reloaded")
	return nil
}

// --------------------------------------------------------------------------
// State dump
// --------------------------------------------------------------------------

func (d *daemon) dumpState() {
	status := d.coreMgr.Status()

	d.mu.Lock()
	cfgPath := d.cfgPath
	dataDir := d.dataDir
	rescueEnabled := d.cfg.Rescue.Enabled
	d.mu.Unlock()

	state := map[string]interface{}{
		"version":         Version,
		"config_path":     cfgPath,
		"data_dir":        dataDir,
		"core_state":      status.State,
		"core_pid":        status.PID,
		"uptime":          status.Uptime,
		"active_profile":  status.ActiveProfile,
		"health_fails":    d.healthMon.Failures(),
		"rescue_attempts": d.rescueMgr.Attempts(),
		"rescue_enabled":  rescueEnabled,
	}

	data, _ := json.MarshalIndent(state, "", "  ")
	log.Printf("STATE DUMP:\n%s", string(data))
}

// --------------------------------------------------------------------------
// PID file
// --------------------------------------------------------------------------

func writePID(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0644)
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// --------------------------------------------------------------------------
// IPC handler registration
// --------------------------------------------------------------------------

func (d *daemon) registerHandlers() {
	handlers := map[string]ipc.Handler{
		"app.list":                  d.handleAppList,
		"app.resolveUid":            d.handleResolveUID,
		"audit":                     d.handleAudit,
		"backend.applyDesiredState": d.handleBackendApplyDesiredState,
		"backend.reset":             d.handleBackendReset,
		"backend.restart":           d.handleBackendRestart,
		"backend.start":             d.handleBackendStart,
		"backend.status":            d.handleBackendStatus,
		"backend.stop":              d.handleBackendStop,
		"config-import":             d.handleConfigImport,
		"config-list":               d.handleConfigList,
		"diagnostics.health":        d.handleDiagnosticsHealth,
		"diagnostics.testNodes":     d.handleDiagnosticsTestNodes,
		"doctor":                    d.handleDoctor,
		"ipc.contract":              d.handleIPCContract,
		"logs":                      d.handleLogs,
		"profile.apply":             d.handleProfileApply,
		"profile.get":               d.handleProfileGet,
		"profile.importNodes":       d.handleProfileImportNodes,
		"profile.setActiveNode":     d.handleProfileSetActiveNode,
		"self-check":                d.handleSelfCheck,
		"subscription.preview":      d.handleSubscriptionPreview,
		"subscription.refresh":      d.handleSubscriptionRefresh,
		"update-check":              d.handleUpdateCheck,
		"update-download":           d.handleUpdateDownload,
		"update-install":            d.handleUpdateInstall,
		"version":                   d.handleVersion,
	}
	for _, contract := range ipc.MethodContracts() {
		handler, ok := handlers[contract.Method]
		if !ok {
			log.Printf("ipc: contract method %s has no daemon handler", contract.Method)
			continue
		}
		d.ipcServer.Register(contract.Method, handler)
	}
}

// --------------------------------------------------------------------------
// IPC handlers
// --------------------------------------------------------------------------

func (d *daemon) rpcErrorFromRuntimeError(err error) *ipc.RPCError {
	var busy *runtimev2.OperationBusyError
	if errors.As(err, &busy) {
		return &ipc.RPCError{
			Code:    ipc.CodeRuntimeBusy,
			Message: busy.Error(),
			Data:    busy.Data(),
		}
	}
	if strings.Contains(err.Error(), "no node configured") {
		return &ipc.RPCError{Code: ipc.CodeConfigError, Message: err.Error()}
	}
	return &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
}

func testTCPConnect(host string, port int, timeout time.Duration) (int64, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return time.Since(start).Milliseconds(), nil
}

func testClashDelay(apiPort int, outboundTag string, testURL string, timeoutMS int) (int64, int, error) {
	if apiPort <= 0 {
		return 0, 0, fmt.Errorf("api_disabled")
	}
	if outboundTag == "" {
		return 0, 0, fmt.Errorf("outbound tag is empty")
	}
	values := neturl.Values{}
	values.Set("timeout", strconv.Itoa(timeoutMS))
	values.Set("url", testURL)
	endpoint := fmt.Sprintf(
		"http://127.0.0.1:%d/proxies/%s/delay?%s",
		apiPort,
		neturl.PathEscape(outboundTag),
		values.Encode(),
	)
	client := &http.Client{Timeout: time.Duration(timeoutMS+1000) * time.Millisecond}
	resp, err := client.Get(endpoint)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, resp.StatusCode, fmt.Errorf("clash delay HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Delay int64 `json:"delay"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, resp.StatusCode, fmt.Errorf("parse clash delay response: %w", err)
	}
	return parsed.Delay, resp.StatusCode, nil
}

func testTransparentURLDelay(cfg *config.Config, testURL string, timeoutMS int) (int64, int, error) {
	metrics, err := testTransparentURLProbe(cfg, testURL, timeoutMS)
	return metrics.LatencyMS, metrics.StatusCode, err
}

type urlProbeMetrics struct {
	LatencyMS     int64
	StatusCode    int
	ResponseBytes int64
	ThroughputBps int64
}

func testTransparentURLProbe(cfg *config.Config, testURL string, timeoutMS int) (urlProbeMetrics, error) {
	var metrics urlProbeMetrics
	if cfg == nil {
		return metrics, fmt.Errorf("config is nil")
	}
	if timeoutMS <= 0 {
		timeoutMS = 5000
	}
	parsed, err := neturl.Parse(strings.TrimSpace(testURL))
	if err != nil {
		return metrics, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return metrics, fmt.Errorf("unsupported URL scheme %q", parsed.Scheme)
	}

	timeout := time.Duration(timeoutMS) * time.Millisecond
	dnsPort := cfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	mark := cfg.Proxy.Mark
	if mark == 0 {
		mark = 0x2023
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: timeout}
			return dialer.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(dnsPort)))
		},
	}
	dialer := &net.Dialer{
		Timeout:  timeout,
		Resolver: resolver,
		Control: func(network, address string, conn syscall.RawConn) error {
			var sockErr error
			if err := conn.Control(func(fd uintptr) {
				sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
			}); err != nil {
				return err
			}
			return sockErr
		},
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: timeout,
		DisableKeepAlives:   true,
	}
	defer transport.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), timeout+time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return metrics, err
	}
	req.Header.Set("User-Agent", "PrivStack/health")

	start := time.Now()
	resp, err := (&http.Client{Timeout: timeout + time.Second, Transport: transport}).Do(req)
	if err != nil {
		return metrics, err
	}
	defer resp.Body.Close()
	bytesRead, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024*1024))
	elapsed := time.Since(start)
	metrics.LatencyMS = elapsed.Milliseconds()
	metrics.StatusCode = resp.StatusCode
	metrics.ResponseBytes = bytesRead
	if bytesRead > 0 && elapsed > 0 {
		metrics.ThroughputBps = int64(float64(bytesRead) / elapsed.Seconds())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return metrics, fmt.Errorf("transparent URL probe HTTP %d", resp.StatusCode)
	}
	return metrics, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (d *daemon) buildTrafficPayload(state core.State, apiPort int) map[string]interface{} {
	payload := map[string]interface{}{
		"txBytes": int64(0),
		"rxBytes": int64(0),
		"txRate":  int64(0),
		"rxRate":  int64(0),
	}
	if state != core.StateRunning && state != core.StateDegraded {
		d.resetRuntimeMetrics()
		return payload
	}

	txBytes, rxBytes, err := queryClashTraffic(apiPort, 1200*time.Millisecond)
	if err != nil {
		return payload
	}

	now := time.Now()
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()

	txRate := int64(0)
	rxRate := int64(0)
	if !d.traffic.CheckedAt.IsZero() {
		elapsed := now.Sub(d.traffic.CheckedAt).Seconds()
		if elapsed > 0 {
			if txBytes >= d.traffic.TxBytes {
				txRate = int64(float64(txBytes-d.traffic.TxBytes) / elapsed)
			}
			if rxBytes >= d.traffic.RxBytes {
				rxRate = int64(float64(rxBytes-d.traffic.RxBytes) / elapsed)
			}
		}
	}
	d.traffic = trafficSnapshot{
		TxBytes:   txBytes,
		RxBytes:   rxBytes,
		CheckedAt: now,
	}

	payload["txBytes"] = txBytes
	payload["rxBytes"] = rxBytes
	payload["txRate"] = txRate
	payload["rxRate"] = rxRate
	return payload
}

func (d *daemon) cachedLatencyMs(state core.State, cfg *config.Config, apiPort int) *int64 {
	latency, _ := d.refreshOutboundURLProbe(state, cfg, apiPort, 2500)
	return latency
}

func (d *daemon) refreshOutboundURLProbe(state core.State, cfg *config.Config, apiPort int, timeoutMS int) (*int64, health.CheckResult) {
	if state != core.StateRunning && state != core.StateDegraded {
		return nil, health.CheckResult{
			Pass:   false,
			Detail: "runtime is not running",
			Code:   "OUTBOUND_NOT_RUNNING",
		}
	}
	if timeoutMS <= 0 {
		timeoutMS = 2500
	}

	now := time.Now()
	d.metricsMu.Lock()
	if d.latency.Valid && now.Sub(d.latency.CheckedAt) < 30*time.Second {
		value := d.latency.Ms
		d.metricsMu.Unlock()
		return &value, health.CheckResult{
			Pass:   true,
			Detail: fmt.Sprintf("recent outbound URL probe ok: %d ms", value),
		}
	}
	if !d.latency.Valid && !d.latency.CheckedAt.IsZero() && now.Sub(d.latency.CheckedAt) < 10*time.Second {
		d.metricsMu.Unlock()
		return nil, health.CheckResult{
			Pass:   false,
			Detail: "recent outbound URL probe failed",
			Code:   "OUTBOUND_URL_FAILED",
		}
	}
	d.metricsMu.Unlock()

	var latency int64
	var err error
	var lastURL string
	for _, testURL := range healthEgressURLs(cfg) {
		lastURL = testURL
		if apiPort > 0 {
			latency, _, err = testClashDelay(apiPort, "proxy", testURL, timeoutMS)
		} else {
			latency, _, err = testTransparentURLDelay(cfg, testURL, timeoutMS)
		}
		if err == nil {
			break
		}
	}

	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	d.latency.CheckedAt = now
	if err != nil {
		d.latency.Valid = false
		return nil, health.CheckResult{
			Pass:   false,
			Detail: fmt.Sprintf("outbound URL probe failed for %s: %v", lastURL, err),
			Code:   "OUTBOUND_URL_FAILED",
		}
	}
	d.latency.Valid = true
	d.latency.Ms = latency
	value := latency
	return &value, health.CheckResult{
		Pass:   true,
		Detail: fmt.Sprintf("outbound URL probe ok: %d ms", latency),
	}
}

func healthEgressURLs(cfg *config.Config) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, 3)
	add := func(raw string) {
		url := strings.TrimSpace(raw)
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		result = append(result, url)
	}
	if cfg != nil {
		for _, url := range cfg.Health.EgressURLs {
			add(url)
		}
		add(cfg.Health.URL)
	}
	add("https://www.gstatic.com/generate_204")
	add("https://cp.cloudflare.com/generate_204")
	return result
}

func (d *daemon) cachedEgressIP(state core.State, httpPort int) *string {
	if state != core.StateRunning && state != core.StateDegraded {
		return nil
	}
	if httpPort <= 0 {
		return nil
	}

	now := time.Now()
	d.metricsMu.Lock()
	if d.egress.Valid && now.Sub(d.egress.CheckedAt) < 30*time.Second {
		value := d.egress.IP
		d.metricsMu.Unlock()
		return &value
	}
	if !d.egress.Valid && !d.egress.CheckedAt.IsZero() && now.Sub(d.egress.CheckedAt) < 10*time.Second {
		d.metricsMu.Unlock()
		return nil
	}
	d.metricsMu.Unlock()

	ip, err := fetchProxyEgressIP(httpPort, 4*time.Second)

	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	d.egress.CheckedAt = now
	if err != nil {
		d.egress.Valid = false
		d.egress.IP = ""
		return nil
	}
	d.egress.Valid = true
	d.egress.IP = ip
	value := ip
	return &value
}

func (d *daemon) resetRuntimeMetrics() {
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	d.traffic = trafficSnapshot{}
	d.latency = latencySnapshot{}
	d.egress = egressSnapshot{}
	d.healthKick = time.Time{}
}

func queryClashTraffic(apiPort int, timeout time.Duration) (int64, int64, error) {
	if apiPort <= 0 {
		return 0, 0, fmt.Errorf("api_disabled")
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/connections", apiPort)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(endpoint)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, 0, fmt.Errorf("connections HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		UploadTotal   int64 `json:"uploadTotal"`
		DownloadTotal int64 `json:"downloadTotal"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, 0, err
	}
	return parsed.UploadTotal, parsed.DownloadTotal, nil
}

func fetchProxyEgressIP(httpPort int, timeout time.Duration) (string, error) {
	if httpPort <= 0 {
		return "", fmt.Errorf("http helper inbound is disabled")
	}
	proxyURL, err := neturl.Parse(fmt.Sprintf("http://127.0.0.1:%d", httpPort))
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://cloudflare.com/cdn-cgi/trace",
	}

	for _, endpoint := range endpoints {
		ip, endpointErr := fetchIPFromEndpoint(client, endpoint)
		if endpointErr == nil {
			return ip, nil
		}
		err = endpointErr
	}
	if err == nil {
		err = fmt.Errorf("no egress ip endpoint succeeded")
	}
	return "", err
}

func fetchIPFromEndpoint(client *http.Client, endpoint string) (string, error) {
	resp, err := client.Get(endpoint)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s HTTP %d", endpoint, resp.StatusCode)
	}
	text := strings.TrimSpace(string(body))
	if endpoint == "https://cloudflare.com/cdn-cgi/trace" {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "ip=") {
				text = strings.TrimSpace(strings.TrimPrefix(line, "ip="))
				break
			}
		}
	}
	if ip := net.ParseIP(text); ip != nil {
		return text, nil
	}
	return "", fmt.Errorf("%s returned invalid ip %q", endpoint, text)
}

func buildHealthPayload(state core.State, result *health.HealthResult, egressReady bool) map[string]interface{} {
	payload := map[string]interface{}{
		"healthy":            false,
		"coreRunning":        state != core.StateStopped,
		"tunActive":          false,
		"dnsOperational":     false,
		"routingReady":       false,
		"egressReady":        egressReady,
		"operationalHealthy": false,
		"lastCode":           nil,
		"lastError":          nil,
		"checkedAt":          int64(0),
	}
	if result == nil {
		return payload
	}

	payload["healthy"] = result.Overall
	payload["checkedAt"] = result.Timestamp.Unix()

	dnsOK := false
	iptablesOK := false
	routingOK := false
	if check, ok := result.Checks["dns"]; ok {
		dnsOK = check.Pass
	}
	if check, ok := result.Checks["dns_listener"]; ok {
		dnsOK = dnsOK && check.Pass
	}
	if check, ok := result.Checks["iptables"]; ok {
		iptablesOK = check.Pass
	}
	if check, ok := result.Checks["routing"]; ok {
		routingOK = check.Pass
	}

	payload["dnsOperational"] = dnsOK
	payload["tunActive"] = false
	payload["routingReady"] = iptablesOK && routingOK
	payload["operationalHealthy"] = result.Overall && dnsOK && egressReady
	if issue := firstHealthIssue(result.Checks, result.Overall, egressReady); issue.Detail != "" {
		if issue.Code != "" {
			payload["lastCode"] = issue.Code
		}
		payload["lastError"] = issue.Detail
	}
	return payload
}

func firstHealthError(checks map[string]health.CheckResult, readinessOK bool, egressReady bool) string {
	return firstHealthIssue(checks, readinessOK, egressReady).Detail
}

func firstHealthIssue(checks map[string]health.CheckResult, readinessOK bool, egressReady bool) healthGateDiagnostic {
	if firstIssue := firstFailedCheckDiagnostic(checks, "singbox_alive", "tproxy_port", "iptables", "routing"); firstIssue.Detail != "" {
		return firstIssue
	}
	if readinessOK {
		for _, name := range []string{"dns_listener", "dns"} {
			if check, ok := checks[name]; ok && !check.Pass {
				return healthGateDiagnostic{
					Code:   firstNonEmpty(check.Code, "PROXY_DNS_UNAVAILABLE"),
					Detail: fmt.Sprintf("operational degraded: proxy DNS unavailable: %s", formatHealthCheckError(name, check)),
				}
			}
		}
		if !egressReady {
			if check, ok := checks["outbound_url"]; ok && !check.Pass {
				return healthGateDiagnostic{
					Code:   firstNonEmpty(check.Code, "OUTBOUND_URL_FAILED"),
					Detail: fmt.Sprintf("operational degraded: outbound URL probe failed: %s", formatHealthCheckError("outbound_url", check)),
				}
			}
			return healthGateDiagnostic{
				Code:   "OUTBOUND_URL_FAILED",
				Detail: "operational degraded: no recent successful egress probe",
			}
		}
	}
	return healthGateDiagnostic{}
}

func firstFailedCheck(checks map[string]health.CheckResult, orderedNames ...string) string {
	return firstFailedCheckDiagnostic(checks, orderedNames...).Detail
}

func firstFailedCheckDiagnostic(checks map[string]health.CheckResult, orderedNames ...string) healthGateDiagnostic {
	for _, name := range orderedNames {
		if check, ok := checks[name]; ok && !check.Pass {
			return healthGateDiagnostic{
				Code:   firstNonEmpty(check.Code, "READINESS_GATE_FAILED"),
				Detail: formatHealthCheckError(name, check),
			}
		}
	}
	return healthGateDiagnostic{}
}

// --------------------------------------------------------------------------
// Utility
// --------------------------------------------------------------------------

// splitLines splits a string into lines, dropping trailing empty line.
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
