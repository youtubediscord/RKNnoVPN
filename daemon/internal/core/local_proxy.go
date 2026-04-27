package core

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

var procNetTCPFiles = []string{"/proc/net/tcp", "/proc/net/tcp6"}

// BuildChainedProxyProtectionEnv returns ports of local proxy outbounds and
// the listener-owner UIDs that may mutually reach those ports.
func BuildChainedProxyProtectionEnv(cfg *config.Config) (string, string) {
	ports := localOutboundProxyPorts(cfg)
	if len(ports) == 0 {
		return "", ""
	}
	owners := tcpListenerOwnersByPort(ports)
	protectedPorts := make([]int, 0, len(ports))
	uidSet := map[int]bool{}
	for _, port := range ports {
		portOwners := owners[port]
		if len(portOwners) == 0 {
			continue
		}
		protectedPorts = append(protectedPorts, port)
		for _, uid := range portOwners {
			if uid >= 0 {
				uidSet[uid] = true
			}
		}
	}
	if len(protectedPorts) == 0 || len(uidSet) == 0 {
		return "", ""
	}
	uids := make([]int, 0, len(uidSet))
	for uid := range uidSet {
		uids = append(uids, uid)
	}
	sort.Ints(protectedPorts)
	sort.Ints(uids)
	return joinInts(protectedPorts), joinInts(uids)
}

func localOutboundProxyPorts(cfg *config.Config) []int {
	if cfg == nil {
		return nil
	}
	profiles := config.ProfilesFromConfigNodes(cfg)
	if len(profiles) == 0 {
		if profile := cfg.ResolveProfile(); profile != nil {
			profiles = []*config.NodeProfile{profile}
		}
	}

	reserved := reservedCorePorts(cfg)
	seen := map[int]bool{}
	var ports []int
	for _, profile := range profiles {
		if profile == nil || profile.Port <= 0 || !isLocalEndpoint(profile.Address) {
			continue
		}
		if reserved[profile.Port] || seen[profile.Port] {
			continue
		}
		seen[profile.Port] = true
		ports = append(ports, profile.Port)
	}
	sort.Ints(ports)
	return ports
}

func reservedCorePorts(cfg *config.Config) map[int]bool {
	ports := map[int]bool{}
	if cfg == nil {
		return ports
	}
	tproxyPort := cfg.Proxy.TProxyPort
	if tproxyPort == 0 {
		tproxyPort = 10853
	}
	dnsPort := cfg.Proxy.DNSPort
	if dnsPort == 0 {
		dnsPort = 10856
	}
	for _, port := range []int{tproxyPort, dnsPort, cfg.Proxy.APIPort} {
		if port > 0 {
			ports[port] = true
		}
	}
	profileInbounds := cfg.ResolveProfileInbounds()
	for _, port := range []int{profileInbounds.SocksPort, profileInbounds.HTTPPort} {
		if port > 0 {
			ports[port] = true
		}
	}
	return ports
}

func isLocalEndpoint(address string) bool {
	host := strings.ToLower(strings.TrimSpace(address))
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	switch host {
	case "localhost", "ip6-localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func tcpListenerOwnersByPort(ports []int) map[int][]int {
	wanted := map[int]bool{}
	for _, port := range ports {
		if port > 0 {
			wanted[port] = true
		}
	}
	owners := map[int]map[int]bool{}
	for _, path := range procNetTCPFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for port, uid := range parseProcNetTCPListeners(string(data), wanted) {
			if owners[port] == nil {
				owners[port] = map[int]bool{}
			}
			owners[port][uid] = true
		}
	}

	result := map[int][]int{}
	for port, set := range owners {
		uids := make([]int, 0, len(set))
		for uid := range set {
			uids = append(uids, uid)
		}
		sort.Ints(uids)
		result[port] = uids
	}
	return result
}

func parseProcNetTCPListeners(raw string, wanted map[int]bool) map[int]int {
	result := map[int]int{}
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[0] == "sl" || fields[3] != "0A" {
			continue
		}
		port, err := parseProcNetPort(fields[1])
		if err != nil || !wanted[port] {
			continue
		}
		uid, err := strconv.Atoi(fields[7])
		if err != nil {
			continue
		}
		result[port] = uid
	}
	return result
}

func parseProcNetPort(localAddress string) (int, error) {
	parts := strings.Split(localAddress, ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("missing port in %q", localAddress)
	}
	port64, err := strconv.ParseInt(parts[len(parts)-1], 16, 32)
	if err != nil {
		return 0, err
	}
	return int(port64), nil
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, " ")
}
