package main

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func (d *daemon) probeNodeBootstrapDNS(cfg *config.Config, host string, timeout time.Duration) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	bootstrapIP := strings.TrimSpace(cfg.DNS.BootstrapIP)
	if bootstrapIP == "" {
		return false
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: timeout}
			return dialer.DialContext(ctx, "udp", net.JoinHostPort(bootstrapIP, "53"))
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	addrs, err := resolver.LookupHost(ctx, host)
	return err == nil && len(addrs) > 0
}
