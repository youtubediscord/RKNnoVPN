package diagnostics

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

type PortStatus struct {
	Role         string `json:"role,omitempty"`
	Port         int    `json:"port"`
	TCPListening bool   `json:"tcpListening"`
	Conflict     bool   `json:"conflict,omitempty"`
}

type PortConflict struct {
	Port  int      `json:"port"`
	Roles []string `json:"roles"`
}

func localhostProxyPortsClear(cfg *config.Config) bool {
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
		if TCPPortListening("127.0.0.1", port, 150*time.Millisecond) {
			return false
		}
	}
	return true
}

func PortStatuses(cfg *config.Config) []PortStatus {
	ports := effectiveLocalPorts(cfg)
	roles := localPortRoles(cfg)
	result := make([]PortStatus, 0, len(ports))
	for _, port := range ports {
		role := strings.Join(roles[port], ",")
		result = append(result, PortStatus{
			Role:         role,
			Port:         port,
			TCPListening: TCPPortListening("127.0.0.1", port, 150*time.Millisecond),
			Conflict:     len(roles[port]) > 1,
		})
	}
	return result
}

func LocalPortConflicts(cfg *config.Config) []PortConflict {
	roles := localPortRoles(cfg)
	conflicts := make([]PortConflict, 0)
	ports := make([]int, 0, len(roles))
	for port := range roles {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	for _, port := range ports {
		if len(roles[port]) <= 1 {
			continue
		}
		conflicts = append(conflicts, PortConflict{
			Port:  port,
			Roles: append([]string(nil), roles[port]...),
		})
	}
	return conflicts
}

func localPortRoles(cfg *config.Config) map[int][]string {
	if cfg == nil {
		return nil
	}
	profileInbounds := cfg.ResolveProfileInbounds()
	candidates := []struct {
		role string
		port int
	}{
		{"tproxy", valueOrDefaultInt(cfg.Proxy.TProxyPort, 10853)},
		{"dns", valueOrDefaultInt(cfg.Proxy.DNSPort, 10856)},
		{"clash_api", cfg.Proxy.APIPort},
		{"socks_helper", profileInbounds.SocksPort},
		{"http_helper", profileInbounds.HTTPPort},
	}
	roles := map[int][]string{}
	for _, candidate := range candidates {
		if candidate.port <= 0 {
			continue
		}
		roles[candidate.port] = append(roles[candidate.port], candidate.role)
	}
	for port := range roles {
		sort.Strings(roles[port])
	}
	return roles
}

func effectiveLocalPorts(cfg *config.Config) []int {
	if cfg == nil {
		return nil
	}
	profileInbounds := cfg.ResolveProfileInbounds()
	ports := []int{
		valueOrDefaultInt(cfg.Proxy.TProxyPort, 10853),
		valueOrDefaultInt(cfg.Proxy.DNSPort, 10856),
		cfg.Proxy.APIPort,
		profileInbounds.SocksPort,
		profileInbounds.HTTPPort,
	}
	seen := make(map[int]bool, len(ports))
	result := make([]int, 0, len(ports))
	for _, port := range ports {
		if port <= 0 || seen[port] {
			continue
		}
		seen[port] = true
		result = append(result, port)
	}
	return result
}

func valueOrDefaultInt(value int, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func TCPPortListening(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
