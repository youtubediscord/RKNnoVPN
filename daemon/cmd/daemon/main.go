package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/control"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/modulecontract"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/rescue"
	rootruntime "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtime/root"
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
	latency               latencySnapshot
	healthKick            time.Time
	lastReloadReport      core.RuntimeStageReport

	collectLeftoversOverride func(*config.Config) []string
}

type latencySnapshot = rootruntime.EgressProbeState

func main() {
	defaultPaths := modulecontract.NewPaths("")
	cfgPath := flag.String("config", filepath.Join(defaultPaths.ConfigDir(), "config.json"), "path to config.json")
	dataDir := flag.String("data-dir", defaultPaths.Dir(), "path to data directory")
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
	log.Printf("daemon %s starting", Version)

	// ---- PID file ----------------------------------------------------------

	if *pidFile != "" {
		if err := writePID(*pidFile); err != nil {
			log.Fatalf("write pid: %v", err)
		}
		defer os.Remove(*pidFile)
	}

	// ---- Data directories --------------------------------------------------

	for _, sub := range []string{"run", "data", "logs", "config/rendered", "backup"} {
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
	scriptEnv := rootruntime.BuildScriptEnv(cfg, *dataDir)
	var d *daemon
	netWatcher := watcher.NewNetworkWatcher(*dataDir, scriptEnv, func() error {
		if d == nil {
			return nil
		}
		_, err := d.runtimeV2.HandleNetworkChange()
		return err
	}, watchLogger)

	modulePaths := modulecontract.NewPaths(*dataDir)
	socketPath := modulePaths.DaemonSocket()

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

	if err := d.registerHandlers(); err != nil {
		log.Fatalf("ipc handler registration: %v", err)
	}
	if err := d.ipcServer.Start(); err != nil {
		log.Fatalf("ipc start: %v", err)
	}

	// ---- Autostart ---------------------------------------------------------

	if cfg.Autostart && fileMissing(modulePaths.ManualFlag()) {
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

	if err := d.applyConfigWithOperation(newCfg, true, runtimev2.OperationReload); err != nil {
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

func (d *daemon) registerHandlers() error {
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
		"diagnostics.report":        d.handleDiagnosticsReport,
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
	return control.RegisterContractHandlers(d.ipcServer, handlers)
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
