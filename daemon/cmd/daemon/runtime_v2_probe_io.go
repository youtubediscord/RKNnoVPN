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
