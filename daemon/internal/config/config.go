package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the canonical daemon configuration.
type Config struct {
	Proxy     ProxyConfig     `json:"proxy"`
	Transport TransportConfig `json:"transport"`
	Node      NodeConfig      `json:"node"`
	Routing   RoutingConfig   `json:"routing"`
	Apps      AppsConfig      `json:"apps"`
	DNS       DNSConfig       `json:"dns"`
	IPv6      IPv6Config      `json:"ipv6"`
	Health    HealthConfig    `json:"health"`
	Rescue    RescueConfig    `json:"rescue"`
	Autostart bool            `json:"autostart"`
}

// ProxyConfig controls the sing-box proxy listener ports.
type ProxyConfig struct {
	TProxyPort int `json:"tproxy_port"`
	DNSPort    int `json:"dns_port"`
	APIPort    int `json:"api_port"`
}

// TransportConfig controls the outbound protocol transport layer.
type TransportConfig struct {
	Protocol   string            `json:"protocol"`    // "reality", "ws", "grpc", "tcp", "h2"
	TLSServer  string            `json:"tls_server"`  // SNI for TLS
	Fingerprint string           `json:"fingerprint"` // uTLS fingerprint
	Extra      map[string]string `json:"extra"`       // protocol-specific fields
}

// NodeConfig describes the remote proxy server.
type NodeConfig struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"` // "vless", "trojan", "vmess", "shadowsocks"
	UUID     string `json:"uuid"`     // also used as password for trojan/ss
	Flow     string `json:"flow"`     // e.g. "xtls-rprx-vision"

	// Shadowsocks-specific fields.
	SSMethod string `json:"ss_method,omitempty"`

	// VMess-specific fields.
	AlterID int    `json:"alter_id,omitempty"`
	Security string `json:"security,omitempty"` // vmess encryption

	// REALITY-specific fields.
	RealityPublicKey string `json:"reality_public_key,omitempty"`
	RealityShortID   string `json:"reality_short_id,omitempty"`
}

// RoutingConfig controls traffic routing rules.
type RoutingConfig struct {
	BypassLAN      bool     `json:"bypass_lan"`
	BypassRussia   bool     `json:"bypass_russia"`
	BlockAds       bool     `json:"block_ads"`
	CustomDirect   []string `json:"custom_direct"`   // domains/IPs to route directly
	CustomProxy    []string `json:"custom_proxy"`     // domains/IPs to force through proxy
	CustomBlock    []string `json:"custom_block"`     // domains/IPs to block
	GeoIPPath      string   `json:"geoip_path"`
	GeoSitePath    string   `json:"geosite_path"`
}

// AppsConfig controls per-app routing (Android split tunnel).
type AppsConfig struct {
	Mode     string   `json:"mode"`      // "all", "include", "exclude"
	Packages []string `json:"packages"`  // package names for include/exclude
}

// DNSConfig controls DNS resolution.
type DNSConfig struct {
	RemoteDoH   string `json:"remote_doh"`   // DoH URL routed via proxy
	DirectDoH   string `json:"direct_doh"`   // DoH URL for direct domains
	BootstrapIP string `json:"bootstrap_ip"` // IP-literal for bootstrapping DoH
	FakeIP      bool   `json:"fake_ip"`      // use fake-ip strategy
}

// IPv6Config controls IPv6 behavior.
type IPv6Config struct {
	Enable bool `json:"enable"`
}

// HealthConfig controls automatic health checking.
type HealthConfig struct {
	IntervalSec int    `json:"interval_sec"`
	URL         string `json:"url"`          // URL to probe
	TimeoutSec  int    `json:"timeout_sec"`
}

// RescueConfig controls automatic fallback on persistent failures.
type RescueConfig struct {
	Enable        bool `json:"enable"`
	MaxFailures   int  `json:"max_failures"`   // consecutive failures before rescue
	CooldownSec   int  `json:"cooldown_sec"`   // wait time before retrying after rescue
}

// NodeProfile is a resolved node profile ready for sing-box config rendering.
// It merges NodeConfig + TransportConfig into a flat structure.
type NodeProfile struct {
	Protocol        string `json:"protocol"`
	Address         string `json:"address"`
	Port            int    `json:"port"`
	UUID            string `json:"uuid"`
	Flow            string `json:"flow,omitempty"`
	Transport       string `json:"transport"`
	TLSServer       string `json:"tls_server"`
	Fingerprint     string `json:"fingerprint"`
	SSMethod        string `json:"ss_method,omitempty"`
	AlterID         int    `json:"alter_id,omitempty"`
	Security        string `json:"security,omitempty"`
	RealityPubKey   string `json:"reality_public_key,omitempty"`
	RealityShortID  string `json:"reality_short_id,omitempty"`
	Extra           map[string]string `json:"extra,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Proxy: ProxyConfig{
			TProxyPort: 10808,
			DNSPort:    10853,
			APIPort:    9090,
		},
		Transport: TransportConfig{
			Protocol:    "reality",
			Fingerprint: "chrome",
		},
		Node: NodeConfig{
			Protocol: "vless",
			Port:     443,
		},
		Routing: RoutingConfig{
			BypassLAN:    true,
			BypassRussia: true,
			GeoIPPath:    "/data/adb/privstack/data/geoip.db",
			GeoSitePath:  "/data/adb/privstack/data/geosite.db",
		},
		Apps: AppsConfig{
			Mode: "all",
		},
		DNS: DNSConfig{
			RemoteDoH:   "https://1.1.1.1/dns-query",
			DirectDoH:   "https://77.88.8.8/dns-query",
			BootstrapIP: "1.1.1.1",
			FakeIP:      false,
		},
		IPv6: IPv6Config{
			Enable: true,
		},
		Health: HealthConfig{
			IntervalSec: 30,
			URL:         "https://cp.cloudflare.com/generate_204",
			TimeoutSec:  5,
		},
		Rescue: RescueConfig{
			Enable:      true,
			MaxFailures: 3,
			CooldownSec: 60,
		},
		Autostart: true,
	}
}

// Load reads a Config from the given JSON file path.
// If the file does not exist, it returns DefaultConfig.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate: %w", err)
	}

	return cfg, nil
}

// Save writes the Config as formatted JSON to the given file path,
// creating parent directories as needed.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0640); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// Validate checks the Config for obvious misconfigurations.
func (c *Config) Validate() error {
	if c.Proxy.TProxyPort < 1 || c.Proxy.TProxyPort > 65535 {
		return fmt.Errorf("proxy.tproxy_port must be 1-65535, got %d", c.Proxy.TProxyPort)
	}
	if c.Proxy.DNSPort < 1 || c.Proxy.DNSPort > 65535 {
		return fmt.Errorf("proxy.dns_port must be 1-65535, got %d", c.Proxy.DNSPort)
	}
	if c.Proxy.APIPort < 1 || c.Proxy.APIPort > 65535 {
		return fmt.Errorf("proxy.api_port must be 1-65535, got %d", c.Proxy.APIPort)
	}

	validProto := map[string]bool{
		"vless": true, "trojan": true, "vmess": true, "shadowsocks": true,
	}
	if c.Node.Protocol != "" && !validProto[c.Node.Protocol] {
		return fmt.Errorf("node.protocol must be one of vless/trojan/vmess/shadowsocks, got %q", c.Node.Protocol)
	}

	validAppMode := map[string]bool{
		"all": true, "include": true, "exclude": true,
	}
	if !validAppMode[c.Apps.Mode] {
		return fmt.Errorf("apps.mode must be all/include/exclude, got %q", c.Apps.Mode)
	}

	if c.Health.IntervalSec < 0 {
		return fmt.Errorf("health.interval_sec must be >= 0, got %d", c.Health.IntervalSec)
	}
	if c.Health.TimeoutSec < 1 {
		return fmt.Errorf("health.timeout_sec must be >= 1, got %d", c.Health.TimeoutSec)
	}
	if c.Rescue.MaxFailures < 1 {
		return fmt.Errorf("rescue.max_failures must be >= 1, got %d", c.Rescue.MaxFailures)
	}

	return nil
}

// ResolveProfile merges Node + Transport into a flat NodeProfile
// suitable for rendering a sing-box outbound.
func (c *Config) ResolveProfile() *NodeProfile {
	return &NodeProfile{
		Protocol:       c.Node.Protocol,
		Address:        c.Node.Address,
		Port:           c.Node.Port,
		UUID:           c.Node.UUID,
		Flow:           c.Node.Flow,
		Transport:      c.Transport.Protocol,
		TLSServer:      c.Transport.TLSServer,
		Fingerprint:    c.Transport.Fingerprint,
		SSMethod:       c.Node.SSMethod,
		AlterID:        c.Node.AlterID,
		Security:       c.Node.Security,
		RealityPubKey:  c.Node.RealityPublicKey,
		RealityShortID: c.Node.RealityShortID,
		Extra:          c.Transport.Extra,
	}
}
