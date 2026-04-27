package diagnostics

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
)

func TestEffectiveLocalPortsDeduplicatesConfiguredPorts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.TProxyPort = 11000
	cfg.Proxy.DNSPort = 11001
	cfg.Proxy.APIPort = 11001
	cfg.Profile.Inbounds = json.RawMessage(`{"socksPort":11002,"httpPort":11002}`)

	got := EffectiveLocalPorts(cfg)
	want := []int{11000, 11001, 11002}
	if len(got) != len(want) {
		t.Fatalf("unexpected port count: got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected ports: got %#v want %#v", got, want)
		}
	}
}

func TestLocalPortConflictsDetectDuplicateRoles(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.TProxyPort = 12000
	cfg.Proxy.DNSPort = 12000

	conflicts := LocalPortConflicts(cfg)
	if len(conflicts) != 1 || conflicts[0].Port != 12000 {
		t.Fatalf("expected one duplicate-port conflict, got %#v", conflicts)
	}
	if len(conflicts[0].Roles) != 2 || conflicts[0].Roles[0] != "dns" || conflicts[0].Roles[1] != "tproxy" {
		t.Fatalf("unexpected conflict roles: %#v", conflicts)
	}
}

func TestLocalhostProxyPortsClearChecksConfiguredPorts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	cfg := config.DefaultConfig()
	cfg.Proxy.APIPort = port

	if LocalhostProxyPortsClear(cfg) {
		t.Fatalf("configured listening localhost port %d should not be clear", port)
	}
}

func TestPortStatusesExposeRolesAndConflicts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Proxy.TProxyPort = 13000
	cfg.Proxy.DNSPort = 13000

	statuses := PortStatuses(cfg)
	for _, status := range statuses {
		if status.Port == 13000 {
			if status.Role != "dns,tproxy" || !status.Conflict {
				t.Fatalf("duplicate port should expose sorted roles and conflict flag: %#v", status)
			}
			return
		}
	}
	t.Fatalf("port status for duplicate port missing: %#v", statuses)
}
