package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/diagnostics"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
)

type Finding struct {
	Code             string `json:"code"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Severity         string `json:"severity"`
	Category         string `json:"category"`
	Recommendation   string `json:"recommendation"`
	AffectedResource string `json:"affectedResource,omitempty"`
}

type Report struct {
	AuditID   string         `json:"auditId"`
	Timestamp int64          `json:"timestamp"`
	Score     int            `json:"score"`
	Findings  []Finding      `json:"findings"`
	Summary   map[string]int `json:"summary"`
}

func BuildReport(cfg *config.Config, cfgPath string, dataDir string, healthResult *health.HealthResult, coreState string, now time.Time) Report {
	if now.IsZero() {
		now = time.Now()
	}

	findings := make([]Finding, 0)
	appendFinding := func(
		code string,
		title string,
		description string,
		severity string,
		category string,
		recommendation string,
		affected string,
	) {
		findings = append(findings, Finding{
			Code:             code,
			Title:            title,
			Description:      description,
			Severity:         severity,
			Category:         category,
			Recommendation:   recommendation,
			AffectedResource: affected,
		})
	}

	if cfg == nil {
		appendFinding(
			"CONFIG_UNAVAILABLE",
			"Конфигурация недоступна",
			"Демон не смог предоставить активную конфигурацию для проверки.",
			"CRITICAL",
			"CONFIG",
			"Восстановите config.json и повторите аудит.",
			"config.json",
		)
		return buildReport(findings, now)
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
			"profile.inbounds",
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
			"profile.inbounds",
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
		if line := diagnostics.FirstVPNLikeInterfaceLine(splitLines(linkOut)); line != "" {
			appendFinding(
				"VPN_LIKE_INTERFACE_PRESENT",
				"Обнаружен VPN-похожий интерфейс",
				"Интерфейсы tun/wg/tap/ppp/ipsec являются прямым детектируемым признаком VPN-подобного стека.",
				"HIGH",
				"LEAK",
				"Не запускайте TUN/WireGuard-интерфейсы вместе с RKNnoVPN; outbound должен жить внутри core.",
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
		if line := diagnostics.FirstLoopbackDNSLine(splitLines(connectivityOut)); line != "" {
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
		cfgPath,
		filepath.Join(dataDir, "config", "rendered", "singbox.json"),
		filepath.Join(dataDir, "logs", "daemon.log"),
		filepath.Join(dataDir, "logs", "sing-box.log"),
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

	if coreState == core.StateRunning.String() || coreState == core.StateDegraded.String() {
		if !localPortProtectionPresent(cfg) {
			appendFinding(
				"LOCAL_PORT_PROTECTION_MISSING",
				"Локальные порты RKNnoVPN защищены не полностью",
				"Обычные приложения могут получить доступ к TPROXY-, DNS-, API-, SOCKS- или HTTP-helper портам.",
				"HIGH",
				"LEAK",
				"Повторно примените правила iptables и проверьте DROP-правила для TPROXY, DNS, API, SOCKS и HTTP-helper портов.",
				"iptables mangle RKNNOVPN_OUT",
			)
		}
	}

	appendHealthFindings(healthResult, appendFinding)
	return buildReport(findings, now)
}

func appendHealthFindings(
	healthResult *health.HealthResult,
	appendFinding func(code string, title string, description string, severity string, category string, recommendation string, affected string),
) {
	if healthResult == nil {
		return
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
}

func buildReport(findings []Finding, now time.Time) Report {
	summary := map[string]int{
		"critical": 0,
		"high":     0,
		"medium":   0,
		"low":      0,
		"info":     0,
	}
	score := 100
	for _, finding := range findings {
		switch finding.Severity {
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

	return Report{
		AuditID:   fmt.Sprintf("audit-%d", now.UnixMilli()),
		Timestamp: now.UnixMilli(),
		Score:     score,
		Findings:  findings,
		Summary:   summary,
	}
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
		if diagnostics.TCPPortListening("127.0.0.1", port, 150*time.Millisecond) {
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
	specs := []portProtectionSpec{
		{port: tproxyPort, protocol: "tcp"},
		{port: tproxyPort, protocol: "udp"},
		{port: dnsPort, protocol: "tcp"},
		{port: dnsPort, protocol: "udp"},
		{port: cfg.Proxy.APIPort, protocol: "tcp"},
		{port: profileInbounds.SocksPort, protocol: "tcp"},
		{port: profileInbounds.HTTPPort, protocol: "tcp"},
	}

	v4, err4 := core.ExecCommand("iptables", "-w", "100", "-t", "mangle", "-S", "RKNNOVPN_OUT")
	if err4 != nil {
		return false
	}
	if !portProtectionOutputContainsAll(v4, specs) {
		return false
	}

	if _, err := core.ExecCommand("ip6tables", "-w", "100", "-t", "mangle", "-L"); err != nil {
		return true
	}
	v6, err6 := core.ExecCommand("ip6tables", "-w", "100", "-t", "mangle", "-S", "RKNNOVPN_OUT")
	if err6 != nil {
		return false
	}
	return portProtectionOutputContainsAll(v6, specs)
}

type portProtectionSpec struct {
	port     int
	protocol string
}

func portProtectionOutputContainsAll(output string, specs []portProtectionSpec) bool {
	for _, spec := range specs {
		if spec.port <= 0 {
			continue
		}
		if !PortProtectionOutputContains(output, spec.protocol, spec.port) {
			return false
		}
	}
	return true
}

func PortProtectionOutputContains(output string, protocol string, port int) bool {
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
