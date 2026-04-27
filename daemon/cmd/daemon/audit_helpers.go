package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
)

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
