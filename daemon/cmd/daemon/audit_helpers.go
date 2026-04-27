package main

import (
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/audit"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func firstVisibleLocalProxyPort(cfg *config.Config) int {
	return audit.FirstVisibleLocalProxyPort(cfg)
}

func pathHasGroupOrWorldBits(path string) bool {
	return audit.PathHasGroupOrWorldBits(path)
}

func localPortProtectionPresent(cfg *config.Config) bool {
	return audit.LocalPortProtectionPresent(cfg)
}

func portProtectionOutputContains(output string, protocol string, port int) bool {
	return audit.PortProtectionOutputContains(output, protocol, port)
}
