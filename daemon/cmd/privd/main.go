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
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/netstack"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/rescue"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/updater"
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

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	profilePath := profiledoc.Path(*cfgPath)
	profileDoc, profileFound, err := profiledoc.Load(profilePath)
	if err != nil {
		log.Fatalf("load profile: %v", err)
	}
	if profileFound {
		cfg, _, err = profiledoc.ApplyToConfig(cfg, profileDoc)
		if err != nil {
			log.Fatalf("apply profile: %v", err)
		}
	} else {
		profileDoc = profiledoc.FromConfig(cfg)
		if err := profiledoc.Save(profilePath, profileDoc); err != nil {
			log.Fatalf("migrate profile: %v", err)
		}
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
	newCfg, err := config.Load(d.cfgPath)
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
	d.ipcServer.Register("backend.status", d.handleBackendStatus)
	d.ipcServer.Register("backend.start", d.handleBackendStart)
	d.ipcServer.Register("backend.stop", d.handleBackendStop)
	d.ipcServer.Register("backend.restart", d.handleBackendRestart)
	d.ipcServer.Register("backend.reset", d.handleBackendReset)
	d.ipcServer.Register("backend.applyDesiredState", d.handleBackendApplyDesiredState)
	d.ipcServer.Register("diagnostics.health", d.handleDiagnosticsHealth)
	d.ipcServer.Register("diagnostics.testNodes", d.handleDiagnosticsTestNodes)
	d.ipcServer.Register("profile.get", d.handleProfileGet)
	d.ipcServer.Register("profile.apply", d.handleProfileApply)
	d.ipcServer.Register("profile.importNodes", d.handleProfileImportNodes)
	d.ipcServer.Register("profile.setActiveNode", d.handleProfileSetActiveNode)
	d.ipcServer.Register("subscription.preview", d.handleSubscriptionPreview)
	d.ipcServer.Register("subscription.refresh", d.handleSubscriptionRefresh)
	d.ipcServer.Register("audit", d.handleAudit)
	d.ipcServer.Register("app.list", d.handleAppList)
	d.ipcServer.Register("app.resolveUid", d.handleResolveUID)
	d.ipcServer.Register("config-list", d.handleConfigList)
	d.ipcServer.Register("config-import", d.handleConfigImport)
	d.ipcServer.Register("doctor", d.handleDoctor)
	d.ipcServer.Register("self-check", d.handleSelfCheck)
	d.ipcServer.Register("logs", d.handleLogs)
	d.ipcServer.Register("version", d.handleVersion)
	d.ipcServer.Register("update-check", d.handleUpdateCheck)
	d.ipcServer.Register("update-download", d.handleUpdateDownload)
	d.ipcServer.Register("update-install", d.handleUpdateInstall)
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
			"MEDIUM",
			"ROUTING",
			"Для privacy-by-design используйте whitelist/off по умолчанию и добавляйте приложения в proxy явно.",
			"apps.mode",
		)
	}

	if cfg.Proxy.APIPort > 0 {
		appendFinding(
			"LOCAL_CLASH_API_ENABLED",
			"Локальный Clash API включён",
			"В production-режиме лишний localhost API расширяет поверхность детекта и диагностики извне процесса.",
			"HIGH",
			"LEAK",
			"Оставьте proxy.api_port = 0, если URL-test по отдельным outbound не нужен для отладки.",
			"proxy.api_port",
		)
	}

	profileInbounds := cfg.ResolveProfileInbounds()
	if profileInbounds.HTTPPort > 0 || profileInbounds.SocksPort > 0 {
		appendFinding(
			"LOCAL_HELPER_INBOUND_ENABLED",
			"Локальный HTTP/SOCKS helper включён",
			"Даже localhost helper выглядит как обычный proxy listener и расширяет поверхность детекта.",
			"HIGH",
			"LEAK",
			"Оставьте HTTP/SOCKS helper ports равными 0 в production-режиме; для URL-test используйте core API только на время диагностики.",
			"panel.inbounds",
		)
	}
	if profileInbounds.AllowLAN && (profileInbounds.HTTPPort > 0 || profileInbounds.SocksPort > 0) {
		appendFinding(
			"LOCAL_HELPER_EXPOSED_ON_LAN",
			"Локальный helper inbound открыт за пределы localhost",
			"HTTP/SOCKS helper должен быть отключён или доступен только локально, иначе он похож на обычный proxy.",
			"HIGH",
			"LEAK",
			"Отключите helper inbound или установите allowLan = false.",
			"panel.inbounds",
		)
	}

	if port := firstVisibleLocalProxyPort(cfg); port > 0 {
		appendFinding(
			"LOCALHOST_PROXY_PORT_VISIBLE",
			"Локальный proxy/API port слушает на localhost",
			"Открытый localhost SOCKS/HTTP/API listener может быть найден scanner-приложениями и выглядит как proxy artifact.",
			"HIGH",
			"LEAK",
			"Отключите helper/API inbound или повторно выполните reset, если listener остался после остановки runtime.",
			fmt.Sprintf("127.0.0.1:%d", port),
		)
	}

	if linkOut, err := core.ExecCommand("ip", "link", "show"); err == nil {
		if line := firstVPNLikeInterfaceLine(splitLines(linkOut)); line != "" {
			appendFinding(
				"VPN_LIKE_INTERFACE_PRESENT",
				"Обнаружен VPN-похожий интерфейс",
				"Интерфейсы tun/wg/tap/ppp/ipsec являются прямым детектируемым признаком VPN-подобного стека.",
				"HIGH",
				"LEAK",
				"Не запускайте TUN/WireGuard-интерфейсы вместе с PrivStack; outbound должен жить внутри core.",
				line,
			)
		}
	}

	if proxyOut, err := core.ExecCommand("settings", "get", "global", "http_proxy"); err == nil {
		value := strings.TrimSpace(proxyOut)
		if value != "" && value != "null" && value != ":0" {
			appendFinding(
				"SYSTEM_HTTP_PROXY_SET",
				"Системный HTTP proxy задан",
				"Системный proxy виден обычным Android API и ломает no-proxy surface.",
				"HIGH",
				"LEAK",
				"Очистите global http_proxy и используйте только TPROXY/per-UID interception.",
				"settings global http_proxy="+value,
			)
		}
	}

	if connectivityOut, err := core.ExecCommand("dumpsys", "connectivity"); err == nil {
		if line := doctorFirstLoopbackDNSLine(splitLines(connectivityOut)); line != "" {
			appendFinding(
				"LOOPBACK_DNS_VISIBLE",
				"System LinkProperties показывает loopback DNS",
				"DNS-сервер 127.x или ::1 в LinkProperties виден обычным Android API и выглядит как proxy/VPN artifact.",
				"HIGH",
				"DNS",
				"Не меняйте системный DNS на loopback; используйте только per-UID DNS redirect на уровне iptables.",
				line,
			)
		}
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
				"Обычные приложения могут получить доступ к TPROXY-, DNS-, API-, SOCKS- или HTTP-helper портам.",
				"HIGH",
				"LEAK",
				"Повторно примените правила iptables и проверьте DROP-правила для TPROXY, DNS, API, SOCKS и HTTP-helper портов.",
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

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(*params, &raw); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "invalid config: " + err.Error(),
		}
	}
	if len(raw) == 0 {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: non-empty full config JSON object",
		}
	}
	for key := range raw {
		if !isFullConfigImportKey(key) {
			return nil, &ipc.RPCError{
				Code:    ipc.CodeInvalidParams,
				Message: fmt.Sprintf("unknown config import field %q; config-import expects a full daemon config object", key),
			}
		}
	}
	if _, ok := raw["schema_version"]; !ok {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "schema_version is required for full config import; use profile.apply for user intent updates",
		}
	}

	newCfg := config.DefaultConfig()
	if err := json.Unmarshal(*params, newCfg); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "invalid config: " + err.Error(),
		}
	}
	newCfg.Migrate()
	d.mu.Lock()
	newCfg.Profile = d.cfg.Profile
	d.mu.Unlock()

	if err := newCfg.Validate(); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeConfigError,
			Message: "validation failed: " + err.Error(),
		}
	}
	if profile := newCfg.ResolveProfile(); profile != nil && profile.Address != "" {
		if _, err := config.RenderSingboxConfig(newCfg, profile); err != nil {
			return nil, &ipc.RPCError{
				Code:    ipc.CodeConfigError,
				Message: "render validation failed: " + err.Error(),
			}
		}
	}

	if err := d.failIfRuntimeOperationActive(); err != nil {
		return nil, d.configApplyRPCError("config-import", err)
	}
	if err := profiledoc.Save(d.profilePath, profiledoc.FromConfig(newCfg)); err != nil {
		return nil, d.configApplyRPCError("config-import", fmt.Errorf("persist profile: %w", err))
	}
	profileSaved := true
	runtimeWasRunning := d.runtimeIsRunning()
	if err := d.applyConfig(newCfg, true); err != nil {
		return nil, d.configApplyRPCErrorSaved("config-import", err, profileSaved)
	}

	return d.configMutationSuccess("config-import", "imported", true, runtimeWasRunning, -1), nil
}

func isFullConfigImportKey(key string) bool {
	switch key {
	case "schema_version",
		"proxy",
		"transport",
		"node",
		"runtime_v2",
		"routing",
		"apps",
		"dns",
		"ipv6",
		"sharing",
		"health",
		"rescue",
		"autostart":
		return true
	default:
		return false
	}
}

func (d *daemon) configApplyRPCError(action string, err error) *ipc.RPCError {
	return d.configApplyRPCErrorSaved(action, err, configMutationWasSaved(err))
}

func (d *daemon) configApplyRPCErrorSaved(action string, err error, saved bool) *ipc.RPCError {
	var busy *runtimev2.OperationBusyError
	if errors.As(err, &busy) {
		rpcErr := d.rpcErrorFromRuntimeError(err)
		rpcErr.Data = d.configMutationErrorData(action, err, saved)
		rpcErr.Data.(map[string]interface{})["busy"] = busy.Data()
		return rpcErr
	}
	rpcErr := &ipc.RPCError{
		Code:    ipc.CodeInternalError,
		Message: err.Error(),
	}
	rpcErr.Data = d.configMutationErrorData(action, err, saved)
	return rpcErr
}

func (d *daemon) runtimeIsRunning() bool {
	state := d.coreMgr.GetState()
	return state == core.StateRunning || state == core.StateDegraded
}

func (d *daemon) configMutationSuccess(action string, status string, reload bool, runtimeWasRunning bool, updated int) map[string]interface{} {
	runtimeApply := configRuntimeApplyStatus(reload, runtimeWasRunning)
	runtimeApplied := runtimeApply == "applied"
	accepted := runtimeApply == "accepted"
	if accepted {
		status = "accepted"
	}
	operation := configMutationOperation(action, status, true, reload, runtimeApplied, runtimeApply, updated, "", "", nil)
	result := map[string]interface{}{
		"ok":               true,
		"status":           status,
		"reload":           reload,
		"config_saved":     true,
		"runtime_applied":  runtimeApplied,
		"runtime_apply":    runtimeApply,
		"accepted":         accepted,
		"operation_active": accepted,
		"operation":        operation,
	}
	if updated >= 0 {
		result["updated"] = updated
	}
	if d.runtimeV2 != nil {
		status := d.runtimeV2.Status()
		attachMutationGenerations(result, operation, status)
		result["runtimeStatus"] = status
	}
	return result
}

func configRuntimeApplyStatus(reload bool, runtimeWasRunning bool) string {
	switch {
	case reload && runtimeWasRunning:
		return "accepted"
	case reload:
		return "skipped_runtime_stopped"
	default:
		return "not_requested"
	}
}

func (d *daemon) configMutationErrorData(action string, err error, saved bool) map[string]interface{} {
	code := runtimeErrorCode(err, "CONFIG_APPLY_FAILED")
	runtimeApply := "not_started"
	status := "failed"
	if saved {
		runtimeApply = "failed"
		status = "saved_not_applied"
	}
	resetReport := resetReportFromRuntimeError(err)
	operation := configMutationOperation(action, status, saved, saved, false, runtimeApply, -1, code, err.Error(), resetReport)
	data := map[string]interface{}{
		"ok":              false,
		"status":          status,
		"config_saved":    saved,
		"runtime_applied": false,
		"message":         err.Error(),
		"code":            code,
		"runtime_apply":   runtimeApply,
		"operation":       operation,
	}
	if d.runtimeV2 != nil {
		status := d.runtimeV2.Status()
		attachMutationGenerations(data, operation, status)
		data["runtimeStatus"] = status
	}
	if resetReport != nil {
		data["resetReport"] = resetReport
	}
	return data
}

func attachMutationGenerations(result map[string]interface{}, operation map[string]interface{}, status runtimev2.Status) {
	appliedGeneration := status.AppliedState.Generation
	desiredGeneration := appliedGeneration
	if status.ActiveOperation != nil {
		desiredGeneration = status.ActiveOperation.Generation
	} else if result["config_saved"] == true || result["configSaved"] == true {
		desiredGeneration = appliedGeneration + 1
	}
	result["desiredGeneration"] = desiredGeneration
	result["appliedGeneration"] = appliedGeneration
	operation["desiredGeneration"] = desiredGeneration
	operation["appliedGeneration"] = appliedGeneration
}

func configMutationOperation(action string, status string, saved bool, reload bool, runtimeApplied bool, runtimeApply string, updated int, code string, message string, resetReport *runtimev2.ResetReport) map[string]interface{} {
	rollback := "not_needed"
	if resetReport != nil {
		rollback = "cleanup_incomplete"
		if resetReport.Status == "ok" {
			rollback = "cleanup_succeeded"
		}
	} else if saved && status == "failed" {
		rollback = "unknown"
	}
	stages := []map[string]interface{}{
		{
			"name":   "validate",
			"status": "ok",
		},
		{
			"name":   "render",
			"status": "ok",
		},
	}
	if status == "failed" && !saved && (code == runtimev2.BusyCodeRuntimeBusy || code == runtimev2.BusyCodeResetInProgress) {
		stages = append(stages, map[string]interface{}{
			"name":   "runtime-idle",
			"status": "failed",
		})
		stages = append(stages, map[string]interface{}{
			"name":   "persist",
			"status": "not_started",
		})
	} else {
		stages = append(stages, map[string]interface{}{
			"name":   "persist",
			"status": stageStatus(saved, status == "failed"),
		})
	}
	if reload {
		stages = append(stages, map[string]interface{}{
			"name":   "runtime-apply",
			"status": runtimeApply,
		})
	} else {
		stages = append(stages, map[string]interface{}{
			"name":   "runtime-apply",
			"status": "not_requested",
		})
	}
	if resetReport != nil {
		stages = append(stages, map[string]interface{}{
			"name":   "cleanup",
			"status": resetReport.Status,
		})
	}
	operation := map[string]interface{}{
		"type":            "config-mutation",
		"action":          action,
		"status":          status,
		"configSaved":     saved,
		"runtimeApplied":  runtimeApplied,
		"runtimeApply":    runtimeApply,
		"accepted":        runtimeApply == "accepted",
		"operationActive": runtimeApply == "accepted",
		"rollback":        rollback,
		"stages":          stages,
	}
	if updated >= 0 {
		operation["updated"] = updated
	}
	if code != "" {
		operation["code"] = code
	}
	if message != "" {
		operation["message"] = message
	}
	return operation
}

func stageStatus(ok bool, failed bool) string {
	if ok {
		return "ok"
	}
	if failed {
		return "failed"
	}
	return "not_started"
}

func configMutationWasSaved(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "config saved") || strings.Contains(msg, "panel saved")
}

func (d *daemon) failIfRuntimeOperationActive() error {
	if d.runtimeV2 == nil {
		return nil
	}
	status := d.runtimeV2.Status()
	if status.ActiveOperation == nil {
		return nil
	}
	return runtimev2.NewRuntimeBusyError(*status.ActiveOperation)
}

func (d *daemon) applyConfig(newCfg *config.Config, reload bool) error {
	wasRunning := d.coreMgr.GetState() == core.StateRunning ||
		d.coreMgr.GetState() == core.StateDegraded

	if err := d.failIfRuntimeOperationActive(); err != nil {
		return err
	}

	d.mu.Lock()
	oldCfg := d.cfg
	d.mu.Unlock()
	needsFullRestart := runtimeReloadNeedsFullRestart(oldCfg, newCfg, d.dataDir)

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
	d.healthMon.SetConfig(healthInterval, healthThreshold, tproxyPort, dnsPort, routeMark, newCfg.Health.URL, newCfg.Health.DNSProbeDomains, newCfg.Health.DNSIsHardReadiness, healthTimeout)
	if d.netWatcher != nil {
		d.netWatcher.SetEnv(buildScriptEnv(newCfg, d.dataDir))
	}
	if err := d.syncRuntimeV2DesiredState(); err != nil {
		return fmt.Errorf("config saved: sync runtime desired state: %w", err)
	}

	if reload && wasRunning {
		if _, err := d.runtimeV2.RunOperation(runtimev2.OperationReload, runtimev2.PhaseStarting, func(generation int64) error {
			return d.reloadRuntimeAfterConfigChange(newCfg, "apply config", "config saved", generation, needsFullRestart)
		}); err != nil {
			return fmt.Errorf("config saved: %w", err)
		}
	}

	return nil
}

func (d *daemon) reloadRuntimeAfterConfigChange(cfg *config.Config, context string, savedLabel string, generation int64, fullRestart bool) error {
	if err := d.failIfResetInProgress(); err != nil {
		return err
	}

	report := core.NewRuntimeStageReport(context)
	d.setLastReloadReport(report)
	recordStage := func(name string, status string, code string, detail string, rollbackApplied bool) {
		report.AddStage(name, status, code, detail, rollbackApplied)
		d.setLastReloadReport(report)
	}
	failStage := func(name string, code string, err error, rollbackApplied bool) error {
		recordStage(name, "failed", code, err.Error(), rollbackApplied)
		return err
	}

	d.stopSubsystems()
	recordStage("stop-subsystems", "ok", "", "", false)
	if fullRestart {
		if err := d.restartRootBackendV2(generation); err != nil {
			if resetReport := resetReportFromRuntimeError(err); resetReport != nil {
				recordStage("reset-after-full-restart-failure", resetReport.Status, "", fmt.Sprintf("errors=%d leftovers=%d", len(resetReport.Errors), len(resetReport.Leftovers)), resetReport.Status != "ok")
			}
			err = failStage("full-restart", runtimeErrorCode(err, "RUNTIME_RESTART_FAILED"), err, resetReportFromRuntimeError(err) != nil)
			return fmt.Errorf("%s full restart failed; %s: %w", context, savedLabel, err)
		}
		recordStage("full-restart", "ok", "", d.coreMgr.LastRuntimeReport().Status, false)
		report.FinishOK()
		d.setLastReloadReport(report)
		return nil
	}
	profile := cfg.ResolveProfile()
	if err := d.coreMgr.HotSwap(profile); err != nil {
		resetReport := d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
		recordStage("reset-after-hot-swap-failure", resetReport.Status, "", fmt.Sprintf("errors=%d leftovers=%d", len(resetReport.Errors), len(resetReport.Leftovers)), resetReport.Status != "ok")
		err = failStage("hot-swap", runtimeErrorCode(err, "CORE_SPAWN_FAILED"), err, resetReport.Status != "ok")
		return runtimeErrorWithResetReport(
			fmt.Errorf("%s hot-swap failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	recordStage("hot-swap", "ok", "", d.coreMgr.LastRuntimeReport().Status, false)
	netReport, err := d.reapplyRuntimeRulesReport(cfg)
	if err != nil {
		resetReport := d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
		recordStage("reset-after-netstack-failure", resetReport.Status, "", fmt.Sprintf("errors=%d leftovers=%d", len(resetReport.Errors), len(resetReport.Leftovers)), resetReport.Status != "ok")
		err = failStage("netstack-reapply", runtimeErrorCode(err, "RULES_NOT_APPLIED"), err, resetReport.Status != "ok")
		return runtimeErrorWithResetReport(
			fmt.Errorf("%s rules failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	recordStage("netstack-reapply", "ok", "", fmt.Sprintf("steps=%d", len(netReport.Steps)), false)
	d.rescueMgr.Reset()
	recordStage("rescue-reset", "ok", "", "", false)
	d.startSubsystems()
	recordStage("start-subsystems", "ok", "", "", false)
	snapshot := d.runtimeV2.RefreshHealth()
	if !snapshot.Healthy() {
		resetReport := d.resetNetworkStateReport(generation, runtimev2.BackendRootTProxy)
		recordStage("reset-after-health-failure", resetReport.Status, "", fmt.Sprintf("errors=%d leftovers=%d", len(resetReport.Errors), len(resetReport.Leftovers)), resetReport.Status != "ok")
		err := fmt.Errorf("%s", firstNonEmpty(snapshot.LastError, "readiness gates failed"))
		err = failStage("health-refresh", firstNonEmpty(snapshot.LastCode, "READINESS_GATE_FAILED"), err, resetReport.Status != "ok")
		return runtimeErrorWithResetReport(
			fmt.Errorf("%s readiness gates failed; %s, runtime stopped for safety: %w", context, savedLabel, err),
			resetReport,
		)
	}
	recordStage("health-refresh", "ok", "", firstNonEmpty(snapshot.LastCode, "healthy"), false)
	report.FinishOK()
	d.setLastReloadReport(report)
	return nil
}

func (d *daemon) resetNetworkState(cfg *config.Config) []string {
	report := netstack.New(d.dataDir, buildScriptEnv(cfg, d.dataDir), core.ExecScript).Cleanup()
	errors := append([]string(nil), report.Errors...)

	_, _ = core.ExecCommand("killall", "-TERM", "sing-box")
	_, _ = core.ExecCommand("killall", "-KILL", "sing-box")
	d.rescueMgr.Reset()
	d.coreMgr.ResetState()
	d.healthMon.Clear()
	d.resetRuntimeMetrics()
	return errors
}

func (d *daemon) reapplyRuntimeRules(cfg *config.Config) error {
	_, err := d.reapplyRuntimeRulesReport(cfg)
	return err
}

func (d *daemon) reapplyRuntimeRulesReport(cfg *config.Config) (netstack.Report, error) {
	manager := netstack.New(d.dataDir, buildScriptEnv(cfg, d.dataDir), core.ExecScript)
	report := manager.Apply()
	if err := report.Err(); err != nil {
		return report, err
	}
	report = manager.Verify()
	if err := report.Err(); err != nil {
		return report, err
	}
	return report, nil
}

func (d *daemon) setLastReloadReport(report core.RuntimeStageReport) {
	d.reportMu.Lock()
	defer d.reportMu.Unlock()
	d.lastReloadReport = report
}

func (d *daemon) LastReloadReport() core.RuntimeStageReport {
	d.reportMu.Lock()
	defer d.reportMu.Unlock()
	return d.lastReloadReport
}

type runtimeCodeError interface {
	RuntimeCode() string
}

func runtimeErrorCode(err error, fallback string) string {
	var coded runtimeCodeError
	if errors.As(err, &coded) {
		if code := strings.TrimSpace(coded.RuntimeCode()); code != "" {
			return code
		}
	}
	var busy *runtimev2.OperationBusyError
	if errors.As(err, &busy) && strings.TrimSpace(busy.Code) != "" {
		return busy.Code
	}
	var netErr *netstack.Error
	if errors.As(err, &netErr) && strings.TrimSpace(netErr.Code) != "" {
		return netErr.Code
	}
	return fallback
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
	profileInbounds := cfg.ResolveProfileInbounds()
	appRouting := core.BuildRuntimeAppRoutingEnv(
		cfg.Apps.Mode,
		cfg.Apps.Packages,
		cfg.Routing.AlwaysDirectApps,
		cfg.Routing.Mode,
	)
	chainProxyPorts, chainProxyUIDs := core.BuildChainedProxyProtectionEnv(cfg)

	return map[string]string{
		"PRIVSTACK_DIR":     dataDir,
		"CORE_GID":          strconv.Itoa(gid),
		"TPROXY_PORT":       strconv.Itoa(tproxyPort),
		"DNS_PORT":          strconv.Itoa(dnsPort),
		"API_PORT":          strconv.Itoa(apiPort),
		"SOCKS_PORT":        strconv.Itoa(profileInbounds.SocksPort),
		"HTTP_PORT":         strconv.Itoa(profileInbounds.HTTPPort),
		"CHAIN_PROXY_PORTS": chainProxyPorts,
		"CHAIN_PROXY_UIDS":  chainProxyUIDs,
		"FWMARK":            fmt.Sprintf("0x%x", mark),
		"ROUTE_TABLE":       "2023",
		"ROUTE_TABLE_V6":    "2024",
		"APP_MODE":          appRouting.AppMode,
		"PROXY_UIDS":        appRouting.ProxyUIDs,
		"DIRECT_UIDS":       appRouting.DirectUIDs,
		"BYPASS_UIDS":       appRouting.BypassUIDs,
		"DNS_SCOPE":         appRouting.DNSScope,
		"DNS_MODE":          appRouting.DNSMode,
		"PROXY_MODE":        "tproxy",
		"SHARING_MODE":      cfg.SharingModeEnv(),
		"SHARING_IFACES":    cfg.SharingInterfacesEnv(),
	}
}

func runtimeReloadNeedsFullRestart(oldCfg *config.Config, newCfg *config.Config, dataDir string) bool {
	if oldCfg == nil || newCfg == nil {
		return true
	}
	oldEnv := buildScriptEnv(oldCfg, dataDir)
	newEnv := buildScriptEnv(newCfg, dataDir)
	for _, key := range []string{
		"CORE_GID",
		"TPROXY_PORT",
		"DNS_PORT",
		"API_PORT",
		"SOCKS_PORT",
		"HTTP_PORT",
		"CHAIN_PROXY_PORTS",
		"CHAIN_PROXY_UIDS",
		"FWMARK",
		"ROUTE_TABLE",
		"ROUTE_TABLE_V6",
		"APP_MODE",
		"PROXY_UIDS",
		"DIRECT_UIDS",
		"BYPASS_UIDS",
		"DNS_SCOPE",
		"DNS_MODE",
		"PROXY_MODE",
		"SHARING_MODE",
		"SHARING_IFACES",
	} {
		if oldEnv[key] != newEnv[key] {
			return true
		}
	}
	return false
}

func firstVisibleLocalProxyPort(cfg *config.Config) int {
	ports := []int{10808, 10809, 9090}
	if cfg != nil {
		profileInbounds := cfg.ResolveProfileInbounds()
		ports = append(ports, cfg.Proxy.APIPort, profileInbounds.SocksPort, profileInbounds.HTTPPort)
	}
	seen := map[int]bool{}
	for _, port := range ports {
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		if isTCPPortListening("127.0.0.1", port, 150*time.Millisecond) {
			return port
		}
	}
	return 0
}

func pathHasGroupOrWorldBits(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0077 != 0
}

func localPortProtectionPresent(cfg *config.Config) bool {
	profileInbounds := cfg.ResolveProfileInbounds()
	tproxyPort := cfg.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	dnsPort := cfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	specs := []struct {
		port     int
		protocol string
	}{
		{port: tproxyPort, protocol: "tcp"},
		{port: tproxyPort, protocol: "udp"},
		{port: dnsPort, protocol: "tcp"},
		{port: dnsPort, protocol: "udp"},
		{port: cfg.Proxy.APIPort, protocol: "tcp"},
		{port: profileInbounds.SocksPort, protocol: "tcp"},
		{port: profileInbounds.HTTPPort, protocol: "tcp"},
	}

	v4, err4 := core.ExecCommand("iptables", "-w", "100", "-t", "mangle", "-S", "PRIVSTACK_OUT")
	if err4 != nil {
		return false
	}
	if !portProtectionOutputContainsAll(v4, specs) {
		return false
	}

	if _, err := core.ExecCommand("ip6tables", "-w", "100", "-t", "mangle", "-L"); err != nil {
		return true
	}
	v6, err6 := core.ExecCommand("ip6tables", "-w", "100", "-t", "mangle", "-S", "PRIVSTACK_OUT")
	if err6 != nil {
		return false
	}
	return portProtectionOutputContainsAll(v6, specs)
}

func portProtectionOutputContainsAll(output string, specs []struct {
	port     int
	protocol string
}) bool {
	for _, spec := range specs {
		if spec.port <= 0 {
			continue
		}
		if !portProtectionOutputContains(output, spec.protocol, spec.port) {
			return false
		}
	}
	return true
}

func portProtectionOutputContains(output string, protocol string, port int) bool {
	portText := fmt.Sprintf("--dport %d", port)
	protocolText := "-p " + protocol
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, portText) &&
			strings.Contains(line, protocolText) &&
			strings.Contains(line, "--uid-owner 0") &&
			strings.Contains(line, "--gid-owner") &&
			strings.Contains(line, "-j DROP") {
			return true
		}
	}
	return false
}

type subscriptionHTTPResult struct {
	Status  int
	Body    string
	Headers map[string]string
}

func fetchSubscriptionURL(rawURL string) (subscriptionHTTPResult, error) {
	var result subscriptionHTTPResult
	if rawURL == "" {
		return result, fmt.Errorf("url is required")
	}
	if err := validateSubscriptionFetchURL(rawURL); err != nil {
		return result, err
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return result, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "RKNnoVPN-subscription/2.0")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = subscriptionFetchDialContext
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("subscription fetch failed: %w", err)
	}
	defer resp.Body.Close()

	const maxSubscriptionBody = 4 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBody+1))
	if err != nil {
		return result, fmt.Errorf("subscription read failed: %w", err)
	}
	if len(body) > maxSubscriptionBody {
		return result, fmt.Errorf("subscription response is too large")
	}

	headers := make(map[string]string, len(resp.Header))
	for key, values := range resp.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return subscriptionHTTPResult{Status: resp.StatusCode, Headers: headers}, fmt.Errorf("subscription fetch returned HTTP %d", resp.StatusCode)
	}

	return subscriptionHTTPResult{
		Status:  resp.StatusCode,
		Body:    string(body),
		Headers: headers,
	}, nil
}

func validateSubscriptionFetchURL(rawURL string) error {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("subscription URL scheme must be http or https")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("subscription URL host is required")
	}
	if isDisallowedSubscriptionHost(host) {
		return fmt.Errorf("subscription URL host is local or private")
	}
	return nil
}

func subscriptionFetchDialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host = address
		port = ""
	}
	if isDisallowedSubscriptionHost(host) {
		return nil, fmt.Errorf("subscription URL host is local or private")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, resolved := range ips {
		if isDisallowedSubscriptionIP(resolved.IP) {
			return nil, fmt.Errorf("subscription URL resolved to local or private address")
		}
	}
	var dialer net.Dialer
	var lastErr error
	for _, resolved := range ips {
		dialAddress := address
		if port != "" {
			dialAddress = net.JoinHostPort(resolved.IP.String(), port)
		}
		conn, err := dialer.DialContext(ctx, network, dialAddress)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("subscription URL host did not resolve")
}

func isDisallowedSubscriptionHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isDisallowedSubscriptionIP(ip)
	}
	return false
}

func isDisallowedSubscriptionIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast()
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
	requestedFiles := []string{"privd"}
	if params != nil {
		var p struct {
			Lines int      `json:"lines"`
			Files []string `json:"files"`
		}
		if err := json.Unmarshal(*params, &p); err == nil {
			if p.Lines > 0 {
				n = p.Lines
			}
			if len(p.Files) > 0 {
				requestedFiles = p.Files
			}
		}
	}
	if n > 500 {
		n = 500
	}

	type logSection struct {
		Name    string   `json:"name"`
		Path    string   `json:"path"`
		Lines   []string `json:"lines"`
		Missing bool     `json:"missing,omitempty"`
		Error   string   `json:"error,omitempty"`
	}

	sections := make([]logSection, 0, len(requestedFiles))
	combined := make([]string, 0, len(requestedFiles)*n)
	for _, spec := range d.resolveLogFileSpecs(requestedFiles) {
		section := logSection{
			Name: spec.Name,
			Path: spec.Path,
		}
		lines, err := readLogTail(spec.Path, n, 512*1024)
		switch {
		case err == nil:
			section.Lines = lines
		case os.IsNotExist(err):
			section.Missing = true
		default:
			section.Error = err.Error()
		}
		sections = append(sections, section)

		combined = append(combined, "== "+section.Path+" ==")
		if section.Missing {
			combined = append(combined, "(missing)")
			continue
		}
		if section.Error != "" {
			combined = append(combined, "(error: "+section.Error+")")
			continue
		}
		combined = append(combined, section.Lines...)
	}

	return map[string]interface{}{
		"lines": combined,
		"logs":  sections,
	}, nil
}

type logFileSpec struct {
	Name string
	Path string
}

func (d *daemon) resolveLogFileSpecs(requested []string) []logFileSpec {
	seen := make(map[string]bool)
	specs := make([]logFileSpec, 0, len(requested))
	for _, raw := range requested {
		key := strings.ToLower(strings.TrimSpace(raw))
		var spec logFileSpec
		switch key {
		case "privd", "daemon":
			spec = logFileSpec{Name: "privd", Path: filepath.Join(d.dataDir, "logs", "privd.log")}
		case "sing-box", "singbox":
			spec = logFileSpec{Name: "sing-box", Path: filepath.Join(d.dataDir, "logs", "sing-box.log")}
		default:
			continue
		}
		if !seen[spec.Name] {
			seen[spec.Name] = true
			specs = append(specs, spec)
		}
	}
	if len(specs) == 0 {
		specs = append(specs, logFileSpec{Name: "privd", Path: filepath.Join(d.dataDir, "logs", "privd.log")})
	}
	return specs
}

func readLogTail(path string, maxLines int, maxBytes int64) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if stat.Size() > maxBytes {
		offset = stat.Size() - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	text := string(data)
	lines := splitLines(text)
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}

func (d *daemon) handleVersion(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	singBoxPath := filepath.Join(d.dataDir, "bin", "sing-box")
	return map[string]interface{}{
		"daemon":                   Version,
		"core":                     Version,
		"privctl":                  Version,
		"module":                   readModuleVersion(),
		"current_release":          doctorReleaseIntegrityReport(d.dataDir),
		"sing_box":                 d.singBoxVersion(singBoxPath, 20),
		"control_protocol":         controlProtocolVersion,
		"control_protocol_version": controlProtocolVersion,
		"schema_version":           config.CurrentSchemaVersion,
		"panel_min_version":        Version,
		"capabilities":             supportedCapabilities(),
		"supported_methods":        supportedRPCMethods(),
	}, nil
}

// --------------------------------------------------------------------------
// Update handlers
// --------------------------------------------------------------------------

func (d *daemon) handleUpdateCheck(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	info, err := updater.CheckForUpdate(updater.NormalizeVersionTag(Version))
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
	info, err := updater.CheckForUpdate(updater.NormalizeVersionTag(Version))
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
	expectedModulePath := filepath.Join(updateDir, "module.zip")
	expectedApkPath := filepath.Join(updateDir, "panel.apk")
	if p.ModulePath == "" {
		p.ModulePath = expectedModulePath
	}
	if p.ApkPath == "" {
		p.ApkPath = expectedApkPath
	}
	p.ModulePath = filepath.Clean(p.ModulePath)
	p.ApkPath = filepath.Clean(p.ApkPath)
	if p.ModulePath != filepath.Clean(expectedModulePath) || p.ApkPath != filepath.Clean(expectedApkPath) {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "update-install only accepts verified artifacts from the canonical update directory",
		}
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
	if moduleExists != apkExists {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "this update requires both module and APK artifacts",
		}
	}
	if err := updater.VerifyDownloadedUpdate(p.ModulePath, p.ApkPath); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "update artifacts are not checksum-verified: " + err.Error(),
		}
	}
	verifiedManifest, err := updater.ReadVerifiedUpdateManifest(updateDir)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "update artifacts are not manifest-verified: " + err.Error(),
		}
	}
	modulePreflight, err := updater.PreflightModuleUpdate(p.ModulePath, d.dataDir)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "module update preflight failed: " + err.Error(),
		}
	}
	if modulePreflight.Version != updater.NormalizeVersionTag(verifiedManifest.LatestVersion) {
		return nil, &ipc.RPCError{
			Code: ipc.CodeInvalidParams,
			Message: fmt.Sprintf(
				"module version %s does not match verified update version %s",
				modulePreflight.Version,
				updater.NormalizeVersionTag(verifiedManifest.LatestVersion),
			),
		}
	}

	wasRunning := d.coreMgr.GetState() == core.StateRunning ||
		d.coreMgr.GetState() == core.StateDegraded

	status, err := d.runtimeV2.RunOperation(runtimev2.OperationUpdateInstall, runtimev2.PhaseStopping, func(generation int64) error {
		installTracker := updater.NewInstallTracker(d.dataDir, generation, p.ModulePath, p.ApkPath)
		if err := installTracker.Begin(); err != nil {
			return fmt.Errorf("record update install state: %w", err)
		}
		markStep := func(name, status, code, detail string) {
			d.runtimeV2.SetActiveOperationStep(generation, name, status, code, detail)
			if err := installTracker.Step(name, status, code, detail); err != nil {
				log.Printf("[updater] warning: record install step %s/%s: %v", name, status, err)
			}
		}
		moduleUpdated := false
		defer func() {
			if moduleUpdated {
				markStep("update-schedule-self-exit", "ok", "", "daemon restart scheduled")
				go updater.ScheduleSelfExit(updater.SelfExitDelay)
			}
		}()
		// Install the APK before replacing module binaries. If the APK is
		// invalid or incompatible, we must fail before the module install can
		// fork a new daemon and schedule the old daemon's self-exit.
		if apkExists {
			markStep("update-install-apk", "running", "APK_INSTALLING", filepath.Base(p.ApkPath))
			if err := updater.InstallApkUpdate(p.ApkPath); err != nil {
				log.Printf("[updater] APK install failed: %v", err)
				markStep("update-install-apk", "failed", "APK_INSTALL_FAILED", err.Error())
				return fmt.Errorf("apk install failed: %w", err)
			}
			if err := installTracker.MarkAPKInstalled(); err != nil {
				log.Printf("[updater] warning: record APK install success: %v", err)
			}
			markStep("update-install-apk", "ok", "", "")
		}

		if moduleExists {
			markStep("update-stop-runtime", "running", "UPDATE_STOP_RUNTIME", "stopping runtime before module install")
			if err := d.failIfResetInProgress(); err != nil {
				markStep("update-stop-runtime", "failed", runtimeErrorCode(err, "RESET_IN_PROGRESS"), err.Error())
				return err
			}
			// Stop subsystems before module update only when we are replacing the
			// daemon/module itself. APK-only installs should not disrupt traffic.
			d.stopSubsystems()
			if err := d.coreMgr.Stop(); err != nil {
				log.Printf("[updater] warning: failed to stop core: %v", err)
			}
			markStep("update-stop-runtime", "ok", "", "")
		}

		// Install module (binaries + scripts + module files).
		if moduleExists {
			markStep("update-install-module", "running", "MODULE_INSTALLING", filepath.Base(p.ModulePath))
			moduleDir := "/data/adb/modules/privstack"
			if err := updater.InstallModuleUpdate(p.ModulePath, d.dataDir, moduleDir); err != nil {
				if wasRunning {
					d.restoreCurrentRuntimeAfterFailedUpdate()
				}
				markStep("update-install-module", "failed", "MODULE_INSTALL_FAILED", err.Error())
				return fmt.Errorf("module install failed: %w", err)
			}
			moduleUpdated = true
			if err := installTracker.MarkModuleInstalled(); err != nil {
				log.Printf("[updater] warning: record module install success: %v", err)
			}
			markStep("update-install-module", "ok", "", "")
		}

		// Clean up downloaded files.
		markStep("update-cleanup-downloads", "running", "UPDATE_CLEANUP", updateDir)
		if err := os.RemoveAll(updateDir); err != nil {
			markStep("update-cleanup-downloads", "failed", "UPDATE_CLEANUP_FAILED", err.Error())
			return fmt.Errorf("update cleanup failed: %w", err)
		}
		markStep("update-cleanup-downloads", "ok", "", "")
		if err := installTracker.Complete(); err != nil {
			log.Printf("[updater] warning: record completed update install state: %v", err)
		}

		return nil
	})
	if err != nil {
		return nil, d.rpcErrorFromRuntimeError(err)
	}

	return status, nil
}

func (d *daemon) restoreCurrentRuntimeAfterFailedUpdate() {
	if err := d.failIfResetInProgress(); err != nil {
		log.Printf("[updater] skipping runtime restore while reset is active: %v", err)
		return
	}

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
