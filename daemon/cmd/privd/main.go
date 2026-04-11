package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/privstack/daemon/internal/config"
	"github.com/privstack/daemon/internal/core"
	"github.com/privstack/daemon/internal/health"
	"github.com/privstack/daemon/internal/ipc"
	"github.com/privstack/daemon/internal/rescue"
	"github.com/privstack/daemon/internal/watcher"
)

const version = "0.2.0"

// daemon holds all runtime state, wiring the internal subsystems together.
type daemon struct {
	cfg     *config.Config
	cfgPath string
	dataDir string

	coreMgr    *core.CoreManager
	healthMon  *health.HealthMonitor
	rescueMgr  *rescue.RescueManager
	netWatcher *watcher.NetworkWatcher
	ipcServer  *ipc.Server

	mu sync.Mutex // protects cfg
}

func main() {
	cfgPath := flag.String("config", "/data/adb/privstack/config.json", "path to config.json")
	dataDir := flag.String("data-dir", "/data/adb/privstack", "path to data directory")
	logFile := flag.String("log-file", "", "path to log file (default: stderr)")
	pidFile := flag.String("pid-file", "", "path to PID file")
	flag.Parse()

	// ---- Logging -----------------------------------------------------------

	if *logFile != "" {
		if err := os.MkdirAll(filepath.Dir(*logFile), 0750); err != nil {
			log.Fatalf("mkdir log dir: %v", err)
		}
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640)
		if err != nil {
			log.Fatalf("open log file: %v", err)
		}
		defer f.Close()
		log.SetOutput(f)
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Printf("privd %s starting", version)

	// ---- PID file ----------------------------------------------------------

	if *pidFile != "" {
		if err := writePID(*pidFile); err != nil {
			log.Fatalf("write pid: %v", err)
		}
		defer os.Remove(*pidFile)
	}

	// ---- Data directories --------------------------------------------------

	for _, sub := range []string{"run", "data", "log", "config/rendered", "backup"} {
		if err := os.MkdirAll(filepath.Join(*dataDir, sub), 0750); err != nil {
			log.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	// ---- Configuration -----------------------------------------------------

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
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
		tproxyPort = 10808
	}

	healthInterval := time.Duration(cfg.Health.IntervalSec) * time.Second
	if healthInterval <= 0 {
		healthInterval = 30 * time.Second
	}

	healthThreshold := cfg.Rescue.MaxFailures
	if healthThreshold < 1 {
		healthThreshold = 3
	}

	healthMon := health.NewHealthMonitor(coreMgr, healthInterval, healthThreshold, tproxyPort, healthLogger)

	cooldown := time.Duration(cfg.Rescue.CooldownSec) * time.Second
	if cooldown <= 0 {
		cooldown = 60 * time.Second
	}
	rescueMgr := rescue.NewRescueManager(coreMgr, cfg, *dataDir, cfg.Rescue.MaxFailures, cooldown, rescueLogger)

	scriptEnv := map[string]string{
		"PRIVSTACK_DIR": *dataDir,
		"CORE_GID":      "23333",
		"TPROXY_PORT":   strconv.Itoa(cfg.Proxy.TProxyPort),
		"DNS_PORT":      strconv.Itoa(cfg.Proxy.DNSPort),
		"FWMARK":        "0x2023",
	}
	netWatcher := watcher.NewNetworkWatcher(*dataDir, scriptEnv, watchLogger)

	socketPath := filepath.Join(*dataDir, "run", "daemon.sock")

	d := &daemon{
		cfg:        cfg,
		cfgPath:    *cfgPath,
		dataDir:    *dataDir,
		coreMgr:    coreMgr,
		healthMon:  healthMon,
		rescueMgr:  rescueMgr,
		netWatcher: netWatcher,
		ipcServer:  ipc.NewServer(socketPath),
	}

	// ---- IPC server --------------------------------------------------------

	d.registerHandlers()
	if err := d.ipcServer.Start(); err != nil {
		log.Fatalf("ipc start: %v", err)
	}

	// ---- Autostart ---------------------------------------------------------

	if cfg.Autostart {
		log.Printf("autostart enabled, starting proxy")
		profile := cfg.ResolveProfile()
		if err := d.coreMgr.Start(profile); err != nil {
			log.Printf("autostart failed: %v", err)
		} else {
			// Proxy is running -- start the supporting subsystems.
			d.startSubsystems()
		}
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
			d.reloadConfig()

		case syscall.SIGUSR1:
			log.Printf("received SIGUSR1, dumping state")
			d.dumpState()
		}
	}
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
	if cfg.Health.IntervalSec > 0 {
		d.healthMon.Start()
	}

	// Network watcher (best-effort -- missing inotifyd is not fatal).
	if err := d.netWatcher.Start(); err != nil {
		log.Printf("network watcher not started: %v", err)
	}
}

// stopSubsystems halts health monitoring and network watching.
func (d *daemon) stopSubsystems() {
	d.healthMon.Stop()
	d.netWatcher.Stop()
}

// shutdown performs a full graceful shutdown of every subsystem.
func (d *daemon) shutdown() {
	d.stopSubsystems()
	if err := d.coreMgr.Stop(); err != nil {
		log.Printf("core stop error: %v", err)
	}
	d.ipcServer.Stop()
}

// --------------------------------------------------------------------------
// Config reload
// --------------------------------------------------------------------------

func (d *daemon) reloadConfig() {
	newCfg, err := config.Load(d.cfgPath)
	if err != nil {
		log.Printf("reload config failed: %v", err)
		return
	}

	wasRunning := d.coreMgr.GetState() == core.StateRunning ||
		d.coreMgr.GetState() == core.StateDegraded

	if wasRunning {
		d.stopSubsystems()
	}

	// Push new config to subsystems.
	d.mu.Lock()
	d.cfg = newCfg
	d.mu.Unlock()

	d.coreMgr.SetConfig(newCfg)
	log.Printf("config reloaded")

	if wasRunning {
		// Hot-swap the proxy with the new config profile.
		profile := newCfg.ResolveProfile()
		if err := d.coreMgr.HotSwap(profile); err != nil {
			log.Printf("hot-swap after reload failed: %v", err)
		}
		d.rescueMgr.Reset()
		d.startSubsystems()
	}
}

// --------------------------------------------------------------------------
// State dump
// --------------------------------------------------------------------------

func (d *daemon) dumpState() {
	status := d.coreMgr.Status()

	d.mu.Lock()
	cfgPath := d.cfgPath
	dataDir := d.dataDir
	rescueEnabled := d.cfg.Rescue.Enable
	d.mu.Unlock()

	state := map[string]interface{}{
		"version":        version,
		"config_path":    cfgPath,
		"data_dir":       dataDir,
		"core_state":     status.State,
		"core_pid":       status.PID,
		"uptime":         status.Uptime,
		"active_profile": status.ActiveProfile,
		"health_fails":   d.healthMon.Failures(),
		"rescue_attempts": d.rescueMgr.Attempts(),
		"rescue_enabled": rescueEnabled,
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

// --------------------------------------------------------------------------
// IPC handler registration
// --------------------------------------------------------------------------

func (d *daemon) registerHandlers() {
	d.ipcServer.Register("status", d.handleStatus)
	d.ipcServer.Register("start", d.handleStart)
	d.ipcServer.Register("stop", d.handleStop)
	d.ipcServer.Register("reload", d.handleReload)
	d.ipcServer.Register("health", d.handleHealth)
	d.ipcServer.Register("config-get", d.handleConfigGet)
	d.ipcServer.Register("config-set", d.handleConfigSet)
	d.ipcServer.Register("config-list", d.handleConfigList)
	d.ipcServer.Register("config-import", d.handleConfigImport)
	d.ipcServer.Register("logs", d.handleLogs)
	d.ipcServer.Register("version", d.handleVersion)
}

// --------------------------------------------------------------------------
// IPC handlers
// --------------------------------------------------------------------------

func (d *daemon) handleStatus(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	status := d.coreMgr.Status()

	result := map[string]interface{}{
		"state":          status.State,
		"pid":            status.PID,
		"uptime":         status.Uptime,
		"active_profile": status.ActiveProfile,
		"started_at":     status.StartedAt,
		"health_fails":   d.healthMon.Failures(),
		"rescue_attempts": d.rescueMgr.Attempts(),
	}
	return result, nil
}

func (d *daemon) handleStart(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	state := d.coreMgr.GetState()
	if state == core.StateRunning || state == core.StateStarting {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeProxyAlready,
			Message: fmt.Sprintf("proxy already %s", state),
		}
	}

	d.mu.Lock()
	profile := d.cfg.ResolveProfile()
	d.mu.Unlock()

	if profile.Address == "" {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "no node configured (address is empty)",
		}
	}

	if err := d.coreMgr.Start(profile); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: err.Error(),
		}
	}

	d.rescueMgr.Reset()
	d.startSubsystems()

	return map[string]string{"status": "started"}, nil
}

func (d *daemon) handleStop(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	state := d.coreMgr.GetState()
	if state == core.StateStopped {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeProxyNotRunning,
			Message: "proxy is not running",
		}
	}

	d.stopSubsystems()
	if err := d.coreMgr.Stop(); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: err.Error(),
		}
	}

	return map[string]string{"status": "stopped"}, nil
}

func (d *daemon) handleReload(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	d.reloadConfig()
	return map[string]string{"status": "reloaded"}, nil
}

func (d *daemon) handleHealth(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	// Run a one-shot health check via the real HealthMonitor.
	healthResult := d.healthMon.RunOnce()

	d.mu.Lock()
	rescueEnabled := d.cfg.Rescue.Enable
	maxFailures := d.cfg.Rescue.MaxFailures
	d.mu.Unlock()

	result := map[string]interface{}{
		"state":           d.coreMgr.GetState().String(),
		"overall":         healthResult.Overall,
		"checks":          healthResult.Checks,
		"timestamp":       healthResult.Timestamp,
		"health_fails":    d.healthMon.Failures(),
		"rescue_enabled":  rescueEnabled,
		"max_failures":    maxFailures,
		"rescue_attempts": d.rescueMgr.Attempts(),
	}
	return result, nil
}

func (d *daemon) handleConfigGet(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: {\"key\": \"...\"}",
		}
	}

	var p struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}

	d.mu.Lock()
	data, err := json.Marshal(d.cfg)
	d.mu.Unlock()
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}

	var full map[string]interface{}
	json.Unmarshal(data, &full)

	val, ok := full[p.Key]
	if !ok {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: fmt.Sprintf("unknown config key: %s", p.Key),
		}
	}

	return map[string]interface{}{"key": p.Key, "value": val}, nil
}

func (d *daemon) handleConfigSet(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: {\"key\": \"...\", \"value\": ...}",
		}
	}

	var p struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	data, _ := json.Marshal(d.cfg)
	var full map[string]json.RawMessage
	json.Unmarshal(data, &full)

	full[p.Key] = p.Value

	patched, err := json.Marshal(full)
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}

	newCfg := config.DefaultConfig()
	if err := json.Unmarshal(patched, newCfg); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "invalid value: " + err.Error(),
		}
	}

	if err := newCfg.Validate(); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "validation failed: " + err.Error(),
		}
	}

	d.cfg = newCfg
	d.coreMgr.SetConfig(newCfg)

	if err := d.cfg.Save(d.cfgPath); err != nil {
		log.Printf("warning: failed to persist config: %v", err)
	}

	return map[string]string{"status": "ok"}, nil
}

func (d *daemon) handleConfigList(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	d.mu.Lock()
	data, err := json.Marshal(d.cfg)
	d.mu.Unlock()
	if err != nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}

	var full map[string]interface{}
	json.Unmarshal(data, &full)

	keys := make(map[string]string)
	for k, v := range full {
		keys[k] = fmt.Sprintf("%T", v)
	}
	return keys, nil
}

func (d *daemon) handleConfigImport(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: full config JSON object",
		}
	}

	newCfg := config.DefaultConfig()
	if err := json.Unmarshal(*params, newCfg); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "invalid config: " + err.Error(),
		}
	}

	if err := newCfg.Validate(); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "validation failed: " + err.Error(),
		}
	}

	d.mu.Lock()
	d.cfg = newCfg
	d.mu.Unlock()

	d.coreMgr.SetConfig(newCfg)

	if err := newCfg.Save(d.cfgPath); err != nil {
		log.Printf("warning: failed to persist imported config: %v", err)
	}

	return map[string]string{"status": "imported"}, nil
}

func (d *daemon) handleLogs(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	n := 50
	if params != nil {
		var p struct {
			Lines int `json:"lines"`
		}
		if err := json.Unmarshal(*params, &p); err == nil && p.Lines > 0 {
			n = p.Lines
		}
	}

	logPath := filepath.Join(d.dataDir, "log", "daemon.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{"lines": []string{}}, nil
		}
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
	}

	lines := splitLines(string(data))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return map[string]interface{}{"lines": lines}, nil
}

func (d *daemon) handleVersion(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	return map[string]string{
		"version": version,
		"go":      "1.22+",
	}, nil
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
