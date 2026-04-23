package main

import (
	"encoding/json"
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

	"github.com/privstack/daemon/internal/config"
	"github.com/privstack/daemon/internal/core"
	"github.com/privstack/daemon/internal/health"
	"github.com/privstack/daemon/internal/ipc"
	"github.com/privstack/daemon/internal/rescue"
	"github.com/privstack/daemon/internal/runtimev2"
	"github.com/privstack/daemon/internal/updater"
	"github.com/privstack/daemon/internal/watcher"
)

var Version = "1.6.0"

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
	runtimeV2  *runtimev2.Orchestrator

	mu         sync.Mutex // protects cfg
	metricsMu  sync.Mutex
	traffic    trafficSnapshot
	latency    latencySnapshot
	egress     egressSnapshot
	healthKick time.Time
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
		cfg:        cfg,
		cfgPath:    *cfgPath,
		dataDir:    *dataDir,
		coreMgr:    coreMgr,
		healthMon:  healthMon,
		rescueMgr:  rescueMgr,
		netWatcher: netWatcher,
		ipcServer:  ipc.NewServer(socketPath),
	}
	d.initRuntimeV2()

	d.healthMon.SetOnDegraded(func() {
		if err := d.rescueMgr.Attempt(); err != nil {
			log.Printf("rescue attempt failed: %v", err)
			d.mu.Lock()
			maxAttempts := d.cfg.Rescue.MaxAttempts
			d.mu.Unlock()
			if maxAttempts < 1 {
				maxAttempts = 1
			}
			if d.rescueMgr.Attempts() >= maxAttempts {
				if rollbackErr := d.rescueMgr.Rollback(); rollbackErr != nil {
					log.Printf("rescue rollback failed: %v", rollbackErr)
				}
			}
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

	if cfg.Autostart {
		log.Printf("autostart enabled, starting proxy")
		profile := cfg.ResolveProfile()
		if err := d.coreMgr.Start(profile); err != nil {
			log.Printf("autostart failed: %v", err)
		} else {
			// Proxy is running -- start the supporting subsystems.
			d.startSubsystems()
			d.syncRuntimeV2DesiredState()
			d.runtimeV2.RefreshHealth()
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
	if cfg.Health.Enabled && cfg.Health.IntervalSec > 0 {
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

	if err := d.applyConfig(newCfg, true); err != nil {
		log.Printf("reload config apply failed: %v", err)
		return
	}

	log.Printf("config reloaded")
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

// --------------------------------------------------------------------------
// IPC handler registration
// --------------------------------------------------------------------------

func (d *daemon) registerHandlers() {
	d.ipcServer.Register("backend.status", d.handleBackendStatus)
	d.ipcServer.Register("backend.start", d.handleBackendStart)
	d.ipcServer.Register("backend.stop", d.handleBackendStop)
	d.ipcServer.Register("backend.restart", d.handleBackendRestart)
	d.ipcServer.Register("backend.reset", d.handleBackendReset)
	d.ipcServer.Register("backend.applyDesiredState", d.handleBackendApplyDesiredState)
	d.ipcServer.Register("diagnostics.health", d.handleDiagnosticsHealth)
	d.ipcServer.Register("diagnostics.testNodes", d.handleDiagnosticsTestNodes)
	d.ipcServer.Register("status", d.handleStatus)
	d.ipcServer.Register("start", d.handleStart)
	d.ipcServer.Register("stop", d.handleStop)
	d.ipcServer.Register("reload", d.handleReload)
	d.ipcServer.Register("network-reset", d.handleNetworkReset)
	d.ipcServer.Register("health", d.handleHealth)
	d.ipcServer.Register("audit", d.handleAudit)
	d.ipcServer.Register("app.list", d.handleAppList)
	d.ipcServer.Register("app.resolveUid", d.handleResolveUID)
	d.ipcServer.Register("config-get", d.handleConfigGet)
	d.ipcServer.Register("config-set", d.handleConfigSet)
	d.ipcServer.Register("config-set-many", d.handleConfigSetMany)
	d.ipcServer.Register("config-list", d.handleConfigList)
	d.ipcServer.Register("config-import", d.handleConfigImport)
	d.ipcServer.Register("subscription-fetch", d.handleSubscriptionFetch)
	d.ipcServer.Register("node-test", d.handleNodeTest)
	d.ipcServer.Register("logs", d.handleLogs)
	d.ipcServer.Register("version", d.handleVersion)
	d.ipcServer.Register("update-check", d.handleUpdateCheck)
	d.ipcServer.Register("update-download", d.handleUpdateDownload)
	d.ipcServer.Register("update-install", d.handleUpdateInstall)
}

// --------------------------------------------------------------------------
// IPC handlers
// --------------------------------------------------------------------------

func (d *daemon) handleStatus(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	status := d.coreMgr.Status()
	healthResult := d.healthMon.LastResult()
	state := d.coreMgr.GetState()
	if state == core.StateRunning || state == core.StateDegraded {
		if d.shouldKickAsyncHealth(state, healthResult) {
			go d.healthMon.RunOnce()
		}
	}
	return d.buildStatusPayload(status, healthResult), nil
}

func (d *daemon) shouldKickAsyncHealth(state core.State, healthResult *health.HealthResult) bool {
	if state != core.StateRunning && state != core.StateDegraded {
		return false
	}
	now := time.Now()
	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()

	if healthResult != nil && now.Sub(healthResult.Timestamp) <= 10*time.Second {
		return false
	}
	if !d.healthKick.IsZero() && now.Sub(d.healthKick) < 10*time.Second {
		return false
	}
	d.healthKick = now
	return true
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
	hasPanelNodes := len(d.cfg.Panel.Nodes) > 0
	d.mu.Unlock()

	if profile.Address == "" && !hasPanelNodes {
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

func (d *daemon) handleNetworkReset(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if d.runtimeV2 == nil {
		return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: "v2 runtime is not initialized"}
	}
	return d.runtimeV2.Reset(), nil
}

func (d *daemon) handleHealth(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	// Run a one-shot health check via the real HealthMonitor.
	healthResult := d.healthMon.RunOnce()
	status := d.coreMgr.Status()
	return d.buildStatusPayload(status, healthResult), nil
}

func (d *daemon) handleAudit(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	healthResult := d.healthMon.RunOnce()

	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	findings := make([]map[string]interface{}, 0)
	appendFinding := func(
		code string,
		title string,
		description string,
		severity string,
		category string,
		recommendation string,
		affected string,
	) {
		finding := map[string]interface{}{
			"code":           code,
			"title":          title,
			"description":    description,
			"severity":       severity,
			"category":       category,
			"recommendation": recommendation,
		}
		if affected != "" {
			finding["affectedResource"] = affected
		}
		findings = append(findings, finding)
	}

	if cfg.Node.Address == "" {
		appendFinding(
			"NODE_NOT_CONFIGURED",
			"Активный сервер не настроен",
			"У демона не указан адрес upstream-сервера, поэтому соединение не может быть установлено.",
			"CRITICAL",
			"CONFIG",
			"Импортируйте или выберите корректный сервер перед подключением.",
			"node.address",
		)
	}

	if cfg.DNS.ProxyDNS == "" {
		appendFinding(
			"PROXY_DNS_EMPTY",
			"Proxy DNS не задан",
			"Не настроен адрес proxy DNS, из-за чего возможны ошибки резолвинга или утечки.",
			"HIGH",
			"DNS",
			"Укажите корректный DoH-адрес для proxy DNS.",
			"dns.proxy_dns",
		)
	}

	if cfg.DNS.DirectDNS == "" {
		appendFinding(
			"DIRECT_DNS_EMPTY",
			"Direct DNS не задан",
			"Не настроен адрес direct DNS для трафика в обход.",
			"MEDIUM",
			"DNS",
			"Укажите корректный DoH-адрес для direct DNS.",
			"dns.direct_dns",
		)
	}

	transportSecurity := ""
	if cfg.Transport.Protocol == "reality" || cfg.Node.RealityPublicKey != "" {
		transportSecurity = "reality"
	} else if cfg.Transport.TLSServer != "" {
		transportSecurity = "tls"
	}
	if (cfg.Node.Protocol == "vless" || cfg.Node.Protocol == "vmess") && transportSecurity == "" {
		appendFinding(
			"TRANSPORT_NOT_ENCRYPTED",
			"Защита транспорта не включена",
			"VLESS или VMess настроен без TLS или REALITY, что ослабляет приватность транспорта.",
			"MEDIUM",
			"ENCRYPTION",
			"Включите TLS или REALITY для активного сервера.",
			"transport",
		)
	}

	if cfg.Apps.Mode == "all" {
		appendFinding(
			"PER_APP_ROUTING_DISABLED",
			"Маршрутизация по приложениям отключена",
			"Все приложения маршрутизируются одинаково, что может повышать риск для чувствительных программ.",
			"INFO",
			"ROUTING",
			"Если нужна изоляция по приложениям, используйте белый список или исключения.",
			"apps.mode",
		)
	}

	if cfg.Routing.Mode == "direct" && cfg.Apps.Mode != "off" {
		appendFinding(
			"DIRECT_MODE_NOT_HARD_BYPASS",
			"Прямой режим не является жёстким обходом",
			"Маршрутизация переведена в direct, но apps.mode всё ещё позволяет помечать трафик для перехвата.",
			"HIGH",
			"ROUTING",
			"Для direct-режима установите apps.mode = off, чтобы отключить iptables и DNS-перехват.",
			"apps.mode",
		)
	}

	for _, path := range []string{
		d.cfgPath,
		filepath.Join(d.dataDir, "config", "rendered", "singbox.json"),
		filepath.Join(d.dataDir, "logs", "privd.log"),
		filepath.Join(d.dataDir, "logs", "sing-box.log"),
	} {
		if pathHasGroupOrWorldBits(path) {
			appendFinding(
				"SENSITIVE_FILE_PERMISSIONS",
				"Чувствительный файл читается вне root",
				"Файлы конфигурации или логов могут раскрывать адреса proxy, учётные данные или runtime-диагностику.",
				"MEDIUM",
				"CONFIG",
				"Установите для конфигов и логов права 0600, а для их директорий оставьте доступ только root.",
				path,
			)
			break
		}
	}

	status := d.coreMgr.Status()
	if status.State == core.StateRunning.String() || status.State == core.StateDegraded.String() {
		if !localPortProtectionPresent(cfg) {
			appendFinding(
				"LOCAL_PORT_PROTECTION_MISSING",
				"Локальные порты PrivStack защищены не полностью",
				"Обычные приложения могут получить доступ к TPROXY-, DNS- или управляющим портам.",
				"HIGH",
				"LEAK",
				"Повторно примените правила iptables и проверьте DROP-правила для портов TPROXY, DNS и API.",
				"iptables mangle PRIVSTACK_OUT",
			)
		}
	}

	for name, check := range healthResult.Checks {
		if check.Pass {
			continue
		}

		category := "SYSTEM"
		severity := "HIGH"
		switch name {
		case "dns":
			category = "DNS"
			severity = "HIGH"
		case "iptables", "routing":
			category = "ROUTING"
			severity = "HIGH"
		case "tproxy_port", "singbox_alive":
			category = "SYSTEM"
			severity = "CRITICAL"
		}

		appendFinding(
			"HEALTH_"+strings.ToUpper(strings.ReplaceAll(name, "-", "_")),
			fmt.Sprintf("Проверка состояния не пройдена: %s", name),
			check.Detail,
			severity,
			category,
			"Устраните проблему в состоянии демона и повторите аудит.",
			name,
		)
	}

	summary := map[string]int{
		"critical": 0,
		"high":     0,
		"medium":   0,
		"low":      0,
		"info":     0,
	}
	score := 100
	for _, finding := range findings {
		switch finding["severity"] {
		case "CRITICAL":
			summary["critical"]++
			score -= 35
		case "HIGH":
			summary["high"]++
			score -= 20
		case "MEDIUM":
			summary["medium"]++
			score -= 10
		case "LOW":
			summary["low"]++
			score -= 5
		default:
			summary["info"]++
			score -= 1
		}
	}
	if score < 0 {
		score = 0
	}

	report := map[string]interface{}{
		"auditId":   fmt.Sprintf("audit-%d", time.Now().UnixMilli()),
		"timestamp": time.Now().UnixMilli(),
		"score":     score,
		"findings":  findings,
		"summary":   summary,
	}
	return report, nil
}

func (d *daemon) handleAppList(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	apps, err := loadInstalledApps()
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "load apps failed: " + err.Error(),
		}
	}
	return apps, nil
}

func (d *daemon) handleResolveUID(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: {\"uid\": 12345}",
		}
	}

	var p struct {
		UID int `json:"uid"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}
	if p.UID <= 0 {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "uid must be > 0",
		}
	}

	apps, err := loadInstalledApps()
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "load apps failed: " + err.Error(),
		}
	}

	var fallback *daemonAppInfo
	for _, app := range apps {
		if app.UID == p.UID {
			return app, nil
		}
		if fallback == nil && app.UID%100000 == p.UID%100000 {
			appCopy := app
			fallback = &appCopy
		}
	}
	if fallback != nil {
		return *fallback, nil
	}

	return nil, &ipc.RPCError{
		Code:    ipc.CodeInvalidParams,
		Message: fmt.Sprintf("no package found for uid %d", p.UID),
	}
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
	data, _ := json.Marshal(d.cfg)
	d.mu.Unlock()
	var full map[string]json.RawMessage
	json.Unmarshal(data, &full)

	full[p.Key] = p.Value

	newCfg, rpcErr := d.buildPatchedConfig(full)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if err := d.applyConfig(newCfg, false); err != nil {
		return nil, d.configApplyRPCError(err)
	}
	return map[string]string{"status": "ok"}, nil
}

func (d *daemon) handleConfigSetMany(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: {\"values\": {...}, \"reload\": true|false}",
		}
	}

	var p struct {
		Values map[string]json.RawMessage `json:"values"`
		Reload bool                       `json:"reload"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}
	if len(p.Values) == 0 {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "values must not be empty",
		}
	}

	d.mu.Lock()
	data, _ := json.Marshal(d.cfg)
	d.mu.Unlock()
	var full map[string]json.RawMessage
	json.Unmarshal(data, &full)

	for key, value := range p.Values {
		full[key] = value
	}

	newCfg, rpcErr := d.buildPatchedConfig(full)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if err := d.applyConfig(newCfg, p.Reload); err != nil {
		return nil, d.configApplyRPCError(err)
	}

	return map[string]interface{}{
		"status":  "ok",
		"reload":  p.Reload,
		"updated": len(p.Values),
	}, nil
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

	if err := d.applyConfig(newCfg, true); err != nil {
		return nil, d.configApplyRPCError(err)
	}

	return map[string]string{"status": "imported"}, nil
}

func (d *daemon) buildPatchedConfig(full map[string]json.RawMessage) (*config.Config, *ipc.RPCError) {
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

	return newCfg, nil
}

func (d *daemon) configApplyRPCError(err error) *ipc.RPCError {
	rpcErr := &ipc.RPCError{
		Code:    ipc.CodeInternalError,
		Message: err.Error(),
	}
	if strings.Contains(err.Error(), "config saved") {
		rpcErr.Data = map[string]interface{}{
			"config_saved": true,
		}
	}
	return rpcErr
}

func (d *daemon) applyConfig(newCfg *config.Config, reload bool) error {
	wasRunning := d.coreMgr.GetState() == core.StateRunning ||
		d.coreMgr.GetState() == core.StateDegraded

	if err := newCfg.Save(d.cfgPath); err != nil {
		return fmt.Errorf("persist config: %w", err)
	}

	d.mu.Lock()
	d.cfg = newCfg
	d.mu.Unlock()

	d.coreMgr.SetConfig(newCfg)
	d.rescueMgr.SetConfig(newCfg)
	healthInterval := time.Duration(newCfg.Health.IntervalSec) * time.Second
	if healthInterval <= 0 {
		healthInterval = 30 * time.Second
	}
	healthTimeout := time.Duration(newCfg.Health.TimeoutSec) * time.Second
	if healthTimeout <= 0 {
		healthTimeout = 5 * time.Second
	}
	healthThreshold := newCfg.Health.Threshold
	if healthThreshold < 1 {
		healthThreshold = 3
	}
	tproxyPort := newCfg.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	dnsPort := newCfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	routeMark := newCfg.Proxy.Mark
	if routeMark == 0 {
		routeMark = 0x2023
	}
	d.healthMon.SetConfig(healthInterval, healthThreshold, tproxyPort, dnsPort, routeMark, newCfg.Health.URL, healthTimeout)
	d.netWatcher.SetEnv(buildScriptEnv(newCfg, d.dataDir))
	d.syncRuntimeV2DesiredState()

	if reload && wasRunning {
		d.stopSubsystems()
		profile := newCfg.ResolveProfile()
		if err := d.coreMgr.HotSwap(profile); err != nil {
			_ = d.resetNetworkState(newCfg)
			return fmt.Errorf("apply config hot-swap failed; config saved, runtime stopped for safety: %w", err)
		}
		if err := d.reapplyRuntimeRules(newCfg); err != nil {
			_ = d.resetNetworkState(newCfg)
			return fmt.Errorf("apply config rules failed; config saved, runtime stopped for safety: %w", err)
		}
		d.rescueMgr.Reset()
		d.startSubsystems()
		d.runtimeV2.RefreshHealth()
	}

	return nil
}

func (d *daemon) resetNetworkState(cfg *config.Config) []string {
	var errors []string
	env := buildScriptEnv(cfg, d.dataDir)

	dnsScript := filepath.Join(d.dataDir, "scripts", "dns.sh")
	if err := core.ExecScript(dnsScript, "stop", env); err != nil {
		errors = append(errors, "dns stop: "+err.Error())
	}

	iptablesScript := filepath.Join(d.dataDir, "scripts", "iptables.sh")
	if err := core.ExecScript(iptablesScript, "stop", env); err != nil {
		errors = append(errors, "iptables stop: "+err.Error())
	}

	_, _ = core.ExecCommand("killall", "-TERM", "sing-box")
	_, _ = core.ExecCommand("killall", "-KILL", "sing-box")
	d.rescueMgr.Reset()
	d.coreMgr.SetState(core.StateStopped)
	d.resetRuntimeMetrics()
	return errors
}

func (d *daemon) reapplyRuntimeRules(cfg *config.Config) error {
	env := buildScriptEnv(cfg, d.dataDir)
	iptablesScript := filepath.Join(d.dataDir, "scripts", "iptables.sh")
	dnsScript := filepath.Join(d.dataDir, "scripts", "dns.sh")

	_ = core.ExecScript(dnsScript, "stop", env)
	_ = core.ExecScript(iptablesScript, "stop", env)

	if err := core.ExecScript(iptablesScript, "start", env); err != nil {
		return fmt.Errorf("iptables start: %w", err)
	}
	if err := core.ExecScript(dnsScript, "start", env); err != nil {
		_ = core.ExecScript(iptablesScript, "stop", env)
		return fmt.Errorf("dns start: %w", err)
	}
	return nil
}

func buildScriptEnv(cfg *config.Config, dataDir string) map[string]string {
	gid := cfg.Proxy.GID
	if gid == 0 {
		gid = 23333
	}
	mark := cfg.Proxy.Mark
	if mark == 0 {
		mark = 0x2023
	}
	tproxyPort := cfg.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	dnsPort := cfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	apiPort := cfg.Proxy.APIPort
	if apiPort == 0 {
		apiPort = 9090
	}
	panelInbounds := cfg.ResolvePanelInbounds()
	appMode := core.MapAppMode(cfg.Apps.Mode)
	dnsMode := "all"
	if appMode == "off" {
		dnsMode = "off"
	} else if appMode == "whitelist" || appMode == "blacklist" {
		dnsMode = "per_uid"
	}
	appUIDs := core.ResolvePackageUIDs(cfg.Apps.Packages)
	bypassUIDs := core.BuildBypassUIDs(cfg.Routing.AlwaysDirectApps)

	return map[string]string{
		"PRIVSTACK_DIR":  dataDir,
		"CORE_GID":       strconv.Itoa(gid),
		"TPROXY_PORT":    strconv.Itoa(tproxyPort),
		"DNS_PORT":       strconv.Itoa(dnsPort),
		"API_PORT":       strconv.Itoa(apiPort),
		"HTTP_PORT":      strconv.Itoa(panelInbounds.HTTPPort),
		"FWMARK":         fmt.Sprintf("0x%x", mark),
		"ROUTE_TABLE":    "2023",
		"ROUTE_TABLE_V6": "2024",
		"APP_MODE":       appMode,
		"APP_UIDS":       appUIDs,
		"BYPASS_UIDS":    bypassUIDs,
		"DNS_MODE":       dnsMode,
		"PROXY_MODE":     "tproxy",
	}
}

func pathHasGroupOrWorldBits(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0077 != 0
}

func localPortProtectionPresent(cfg *config.Config) bool {
	panelInbounds := cfg.ResolvePanelInbounds()
	ports := []int{cfg.Proxy.TProxyPort, cfg.Proxy.DNSPort, cfg.Proxy.APIPort, panelInbounds.HTTPPort}
	if ports[0] == 0 {
		ports[0] = 10853
	}
	if ports[1] == 0 {
		ports[1] = 10856
	}
	if ports[2] == 0 {
		ports[2] = 9090
	}
	if ports[3] == 0 {
		ports[3] = 10809
	}

	v4, err4 := core.ExecCommand("iptables", "-w", "100", "-t", "mangle", "-S", "PRIVSTACK_OUT")
	v6, err6 := core.ExecCommand("ip6tables", "-w", "100", "-t", "mangle", "-S", "PRIVSTACK_OUT")
	if err4 != nil || err6 != nil {
		return false
	}
	for _, port := range ports {
		if !portProtectionOutputContains(v4, port) || !portProtectionOutputContains(v6, port) {
			return false
		}
	}
	return true
}

func portProtectionOutputContains(output string, port int) bool {
	portText := fmt.Sprintf("--dport %d", port)
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, portText) &&
			strings.Contains(line, "--uid-owner 0") &&
			strings.Contains(line, "--gid-owner") &&
			strings.Contains(line, "-j DROP") {
			return true
		}
	}
	return false
}

func (d *daemon) handleSubscriptionFetch(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: {\"url\": \"https://...\"}",
		}
	}

	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}
	if p.URL == "" {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "url is required",
		}
	}

	req, err := http.NewRequest(http.MethodGet, p.URL, nil)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "invalid URL: " + err.Error(),
		}
	}
	req.Header.Set("User-Agent", "RKNnoVPN-subscription-fetch/1.0")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "subscription fetch failed: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "subscription read failed: " + err.Error(),
		}
	}

	headers := make(map[string]string, len(resp.Header))
	for key, values := range resp.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: fmt.Sprintf("subscription fetch returned HTTP %d", resp.StatusCode),
			Data: map[string]interface{}{
				"status":  resp.StatusCode,
				"headers": headers,
			},
		}
	}

	return map[string]interface{}{
		"status":  resp.StatusCode,
		"body":    string(body),
		"headers": headers,
	}, nil
}

func (d *daemon) handleNodeTest(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	var p struct {
		NodeIDs   []string `json:"node_ids"`
		URL       string   `json:"url"`
		TimeoutMS int      `json:"timeout_ms"`
	}
	if params != nil {
		if err := json.Unmarshal(*params, &p); err != nil {
			return nil, &ipc.RPCError{
				Code:    ipc.CodeInvalidParams,
				Message: "invalid params: " + err.Error(),
			}
		}
	}
	if p.TimeoutMS <= 0 {
		p.TimeoutMS = 5000
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	requested := make(map[string]bool, len(p.NodeIDs))
	for _, id := range p.NodeIDs {
		requested[id] = true
	}

	d.mu.Lock()
	cfg := d.cfg
	profiles := config.ProfilesFromPanelNodes(cfg)
	if len(profiles) == 0 {
		profile := cfg.ResolveProfile()
		if profile.Address != "" {
			profile.Tag = "proxy"
			profiles = []*config.NodeProfile{profile}
		}
	}
	apiPort := cfg.Proxy.APIPort
	if apiPort == 0 {
		apiPort = 9090
	}
	testURL := strings.TrimSpace(p.URL)
	if testURL == "" {
		testURL = strings.TrimSpace(cfg.Health.URL)
	}
	d.mu.Unlock()
	if testURL == "" {
		testURL = "https://www.gstatic.com/generate_204"
	}

	results := make([]map[string]interface{}, 0, len(profiles))
	for _, profile := range profiles {
		if len(requested) > 0 && !requested[profile.ID] {
			continue
		}
		result := map[string]interface{}{
			"id":       profile.ID,
			"name":     firstNonEmpty(profile.Name, profile.Tag, profile.Address),
			"tag":      profile.Tag,
			"server":   profile.Address,
			"port":     profile.Port,
			"protocol": profile.Protocol,
		}

		tcpMS, tcpErr := testTCPConnect(profile.Address, profile.Port, timeout)
		if tcpErr != nil {
			result["tcp_error"] = tcpErr.Error()
		} else {
			result["tcp_ms"] = tcpMS
		}

		urlMS, statusCode, urlErr := testClashDelay(apiPort, profile.Tag, testURL, p.TimeoutMS)
		if urlErr != nil {
			result["url_error"] = urlErr.Error()
		} else {
			result["url_ms"] = urlMS
			result["status"] = statusCode
		}
		results = append(results, result)
	}

	return map[string]interface{}{
		"url":     testURL,
		"results": results,
	}, nil
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type daemonAppInfo struct {
	PackageName string  `json:"packageName"`
	AppName     string  `json:"appName"`
	UID         int     `json:"uid"`
	IsSystemApp bool    `json:"isSystemApp"`
	Category    string  `json:"category"`
	ApkPath     *string `json:"apkPath,omitempty"`
	VersionName *string `json:"versionName,omitempty"`
	Enabled     bool    `json:"enabled"`
}

func loadInstalledApps() ([]daemonAppInfo, error) {
	data, err := os.ReadFile("/data/system/packages.list")
	if err != nil {
		return nil, fmt.Errorf("read packages.list: %w", err)
	}

	apps := make([]daemonAppInfo, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		uid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		dataDir := ""
		if len(fields) >= 4 {
			dataDir = fields[3]
		}

		appName := prettyPackageLabel(fields[0])

		isSystem := strings.HasPrefix(dataDir, "/system/") ||
			strings.HasPrefix(dataDir, "/vendor/") ||
			strings.HasPrefix(dataDir, "/product/") ||
			strings.HasPrefix(dataDir, "/system_ext/")

		category := classifyDaemonApp(fields[0], isSystem)

		apps = append(apps, daemonAppInfo{
			PackageName: fields[0],
			AppName:     appName,
			UID:         uid,
			IsSystemApp: isSystem,
			Category:    category,
			Enabled:     true,
		})
	}

	return apps, nil
}

func prettyPackageLabel(packageName string) string {
	last := packageName
	if idx := strings.LastIndex(packageName, "."); idx != -1 && idx+1 < len(packageName) {
		last = packageName[idx+1:]
	}
	last = strings.ReplaceAll(last, "_", " ")
	last = strings.ReplaceAll(last, "-", " ")
	if last == "" {
		return packageName
	}
	return strings.ToUpper(last[:1]) + last[1:]
}

func classifyDaemonApp(packageName string, isSystem bool) string {
	if isSystem {
		return "SYSTEM"
	}

	lower := strings.ToLower(packageName)
	switch {
	case strings.Contains(lower, "telegram"),
		strings.Contains(lower, "whatsapp"),
		strings.Contains(lower, "discord"),
		strings.Contains(lower, "signal"),
		strings.Contains(lower, "messenger"):
		return "MESSAGING"
	case strings.Contains(lower, "youtube"),
		strings.Contains(lower, "netflix"),
		strings.Contains(lower, "twitch"),
		strings.Contains(lower, "video"):
		return "VIDEO"
	case strings.Contains(lower, "spotify"),
		strings.Contains(lower, "music"),
		strings.Contains(lower, "audio"):
		return "AUDIO"
	case strings.Contains(lower, "chrome"),
		strings.Contains(lower, "firefox"),
		strings.Contains(lower, "browser"),
		strings.Contains(lower, "opera"),
		strings.Contains(lower, "brave"):
		return "BROWSER"
	case strings.Contains(lower, "game"):
		return "GAME"
	case strings.Contains(lower, "bank"),
		strings.Contains(lower, "wallet"),
		strings.Contains(lower, "finance"),
		strings.Contains(lower, "sber"),
		strings.Contains(lower, "tinkoff"):
		return "PRODUCTIVITY"
	case strings.Contains(lower, "social"),
		strings.Contains(lower, "twitter"),
		strings.Contains(lower, "instagram"),
		strings.Contains(lower, "reddit"),
		strings.Contains(lower, "facebook"),
		strings.Contains(lower, "vk"):
		return "SOCIAL"
	default:
		return "OTHER"
	}
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

	logPaths := []string{
		filepath.Join(d.dataDir, "logs", "privd.log"),
		filepath.Join(d.dataDir, "logs", "daemon.log"),
		filepath.Join(d.dataDir, "log", "daemon.log"),
	}

	var (
		data []byte
		err  error
	)
	for _, logPath := range logPaths {
		data, err = os.ReadFile(logPath)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return nil, &ipc.RPCError{Code: ipc.CodeInternalError, Message: err.Error()}
		}
	}
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
		"daemon":  Version,
		"core":    Version,
		"privctl": Version,
		// Keep legacy keys for backward compatibility.
		"version": Version,
		"go":      "1.22+",
	}, nil
}

// --------------------------------------------------------------------------
// Update handlers
// --------------------------------------------------------------------------

func (d *daemon) handleUpdateCheck(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	info, err := updater.CheckForUpdate("v" + Version)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "update check failed: " + err.Error(),
		}
	}
	return info, nil
}

func (d *daemon) handleUpdateDownload(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	// First, run a check to get the download URLs.
	info, err := updater.CheckForUpdate("v" + Version)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "update check failed: " + err.Error(),
		}
	}

	if !info.HasUpdate {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "no update available",
		}
	}

	destDir := filepath.Join(d.dataDir, "update")
	downloaded, err := updater.DownloadUpdate(info, destDir, func(downloaded, total int64) {
		log.Printf("[updater] download progress: %d / %d bytes", downloaded, total)
	})
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "download failed: " + err.Error(),
		}
	}

	return downloaded, nil
}

func (d *daemon) handleUpdateInstall(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	// Parse optional params.
	var p struct {
		ModulePath string `json:"module_path"`
		ApkPath    string `json:"apk_path"`
	}
	if params != nil {
		_ = json.Unmarshal(*params, &p)
	}

	// Default paths if not specified.
	updateDir := filepath.Join(d.dataDir, "update")
	if p.ModulePath == "" {
		p.ModulePath = filepath.Join(updateDir, "module.zip")
	}
	if p.ApkPath == "" {
		p.ApkPath = filepath.Join(updateDir, "panel.apk")
	}

	result := map[string]interface{}{
		"module_installed": false,
		"apk_installed":    false,
	}

	moduleExists := false
	if _, err := os.Stat(p.ModulePath); err == nil {
		moduleExists = true
	}
	apkExists := false
	if _, err := os.Stat(p.ApkPath); err == nil {
		apkExists = true
	}
	if !moduleExists && !apkExists {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "no downloaded update files found",
		}
	}

	wasRunning := d.coreMgr.GetState() == core.StateRunning ||
		d.coreMgr.GetState() == core.StateDegraded

	if moduleExists {
		// Stop subsystems before module update only when we are replacing the
		// daemon/module itself. APK-only installs should not disrupt traffic.
		d.stopSubsystems()
		if err := d.coreMgr.Stop(); err != nil {
			log.Printf("[updater] warning: failed to stop core: %v", err)
		}
	}

	moduleUpdated := false

	// Install module (binaries + scripts + module files).
	if moduleExists {
		moduleDir := "/data/adb/modules/privstack"
		if err := updater.InstallModuleUpdate(p.ModulePath, d.dataDir, moduleDir); err != nil {
			if wasRunning {
				d.restoreCurrentRuntimeAfterFailedUpdate()
			}
			return nil, &ipc.RPCError{
				Code:    ipc.CodeInternalError,
				Message: "module install failed: " + err.Error(),
			}
		}
		result["module_installed"] = true
		moduleUpdated = true
	}

	// Install APK.
	if apkExists {
		if err := updater.InstallApkUpdate(p.ApkPath); err != nil {
			log.Printf("[updater] APK install failed: %v", err)
			result["apk_error"] = err.Error()
		} else {
			result["apk_installed"] = true
		}
	}

	// Clean up downloaded files.
	os.RemoveAll(updateDir)

	result["status"] = "installed"

	// If we installed a new module (which includes a new privd binary),
	// the new daemon is already forked and listening. Schedule this old
	// daemon to exit after the IPC response has been written back to the
	// client, so we don't cut the connection mid-reply.
	if moduleUpdated {
		go updater.ScheduleSelfExit(updater.SelfExitDelay)
	}

	return result, nil
}

func (d *daemon) restoreCurrentRuntimeAfterFailedUpdate() {
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()

	profile := cfg.ResolveProfile()
	if profile.Address == "" {
		return
	}
	if err := d.coreMgr.Start(profile); err != nil {
		log.Printf("[updater] warning: failed to restore previous runtime after failed update: %v", err)
		return
	}
	d.rescueMgr.Reset()
	d.startSubsystems()
}

func (d *daemon) buildStatusPayload(status *core.StatusInfo, healthResult *health.HealthResult) map[string]interface{} {
	activeNodeID, activeNodeName, activeNodeProtocol := d.activePanelNode()
	if status == nil {
		status = &core.StatusInfo{}
	}
	state := d.coreMgr.GetState()
	d.mu.Lock()
	cfg := d.cfg
	d.mu.Unlock()
	apiPort := cfg.Proxy.APIPort
	if apiPort == 0 {
		apiPort = 9090
	}
	panelInbounds := cfg.ResolvePanelInbounds()
	traffic := d.buildTrafficPayload(state, apiPort)
	latencyMs := d.cachedLatencyMs(state, cfg, apiPort)
	egressIP := d.cachedEgressIP(state, panelInbounds.HTTPPort)

	return map[string]interface{}{
		"state":                mapCoreStateToConnectionState(status.State),
		"active_node_id":       activeNodeID,
		"active_node_name":     activeNodeName,
		"active_node_protocol": activeNodeProtocol,
		"egress_ip":            egressIP,
		"country_flag":         nil,
		"uptime":               uptimeSeconds(status.StartedAt),
		"traffic":              traffic,
		"latency_ms":           latencyMs,
		"health":               buildHealthPayload(state, healthResult),

		// Keep the legacy fields for older clients and debugging tools.
		"pid":             status.PID,
		"active_profile":  status.ActiveProfile,
		"started_at":      status.StartedAt,
		"uptime_legacy":   status.Uptime,
		"health_fails":    d.healthMon.Failures(),
		"rescue_attempts": d.rescueMgr.Attempts(),
	}
}

func (d *daemon) activePanelNode() (string, string, string) {
	d.mu.Lock()
	panel := d.cfg.Panel
	d.mu.Unlock()

	activeID := panel.ActiveNodeID
	type nodeMeta struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
	}
	for _, raw := range panel.Nodes {
		var node nodeMeta
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		if node.ID == activeID {
			return node.ID, node.Name, node.Protocol
		}
	}

	if len(panel.Nodes) > 0 {
		var first nodeMeta
		if err := json.Unmarshal(panel.Nodes[0], &first); err == nil {
			return first.ID, first.Name, first.Protocol
		}
	}

	return activeID, "", ""
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
	if state != core.StateRunning && state != core.StateDegraded {
		return nil
	}

	now := time.Now()
	d.metricsMu.Lock()
	if d.latency.Valid && now.Sub(d.latency.CheckedAt) < 30*time.Second {
		value := d.latency.Ms
		d.metricsMu.Unlock()
		return &value
	}
	if !d.latency.Valid && !d.latency.CheckedAt.IsZero() && now.Sub(d.latency.CheckedAt) < 10*time.Second {
		d.metricsMu.Unlock()
		return nil
	}
	d.metricsMu.Unlock()

	testURL := cfg.Health.URL
	if testURL == "" {
		testURL = "https://www.gstatic.com/generate_204"
	}
	latency, _, err := testClashDelay(apiPort, "proxy", testURL, 2500)

	d.metricsMu.Lock()
	defer d.metricsMu.Unlock()
	d.latency.CheckedAt = now
	if err != nil {
		d.latency.Valid = false
		return nil
	}
	d.latency.Valid = true
	d.latency.Ms = latency
	value := latency
	return &value
}

func (d *daemon) cachedEgressIP(state core.State, httpPort int) *string {
	if state != core.StateRunning && state != core.StateDegraded {
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
	if apiPort == 0 {
		apiPort = 9090
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
	if httpPort == 0 {
		httpPort = 10809
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

func mapCoreStateToConnectionState(state string) string {
	switch state {
	case "running":
		return "CONNECTED"
	case "starting", "stopping":
		return "CONNECTING"
	case "degraded", "rescue":
		return "ERROR"
	case "stopped":
		return "DISCONNECTED"
	default:
		return "UNKNOWN"
	}
}

func uptimeSeconds(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	return int64(time.Since(startedAt).Seconds())
}

func buildHealthPayload(state core.State, result *health.HealthResult) map[string]interface{} {
	payload := map[string]interface{}{
		"healthy":        false,
		"coreRunning":    state != core.StateStopped,
		"tunActive":      false,
		"dnsOperational": false,
		"lastError":      nil,
		"checkedAt":      int64(0),
	}
	if result == nil {
		return payload
	}

	payload["healthy"] = result.Overall
	payload["checkedAt"] = result.Timestamp.Unix()

	dnsOK := false
	tunOK := false
	var firstError string
	for name, check := range result.Checks {
		if name == "dns" {
			dnsOK = check.Pass
		}
		if name == "iptables" || name == "routing" || name == "tproxy_port" {
			if check.Pass {
				tunOK = true
			}
		}
		if !check.Pass && firstError == "" {
			firstError = fmt.Sprintf("%s: %s", name, check.Detail)
		}
	}

	payload["dnsOperational"] = dnsOK
	payload["tunActive"] = tunOK
	if firstError != "" {
		payload["lastError"] = firstError
	}
	return payload
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
