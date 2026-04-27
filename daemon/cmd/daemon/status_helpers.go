package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
)

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
	req.Header.Set("User-Agent", "RKNnoVPN/health")

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
